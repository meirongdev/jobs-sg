package web

import (
	"context"
	"net/http"

	"github.com/meirongdev/jobs-sg/internal/metric"
	"github.com/meirongdev/jobs-sg/internal/view"
)

func (s *Server) handleCompanies(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), dailyTimeout)
	defer cancel()

	lens, err := parseLens(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	now := s.now()
	s.servePage(w, "companies:"+lens.Key(), now, func() (string, error) {
		rep, err := metric.CompanyReportFor(ctx, s.db, now, lens)
		if err != nil {
			return "", err
		}
		return view.CompanyPage(rep)
	})
}
