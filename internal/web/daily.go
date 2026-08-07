package web

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/meirongdev/jobs-sg/internal/report"
)

// Daily pages are rendered per request from the read-only DB rather than
// written to disk by a CronJob: ingest finishes at ~02:20 SGT and the numbers
// must be current the moment it does, and /metrics already derives its state
// from the same tables (docs/02 §4.4).

// maxWindowDays bounds the ?days= parameter so a crafted URL cannot ask the
// web pod to build an arbitrarily large page.
const maxWindowDays = 90

// dailyTimeout bounds the whole page build; SQLite is local and these are
// indexed aggregates, so anything slower means something is wrong.
const dailyTimeout = 5 * time.Second

// Cache settings: 60s keeps the page effectively live (ingest runs once a day)
// while collapsing crawler bursts into one recompute. 64 entries ≈ 1–2 MiB of
// HTML, comfortably inside the pod's 64Mi request.
const (
	dailyCacheTTL     = 60 * time.Second
	dailyCacheEntries = 64
)

func (s *Server) handleDaily(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), dailyTimeout)
	defer cancel()

	days := report.DefaultWindowDays
	if q := r.URL.Query().Get("days"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 || n > maxWindowDays {
			http.Error(w, "days must be 1.."+strconv.Itoa(maxWindowDays), http.StatusBadRequest)
			return
		}
		days = n
	}

	now := s.now()
	s.servePage(w, "overview:"+strconv.Itoa(days), now, func() (string, error) {
		o, err := report.ComputeDailyOverview(ctx, s.db, now, days)
		if err != nil {
			return "", err
		}
		return report.RenderDailyOverviewHTML(o)
	})
}

func (s *Server) handleDailyDate(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), dailyTimeout)
	defer cancel()

	date := r.PathValue("date")
	day, err := report.ParseDay(date)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	now := s.now()
	if day.After(now) {
		http.NotFound(w, r) // no data can exist for a future day
		return
	}

	s.servePage(w, "day:"+date, now, func() (string, error) {
		d, err := report.ComputeDayDetail(ctx, s.db, day, now, report.DayJobLimit)
		if err != nil {
			return "", err
		}
		return report.RenderDayDetailHTML(d)
	})
}

// servePage returns the cached rendering of a page, building it on a miss.
func (s *Server) servePage(w http.ResponseWriter, key string, now time.Time, build func() (string, error)) {
	html, err := s.cache.do(key, now, build)
	if err != nil {
		slog.Error("build page", "page", key, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeHTML(w, html)
}

// handleRobots keeps crawlers off the per-day drill-downs: every past date is
// a valid URL backed by real queries, so an unguided crawl is unbounded work
// for pages nobody searches for. The report pages stay indexable.
func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("User-agent: *\nDisallow: /ops/\nCrawl-delay: 10\n"))
}

func writeHTML(w http.ResponseWriter, html string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// live-derived pages: let a reverse proxy hold them briefly, never longer
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.Write([]byte(html))
}
