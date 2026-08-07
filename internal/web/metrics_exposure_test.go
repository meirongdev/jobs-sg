package web

import (
	"net/http"
	"strings"
	"testing"
)

// The HTTPRoute in deploy/web.yaml matches PathPrefix "/", so every route on
// the public mux is reachable at jobs.meirong.dev. The statistics are meant to
// be — /metrics is not: it reports enrich backlog depth, run durations and
// cumulative error counts, which is operational posture rather than content.
//
// This is the property that must not regress by someone adding one convenient
// line back to Handler().
func TestPublicHandlerDoesNotServeMetrics(t *testing.T) {
	s := setupWeb(t)
	rec := get(t, s, "/metrics")
	if rec.Code != http.StatusNotFound {
		t.Errorf("/metrics on the public handler = %d, want 404 — it is bound to its own listener", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "jobs_sg_") {
		t.Error("the public handler emitted Prometheus metrics")
	}
}

// ...and the split must not have simply lost the endpoint.
func TestMetricsHandlerServesOnlyMetrics(t *testing.T) {
	s := setupWeb(t)
	if rec := getFrom(t, s.MetricsHandler(), "/metrics"); rec.Code != http.StatusOK {
		t.Errorf("/metrics on the metrics handler = %d, want 200", rec.Code)
	}
	// the site must not be a second time reachable on the scrape port
	for _, path := range []string{"/", "/tech", "/pay", "/ops"} {
		if rec := getFrom(t, s.MetricsHandler(), path); rec.Code != http.StatusNotFound {
			t.Errorf("%s on the metrics handler = %d, want 404", path, rec.Code)
		}
	}
}
