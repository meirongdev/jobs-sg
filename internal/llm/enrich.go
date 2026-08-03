package llm

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"

	"github.com/meirongdev/jobs-sg/internal/mcf"
	"github.com/meirongdev/jobs-sg/internal/store"
	"github.com/meirongdev/jobs-sg/internal/tech"
)

// Enricher orchestrates rule-first then LLM tech-stack enrichment.
type Enricher struct {
	DB            *store.DB
	DataDir       string
	Taxonomy      *tech.Taxonomy
	LLM           Extractor // nil => rule-only mode
	Model         string
	PromptVersion string
	Concurrency   int
}

// EnrichResult summarises an enrich run for ingest_run.
type EnrichResult struct {
	Status    string
	RuleJobs  int
	LLMCalls  int
	LLMCached int
	Errors    int
}

// Run executes the enrich pass (docs/02 §4.2): rule layer always runs first;
// the LLM layer supplements, is cached, and fails open.
func (e *Enricher) Run(ctx context.Context) (EnrichResult, error) {
	if e.Concurrency <= 0 {
		e.Concurrency = 3
	}
	if e.Model == "" {
		e.Model = "custom_dgx/deepseek-v4-flash"
	}
	if e.PromptVersion == "" {
		e.PromptVersion = "v1"
	}
	res := EnrichResult{}
	runID, err := e.DB.StartRun(ctx, store.RunEnrich)
	if err != nil {
		return res, err
	}

	ruleBacklog, err := e.DB.RuleBacklog(ctx)
	if err != nil {
		return res, err
	}
	// Fetched before the rule layer runs so both layers' descriptions come from a
	// single archive pass. Safe: the two backlogs filter on different job_tech
	// sources, so writing rule rows does not change the LLM backlog.
	var llmBacklog []store.JobRef
	if e.LLM != nil {
		llmBacklog, err = e.DB.LLMBacklog(ctx)
		if err != nil {
			return res, err
		}
	}

	// One archive pass for the whole run. Reading per job was O(archive) each
	// time — see mcf.ReadArchiveDescriptions for the measured cost.
	descs := e.readDescriptions(ruleBacklog, llmBacklog)

	// 1) rule layer — always
	for _, ref := range ruleBacklog {
		desc, ok := descs[ref.RawPath]
		if !ok {
			res.Errors++
			slog.Warn("enrich: description unavailable", "uuid", ref.UUID, "raw_path", ref.RawPath)
			continue
		}
		techs := e.Taxonomy.Extract(ref.Title + " " + desc)
		rows := make([]store.TechRow, 0, len(techs))
		for _, t := range techs {
			rows = append(rows, store.TechRow{Slug: t.Slug, Kind: t.Kind})
		}
		if err := e.DB.WriteRuleTech(ctx, ref.UUID, rows); err != nil {
			res.Errors++
			slog.Warn("enrich: write rule tech failed", "uuid", ref.UUID, "err", err)
			continue
		}
		res.RuleJobs++
	}

	// 2) LLM layer — optional, cached, fail-open, bounded concurrency
	if e.LLM != nil {
		sem := make(chan struct{}, e.Concurrency)
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, ref := range llmBacklog {
			desc, ok := descs[ref.RawPath]
			if !ok {
				// Previously this failed silently inside enrichOne, which made a
				// wholly unreadable archive look like a hang: full CPU, no logs,
				// no progress.
				res.Errors++
				slog.Warn("enrich: description unavailable, skipping llm", "uuid", ref.UUID, "raw_path", ref.RawPath)
				continue
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return res, ctx.Err()
			}
			wg.Add(1)
			go func(ref store.JobRef, desc string) {
				defer wg.Done()
				defer func() { <-sem }()
				calls, cached, errs := e.enrichOne(ctx, ref, desc)
				mu.Lock()
				res.LLMCalls += calls
				res.LLMCached += cached
				res.Errors += errs
				mu.Unlock()
			}(ref, desc)
		}
		wg.Wait()
	}

	res.Status = store.StatusSuccess
	if res.Errors > 0 {
		res.Status = store.StatusPartial // fail-open: exit 0, flagged partial
	}
	if err := e.DB.FinishRun(ctx, runID, res.Status, 0, 0, 0, 0, 0, res.LLMCalls, res.LLMCached, res.Errors, ""); err != nil {
		return res, err
	}
	slog.Info("enrich run finished", "status", res.Status, "rule_jobs", res.RuleJobs,
		"llm_calls", res.LLMCalls, "llm_cached", res.LLMCached, "errors", res.Errors)
	return res, nil
}

