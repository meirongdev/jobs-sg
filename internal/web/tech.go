package web

import (
	"context"
	"net/http"

	"github.com/meirongdev/jobs-sg/internal/metric"
	"github.com/meirongdev/jobs-sg/internal/view"
)

// parseLens reads the site-wide lens off the query string. An unknown value is
// a 400, not a silent no-op: rendering numbers that contradict the URL is
// worse than refusing, and free-text values would let a crafted URL mint
// unbounded cache keys.
func parseLens(r *http.Request) (metric.Lens, error) {
	return metric.ParseLens(r.URL.Query().Get("exp"), r.URL.Query().Get("role"))
}

func (s *Server) handleTech(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), dailyTimeout)
	defer cancel()

	lens, err := parseLens(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	now := s.now()
	s.servePage(w, "tech:"+lens.Key(), now, func() (string, error) {
		rep, err := metric.TechReportFor(ctx, s.db, now, lens)
		if err != nil {
			return "", err
		}
		return view.TechPage(rep)
	})
}
