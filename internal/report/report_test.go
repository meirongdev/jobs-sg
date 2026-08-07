package report

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meirongdev/jobs-sg/internal/classify"
	"github.com/meirongdev/jobs-sg/internal/mcf"
	"github.com/meirongdev/jobs-sg/internal/store"
	"github.com/meirongdev/jobs-sg/internal/tech"
)

func salary(min, max float64, typ string, hidden bool) *mcf.Salary {
	return &mcf.Salary{Minimum: min, Maximum: max, Type: mcf.SalaryType{SalaryType: typ}}
}

func seedReportData(t *testing.T, db *store.DB) {
	ctx := context.Background()
	cl := classify.New(map[string]string{"25121": "Backend", "25131": "Frontend"})
	mk := func(uuid, title, fam, date string, minY int, sal *mcf.Salary, hidden bool, view, app int) {
		ssoc := "25121"
		if fam == "Frontend" {
			ssoc = "25131"
		}
		j := mcf.Job{
			UUID: uuid, Title: title, Description: "desc",
			Metadata: mcf.Metadata{JobPostID: "MCF-" + uuid, NewPostingDate: date, ExpiryDate: "2026-12-31",
				TotalNumberOfView: view, TotalNumberJobApplication: app, IsHideSalary: hidden},
			SSOCCode: ssoc, Categories: []mcf.Category{{Category: "Information Technology"}},
			MinimumYearsExperience: intP(minY), Salary: sal, NumberOfVacancies: intP(3),
			PostedCompany: &mcf.PostedCompany{UEN: "UEN" + uuid, Name: "Company " + fam, SSICCode: "62011", EmployeeCount: intP(500)},
		}
		if _, err := db.UpsertJob(ctx, j, cl.Classify(j), "raw/2026-08-03/000.jsonl.gz#0"); err != nil {
			t.Fatal(err)
		}
	}
	// week of 2026-08-03 (Monday) in SGT -> UTC range [2026-08-02T16:00Z, 2026-08-09T16:00Z)
	// 3 backend jobs in week, 1 frontend in week, 1 backend previous week.
	// Dates are date-only — the live API format (testdata/live).
	mk("b1", "Backend Engineer", "Backend", "2026-08-03", 3, salary(6000, 8000, "Monthly", false), false, 100, 5)
	mk("b2", "Backend Engineer", "Backend", "2026-08-04", 3, salary(6500, 8500, "Monthly", false), false, 120, 6)
	mk("b3", "Backend Engineer", "Backend", "2026-08-05", 0, nil, true, 90, 4) // hidden salary, no exp
	mk("f1", "Frontend Engineer", "Frontend", "2026-08-06", 3, salary(5500, 7500, "Monthly", false), false, 80, 3)
	// Two more advertising a monthly range, so the sample clears
	// metric.MinSalarySamplesPerCell and the median is actually published
	// rather than withheld — the suppression path has its own test in
	// internal/metric.
	mk("b4", "Backend Engineer", "Backend", "2026-08-06", 5, salary(6000, 10000, "Monthly", false), false, 70, 2)
	mk("f2", "Frontend Engineer", "Frontend", "2026-08-07", 2, salary(5000, 7000, "Monthly", false), false, 60, 1)
	mk("b0", "Backend Engineer", "Backend", "2026-07-27", 3, nil, false, 50, 2) // prev week
}

func intP(i int) *int { return &i }

