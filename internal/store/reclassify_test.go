package store

import (
	"context"
	"testing"

	"github.com/meirongdev/jobs-sg/internal/classify"
	"github.com/meirongdev/jobs-sg/internal/mcf"
)

// lifecycle is the state ApplyClassifications must never touch.
type lifecycle struct {
	firstSeen string
	lastSeen  string
	closedAt  *string
	missCount int
}

func readLifecycle(t *testing.T, db *DB, uuid string) lifecycle {
	t.Helper()
	var lc lifecycle
	err := db.QueryRowContext(context.Background(),
		`SELECT first_seen_at, last_seen_at, closed_at, miss_count FROM job WHERE uuid=?`, uuid).
		Scan(&lc.firstSeen, &lc.lastSeen, &lc.closedAt, &lc.missCount)
	if err != nil {
		t.Fatalf("read lifecycle: %v", err)
	}
	return lc
}

func seedJob(t *testing.T, db *DB, uuid string) mcf.Job {
	t.Helper()
	j := mcf.Job{
		UUID: uuid, Title: "Backend Engineer", Description: "<p>Go</p>",
		Metadata:      mcf.Metadata{JobPostID: "MCF-" + uuid, NewPostingDate: "2026-08-01", ExpiryDate: "2026-09-01"},
		SSOCCode:      "25121",
		Categories:    []mcf.Category{{Category: "Information Technology"}},
		PostedCompany: &mcf.PostedCompany{UEN: "UEN-" + uuid, Name: "ACME", SSICCode: "62011", EmployeeCount: intPtr(50)},
	}
	cl := classify.New(map[string]string{"25121": "Backend"})
	if _, err := db.UpsertJob(context.Background(), j, cl.Classify(j), "raw/2026-08-03/000.jsonl.gz#0"); err != nil {
		t.Fatalf("seed UpsertJob: %v", err)
	}
	return j
}

// TestApplyClassificationsLeavesLifecycleUntouched is the safety property the
// whole tool rests on. Reprocessing the archive says nothing about whether a
// posting is still on the board: if a rewrite ever bumped last_seen_at or
// cleared closed_at, replaying the archive would quietly resurrect every closed
// posting and destroy the lifecycle history it was supposed to leave alone.
func TestApplyClassificationsLeavesLifecycleUntouched(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	seedJob(t, db, "u1")

	// Put the row into the state a reprocess could plausibly corrupt: closed,
	// with a miss streak behind it.
	if _, err := db.ExecContext(ctx,
		`UPDATE job SET closed_at='2026-08-06T00:00:00Z', miss_count=2, last_seen_at='2026-08-05T00:00:00Z' WHERE uuid='u1'`); err != nil {
		t.Fatal(err)
	}
	before := readLifecycle(t, db, "u1")

	n, err := db.ApplyClassifications(ctx, []ClassificationUpdate{{
		UUID:           "u1",
		Classification: Classification{RoleFamily: "SRE", Seniority: "Staff+", WorkMode: "Remote", IsSWE: true},
		CompanyUEN:     "UEN-u1",
		CompanyType:    "MNC",
	}})
	if err != nil {
		t.Fatalf("ApplyClassifications: %v", err)
	}
	if n != 1 {
		t.Errorf("updated = %d, want 1", n)
	}

	after := readLifecycle(t, db, "u1")
	if after.firstSeen != before.firstSeen || after.lastSeen != before.lastSeen || after.missCount != before.missCount {
		t.Errorf("lifecycle moved: before %+v after %+v", before, after)
	}
	if after.closedAt == nil || *after.closedAt != *before.closedAt {
		t.Errorf("closed_at changed: before %v after %v", before.closedAt, after.closedAt)
	}

	got, err := db.LoadClassifications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := Classification{RoleFamily: "SRE", Seniority: "Staff+", WorkMode: "Remote", IsSWE: true}
	if got["u1"] != want {
		t.Errorf("classification = %+v, want %+v", got["u1"], want)
	}

	var ctype string
	if err := db.QueryRowContext(ctx, `SELECT company_type FROM company WHERE uen='UEN-u1'`).Scan(&ctype); err != nil {
		t.Fatal(err)
	}
	if ctype != "MNC" {
		t.Errorf("company_type = %q, want MNC — it is derived by the same classifier and must move with the rest", ctype)
	}
}

// TestApplyClassificationsSkipsUnknownUUIDs: a posting in the archive that the
// pipeline never stored has no honest first_seen_at/last_seen_at, so it is
// counted by the caller, never invented here.
func TestApplyClassificationsSkipsUnknownUUIDs(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	seedJob(t, db, "u1")

	n, err := db.ApplyClassifications(ctx, []ClassificationUpdate{
		{UUID: "u1", Classification: Classification{RoleFamily: "Backend", Seniority: "Mid", WorkMode: "Unknown"}},
		{UUID: "ghost", Classification: Classification{RoleFamily: "Backend", Seniority: "Mid", WorkMode: "Unknown"}},
	})
	if err != nil {
		t.Fatalf("ApplyClassifications: %v", err)
	}
	if n != 1 {
		t.Errorf("updated = %d, want 1 — the unknown uuid must not be inserted", n)
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM job`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("job rows = %d, want 1", rows)
	}
}

func TestLoadClassificationsReflectsWhatIngestStored(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	j := seedJob(t, db, "u1")

	got, err := db.LoadClassifications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cl := classify.New(map[string]string{"25121": "Backend"})
	res := cl.Classify(j)
	want := Classification{
		RoleFamily: res.RoleFamily, Seniority: res.Seniority,
		WorkMode: res.WorkMode, IsSWE: res.IsSWE,
	}
	if got["u1"] != want {
		t.Errorf("loaded %+v, want %+v — the dry run compares against this, so a mismatch would misreport every row", got["u1"], want)
	}
}
