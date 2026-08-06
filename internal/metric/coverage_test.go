package metric

import "testing"

func TestSampleCoverageSuppressesBelowThreshold(t *testing.T) {
	if c := SampleCoverage(4, MinSalarySamplesPerCell); !c.Suppressed || c.Reason != ReasonSample {
		t.Errorf("n=4 -> %+v, want suppressed by sample", c)
	}
	if c := SampleCoverage(5, MinSalarySamplesPerCell); c.Suppressed {
		t.Errorf("n=5 -> %+v, want not suppressed", c)
	}
}

func TestHistoryCoverageSuppressesShortHistory(t *testing.T) {
	if c := HistoryCoverage(4, MinWeeksForMomentum); !c.Suppressed || c.Reason != ReasonHistory {
		t.Errorf("4 of 5 weeks -> %+v, want suppressed by history", c)
	}
	if c := HistoryCoverage(5, MinWeeksForMomentum); c.Suppressed {
		t.Errorf("5 of 5 weeks -> %+v, want not suppressed", c)
	}
}

func TestPercentileReturnsValuesThatActuallyAppeared(t *testing.T) {
	vals := []float64{5000, 6000, 7000, 8000, 9000}
	for _, q := range []float64{0.25, 0.5, 0.75} {
		got := Percentile(vals, q)
		found := false
		for _, v := range vals {
			if v == got {
				found = true
			}
		}
		if !found {
			t.Errorf("Percentile(q=%v) = %v, which is not in the sample", q, got)
		}
	}
}

func TestPercentileMatchesTheExistingUpperMedian(t *testing.T) {
	// The weekly report used vals[len(vals)/2]; Percentile(.,0.5) must agree so
	// the two never disagree on the same data.
	for _, vals := range [][]float64{
		{1, 2, 3},
		{1, 2, 3, 4},
		{1, 2, 3, 4, 5},
	} {
		if got, want := Percentile(vals, 0.5), vals[len(vals)/2]; got != want {
			t.Errorf("Percentile(%v, 0.5) = %v, want %v", vals, got, want)
		}
	}
}

func TestPercentileEmptyAndBounds(t *testing.T) {
	if got := Percentile(nil, 0.5); got != 0 {
		t.Errorf("empty sample = %v, want 0", got)
	}
	// q=0.75 over 4 values lands on the maximum — intentional (spec §3.3), and
	// such small cells are suppressed anyway.
	if got := Percentile([]float64{1, 2, 3, 4}, 0.75); got != 4 {
		t.Errorf("p75 of 4 values = %v, want 4", got)
	}
	if got := Percentile([]float64{1, 2, 3, 4}, 1.0); got != 4 {
		t.Errorf("q=1.0 must clamp to the max, got %v", got)
	}
}