func TestComputeMetricsAndRender(t *testing.T) {
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
	seedReportData(t, db)

	// add tech to b1 (tech_freq)
	rows, _ := db.LoadTechTaxonomy(ctx)
	tax := tech.LoadTaxonomy(rows)
	techs := tax.Extract("Backend Engineer golang kubernetes")
	techRows := make([]store.TechRow, len(techs))
	for i, t := range techs {
		techRows[i] = store.TechRow{Slug: t.Slug, Kind: t.Kind}
	}
	if err := db.WriteRuleTech(ctx, "b1", techRows); err != nil {
		t.Fatal(err)
	}

	monday := time.Date(2026, 8, 3, 0, 0, 0, 0, time.FixedZone("SGT", 8*3600))
	r, err := ComputeMetrics(ctx, db, monday)
	if err != nil {
		t.Fatalf("ComputeMetrics: %v", err)
	}
	if r.WeekLabel != "2026-W32" {
		t.Errorf("week label = %s, want 2026-W32", r.WeekLabel)
	}
	if r.NewJobs != 6 {
		t.Errorf("new_jobs = %d, want 6", r.NewJobs)
	}
	if r.PrevNewJobs != 1 {
		t.Errorf("prev_new_jobs = %d, want 1", r.PrevNewJobs)
	}
	if r.ActiveJobs != 7 {
		t.Errorf("active_jobs = %d, want 7 (all still open)", r.ActiveJobs)
	}
	// midpoints 7000/7500/6500/8000/6000 -> sorted [6000 6500 7000 7500 8000]
	// -> upper median 7000, the same nearest-rank convention /pay prints
	if int(r.SalaryMedian) != 7000 {
		t.Errorf("salary median = %.0f, want 7000", r.SalaryMedian)
	}
	// Entry-level counts replace no_exp_ratio, which folded "did not say" in
	// with "no experience required" (spec §3.7-1). They are counts, and a
	// subset of the week's new postings.
	if r.Market == nil || r.Market.EntryJobs > r.NewJobs {
		t.Errorf("entry-level %v is not a subset of %d new postings", r.Market, r.NewJobs)
	}
	// The sections now come from the metric layer, so the report and the live
	// pages cannot disagree about what a week contained.
	if r.Market.NewJobs != r.NewJobs {
		t.Errorf("metric layer counts %d new postings, report counts %d", r.Market.NewJobs, r.NewJobs)
	}
	if r.Tech == nil || r.Tech.Week != r.WeekLabel {
		t.Errorf("tech section reports week %v, want %s", r.Tech, r.WeekLabel)
	}
	// tech freq has kubernetes/golang from b1
	found := false
	for _, kv := range r.TopTechs {
		if kv.Key == "kubernetes" && int(kv.Value) == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("tech_freq missing kubernetes: %+v", r.TopTechs)
	}

	// weekly_metric materialised
	var rowsCount int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM weekly_metric WHERE week_start='2026-08-03'").Scan(&rowsCount); err != nil {
		t.Fatal(err)
	}
	if rowsCount == 0 {
		t.Error("weekly_metric not materialised")
	}

	// render both formats
	html, err := RenderHTML(r)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	// Section order follows spec §4.5: snapshot, technology, pay, entry, then
	// competition, employers, and a plain statement of method. Data Quality is
	// no longer a section — it is one line in the footer linking /ops.
	wantOrder := []string{
		"1. Snapshot", "2. Technology", "3. Pay", "4. Getting in",
		"5. Competition and listing length", "6. Employers", "7. About these numbers",
	}
	at := -1
	for _, sec := range wantOrder {
		i := strings.Index(html, sec)
		if i < 0 {
			t.Errorf("HTML missing section %q", sec)
			continue
		}
		if i < at {
			t.Errorf("section %q is out of order", sec)
		}
		at = i
	}
	if strings.Contains(html, "<h2>8.") {
		t.Error("Data Quality should be a footer line, not an eighth section")
	}
	if !strings.Contains(html, `href="/ops"`) {
		t.Error("the footer must link the collection history")
	}
	if !strings.Contains(html, "2026-W32") {
		t.Error("HTML missing week label")
	}
	// docs/01 §5 red line: an outward page must say the data is a daily batch,
	// or a reader treats a week-old snapshot as a live job board.
	if !strings.Contains(html, "lag the live market by up to 24h") {
		t.Error("weekly report does not disclose the data lag (docs/01 §5)")
	}
	md, err := RenderMarkdown(r)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if !strings.Contains(md, "# Singapore SWE Hiring Report") {
		t.Error("markdown missing title")
	}
}

// TestWeekWindowDateOnlyBoundaries pins the week-window comparison against
// the live date-only posting_date format at all four edges. The bounds are
// SGT midnights rendered as RFC3339 UTC (Sunday 16:00Z); comparing date-only
// strings against them is correct ONLY because SGT is UTC+8, so the bound's
// UTC calendar date is never an in-week SGT date. Guards anyone "simplifying"
// the bounds to UTC midnight — that would shift the window by a day.
func TestWeekWindowDateOnlyBoundaries(t *testing.T) {
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
	mk := func(uuid, date string) {
		j := mcf.Job{
			UUID: uuid, Title: "Backend Engineer", Description: "desc",
			Metadata: mcf.Metadata{JobPostID: "MCF-" + uuid, NewPostingDate: date, ExpiryDate: "2026-12-31"},
			SSOCCode: "25121", Categories: []mcf.Category{{Category: "Information Technology"}},
		}
		if _, err := db.UpsertJob(ctx, j, cl.Classify(j), "raw/2026-08-03/000.jsonl.gz#0"); err != nil {
			t.Fatal(err)
		}
	}
	mk("prev-sun", "2026-08-02") // last day of previous week
	mk("mon", "2026-08-03")      // first day of this week
	mk("sun", "2026-08-09")      // last day of this week
	mk("next-mon", "2026-08-10") // first day of next week

	monday := time.Date(2026, 8, 3, 0, 0, 0, 0, time.FixedZone("SGT", 8*3600))
	r, err := ComputeMetrics(ctx, db, monday)
	if err != nil {
		t.Fatalf("ComputeMetrics: %v", err)
	}
	if r.NewJobs != 2 {
		t.Errorf("new_jobs = %d, want 2 (mon + sun; boundary days must stay in their week)", r.NewJobs)
	}
	if r.PrevNewJobs != 1 {
		t.Errorf("prev_new_jobs = %d, want 1 (prev-sun)", r.PrevNewJobs)
	}
}

func TestWeekBounds(t *testing.T) {
	monday := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	start, end := WeekBounds(monday)
	if !start.Equal(time.Date(2026, 8, 2, 16, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %v, want 2026-08-02T16:00:00Z", start)
	}
	if !end.Equal(time.Date(2026, 8, 9, 16, 0, 0, 0, time.UTC)) {
		t.Errorf("end = %v, want 2026-08-09T16:00:00Z", end)
	}
}

func TestTelegramDisabledIsNoop(t *testing.T) {
	tg := &Telegram{}
	if err := tg.SendSummary(context.Background(), "x"); err != nil {
		t.Fatalf("disabled telegram should be no-op, got %v", err)
	}
}
