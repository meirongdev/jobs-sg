package store

import (
	"context"
	"sort"
)

// RuleTechUpdate is one posting's recomputed rule-layer tech set.
type RuleTechUpdate struct {
	UUID  string
	Techs []TechRow
}

// LoadRuleTech returns uuid -> sorted rule-layer slugs currently stored, for
// every job that has any. The caller diffs against a recomputed set so it can
// report and write only genuine changes.
//
// Only source='rule' is loaded. The LLM layer's rows share the table but are
// not reproducible from the alias table — re-deriving them would need the model
// — so they are neither read nor written here.
func (d *DB) LoadRuleTech(ctx context.Context) (map[string][]string, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT job_uuid, tech_slug FROM job_tech WHERE source='rule' ORDER BY job_uuid, tech_slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]string)
	for rows.Next() {
		var uuid, slug string
		if err := rows.Scan(&uuid, &slug); err != nil {
			return nil, err
		}
		out[uuid] = append(out[uuid], slug)
	}
	return out, rows.Err()
}

// ApplyRuleTech replaces the rule-layer rows for the given postings in one
// transaction, and returns how many postings it rewrote.
//
// Delete-then-insert, not INSERT OR IGNORE like the nightly writeTech: the
// point of a replay is to remove rows a fixed alias no longer produces, and an
// upsert can only ever add. Scoped to source='rule' in both statements, so a
// posting's LLM rows survive untouched.
//
// enrich_done is filled in but never refreshed, and the difference matters.
//
// Refreshing an existing marker's done_at would rewrite thousands of rows to
// claim the nightly run just did work it didn't, so the insert is OR IGNORE.
// But a *missing* marker has to be created: 4,140 postings were enriched before
// enrich_done existed, and for those the job_tech rows are the only evidence the
// layer ever ran (see enrich_done in schema.go — "the job_tech check keeps jobs
// enriched before enrich_done existed out of the backlog"). Empty one of those
// postings and it loses its only proof, landing back in enrichBacklog: measured
// 173 postings on the 2026-08-24 replay. Writing the marker is also the honest
// record — the rule layer *has* processed it, just now, and "processed, zero
// matches" is a state only the marker can express.
//
// A uuid with no job row cannot occur here (callers diff against stored rows),
// but the delete is harmless if it does: it removes nothing.
func (d *DB) ApplyRuleTech(ctx context.Context, batch []RuleTechUpdate) (int, error) {
	var written int
	err := d.retryBusy(ctx, func() error {
		written = 0
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		del, err := tx.PrepareContext(ctx,
			`DELETE FROM job_tech WHERE job_uuid=? AND source='rule'`)
		if err != nil {
			return err
		}
		defer del.Close()
		ins, err := tx.PrepareContext(ctx,
			`INSERT OR IGNORE INTO job_tech(job_uuid, tech_slug, tech_kind, source) VALUES(?,?,?,'rule')`)
		if err != nil {
			return err
		}
		defer ins.Close()
		// OR IGNORE, not upsert: create a missing marker, never restamp one that
		// is already there.
		done, err := tx.PrepareContext(ctx,
			`INSERT OR IGNORE INTO enrich_done(job_uuid, source, done_at) VALUES(?,'rule',?)`)
		if err != nil {
			return err
		}
		defer done.Close()
		now := NowUTC()

		for _, u := range batch {
			if _, err := del.ExecContext(ctx, u.UUID); err != nil {
				return err
			}
			for _, t := range u.Techs {
				if _, err := ins.ExecContext(ctx, u.UUID, t.Slug, t.Kind); err != nil {
					return err
				}
			}
			if _, err := done.ExecContext(ctx, u.UUID, now); err != nil {
				return err
			}
			written++
		}
		return tx.Commit()
	})
	return written, err
}

// SlugsOf returns the sorted slugs of a tech set, for comparing a recomputed
// set against LoadRuleTech's stored one.
func SlugsOf(techs []TechRow) []string {
	out := make([]string, 0, len(techs))
	for _, t := range techs {
		out = append(out, t.Slug)
	}
	sort.Strings(out)
	return out
}
