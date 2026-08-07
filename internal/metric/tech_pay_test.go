package metric

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/meirongdev/jobs-sg/internal/classify"
	"github.com/meirongdev/jobs-sg/internal/mcf"
	"github.com/meirongdev/jobs-sg/internal/store"
)

func TestPremiumBaselineIsARealAdvertisedSalary(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	r, err := TechReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Salary.Disclosed == 0 || r.MedianAll == 0 {
		t.Fatalf("no disclosed salaries behind the baseline: n=%d median=%v", r.Salary.Disclosed, r.MedianAll)
	}
	if r.Salary.Total < r.Salary.Disclosed || r.Salary.Total == 0 {
		t.Errorf("transparency denominator %d must cover the disclosed sample %d", r.Salary.Total, r.Salary.Disclosed)
	}
	if p := r.Salary.Pct(); p <= 0 || p > 1 {
		t.Errorf("transparency rate = %v, want (0,1]", p)
	}
	if got, want := r.Salary.Pct(), float64(r.Salary.Disclosed)/float64(r.Salary.Total); got != want {
		t.Errorf("Salary.Pct() = %v, want Salary.Disclosed/Salary.Total = %v", got, want)
	}
	var n int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM job
		WHERE is_swe=1 AND salary_hidden=0 AND salary_type='Monthly'
		  AND salary_min IS NOT NULL AND salary_max IS NOT NULL
		  AND (salary_min+salary_max)/2.0 = ?`, r.MedianAll).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Errorf("median %v was never advertised by any posting", r.MedianAll)
	}
}

func TestTechTransparencyIsOneAtomicPair(t *testing.T) {
	// Disclosed and Total come from a single row now, so Disclosed > Total is
	// unrepresentable rather than merely unobserved. Also pin that switching to
	// the atomic query did not change the disclosed count: it must still equal
	// the number of rows the salary sample returns.
	ctx := context.Background()
	db := seedFixture(t)
	r, err := TechReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Salary.Disclosed > r.Salary.Total {
		t.Errorf("disclosed %d exceeds total %d", r.Salary.Disclosed, r.Salary.Total)
	}
	if r.Salary.Total == 0 {
		t.Fatal("fixture window has no SWE postings")
	}
	var sample int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) `+swePosted+` `+disclosedSalary, Rolling(fixtureNow, RollingDays).Args()...).Scan(&sample); err != nil {
		t.Fatal(err)
	}
	if r.Salary.Disclosed != sample {
		t.Errorf("Disclosed = %d, want %d (the disclosed-salary sample size)", r.Salary.Disclosed, sample)
	}
}

