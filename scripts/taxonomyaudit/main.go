// Command taxonomyaudit reports what the collected data says about the
// classification taxonomy, so the Phase 0 calibration docs/05 asks for can be
// done from real postings instead of guesses.
//
// Phase 0 was written as "sample 2,000 IT postings before any production code",
// which assumed a one-off scraping script. After the first ingest that sample
// is simply what is already in jobs.db, so this reads the database instead of
// the network — the reason deferring Phase 0 past deployment made it cheaper
// rather than riskier (docs/05 Backlog §1).
//
// Read-only. Run it against a copy if the pipeline is live:
//
//	go run ./scripts/taxonomyaudit --data-dir ./data
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/meirongdev/jobs-sg/internal/store"
)

func main() {
	dataDir := flag.String("data-dir", "/data", "directory holding jobs.db")
	top := flag.Int("top", 40, "how many rows per ranked section")
	flag.Parse()

	if err := run(*dataDir, *top); err != nil {
		fmt.Fprintln(os.Stderr, "taxonomyaudit:", err)
		os.Exit(1)
	}
}

func run(dataDir string, top int) error {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(dataDir, "jobs.db"), true)
	if err != nil {
		return err
	}
	defer db.Close()

	ssoc, err := db.LoadSSOCMap(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()

	var total int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM job`).Scan(&total); err != nil {
		return err
	}
	fmt.Fprintf(w, "jobs.db holds %d candidate postings; ssoc_taxonomy has %d codes\n\n", total, len(ssoc))

	if err := ssocSection(ctx, db, w, ssoc, top); err != nil {
		return err
	}
	if err := distributions(ctx, db, w, top); err != nil {
		return err
	}
	return unmappedTech(ctx, db, w, top)
}

// ssocSection is the actionable half: which occupation codes carry postings,
// which of them the taxonomy does not name, and what those postings are being
// classified as in the meantime.
//
// An unmapped code beginning 251 falls back to Backend and an unmapped code
// anywhere else falls back to Other-IT (internal/classify). Neither is a
// measurement, so the postings behind those rows are the ones a calibration
// pass buys back — ranked, so the first few edits do most of the work.
func ssocSection(ctx context.Context, db *store.DB, w *tabwriter.Writer, ssoc map[string]string, top int) error {
	rows, err := db.QueryContext(ctx, `
		SELECT coalesce(ssoc_code,''), coalesce(role_family,''), count(*)
		FROM job GROUP BY ssoc_code, role_family ORDER BY count(*) DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type row struct {
		code, family string
		n            int
		mapped       bool
	}
	var all []row
	fallback := map[string]int{}
	var mappedN, unmappedN int
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.code, &r.family, &r.n); err != nil {
			return err
		}
		_, r.mapped = ssoc[r.code]
		if r.mapped {
			mappedN += r.n
		} else {
			unmappedN += r.n
			fallback[r.family] += r.n
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	fmt.Fprintf(w, "SSOC COVERAGE\n")
	fmt.Fprintf(w, "  explicitly mapped\t%d postings\n", mappedN)
	fmt.Fprintf(w, "  falling back\t%d postings\n", unmappedN)
	for fam, n := range fallback {
		fmt.Fprintf(w, "    -> %s\t%d\n", fam, n)
	}
	fmt.Fprintf(w, "\nUNMAPPED CODES BY VOLUME  (add these to ssoc_taxonomy, biggest first)\n")
	fmt.Fprintf(w, "  ssoc\tcurrently classified as\tpostings\n")
	shown := 0
	for _, r := range all {
		if r.mapped || shown >= top {
			continue
		}
		label := r.family
		if label == "" {
			label = "(none)"
		}
		fmt.Fprintf(w, "  %s\t%s\t%d\n", r.code, label, r.n)
		shown++
	}
	if shown == 0 {
		fmt.Fprintf(w, "  (none — every code carrying postings is named)\n")
	}
	fmt.Fprintln(w)
	return nil
}

