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

// Seed upserts the tech_taxonomy and ssoc_taxonomy seed rows. Idempotent.
func (d *DB) Seed(ctx context.Context) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
