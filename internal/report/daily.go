package report

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/meirongdev/jobs-sg/internal/store"
)

// Daily pages answer "what did the crawler actually do today", so every bucket
// is an SGT calendar day: ingest runs at 02:15 SGT, which is 18:15 UTC the day
// before — bucketing by UTC date would file each run under the previous day.
// Timestamps are stored as RFC3339 UTC (store.NowUTC), so SQL grouping uses
// date(col,'+8 hours') and Go grouping uses .In(sgt).

// DefaultWindowDays is how many SGT days the overview page covers.
const DefaultWindowDays = 30

// DayJobLimit caps the per-day posting list so one busy day cannot blow the
// 64Mi web budget.
const DayJobLimit = 200

// RunRow is one ingest_run row projected for the daily pages.
type RunRow struct {
	ID        int64
	Kind      string
	Status    string
	StartedAt time.Time
	EndedAt   time.Time // zero while the run is still going
	Duration  time.Duration
	Pages     int
	Seen      int
	New       int
	Updated   int
	Closed    int
	LLMCalls  int
	LLMCached int
	Errors    int
	Watermark string
}

// Running reports whether the run never recorded an ended_at.
func (r RunRow) Running() bool { return r.EndedAt.IsZero() }

// DurationLabel distinguishes a run that is still going from one that finished
// inside a second (report runs routinely do).
func (r RunRow) DurationLabel() string {
	switch {
	case r.Running():
		return "running"
	case r.Duration < time.Second:
		return "<1s"
	default:
		return humanDuration(r.Duration)
	}
}

// StartedSGT / EndedSGT render the run window in the report timezone.
func (r RunRow) StartedSGT() string { return r.StartedAt.In(sgt).Format("15:04:05") }
func (r RunRow) EndedSGT() string {
	if r.EndedAt.IsZero() {
		return "—"
	}
	return r.EndedAt.In(sgt).Format("15:04:05")
}

// DayRow aggregates one SGT day of pipeline activity: run counters from
// ingest_run, stored/closed counts from job.
type DayRow struct {
	Date      string // YYYY-MM-DD (SGT)
	Kinds     []string
	Status    string // worst status across the day's runs; "" when idle
	Duration  time.Duration
	Pages     int
	Archived  int // jobs_seen: every job archived, all categories
	Updated   int
	Errors    int
	LLMCalls  int
	LLMCached int
	New       int // candidate jobs first seen this day
	NewSWE    int
	Closed    int // jobs whose closed_at falls on this day
}

// Idle reports whether no run touched this day (a gap in the schedule).
func (d DayRow) Idle() bool { return d.Status == "" }

// DailyOverview is the model for the /daily index page.
type DailyOverview struct {
	Days          []DayRow // newest first
	Trend         []KV     // MM-DD -> new SWE jobs, oldest first
	Techs         []KV     // trailing-7-day tech frequency
	WindowDays    int
	From          string
	To            string
	Generated     string
	LastRun       *RunRow
	ActiveJobs    int
	NewSWE7d      int
	Archived7d    int
	EnrichBacklog int
	UnmappedTech  int
}

// DaysLabel describes how many days the page actually covers: the requested
// window minus any days before the pipeline's first activity.
func (o *DailyOverview) DaysLabel() string {
	if len(o.Days) == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", len(o.Days))
}

// JobRow is one newly crawled posting on the day page. Public corporate and
// posting fields only — no personal data (docs/01 §5).
type JobRow struct {
	Title       string
	Company     string
	RoleFamily  string
	Seniority   string
	Salary      string
	IsSWE       bool
	PostingDate string
	FirstSeen   string // HH:MM SGT
}

// DayDetail is the model for the /daily/{YYYY-MM-DD} drill-down page.
type DayDetail struct {
	Date        string
	Prev        string
	Next        string // "" when the day is today (no next page yet)
	Summary     DayRow
	Runs        []RunRow
	Roles       []KV
	Seniorities []KV
	Techs       []KV
	Jobs        []JobRow
	JobsTotal   int
	Truncated   bool
}