func TestPremiumSuppressedBelowSampleThreshold(t *testing.T) {
	db := seedFixture(t)
	r, err := TechReportFor(context.Background(), db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	var shown, hidden int
	for _, s := range r.Ranked {
		if s.Premium.Suppressed {
			hidden++
			if s.Premium.Samples >= MinSalarySamplesPerTech {
				t.Errorf("%s suppressed with n=%d, above the threshold", s.Slug, s.Premium.Samples)
			}
			if s.PremiumPct != 0 {
				t.Errorf("%s carries a premium while suppressed: %v", s.Slug, s.PremiumPct)
			}
		} else {
			shown++
			if s.Premium.Samples < MinSalarySamplesPerTech {
				t.Errorf("%s shown with only n=%d", s.Slug, s.Premium.Samples)
			}
		}
	}
	if shown == 0 || hidden == 0 {
		t.Fatalf("fixture must exercise both sides (shown=%d hidden=%d)", shown, hidden)
	}
}

func TestPremiumFollowsTheLens(t *testing.T) {
	// spec §3.2: a raw premium mixes seniority in, so the number must be
	// recomputed inside the active experience band.
	ctx := context.Background()
	db := seedFixture(t)
	all, err := TechReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	lens, err := ParseLens("6+", "")
	if err != nil {
		t.Fatal(err)
	}
	senior, err := TechReportFor(ctx, db, fixtureNow, lens)
	if err != nil {
		t.Fatal(err)
	}
	if senior.Salary.Disclosed == 0 {
		t.Fatal("6+ band has no disclosed salaries in the fixture")
	}
	// Salary.Disclosed comes straight out of salarySample, so a smaller
	// sample under the lens is itself the proof that the lens reaches the
	// salary query. Do not assert the two medians differ — over random
	// fixture salaries that is a coin flip, and a flaky test is worse than
	// no test.
	if senior.Salary.Disclosed >= all.Salary.Disclosed {
		t.Errorf("lensed salary sample %d must be smaller than %d", senior.Salary.Disclosed, all.Salary.Disclosed)
	}
	if senior.MedianAll == 0 {
		t.Error("6+ band median is 0 despite a non-empty sample")
	}
}

func TestEntryFriendlyIsAShareOfMentioningPostings(t *testing.T) {
	db := seedFixture(t)
	r, err := TechReportFor(context.Background(), db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	nonZero := 0
	for _, s := range r.Ranked {
		if s.EntryFriendly < 0 || s.EntryFriendly > 1 {
			t.Errorf("%s entry-friendliness = %v, want 0..1", s.Slug, s.EntryFriendly)
		}
		if s.EntryFriendly > 0 {
			nonZero++
		}
	}
	if nonZero == 0 {
		t.Error("no technology has any entry-level posting; the fixture has 0-2 year rows")
	}
}

// TestPremiumSignFollowsPay pins the sign convention median(with t)/median(all) − 1.
// The shared fixture randomizes salary independent of technology, so this
// seeds a controlled DB: one tech consistently above the market median, one
// below, and untagged mid-pay ballast that pins the overall median strictly
// between them (with an even high/low split alone, the upper median lands ON
// the high group and the premium degenerates to exactly 0). A flipped
// convention (1 − x/y) inverts both asserted signs.
func TestPremiumSignFollowsPay(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "jobs.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	cl := classify.New(map[string]string{"25121": "Backend"})
	week := LastCompletedWeek(fixtureNow)
	day := week.Start.In(SGT).Format("2006-01-02")
	mk := func(uuid, slug string, lo, hi float64) {
		j := mcf.Job{
			UUID: uuid, Title: "Backend Engineer", Description: "d",
			Metadata: mcf.Metadata{JobPostID: "MCF-" + uuid, NewPostingDate: day, ExpiryDate: "2026-12-31"},
			SSOCCode: "25121", Categories: []mcf.Category{{Category: "Information Technology"}},
			Salary: &mcf.Salary{Minimum: lo, Maximum: hi, Type: mcf.SalaryType{SalaryType: "Monthly"}},
		}
		if _, err := db.UpsertJob(ctx, j, cl.Classify(j), "raw/x#0"); err != nil {
			t.Fatal(err)
		}
		if slug != "" {
			if err := db.WriteRuleTech(ctx, uuid, []store.TechRow{{Slug: slug, Kind: "language"}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := 0; i < MinSalarySamplesPerTech; i++ {
		mk(fmt.Sprintf("gold-%03d", i), "goldlang", 9000, 11000) // midpoint 10000
		mk(fmt.Sprintf("lead-%03d", i), "leadlang", 3000, 5000)  // midpoint 4000
	}
	for i := 0; i < MinSalarySamplesPerTech+1; i++ {
		mk(fmt.Sprintf("mid-%03d", i), "", 6000, 8000) // untagged ballast, midpoint 7000
	}
	r, err := TechReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	var gold, lead *TechStat
	for i := range r.Ranked {
		switch r.Ranked[i].Slug {
		case "goldlang":
			gold = &r.Ranked[i]
		case "leadlang":
			lead = &r.Ranked[i]
		}
	}
	if gold == nil || lead == nil {
		t.Fatalf("seeded techs missing from ranking: %+v", r.Ranked)
	}
	if gold.Premium.Suppressed || lead.Premium.Suppressed {
		t.Fatalf("premiums suppressed at n=%d each", MinSalarySamplesPerTech)
	}
	if gold.PremiumPct <= 0 {
		t.Errorf("above-median tech premium = %v, want > 0", gold.PremiumPct)
	}
	if lead.PremiumPct >= 0 {
		t.Errorf("below-median tech premium = %v, want < 0", lead.PremiumPct)
	}
}