// readDescriptions reads every description the run needs in one archive pass and
// returns them HTML-stripped, keyed by raw_path. Deduplicates across backlogs so
// a job in both layers is read once.
func (e *Enricher) readDescriptions(backlogs ...[]store.JobRef) map[string]string {
	seen := make(map[string]struct{})
	paths := make([]string, 0, len(backlogs))
	for _, b := range backlogs {
		for _, ref := range b {
			if _, dup := seen[ref.RawPath]; dup {
				continue
			}
			seen[ref.RawPath] = struct{}{}
			paths = append(paths, ref.RawPath)
		}
	}
	raw, err := mcf.ReadArchiveDescriptions(e.DataDir, paths)
	if err != nil {
		// Fail-open: whatever was read still gets enriched; per-job gaps are
		// reported by the callers that find a raw_path missing.
		slog.Warn("enrich: archive read incomplete", "read", len(raw), "wanted", len(paths), "err", err)
	}
	out := make(map[string]string, len(raw))
	for p, d := range raw {
		out[p] = tech.StripHTML(d)
	}
	return out
}

func (e *Enricher) enrichOne(ctx context.Context, ref store.JobRef, desc string) (calls, cached, errors int) {
	var err error
	// cache lookup
	if cachedJSON, ok, err := e.DB.CacheGet(ctx, ref.DescriptionHash, e.Model, e.PromptVersion); err != nil {
		return 0, 0, 1
	} else if ok {
		var r Result
		if err := json.Unmarshal([]byte(cachedJSON), &r); err != nil {
			return 0, 0, 1
		}
		e.writeResult(ctx, ref.UUID, r)
		return 0, 1, 0
	}

	var r Result
	// retry once (design: 失败重试 1 次)
	for attempt := 0; attempt < 2; attempt++ {
		r, err = e.LLM.Extract(ctx, ref.Title, desc)
		if err == nil {
			break
		}
	}
	if err != nil {
		slog.Warn("enrich: llm failed (fail-open)", "uuid", ref.UUID, "err", err)
		return 1, 0, 1
	}
	raw, _ := json.Marshal(r)
	if err := e.DB.CachePut(ctx, ref.DescriptionHash, e.Model, e.PromptVersion, string(raw)); err != nil {
		return 1, 0, 1
	}
	if err := e.writeResult(ctx, ref.UUID, r); err != nil {
		return 1, 0, 1
	}
	return 1, 0, 0
}

// writeResult normalizes LLM output into job_tech + unmapped_tech.
func (e *Enricher) writeResult(ctx context.Context, uuid string, r Result) error {
	var rows []store.TechRow
	var unmapped []string
	for _, term := range r.All() {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		if slug, kind, ok := e.Taxonomy.NormalizeTerm(term); ok {
			rows = append(rows, store.TechRow{Slug: slug, Kind: kind})
		} else {
			unmapped = append(unmapped, term)
		}
	}
	if err := e.DB.WriteLLMTech(ctx, uuid, rows, unmapped); err != nil {
		slog.Warn("enrich: write llm tech failed", "uuid", uuid, "err", err)
		return err
	}
	return nil
}

// (the per-job readDescription helper was removed in favour of readDescriptions:
// one archive pass per run instead of one per job)
