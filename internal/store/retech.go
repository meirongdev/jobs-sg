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
// enrich_done is deliberately left alone. The marker means "this layer has
// processed this posting", which stays true across a replay — refreshing
// done_at would rewrite ~7k rows to say the nightly run did something it
// didn't, and clearing it would push every posting back into the backlog for
// enrich to redo one at a time.
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

		for _, u := range batch {
			if _, err := del.ExecContext(ctx, u.UUID); err != nil {
				return err
			}
			for _, t := range u.Techs {
				if _, err := ins.ExecContext(ctx, u.UUID, t.Slug, t.Kind); err != nil {
					return err
				}
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
