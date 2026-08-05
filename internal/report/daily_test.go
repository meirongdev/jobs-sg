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
)

// Daily fixtures are written with explicit timestamps (UpsertJob/StartRun
// stamp time.Now), because the whole point of these tests is SGT bucketing:
// the 02:15 SGT ingest is stored as 18:15 UTC the day before.
func openDailyDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "jobs.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

func addRun(t *testing.T, db *store.DB, kind, status, started, ended string, pages, seen, newJobs, updated, closed, llmCalls, llmCached, errs int) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO ingest_run(kind, started_at, ended_at, pages_fetched, jobs_seen, jobs_new,
		  jobs_updated, jobs_closed, llm_calls, llm_cached, errors, watermark, status)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		kind, started, ended, pages, seen, newJobs, updated, closed, llmCalls, llmCached, errs,
		"2026-08-04T00:00:00Z", status)
	if err != nil {
		t.Fatal(err)
	}
}

// addJob stores one candidate job and back-dates first_seen_at (and optionally
// closed_at) to the given UTC instants.
func addJob(t *testing.T, db *store.DB, uuid, title, ssoc, category, firstSeen, closedAt string) classify.Result {
	t.Helper()
	ctx := context.Background()
	cl := classify.New(map[string]string{"25121": "Backend", "25131": "Frontend"})
	j := mcf.Job{
		UUID: uuid, Title: title, Description: "desc",
		Metadata:   mcf.Metadata{JobPostID: "MCF-" + uuid, NewPostingDate: "2026-08-03"},
		SSOCCode:   ssoc,
		Categories: []mcf.Category{{Category: category}},
		PostedCompany: &mcf.PostedCompany{
			UEN: "UEN" + uuid, Name: "Company " + uuid, SSICCode: "62011", EmployeeCount: intP(50),
		},
	}
	res := cl.Classify(j)
	if !res.IsCandidate {
		t.Fatalf("fixture %q is not a candidate; test intent broken", title)
	}
	if _, err := db.UpsertJob(ctx, j, res, "raw/2026-08-03/000.jsonl.gz#0"); err != nil {
		t.Fatal(err)
	}
	var closed any
	if closedAt != "" {
		closed = closedAt
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE job SET first_seen_at=?, closed_at=? WHERE uuid=?`, firstSeen, closed, uuid); err != nil {
		t.Fatal(err)
	}
	return res
}

// seedDaily lays out three SGT days: 08-03 idle, 08-04 busy (ingest+enrich),
// 08-05 a clean ingest.
func seedDaily(t *testing.T, db *store.DB) {
	t.Helper()
	// 02:15 SGT 08-04 = 18:15 UTC 08-03
	addRun(t, db, store.RunIncremental, store.StatusSuccess,
		"2026-08-03T18:15:00Z", "2026-08-03T18:19:00Z", 12, 1200, 3, 5, 0, 0, 0, 0)
	// 03:10 SGT 08-04 = 19:10 UTC 08-03, degraded
	addRun(t, db, store.RunEnrich, store.StatusPartial,
		"2026-08-03T19:10:00Z", "2026-08-03T19:20:00Z", 0, 0, 0, 0, 0, 20, 5, 2)
	// 02:15 SGT 08-05
	addRun(t, db, store.RunIncremental, store.StatusSuccess,
		"2026-08-04T18:15:00Z", "2026-08-04T18:22:00Z", 9, 900, 1, 2, 0, 0, 0, 0)

	// two SWE + one non-SWE candidate crawled on SGT 08-04
	addJob(t, db, "s1", "Backend Engineer", "25121", "Information Technology", "2026-08-03T18:30:00Z", "")
	addJob(t, db, "s2", "Frontend Engineer", "25131", "Information Technology", "2026-08-03T20:00:00Z", "")
	if r := addJob(t, db, "o1", "IT Support Specialist", "", "Information Technology", "2026-08-03T18:31:00Z", ""); r.IsSWE {
		t.Fatal("fixture o1 should be a non-SWE candidate")
	}
	// crawled on SGT 08-05
	addJob(t, db, "s3", "Backend Engineer", "25121", "Information Technology", "2026-08-04T18:40:00Z", "")
	// first seen earlier, closed on SGT 08-04
	addJob(t, db, "c1", "Backend Engineer", "25121", "Information Technology", "2026-07-20T02:00:00Z", "2026-08-03T18:45:00Z")
}

func dayByDate(t *testing.T, o *DailyOverview, date string) DayRow {
	t.Helper()
	for _, d := range o.Days {
		if d.Date == date {
			return d
		}
	}
	t.Fatalf("day %s missing from overview (%d days)", date, len(o.Days))
	return DayRow{}
}

// now = 2026-08-05 18:00 SGT
var dailyNow = time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)

func TestDayBoundsSGT(t *testing.T) {
	start, end := DayBounds(time.Date(2026, 8, 4, 15, 30, 0, 0, sgt))
	if !start.Equal(time.Date(2026, 8, 3, 16, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %v, want 2026-08-03T16:00:00Z", start)
	}
	if !end.Equal(time.Date(2026, 8, 4, 16, 0, 0, 0, time.UTC)) {
		t.Errorf("end = %v, want 2026-08-04T16:00:00Z", end)
	}
}

func TestDailyOverviewBucketsRunsBySGTDay(t *testing.T) {
	db := openDailyDB(t)
	seedDaily(t, db)

	o, err := ComputeDailyOverview(context.Background(), db, dailyNow, 3)
	if err != nil {
		t.Fatalf("ComputeDailyOverview: %v", err)
	}
	if len(o.Days) != 3 {
		t.Fatalf("days = %d, want 3", len(o.Days))
	}
	if o.Days[0].Date != "2026-08-05" {
		t.Errorf("first row = %s, want newest day 2026-08-05", o.Days[0].Date)
	}

	// the 18:15 UTC ingest belongs to the next SGT day, not the UTC one
	d4 := dayByDate(t, o, "2026-08-04")
	if d4.Pages != 12 || d4.Archived != 1200 || d4.Updated != 5 {
		t.Errorf("08-04 ingest counters = pages %d archived %d updated %d, want 12/1200/5",
			d4.Pages, d4.Archived, d4.Updated)
	}
	if d4.Duration != 4*time.Minute {
		t.Errorf("08-04 ingest duration = %v, want 4m (enrich time excluded)", d4.Duration)
	}
	if d4.Status != store.StatusPartial {
		t.Errorf("08-04 status = %q, want partial (worst of success+partial)", d4.Status)
	}
	if d4.LLMCalls != 20 || d4.LLMCached != 5 || d4.Errors != 2 {
		t.Errorf("08-04 enrich counters = %d/%d/%d, want 20/5/2", d4.LLMCalls, d4.LLMCached, d4.Errors)
	}
	if len(d4.Kinds) != 2 {
		t.Errorf("08-04 kinds = %v, want incremental+enrich", d4.Kinds)
	}
	if d4.New != 3 || d4.NewSWE != 2 {
		t.Errorf("08-04 stored = %d new / %d SWE, want 3/2", d4.New, d4.NewSWE)
	}
	if d4.Closed != 1 {
		t.Errorf("08-04 closed = %d, want 1", d4.Closed)
	}

	d3 := dayByDate(t, o, "2026-08-03")
	if !d3.Idle() || d3.Status != "" {
		t.Errorf("08-03 should be an idle gap, got status %q", d3.Status)
	}

	d5 := dayByDate(t, o, "2026-08-05")
	if d5.Archived != 900 || d5.NewSWE != 1 {
		t.Errorf("08-05 = archived %d, SWE %d, want 900/1", d5.Archived, d5.NewSWE)
	}
}

func TestDailyOverviewHeadlines(t *testing.T) {
	db := openDailyDB(t)
	seedDaily(t, db)

	o, err := ComputeDailyOverview(context.Background(), db, dailyNow, 30)
	if err != nil {
		t.Fatalf("ComputeDailyOverview: %v", err)
	}
	if o.NewSWE7d != 3 {
		t.Errorf("NewSWE7d = %d, want 3 (s1,s2 on 08-04 + s3 on 08-05)", o.NewSWE7d)
	}
	if o.Archived7d != 2100 {
		t.Errorf("Archived7d = %d, want 2100", o.Archived7d)
	}
	if o.ActiveJobs != 4 {
		t.Errorf("ActiveJobs = %d, want 4 (c1 is closed)", o.ActiveJobs)
	}
	if o.LastRun == nil || !o.LastRun.StartedAt.Equal(time.Date(2026, 8, 4, 18, 15, 0, 0, time.UTC)) {
		t.Errorf("LastRun = %+v, want the 2026-08-04T18:15Z ingest", o.LastRun)
	}
	if o.Trend[len(o.Trend)-1].Key != "08-05" || o.Trend[len(o.Trend)-1].Value != 1 {
		t.Errorf("trend must run oldest→newest, last = %+v", o.Trend[len(o.Trend)-1])
	}
	if len(o.Trend) != len(o.Days) {
		t.Errorf("trend has %d points for %d days", len(o.Trend), len(o.Days))
	}
}

func TestHeadlinesAndLastRunIgnoreTheWindow(t *testing.T) {
	db := openDailyDB(t)
	seedDaily(t, db)

	// a 1-day window must still report a true 7-day headline...
	narrow, err := ComputeDailyOverview(context.Background(), db, dailyNow, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(narrow.Days) != 1 {
		t.Fatalf("days = %d, want 1", len(narrow.Days))
	}
	if narrow.NewSWE7d != 3 {
		t.Errorf("NewSWE7d = %d under ?days=1, want 3 (the card is labelled 7d)", narrow.NewSWE7d)
	}
	if narrow.Archived7d != 2100 {
		t.Errorf("Archived7d = %d under ?days=1, want 2100", narrow.Archived7d)
	}
	if narrow.DaysLabel() != "1 day" {
		t.Errorf("DaysLabel = %q, want \"1 day\"", narrow.DaysLabel())
	}

	// ...and a window that predates every run must still name the last one
	stale := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC) // ~8 weeks of silence
	o, err := ComputeDailyOverview(context.Background(), db, stale, 7)
	if err != nil {
		t.Fatal(err)
	}
	if o.LastRun == nil {
		t.Fatal("LastRun must survive a window with no runs in it")
	}
	if !o.LastRun.StartedAt.Equal(time.Date(2026, 8, 4, 18, 15, 0, 0, time.UTC)) {
		t.Errorf("LastRun = %v, want the 2026-08-04T18:15Z ingest", o.LastRun.StartedAt)
	}
}

func TestDailyOverviewTrimsLeadingEmptyDays(t *testing.T) {
	db := openDailyDB(t)
	seedDaily(t, db)

	// the 30-day window opens on 07-07, but the oldest activity is c1's
	// 07-20 first_seen: everything before that is noise on a young install
	o, err := ComputeDailyOverview(context.Background(), db, dailyNow, 30)
	if err != nil {
		t.Fatal(err)
	}
	oldest := o.Days[len(o.Days)-1]
	if oldest.Date != "2026-07-20" {
		t.Errorf("oldest day = %s, want 2026-07-20 (first activity)", oldest.Date)
	}
	if o.From != "2026-07-20" {
		t.Errorf("From = %s, must match the oldest rendered day", o.From)
	}
	// gaps *between* active days stay visible — those are missed runs
	if d := dayByDate(t, o, "2026-08-03"); !d.Idle() {
		t.Error("08-03 gap between active days must be kept")
	}
	// an empty DB still renders one day rather than collapsing to nothing
	empty := openDailyDB(t)
	eo, err := ComputeDailyOverview(context.Background(), empty, dailyNow, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(eo.Days) != 1 || eo.Days[0].Date != "2026-08-05" {
		t.Errorf("empty DB days = %+v, want just today", eo.Days)
	}
}

func TestDayDetailListsRunsAndPostings(t *testing.T) {
	db := openDailyDB(t)
	seedDaily(t, db)
	ctx := context.Background()
	if err := db.WriteRuleTech(ctx, "s1", []store.TechRow{{Slug: "go", Kind: "language"}}); err != nil {
		t.Fatal(err)
	}

	day := time.Date(2026, 8, 4, 0, 0, 0, 0, sgt)
	d, err := ComputeDayDetail(ctx, db, day, dailyNow, 0)
	if err != nil {
		t.Fatalf("ComputeDayDetail: %v", err)
	}
	if len(d.Runs) != 2 {
		t.Fatalf("runs = %d, want 2 (ingest + enrich)", len(d.Runs))
	}
	if d.Runs[0].Kind != store.RunEnrich {
		t.Errorf("runs must be newest first, got %s", d.Runs[0].Kind)
	}
	if d.Runs[0].Duration != 10*time.Minute || d.Runs[0].DurationLabel() != "10m00s" {
		t.Errorf("enrich duration = %v (%s), want 10m", d.Runs[0].Duration, d.Runs[0].DurationLabel())
	}
	if d.Summary.NewSWE != 2 || d.Summary.Closed != 1 {
		t.Errorf("summary = %d SWE / %d closed, want 2/1", d.Summary.NewSWE, d.Summary.Closed)
	}
	if d.JobsTotal != 3 || len(d.Jobs) != 3 || d.Truncated {
		t.Errorf("jobs = %d of %d (truncated=%v), want 3/3/false", len(d.Jobs), d.JobsTotal, d.Truncated)
	}
	if !d.Jobs[0].IsSWE {
		t.Error("SWE postings must sort first")
	}
	if d.Jobs[0].FirstSeen != "02:30" {
		t.Errorf("first seen = %s, want 02:30 SGT", d.Jobs[0].FirstSeen)
	}
	if len(d.Techs) != 1 || d.Techs[0].Key != "go" {
		t.Errorf("techs = %+v, want [go]", d.Techs)
	}
	if d.Prev != "2026-08-03" || d.Next != "2026-08-05" {
		t.Errorf("pager = prev %s next %s, want 2026-08-03 / 2026-08-05", d.Prev, d.Next)
	}

	// today has no next page
	today, err := ComputeDayDetail(ctx, db, dailyNow, dailyNow, 0)
	if err != nil {
		t.Fatal(err)
	}
	if today.Next != "" {
		t.Errorf("today.Next = %q, want empty", today.Next)
	}
}

func TestRunDurationLabels(t *testing.T) {
	start := time.Date(2026, 8, 3, 18, 15, 0, 0, time.UTC)
	cases := []struct {
		name string
		run  RunRow
		want string
	}{
		{"still going", RunRow{StartedAt: start}, "running"},
		{"sub-second", RunRow{StartedAt: start, EndedAt: start}, "<1s"},
		{"minutes", RunRow{StartedAt: start, EndedAt: start.Add(4 * time.Minute), Duration: 4 * time.Minute}, "4m00s"},
		{"hours", RunRow{StartedAt: start, EndedAt: start.Add(90 * time.Minute), Duration: 90 * time.Minute}, "1h30m"},
	}
	for _, c := range cases {
		if got := c.run.DurationLabel(); got != c.want {
			t.Errorf("%s: DurationLabel() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDayDetailTruncatesLongList(t *testing.T) {
	db := openDailyDB(t)
	seedDaily(t, db)
	d, err := ComputeDayDetail(context.Background(), db, time.Date(2026, 8, 4, 0, 0, 0, 0, sgt), dailyNow, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Jobs) != 2 || d.JobsTotal != 3 || !d.Truncated {
		t.Errorf("jobs = %d of %d (truncated=%v), want 2/3/true", len(d.Jobs), d.JobsTotal, d.Truncated)
	}
}

// The first-run baseline stores the whole live market in one day; without
// outlier handling every later day renders as a 1px stub.
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
		kvs := make([]KV, len(c.vals))
		for i, v := range c.vals {
			kvs[i] = KV{Key: "d", Value: v}
		}
		if got := chartScale(kvs); got != c.want {
			t.Errorf("%s: chartScale = %v, want %v", c.name, got, c.want)
		}
	}

	// the clipped column still reports its real value on the page
	svg := string(columnSVG([]KV{{Key: "08-01", Value: 6666}, {Key: "08-02", Value: 40}}, "new SWE"))
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

func TestDailyEmptyDBRenders(t *testing.T) {
	db := openDailyDB(t)
	o, err := ComputeDailyOverview(context.Background(), db, dailyNow, 7)
	if err != nil {
		t.Fatalf("ComputeDailyOverview on empty DB: %v", err)
	}
	if o.LastRun != nil {
		t.Errorf("LastRun = %+v, want nil", o.LastRun)
	}
	html, err := RenderDailyOverviewHTML(o)
	if err != nil {
		t.Fatalf("RenderDailyOverviewHTML: %v", err)
	}
	if !strings.Contains(html, "no runs recorded") {
		t.Error("empty overview should say there were no runs")
	}
	d, err := ComputeDayDetail(context.Background(), db, dailyNow, dailyNow, 0)
	if err != nil {
		t.Fatalf("ComputeDayDetail on empty DB: %v", err)
	}
	if _, err := RenderDayDetailHTML(d); err != nil {
		t.Fatalf("RenderDayDetailHTML: %v", err)
	}
}

func TestRenderDailyPagesAreSelfContained(t *testing.T) {
	db := openDailyDB(t)
	seedDaily(t, db)
	ctx := context.Background()

	o, err := ComputeDailyOverview(ctx, db, dailyNow, 30)
	if err != nil {
		t.Fatal(err)
	}
	overview, err := RenderDailyOverviewHTML(o)
	if err != nil {
		t.Fatalf("RenderDailyOverviewHTML: %v", err)
	}
	for _, want := range []string{
		"Daily Crawl Statistics",
		`href="/daily/2026-08-04"`, // day rows drill down
		`href="/"`,                 // nav back to the weekly report
		"Daily crawl detail",
		"New SWE postings per day",
		`class="pill s-partial"`, // degraded enrich surfaces on the day row
		`class="pill s-idle"`,    // idle days are visible gaps
	} {
		if !strings.Contains(overview, want) {
			t.Errorf("overview HTML missing %q", want)
		}
	}

	d, err := ComputeDayDetail(ctx, db, time.Date(2026, 8, 4, 0, 0, 0, 0, sgt), dailyNow, 0)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := RenderDayDetailHTML(d)
	if err != nil {
		t.Fatalf("RenderDayDetailHTML: %v", err)
	}
	for _, want := range []string{
		"Crawl Detail — 2026-08-04",
		"Backend Engineer",
		"Postings first seen this day",
		`href="/daily/2026-08-03"`,
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail HTML missing %q", want)
		}
	}

	// docs/02 §4.3: no external resources on any rendered page (the SVG
	// namespace URI is a declaration, not a fetch, so it does not count)
	for name, page := range map[string]string{"overview": overview, "detail": detail} {
		stripped := strings.ReplaceAll(page, "http://www.w3.org/2000/svg", "")
		for _, bad := range []string{"http://", "https://", "<script", "<link"} {
			if strings.Contains(stripped, bad) {
				t.Errorf("%s page references %q; pages must be self-contained", name, bad)
			}
		}
	}
}
