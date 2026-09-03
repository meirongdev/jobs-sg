// Command enrich runs rule-layer (always) + LLM-layer (optional) technology
// extraction. It fails open: LLM errors keep rule results and exit 0
// (docs/02 §4.2); JobsSgEnrichBacklog surfaces the backlog.
//
// Every LLM knob is an LLM_* environment variable read by llm.ConfigFromEnv, so
// swapping the model or the endpoint is a manifest edit. See docs/09 §3.2.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"sort"

	"github.com/meirongdev/jobs-sg/internal/llm"
	"github.com/meirongdev/jobs-sg/internal/store"
	"github.com/meirongdev/jobs-sg/internal/tech"
)

func main() {
	dataDir := flag.String("data-dir", "/data", "directory holding jobs.db and raw/")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	db, err := store.Open(*dataDir+"/jobs.db", false)
	if err != nil {
		slog.Error("open db", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.Migrate(context.Background()); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}
	rows, err := db.LoadTechTaxonomy(context.Background())
	if err != nil {
		slog.Error("load taxonomy", "err", err)
		os.Exit(1)
	}
	tax := tech.LoadTaxonomy(rows)

	cfg, warnings := llm.ConfigFromEnv(os.Getenv)
	for _, w := range warnings {
		// A dropped knob is otherwise invisible: enrich would run happily with a
		// setting the operator believes is in effect.
		slog.Warn("llm config", "problem", w)
	}

	var extractor llm.Extractor
	if cfg.Enabled() {
		extractor = llm.Chain{Extractors: cfg.Extractors()}
		// Log the effective configuration, not the requested one, so a swap can
		// be verified from the job's first log line.
		slog.Info("llm enabled",
			"base_url", cfg.BaseURL,
			"models", cfg.Models,
			"auth", authSummary(cfg),
			"timeout", cfg.EffectiveTimeout().String(),
			"concurrency", cfg.EffectiveConcurrency(),
			"attempts", cfg.EffectiveAttempts(),
			"thinking", cfg.ThinkingSummary(),
			"max_tokens", cfg.MaxTokens,
			"prompt", cfg.PromptSummary(),
			"prompt_version", cfg.EffectivePromptVersion(),
			"extra_body", extraBodyKeys(cfg))
	} else {
		slog.Info("LLM_BASE_URL unset -> rule-only mode")
	}

	en := &llm.Enricher{
		DB:          db,
		DataDir:     *dataDir,
		Taxonomy:    tax,
		LLM:         extractor,
		Concurrency: cfg.Concurrency,
		// Provenance recorded on job_tech rows and part of the LLM cache key —
		// must name the model that actually ran.
		Model: cfg.PrimaryModel(),
		// Part of the enrich cache key alongside Model, so it must track the
		// prompt actually in use — ConfigFromEnv derives one for a custom prompt.
		PromptVersion: cfg.EffectivePromptVersion(),
		Attempts:      cfg.EffectiveAttempts(),
	}
	res, err := en.Run(context.Background())
	if err != nil {
		slog.Error("enrich failed", "err", err)
		os.Exit(1)
	}
	// fail-open: partial is normal, exit 0
	if res.Status == store.StatusFailed {
		os.Exit(1)
	}
}

// authSummary names the auth header without logging the key.
func authSummary(cfg llm.Config) string {
	if cfg.APIKey == "" {
		return "none"
	}
	return cfg.AuthHeader
}

// extraBodyKeys lists what LLM_EXTRA_BODY contributed, so an override that is
// not taking effect is visible in the log.
func extraBodyKeys(cfg llm.Config) []string {
	keys := make([]string, 0, len(cfg.ExtraBody))
	for k := range cfg.ExtraBody {
		keys = append(keys, k)
	}
	sort.Strings(keys) // stable log line
	return keys
}
