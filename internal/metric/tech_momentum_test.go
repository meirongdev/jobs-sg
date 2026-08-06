package metric

import (
	"context"
	"testing"
	"time"
)

func TestMomentumIsPercentagePointsNotRelativeChange(t *testing.T) {
	db := seedFixture(t)
	r, err := TechReportFor(context.Background(), db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if r.History.Suppressed {
		t.Fatalf("fixture covers W27..W32, momentum history should be complete: %+v", r.History)
	}
	// The fixture repeats the same template rows every week, so shares are
	// near-flat: a pp delta stays tiny while a relative delta would too, but a
	// pp value can never exceed 1.0 in magnitude by construction.
	for _, s := range r.Ranked {
		if s.Momentum.Suppressed {
			continue
		}
		if s.MomentumPP < -1 || s.MomentumPP > 1 {
			t.Errorf("%s momentum = %v, outside the -1..1 range a share delta must live in",
				s.Slug, s.MomentumPP)
		}
	}
}

func TestMomentumSuppressedForThinTechnologies(t *testing.T) {
	db := seedFixture(t)
	r, err := TechReportFor(context.Background(), db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	var thin, thick int
	for _, s := range r.Ranked {
		if s.Count < MinTechCountForMomentum {
			thin++
			if !s.Momentum.Suppressed || s.Momentum.Reason != ReasonSample {
				t.Errorf("%s has %d postings but momentum is not sample-suppressed: %+v",
					s.Slug, s.Count, s.Momentum)
			}
		} else {
			thick++
			if s.Momentum.Suppressed {
				t.Errorf("%s has %d postings, momentum should be shown: %+v",
					s.Slug, s.Count, s.Momentum)
			}
		}
	}
	if thin == 0 || thick == 0 {
		t.Fatalf("fixture must exercise both sides of the threshold (thin=%d thick=%d)", thin, thick)
	}
}

func TestMomentumSuppressedWhenHistoryIsShort(t *testing.T) {
	// A clock early in the fixture leaves fewer than 4 baseline weeks behind the
	// reported week, which must degrade to a history suppression, not a 0.
	db := seedFixture(t)
	early := time.Date(2026, 7, 13, 9, 0, 0, 0, SGT) // W29 Monday -> reports W28
	r, err := TechReportFor(context.Background(), db, early, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.History.Suppressed || r.History.Reason != ReasonHistory {
		t.Errorf("history = %+v, want history suppression", r.History)
	}
	if r.History.WeeksRequired != MinWeeksForMomentum {
		t.Errorf("WeeksRequired = %d, want %d", r.History.WeeksRequired, MinWeeksForMomentum)
	}
	for _, s := range r.Ranked {
		if !s.Momentum.Suppressed {
			t.Errorf("%s momentum shown despite short history", s.Slug)
		}
		if s.MomentumPP != 0 {
			t.Errorf("%s carries a momentum value while suppressed: %v", s.Slug, s.MomentumPP)
		}
	}
	if len(r.Rising) != 0 || len(r.Falling) != 0 {
		t.Errorf("rising/falling boards must be empty with short history")
	}
}

func TestMomentumIsAgainstTheFourWeekMean(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	// Perturb exactly ONE baseline week: attach kubernetes to every SWE
	// posting of W28. Under the correct formula the momentum moves by a
	// quarter of that week's share change; a "versus last baseline week"
	// regression would not move at all (W31 is untouched).
	week := LastCompletedWeek(fixtureNow)
	baseline := PrevWeeks(week, MinWeeksForMomentum-1)
	if _, err := db.ExecContext(ctx, `
		INSERT OR IGNORE INTO job_tech(job_uuid, tech_slug, tech_kind, source)
		SELECT j.uuid, 'kubernetes', 'tool', 'rule' FROM job j
		WHERE j.is_swe=1 AND j.posting_date >= ? AND j.posting_date < ?`,
		baseline[0].Args()...); err != nil {
		t.Fatal(err)
	}
	r, err := TechReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	// Recompute the expectation independently, week by week, from the DB.
	share := func(w Window) float64 {
		var denom, count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) `+swePosted+` `+enrichedPredicate, w.Args()...).Scan(&denom); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `
			SELECT count(DISTINCT j.uuid) FROM job j JOIN job_tech t ON t.job_uuid=j.uuid
			WHERE j.is_swe=1 AND j.posting_date >= ? AND j.posting_date < ? AND t.tech_slug='kubernetes'`,
			w.Args()...).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return float64(count) / float64(denom)
	}
	var sum float64
	for _, bw := range baseline {
		sum += share(bw)
	}
	want := share(week) - sum/float64(len(baseline))
	got, found := 0.0, false
	for _, s := range r.Ranked {
		if s.Slug == "kubernetes" {
			if s.Momentum.Suppressed {
				t.Fatalf("kubernetes momentum suppressed (%+v); it clears both gates in this fixture", s.Momentum)
			}
			got, found = s.MomentumPP, true
		}
	}
	if !found {
		t.Fatal("kubernetes missing from the ranking")
	}
	if diff := got - want; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("momentum = %v, want share_W − mean(4 baselines) = %v", got, want)
	}
}

func TestRisingAndFallingExcludeSuppressedRows(t *testing.T) {
	db := seedFixture(t)
	r, err := TechReportFor(context.Background(), db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range append(append([]TechStat{}, r.Rising...), r.Falling...) {
		if s.Momentum.Suppressed {
			t.Errorf("%s is suppressed but appears on a momentum board", s.Slug)
		}
	}
	for i := 1; i < len(r.Rising); i++ {
		if r.Rising[i-1].MomentumPP < r.Rising[i].MomentumPP {
			t.Errorf("rising board not descending at %d", i)
		}
	}
	for i := 1; i < len(r.Falling); i++ {
		if r.Falling[i-1].MomentumPP > r.Falling[i].MomentumPP {
			t.Errorf("falling board not ascending at %d", i)
		}
	}
}

func TestMomentumEligibleCountsGateClearers(t *testing.T) {
	ctx := context.Background()
	db := seedFixture(t)
	all, err := TechReportFor(ctx, db, fixtureNow, Lens{})
	if err != nil {
		t.Fatal(err)
	}
	if all.MomentumEligible == 0 {
		t.Fatal("unlensed fixture has techs above the bar; eligible must be > 0")
	}
	if all.MomentumFloor != MinTechCountForMomentum {
		t.Errorf("floor = %d, want %d", all.MomentumFloor, MinTechCountForMomentum)
	}
	// Backend postings exist every week (history complete) but no single tech
	// clears 10 mentions within the lens — the exact state the gated-boards
	// message exists for.
	lens, err := ParseLens("", "Backend")
	if err != nil {
		t.Fatal(err)
	}
	backend, err := TechReportFor(ctx, db, fixtureNow, lens)
	if err != nil {
		t.Fatal(err)
	}
	if backend.History.Suppressed {
		t.Fatal("Backend lens has data in every window; history must be complete")
	}
	if backend.MomentumEligible != 0 {
		t.Errorf("eligible = %d under the Backend lens, want 0 (max weekly count is below the bar)", backend.MomentumEligible)
	}
	if len(backend.Rising) != 0 || len(backend.Falling) != 0 {
		t.Error("no eligible techs but a board is non-empty")
	}
}
