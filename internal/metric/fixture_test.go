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
	return sc.Err()
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
