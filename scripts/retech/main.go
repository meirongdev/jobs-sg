// Command retech recomputes the rule-layer technology extraction for every
// posting in the raw archive and rewrites the source='rule' rows in jobs.db.
//
// Why this exists: enrich's backlog skips any posting that already has
// job_tech rows or an enrich_done marker for the layer (internal/store:
// enrichBacklog). That is right for a nightly run — it is what stops the LLM
// being re-billed for work already done — but it means a fix to the alias table
// or to the matcher reaches new postings only. Every posting already enriched
// keeps whatever the old rules said, forever, and the weekly history keeps
// being wrong about it. The raw archive holds every posting ever seen and the
// rule layer is a pure function of that JSON plus tech_taxonomy, so the whole
// history can be recomputed offline.
//
// It makes **no network calls** and never touches the LLM layer: source='llm'
// rows are not reproducible without the model, so they are left exactly as
// they are. Run it after changing techSeeds or internal/tech.
//
// ⚠️ Reading the report: a changed tech set does not always mean the rules
// changed. The replay uses the **last archived copy** of each posting, so a
// posting whose description was edited after it was enriched also shows up —
// that is what the long tail of one-posting slugs in the report is (verified on
// 2026-08-24: the single `kibana` and `swift` removals were both postings whose
// current text no longer mentions the tool). Rule-driven changes are the ones
// that move hundreds of postings at once.
//
// Dry run by default — it prints what would change and writes nothing. Pass
// --apply to commit.
//
//	go run ./scripts/retech --data-dir ./data            # report only
//	go run ./scripts/retech --data-dir ./data --apply    # write
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"text/tabwriter"

	"github.com/meirongdev/jobs-sg/internal/mcf"
	"github.com/meirongdev/jobs-sg/internal/store"
	"github.com/meirongdev/jobs-sg/internal/tech"
)

func main() {
	dataDir := flag.String("data-dir", "/data", "directory holding jobs.db and raw/")
	apply := flag.Bool("apply", false, "write the changes (default: report only)")
	batchSize := flag.Int("batch", 2000, "postings per write transaction")
	top := flag.Int("top", 25, "how many slugs to list per direction")
	flag.Parse()

	if err := run(*dataDir, *apply, *batchSize, *top); err != nil {
		fmt.Fprintln(os.Stderr, "retech:", err)
		os.Exit(1)
	}
}

func run(dataDir string, apply bool, batchSize, top int) error {
	ctx := context.Background()
	// Read-write even for a dry run, so the dry run does not depend on a
	// different open mode than the run it predicts (as in scripts/reclassify).
	db, err := store.Open(filepath.Join(dataDir, "jobs.db"), false)
	if err != nil {
		return err
	}
	defer db.Close()

	// Seed first: the taxonomy in the table is what the rule layer reads, and
	// on a database seeded by an older build it still holds retired aliases.
	// Replaying against those would faithfully reproduce the bug being fixed.
	if err := db.Seed(ctx); err != nil {
		return fmt.Errorf("seed taxonomy: %w", err)
	}
	rows, err := db.LoadTechTaxonomy(ctx)
	if err != nil {
		return fmt.Errorf("load taxonomy: %w", err)
	}
	tax := tech.LoadTaxonomy(rows)

	stored, err := db.LoadRuleTech(ctx)
	if err != nil {
		return fmt.Errorf("load stored rule tech: %w", err)
	}

	// Last archived copy of each posting wins: WalkArchives visits files in
	// chronological order, so a later sighting overwrites an earlier one. A
	// posting is only recomputed if the rule layer has already run on it —
	// postings the archive holds but enrich has never processed belong to the
	// nightly backlog, and inserting rows for them here would take them out of
	// it without the LLM layer ever seeing them.
	latest := make(map[string][]store.TechRow, len(stored))
	var records, notEnriched int
	err = mcf.WalkArchives(dataDir, func(_ string, j mcf.Job) error {
		records++
		if j.UUID == "" {
			return nil
		}
		if _, ok := stored[j.UUID]; !ok {
			notEnriched++
			return nil
		}
		techs := tax.Extract(j.Title + " " + tech.StripHTML(j.Description))
		out := make([]store.TechRow, 0, len(techs))
		for _, t := range techs {
			out = append(out, store.TechRow{Slug: t.Slug, Kind: t.Kind})
		}
		latest[j.UUID] = out
		return nil
	})
	if err != nil {
		return err
	}

	var work []store.RuleTechUpdate
	added, removed := map[string]int{}, map[string]int{}
	for uuid, techs := range latest {
		was, now := stored[uuid], store.SlugsOf(techs)
		if slices.Equal(was, now) {
			continue
		}
		work = append(work, store.RuleTechUpdate{UUID: uuid, Techs: techs})
		for _, s := range now {
			if !slices.Contains(was, s) {
				added[s]++
			}
		}
		for _, s := range was {
			if !slices.Contains(now, s) {
				removed[s]++
			}
		}
	}
	// Deterministic write order, so two runs over the same archive do the same
	// thing and a diff of two reports is meaningful.
	sort.Slice(work, func(i, j int) bool { return work[i].UUID < work[j].UUID })

	report(records, len(stored), notEnriched, len(work), added, removed, top)

	if !apply {
		fmt.Printf("\nDRY RUN — nothing written. Re-run with --apply to rewrite %d posting(s).\n", len(work))
		return nil
	}
	if len(work) == 0 {
		fmt.Println("\nNothing to write.")
		return nil
	}
	var written int
	for start := 0; start < len(work); start += batchSize {
		end := min(start+batchSize, len(work))
		n, err := db.ApplyRuleTech(ctx, work[start:end])
		if err != nil {
			return fmt.Errorf("apply postings %d-%d (%d already committed): %w", start, end, written, err)
		}
		written += n
	}
	fmt.Printf("\nAPPLIED — rule-layer rows rewritten for %d posting(s).\n", written)
	return nil
}

func report(records, enriched, notEnriched, changed int, added, removed map[string]int, top int) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "archive records read\t", records, "\t(the same posting appears once per sighting)")
	fmt.Fprintln(tw, "postings with rule rows\t", enriched, "\t(the replay population)")
	fmt.Fprintln(tw, "archived, not yet enriched\t", notEnriched, "\t(left to the nightly backlog)")
	fmt.Fprintln(tw, "postings whose tech set changes\t", changed, "")
	tw.Flush()

	for _, sec := range []struct {
		label string
		m     map[string]int
	}{{"REMOVED (slug no longer matches)", removed}, {"ADDED (slug now matches)", added}} {
		if len(sec.m) == 0 {
			continue
		}
		fmt.Printf("\n%s\n", sec.label)
		keys := make([]string, 0, len(sec.m))
		for k := range sec.m {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if sec.m[keys[i]] != sec.m[keys[j]] {
				return sec.m[keys[i]] > sec.m[keys[j]]
			}
			return keys[i] < keys[j]
		})
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, k := range keys[:min(top, len(keys))] {
			fmt.Fprintf(tw, "  %s\t%d\tposting(s)\n", k, sec.m[k])
		}
		tw.Flush()
		if len(keys) > top {
			fmt.Printf("  … and %d more slug(s)\n", len(keys)-top)
		}
	}
}
