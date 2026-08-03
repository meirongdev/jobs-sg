package web

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/meirongdev/jobs-sg/internal/store"
)

// handleMetrics renders Prometheus text exposition from DB state (docs/04
// §3.1). All values are derived from tables, not process memory.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	var b builder
	for _, kind := range []string{store.RunIncremental, store.RunReconcile, store.RunEnrich, store.RunReport} {
		ts, ok, err := s.db.LastSuccess(ctx, kind)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		if ok {
			b.f("jobs_sg_last_success_timestamp_seconds{kind=%q} %d", kind, ts.Unix())
		}
	}

	// run durations: seconds between started_at and ended_at of the last run per kind
	for _, kind := range []string{store.RunIncremental, store.RunReconcile, store.RunEnrich, store.RunReport} {
		var started, ended string
		err := s.db.QueryRowContext(ctx, `
			SELECT started_at, ended_at FROM ingest_run WHERE kind=? AND ended_at IS NOT NULL
			ORDER BY id DESC LIMIT 1`, kind).Scan(&started, &ended)
		if err == nil {
			st, err1 := time.Parse(time.RFC3339, started)
			en, err2 := time.Parse(time.RFC3339, ended)
			if err1 == nil && err2 == nil {
				b.f("jobs_sg_run_duration_seconds{kind=%q} %d", kind, int64(en.Sub(st).Seconds()))
			}
		}
	}

	// job state counts
	var active, closed int
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM job WHERE closed_at IS NULL`).Scan(&active)
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM job WHERE closed_at IS NOT NULL`).Scan(&closed)
	b.f("jobs_sg_jobs_total{state=\"active\"} %d", active)
	b.f("jobs_sg_jobs_total{state=\"closed\"} %d", closed)

	// new jobs last week (by max week_start in weekly_metric)
	var weekStart string
	if err := s.db.QueryRowContext(ctx, `SELECT week_start FROM weekly_metric WHERE metric='new_jobs' ORDER BY week_start DESC LIMIT 1`).Scan(&weekStart); err == nil {
		var v float64
		if err := s.db.QueryRowContext(ctx, `SELECT value FROM weekly_metric WHERE week_start=? AND metric='new_jobs' AND dim_key=''`, weekStart).Scan(&v); err == nil {
			b.f("jobs_sg_jobs_new_total{week=%q} %d", weekStart, int(v))
		}
	}

	// LLM + error + backlog metrics
	var llmCalls, llmCached, llmErrors, ingestErrors int
	_ = s.db.QueryRowContext(ctx, `SELECT coalesce(sum(llm_calls),0) FROM ingest_run WHERE kind='enrich'`).Scan(&llmCalls)
	_ = s.db.QueryRowContext(ctx, `SELECT coalesce(sum(llm_cached),0) FROM ingest_run WHERE kind='enrich'`).Scan(&llmCached)
	_ = s.db.QueryRowContext(ctx, `SELECT coalesce(sum(errors),0) FROM ingest_run WHERE kind='enrich'`).Scan(&llmErrors)
	_ = s.db.QueryRowContext(ctx, `SELECT coalesce(sum(errors),0) FROM ingest_run WHERE kind IN ('incremental','full_reconcile')`).Scan(&ingestErrors)
	b.f("jobs_sg_llm_calls_total %d", llmCalls)
	b.f("jobs_sg_llm_cache_hits_total %d", llmCached)
	b.f("jobs_sg_llm_errors_total %d", llmErrors)
	b.f("jobs_sg_ingest_errors_total %d", ingestErrors)

	if n, err := s.db.EnrichBacklogCount(ctx); err == nil {
		b.f("jobs_sg_enrich_backlog %d", n)
	}
	if n, err := s.db.UnmappedTechCount(ctx); err == nil {
		b.f("jobs_sg_unmapped_tech_total %d", n)
	}

	w.Write([]byte(b.String()))
}

type builder struct{ lines []string }

func (b *builder) f(format string, args ...any) {
	b.lines = append(b.lines, fmt.Sprintf(format, args...))
}

func (b builder) String() string {
	sort.Strings(b.lines)
	var out string
	for _, l := range b.lines {
		out += l + "\n"
	}
	return out
}
