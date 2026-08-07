// Package report computes weekly metrics (SQL only — LLM never produces
// numbers, docs/01 §4) and renders self-contained HTML/Markdown reports.
package report

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/meirongdev/jobs-sg/internal/metric"
	"github.com/meirongdev/jobs-sg/internal/store"
)

// sgt is the report timezone: every week and day bucket is an SGT calendar
// period, while timestamps are stored as UTC (docs/03 §2).
var sgt = time.FixedZone("SGT", 8*3600)

// KV is a labeled value for report sections. It aliases metric.KV so the
// aggregate layer and the renderer cannot drift apart.
type KV = metric.KV

// CompanyCount is a top-company row.
type CompanyCount struct {
	Name  string
	Count int
}

// DataQuality carries the "is this report trustworthy" section (docs/01 §2).
type DataQuality struct {
	IngestStatus  string
	IngestLast    string
	ReconcileLast string
	EnrichStatus  string
	UnmappedTech  int
	EnrichBacklog int
	LLMCalls      int
	LLMCached     int
}

// Report holds all aggregates for one week (rendered from this struct; every
// number computed by SQL above).
type Report struct {
	WeekStart    string // YYYY-MM-DD (SGT Monday)
	WeekLabel    string // YYYY-Www
	NewJobs      int
	PrevNewJobs  int
	ActiveJobs   int
	TopRole      string
	Roles        []KV
	Seniorities  []KV
	WorkModes    []KV
	CompanyTypes []KV
	TopCompanies []CompanyCount
	TopTechs     []KV
	SalaryMedian float64

	// Sections sourced from internal/metric rather than recomputed here. The
	// report and the live pages now answer the same questions from one
	// definition — which is the point of the metric layer (spec §4.2), and the
	// only way "口径可比" survives having two renderers.
	//
	// It also retires three figures spec §3.7 called wrong: a single salary
	// median with no sample disclosure (§3.7-4) becomes the percentile ladder;
	// raw view/application averages that actually measured posting age
	// (§3.7-2) become per-day rates; and a "no experience required" share that
	// folded "unstated" in with "zero" (§3.7-1) becomes entry-level counts.
	Tech    *metric.TechReport
	Pay     *metric.PayReport
	Market  *metric.MarketReport
	Company *metric.CompanyReport

	DataQuality DataQuality
}

// ISOWeekLabel returns the YYYY-Www label (ISO 8601 week, SGT timezone).
func ISOWeekLabel(t time.Time) string {
	_, w := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", t.Year(), w)
}

// WeekBounds converts a SGT Monday date (YYYY-MM-DD) into the UTC [start,end)
// interval for that ISO week. SGT = UTC+8, so Monday 00:00 SGT = Sunday 16:00 UTC.
func WeekBounds(monday time.Time) (start, end time.Time) {
	// normalise to SGT midnight
	m := time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, sgt)
	start = m.UTC()
	end = m.AddDate(0, 0, 7).UTC()
	return start, end
}

