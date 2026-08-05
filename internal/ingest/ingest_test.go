package ingest

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meirongdev/jobs-sg/internal/mcf"
	"github.com/meirongdev/jobs-sg/internal/store"
)

type pageRT struct {
	pages [][]mcf.Job
	total int
	idx   int
}

func (p *pageRT) RoundTrip(r *http.Request) (*http.Response, error) {
	var jobs []mcf.Job
	if p.idx < len(p.pages) {
		jobs = p.pages[p.idx]
		p.idx++
	}
	body, _ := json.Marshal(mcf.Page{Results: jobs, Total: p.total})
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
}

func sweJob(uuid, date string) mcf.Job {
	return mcf.Job{
		UUID: uuid, Title: "Backend Engineer", Description: "<p>Go API</p>",
		Metadata: mcf.Metadata{JobPostID: "MCF-" + uuid, NewPostingDate: date, ExpiryDate: "2026-12-31T00:00:00Z", TotalNumberOfView: 5, TotalNumberJobApplication: 1},
		SSOCCode: "25121", Categories: []mcf.Category{{Category: "Information Technology"}},
		PostedCompany: &mcf.PostedCompany{UEN: "UEN-" + uuid, Name: "ACME", SSICCode: "62011", EmployeeCount: intPtr(500)},
	}
}

func nonSWEJob(uuid, date string) mcf.Job {
	return mcf.Job{
		UUID: uuid, Title: "Receptionist", Description: "<p>front desk</p>",
		Metadata: mcf.Metadata{JobPostID: "MCF-" + uuid, NewPostingDate: date, ExpiryDate: "2026-12-31T00:00:00Z"},
		SSOCCode: "99999", Categories: []mcf.Category{{Category: "Administration"}},
	}
}

func TestFirstRunBaselineArchivesAllAndUpsertsCandidates(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	// page 0: 1 SWE + 1 non-SWE; page 1: 1 SWE -> total 3
	rt := &pageRT{
		pages: [][]mcf.Job{
			{sweJob("a", "2026-08-01T00:00:00Z"), nonSWEJob("x", "2026-08-01T00:00:00Z")},
			{sweJob("b", "2026-07-31T00:00:00Z")},
		},
		total: 3,
	}
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	res, err := Run(ctx, Config{DataDir: dir, Transport: rt, Now: func() time.Time { return now }, Delay: 0, BackoffWindow: 48 * time.Hour})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Kind != store.RunIncremental {
		t.Errorf("kind = %s, want incremental (baseline recorded as incremental)", res.Kind)
	}
	if res.Status != store.StatusSuccess {
		t.Errorf("status = %s, want success", res.Status)
	}
	if res.Pages != 3 || res.Seen != 3 {
		t.Errorf("pages=%d seen=%d, want 3 (2 data + terminator) / 3", res.Pages, res.Seen)
	}
	if res.New != 2 {
		t.Errorf("new = %d, want 2 (candidates a,b)", res.New)
	}
	// all 3 archived, only 2 candidates in DB
	db, err := store.Open(dir+"/jobs.db", true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var jobs, archived int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM job").Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 2 {
		t.Errorf("job rows = %d, want 2", jobs)
	}
	// archive file exists with 3 records
	archived = countArchiveRecords(t, dir)
	if archived != 3 {
		t.Errorf("archived records = %d, want 3 (all categories)", archived)
	}
}