// distributions covers the rest of what Phase 0 asks to measure before fixing
// the taxonomy: which position levels and categories actually occur, and how
// often the optional fields are filled in at all.
func distributions(ctx context.Context, db *store.DB, w *tabwriter.Writer, top int) error {
	for _, sec := range []struct{ title, query string }{
		{"POSITION LEVELS", `SELECT coalesce(nullif(position_level,''),'(blank)'), count(*) FROM job GROUP BY 1 ORDER BY 2 DESC`},
		{"CATEGORIES", `SELECT coalesce(nullif(category,''),'(blank)'), count(*) FROM job GROUP BY 1 ORDER BY 2 DESC`},
		{"WORK MODE", `SELECT coalesce(nullif(work_mode,''),'(blank)'), count(*) FROM job GROUP BY 1 ORDER BY 2 DESC`},
		{"EMPLOYMENT TYPE", `SELECT coalesce(nullif(employment_type,''),'(blank)'), count(*) FROM job GROUP BY 1 ORDER BY 2 DESC`},
		{"SENIORITY (derived)", `SELECT coalesce(nullif(seniority,''),'(blank)'), count(*) FROM job GROUP BY 1 ORDER BY 2 DESC`},
	} {
		rows, err := db.QueryContext(ctx, sec.query)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "%s\n", sec.title)
		n := 0
		for rows.Next() && n < top {
			var k string
			var c int
			if err := rows.Scan(&k, &c); err != nil {
				rows.Close()
				return err
			}
			fmt.Fprintf(w, "  %s\t%d\n", k, c)
			n++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		fmt.Fprintln(w)
	}

	// Fill rates for the fields whose absence changes a metric's meaning.
	var total, hidden, noExp, noSalary int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*),
		       coalesce(sum(salary_hidden),0),
		       coalesce(sum(CASE WHEN min_years_exp IS NULL THEN 1 ELSE 0 END),0),
		       coalesce(sum(CASE WHEN salary_min IS NULL THEN 1 ELSE 0 END),0)
		FROM job`).Scan(&total, &hidden, &noExp, &noSalary); err != nil {
		return err
	}
	fmt.Fprintf(w, "FILL RATES  (each of these drives a suppression rule)\n")
	if total > 0 {
		fmt.Fprintf(w, "  salary_hidden=1\t%d\t%.1f%%\n", hidden, 100*float64(hidden)/float64(total))
		fmt.Fprintf(w, "  salary_min IS NULL\t%d\t%.1f%%\n", noSalary, 100*float64(noSalary)/float64(total))
		fmt.Fprintf(w, "  min_years_exp IS NULL\t%d\t%.1f%%\n", noExp, 100*float64(noExp)/float64(total))
	}
	fmt.Fprintln(w)
	return nil
}

// unmappedTech is the other half of Phase 0's DoD: the tech_taxonomy seed list.
// These are terms the LLM returned that no alias matched, ranked by how often
// they appeared — docs/03 §7 calls this the entry point for the taxonomy's
// evolution, and it is empty until the LLM layer has run.
func unmappedTech(ctx context.Context, db *store.DB, w *tabwriter.Writer, top int) error {
	rows, err := db.QueryContext(ctx, `
		SELECT raw_term, seen_count FROM unmapped_tech
		WHERE reviewed=0 ORDER BY seen_count DESC, raw_term`)
	if err != nil {
		return err
	}
	defer rows.Close()
	fmt.Fprintf(w, "UNREVIEWED TECH TERMS  (candidates for tech_taxonomy)\n")
	n := 0
	for rows.Next() && n < top {
		var term string
		var c int
		if err := rows.Scan(&term, &c); err != nil {
			return err
		}
		fmt.Fprintf(w, "  %s\t%d\n", term, c)
		n++
	}
	if n == 0 {
		fmt.Fprintf(w, "  (none — the LLM layer has not run, or every term mapped)\n")
	}
	fmt.Fprintln(w)
	return rows.Err()
}
