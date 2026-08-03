// Command enrich runs rule-layer (always) + LLM-layer (optional) technology
// extraction. It fails open: LLM errors keep rule results and exit 0
// (docs/02 §4.2); JobsSgEnrichBacklog surfaces the backlog.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

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

	var extractor llm.Extractor
	if baseURL := os.Getenv("LLM_BASE_URL"); baseURL != "" {
		vk := os.Getenv("BIFROST_VK")
		modelChain := []string{"custom_dgx/deepseek-v4-flash", "custom_m2"}
		var chain []llm.Extractor
		for _, m := range modelChain {
			chain = append(chain, &llm.OpenAIExtractor{BaseURL: baseURL, VirtualKey: vk, Model: m})
		}
		extractor = llm.Chain{Extractors: chain}
		slog.Info("llm enabled", "models", modelChain)
	} else {
		slog.Info("LLM_BASE_URL unset -> rule-only mode")
	}

	en := &llm.Enricher{
		DB:            db,
		DataDir:       *dataDir,
		Taxonomy:      tax,
		LLM:           extractor,
		Model:         "custom_dgx/deepseek-v4-flash",
		PromptVersion: "v1",
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
