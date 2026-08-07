package metric

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/meirongdev/jobs-sg/internal/classify"
	"github.com/meirongdev/jobs-sg/internal/mcf"
	"github.com/meirongdev/jobs-sg/internal/store"
	"github.com/meirongdev/jobs-sg/internal/tech"
)

// fixtureNow is the clock every fixture-backed test uses: Monday of 2026-W33.
// LastCompletedWeek(fixtureNow) is W32 and the four baseline weeks W28..W31 are
// all populated, because scripts/genfixture spreads postings over W27..W32.
var fixtureNow = time.Date(2026, 8, 10, 9, 0, 0, 0, SGT)

// fixtureTemplate is the once-built reference DB every test copies. Building
// it replays 360 fixture rows through the production UpsertJob/WriteRuleTech
// path — roughly 720 self-committed, journal-synced transactions. At ~0.4s a
// build, doing that in every test made this the slowest package in the repo,
// so TestMain pays the cost once and seedFixture hands each test its own
// disposable copy.
var fixtureTemplate string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "metric-fixture-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "fixture template dir:", err)
		os.Exit(1)
	}
	fixtureTemplate = filepath.Join(dir, "jobs.db")
	if err := buildFixtureDB(fixtureTemplate); err != nil {
		os.RemoveAll(dir)
		fmt.Fprintln(os.Stderr, "build fixture template:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// buildFixtureDB loads testdata/fixture/jobs.jsonl into a fresh DB at path the
// way cmd/ingest + cmd/enrich would: classify every posting, then run the rule
// layer over its description.
func buildFixtureDB(path string) error {
	ctx := context.Background()
	db, err := store.Open(path, false)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		return err
	}
	if err := db.Seed(ctx); err != nil {
		return err
	}
	ssoc, err := db.LoadSSOCMap(ctx)
	if err != nil {
		return err
	}
	cl := classify.New(ssoc)
	taxRows, err := db.LoadTechTaxonomy(ctx)
	if err != nil {
		return err
	}
	tax := tech.LoadTaxonomy(taxRows)

	f, err := os.Open("../../testdata/fixture/jobs.jsonl")
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var j mcf.Job
		if err := json.Unmarshal(sc.Bytes(), &j); err != nil {
			return err
		}
		if _, err := db.UpsertJob(ctx, j, cl.Classify(j), "raw/fixture.jsonl.gz#0"); err != nil {
			return err
		}
		hits := tax.Extract(j.Title + " " + j.Description)
		rows := make([]store.TechRow, len(hits))
		for i, h := range hits {
			rows[i] = store.TechRow{Slug: h.Slug, Kind: h.Kind}
		}
		if err := db.WriteRuleTech(ctx, j.UUID, rows); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return closeFixturePostings(ctx, db)
}

// lifetimeBands are the listing-length buckets spec §3.5 reports, and the days
// each seeded closure lands on — one comfortably inside every band, plus the
// boundary values the bucketing has to get right.
var lifetimeBands = []int{3, 6, 7, 10, 14, 15, 22, 30, 31, 45, 60, 61, 90}

// closeFixturePostings marks part of the fixture as taken down.
//
// closed_at cannot come from the fixture file: it is not an API field. MCF
// returns live postings only, and the weekly reconcile derives closure from a
// posting's absence — so a JSONL of API responses can never carry one, and the
// job lifetime in spec §3.5 had nothing to measure. This stands in for "the
// reconcile has run and these postings went away".
//
// Deterministic on purpose, like the rest of the fixture: every third posting
// closes, cycling through lifetimeBands, so the same DB is built on every run
// and a test can assert an exact bucket count. Two thirds stay open, which is
// what makes the right-censoring in §3.5 visible — the median of closed
// postings is not the median of all of them.
func closeFixturePostings(ctx context.Context, db *store.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT uuid, posting_date FROM job ORDER BY uuid`)
	if err != nil {
		return err
	}
	type closure struct {
		uuid string
		days int
	}
	var todo []closure
	i := 0
	for rows.Next() {
		var uuid, posted string
		if err := rows.Scan(&uuid, &posted); err != nil {
			rows.Close()
			return err
		}
		if i%3 == 0 {
			todo = append(todo, closure{uuid, lifetimeBands[(i/3)%len(lifetimeBands)]})
		}
		i++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range todo {
		// SQLite date arithmetic keeps this in one statement and mirrors how the
		// lifetime query itself reads the pair back.
		if _, err := db.ExecContext(ctx, `
			UPDATE job SET closed_at = datetime(posting_date, '+' || ? || ' days')
			WHERE uuid = ?`, c.days, c.uuid); err != nil {
			return err
		}
	}
	return nil
}

// seedFixture hands the test a disposable copy of the reference DB built by
// TestMain. The per-test copy is what keeps mutation-heavy tests safe:
// TestTechCountsDedupeRuleAndLLMRows and TestEnrichedDenominatorExcludesBacklog
// both rewrite enrichment rows and must never see each other's changes.
// Copying the closed file is sound: journal_mode=DELETE leaves no sidecar
// files after a clean Close.
func seedFixture(t *testing.T) *store.DB {
	t.Helper()
	src, err := os.ReadFile(fixtureTemplate)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "jobs.db")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
