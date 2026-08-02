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
// docs/03 §1: WAL, busy_timeout=10000, synchronous=NORMAL. foreign_keys=1 so
// REFERENCES constraints are actually enforced.
func Open(path string, readOnly bool) (*DB, error) {
	// modernc.org/sqlite does not accept mode=rw in the DSN; the default
	// (absent mode) creates/opens read-write. mode=ro is only valid once the
	// file already exists.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)", path)
	if readOnly {
		dsn = fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)", path)
	}
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1) // SQLite single-writer; serialise our own writes
	db := &DB{DB: sqlDB}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping %s: %w", path, err)
	}
	return db, nil
}

// Migrate creates the schema if it does not exist. Idempotent.
func (d *DB) Migrate(ctx context.Context) error {
	_, err := d.ExecContext(ctx, schema)
	return err
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
