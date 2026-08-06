package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meirongdev/jobs-sg/internal/classify"
	"github.com/meirongdev/jobs-sg/internal/mcf"
	"github.com/meirongdev/jobs-sg/internal/store"
)

func setupWeb(t *testing.T) *Server {
	t.Helper()
	s, _ := setupWebClock(t, nil)
	return s
}

// setupWebClock builds the server with an injectable clock and hands back the
// data dir so tests can write to the DB behind the server's read-only handle.
func setupWebClock(t *testing.T, now func() time.Time) (*Server, string) {
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

	srv, err := New(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv, dir
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

// sgtToday is the SGT calendar day the daily pages bucket "now" into.
func sgtToday() string {
	return time.Now().In(time.FixedZone("SGT", 8*3600)).Format("2006-01-02")
}

func TestDailyOverviewRoute(t *testing.T) {
	s := setupWeb(t)
	rec := get(t, s, "/ops")
	if rec.Code != http.StatusOK {
		t.Fatalf("/ops = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	// rendered live from the read-only handle, so today's run must be there
	for _, want := range []string{"Daily Crawl Statistics", sgtToday(), "incr"} {
		if !strings.Contains(body, want) {
			t.Errorf("/ops missing %q", want)
		}
	}
}

func TestDailyWindowParam(t *testing.T) {
	s := setupWeb(t)
	// the fixture has one day of history, so the window collapses to it
	if rec := get(t, s, "/ops?days=3"); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "1 day · "+sgtToday()) {
		t.Errorf("/ops?days=3 = %d, body missing the single-day range", rec.Code)
	}
	for _, bad := range []string{"0", "-1", "500", "abc"} {
		if rec := get(t, s, "/ops?days="+bad); rec.Code != http.StatusBadRequest {
			t.Errorf("/ops?days=%s = %d, want 400", bad, rec.Code)
		}
	}
}

func TestDailyDayRoute(t *testing.T) {
	s := setupWeb(t)
	today := sgtToday()
	rec := get(t, s, "/ops/"+today)
	if rec.Code != http.StatusOK {
		t.Fatalf("/ops/%s = %d", today, rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Crawl Detail — " + today, "Backend Engineer", "Runs"} {
		if !strings.Contains(body, want) {
			t.Errorf("/ops/%s missing %q", today, want)
		}
	}
}

func TestDailyDayRouteRejectsBadDates(t *testing.T) {
	s := setupWeb(t)
	future := time.Now().In(time.FixedZone("SGT", 8*3600)).AddDate(0, 0, 1).Format("2006-01-02")
	for _, path := range []string{
		"/ops/not-a-date",
		"/ops/2026-13-40",
		"/ops/" + future, // no data can exist yet
		"/ops/..%2f..%2fetc",
	} {
		if rec := get(t, s, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}

func TestRobotsKeepsCrawlersOffDrillDowns(t *testing.T) {
	s := setupWeb(t)
	rec := get(t, s, "/robots.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("/robots.txt = %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "Disallow: /ops/") {
		t.Errorf("robots.txt must disallow the day drill-downs, got:\n%s", body)
	}
}

func TestDailyPagesAreCachedUntilTTLExpires(t *testing.T) {
	clock := time.Now()
	s, dir := setupWebClock(t, func() time.Time { return clock })

	before := get(t, s, "/ops").Body.String()

	// write behind the server's read-only handle: without the cache the next
	// request would pick this up immediately
	db, err := store.Open(filepath.Join(dir, "jobs.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, err := db.StartRun(ctx, store.RunEnrich)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.FinishRun(ctx, id, store.StatusPartial, 0, 0, 0, 0, 0, 77, 5, 3, ""); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if cached := get(t, s, "/ops").Body.String(); cached != before {
		t.Error("second request inside the TTL should be served from cache")
	}
	clock = clock.Add(dailyCacheTTL + time.Second)
	after := get(t, s, "/ops").Body.String()
	if after == before {
		t.Error("page should be rebuilt once the TTL expires")
	}
	if !strings.Contains(after, "77 / 5") {
		t.Errorf("rebuilt page missing the new enrich counters:\n%s", after)
	}
	// windows and day pages are keyed separately, so one cannot serve another
	get(t, s, "/ops?days=1")
	get(t, s, "/ops/"+sgtToday())
	for _, key := range []string{"overview:30", "overview:1", "day:" + sgtToday()} {
		if _, ok := s.cache.get(key, clock); !ok {
			t.Errorf("cache missing entry %q", key)
		}
	}
}

func TestPageCacheDropsEntriesAtCapacity(t *testing.T) {
	now := time.Now()
	c := newPageCache(time.Minute, 2)
	c.put("a", "A", now)
	c.put("b", "B", now)
	c.put("c", "C", now) // over capacity: the map is cleared, then c stored
	if _, ok := c.get("c", now); !ok {
		t.Error("newest entry must survive")
	}
	if len(c.entries) > 2 {
		t.Errorf("cache holds %d entries, want <= 2", len(c.entries))
	}
	if _, ok := c.get("c", now.Add(2*time.Minute)); ok {
		t.Error("entry must expire after its TTL")
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

func TestDailyRedirectsToOps(t *testing.T) {
	s := setupWeb(t)
	for from, to := range map[string]string{
		"/daily":               "/ops",
		"/daily?days=7":        "/ops?days=7",
		"/daily/" + sgtToday(): "/ops/" + sgtToday(),
	} {
		rec := get(t, s, from)
		if rec.Code != http.StatusMovedPermanently {
			t.Errorf("GET %s = %d, want 301", from, rec.Code)
		}
		if got := rec.Header().Get("Location"); got != to {
			t.Errorf("GET %s -> %q, want %q", from, got, to)
		}
	}
}

func TestOpsIsNotInTheJobSeekerNav(t *testing.T) {
	// /ops stays reachable — but from the footer as a data-freshness link, not
	// from the nav a job seeker reads. Slice out the nav block and check there,
	// since a plain Contains would also match the footer link.
	s := setupWeb(t)
	body := get(t, s, "/tech").Body.String()
	open := strings.Index(body, `<nav class="nav">`)
	if open < 0 {
		t.Fatal("/tech has no nav block")
	}
	end := strings.Index(body[open:], "</nav>")
	if end < 0 {
		t.Fatal("/tech nav block is unterminated")
	}
	if nav := body[open : open+end]; strings.Contains(nav, "/ops") {
		t.Errorf("nav must not link to /ops, got: %s", nav)
	}
	if !strings.Contains(body, `href="/ops"`) {
		t.Error("/ops must still be reachable from the footer")
	}
}

func TestRedirectTargetDoesNotAccumulateAcrossRequests(t *testing.T) {
	// get() rebuilds the mux per call, resetting redirectTo's closure — which
	// is precisely why it can never catch the accumulation bug the closure's
	// comment warns about. Production builds the handler ONCE (cmd/web), so
	// this test does too, and sends several requests through that instance.
	s := setupWeb(t)
	h := s.Handler()
	do := func(path string) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMovedPermanently {
			t.Fatalf("GET %s = %d, want 301", path, rec.Code)
		}
		return rec.Header().Get("Location")
	}
	if got := do("/daily?days=7"); got != "/ops?days=7" {
		t.Fatalf("first redirect = %q, want /ops?days=7", got)
	}
	// The second request must not inherit the first one's query string.
	if got := do("/daily"); got != "/ops" {
		t.Errorf("second redirect = %q, want bare /ops — the closure accumulated state", got)
	}
	if got := do("/daily?days=3"); got != "/ops?days=3" {
		t.Errorf("third redirect = %q, want /ops?days=3", got)
	}
}
