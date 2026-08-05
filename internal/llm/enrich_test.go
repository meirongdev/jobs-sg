package llm

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/meirongdev/jobs-sg/internal/classify"
	"github.com/meirongdev/jobs-sg/internal/mcf"
	"github.com/meirongdev/jobs-sg/internal/store"
	"github.com/meirongdev/jobs-sg/internal/tech"
)

type stubExtractor struct {
	res Result
	err error
}

func (s stubExtractor) Extract(ctx context.Context, title, desc string) (Result, error) {
	if s.err != nil {
		return Result{}, s.err
	}
	return s.res, nil
}

func setupEnrich(t *testing.T) (*store.DB, *tech.Taxonomy, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "jobs.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := db.LoadTechTaxonomy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return db, tech.LoadTaxonomy(rows), dir
}

func seedCandidate(t *testing.T, db *store.DB, dir, uuid, title, desc string) {
	t.Helper()
	ctx := context.Background()
	aw, err := mcf.NewArchiveWriter(filepath.Join(dir, "raw"), time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	job := mcf.Job{
		UUID: uuid, Title: title, Description: "<p>" + desc + "</p>",
		Metadata: mcf.Metadata{JobPostID: "MCF-" + uuid, NewPostingDate: "2026-08-01T00:00:00Z"},
		SSOCCode: "25121", Categories: []mcf.Category{{Category: "Information Technology"}},
	}
	loc, err := aw.Write(job)
	if err != nil {
		t.Fatal(err)
	}
	aw.Close()
	cl := classify.New(map[string]string{"25121": "Backend"})
	if _, err := db.UpsertJob(ctx, job, cl.Classify(job), loc); err != nil {
		t.Fatal(err)
	}
}

func TestRuleLayerAlwaysRuns(t *testing.T) {
	ctx := context.Background()
	db, tax, dir := setupEnrich(t)
	seedCandidate(t, db, dir, "u1", "Backend Engineer", "We use golang, k8s and gcp.")
	en := &Enricher{DB: db, DataDir: dir, Taxonomy: tax, LLM: nil} // rule-only
	res, err := en.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != store.StatusSuccess {
		t.Errorf("status = %s, want success (rule-only)", res.Status)
	}
	if res.RuleJobs != 1 {
		t.Errorf("rule_jobs = %d, want 1", res.RuleJobs)
	}
	var slug, source string
	if err := db.QueryRowContext(ctx, "SELECT tech_slug, source FROM job_tech WHERE job_uuid='u1' AND tech_slug='go'").Scan(&slug, &source); err != nil {
		t.Fatalf("rule go tech missing: %v", err)
	}
	if source != "rule" {
		t.Errorf("source = %s, want rule", source)
	}
}

func TestLLMCacheAndUnmapped(t *testing.T) {
	ctx := context.Background()
	db, tax, dir := setupEnrich(t)
	// two jobs with identical description -> one LLM call, one cache hit
	seedCandidate(t, db, dir, "u1", "Python Engineer", "Build with Django and PyTorch.")
	seedCandidate(t, db, dir, "u2", "Python Engineer", "Build with Django and PyTorch.")
	stub := stubExtractor{res: Result{
		Languages:  []string{"Python"},
		Frameworks: []string{"Django"},
		AI:         []string{"PyTorch"},
		Tools:      []string{"Frobnicate"}, // unmapped
	}}
	en := &Enricher{DB: db, DataDir: dir, Taxonomy: tax, LLM: stub, Model: "m", PromptVersion: "v1", Concurrency: 2}
	res, err := en.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != store.StatusSuccess {
		t.Errorf("status = %s, want success", res.Status)
	}
	// two identical descriptions processed concurrently: each is either called
	// or cache-hit, never lost (calls + cached == 2).
	if res.LLMCalls+res.LLMCached != 2 {
		t.Errorf("calls+cached = %d, want 2", res.LLMCalls+res.LLMCached)
	}
	// llm tech written + normalized
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM job_tech WHERE source='llm' AND tech_slug='python'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("llm python rows = %d, want 2", n)
	}
	// unmapped recorded (once per job)
	var unmapped int
	if err := db.QueryRowContext(ctx, "SELECT seen_count FROM unmapped_tech WHERE raw_term='Frobnicate'").Scan(&unmapped); err != nil {
		t.Fatal(err)
	}
	if unmapped != 2 {
		t.Errorf("Frobnicate seen_count = %d, want 2", unmapped)
	}
	// new identical job (u3) must be served from cache: calls=0, cached=1
	seedCandidate(t, db, dir, "u3", "Python Engineer", "Build with Django and PyTorch.")
	res2, err := en.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res2.LLMCalls != 0 || res2.LLMCached != 1 {
		t.Errorf("re-run calls=%d cached=%d, want 0/1", res2.LLMCalls, res2.LLMCached)
	}
}

