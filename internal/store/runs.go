package store

import (
	"context"
	"database/sql"
	"time"
)

// Run kinds (docs/03 §2 ingest_run.kind).
const (
	RunIncremental = "incremental"
	RunReconcile   = "full_reconcile"
	RunEnrich      = "enrich"
	RunReport      = "report"
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

// ScanAudit is what a paginated sweep observed about the board it walked, as
// opposed to what it stored. Scanned is every posting the sweep walked past;
// Total is the size the API advertised as of the last page; Min/Max bracket how
// far that advertised size moved during the sweep. CloseSkipped records that the
// reconcile declined to close on absence because Scanned came up short of Total.
type ScanAudit struct {
	Scanned      int
	Total        int
	TotalMin     int
	TotalMax     int
	CloseSkipped bool
}

// RecordScanAudit stores what the sweep saw, alongside the counters FinishRun
// already wrote.
//
// Kept off FinishRun deliberately: enrich and report also call that, and neither
// paginates anything, so widening its already eleven-parameter signature with
// five values only ingest can supply would make every call site carry them as
// zeros. The two writes are separate statements against the same row rather than
// one transaction because they are independent facts — losing the audit must not
// cost the run its status, which is what the alerts read.
func (d *DB) RecordScanAudit(ctx context.Context, id int64, a ScanAudit) error {
	_, err := d.ExecContext(ctx, `
		UPDATE ingest_run SET jobs_scanned=?, total_reported=?, total_min=?, total_max=?, close_skipped=?
		WHERE id=?`,
		a.Scanned, a.Total, a.TotalMin, a.TotalMax, boolInt(a.CloseSkipped), id)
	return err
}

// LastSuccess returns the most recent success timestamp for a run kind
// (web /metrics derive state from DB, not process memory — docs/02 §4.4).
// modernc.org/sqlite returns timestamps as strings, so we parse them here.
//
// The incremental kind also accepts reconcile rows whose scan was clean
// (errors=0), whatever their status. A reconcile does everything an
// incremental does — walks the board, archives what it never stored, upserts
// every candidate — so a clean sweep IS fresh data even when the close gate
// declined to act on absence and the run went partial. Without this, one
// cautious Sunday withheld both kinds' stamps at once and JobsSgIngestStale
// spent 5–8 hours claiming data was stale that had just been fully refreshed.
//
// The asymmetry is the point: full_reconcile does NOT get the same treatment.
// Its stamp means "the lifecycle was reconciled", and a close-skipped round is
// precisely a round where it was not — that withheld stamp is what lets
// JobsSgReconcileStale catch a gate stuck cautious for weeks. errors=0 (not
// status) is the discriminator because a clean-scan close-skip is the only way
// a reconcile goes partial without errors; ended_at IS NOT NULL keeps
// still-running rows out.
func (d *DB) LastSuccess(ctx context.Context, kind string) (time.Time, bool, error) {
	query := `SELECT ended_at FROM ingest_run WHERE kind=? AND status='success' ORDER BY ended_at DESC LIMIT 1`
	args := []any{kind}
	if kind == RunIncremental {
		query = `SELECT ended_at FROM ingest_run
			WHERE (kind=? AND status='success')
			   OR (kind=? AND errors=0 AND ended_at IS NOT NULL)
			ORDER BY ended_at DESC LIMIT 1`
		args = []any{RunIncremental, RunReconcile}
	}
	var s string
	err := d.QueryRowContext(ctx, query, args...).Scan(&s)
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