// ComputeMetrics materialises weekly_metric for the week and returns the
// Report aggregate. It clears prior rows for the week (idempotent recompute).
func ComputeMetrics(ctx context.Context, db *store.DB, monday time.Time) (*Report, error) {
	start, end := WeekBounds(monday)
	weekStart := fmt.Sprintf("%04d-%02d-%02d", monday.Year(), int(monday.Month()), monday.Day())
	label := ISOWeekLabel(monday)
	r := &Report{WeekStart: weekStart, WeekLabel: label}
	var err error

	// clear + recompute
	if _, err := db.ExecContext(ctx, `DELETE FROM weekly_metric WHERE week_start=?`, weekStart); err != nil {
		return nil, err
	}
	put := func(metric, dimKey, dimType string, value float64) error {
		_, err := db.ExecContext(ctx, `
			INSERT INTO weekly_metric(week_start, metric, dim_key, dim_type, value, computed_at)
			VALUES(?,?,?,?,?,?) ON CONFLICT(week_start, metric, dim_type, dim_key)
			DO UPDATE SET value=excluded.value, computed_at=excluded.computed_at`,
			weekStart, metric, dimKey, dimType, value, store.NowUTC())
		return err
	}
	// posting_date is date-only on the live API ("2026-08-03") while these
	// bounds render as RFC3339. The string comparison is still correct at
	// every edge, but only because the bounds are SGT midnights (= Sunday
	// 16:00Z): the bound's UTC calendar date is never an in-week SGT date.
	// Do NOT "simplify" the bounds to UTC midnight — that shifts the window
	// by a day. Pinned by TestWeekWindowDateOnlyBoundaries.
	sweIn := `FROM job WHERE is_swe=1 AND posting_date >= ? AND posting_date < ?`
	args := func() []any { return []any{start.Format(time.RFC3339), end.Format(time.RFC3339)} }

	// new jobs (distinct canonical uuid posted in week)
	if err := db.QueryRowContext(ctx, `SELECT count(*) `+sweIn, args()...).Scan(&r.NewJobs); err != nil {
		return nil, err
	}
	put("new_jobs", "", "", float64(r.NewJobs))

	// previous week new jobs (WoW)
	prevStart, prevEnd := start.AddDate(0, 0, -7), end.AddDate(0, 0, -7)
	if err := db.QueryRowContext(ctx, `SELECT count(*) `+sweIn, prevStart.Format(time.RFC3339), prevEnd.Format(time.RFC3339)).Scan(&r.PrevNewJobs); err != nil {
		return nil, err
	}

	// active jobs (closed_at IS NULL and not expired)
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM job
		WHERE is_swe=1 AND closed_at IS NULL AND (expiry_date IS NULL OR expiry_date >= ?)`,
		end.Format(time.RFC3339)).Scan(&r.ActiveJobs); err != nil {
		return nil, err
	}
	put("active_jobs", "", "", float64(r.ActiveJobs))

	// role_family distribution
	r.Roles, err = groupCount(ctx, db, `SELECT role_family, count(*) `+sweIn+` GROUP BY role_family`, args()...)
	if err != nil {
		return nil, err
	}
	writeGroup(ctx, db, put, weekStart, "role_family", "role_family", r.Roles)
	r.TopRole = ""
	if len(r.Roles) > 0 {
		r.TopRole = r.Roles[0].Key
	}

	// seniority
	r.Seniorities, err = groupCount(ctx, db, `SELECT seniority, count(*) `+sweIn+` GROUP BY seniority`, args()...)
	if err != nil {
		return nil, err
	}
	writeGroup(ctx, db, put, weekStart, "seniority", "seniority", r.Seniorities)

	// work_mode
	r.WorkModes, err = groupCount(ctx, db, `SELECT work_mode, count(*) `+sweIn+` GROUP BY work_mode`, args()...)
	if err != nil {
		return nil, err
	}
	writeGroup(ctx, db, put, weekStart, "work_mode", "work_mode", r.WorkModes)

	// company_type
	r.CompanyTypes, err = groupCount(ctx, db, `SELECT c.company_type, count(*) FROM job j JOIN company c ON c.uen=j.company_uen WHERE j.is_swe=1 AND j.posting_date >= ? AND j.posting_date < ? AND c.company_type IS NOT NULL GROUP BY c.company_type`, args()...)
	if err != nil {
		return nil, err
	}
	writeGroup(ctx, db, put, weekStart, "company_type", "company_type", r.CompanyTypes)

	// top 10 companies
	r.TopCompanies, err = topCompanies(ctx, db, start, end)
	if err != nil {
		return nil, err
	}

	// tech frequency (job-level distinct, docs/03 §6)
	r.TopTechs, err = techFrequency(ctx, db, start, end)
	if err != nil {
		return nil, err
	}
	writeGroup(ctx, db, put, weekStart, "tech_freq", "tech", r.TopTechs)

	// Sections computed by the metric layer, anchored on the reported week
	// rather than on wall-clock time, so regenerating an old report reproduces
	// its numbers instead of recomputing them against today.
	//
	// Two anchors, because the two window helpers want opposite sides of the
	// boundary: LastCompletedWeek needs an instant INSIDE the following week to
	// return the reported one, while Rolling ends at the end of the SGT day
	// containing its argument, so it needs the reported week's last instant to
	// stop exactly at the week boundary.
	weekAnchor := end
	rollAnchor := end.Add(-time.Nanosecond)
	lens := metric.Lens{}

	if r.Tech, err = metric.TechReportFor(ctx, db, weekAnchor, lens); err != nil {
		return nil, err
	}
	if r.Pay, err = metric.PayReportFor(ctx, db, rollAnchor, lens); err != nil {
		return nil, err
	}
	if r.Market, err = metric.MarketReportFor(ctx, db, weekAnchor, lens); err != nil {
		return nil, err
	}
	if r.Company, err = metric.CompanyReportFor(ctx, db, rollAnchor, lens); err != nil {
		return nil, err
	}

	// salary_median keeps its weekly_metric row for historical continuity, but
	// the figure now comes from the same percentile code the site prints —
	// including its suppression. Below the sample floor the row is omitted
	// rather than written as 0: weekly_metric is what a future trend query
	// reads, and a fabricated zero there outlives the page that refused to
	// show it.
	r.SalaryMedian = r.Pay.Overall.P50
	if !r.Pay.Overall.Coverage.Suppressed {
		put("salary_median", "monthly", "salary", r.SalaryMedian)
	}

	// Momentum's inputs, materialised for audit. The pages compute share live;
	// these rows are what makes "口径变更须全量重算" (docs/01 §4) checkable
	// after the fact, and what a future trend query reads instead of replaying
	// five weeks of aggregation (spec §3.1).
	put("swe_enriched", "", "", float64(r.Tech.Denom))
	for _, t := range r.Tech.Ranked {
		put("tech_share", t.Slug, "tech", t.Share)
	}

	// data quality
	r.DataQuality, err = quality(ctx, db)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func groupCount(ctx context.Context, db *store.DB, q string, args ...any) ([]KV, error) {
	rows, err := db.QueryContext(ctx, q, args...)
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
		if k == "" {
			continue
		}
		out = append(out, KV{Key: k, Value: float64(v)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Value > out[j].Value })
	return out, rows.Err()
}

func writeGroup(ctx context.Context, db *store.DB, put func(string, string, string, float64) error, weekStart, metric, dimType string, kvs []KV) {
	for _, kv := range kvs {
		if err := put(metric, kv.Key, dimType, kv.Value); err != nil {
			return // best-effort materialisation; render uses in-memory struct
		}
	}
}

func topCompanies(ctx context.Context, db *store.DB, start, end time.Time) ([]CompanyCount, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.name, count(*) FROM job j JOIN company c ON c.uen=j.company_uen
		WHERE j.is_swe=1 AND j.posting_date >= ? AND j.posting_date < ?
		GROUP BY c.uen ORDER BY count(*) DESC LIMIT 10`, start.Format(time.RFC3339), end.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CompanyCount
	for rows.Next() {
		var c CompanyCount
		if err := rows.Scan(&c.Name, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func techFrequency(ctx context.Context, db *store.DB, start, end time.Time) ([]KV, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT t.tech_slug, count(DISTINCT j.uuid) FROM job j
		JOIN job_tech t ON t.job_uuid=j.uuid
		WHERE j.is_swe=1 AND j.posting_date >= ? AND j.posting_date < ?
		GROUP BY t.tech_slug ORDER BY count(DISTINCT j.uuid) DESC LIMIT 30`,
		start.Format(time.RFC3339), end.Format(time.RFC3339))
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

func quality(ctx context.Context, db *store.DB) (DataQuality, error) {
	var q DataQuality
	last := func(kind string) (string, string) {
		var status, ended sqlNullableString
		_ = db.QueryRowContext(ctx, `
			SELECT status, ended_at FROM ingest_run WHERE kind=? ORDER BY ended_at DESC LIMIT 1`, kind).Scan(&status, &ended)
		return status.StrOr(""), ended.StrOr("")
	}
	q.IngestStatus, q.IngestLast = last(store.RunIncremental)
	q.ReconcileLast, _ = last(store.RunReconcile)
	q.EnrichStatus, _ = last(store.RunEnrich)
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM unmapped_tech WHERE reviewed=0`).Scan(&n); err == nil {
		q.UnmappedTech = n
	}
	if n, err := db.EnrichBacklogCount(ctx); err == nil {
		q.EnrichBacklog = n
	}
	_ = db.QueryRowContext(ctx, `SELECT coalesce(sum(llm_calls),0), coalesce(sum(llm_cached),0) FROM ingest_run WHERE kind='enrich'`).Scan(&q.LLMCalls, &q.LLMCached)
	return q, nil
}

// sqlNullableInt is a nullable integer that also accepts a float.
//
// sql.NullInt64 would be the obvious choice, but modernc.org/sqlite hands back
// float64 for expressions over INTEGER columns (SQLite's dynamic typing), and
// NullInt64 rejects that with a conversion error. Counters here are small
// enough that the float round-trip is lossless.
type sqlNullableInt struct {
	Valid bool
	Int64 int64
}

func (s *sqlNullableInt) Scan(v any) error {
	if v == nil {
		s.Valid = false
		return nil
	}
	switch n := v.(type) {
	case int64:
		s.Valid = true
		s.Int64 = n
	case float64:
		s.Valid = true
		s.Int64 = int64(n)
	default:
		s.Valid = false
	}
	return nil
}

// sqlNullableString is the string counterpart, accepting both string and
// []byte because the driver returns either depending on the column.
type sqlNullableString struct {
	Valid bool
	S     string
}

func (s *sqlNullableString) Scan(v any) error {
	if v == nil {
		s.Valid = false
		return nil
	}
	if b, ok := v.([]byte); ok {
		s.Valid = true
		s.S = string(b)
		return nil
	}
	if str, ok := v.(string); ok {
		s.Valid = true
		s.S = str
		return nil
	}
	s.Valid = false
	return nil
}

func (s sqlNullableString) StrOr(def string) string {
	if s.Valid {
		return s.S
	}
	return def
}
