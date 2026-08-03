// Command ingest pulls MyCareersFuture jobs daily (02:15 SGT), archives all
// categories to gzip JSONL and upserts candidate jobs into SQLite. Sunday runs
// (or --reconcile) do the weekly full reconcile with closed_at lifecycle.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/meirongdev/jobs-sg/internal/ingest"
)

func main() {
	var (
		dataDir       = flag.String("data-dir", "/data", "directory holding jobs.db and raw/")
		baseURL       = flag.String("base-url", "https://api.mycareersfuture.gov.sg/v2", "MCF API base URL")
		limit         = flag.Int("limit", 100, "page size (API max 100)")
		delayMS       = flag.Int("delay-ms", 1500, "rate limit between page requests")
		maxPages      = flag.Int("max-pages", 300, "incremental circuit breaker (pages)")
		fullScanPages = flag.Int("full-scan-pages", 1000, "baseline/reconcile full-scan cap (pages)")
		reconcile     = flag.Bool("reconcile", false, "force weekly full reconcile")
	)
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	sgt, _ := time.LoadLocation("Asia/Singapore")
	isSunday := time.Now().In(sgt).Weekday() == time.Sunday
	doReconcile := *reconcile || isSunday

	ctx := context.Background()
	res, err := ingest.Run(ctx, ingest.Config{
		DataDir:          *dataDir,
		BaseURL:          *baseURL,
		Limit:            *limit,
		MaxPages:         *maxPages,
		FullScanMaxPages: *fullScanPages,
		Delay:            time.Duration(*delayMS) * time.Millisecond,
		Reconcile:        doReconcile,
	})
	if err != nil {
		slog.Error("ingest failed", "err", err)
		os.Exit(1)
	}
	if res.Status == "failed" {
		os.Exit(1)
	}
}
