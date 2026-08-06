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
