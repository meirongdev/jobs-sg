// Package web serves two pages read-only — the weekly report (static files
// from cmd/report) and the daily crawl statistics (rendered per request from
// the DB) — plus Prometheus metrics. State lives in the DB, not process
// memory, so restarts lose nothing (docs/02 §4.4).
package web

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/meirongdev/jobs-sg/internal/report"
	"github.com/meirongdev/jobs-sg/internal/store"
	"github.com/meirongdev/jobs-sg/internal/view"
)

// Server holds the read-only DB handle, the report output directory and the
// short-lived cache for live-rendered pages.
type Server struct {
	db        *store.DB
	reportDir string
	now       func() time.Time
	cache     *pageCache
}

// New opens the DB read-only and builds the server.
func New(dataDir string, now func() time.Time) (*Server, error) {
	db, err := store.Open(filepath.Join(dataDir, "jobs.db"), true)
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &Server{
		db:        db,
		reportDir: filepath.Join(dataDir, "report"),
		now:       now,
		cache:     newPageCache(dailyCacheTTL, dailyCacheEntries),
	}, nil
}

// Close releases the read-only handle.
func (s *Server) Close() error { return s.db.Close() }

// Handler returns the public site: everything the HTTPRoute at
// jobs.meirong.dev is allowed to reach.
//
// /metrics is deliberately absent — see MetricsHandler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleRoot)
	mux.HandleFunc("GET /tech", s.handleTech)
	mux.HandleFunc("GET /pay", s.handlePay)
	mux.HandleFunc("GET /w/{week}", s.handleWeek)
	// Operational pages: kept as troubleshooting and data-freshness evidence,
	// but out of the job-seeker nav. The old /daily paths stay as permanent
	// redirects so existing links and bookmarks survive.
	mux.HandleFunc("GET /ops", s.handleDaily)
	mux.HandleFunc("GET /ops/{date}", s.handleDailyDate)
	mux.HandleFunc("GET /daily", redirectTo("/ops"))
	mux.HandleFunc("GET /daily/{date}", s.redirectDailyDate)
	mux.HandleFunc("GET /robots.txt", s.handleRobots)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	return mux
}

// MetricsHandler returns the Prometheus endpoint, to be bound to a listener of
// its own.
//
// The site is fronted by an HTTPRoute matching PathPrefix "/", so anything on
// the public mux is world-readable at jobs.meirong.dev. That is the right call
// for the statistics — they are public labour-market data, which is why docs/02
// §4.4 declines auth — but /metrics is a different category: enrich backlog
// depth, per-run durations, cumulative error counts. Operational posture, not
// content, and nothing about publishing job statistics requires publishing it.
//
// Splitting the listener rather than filtering at the gateway keeps the property
// local to the process: the ServiceMonitor scrapes this port in-cluster, and a
// later edit to the route cannot re-expose what was never bound to the public
// listener in the first place.
func (s *Server) MetricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	return mux
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		s.notFound(w, "Page not found", "That URL does not exist on this site.")
		return
	}
	// The weekly report is written by a CronJob, so between a fresh deploy and
	// the first Monday 09:00 SGT run there is no latest.html — and "/" is the
	// first entry in the nav every other page renders. Explain that rather than
	// dead-ending a visitor on a bare 404, and point at the pages that do work:
	// they are computed live and have numbers from day one.
	if !s.reportExists("latest.html") {
		s.serveNotice(w, http.StatusOK, "Weekly report",
			"No weekly report yet",
			"The first report is published Monday 09:00 SGT, covering the previous ISO week. Until then, the live pages below already have numbers.",
			"/")
		return
	}
	s.serveReportFile(w, r, "latest.html")
}

func (s *Server) handleWeek(w http.ResponseWriter, r *http.Request) {
	week := r.PathValue("week")
	s.serveReportFile(w, r, week+".html")
}

// reportExists reports whether a report file is on disk, rejecting any name
// that is not a plain basename (path-traversal guard).
func (s *Server) reportExists(name string) bool {
	if filepath.Base(name) != name {
		return false
	}
	_, err := os.Stat(filepath.Join(s.reportDir, name))
	return err == nil
}

func (s *Server) serveReportFile(w http.ResponseWriter, r *http.Request, name string) {
	if !s.reportExists(name) {
		s.notFound(w, "Report not found",
			"No report has been published for that week. Reports start from the first full ISO week after this site went live.")
		return
	}
	http.ServeFile(w, r, filepath.Join(s.reportDir, name))
}

// notFound renders a 404 that keeps the site's navigation, so a wrong URL is a
// wrong turn rather than an exit.
func (s *Server) notFound(w http.ResponseWriter, heading, body string) {
	s.serveNotice(w, http.StatusNotFound, "Not found", heading, body, "")
}

func (s *Server) serveNotice(w http.ResponseWriter, code int, title, heading, body, active string) {
	html, err := view.Notice(title, heading, body, active)
	if err != nil {
		// The notice page is the fallback; if it cannot render, say so plainly
		// rather than recursing into another failing render.
		slog.Error("render notice", "err", err)
		http.Error(w, http.StatusText(code), code)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	w.Write([]byte(html))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		http.Error(w, "unhealthy", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// redirectTo permanently redirects a retired path, preserving the query string.
//
// `to` is a per-request copy on purpose: appending to the captured `target`
// would accumulate query strings across requests, so the second visitor to
// /daily?days=7 would be sent to /ops?days=7?days=7.
func redirectTo(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		to := target
		if q := r.URL.RawQuery; q != "" {
			to += "?" + q
		}
		http.Redirect(w, r, to, http.StatusMovedPermanently)
	}
}

// redirectDailyDate maps /daily/{date} onto /ops/{date}. The date is validated
// before it lands in a Location header so the redirect cannot echo arbitrary
// path input back to the client.
func (s *Server) redirectDailyDate(w http.ResponseWriter, r *http.Request) {
	date := r.PathValue("date")
	if _, err := report.ParseDay(date); err != nil {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/ops/"+date, http.StatusMovedPermanently)
}