func TestIncrementalStopsAtWatermarkWindow(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	// first run: establishes watermark 2026-08-01
	rt := &pageRT{pages: [][]mcf.Job{{sweJob("a", "2026-08-01T00:00:00Z")}}, total: 1}
	if _, err := Run(ctx, Config{DataDir: dir, Transport: rt, Now: func() time.Time { return now }, Delay: 0}); err != nil {
		t.Fatal(err)
	}

	// second run: page0 has one job newer than watermark and one older than
	// watermark-2d; the older one must stop paging and NOT be upserted
	rt2 := &pageRT{
		pages: [][]mcf.Job{
			{sweJob("c", "2026-08-02T00:00:00Z"), sweJob("d", "2026-07-01T00:00:00Z")},
			{sweJob("e", "2026-06-01T00:00:00Z")},
		},
		total: 3,
	}
	res, err := Run(ctx, Config{DataDir: dir, Transport: rt2, Now: func() time.Time { return now }, Delay: 0, BackoffWindow: 48 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pages != 1 {
		t.Errorf("pages = %d, want 1 (stopped at old job on page 0; no terminator fetched)", res.Pages)
	}
	if res.Status != store.StatusSuccess {
		t.Errorf("status = %s, want success", res.Status)
	}
	// c is new; d and e are outside window -> not inserted
	db, err := store.Open(dir+"/jobs.db", true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM job WHERE uuid IN ('c','d','e')").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("inserted in-window jobs = %d, want 1 (only c)", n)
	}
	// archive still contains the whole page including the out-of-window job d
	// (archive-before-parse): 1 prior (a) + 2 on page (c,d) = 3
	if got := countArchiveRecords(t, dir); got != 3 {
		t.Errorf("archived total = %d, want 3", got)
	}
}

// TestIncrementalStopsWithDateOnlyPostingDates is the same scenario in the
// format the live API actually returns ("2026-08-03", not RFC3339). This was
// a production bug: RFC3339-only parsing made both the watermark and the
// per-job stop check fail silently, so every incremental scan ran to the
// page-limit circuit breaker and finished partial.
func TestIncrementalStopsWithDateOnlyPostingDates(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	rt := &pageRT{pages: [][]mcf.Job{{sweJob("a", "2026-08-01")}}, total: 1}
	if _, err := Run(ctx, Config{DataDir: dir, Transport: rt, Now: func() time.Time { return now }, Delay: 0}); err != nil {
		t.Fatal(err)
	}

	rt2 := &pageRT{
		pages: [][]mcf.Job{
			{sweJob("c", "2026-08-02"), sweJob("d", "2026-07-01")},
			{sweJob("e", "2026-06-01")},
		},
		total: 3,
	}
	res, err := Run(ctx, Config{DataDir: dir, Transport: rt2, Now: func() time.Time { return now }, Delay: 0, BackoffWindow: 48 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pages != 1 {
		t.Errorf("pages = %d, want 1 (early stop must work on date-only postings)", res.Pages)
	}
	if res.Status != store.StatusSuccess {
		t.Errorf("status = %s, want success (partial means the circuit breaker fired)", res.Status)
	}
	if res.Errors != 0 {
		t.Errorf("errors = %d, want 0", res.Errors)
	}
}

func TestReconcileClosesAfterTwoMisses(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	// baseline: a and b open
	rt := &pageRT{pages: [][]mcf.Job{{sweJob("a", "2026-08-01T00:00:00Z"), sweJob("b", "2026-08-01T00:00:00Z")}}, total: 2}
	if _, err := Run(ctx, Config{DataDir: dir, Transport: rt, Now: func() time.Time { return now }, Delay: 0}); err != nil {
		t.Fatal(err)
	}

	// reconcile round 1: only a seen -> b miss_count=1, not closed
	rt2 := &pageRT{pages: [][]mcf.Job{{sweJob("a", "2026-08-01T00:00:00Z")}}, total: 1}
	res1, err := Run(ctx, Config{DataDir: dir, Transport: rt2, Now: func() time.Time { return now }, Delay: 0, Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	if res1.Kind != store.RunReconcile {
		t.Errorf("kind = %s, want full_reconcile", res1.Kind)
	}
	if res1.Status != store.StatusSuccess {
		t.Errorf("status = %s, want success", res1.Status)
	}
	db, err := store.Open(dir+"/jobs.db", true)
	if err != nil {
		t.Fatal(err)
	}
	var miss int
	if err := db.QueryRowContext(ctx, "SELECT miss_count FROM job WHERE uuid='b'").Scan(&miss); err != nil {
		t.Fatal(err)
	}
	if miss != 1 {
		t.Errorf("b miss_count = %d, want 1 (not closed after one miss)", miss)
	}
	db.Close()

	// reconcile round 2: still only a seen -> b miss_count=2 -> closed
	rt3 := &pageRT{pages: [][]mcf.Job{{sweJob("a", "2026-08-01T00:00:00Z")}}, total: 1}
	res2, err := Run(ctx, Config{DataDir: dir, Transport: rt3, Now: func() time.Time { return now }, Delay: 0, Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Closed != 1 {
		t.Errorf("closed = %d, want 1 (b after two misses)", res2.Closed)
	}
	db, err = store.Open(dir+"/jobs.db", true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var closed int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM job WHERE uuid='b' AND closed_at IS NOT NULL").Scan(&closed); err != nil {
		t.Fatal(err)
	}
	if closed != 1 {
		t.Errorf("closed rows for b = %d, want 1", closed)
	}
}

func TestCircuitBreakerMarksPartial(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	// establish a watermark first so the next run is a plain incremental
	seed := &pageRT{pages: [][]mcf.Job{{sweJob("seed", "2026-08-01T00:00:00Z")}}, total: 1}
	if _, err := Run(ctx, Config{DataDir: dir, Transport: seed, Now: func() time.Time { return now }, Delay: 0}); err != nil {
		t.Fatal(err)
	}
	rt := &pageRT{
		pages: [][]mcf.Job{
			{sweJob("a", "2026-08-02T00:00:00Z")},
			{sweJob("b", "2026-08-02T00:00:00Z")},
			{sweJob("c", "2026-08-02T00:00:00Z")},
		},
		total: 3,
	}
	res, err := Run(ctx, Config{DataDir: dir, Transport: rt, Now: func() time.Time { return now }, Delay: 0, MaxPages: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != store.StatusPartial {
		t.Errorf("status = %s, want partial (circuit open)", res.Status)
	}
	if res.New != 2 {
		t.Errorf("new = %d, want 2 (data preserved before breaker)", res.New)
	}
}

func countArchiveRecords(t *testing.T, dir string) int {
	t.Helper()
	matches, err := filepathGlob(dir + "/raw/*/*.jsonl.gz")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range matches {
		n += countLinesInGz(t, m)
	}
	return n
}

func filepathGlob(pattern string) ([]string, error) { return filepath.Glob(pattern) }

func countLinesInGz(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	sc := bufio.NewScanner(gz)
	n := 0
	for sc.Scan() {
		n++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return n
}

func intPtr(i int) *int { return &i }
