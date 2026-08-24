package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps *sql.DB with jobs-sg conventions.
type DB struct {
	*sql.DB
}

// Open opens (creating if needed) a SQLite DB at path.
//
// DSN uses modernc.org/sqlite _pragma params to enforce the config from
// docs/03 §1: rollback journal (DELETE), busy_timeout=10000, synchronous=NORMAL.
// foreign_keys=1 so REFERENCES constraints are actually enforced.
//
// Rollback journal, not WAL: the web pod mounts /data read-only, and WAL needs
// to create/attach a -shm file in the data directory — impossible on a read-only
// filesystem (SQLITE_CANTOPEN on open). Writes are serialized by the cron
// schedule (ingest/enrich/report never overlap), so a read-only web can open the
// DELETE-journal DB directly and the read-only connection needs no journal pragma.
func Open(path string, readOnly bool) (*DB, error) {
	// modernc.org/sqlite does not accept mode=rw in the DSN; the default
	// (absent mode) creates/opens read-write. mode=ro is only valid once the
	// file already exists.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(DELETE)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)", path)
	if readOnly {
		dsn = fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)", path)
	}
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite has a single writer, so writers stay pinned to one connection and
	// serialise themselves. Readers do not: holding the web pod to one
	// connection queues /daily (8 queries) ahead of /healthz, and a handful of
	// concurrent requests can push the liveness probe past its 2s timeout into
	// a pod restart. Readers cannot block each other, and busy_timeout covers
	// the brief overlap with a cron writer.
	if readOnly {
		sqlDB.SetMaxOpenConns(4)
	} else {
		sqlDB.SetMaxOpenConns(1)
	}
	db := &DB{DB: sqlDB}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping %s: %w", path, err)
	}
	return db, nil
}

// addedColumns are columns added to tables that shipped without them. Table and
// column names here are compile-time constants, never caller input.
var addedColumns = []struct{ table, column, decl string }{
	{"ingest_run", "jobs_scanned", "INTEGER DEFAULT 0"},
	{"ingest_run", "total_reported", "INTEGER DEFAULT 0"},
	{"ingest_run", "total_min", "INTEGER DEFAULT 0"},
	{"ingest_run", "total_max", "INTEGER DEFAULT 0"},
	{"ingest_run", "close_skipped", "INTEGER DEFAULT 0"},
}

// Migrate creates the schema if it does not exist, then applies additive column
// migrations. Idempotent.
//
// Both halves are needed. `schema` is CREATE TABLE IF NOT EXISTS, which is a
// no-op against a database that already holds the table — so a column added to
// that literal reaches new deployments only, while the live database keeps the
// old shape until the first write of the new column fails at runtime. SQLite has
// no ADD COLUMN IF NOT EXISTS, so existing columns are detected with PRAGMA
// rather than by swallowing the ALTER error, which would swallow real ones too.
//
// Only the writer commands (ingest, enrich, report) call this; the web server
// opens the database read-only and never migrates, so a scrape can observe the
// pre-migration shape — HasColumn is what lets /metrics tolerate that window
// instead of failing the whole scrape.
func (d *DB) Migrate(ctx context.Context) error {
	if _, err := d.ExecContext(ctx, schema); err != nil {
		return err
	}
	for _, c := range addedColumns {
		have, err := d.HasColumn(ctx, c.table, c.column)
		if err != nil {
			return err
		}
		if have {
			continue
		}
		if _, err := d.ExecContext(ctx,
			fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, c.table, c.column, c.decl)); err != nil {
			return fmt.Errorf("add %s.%s: %w", c.table, c.column, err)
		}
	}
	return nil
}

// HasColumn reports whether a table already has a column. Read-only, so it is
// safe on the web server's read-only handle.
func (d *DB) HasColumn(ctx context.Context, table, column string) (bool, error) {
	rows, err := d.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			found = true
		}
	}
	return found, rows.Err()
}

// Seed upserts the tech_taxonomy and ssoc_taxonomy seed rows, and deletes
// tech_taxonomy aliases the seed list no longer contains. Idempotent.
//
// The delete is what makes removing an alias mean anything. Upserting alone
// leaves a retired alias live in every database that already has it — and
// since LoadTaxonomy reads the table, not this list, the alias keeps matching
// forever while the source says it is gone. That is not hypothetical: it is how
// `express` would have survived being fixed (it matched "express themselves"
// and "Recruit Express Pte Ltd" across 4% of postings).
//
// ⚠️ Consequence: techSeeds is the only way to add an alias. An alias inserted
// into tech_taxonomy by hand is deleted by the next ingest/enrich run, which is
// the intended trade — docs/03 §7 has the review loop end in an edit to
// techSeeds, so a hand-inserted row is untracked state either way.
//
// ⚠️ ssoc_taxonomy is NOT pruned — but the reason first written here ("its rows
// carry a human `note` column, so a row absent from ssocSeeds may be
// hand-curated") does not survive reading the next statement: the ssoc upsert
// below does `note=excluded.note`, so a hand-written note on any code that IS in
// ssocSeeds is overwritten on every run. Hand-curation is protected only for
// codes absent from the seed list — the arbitrary half.
//
// Honest statement of the current state: the asymmetry is untested, and the
// tech-side argument above ("a hand-inserted row is untracked state either way")
// applies here too. LoadSSOCMap reads the table, so dropping a line from
// ssocSeeds is still exactly the no-op the tech prune was written to eliminate —
// on the layer that decides is_swe/role_family for every posting, a larger blast
// radius than one tech alias. Left as-is because changing it is a behaviour
// change to classification rather than a cleanup: it needs a look at what the
// live table actually holds beyond ssocSeeds first.
func (d *DB) Seed(ctx context.Context) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Whole-table delete, then re-insert every seed: since techSeeds is the only
	// source of aliases, the post-state is identical to computing the set
	// difference and deleting just the retired rows — with one statement instead
	// of a SELECT, a map, and N+1 round-trips. Safe as a truncate because the
	// table is (alias PK, tech_slug, tech_kind) with no foreign keys pointing at
	// it (job_tech.tech_slug is a plain column, not a reference), and both
	// statements are in the same transaction, so no reader observes the gap.
	if _, err := tx.ExecContext(ctx, `DELETE FROM tech_taxonomy`); err != nil {
		return err
	}
	for _, s := range techSeeds {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tech_taxonomy(alias, tech_slug, tech_kind) VALUES(?,?,?)
			 ON CONFLICT(alias) DO UPDATE SET tech_slug=excluded.tech_slug, tech_kind=excluded.tech_kind`,
			s[0], s[1], s[2]); err != nil {
			return err
		}
	}
	for _, s := range ssocSeeds {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO ssoc_taxonomy(ssoc_code, role_family, note) VALUES(?,?,?)
			 ON CONFLICT(ssoc_code) DO UPDATE SET role_family=excluded.role_family, note=excluded.note`,
			s[0], s[1], s[2]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// NowUTC returns current time as the canonical UTC ISO8601 string used across
// the schema.
func NowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}
