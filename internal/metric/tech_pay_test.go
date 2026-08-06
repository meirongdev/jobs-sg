package metric

import (
	"context"
	"testing"
)

func TestPremiumBaselineIsARealAdvertisedSalary(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	r, err := TechReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if r.SalaryN == 0 || r.MedianAll == 0 {
		t.Fatalf("no disclosed salaries behind the baseline: n=%d median=%v", r.SalaryN, r.MedianAll)
	}
	if r.SalaryTotal < r.SalaryN || r.SalaryTotal == 0 {
		t.Errorf("transparency denominator %d must cover the disclosed sample %d", r.SalaryTotal, r.SalaryN)
	}
	if p := r.TransparencyPct(); p <= 0 || p > 1 {
		t.Errorf("transparency rate = %v, want (0,1]", p)
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
	if senior.SalaryN == 0 {
		t.Fatal("6+ band has no disclosed salaries in the fixture")
	}
	// SalaryN comes straight out of salarySample, so a smaller sample under the
	// lens is itself the proof that the lens reaches the salary query. Do not
	// assert the two medians differ — over random fixture salaries that is a
	// coin flip, and a flaky test is worse than no test.
	if senior.SalaryN >= all.SalaryN {
		t.Errorf("lensed salary sample %d must be smaller than %d", senior.SalaryN, all.SalaryN)
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
