package store

import (
	"context"
	"database/sql"
	"time"
)

// Run kinds (docs/03 §2 ingest_run.kind).
const (
	RunIncremental  = "incremental"
	RunReconcile    = "full_reconcile"
	RunEnrich       = "enrich"
	RunReport       = "report"
)

// Run statuses.
const (
	StatusRunning = "running"
	StatusSuccess = "success"
	StatusPartial = "partial"
	StatusFailed  = "failed"
)

// StartRun inserts a running audit row and returns its id.
func (d *DB) StartRun(ctx context.Context, kind string) (int64, error) {
	res, err := d.ExecContext(ctx,
		`INSERT INTO ingest_run(kind, started_at, status) VALUES(?,?,?)`,
		kind, NowUTC(), StatusRunning)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishRun updates an audit row with its final counters and status.
func (d *DB) FinishRun(ctx context.Context, id int64, status string, pages, seen, newJobs, updated, closed, llmCalls, llmCached, errors int, watermark string) error {
	_, err := d.ExecContext(ctx, `
		UPDATE ingest_run SET ended_at=?, status=?, pages_fetched=?, jobs_seen=?, jobs_new=?,
		  jobs_updated=?, jobs_closed=?, llm_calls=?, llm_cached=?, errors=?, watermark=?
		WHERE id=?`,
		NowUTC(), status, pages, seen, newJobs, updated, closed, llmCalls, llmCached, errors, watermark, id)
	return err
}

// LastSuccess returns the most recent success timestamp for a run kind
// (web /metrics derive state from DB, not process memory — docs/02 §4.4).
// modernc.org/sqlite returns timestamps as strings, so we parse them here.
func (d *DB) LastSuccess(ctx context.Context, kind string) (time.Time, bool, error) {
	var s string
	err := d.QueryRowContext(ctx,
		`SELECT ended_at FROM ingest_run WHERE kind=? AND status='success' ORDER BY ended_at DESC LIMIT 1`,
		kind).Scan(&s)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false, err
	}
	return t, true, nil
}
