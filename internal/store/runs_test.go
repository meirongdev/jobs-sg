package store

import (
	"context"
	"testing"
	"time"
)

// insertRun writes a finished ingest_run row with a controlled ended_at.
// FinishRun stamps NowUTC (second precision), so two runs inside one second —
// which is every test — cannot be ordered by it; explicit timestamps can.
func insertRun(t *testing.T, db *DB, kind, status, endedAt string, errors int) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO ingest_run(kind, started_at, ended_at, status, errors) VALUES(?,?,?,?,?)`,
		kind, endedAt, endedAt, status, errors); err != nil {
		t.Fatal(err)
	}
}

func wantLastSuccess(t *testing.T, db *DB, kind, want string) {
	t.Helper()
	ts, ok, err := db.LastSuccess(context.Background(), kind)
	if err != nil {
		t.Fatal(err)
	}
	if want == "" {
		if ok {
			t.Errorf("LastSuccess(%s) = %s, want none", kind, ts.Format(time.RFC3339))
		}
		return
	}
	if !ok {
		t.Errorf("LastSuccess(%s) = none, want %s", kind, want)
		return
	}
	if got := ts.UTC().Format(time.RFC3339); got != want {
		t.Errorf("LastSuccess(%s) = %s, want %s", kind, got, want)
	}
}

// A reconcile whose scan was clean refreshes the data even when the close gate
// declined to act on absence, so it advances the *incremental* freshness stamp
// — while the *full_reconcile* stamp stays withheld, because the lifecycle was
// exactly what that round did not reconcile. Before this split, one cautious
// Sunday withheld both stamps at once and JobsSgIngestStale spent hours
// claiming data was stale that a full sweep had just refreshed; collapsing the
// two stamps into one is also what would blind JobsSgReconcileStale to a gate
// stuck cautious for weeks.
func TestLastSuccessIncrementalAcceptsCleanReconcileScan(t *testing.T) {
	db := openTestDB(t)
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	insertRun(t, db, RunIncremental, StatusSuccess, "2026-08-07T18:19:53Z", 0)
	// close-skipped round: clean scan (errors=0), recorded partial by the gate
	insertRun(t, db, RunReconcile, StatusPartial, "2026-08-08T18:40:14Z", 0)

	wantLastSuccess(t, db, RunIncremental, "2026-08-08T18:40:14Z")
	wantLastSuccess(t, db, RunReconcile, "")

	// an errorful partial reconcile is a broken round, not a cautious one —
	// it must advance neither stamp
	insertRun(t, db, RunReconcile, StatusPartial, "2026-08-09T18:40:14Z", 1)
	wantLastSuccess(t, db, RunIncremental, "2026-08-08T18:40:14Z")
	wantLastSuccess(t, db, RunReconcile, "")

	// a fully successful reconcile advances both
	insertRun(t, db, RunReconcile, StatusSuccess, "2026-08-10T18:42:00Z", 0)
	wantLastSuccess(t, db, RunIncremental, "2026-08-10T18:42:00Z")
	wantLastSuccess(t, db, RunReconcile, "2026-08-10T18:42:00Z")

	// the special case is incremental's alone: enrich and report never widen
	wantLastSuccess(t, db, RunEnrich, "")
}
