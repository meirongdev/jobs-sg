package store

import (
	"context"
	"database/sql"
)

// JobRef is a job needing enrichment (title + archive path; description is
// read back from raw_path, docs/03 §3).
type JobRef struct {
	UUID            string
	Title           string
	DescriptionHash string
	RawPath         string
}

// RuleBacklog lists candidate jobs lacking rule-layer tech extraction.
func (d *DB) RuleBacklog(ctx context.Context) ([]JobRef, error) {
	return d.enrichBacklog(ctx, "rule")
}

// LLMBacklog lists candidate jobs lacking LLM-layer extraction.
func (d *DB) LLMBacklog(ctx context.Context) ([]JobRef, error) {
	return d.enrichBacklog(ctx, "llm")
}

// enrichBacklog lists SWE jobs the given layer has not processed yet. The
// enrich_done check is what lets "processed, zero taxonomy matches" jobs
// leave the backlog; the job_tech check keeps jobs enriched before enrich_done
// existed out of the backlog without a backfill.
func (d *DB) enrichBacklog(ctx context.Context, source string) ([]JobRef, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT j.uuid, j.title, j.description_sha256, j.raw_path FROM job j
		WHERE j.is_swe=1
		  AND NOT EXISTS (SELECT 1 FROM job_tech t WHERE t.job_uuid=j.uuid AND t.source=?)
		  AND NOT EXISTS (SELECT 1 FROM enrich_done e WHERE e.job_uuid=j.uuid AND e.source=?)
		ORDER BY j.posting_date`, source, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobRef
	for rows.Next() {
		var r JobRef
		if err := rows.Scan(&r.UUID, &r.Title, &r.DescriptionHash, &r.RawPath); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// WriteRuleTech inserts rule-layer tech rows for a job.
func (d *DB) WriteRuleTech(ctx context.Context, uuid string, techs []TechRow) error {
	return d.writeTech(ctx, uuid, techs, "rule")
}

// WriteLLMTech inserts LLM-layer tech rows and records unmapped terms.
func (d *DB) WriteLLMTech(ctx context.Context, uuid string, techs []TechRow, unmapped []string) error {
	if err := d.writeTech(ctx, uuid, techs, "llm"); err != nil {
		return err
	}
	now := NowUTC()
	for _, term := range unmapped {
		if _, err := d.ExecContext(ctx, `
			INSERT INTO unmapped_tech(raw_term, first_seen_at, seen_count, reviewed)
			VALUES(?,?,1,0)
			ON CONFLICT(raw_term) DO UPDATE SET seen_count = unmapped_tech.seen_count + 1`,
			term, now); err != nil {
			return err
		}
	}
	return nil
}

// TechRow is one job_tech row (decoupled from tech package).
type TechRow struct {
	Slug string
	Kind string
}

func (d *DB) writeTech(ctx context.Context, uuid string, techs []TechRow, source string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range techs {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO job_tech(job_uuid, tech_slug, tech_kind, source) VALUES(?,?,?,?)`,
			uuid, t.Slug, t.Kind, source); err != nil {
			return err
		}
	}
	// Mark the layer done even when techs is empty — zero taxonomy matches is
	// a valid outcome, not "retry tomorrow" (see enrich_done in schema.go).
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO enrich_done(job_uuid, source, done_at) VALUES(?,?,?)
		ON CONFLICT(job_uuid, source) DO UPDATE SET done_at=excluded.done_at`,
		uuid, source, NowUTC()); err != nil {
		return err
	}
	return tx.Commit()
}

// CacheGet returns the cached LLM result for a description hash.
func (d *DB) CacheGet(ctx context.Context, hash, model, promptVersion string) (string, bool, error) {
	var out string
	err := d.QueryRowContext(ctx,
		`SELECT result_json FROM enrich_cache WHERE description_sha256=? AND model=? AND prompt_version=?`,
		hash, model, promptVersion).Scan(&out)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return out, true, nil
}

// CachePut stores an LLM result keyed by description hash + model + prompt.
func (d *DB) CachePut(ctx context.Context, hash, model, promptVersion, resultJSON string) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO enrich_cache(description_sha256, model, prompt_version, result_json, created_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(description_sha256, model, prompt_version) DO UPDATE SET result_json=excluded.result_json, created_at=excluded.created_at`,
		hash, model, promptVersion, resultJSON, NowUTC())
	return err
}

// EnrichBacklogCount returns the count of candidate jobs the LLM layer has
// not processed yet (the JobsSgEnrichBacklog metric, docs/04 §3.1). Must
// mirror enrichBacklog's conditions, or the metric counts jobs the nightly
// run will never touch.
func (d *DB) EnrichBacklogCount(ctx context.Context) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `
		SELECT count(*) FROM job j
		WHERE j.is_swe=1
		  AND NOT EXISTS (SELECT 1 FROM job_tech t WHERE t.job_uuid=j.uuid AND t.source='llm')
		  AND NOT EXISTS (SELECT 1 FROM enrich_done e WHERE e.job_uuid=j.uuid AND e.source='llm')`).Scan(&n)
	return n, err
}

// UnmappedTechCount returns the number of unreviewed unmapped terms.
func (d *DB) UnmappedTechCount(ctx context.Context) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT count(*) FROM unmapped_tech WHERE reviewed=0`).Scan(&n)
	return n, err
}
