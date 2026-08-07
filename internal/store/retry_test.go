package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// realBusyError provokes a genuine lock collision and hands back what SQLite
// returned. Constructing a *sqlite.Error by hand is impossible from outside its
// package (unexported fields), so provoking one is the only honest way to test
// the predicate that decides whether a retry happens at all.
//
// Both handles use busy_timeout(1): production sets 10s, and waiting that out
// would make the test slow without making it any more truthful — what is under
// test is the classification of the error, not how long SQLite waits first.
func realBusyError(t *testing.T) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "busy.db")
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(1)&_pragma=journal_mode(DELETE)", path)

	setup, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open setup: %v", err)
	}
	defer setup.Close()
	if _, err := setup.ExecContext(context.Background(), `CREATE TABLE t(x INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	blocker, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open blocker: %v", err)
	}
	defer blocker.Close()
	tx, err := blocker.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("blocker begin: %v", err)
	}
	defer tx.Rollback()
	// The INSERT is what takes the write lock; holding the tx open holds it.
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO t(x) VALUES(1)`); err != nil {
		t.Fatalf("blocker write: %v", err)
	}

	victim, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open victim: %v", err)
	}
	defer victim.Close()
	_, got := victim.ExecContext(context.Background(), `INSERT INTO t(x) VALUES(2)`)
	if got == nil {
		t.Fatal("expected the second writer to be locked out, got nil error")
	}
	return got
}

func TestIsBusyErrRecognisesARealLockCollision(t *testing.T) {
	busy := realBusyError(t)
	if !isBusyErr(busy) {
		t.Errorf("isBusyErr(%v) = false, want true — if this is wrong the retry never engages", busy)
	}
	if isBusyErr(errors.New("some other failure")) {
		t.Error("isBusyErr must not treat an arbitrary error as retryable")
	}
	if isBusyErr(nil) {
		t.Error("isBusyErr(nil) must be false")
	}
	// A wrapped one still has to be recognised: callers wrap freely.
	if !isBusyErr(fmt.Errorf("upsert %q: %w", "u1", busy)) {
		t.Error("isBusyErr must see through a wrapped error")
	}
}

// TestRetryBusyTreatsARealLockErrorAsRetryable wires the two halves together:
// the predicate the production path actually uses, driven by a real SQLite
// lock error, through retryBusy. It does not contend on a live UpsertJob
// because the production DSN sets busy_timeout=10s — SQLite would simply wait
// out a blocker instead of returning BUSY, and the test would pass without the
// retry ever running.
func TestRetryBusyTreatsARealLockErrorAsRetryable(t *testing.T) {
	busy := realBusyError(t)
	db := openTestDB(t)

	calls := 0
	err := db.retryBusy(context.Background(), func() error {
		calls++
		if calls < 3 {
			return busy
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retryBusy = %v, want nil once the lock clears", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3 — the first two lock errors must be retried", calls)
	}
}

// TestRetryStopsForTheRightReasons pins the loop itself. The predicate is a
// parameter precisely so this needs no database.
func TestRetryStopsForTheRightReasons(t *testing.T) {
	always := func(error) bool { return true }
	boom := errors.New("boom")

	t.Run("returns nil without retrying when op succeeds", func(t *testing.T) {
		calls := 0
		err := retry(context.Background(), always, func() error { calls++; return nil })
		if err != nil || calls != 1 {
			t.Errorf("err=%v calls=%d, want nil/1", err, calls)
		}
	})

	t.Run("gives up after the backoff table is exhausted", func(t *testing.T) {
		calls := 0
		err := retry(context.Background(), always, func() error { calls++; return boom })
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want boom", err)
		}
		if want := len(busyBackoff) + 1; calls != want {
			t.Errorf("calls = %d, want %d (one initial attempt plus one per backoff)", calls, want)
		}
	})

	t.Run("does not retry an error the predicate rejects", func(t *testing.T) {
		calls := 0
		never := func(error) bool { return false }
		err := retry(context.Background(), never, func() error { calls++; return boom })
		if !errors.Is(err, boom) || calls != 1 {
			t.Errorf("err=%v calls=%d, want boom/1", err, calls)
		}
	})

	t.Run("stops on a cancelled context and reports the sqlite error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		start := time.Now()
		err := retry(ctx, always, func() error { calls++; return boom })
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want the op's error rather than the context's", err)
		}
		if calls != 1 {
			t.Errorf("calls = %d, want 1 — a cancelled context must not sleep through the table", calls)
		}
		if el := time.Since(start); el > time.Second {
			t.Errorf("took %v, want an immediate return", el)
		}
	})
}
