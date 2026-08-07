// Command reclassify recomputes the classify layer for every posting in the raw
// archive and rewrites the derived columns in jobs.db.
//
// Why this exists: classification (is_swe, role_family, seniority, work_mode,
// company_type) is computed during ingest and stored on the row. Fix a
// classifier bug and only postings the pipeline sees again get the fix — a
// posting that has since left the board keeps the wrong verdict forever, and
// the weekly history keeps being wrong about it. The raw archive holds every
// posting ever seen, and the rules are pure functions of that JSON, so the
// whole history can be recomputed offline.
//
// It makes **no network calls**. Compared with forcing a full reconcile:
//
//	                reconcile          reclassify
//	MCF API         ~867 pages         none
//	wall clock      20-25 min          one pass over the archive
//	coverage        live postings      every posting ever archived
//
// Dry run by default — it prints what would change and writes nothing. Pass
// --apply to commit.
//
//	go run ./scripts/reclassify --data-dir ./data            # report only
//	go run ./scripts/reclassify --data-dir ./data --apply    # write
//
// ⚠️ It rewrites only derived columns; lifecycle state (closed_at,
// last_seen_at, miss_count) is never touched — see store.ApplyClassifications.
// Postings present in the archive but absent from jobs.db are counted and left
// alone rather than inserted.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/meirongdev/jobs-sg/internal/classify"
	"github.com/meirongdev/jobs-sg/internal/mcf"
	"github.com/meirongdev/jobs-sg/internal/store"
)

func main() {
	dataDir := flag.String("data-dir", "/data", "directory holding jobs.db and raw/")
	apply := flag.Bool("apply", false, "write the changes (default: report only)")
	batchSize := flag.Int("batch", 2000, "rows per write transaction")
	flag.Parse()

	if err := run(*dataDir, *apply, *batchSize); err != nil {
		fmt.Fprintln(os.Stderr, "reclassify:", err)
		os.Exit(1)
	}
}

// transitions counts old -> new for one column, so the report shows the shape
// of the change rather than just its size.
type transitions map[string]map[string]int

func (t transitions) add(from, to string) {
	if t[from] == nil {
		t[from] = map[string]int{}
	}
	t[from][to]++
}

func run(dataDir string, apply bool, batchSize int) error {
	ctx := context.Background()
	// Read-write even for a dry run: opening read-only would fail on a data dir
	// whose jobs.db has never been created, and the dry run must not depend on
	// a different open mode than the run it is predicting.
	db, err := store.Open(filepath.Join(dataDir, "jobs.db"), false)
	if err != nil {
		return err
	}
	defer db.Close()

	ssoc, err := db.LoadSSOCMap(ctx)
	if err != nil {
		return fmt.Errorf("load ssoc taxonomy: %w", err)
	}
	classifier := classify.New(ssoc)

	current, err := db.LoadClassifications(ctx)
	if err != nil {
		return fmt.Errorf("load current classifications: %w", err)
	}

	// Last archived copy of each posting wins: WalkArchives visits files in
	// chronological order, so a later sighting overwrites an earlier one.
	latest := make(map[string]store.ClassificationUpdate, len(current))
	var records, unknownUUID int
	err = mcf.WalkArchives(dataDir, func(_ string, j mcf.Job) error {
		records++
		if j.UUID == "" {
			return nil
		}
		if _, ok := current[j.UUID]; !ok {
			unknownUUID++
			return nil // not in jobs.db: counted, never inserted
		}
		res := classifier.Classify(j)
		u := store.ClassificationUpdate{
			UUID: j.UUID,
			Classification: store.Classification{
				RoleFamily: res.RoleFamily,
				Seniority:  res.Seniority,
				WorkMode:   res.WorkMode,
				IsSWE:      res.IsSWE,
			},
		}
		if j.PostedCompany != nil {
			u.CompanyUEN, u.CompanyType = j.PostedCompany.UEN, res.CompanyType
		}
		latest[j.UUID] = u
		return nil
	})
	if err != nil {
		return err
	}

	work := make([]store.ClassificationUpdate, 0, len(latest))
	mode, family, sen := transitions{}, transitions{}, transitions{}
	sweGained, sweLost := 0, 0
	for uuid, u := range latest {
		was := current[uuid]
		if was == u.Classification {
			continue
		}
		work = append(work, u)
		if was.WorkMode != u.WorkMode {
			mode.add(was.WorkMode, u.WorkMode)
		}
		if was.RoleFamily != u.RoleFamily {
			family.add(was.RoleFamily, u.RoleFamily)
		}
		if was.Seniority != u.Seniority {
			sen.add(was.Seniority, u.Seniority)
		}
		switch {
		case !was.IsSWE && u.IsSWE:
			sweGained++
		case was.IsSWE && !u.IsSWE:
			sweLost++
		}
	}
	// Deterministic write order, so two runs over the same archive do the same
	// thing and a diff of two reports is meaningful.
	sort.Slice(work, func(i, j int) bool { return work[i].UUID < work[j].UUID })

	report(os.Stdout, records, len(latest), unknownUUID, len(work), sweGained, sweLost, mode, family, sen)

	if !apply {
		fmt.Printf("\nDRY RUN — nothing written. Re-run with --apply to commit %d row(s).\n", len(work))
		return nil
	}
	if len(work) == 0 {
		fmt.Println("\nNothing to write.")
		return nil
	}

	var updated int
	for start := 0; start < len(work); start += batchSize {
		end := min(start+batchSize, len(work))
		n, err := db.ApplyClassifications(ctx, work[start:end])
		if err != nil {
			return fmt.Errorf("apply rows %d-%d (%d already committed): %w", start, end, updated, err)
		}
		updated += n
	}
	fmt.Printf("\nAPPLIED — %d job row(s) updated.\n", updated)
	return nil
}

func report(w *os.File, records, postings, unknown, changed, sweGained, sweLost int, mode, family, sen transitions) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "archive records read\t", records)
	fmt.Fprintln(tw, "distinct postings in jobs.db\t", postings)
	fmt.Fprintln(tw, "archived but not in jobs.db\t", unknown, "\t(left alone — see the command doc)")
	fmt.Fprintln(tw, "postings whose verdict changes\t", changed)
	tw.Flush()

	if sweGained > 0 || sweLost > 0 {
		fmt.Printf("\n⚠️  is_swe moves: +%d / -%d — this changes the population every\n"+
			"    metric is computed over, not just one column.\n", sweGained, sweLost)
	}
	printTransitions(w, "work_mode", mode)
	printTransitions(w, "role_family", family)
	printTransitions(w, "seniority", sen)
}

func printTransitions(w *os.File, name string, t transitions) {
	if len(t) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s\n", name)
	froms := make([]string, 0, len(t))
	for f := range t {
		froms = append(froms, f)
	}
	sort.Strings(froms)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, f := range froms {
		tos := make([]string, 0, len(t[f]))
		for to := range t[f] {
			tos = append(tos, to)
		}
		sort.Slice(tos, func(i, j int) bool { return t[f][tos[i]] > t[f][tos[j]] })
		for _, to := range tos {
			fmt.Fprintf(tw, "  %s\t-> %s\t%d\n", orDash(f), orDash(to), t[f][to])
		}
	}
	tw.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "(empty)"
	}
	return s
}
