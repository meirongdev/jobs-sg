package metric

import (
	"bufio"
	"context"
	"encoding/json"
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

// seedFixture loads testdata/fixture/jobs.jsonl into a temp DB the way
// cmd/ingest + cmd/enrich would: classify every posting, then run the rule
// layer over its description.
func seedFixture(t *testing.T) *store.DB {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "jobs.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	ssoc, err := db.LoadSSOCMap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cl := classify.New(ssoc)
	taxRows, err := db.LoadTechTaxonomy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tax := tech.LoadTaxonomy(taxRows)

	f, err := os.Open("../../testdata/fixture/jobs.jsonl")
	if err != nil {
		t.Fatal(err)
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
			t.Fatal(err)
		}
		if _, err := db.UpsertJob(ctx, j, cl.Classify(j), "raw/fixture.jsonl.gz#0"); err != nil {
			t.Fatal(err)
		}
		hits := tax.Extract(j.Title + " " + j.Description)
		rows := make([]store.TechRow, len(hits))
		for i, h := range hits {
			rows[i] = store.TechRow{Slug: h.Slug, Kind: h.Kind}
		}
		if err := db.WriteRuleTech(ctx, j.UUID, rows); err != nil {
			t.Fatal(err)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return db
}
