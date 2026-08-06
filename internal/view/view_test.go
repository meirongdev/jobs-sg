package view

import (
	"strings"
	"testing"

	"github.com/meirongdev/jobs-sg/internal/metric"
)

func TestBarHeightFollowsBarCount(t *testing.T) {
	// A fixed viewBox once clipped everything past the 11th bar while callers
	// asked for 15.
	kvs := make([]metric.KV, 15)
	for i := range kvs {
		kvs[i] = metric.KV{Key: "t", Value: float64(i + 1)}
	}
	svg := string(Bar(kvs, 15))
	if !strings.Contains(svg, "viewBox=\"0 0 520 430\"") {
		t.Errorf("15 bars must size the viewBox to 10+15*28=430, got:\n%s", svg)
	}
}

func TestColumnIgnoresALoneOutlierWhenScaling(t *testing.T) {
	// The first-run baseline scan stores the whole live market on one day; with
	// a true-max axis every ordinary day renders as a 1px stub.
	kvs := []metric.KV{{Key: "01", Value: 86000}, {Key: "02", Value: 100}, {Key: "03", Value: 90}}
	svg := string(Column(kvs, "new postings"))
	if !strings.Contains(svg, ">86000<") {
		t.Errorf("the clipped outlier must still print its real value:\n%s", svg)
	}
}

func TestSuppressedNeverRendersZero(t *testing.T) {
	sample := Suppressed(metric.SampleCoverage(3, metric.MinSalarySamplesPerCell))
	if got := string(sample); !strings.Contains(got, "n=3") || strings.Contains(got, "0") {
		t.Errorf("sample suppression = %q, want an n=3 marker and no zero", got)
	}
	hist := Suppressed(metric.HistoryCoverage(2, metric.MinWeeksForMomentum))
	for _, want := range []string{"2", "5"} {
		if !strings.Contains(string(hist), want) {
			t.Errorf("history suppression %q must state available/required weeks", hist)
		}
	}
}

func TestSuppressedIsEmptyWhenNotSuppressed(t *testing.T) {
	if got := Suppressed(metric.SampleCoverage(50, 5)); got != "" {
		t.Errorf("unsuppressed coverage = %q, want empty", got)
	}
}

// Migrated from internal/report/daily_test.go when chartScale moved here: the
// first-run baseline stores the whole live market in one day; without outlier
// handling every later day renders as a 1px stub.
func TestChartScaleIgnoresBaselineOutlier(t *testing.T) {
	cases := []struct {
		name string
		vals []float64
		want float64
	}{
		{"baseline day dwarfs the rest", []float64{6666, 40, 38, 45, 41}, 45},
		{"ordinary spread keeps true max", []float64{40, 38, 45, 41}, 45},
		{"2x is not an outlier", []float64{80, 40, 38}, 80},
		{"all zero", []float64{0, 0}, 1},
		{"single point", []float64{12}, 12},
	}
	for _, c := range cases {
		kvs := make([]metric.KV, len(c.vals))
		for i, v := range c.vals {
			kvs[i] = metric.KV{Key: "d", Value: v}
		}
		if got := chartScale(kvs); got != c.want {
			t.Errorf("%s: chartScale = %v, want %v", c.name, got, c.want)
		}
	}

	// the clipped column still reports its real value on the page
	svg := string(Column([]metric.KV{{Key: "08-01", Value: 6666}, {Key: "08-02", Value: 40}}, "new SWE"))
	if !strings.Contains(svg, ">6666<") {
		t.Errorf("clipped column must print its value:\n%s", svg)
	}
	if !strings.Contains(svg, `fill="#7c3aed"`) {
		t.Errorf("clipped column must be visually distinct:\n%s", svg)
	}
	if strings.Contains(svg, `height="0"`) {
		t.Errorf("ordinary column collapsed to zero height:\n%s", svg)
	}
}

func TestSignedPctIsARelativePercentNotPP(t *testing.T) {
	if got := SignedPct(0.035); got != "+3.5%" {
		t.Errorf("SignedPct(0.035) = %q, want +3.5%%", got)
	}
	if got := SignedPct(-0.043); got != "-4.3%" {
		t.Errorf("SignedPct(-0.043) = %q, want -4.3%%", got)
	}
}

func TestMomentumGatedMessageNamesTheBarNotAFlatMarket(t *testing.T) {
	// History is complete but every tech is sample-suppressed: the page must
	// name the gate, never claim the market was flat.
	r := &metric.TechReport{
		Week: "2026-W32", Denom: 40, MomentumFloor: 10,
		Ranked: []metric.TechStat{{Slug: "go", Count: 9, Share: 0.2,
			Momentum: metric.SampleCoverage(9, 10)}},
	}
	html, err := TechPage(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "Nothing rose this week") {
		t.Error("gated boards must not claim the market was flat")
	}
	for _, want := range []string{"momentum bar", "10+ postings"} {
		if !strings.Contains(html, want) {
			t.Errorf("gated message missing %q", want)
		}
	}
}
