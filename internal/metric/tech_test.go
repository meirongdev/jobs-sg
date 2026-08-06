package metric

import (
	"context"
	"testing"
)

func TestTechDemandRanksTheReportedWeek(t *testing.T) {
	db := seedFixture(t)
	r, err := TechReportFor(context.Background(), db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Week != "2026-W32" {
		t.Errorf("week = %s, want 2026-W32 (the last completed week)", r.Week)
	}
	if r.Denom == 0 {
		t.Fatal("enriched denominator is 0; the fixture should have enriched SWE postings")
	}
	if len(r.Ranked) == 0 {
		t.Fatal("no ranked technologies")
	}
	for i := 1; i < len(r.Ranked); i++ {
		if r.Ranked[i-1].Count < r.Ranked[i].Count {
			t.Fatalf("ranking not descending at %d: %+v", i, r.Ranked[i-1:i+1])
		}
	}
	for _, s := range r.Ranked {
		if s.Count > r.Denom {
			t.Errorf("%s count %d exceeds denominator %d", s.Slug, s.Count, r.Denom)
		}
		if s.Share < 0 || s.Share > 1 {
			t.Errorf("%s share = %v, want 0..1", s.Slug, s.Share)
		}
		if r.Denom > 0 && s.Share != float64(s.Count)/float64(r.Denom) {
			t.Errorf("%s share = %v, want Count/Denom = %v", s.Slug, s.Share, float64(s.Count)/float64(r.Denom))
		}
	}
}

func TestTechCountsDedupeRuleAndLLMRows(t *testing.T) {
	// job_tech's primary key is (job_uuid, tech_slug, source): the same posting
	// can carry the same technology from both layers. Counting rows instead of
	// distinct postings would double it.
	ctx := context.Background()
	db := seedFixture(t)
	var uuid string
	if err := db.QueryRowContext(ctx, `
		SELECT job_uuid FROM job_tech WHERE tech_slug='kubernetes' LIMIT 1`).Scan(&uuid); err != nil {
		t.Fatal(err)
	}
	before, err := TechReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO job_tech(job_uuid, tech_slug, tech_kind, source) VALUES(?,?,?,'llm')`,
		uuid, "kubernetes", "tool"); err != nil {
		t.Fatal(err)
	}
	after, err := TechReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if countOf(before.Ranked, "kubernetes") != countOf(after.Ranked, "kubernetes") {
		t.Errorf("duplicate source row changed the count: %d -> %d",
			countOf(before.Ranked, "kubernetes"), countOf(after.Ranked, "kubernetes"))
	}
}

func TestEnrichedDenominatorExcludesBacklog(t *testing.T) {
	// A posting with neither job_tech rows nor an enrich_done marker is still in
	// the backlog; counting it would depress every technology's share.
	ctx := context.Background()
	db := seedFixture(t)
	before, err := TechReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	// The probe must come from the REPORTED week: the fixture is written in
	// chronological order, so an unconstrained LIMIT 1 lands in 2026-W27 —
	// five weeks before the reported window — and deleting its enrichment
	// could never move the W32 denominator.
	week := LastCompletedWeek(fixtureNow)
	var uuid string
	if err := db.QueryRowContext(ctx, `
		SELECT j.uuid FROM job j
		WHERE j.is_swe=1 AND j.posting_date >= ? AND j.posting_date < ? LIMIT 1`,
		week.Args()...).Scan(&uuid); err != nil {
		t.Fatal(err)
	}
	// Un-enriching means removing BOTH traces: writeTech marks enrich_done even
	// for zero-match jobs (internal/store/enrich.go), so deleting job_tech
	// alone leaves the posting "processed" and the denominator unchanged.
	if _, err := db.ExecContext(ctx, `DELETE FROM job_tech WHERE job_uuid=?`, uuid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM enrich_done WHERE job_uuid=?`, uuid); err != nil {
		t.Fatal(err)
	}
	after, err := TechReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if after.Denom >= before.Denom {
		t.Errorf("denominator %d did not shrink after un-enriching a posting (was %d)",
			after.Denom, before.Denom)
	}
}

func TestTechLensNarrowsTheDenominator(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	all, err := TechReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	lens, err := ParseLens("0-2", "")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := TechReportFor(ctx, db, fixtureNow, lens)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Denom == 0 {
		t.Fatal("0-2 band has no enriched postings in the fixture")
	}
	if entry.Denom >= all.Denom {
		t.Errorf("lensed denominator %d must be smaller than %d", entry.Denom, all.Denom)
	}
}

func countOf(stats []TechStat, slug string) int {
	for _, s := range stats {
		if s.Slug == slug {
			return s.Count
		}
	}
	return -1
}
