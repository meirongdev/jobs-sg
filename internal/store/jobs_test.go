package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/meirongdev/jobs-sg/internal/classify"
	"github.com/meirongdev/jobs-sg/internal/mcf"
)

func TestUpsertJobInsertThenUpdate(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cl := classify.New(map[string]string{"25121": "Backend"})
	base := mcf.Job{
		UUID: "u1", Title: "Backend Engineer", Description: "<p>Go API</p>",
		Metadata: mcf.Metadata{JobPostID: "MCF-1", NewPostingDate: "2026-08-01T00:00:00Z", ExpiryDate: "2026-09-01T00:00:00Z", RepostCount: 0, TotalNumberOfView: 10, TotalNumberJobApplication: 2},
		SSOCCode: "25121", Categories: []mcf.Category{{Category: "Information Technology"}},
		Skills:        []mcf.Skill{{Skill: "Go", IsKeySkill: true}},
		PostedCompany: &mcf.PostedCompany{UEN: "UEN1", Name: "ACME", SSICCode: "62011", EmployeeCount: intPtr(500)},
	}
	res := cl.Classify(base)
	new, err := db.UpsertJob(ctx, base, res, "raw/2026-08-03/000.jsonl.gz#0")
	if err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	if !new {
		t.Fatal("first insert should be new")
	}

	// same uuid refresh: repost_count increments, description changes
	base.Metadata.RepostCount = 1
	base.Description = "<p>Go + Kubernetes API</p>"
	new, err = db.UpsertJob(ctx, base, res, "raw/2026-08-03/000.jsonl.gz#1")
	if err != nil {
		t.Fatalf("UpsertJob refresh: %v", err)
	}
	if new {
		t.Fatal("refresh must UPDATE, not INSERT")
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM job WHERE uuid='u1'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("job rows = %d, want 1 (uuid dedup)", n)
	}
	var repost int
	if err := db.QueryRowContext(ctx, "SELECT repost_count FROM job WHERE uuid='u1'").Scan(&repost); err != nil {
		t.Fatal(err)
	}
	if repost != 1 {
		t.Errorf("repost_count = %d, want 1", repost)
	}
	// skills replaced
	var skillCount int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM job_skill WHERE job_uuid='u1'").Scan(&skillCount); err != nil {
		t.Fatal(err)
	}
	if skillCount != 1 {
		t.Errorf("skill rows = %d, want 1", skillCount)
	}
	// watermark = posting date
	wm, err := db.QueryWatermark(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !wm.Valid || wm.String != "2026-08-01T00:00:00Z" {
		t.Errorf("watermark = %+v", wm)
	}
}

func TestReconcileLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cl := classify.New(nil)
	// see() is how a scan records a posting it found — the same call ingest
	// makes, so what this test exercises is the production path rather than a
	// store helper written only for it. Returns whether a row was inserted.
	see := func(uuid, expiry string, view, app int) bool {
		j := mcf.Job{UUID: uuid, Title: "Software Engineer", Description: "d",
			Metadata: mcf.Metadata{JobPostID: "MCF-" + uuid, NewPostingDate: "2026-08-01T00:00:00Z", ExpiryDate: expiry, TotalNumberOfView: view, TotalNumberJobApplication: app},
			SSOCCode: "25121", Categories: []mcf.Category{{Category: "Information Technology"}}}
		isNew, err := db.UpsertJob(ctx, j, cl.Classify(j), "raw/2026-08-03/000.jsonl.gz#0")
		if err != nil {
			t.Fatal(err)
		}
		return isNew
	}
	see("a", "2026-12-01T00:00:00Z", 5, 1) // stays open
	see("b", "2026-09-01T00:00:00Z", 5, 1) // expired -> close by expiry
	see("c", "2026-12-01T00:00:00Z", 5, 1) // disappears for 2 rounds

	// Round 1: seen = {a, b}
	see("a", "2026-12-01T00:00:00Z", 6, 2)
	see("b", "2026-09-01T00:00:00Z", 7, 3)
	today := "2026-10-01"
	// Round 1 sees a only. b is unseen AND expired -> closes immediately;
	// c is unseen but not expired -> one miss, no close.
	expired1, closed1, err := db.MissAndClose(ctx, map[string]bool{"a": true}, today)
	if err != nil {
		t.Fatal(err)
	}
	if expired1 != 1 {
		t.Errorf("expired = %d, want 1 (job b)", expired1)
	}
	if closed1 != 0 {
		t.Errorf("round1 closed = %d, want 0 (need 2 misses)", closed1)
	}
	// b closed by expiry, not by the miss rule
	var closedB sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT closed_at FROM job WHERE uuid='b'").Scan(&closedB); err != nil {
		t.Fatal(err)
	}
	if !closedB.Valid {
		t.Error("b is unseen and past expiry; it should close on this round")
	}
	var missC int
	if err := db.QueryRowContext(ctx, "SELECT miss_count FROM job WHERE uuid='c'").Scan(&missC); err != nil {
		t.Fatal(err)
	}
	if missC != 1 {
		t.Errorf("c miss_count = %d, want 1", missC)
	}

	// Round 2: c still unseen -> miss_count=2 -> closed
	_, closed2, err := db.MissAndClose(ctx, map[string]bool{"a": true}, today)
	if err != nil {
		t.Fatal(err)
	}
	if closed2 != 1 {
		t.Errorf("round2 closed = %d, want 1 (job c)", closed2)
	}
	var closedAt sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT closed_at FROM job WHERE uuid='c'").Scan(&closedAt); err != nil {
		t.Fatal(err)
	}
	if !closedAt.Valid {
		t.Errorf("c should be closed after 2 misses")
	}
	// a job present in the seen set is never touched, even carrying a stale
	// miss_count from earlier rounds
	if _, err := db.ExecContext(ctx, `UPDATE job SET miss_count=5 WHERE uuid='a'`); err != nil {
		t.Fatal(err)
	}
	if _, closed, err := db.MissAndClose(ctx, map[string]bool{"a": true}, today); err != nil || closed != 0 {
		t.Errorf("seen job with stale miss_count: closed = %d (err %v), want 0", closed, err)
	}
	var missA int
	var closedA sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT miss_count, closed_at FROM job WHERE uuid='a'").Scan(&missA, &closedA); err != nil {
		t.Fatal(err)
	}
	if missA != 5 || closedA.Valid {
		t.Errorf("seen job = miss_count %d closed %v, want 5 and open", missA, closedA.Valid)
	}

	// a's counts refreshed by being seen again
	var view int
	if err := db.QueryRowContext(ctx, "SELECT view_count FROM job WHERE uuid='a'").Scan(&view); err != nil {
		t.Fatal(err)
	}
	if view != 6 {
		t.Errorf("a view_count = %d, want 6", view)
	}

	// reopen: a previously-closed job seen again -> closed_at NULL, miss reset,
	// and no new row (BDD "reopen 不清除新增归属": a revived posting must not
	// register as this week's new demand)
	if isNew := see("c", "2026-12-01T00:00:00Z", 9, 4); isNew {
		t.Errorf("reopening c inserted a new row; want an update")
	}
	if err := db.QueryRowContext(ctx, "SELECT closed_at, miss_count FROM job WHERE uuid='c'").Scan(&closedAt, &missC); err != nil {
		t.Fatal(err)
	}
	if closedAt.Valid {
		t.Errorf("c should be reopened (closed_at NULL)")
	}
	if missC != 0 {
		t.Errorf("c miss_count after reopen = %d, want 0", missC)
	}
}

func TestRunsAudit(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := db.StartRun(ctx, RunIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.FinishRun(ctx, id, StatusSuccess, 10, 1000, 5, 2, 1, 0, 0, 0, "2026-08-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	ts, ok, err := db.LastSuccess(ctx, RunIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected last success timestamp")
	}
	if ts.IsZero() {
		t.Fatal("expected non-zero timestamp")
	}
	// start a second run and leave running; last success should still be the first
	if _, err := db.StartRun(ctx, RunIncremental); err != nil {
		t.Fatal(err)
	}
	ts2, ok2, err := db.LastSuccess(ctx, RunIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if !ok2 || !ts2.Equal(ts) {
		t.Fatalf("expected unchanged last success, got %v (ok=%v)", ts2, ok2)
	}
	_ = time.Now
}

func intPtr(i int) *int { return &i }
