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
	return &mcf.Salary{Minimum: min, Maximum: max, Type: typ}
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
			Metadata: mcf.Metadata{JobPostID: "MCF-" + uuid, NewPostingDate: date, ExpiryDate: "2026-12-31T00:00:00Z",
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
	// 3 backend jobs in week, 1 frontend in week, 1 backend previous week
	mk("b1", "Backend Engineer", "Backend", "2026-08-03T02:00:00Z", 3, salary(6000, 8000, "Monthly", false), false, 100, 5)
	mk("b2", "Backend Engineer", "Backend", "2026-08-04T02:00:00Z", 3, salary(6500, 8500, "Monthly", false), false, 120, 6)
	mk("b3", "Backend Engineer", "Backend", "2026-08-05T02:00:00Z", 0, nil, true, 90, 4) // hidden salary, no exp
	mk("f1", "Frontend Engineer", "Frontend", "2026-08-06T02:00:00Z", 3, salary(5500, 7500, "Monthly", false), false, 80, 3)
	mk("b0", "Backend Engineer", "Backend", "2026-07-27T02:00:00Z", 3, nil, false, 50, 2) // prev week
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
	if r.NewJobs != 4 {
		t.Errorf("new_jobs = %d, want 4", r.NewJobs)
	}
	if r.PrevNewJobs != 1 {
		t.Errorf("prev_new_jobs = %d, want 1", r.PrevNewJobs)
	}
	if r.ActiveJobs != 5 {
		t.Errorf("active_jobs = %d, want 5 (all still open)", r.ActiveJobs)
	}
	// salary median: b1 7000, b2 7500, f1 6500 -> sorted [6500 7000 7500] -> median 7000
	if int(r.SalaryMedian) != 7000 {
		t.Errorf("salary median = %.0f, want 7000", r.SalaryMedian)
	}
	// no_exp_ratio: 1 of 4 has min_years_exp=0
	if r.NoExpRatio != 0.25 {
		t.Errorf("no_exp_ratio = %v, want 0.25", r.NoExpRatio)
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
	for _, sec := range []string{"Executive Snapshot", "Hiring Trends", "Tech Trends", "Compensation", "Demand Signals", "Skills-first", "Insights", "Data Quality"} {
		if !strings.Contains(html, sec) {
			t.Errorf("HTML missing section %q", sec)
		}
	}
	if !strings.Contains(html, "2026-W32") {
		t.Error("HTML missing week label")
	}
	md, err := RenderMarkdown(r)
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if !strings.Contains(md, "# Singapore SWE Hiring Report") {
		t.Error("markdown missing title")
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