// DayBounds converts an SGT calendar day into its UTC [start,end) interval.
func DayBounds(day time.Time) (start, end time.Time) {
	d := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, sgt)
	return d.UTC(), d.AddDate(0, 0, 1).UTC()
}

// ParseDay parses a YYYY-MM-DD page parameter as an SGT calendar day.
func ParseDay(s string) (time.Time, error) { return time.ParseInLocation("2006-01-02", s, sgt) }

// ComputeDailyOverview builds the /daily model for the windowDays SGT days
// ending on the SGT day containing now. Read-only: the web pod opens the DB
// with mode=ro, so nothing here may write (docs/02 §4.4).
func ComputeDailyOverview(ctx context.Context, db *store.DB, now time.Time, windowDays int) (*DailyOverview, error) {
	if windowDays <= 0 {
		windowDays = DefaultWindowDays
	}
	today := now.In(sgt)
	_, end := DayBounds(today)
	start, _ := DayBounds(today.AddDate(0, 0, -(windowDays - 1)))

	o := &DailyOverview{
		WindowDays: windowDays,
		From:       start.In(sgt).Format("2006-01-02"),
		To:         today.Format("2006-01-02"),
		Generated:  now.In(sgt).Format("2006-01-02 15:04 SGT"),
	}

	runs, err := listRuns(ctx, db, start, end)
	if err != nil {
		return nil, err
	}
	byDay := map[string]*DayRow{}
	for i := range runs {
		r := runs[i]
		d := dayKey(r.StartedAt)
		row := byDay[d]
		if row == nil {
			row = &DayRow{Date: d}
			byDay[d] = row
		}
		applyRun(row, r)
	}
	// LastRun is looked up across all history, not just the window: when the
	// pipeline has been down longer than the window, "when did it last run"
	// is exactly the question the page has to answer.
	if o.LastRun, err = lastRun(ctx, db); err != nil {
		return nil, err
	}

	stored, err := storedPerDay(ctx, db, start, end)
	if err != nil {
		return nil, err
	}
	closed, err := closedPerDay(ctx, db, start, end)
	if err != nil {
		return nil, err
	}

	// walk the calendar so idle days show up as gaps instead of vanishing
	for i := 0; i < windowDays; i++ {
		d := today.AddDate(0, 0, -i).Format("2006-01-02")
		row := byDay[d]
		if row == nil {
			row = &DayRow{Date: d}
		}
		if s, ok := stored[d]; ok {
			row.New, row.NewSWE = s[0], s[1]
		}
		row.Closed = closed[d]
		o.Days = append(o.Days, *row)
	}
	// A gap between two active days is a signal worth seeing; days before the
	// pipeline ever ran are just noise on a young deployment. Trim against the
	// first activity in the whole DB, not the first in the window, so a quiet
	// stretch at the start of the window still shows as the gap it is.
	since, err := firstActivityDay(ctx, db)
	if err != nil {
		return nil, err
	}
	for i := len(o.Days) - 1; i > 0; i-- {
		if since != "" && o.Days[i].Date >= since {
			break
		}
		o.Days = o.Days[:i]
	}
	o.From = o.Days[len(o.Days)-1].Date
	for i := len(o.Days) - 1; i >= 0; i-- {
		d := o.Days[i]
		o.Trend = append(o.Trend, KV{Key: d.Date[5:], Value: float64(d.NewSWE)})
	}

	// The headline cards are always a trailing 7 SGT days, so they are queried
	// directly rather than summed over the rendered window — ?days=3 must not
	// silently relabel 3 days of data as a week.
	weekStart, _ := DayBounds(today.AddDate(0, 0, -6))
	wkArgs := []any{weekStart.Format(time.RFC3339), end.Format(time.RFC3339)}
	if err := db.QueryRowContext(ctx, `
		SELECT coalesce(sum(is_swe),0) FROM job
		WHERE first_seen_at >= ? AND first_seen_at < ?`, wkArgs...).Scan(&o.NewSWE7d); err != nil {
		return nil, err
	}
	if err := db.QueryRowContext(ctx, `
		SELECT coalesce(sum(jobs_seen),0) FROM ingest_run
		WHERE kind IN ('incremental','full_reconcile') AND started_at >= ? AND started_at < ?`,
		wkArgs...).Scan(&o.Archived7d); err != nil {
		return nil, err
	}
	if o.Techs, err = techFrequencyBySeen(ctx, db, weekStart, end, 15); err != nil {
		return nil, err
	}

	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM job WHERE closed_at IS NULL`).Scan(&o.ActiveJobs); err != nil {
		return nil, err
	}
	if n, err := db.EnrichBacklogCount(ctx); err == nil {
		o.EnrichBacklog = n
	}
	if n, err := db.UnmappedTechCount(ctx); err == nil {
		o.UnmappedTech = n
	}
	return o, nil
}

// ComputeDayDetail builds the /daily/{date} model for one SGT day. Read-only.
func ComputeDayDetail(ctx context.Context, db *store.DB, day time.Time, now time.Time, jobLimit int) (*DayDetail, error) {
	if jobLimit <= 0 {
		jobLimit = DayJobLimit
	}
	day = day.In(sgt)
	start, end := DayBounds(day)
	date := day.Format("2006-01-02")

	d := &DayDetail{
		Date:    date,
		Prev:    day.AddDate(0, 0, -1).Format("2006-01-02"),
		Summary: DayRow{Date: date},
	}
	if next := day.AddDate(0, 0, 1); !next.After(now.In(sgt)) {
		d.Next = next.Format("2006-01-02")
	}

	runs, err := listRuns(ctx, db, start, end)
	if err != nil {
		return nil, err
	}
	d.Runs = runs
	for _, r := range runs {
		applyRun(&d.Summary, r)
	}

	stored, err := storedPerDay(ctx, db, start, end)
	if err != nil {
		return nil, err
	}
	if s, ok := stored[date]; ok {
		d.Summary.New, d.Summary.NewSWE = s[0], s[1]
	}
	closed, err := closedPerDay(ctx, db, start, end)
	if err != nil {
		return nil, err
	}
	d.Summary.Closed = closed[date]

	seenIn := `FROM job WHERE is_swe=1 AND first_seen_at >= ? AND first_seen_at < ?`
	args := []any{start.Format(time.RFC3339), end.Format(time.RFC3339)}
	if d.Roles, err = groupCount(ctx, db, `SELECT role_family, count(*) `+seenIn+` GROUP BY role_family`, args...); err != nil {
		return nil, err
	}
	if d.Seniorities, err = groupCount(ctx, db, `SELECT seniority, count(*) `+seenIn+` GROUP BY seniority`, args...); err != nil {
		return nil, err
	}
	if d.Techs, err = techFrequencyBySeen(ctx, db, start, end, 15); err != nil {
		return nil, err
	}

	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM job WHERE first_seen_at >= ? AND first_seen_at < ?`,
		args...).Scan(&d.JobsTotal); err != nil {
		return nil, err
	}
	if d.Jobs, err = jobsFirstSeen(ctx, db, start, end, jobLimit); err != nil {
		return nil, err
	}
	d.Truncated = d.JobsTotal > len(d.Jobs)
	return d, nil
}

