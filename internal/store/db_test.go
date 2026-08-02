package store

import (
	"context"
	"path/filepath"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"), false)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrateAndIntegrity(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	var check string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&check); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if check != "ok" {
		t.Fatalf("integrity_check = %q, want ok", check)
	}
	for _, tbl := range []string{"job", "company", "job_tech", "ingest_run", "weekly_metric", "enrich_cache", "unmapped_tech", "ssoc_taxonomy", "tech_taxonomy", "job_repost", "job_source_xref"} {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", tbl).Scan(&n); err != nil {
			t.Fatalf("query %s: %v", tbl, err)
		}
		if n != 1 {
			t.Errorf("table %s missing after migrate", tbl)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate should be idempotent: %v", err)
	}
}

func TestSeedSeedsTaxonomies(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := db.Seed(ctx); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	var slug, kind string
	if err := db.QueryRowContext(ctx, "SELECT tech_slug, tech_kind FROM tech_taxonomy WHERE alias='golang'").Scan(&slug, &kind); err != nil {
		t.Fatalf("golang alias: %v", err)
	}
	if slug != "go" || kind != "language" {
		t.Errorf("golang -> (%s,%s), want (go,language)", slug, kind)
	}
	if err := db.QueryRowContext(ctx, "SELECT tech_slug, tech_kind FROM tech_taxonomy WHERE alias='k8s'").Scan(&slug, &kind); err != nil {
		t.Fatalf("k8s alias: %v", err)
	}
	if slug != "kubernetes" || kind != "tool" {
		t.Errorf("k8s -> (%s,%s), want (kubernetes,tool)", slug, kind)
	}
	var family string
	if err := db.QueryRowContext(ctx, "SELECT role_family FROM ssoc_taxonomy WHERE ssoc_code='25121'").Scan(&family); err != nil {
		t.Fatalf("ssoc 25121: %v", err)
	}
	if family != "Backend" {
		t.Errorf("ssoc 25121 -> %s, want Backend", family)
	}
	if err := db.Seed(ctx); err != nil {
		t.Fatalf("second Seed should be idempotent: %v", err)
	}
}

func TestReadOnlyOpenRejectsWrites(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path, false)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	db.Close()

	ro, err := Open(path, true)
	if err != nil {
		t.Fatalf("Open ro: %v", err)
	}
	t.Cleanup(func() { ro.Close() })
	if _, err := ro.ExecContext(ctx, "CREATE TABLE nope(id INTEGER)"); err == nil {
		t.Fatal("expected write on read-only DB to fail")
	}
}
