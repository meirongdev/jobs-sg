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

// posting is the raw archived text of one posting, held between the archive walk
// and the single Extract pass.
type posting struct{ title, desc string }

func run(dataDir string, apply bool, batchSize, top int) error {
	ctx := context.Background()
	// Read-write even for a dry run, so the dry run does not depend on a
	// different open mode than the run it predicts (as in scripts/reclassify).
	db, err := store.Open(filepath.Join(dataDir, "jobs.db"), false)
	if err != nil {
		return err
	}
	defer db.Close()

	// Replay against store.TechSeeds(), not against tech_taxonomy as it stands.
	// On a database seeded by an older build the table still holds retired
	// aliases, and replaying with those would faithfully reproduce the bug being
	// fixed. Seed makes the table equal the seed list, so reading the list is
	// the same taxonomy — and it keeps the dry run genuinely read-only, which a
	// Seed-then-read would not (Seed prunes).
	tax := tech.LoadTaxonomy(store.TechSeeds())

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
	// Keyed raw text, not a computed tech set: a posting appears in the archive
	// once per sighting (measured 2.27x on 2026-08-24) and only the last copy is
	// used, so computing Extract inside the walk threw away 56% of its own work.
	// Title and description are kept apart — the title contains spaces, so a
	// concatenated form cannot be split back.
	latest := make(map[string]posting, len(stored))
	var records int
	// ⚠️ Distinct uuids, not records. Every reconcile re-archives the whole board,
	// so a record count reads an order of magnitude too high next to the posting
	// counts it is printed beside — the exact trap scripts/reclassify documents
	// and that this tool's first version copied without the lesson.
	notEnriched := map[string]struct{}{}
	err = mcf.WalkArchives(dataDir, func(_ string, j mcf.Job) error {
		records++
		if j.UUID == "" {
			return nil
		}
		if _, ok := stored[j.UUID]; !ok {
			notEnriched[j.UUID] = struct{}{}
			return nil
		}
		latest[j.UUID] = posting{title: j.Title, desc: j.Description}
		return nil
	})
	if err != nil {
		return err
	}

	// One Extract per posting, after the walk. Must mirror the nightly rule layer's
	// input exactly (internal/llm/enrich.go: title + " " + StripHTML(desc)) — the
	// whole premise of a replay is bit-identical reproduction.
	var work []store.RuleTechUpdate
	added, removed := map[string]int{}, map[string]int{}
	for uuid, p := range latest {
		techs := make([]store.TechRow, 0, 8)
		for _, t := range tax.Extract(p.title + " " + tech.StripHTML(p.desc)) {
			techs = append(techs, store.TechRow{Slug: t.Slug, Kind: t.Kind})
		}
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

	report(records, len(stored), len(notEnriched), len(work), added, removed, top)

	if !apply {
		fmt.Printf("\nDRY RUN — nothing written. Re-run with --apply to rewrite %d posting(s).\n", len(work))
		return nil
	}
	// Converge the table too: the nightly enrich reads tech_taxonomy, so leaving
	// a retired alias there would keep it matching on new postings even though
	// this replay just cleaned it out of the history.
	if err := db.Seed(ctx); err != nil {
		return fmt.Errorf("seed taxonomy: %w", err)
	}
	if len(work) == 0 {
		fmt.Println("\nNothing to write (taxonomy re-seeded).")
		return nil
	}
	for start := 0; start < len(work); start += batchSize {
		end := min(start+batchSize, len(work))
		if err := db.ApplyRuleTech(ctx, work[start:end]); err != nil {
			return fmt.Errorf("apply postings %d-%d (%d already committed): %w", start, end, start, err)
		}
	}
	fmt.Printf("\nAPPLIED — rule-layer rows rewritten for %d posting(s).\n", len(work))
	return nil
}

func report(records, enriched, notEnriched, changed int, added, removed map[string]int, top int) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "archive records read\t", records, "\t(the same posting appears once per sighting)")
	fmt.Fprintln(tw, "postings with rule rows\t", enriched, "\t(the replay population)")
	fmt.Fprintln(tw, "archived, not yet enriched\t", notEnriched, "\t(distinct postings, left to the nightly backlog)")
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
