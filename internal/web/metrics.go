package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/meirongdev/jobs-sg/internal/store"
)

// runKinds is the set every per-kind metric reports over.
var runKinds = []string{store.RunIncremental, store.RunReconcile, store.RunEnrich, store.RunReport}

// family is one metric family: its HELP/TYPE header and every sample beneath it.
//
// Rendering family by family, rather than sorting one flat list of lines, is
// what keeps the exposition valid — a family's header has to precede its
// samples and its samples must not be interleaved with another family's. The
// previous flat-then-sort approach happened to satisfy that only because "#"
// sorts before every metric name.
type family struct {
	name    string
	help    string
	typ     string // gauge | counter
	samples []string
}

func (f *family) add(format string, args ...any) {
	f.samples = append(f.samples, fmt.Sprintf(format, args...))
}

// render writes the families that have something to say. A family with no
// samples is omitted rather than printed as zero: before the first report run
// there is genuinely no value, and a fabricated 0 is indistinguishable from a
// real one.
func render(families []*family) string {
	var b strings.Builder
	for _, f := range families {
		if len(f.samples) == 0 {
			continue
		}
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", f.name, f.help, f.name, f.typ)
		for _, s := range f.samples {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// handleMetrics renders Prometheus text exposition from DB state (docs/04
// §3.1). All values are derived from tables, not process memory, so a restart
// loses nothing.
//
// A DB error fails the whole scrape with a 500 instead of emitting a partial
// body. Prometheus then marks the target down, which is itself a signal — where
// a body reporting `jobs_sg_jobs_total{state="active"} 0` because a query
// errored is indistinguishable from a market that emptied overnight, and every
// alert resting on it fires on a lie. "No rows yet" is a different thing and
// stays a legitimate omission.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	body, err := s.renderMetrics(ctx)
	if err != nil {
		slog.Error("metrics scrape failed", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write([]byte(body))
}

func (s *Server) renderMetrics(ctx context.Context) (string, error) {
	lastSuccess := &family{
		name: "jobs_sg_last_success_timestamp_seconds",
		help: "Unix time of the last successful run of each job kind.",
		typ:  "gauge",
	}
	for _, kind := range runKinds {
		ts, ok, err := s.db.LastSuccess(ctx, kind)
		if err != nil {
			return "", fmt.Errorf("last success %s: %w", kind, err)
		}
		if ok {
			lastSuccess.add("jobs_sg_last_success_timestamp_seconds{kind=%q} %d", kind, ts.Unix())
		}
	}

	duration := &family{
		name: "jobs_sg_run_duration_seconds",
		help: "Wall-clock seconds of the most recent finished run of each job kind.",
		typ:  "gauge",
	}
	for _, kind := range runKinds {
		var started, ended string
		found, err := s.scanRow(ctx, `
			SELECT started_at, ended_at FROM ingest_run WHERE kind=? AND ended_at IS NOT NULL
			ORDER BY id DESC LIMIT 1`, []any{kind}, &started, &ended)
		if err != nil {
			return "", fmt.Errorf("run duration %s: %w", kind, err)
		}
		if !found {
			continue
		}
		st, err1 := time.Parse(time.RFC3339, started)
		en, err2 := time.Parse(time.RFC3339, ended)
		if err1 != nil || err2 != nil {
			// a malformed timestamp is a data bug, not a scrape failure
			slog.Warn("unparseable run timestamps", "kind", kind, "started", started, "ended", ended)
			continue
		}
		duration.add("jobs_sg_run_duration_seconds{kind=%q} %d", kind, int64(en.Sub(st).Seconds()))
	}

	// _total is the counter convention, and both of these move both ways —
	// active falls whenever postings close, unmapped falls as terms get
	// reviewed. Renamed while nothing is deployed and nothing queries them yet;
	// doing it later would break dashboards for no added benefit.
	jobs := &family{
		name: "jobs_sg_jobs",
		help: "Candidate postings currently stored, by lifecycle state.",
		typ:  "gauge",
	}
	var active, closed int
	if _, err := s.scanRow(ctx, `SELECT count(*) FROM job WHERE closed_at IS NULL`, nil, &active); err != nil {
		return "", fmt.Errorf("active jobs: %w", err)
	}
	if _, err := s.scanRow(ctx, `SELECT count(*) FROM job WHERE closed_at IS NOT NULL`, nil, &closed); err != nil {
		return "", fmt.Errorf("closed jobs: %w", err)
	}
	jobs.add("jobs_sg_jobs{state=%q} %d", "active", active)
	jobs.add("jobs_sg_jobs{state=%q} %d", "closed", closed)

	// The reported week used to ride along as a label, which minted a new time
	// series every Monday and retired none — an unbounded label value is the
	// standard way to grow a Prometheus instance without bound. The value alone
	// is the metric; Prometheus's own time axis records when it held.
	newJobs := &family{
		name: "jobs_sg_jobs_new",
		help: "New SWE postings in the most recent materialised ISO week.",
		typ:  "gauge",
	}
	var weekStart string
	found, err := s.scanRow(ctx,
		`SELECT week_start FROM weekly_metric WHERE metric='new_jobs' ORDER BY week_start DESC LIMIT 1`,
		nil, &weekStart)
	if err != nil {
		return "", fmt.Errorf("latest week: %w", err)
	}
	if found {
		var v float64
		ok, err := s.scanRow(ctx,
			`SELECT value FROM weekly_metric WHERE week_start=? AND metric='new_jobs' AND dim_key=''`,
			[]any{weekStart}, &v)
		if err != nil {
			return "", fmt.Errorf("new jobs: %w", err)
		}
		if ok {
			newJobs.add("jobs_sg_jobs_new %d", int(v))
		}
	}

	counters := []struct {
		name, help, query string
	}{
		{"jobs_sg_llm_calls_total", "LLM extraction calls made across all enrich runs.",
			`SELECT coalesce(sum(llm_calls),0) FROM ingest_run WHERE kind='enrich'`},
		{"jobs_sg_llm_cache_hits_total", "Enrich cache hits across all enrich runs.",
			`SELECT coalesce(sum(llm_cached),0) FROM ingest_run WHERE kind='enrich'`},
		{"jobs_sg_llm_errors_total", "Errors recorded by enrich runs.",
			`SELECT coalesce(sum(errors),0) FROM ingest_run WHERE kind='enrich'`},
		{"jobs_sg_ingest_errors_total", "Errors recorded by incremental and reconcile runs.",
			`SELECT coalesce(sum(errors),0) FROM ingest_run WHERE kind IN ('incremental','full_reconcile')`},
	}
	families := []*family{lastSuccess, duration, jobs, newJobs}
	for _, c := range counters {
		f := &family{name: c.name, help: c.help, typ: "counter"}
		var n int
		if _, err := s.scanRow(ctx, c.query, nil, &n); err != nil {
			return "", fmt.Errorf("%s: %w", c.name, err)
		}
		f.add("%s %d", c.name, n)
		families = append(families, f)
	}

	backlog := &family{
		name: "jobs_sg_enrich_backlog",
		help: "SWE postings the LLM layer has not processed yet.",
		typ:  "gauge",
	}
	n, err := s.db.EnrichBacklogCount(ctx)
	if err != nil {
		return "", fmt.Errorf("enrich backlog: %w", err)
	}
	backlog.add("jobs_sg_enrich_backlog %d", n)

	unmapped := &family{
		name: "jobs_sg_unmapped_tech",
		help: "Unreviewed terms the LLM returned that the taxonomy could not map.",
		typ:  "gauge",
	}
	n, err = s.db.UnmappedTechCount(ctx)
	if err != nil {
		return "", fmt.Errorf("unmapped tech: %w", err)
	}
	unmapped.add("jobs_sg_unmapped_tech %d", n)

	return render(append(families, backlog, unmapped)), nil
}

// scanRow runs a single-row query. A missing row is not an error: several of
// these metrics have no value until the corresponding job has run once.
func (s *Server) scanRow(ctx context.Context, query string, args []any, dest ...any) (bool, error) {
	err := s.db.QueryRowContext(ctx, query, args...).Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
