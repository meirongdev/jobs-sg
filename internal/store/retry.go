package store

import (
	"context"
	"errors"
	"time"

	sqlite "modernc.org/sqlite"
)

// SQLite primary result codes. Extended codes carry these in the low byte
// (SQLITE_BUSY_SNAPSHOT is 5 | 2<<8), so callers mask before comparing.
const (
	sqliteBusy   = 5 // SQLITE_BUSY
	sqliteLocked = 6 // SQLITE_LOCKED
)

// busyBackoff is the delay before each retry. Five attempts spend <800ms in
// the worst case, which is nothing against a run measured in minutes, and the
// contention it exists for clears in milliseconds — the reader holding us off
// is one page render.
var busyBackoff = []time.Duration{
	25 * time.Millisecond,
	50 * time.Millisecond,
	100 * time.Millisecond,
	200 * time.Millisecond,
	400 * time.Millisecond,
}

// isBusyErr reports whether err is SQLite telling us the database was locked by
// another connection, as opposed to something wrong with the statement.
func isBusyErr(err error) bool {
	var serr *sqlite.Error
	if !errors.As(err, &serr) {
		return false
	}
	switch serr.Code() & 0xff {
	case sqliteBusy, sqliteLocked:
		return true
	}
	return false
}

// retry re-runs op while retryable says its error is transient.
//
// ⚠️ busy_timeout does NOT cover the case this exists for. In rollback-journal
// mode a writer takes RESERVED, then needs to upgrade to EXCLUSIVE to commit.
// If a reader still holds SHARED at that moment, SQLite returns SQLITE_BUSY
// *immediately without invoking the busy handler* — sleeping there could
// deadlock, because the reader may itself be waiting on this writer. So no
// busy_timeout value, however large, can help; only re-running the whole
// transaction can. Ours is set to 10s and still lost (2026-08-08, one upsert of
// a 13600-posting run, while the web pod was rendering an aggregate page).
//
// That single dropped posting cost the whole run its status: any error marks a
// run `partial` (ingest.go), and a `partial` reconcile skips the close pass
// entirely — so one lock collision during the 20-minute Sunday walk silently
// costs a week of lifecycle data. Retrying is much cheaper than that.
//
// The predicate is a parameter so the loop can be tested without conjuring a
// *sqlite.Error, whose fields are unexported and cannot be built from outside
// its package.
func retry(ctx context.Context, retryable func(error) bool, op func() error) error {
	var err error
	for attempt := 0; ; attempt++ {
		if err = op(); err == nil || !retryable(err) {
			return err
		}
		if attempt >= len(busyBackoff) {
			return err
		}
		select {
		case <-ctx.Done():
			// Return the SQLite error, not the context one: the caller is
			// deciding whether the round was clean, and "the DB was locked" is
			// the more useful thing to log than "and then we ran out of time".
			return err
		case <-time.After(busyBackoff[attempt]):
		}
	}
}

// retryBusy re-runs op while it fails with a lock error.
func (d *DB) retryBusy(ctx context.Context, op func() error) error {
	return retry(ctx, isBusyErr, op)
}
