package report

import (
	"strings"
	"testing"

	"github.com/meirongdev/jobs-sg/internal/metric"
)

func sampleReport() *Report {
	return &Report{
		WeekLabel:   "2026-W32",
		NewJobs:     120,
		PrevNewJobs: 100,
		Tech: &metric.TechReport{
			Rising: []metric.TechStat{
				{Slug: "rust", MomentumPP: 0.021},
				{Slug: "go", MomentumPP: 0.014},
				{Slug: "kubernetes", MomentumPP: 0.009},
				{Slug: "terraform", MomentumPP: 0.004},
			},
		},
		Market: &metric.MarketReport{NewJobs: 120, EntryJobs: 37, ActiveEntry: 210},
		Pay: &metric.PayReport{Ladder: []metric.PayBand{
			{Label: "0", P50: 4200},
			{Label: "1-2", P50: 5000},
			{Label: "3-5", P50: 7000},
			{Label: "6+", P50: 0, Coverage: metric.SampleCoverage(2, 5)},
		}},
	}
}

// The push leads with what changed and what a reader can act on, not with
// pipeline counts (spec §4.5).
func TestTelegramSummarySpeaksToJobSeekers(t *testing.T) {
	got := TelegramSummary(sampleReport(), "https://jobs.meirong.dev/")

	for _, want := range []string{
		"2026-W32",
		"120 new postings",
		"+20%",           // week-over-week
		"rust",           // rising technology
		"37 entry-level", // absolute count, not a share
		"210 on the board now",
		"3-5 S$7000",      // pay by band
		"lags the market", // freshness line
		"/w/2026-W32",     // link to the archive
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
	// Only the top three rising technologies; a Telegram push is not a table.
	if strings.Contains(got, "terraform") {
		t.Errorf("summary listed a fourth rising technology:\n%s", got)
	}
	// A withheld band must not appear as S$0.
	if strings.Contains(got, "S$0") {
		t.Errorf("summary printed a suppressed band as zero:\n%s", got)
	}
}

// Momentum without enough history is not reported at all, rather than as an
// empty or flat board.
func TestTelegramSummaryOmitsMomentumWithoutHistory(t *testing.T) {
	r := sampleReport()
	r.Tech.History = metric.HistoryCoverage(2, metric.MinWeeksForMomentum)
	got := TelegramSummary(r, "https://jobs.meirong.dev")
	if strings.Contains(got, "Heating up") || strings.Contains(got, "rust") {
		t.Errorf("summary reported momentum it does not have:\n%s", got)
	}
	// the rest still goes out
	if !strings.Contains(got, "37 entry-level") {
		t.Errorf("summary dropped the entry-level line:\n%s", got)
	}
}

// A first-ever report has no previous week: n from 0 is not a percentage.
func TestTelegramSummaryOmitsWoWWithoutAPreviousWeek(t *testing.T) {
	r := sampleReport()
	r.PrevNewJobs = 0
	got := TelegramSummary(r, "https://jobs.meirong.dev")
	if strings.Contains(got, "vs last week") {
		t.Errorf("summary claimed a week-over-week move with no baseline:\n%s", got)
	}
}

// The model is populated section by section, so a report generated before a
// section exists must not take the push down with it.
func TestTelegramSummarySurvivesMissingSections(t *testing.T) {
	got := TelegramSummary(&Report{WeekLabel: "2026-W32", NewJobs: 5}, "https://jobs.meirong.dev")
	if !strings.Contains(got, "2026-W32") || !strings.Contains(got, "5 new postings") {
		t.Errorf("summary lost its headline:\n%s", got)
	}
}
