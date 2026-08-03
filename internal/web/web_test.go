package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meirongdev/jobs-sg/internal/classify"
	"github.com/meirongdev/jobs-sg/internal/mcf"
	"github.com/meirongdev/jobs-sg/internal/store"
)

func setupWeb(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "jobs.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	cl := classify.New(map[string]string{"25121": "Backend"})
	j := mcf.Job{
		UUID: "u1", Title: "Backend Engineer", Description: "d",
		Metadata: mcf.Metadata{JobPostID: "MCF-u1", NewPostingDate: "2026-08-03T00:00:00Z"},
		SSOCCode: "25121", Categories: []mcf.Category{{Category: "Information Technology"}},
	}
	if _, err := db.UpsertJob(ctx, j, cl.Classify(j), "raw/2026-08-03/000.jsonl.gz#0"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.StartRun(ctx, store.RunIncremental); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishRun(ctx, 1, store.StatusSuccess, 1, 1, 1, 0, 0, 0, 0, 0, "2026-08-03T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// report files
	if err := os.MkdirAll(filepath.Join(dir, "report"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report", "latest.html"), []byte("<h1>latest</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report", "2026-W32.html"), []byte("<h1>W32</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

func get(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestHealthz(t *testing.T) {
	s := setupWeb(t)
	if rec := get(t, s, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("/healthz = %d, want 200", rec.Code)
	}
}

func TestRootServesLatest(t *testing.T) {
	s := setupWeb(t)
	rec := get(t, s, "/")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "latest") {
		t.Errorf("root = %d %q", rec.Code, rec.Body.String())
	}
}

func TestWeekRoute(t *testing.T) {
	s := setupWeb(t)
	if rec := get(t, s, "/w/2026-W32"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "W32") {
		t.Errorf("/w/2026-W32 = %d", rec.Code)
	}
	// missing week -> 404
	if rec := get(t, s, "/w/1999-W01"); rec.Code != http.StatusNotFound {
		t.Errorf("/w/1999-W01 = %d, want 404", rec.Code)
	}
}

func TestPathTraversalBlocked(t *testing.T) {
	s := setupWeb(t)
	if rec := get(t, s, "/w/..%2f..%2fetc"); rec.Code != http.StatusNotFound {
		t.Errorf("traversal = %d, want 404", rec.Code)
	}
}

func TestMetricsFromDB(t *testing.T) {
	s := setupWeb(t)
	rec := get(t, s, "/metrics")
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"jobs_sg_last_success_timestamp_seconds{kind=\"incremental\"}",
		"jobs_sg_jobs_total{state=\"active\"} 1",
		"jobs_sg_jobs_total{state=\"closed\"} 0",
		"jobs_sg_enrich_backlog",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q\n%s", want, body)
		}
	}
}