// TestZeroMatchJobLeavesBacklog reproduces a production bug: jobs whose
// extraction mapped to zero taxonomy terms wrote no job_tech rows, so they
// never left either backlog — ~1.4k jobs were re-fetched from enrich_cache
// every night (llm_cached ≈ backlog on every run) and the backlog metric
// never drained. Zero matches must count as processed.
func TestZeroMatchJobLeavesBacklog(t *testing.T) {
	ctx := context.Background()
	db, tax, dir := setupEnrich(t)
	seedCandidate(t, db, dir, "u1", "Engineering Generalist", "You will wear many hats.")
	stub := stubExtractor{res: Result{Tools: []string{"hats"}}} // unmapped only
	en := &Enricher{DB: db, DataDir: dir, Taxonomy: tax, LLM: stub, Model: "m", PromptVersion: "v1", Concurrency: 1}

	res, err := en.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != store.StatusSuccess || res.LLMCalls != 1 {
		t.Errorf("status=%s calls=%d, want success/1", res.Status, res.LLMCalls)
	}
	var techRows int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM job_tech WHERE job_uuid='u1'").Scan(&techRows); err != nil {
		t.Fatal(err)
	}
	if techRows != 0 {
		t.Fatalf("job_tech rows = %d, want 0 (nothing mappable)", techRows)
	}
	if n, err := db.EnrichBacklogCount(ctx); err != nil || n != 0 {
		t.Errorf("backlog after run = %d (err %v), want 0 — zero matches is processed, not pending", n, err)
	}

	// re-run must be a no-op: no rule re-scan, no LLM call, no cache re-fetch
	res2, err := en.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res2.RuleJobs != 0 || res2.LLMCalls != 0 || res2.LLMCached != 0 {
		t.Errorf("re-run rule=%d calls=%d cached=%d, want 0/0/0", res2.RuleJobs, res2.LLMCalls, res2.LLMCached)
	}
}

func TestFailOpenPreservesRuleResults(t *testing.T) {
	ctx := context.Background()
	db, tax, dir := setupEnrich(t)
	seedCandidate(t, db, dir, "u1", "Backend Engineer", "We use golang.")
	stub := stubExtractor{err: errors.New("bifrost unreachable")}
	en := &Enricher{DB: db, DataDir: dir, Taxonomy: tax, LLM: stub, Model: "m", PromptVersion: "v1"}
	res, err := en.Run(ctx)
	if err != nil {
		t.Fatalf("Run should fail-open (nil error), got %v", err)
	}
	if res.Status != store.StatusPartial {
		t.Errorf("status = %s, want partial", res.Status)
	}
	if res.Errors == 0 {
		t.Error("errors should be > 0")
	}
	// rule layer results preserved despite LLM failure
	var slug string
	if err := db.QueryRowContext(ctx, "SELECT tech_slug FROM job_tech WHERE job_uuid='u1' AND source='rule'").Scan(&slug); err != nil {
		t.Fatalf("rule tech missing after fail-open: %v", err)
	}
}

func TestChainDegradesToNext(t *testing.T) {
	ctx := context.Background()
	failing := stubExtractor{err: errors.New("custom_dgx down")}
	ok := stubExtractor{res: Result{Languages: []string{"Go"}}}
	chain := Chain{Extractors: []Extractor{failing, ok}}
	res, err := chain.Extract(ctx, "t", "d")
	if err != nil {
		t.Fatalf("Chain.Extract: %v", err)
	}
	if len(res.Languages) != 1 || res.Languages[0] != "Go" {
		t.Errorf("chain result = %+v", res)
	}
}
