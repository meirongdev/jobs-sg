package store

import (
	"context"
	"slices"
	"testing"
)

// seedTechRows puts a job with both layers' rows in place, the shape a replay
// has to survive: rule rows to be replaced, LLM rows to be left alone.
func seedTechRows(t *testing.T, db *DB, ctx context.Context) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO job(uuid, job_post_id, title, description_sha256, posting_date,
		                first_seen_at, last_seen_at, raw_path)
		VALUES('u1','MCF-1','Backend Engineer','h1','2026-08-01','t','t','raw/x#0')`); err != nil {
		t.Fatalf("insert job: %v", err)
	}
	for _, r := range [][3]string{
		{"expressjs", "framework", "rule"}, // the false positive a replay removes
		{"go", "language", "rule"},
		{"python", "language", "llm"}, // must survive
		{"go", "language", "llm"},     // same slug, other layer: must survive
	} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO job_tech(job_uuid, tech_slug, tech_kind, source) VALUES('u1',?,?,?)`,
			r[0], r[1], r[2]); err != nil {
			t.Fatalf("insert job_tech: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO enrich_done(job_uuid, source, done_at) VALUES('u1','rule','then')`); err != nil {
		t.Fatalf("insert enrich_done: %v", err)
	}
}

func techRows(t *testing.T, db *DB, ctx context.Context, source string) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx,
		`SELECT tech_slug FROM job_tech WHERE job_uuid='u1' AND source=? ORDER BY tech_slug`, source)
	if err != nil {
		t.Fatalf("query %s rows: %v", source, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, s)
	}
	return out
}

func TestApplyRuleTechReplacesOnlyRuleRows(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	seedTechRows(t, db, ctx)

	n, err := db.ApplyRuleTech(ctx, []RuleTechUpdate{{
		UUID:  "u1",
		Techs: []TechRow{{Slug: "go", Kind: "language"}, {Slug: "kubernetes", Kind: "tool"}},
	}})
	if err != nil {
		t.Fatalf("ApplyRuleTech: %v", err)
	}
	if n != 1 {
		t.Errorf("wrote %d postings, want 1", n)
	}
	if got, want := techRows(t, db, ctx, "rule"), []string{"go", "kubernetes"}; !slices.Equal(got, want) {
		t.Errorf("rule rows = %v, want %v (expressjs should be gone, kubernetes added)", got, want)
	}
	if got, want := techRows(t, db, ctx, "llm"), []string{"go", "python"}; !slices.Equal(got, want) {
		t.Errorf("llm rows = %v, want %v — the replay must not touch the LLM layer", got, want)
	}
	// enrich_done stays as it was: the layer has still processed this posting,
	// and clearing it would push it back into the nightly backlog.
	var doneAt string
	if err := db.QueryRowContext(ctx,
		`SELECT done_at FROM enrich_done WHERE job_uuid='u1' AND source='rule'`).Scan(&doneAt); err != nil {
		t.Fatalf("enrich_done lookup: %v", err)
	}
	if doneAt != "then" {
		t.Errorf("enrich_done.done_at = %q, want it untouched (%q)", doneAt, "then")
	}
}

func TestApplyRuleTechEmptySetClearsRows(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	seedTechRows(t, db, ctx)

	// A posting whose only matches were false positives ends with no rule rows
	// at all — the case an upsert-based writer can never reach.
	if _, err := db.ApplyRuleTech(ctx, []RuleTechUpdate{{UUID: "u1"}}); err != nil {
		t.Fatalf("ApplyRuleTech: %v", err)
	}
	if got := techRows(t, db, ctx, "rule"); len(got) != 0 {
		t.Errorf("rule rows = %v, want none", got)
	}
	if got, want := techRows(t, db, ctx, "llm"), []string{"go", "python"}; !slices.Equal(got, want) {
		t.Errorf("llm rows = %v, want %v", got, want)
	}
}

func TestLoadRuleTechIgnoresLLMLayer(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	seedTechRows(t, db, ctx)

	got, err := db.LoadRuleTech(ctx)
	if err != nil {
		t.Fatalf("LoadRuleTech: %v", err)
	}
	if want := []string{"expressjs", "go"}; !slices.Equal(got["u1"], want) {
		t.Errorf("LoadRuleTech()[u1] = %v, want %v", got["u1"], want)
	}
}

func TestSlugsOfSorts(t *testing.T) {
	got := SlugsOf([]TechRow{{Slug: "python"}, {Slug: "go"}, {Slug: "aws"}})
	if want := []string{"aws", "go", "python"}; !slices.Equal(got, want) {
		t.Errorf("SlugsOf = %v, want %v", got, want)
	}
}
