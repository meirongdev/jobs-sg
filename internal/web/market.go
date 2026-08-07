package web

import (
	"context"
	"net/http"

	"github.com/meirongdev/jobs-sg/internal/metric"
	"github.com/meirongdev/jobs-sg/internal/view"
)

// handleMarket serves / — the live market snapshot.
//
// It replaces serving the latest weekly report from disk. "How many jobs are
// there" has to be a current number for a job seeker, and the static report is
// up to seven days stale by the Sunday before the next one (spec §2.2). The
// report keeps its own permanent home under /w/{week}, where being a frozen
// archive is the point.
func (s *Server) handleMarket(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		s.notFound(w, "Page not found", "That URL does not exist on this site.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dailyTimeout)
	defer cancel()

	lens, err := parseLens(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	now := s.now()
	s.servePage(w, "market:"+lens.Key(), now, func() (string, error) {
		rep, err := metric.MarketReportFor(ctx, s.db, now, lens)
		if err != nil {
			return "", err
		}
		return view.MarketPage(rep)
	})
}
