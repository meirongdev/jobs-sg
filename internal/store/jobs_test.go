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
	mk := func(uuid, expiry string) {
		j := mcf.Job{UUID: uuid, Title: "Software Engineer", Description: "d",
			Metadata: mcf.Metadata{JobPostID: "MCF-" + uuid, NewPostingDate: "2026-08-01T00:00:00Z", ExpiryDate: expiry, TotalNumberOfView: 5, TotalNumberJobApplication: 1},
			SSOCCode: "25121", Categories: []mcf.Category{{Category: "Information Technology"}}}
		if _, err := db.UpsertJob(ctx, j, cl.Classify(j), "raw/2026-08-03/000.jsonl.gz#0"); err != nil {
			t.Fatal(err)
		}
	}
	mk("a", "2026-12-01T00:00:00Z") // stays open
	mk("b", "2026-09-01T00:00:00Z") // expired -> close by expiry
	mk("c", "2026-12-01T00:00:00Z") // disappears for 2 rounds

	// Round 1: seen = {a, b}
	if err := db.MarkSeen(ctx, "a", 6, 2); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkSeen(ctx, "b", 7, 3); err != nil {
		t.Fatal(err)
	}
	today := "2026-10-01"
	closedExpired, err := db.CloseExpired(ctx, today)
	if err != nil {
		t.Fatal(err)
	}
	if closedExpired != 1 {
		t.Errorf("closeExpired = %d, want 1 (job b)", closedExpired)
	}
	// c unseen once -> miss_count=1, not closed
	closed1, err := db.MissAndClose(ctx, map[string]bool{"a": true, "b": true})
	if err != nil {
		t.Fatal(err)
	}
	if closed1 != 0 {
		t.Errorf("round1 closed = %d, want 0 (need 2 misses)", closed1)
	}
	var missC int
	if err := db.QueryRowContext(ctx, "SELECT miss_count FROM job WHERE uuid='c'").Scan(&missC); err != nil {
		t.Fatal(err)
	}
	if missC != 1 {
		t.Errorf("c miss_count = %d, want 1", missC)
	}

	// Round 2: c still unseen -> miss_count=2 -> closed
	closed2, err := db.MissAndClose(ctx, map[string]bool{"a": true})
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
	if closed, err := db.MissAndClose(ctx, map[string]bool{"a": true}); err != nil || closed != 0 {
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

	// a's counts refreshed by MarkSeen
	var view int
	if err := db.QueryRowContext(ctx, "SELECT view_count FROM job WHERE uuid='a'").Scan(&view); err != nil {
		t.Fatal(err)
	}
	if view != 6 {
		t.Errorf("a view_count = %d, want 6", view)
	}

	// reopen: a previously-closed job seen again -> closed_at NULL, miss reset
	if err := db.MarkSeen(ctx, "c", 9, 4); err != nil {
		t.Fatal(err)
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
