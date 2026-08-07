package ingest

import (
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
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

// The weekly reconcile walks the whole live board, so archiving what it already
// holds would multiply the archive's yearly growth by ~5 against a PVC sized for
// five years (docs/03 §3). It archives only what it has never stored.
func TestReconcileArchivesOnlyPostingsItHasNotStored(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

	rt := &pageRT{pages: [][]mcf.Job{{sweJob("a", "2026-08-01T00:00:00Z"), sweJob("b", "2026-08-01T00:00:00Z")}}, total: 2}
	if _, err := Run(ctx, Config{DataDir: dir, Transport: rt, Now: func() time.Time { return now }, Delay: 0}); err != nil {
		t.Fatal(err)
	}
	if got := countArchiveRecords(t, dir); got != 2 {
		t.Fatalf("after baseline: archived records = %d, want 2", got)
	}

	// reconcile re-sights a and b, and finds c for the first time
	rt2 := &pageRT{pages: [][]mcf.Job{{
		sweJob("a", "2026-08-01T00:00:00Z"),
		sweJob("b", "2026-08-01T00:00:00Z"),
		sweJob("c", "2026-08-01T00:00:00Z"),
	}}, total: 3}
	res, err := Run(ctx, Config{DataDir: dir, Transport: rt2, Now: func() time.Time { return now }, Delay: 0, Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Seen != 1 {
		t.Errorf("archived = %d, want 1 (only the newly discovered c)", res.Seen)
	}
	if got := countArchiveRecords(t, dir); got != 3 {
		t.Errorf("archived records = %d, want 3 (2 from baseline + c); re-archiving the board is what fills the PVC", got)
	}
	if res.New != 1 {
		t.Errorf("new = %d, want 1 (c)", res.New)
	}
	// c must carry a real archive pointer, or enrich can never read its
	// description back and it sits in the backlog forever
	db, err := store.Open(dir+"/jobs.db", true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var rawPath string
	if err := db.QueryRowContext(ctx, "SELECT raw_path FROM job WHERE uuid='c'").Scan(&rawPath); err != nil {
		t.Fatal(err)
	}
	if rawPath == "" {
		t.Errorf("c raw_path is empty; a posting first stored by the reconcile still needs its own archived copy")
	}
}

// A reconcile that archives nothing for a posting must not blank the pointer to
// the copy an earlier run archived: raw_path is how enrich reads descriptions
// back, so wiping it strands every re-sighted posting in the enrich backlog.
func TestReconcileKeepsRawPathItDidNotRewrite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

	rt := &pageRT{pages: [][]mcf.Job{{sweJob("a", "2026-08-01T00:00:00Z")}}, total: 1}
	if _, err := Run(ctx, Config{DataDir: dir, Transport: rt, Now: func() time.Time { return now }, Delay: 0}); err != nil {
		t.Fatal(err)
	}
	readRawPath := func() string {
		t.Helper()
		db, err := store.Open(dir+"/jobs.db", true)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var p string
		if err := db.QueryRowContext(ctx, "SELECT raw_path FROM job WHERE uuid='a'").Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	before := readRawPath()
	if before == "" {
		t.Fatal("baseline stored an empty raw_path")
	}

	rt2 := &pageRT{pages: [][]mcf.Job{{sweJob("a", "2026-08-01T00:00:00Z")}}, total: 1}
	if _, err := Run(ctx, Config{DataDir: dir, Transport: rt2, Now: func() time.Time { return now }, Delay: 0, Reconcile: true}); err != nil {
		t.Fatal(err)
	}
	if after := readRawPath(); after != before {
		t.Errorf("raw_path after reconcile = %q, want %q unchanged", after, before)
	}
}

// MCF reports expiry_date as a bare Singapore-local date, and the reconcile
// runs at 02:15 SGT — 18:15 UTC the day before. Comparing against the UTC date
// therefore asked "did this expire before yesterday?", so a posting that
// expired yesterday survived the expiry rule and fell through to the slower
// two-week miss_count path.
func TestReconcileClosesExpiredAgainstTheSGTDate(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	// 18:15 UTC on the 6th == 02:15 SGT on the 7th, the real nightly slot
	runAt := time.Date(2026, 8, 6, 18, 15, 0, 0, time.UTC)
	now := func() time.Time { return runAt }

	// expired at the end of the 6th SGT: dead by the time this run starts
	job := sweJob("a", "2026-07-01")
	job.Metadata.ExpiryDate = "2026-08-06"
	rt := &pageRT{pages: [][]mcf.Job{{job}}, total: 1}
	if _, err := Run(ctx, Config{DataDir: dir, Transport: rt, Now: now, Delay: 0}); err != nil {
		t.Fatal(err)
	}

	rt2 := &pageRT{pages: [][]mcf.Job{{job}}, total: 1}
	res, err := Run(ctx, Config{DataDir: dir, Transport: rt2, Now: now, Delay: 0, Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Closed != 1 {
		t.Errorf("closed = %d, want 1 — expiry_date 2026-08-06 is past on 2026-08-07 SGT", res.Closed)
	}
	db, err := store.Open(dir+"/jobs.db", true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var closedAt sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT closed_at FROM job WHERE uuid='a'").Scan(&closedAt); err != nil {
		t.Fatal(err)
	}
	if !closedAt.Valid {
		t.Error("an expired posting should carry closed_at after a successful reconcile")
	}
}

// A posting that comes back after being closed must reopen. The store-level
// lifecycle test cannot stand in for this one: reopen only matters if the path
// ingest actually walks performs it, and for a while it did not — the only code
// that cleared closed_at was a store helper no caller reached.
func TestReconcileReopensReturningJob(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	both := func() *pageRT {
		return &pageRT{pages: [][]mcf.Job{{sweJob("a", "2026-08-01T00:00:00Z"), sweJob("b", "2026-08-01T00:00:00Z")}}, total: 2}
	}
	onlyA := func() *pageRT {
		return &pageRT{pages: [][]mcf.Job{{sweJob("a", "2026-08-01T00:00:00Z")}}, total: 1}
	}
	run := func(rt *pageRT, reconcile bool) Result {
		t.Helper()
		res, err := Run(ctx, Config{DataDir: dir, Transport: rt, Now: func() time.Time { return now }, Delay: 0, Reconcile: reconcile})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	run(both(), false) // baseline: a and b open
	run(onlyA(), true) // b missed once
	if res := run(onlyA(), true); res.Closed != 1 {
		t.Fatalf("closed = %d, want 1 (b after two misses)", res.Closed)
	}

	// b is back on the board
	res := run(both(), true)
	if res.New != 0 {
		t.Errorf("new = %d, want 0 (a revived posting is not new demand)", res.New)
	}
	db, err := store.Open(dir+"/jobs.db", true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var closedAt sql.NullString
	var miss int
	if err := db.QueryRowContext(ctx, "SELECT closed_at, miss_count FROM job WHERE uuid='b'").Scan(&closedAt, &miss); err != nil {
		t.Fatal(err)
	}
	if closedAt.Valid {
		t.Errorf("b closed_at = %q, want NULL (reopened)", closedAt.String)
	}
	if miss != 0 {
		t.Errorf("b miss_count = %d, want 0 after reopen", miss)
	}
}

// A run that could not record everything it fetched must not report success.
//
// Previously only an incomplete scan downgraded the status, so a failed archive
// write or upsert bumped res.Errors and the run still said success — the
// posting lands in neither the archive nor the DB, the watermark moves past it,
// and jobs_sg_last_success_timestamp_seconds keeps ticking so nothing alerts.
//
// The failure is induced through job_post_id's UNIQUE constraint: two distinct
// uuids advertising one post id is both realistic and the easiest of the class
// to provoke. Archive-write failures reach the same counter and the same rule.
func TestRunThatDroppedAPostingReportsPartial(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)

	good := sweJob("a", "2026-08-01")
	clash := sweJob("b", "2026-08-01")
	clash.Metadata.JobPostID = good.Metadata.JobPostID // collides on INSERT

	rt := &pageRT{pages: [][]mcf.Job{{good, clash}}, total: 2}
	res, err := Run(ctx, Config{DataDir: dir, Transport: rt, Now: func() time.Time { return now }, Delay: 0})
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors == 0 {
		t.Fatal("the colliding posting should have failed to upsert")
	}
	if res.Status != store.StatusPartial {
		t.Errorf("status = %s, want partial — this run dropped a posting", res.Status)
	}

	db, err := store.Open(dir+"/jobs.db", true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// and the audit row has to agree, since /metrics and the staleness alert
	// read the status from there, not from Result
	var status string
	if err := db.QueryRowContext(ctx,
		`SELECT status FROM ingest_run ORDER BY id DESC LIMIT 1`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != store.StatusPartial {
		t.Errorf("ingest_run.status = %s, want partial", status)
	}
}

// The success gate exists so a partial round never mass-closes (docs/02 §4.1).
// Folding dropped postings into the status means an archive or upsert failure
// now protects the lifecycle too, rather than only a failed scan doing so.
func TestReconcileWithADroppedPostingSkipsClosing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

	// baseline: a and b stored
	rt := &pageRT{pages: [][]mcf.Job{{sweJob("a", "2026-08-01"), sweJob("b", "2026-08-01")}}, total: 2}
	if _, err := Run(ctx, Config{DataDir: dir, Transport: rt, Now: func() time.Time { return now }, Delay: 0}); err != nil {
		t.Fatal(err)
	}

	// reconcile sees only a, plus a new posting that cannot be stored
	clash := sweJob("c", "2026-08-01")
	clash.Metadata.JobPostID = "MCF-a" // collides with the stored posting a
	rt2 := &pageRT{pages: [][]mcf.Job{{sweJob("a", "2026-08-01"), clash}}, total: 2}
	res, err := Run(ctx, Config{DataDir: dir, Transport: rt2, Now: func() time.Time { return now }, Delay: 0, Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != store.StatusPartial {
		t.Fatalf("status = %s, want partial", res.Status)
	}
	if res.Closed != 0 {
		t.Errorf("closed = %d, want 0 — a partial round must not touch the lifecycle", res.Closed)
	}
	db, err := store.Open(dir+"/jobs.db", true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var miss int
	if err := db.QueryRowContext(ctx, "SELECT miss_count FROM job WHERE uuid='b'").Scan(&miss); err != nil {
		t.Fatal(err)
	}
	if miss != 0 {
		t.Errorf("b miss_count = %d, want 0 — an incomplete round must not count a miss", miss)
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
