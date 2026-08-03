package store

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
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

// queryPlan returns the concatenated EXPLAIN QUERY PLAN detail lines.
func queryPlan(t *testing.T, db *DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan += detail + "\n"
	}
	return plan
}

// The daily pages and /metrics slice job by crawl time and closure state on
// every request; a planner regression here is a silent full scan.
func TestCrawlTimeQueriesUseIndexes(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	cases := []struct {
		name  string
		query string
		args  []any
		want  string
	}{
		{
			"daily stored-per-day",
			`SELECT date(first_seen_at,'+8 hours') d, count(*) FROM job
			 WHERE first_seen_at >= ? AND first_seen_at < ? GROUP BY d`,
			[]any{"2026-08-01T00:00:00Z", "2026-08-05T00:00:00Z"},
			"idx_job_first_seen",
		},
		{
			"daily closed-per-day",
			`SELECT date(closed_at,'+8 hours') d, count(*) FROM job
			 WHERE closed_at IS NOT NULL AND closed_at >= ? AND closed_at < ? GROUP BY d`,
			[]any{"2026-08-01T00:00:00Z", "2026-08-05T00:00:00Z"},
			"idx_job_closed",
		},
		{"metrics active count", `SELECT count(*) FROM job WHERE closed_at IS NULL`, nil, "idx_job_closed"},
		{"metrics closed count", `SELECT count(*) FROM job WHERE closed_at IS NOT NULL`, nil, "idx_job_closed"},
		{"first activity seek", `SELECT date(min(first_seen_at),'+8 hours') FROM job`, nil, "idx_job_first_seen"},
	}
	for _, c := range cases {
		plan := queryPlan(t, db, c.query, c.args...)
		if !strings.Contains(plan, c.want) {
			t.Errorf("%s does not use %s:\n%s", c.name, c.want, plan)
		}
		if strings.Contains(plan, "SCAN job\n") {
			t.Errorf("%s falls back to a full table scan:\n%s", c.name, plan)
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

// Readers must not queue behind one another: the web pod serves /daily,
// /metrics and /healthz off the same handle, and a serialised pool lets slow
// page renders starve the liveness probe.
func TestReadOnlyHandleServesConcurrentReaders(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	rw, err := Open(path, false)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := rw.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if n := rw.Stats().MaxOpenConnections; n != 1 {
		t.Errorf("writer pool = %d, want 1 (SQLite single-writer)", n)
	}
	rw.Close()

	ro, err := Open(path, true)
	if err != nil {
		t.Fatalf("Open ro: %v", err)
	}
	t.Cleanup(func() { ro.Close() })
	if n := ro.Stats().MaxOpenConnections; n < 2 {
		t.Errorf("reader pool = %d, want >1", n)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var n int
			if err := ro.QueryRowContext(ctx, `SELECT count(*) FROM job`).Scan(&n); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent read failed: %v", err)
	}
}
