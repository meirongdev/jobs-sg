// Package web serves two pages read-only — the weekly report (static files
// from cmd/report) and the daily crawl statistics (rendered per request from
// the DB) — plus Prometheus metrics. State lives in the DB, not process
// memory, so restarts lose nothing (docs/02 §4.4).
package web

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/meirongdev/jobs-sg/internal/store"
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

// Handler returns the HTTP handler with all routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleRoot)
	mux.HandleFunc("GET /tech", s.handleTech)
	mux.HandleFunc("GET /w/{week}", s.handleWeek)
	mux.HandleFunc("GET /daily", s.handleDaily)
	mux.HandleFunc("GET /daily/{date}", s.handleDailyDate)
	mux.HandleFunc("GET /robots.txt", s.handleRobots)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	return mux
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.serveReportFile(w, r, "latest.html")
}

func (s *Server) handleWeek(w http.ResponseWriter, r *http.Request) {
	week := r.PathValue("week")
	s.serveReportFile(w, r, week+".html")
}

func (s *Server) serveReportFile(w http.ResponseWriter, r *http.Request, name string) {
	// path-traversal guard: only allow a safe basename
	if filepath.Base(name) != name {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.reportDir, name)
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
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