const runCols = `SELECT id, kind, status, started_at, coalesce(ended_at,''),
	  coalesce(pages_fetched,0), coalesce(jobs_seen,0), coalesce(jobs_new,0),
	  coalesce(jobs_updated,0), coalesce(jobs_closed,0),
	  coalesce(llm_calls,0), coalesce(llm_cached,0), coalesce(errors,0), coalesce(watermark,'')
	FROM ingest_run `

func listRuns(ctx context.Context, db *store.DB, start, end time.Time) ([]RunRow, error) {
	rows, err := db.QueryContext(ctx,
		runCols+`WHERE started_at >= ? AND started_at < ? ORDER BY started_at DESC, id DESC`,
		start.Format(time.RFC3339), end.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	return scanRuns(rows)
}

// lastRun returns the most recent run of any kind across all history.
func lastRun(ctx context.Context, db *store.DB) (*RunRow, error) {
	rows, err := db.QueryContext(ctx, runCols+`ORDER BY started_at DESC, id DESC LIMIT 1`)
	if err != nil {
		return nil, err
	}
	out, err := scanRuns(rows)
	if err != nil || len(out) == 0 {
		return nil, err
	}
	return &out[0], nil
}

func scanRuns(rows *sql.Rows) ([]RunRow, error) {
	defer rows.Close()
	var out []RunRow
	for rows.Next() {
		var r RunRow
		var started, ended string
		if err := rows.Scan(&r.ID, &r.Kind, &r.Status, &started, &ended,
			&r.Pages, &r.Seen, &r.New, &r.Updated, &r.Closed,
			&r.LLMCalls, &r.LLMCached, &r.Errors, &r.Watermark); err != nil {
			return nil, err
		}
		t, perr := time.Parse(time.RFC3339, started)
		if perr != nil {
			continue // unparseable timestamp: skip rather than mis-bucket
		}
		r.StartedAt = t
		if ended != "" {
			if e, perr := time.Parse(time.RFC3339, ended); perr == nil {
				r.EndedAt = e
				r.Duration = e.Sub(t)
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// applyRun folds one run into a day bucket. Ingest kinds own the crawl
// counters; enrich owns the LLM counters; every kind contributes errors.
func applyRun(row *DayRow, r RunRow) {
	if !slices.Contains(row.Kinds, r.Kind) {
		row.Kinds = append(row.Kinds, r.Kind)
	}
	row.Status = worseStatus(row.Status, r.Status)
	row.Errors += r.Errors
	switch r.Kind {
	case store.RunIncremental, store.RunReconcile:
		row.Duration += r.Duration
		row.Pages += r.Pages
		row.Archived += r.Seen
		row.Updated += r.Updated
	case store.RunEnrich:
		row.LLMCalls += r.LLMCalls
		row.LLMCached += r.LLMCached
	}
}

// firstActivityDay returns the earliest SGT day the pipeline recorded anything
// (a run or a stored posting), or "" on a virgin DB.
func firstActivityDay(ctx context.Context, db *store.DB) (string, error) {
	// date(min(col)) not min(date(col)): the shift is monotonic so the two are
	// equivalent, but wrapping the column in a function defeats the index and
	// turns this into a full scan of job on every page load.
	var d sqlNullableString
	err := db.QueryRowContext(ctx, `
		SELECT min(d) FROM (
		  SELECT date(min(started_at),'+8 hours') AS d FROM ingest_run
		  UNION ALL
		  SELECT date(min(first_seen_at),'+8 hours') FROM job
		)`).Scan(&d)
	if err != nil {
		return "", err
	}
	return d.StrOr(""), nil
}

// storedPerDay returns SGT date -> [candidates first seen, of which SWE].
func storedPerDay(ctx context.Context, db *store.DB, start, end time.Time) (map[string][2]int, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT date(first_seen_at,'+8 hours') AS d, count(*), coalesce(sum(is_swe),0)
		FROM job WHERE first_seen_at >= ? AND first_seen_at < ? GROUP BY d`,
		start.Format(time.RFC3339), end.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][2]int{}
	for rows.Next() {
		var d string
		var n, swe int
		if err := rows.Scan(&d, &n, &swe); err != nil {
			return nil, err
		}
		out[d] = [2]int{n, swe}
	}
	return out, rows.Err()
}

// closedPerDay returns SGT date -> jobs closed that day.
func closedPerDay(ctx context.Context, db *store.DB, start, end time.Time) (map[string]int, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT date(closed_at,'+8 hours') AS d, count(*) FROM job
		WHERE closed_at IS NOT NULL AND closed_at >= ? AND closed_at < ? GROUP BY d`,
		start.Format(time.RFC3339), end.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var d string
		var n int
		if err := rows.Scan(&d, &n); err != nil {
			return nil, err
		}
		out[d] = n
	}
	return out, rows.Err()
}

// techFrequencyBySeen counts SWE postings *crawled* in the window that mention
// each tech. The weekly report counts by posting_date instead (docs/03 §6);
// the daily view is about the crawl, so it keys off first_seen_at.
func techFrequencyBySeen(ctx context.Context, db *store.DB, start, end time.Time, limit int) ([]KV, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT t.tech_slug, count(DISTINCT j.uuid) AS n FROM job j
		JOIN job_tech t ON t.job_uuid=j.uuid
		WHERE j.is_swe=1 AND j.first_seen_at >= ? AND j.first_seen_at < ?
		GROUP BY t.tech_slug ORDER BY n DESC, t.tech_slug LIMIT ?`,
		start.Format(time.RFC3339), end.Format(time.RFC3339), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KV
	for rows.Next() {
		var k string
		var v int
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out = append(out, KV{Key: k, Value: float64(v)})
	}
	return out, rows.Err()
}

func jobsFirstSeen(ctx context.Context, db *store.DB, start, end time.Time, limit int) ([]JobRow, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT j.title, coalesce(c.name,''), coalesce(j.role_family,''), coalesce(j.seniority,''),
		  j.salary_min, j.salary_max, coalesce(j.salary_type,''), j.salary_hidden,
		  j.is_swe, j.posting_date, j.first_seen_at
		FROM job j LEFT JOIN company c ON c.uen=j.company_uen
		WHERE j.first_seen_at >= ? AND j.first_seen_at < ?
		ORDER BY j.is_swe DESC, j.first_seen_at, j.title LIMIT ?`,
		start.Format(time.RFC3339), end.Format(time.RFC3339), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobRow
	for rows.Next() {
		var j JobRow
		var lo, hi sqlNullableInt
		var salType, posting, seen string
		var hidden, isSWE int
		if err := rows.Scan(&j.Title, &j.Company, &j.RoleFamily, &j.Seniority,
			&lo, &hi, &salType, &hidden, &isSWE, &posting, &seen); err != nil {
			return nil, err
		}
		j.IsSWE = isSWE == 1
		j.Salary = salaryRange(lo, hi, salType, hidden == 1)
		j.PostingDate = shortDate(posting)
		j.FirstSeen = shortTime(seen)
		out = append(out, j)
	}
	return out, rows.Err()
}

func salaryRange(lo, hi sqlNullableInt, salType string, hidden bool) string {
	if hidden {
		return "hidden"
	}
	if !lo.Valid || !hi.Valid || (lo.Int64 == 0 && hi.Int64 == 0) {
		return "—"
	}
	unit := ""
	if salType != "" {
		unit = " (" + salType + ")"
	}
	return fmt.Sprintf("S$%d–%d%s", lo.Int64, hi.Int64, unit)
}

func shortDate(ts string) string {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.In(sgt).Format("2006-01-02")
	}
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}

func shortTime(ts string) string {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.In(sgt).Format("15:04")
	}
	return "—"
}

func dayKey(t time.Time) string { return t.In(sgt).Format("2006-01-02") }

// worseStatus keeps the most alarming status of a day: failed beats partial
// beats running beats success, so a green day means every run was green.
func worseStatus(a, b string) string {
	rank := map[string]int{"": 0, store.StatusSuccess: 1, store.StatusRunning: 2, store.StatusPartial: 3, store.StatusFailed: 4}
	if rank[b] > rank[a] {
		return b
	}
	return a
}
