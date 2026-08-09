package web

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meirongdev/jobs-sg/internal/store"
)

// A rollout puts a new web image in front of a database no writer has migrated
// yet: ingest, enrich and report migrate on start, the web server opens the file
// read-only and never does. /metrics deliberately fails the entire scrape on a
// DB error, so a hard reference to a column that is not there yet would take
// every jobs-sg alert down until the next 02:15 SGT ingest — including the
// staleness alert whose whole job is to notice outages.
func TestMetricsToleratesAPreMigrationDatabase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "jobs.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// wind the schema back to the shape the live database has until the first
	// migrated writer runs
	for _, col := range []string{"jobs_scanned", "total_reported", "total_min", "total_max", "close_skipped"} {
		if _, err := db.ExecContext(ctx, "ALTER TABLE ingest_run DROP COLUMN "+col); err != nil {
			t.Fatalf("drop %s: %v", col, err)
		}
	}
	if _, err := db.StartRun(ctx, store.RunReconcile); err != nil {
		t.Fatal(err)
	}
	db.Close()

	srv, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })

	rec := getFrom(t, srv.MetricsHandler(), "/metrics")
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics = %d, want 200 — a column the writers have not added yet must not fail the scrape", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "jobs_sg_reconcile_scan_deviation_ratio") {
		t.Error("the audit family should be omitted, not fabricated, while the columns are absent")
	}
	if !strings.Contains(body, "jobs_sg_jobs{") {
		t.Error("the rest of the exposition must still be served")
	}
}

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
