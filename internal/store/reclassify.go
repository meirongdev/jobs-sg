package store

import (
	"context"
)

// Classification is the classify-layer verdict as stored on a job row. These
// are pure functions of the posting JSON (internal/classify), which is what
// makes them safe to recompute from the archive long after the fact.
type Classification struct {
	RoleFamily string
	Seniority  string
	WorkMode   string
	IsSWE      bool
}

// ClassificationUpdate is one posting's recomputed verdict.
type ClassificationUpdate struct {
	UUID string
	Classification
	// CompanyUEN/CompanyType are applied to the company row when UEN is set.
	// company_type is derived by the same classifier but lives on company, not
	// job, so recomputing one without the other would leave the two disagreeing.
	CompanyUEN  string
	CompanyType string
}

// LoadClassifications returns uuid -> currently stored classification for every
// job. The caller compares against a recomputed value so it can report and
// write only genuine changes: an UPDATE that sets a column to what it already
// held still counts as a modified row in SQLite, so RowsAffected cannot tell
// "this changed" from "this row exists".
func (d *DB) LoadClassifications(ctx context.Context) (map[string]Classification, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT uuid, COALESCE(role_family,''), COALESCE(seniority,''), COALESCE(work_mode,''), is_swe FROM job`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]Classification)
	for rows.Next() {
		var uuid string
		var c Classification
		var isSWE int
		if err := rows.Scan(&uuid, &c.RoleFamily, &c.Seniority, &c.WorkMode, &isSWE); err != nil {
			return nil, err
		}
		c.IsSWE = isSWE == 1
		out[uuid] = c
	}
	return out, rows.Err()
}

// ApplyClassifications rewrites the classify-layer columns for the given
// postings in one transaction, and returns how many job rows it touched.
//
// ☠️ It writes **only** derived classification columns. The lifecycle columns —
// closed_at, last_seen_at, first_seen_at, miss_count — are deliberately absent
// from both statements. Reprocessing the archive says nothing about whether a
// posting is still on the board: the archive is a record of what was seen, not
// of what is live. Touching last_seen_at here would make a posting look
// re-sighted and quietly reverse a close; touching closed_at would invent a
// lifecycle event out of a file read. Whoever edits this must keep that split.
//
// A uuid with no job row is skipped, not inserted: an INSERT would have to
// invent first_seen_at and last_seen_at, and a posting the pipeline never
// stored has no honest values for them. Callers report the count instead.
func (d *DB) ApplyClassifications(ctx context.Context, batch []ClassificationUpdate) (int, error) {
	var updated int
	err := d.retryBusy(ctx, func() error {
		updated = 0
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		jobStmt, err := tx.PrepareContext(ctx,
			`UPDATE job SET role_family=?, seniority=?, work_mode=?, is_swe=? WHERE uuid=?`)
		if err != nil {
			return err
		}
		defer jobStmt.Close()
		coStmt, err := tx.PrepareContext(ctx,
			`UPDATE company SET company_type=? WHERE uen=?`)
		if err != nil {
			return err
		}
		defer coStmt.Close()

		for _, u := range batch {
			res, err := jobStmt.ExecContext(ctx, u.RoleFamily, u.Seniority, u.WorkMode, boolInt(u.IsSWE), u.UUID)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			updated += int(n)
			if u.CompanyUEN != "" && u.CompanyType != "" {
				if _, err := coStmt.ExecContext(ctx, u.CompanyType, u.CompanyUEN); err != nil {
					return err
				}
			}
		}
		return tx.Commit()
	})
	return updated, err
}
