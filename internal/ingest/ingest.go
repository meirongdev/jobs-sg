// Package ingest implements the daily incremental pull + weekly full
// reconcile (docs/02 §4.1). Archive-before-parse: every fetched job is
// archived (all categories) before any filtering.
package ingest

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/meirongdev/jobs-sg/internal/classify"
	"github.com/meirongdev/jobs-sg/internal/mcf"
	"github.com/meirongdev/jobs-sg/internal/store"
)

// Config drives one ingest run.
type Config struct {
	DataDir          string
	BaseURL          string
	UA               string
	Limit            int
	MaxPages         int // incremental circuit breaker (~3 workdays, docs/02 §4.1)
	FullScanMaxPages int // baseline/reconcile full scans (~867 pages)
	Delay            time.Duration
	BackoffWindow    time.Duration // incremental stop window (default 2d)
	Reconcile        bool          // force full reconcile
	Transport        http.RoundTripper
	Now              func() time.Time // injectable clock for tests
}

// Result summarises a run for ingest_run and callers.
type Result struct {
	Kind      string
	Status    string
	Pages     int
	Seen      int
	New       int
	Updated   int
	Closed    int
	Errors    int
	Watermark string
}

// Run executes one ingest pass and returns its result.
func Run(ctx context.Context, cfg Config) (Result, error) {
	res := Result{}
	if cfg.DataDir == "" {
		cfg.DataDir = "data"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.mycareersfuture.gov.sg/v2"
	}
	if cfg.UA == "" {
		cfg.UA = mcf.DefaultUserAgent
	}
	if cfg.Limit == 0 {
		cfg.Limit = 100
	}
	if cfg.MaxPages == 0 {
		cfg.MaxPages = 300
	}
	if cfg.FullScanMaxPages == 0 {
		cfg.FullScanMaxPages = 1000
	}
	if cfg.BackoffWindow == 0 {
		cfg.BackoffWindow = 48 * time.Hour
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	db, err := store.Open(filepath.Join(cfg.DataDir, "jobs.db"), false)
	if err != nil {
		return res, err
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		return res, err
	}
	if err := db.Seed(ctx); err != nil {
		return res, err
	}

	ssoc, err := db.LoadSSOCMap(ctx)
	if err != nil {
		return res, err
	}
	classifier := classify.New(ssoc)

	// pre-run watermark: NULL means first run -> baseline full scan.
	var watermark time.Time
	haveWatermark := false
	wm0, err := db.QueryWatermark(ctx)
	if err != nil {
		return res, err
	}
	// baseline = first run (no watermark): full scan but recorded as an
	// incremental run (BDD "首次运行执行基线扫描"); reconcile = weekly full
	// scan with close lifecycle. Only reconcile applies close logic.
	isReconcile := cfg.Reconcile
	baseline := !isReconcile && !wm0.Valid
	if wm0.Valid {
		if t, perr := time.Parse(time.RFC3339, wm0.String); perr == nil {
			watermark = t
			haveWatermark = true
		}
	}
	kind := store.RunIncremental
	maxPages := cfg.MaxPages
	if isReconcile {
		kind = store.RunReconcile
	}
	if isReconcile || baseline {
		maxPages = cfg.FullScanMaxPages
	}

	runID, err := db.StartRun(ctx, kind)
	if err != nil {
		return res, err
	}
	res.Kind = kind

	archive, err := mcf.NewArchiveWriter(filepath.Join(cfg.DataDir, "raw"), now())
	if err != nil {
		db.FinishRun(ctx, runID, store.StatusFailed, 0, 0, 0, 0, 0, 0, 0, 1, "")
		return res, err
	}
	defer archive.Close()

	client := mcf.NewClientWithRT(cfg.BaseURL, cfg.UA, cfg.Limit, maxPages, cfg.Delay, cfg.Transport)

	seen := map[string]bool{}
	newCount, updatedCount, seenCount := 0, 0, 0

	var scanErr error
	summary, err := client.EachPage(ctx, func(jobs []mcf.Job, _ int) (bool, error) {
		// pass 1: archive the whole page first (archive-before-parse)
		locs := make([]string, len(jobs))
		for i := range jobs {
			loc, aerr := archive.Write(jobs[i])
			if aerr != nil {
				res.Errors++
				slog.Warn("archive write failed", "uuid", jobs[i].UUID, "err", aerr)
				continue
			}
			locs[i] = loc
			seenCount++
		}
		// pass 2: incremental window stop + candidate upsert
		for i := range jobs {
			if locs[i] == "" {
				continue // archive failed; skip (DB stays rebuildable from archive)
			}
			if !isReconcile {
				t, terr := time.Parse(time.RFC3339, jobs[i].Metadata.NewPostingDate)
				if terr == nil && haveWatermark && t.Before(watermark.Add(-cfg.BackoffWindow)) {
					return true, nil
				}
			}
			r := classifier.Classify(jobs[i])
			if r.IsCandidate {
				isNew, uerr := db.UpsertJob(ctx, jobs[i], r, locs[i])
				if uerr != nil {
					res.Errors++
					slog.Warn("upsert failed", "uuid", jobs[i].UUID, "err", uerr)
					continue
				}
				if isNew {
					newCount++
				} else {
					updatedCount++
				}
				if isReconcile {
					seen[jobs[i].UUID] = true
				}
			}
		}
		return false, nil
	})
	if err != nil {
		scanErr = err
		res.Errors++
		slog.Warn("scan incomplete", "err", err)
	}

	wm, werr := db.QueryWatermark(ctx)
	if werr == nil && wm.Valid {
		res.Watermark = wm.String
	}

	status := store.StatusSuccess
	if scanErr != nil {
		status = store.StatusPartial // data preserved, never fail the batch
	}

	// reconcile close logic is gated on a clean scan (docs/02 §4.1: success
	// gate; partial scans must never mass-close).
	if isReconcile && status == store.StatusSuccess {
		deviation := 0.0
		if summary.Total > 0 {
			deviation = float64(absInt(summary.Total-summary.Jobs)) / float64(summary.Total)
		}
		if deviation >= 0.02 {
			status = store.StatusPartial
			res.Errors++
			slog.Warn("reconcile deviation too high, skipping close", "dev", deviation, "seen", summary.Jobs, "total", summary.Total)
		} else {
			today := now().UTC().Format("2006-01-02")
			expired, cerr := db.CloseExpired(ctx, today)
			if cerr != nil {
				res.Errors++
				slog.Warn("close expired failed", "err", cerr)
			}
			missed, merr := db.MissAndClose(ctx, seen)
			if merr != nil {
				res.Errors++
				slog.Warn("miss-and-close failed", "err", merr)
			}
			res.Closed = expired + missed
		}
	}

	res.Pages = summary.Pages
	res.Seen = seenCount
	res.New = newCount
	res.Updated = updatedCount
	res.Status = status
	if err := db.FinishRun(ctx, runID, status, summary.Pages, seenCount, newCount, updatedCount, res.Closed, 0, 0, res.Errors, res.Watermark); err != nil {
		return res, err
	}
	slog.Info("ingest run finished",
		"kind", kind, "status", status, "pages", summary.Pages,
		"seen", seenCount, "new", newCount, "updated", updatedCount,
		"closed", res.Closed, "errors", res.Errors, "watermark", res.Watermark)
	return res, nil
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
