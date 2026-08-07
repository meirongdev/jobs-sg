// Command report materialises weekly metrics and renders the self-contained
// HTML + Markdown report for the previous ISO week (Monday 09:00 SGT), then
// updates index/latest and optionally pushes a Telegram summary.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/meirongdev/jobs-sg/internal/report"
	"github.com/meirongdev/jobs-sg/internal/store"
)

func main() {
	var (
		dataDir = flag.String("data-dir", "/data", "directory holding jobs.db and raw/")
		week    = flag.String("week", "", "target Monday YYYY-MM-DD (SGT); default = most recent completed week")
		baseURL = flag.String("base-url", "https://jobs.meirong.dev", "public base URL for report links")
	)
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	sgt := time.FixedZone("SGT", 8*3600)
	var monday time.Time
	if *week != "" {
		t, err := time.ParseInLocation("2006-01-02", *week, sgt)
		if err != nil {
			slog.Error("bad --week", "err", err)
			os.Exit(1)
		}
		monday = t
	} else {
		now := time.Now().In(sgt)
		offset := (int(now.Weekday()) + 6) % 7 // days since Monday
		thisMonday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, sgt).AddDate(0, 0, -offset)
		monday = thisMonday.AddDate(0, 0, -7) // completed week
	}

	db, err := store.Open(filepath.Join(*dataDir, "jobs.db"), false)
	if err != nil {
		slog.Error("open db", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.Migrate(context.Background()); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}

	r, err := report.ComputeMetrics(context.Background(), db, monday)
	if err != nil {
		slog.Error("compute metrics", "err", err)
		os.Exit(1)
	}

	html, err := report.RenderHTML(r)
	if err != nil {
		slog.Error("render html", "err", err)
		os.Exit(1)
	}
	md, err := report.RenderMarkdown(r)
	if err != nil {
		slog.Error("render md", "err", err)
		os.Exit(1)
	}

	outDir := filepath.Join(*dataDir, "report")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		slog.Error("mkdir report", "err", err)
		os.Exit(1)
	}
	htmlPath := filepath.Join(outDir, r.WeekLabel+".html")
	mdPath := filepath.Join(outDir, r.WeekLabel+".md")
	if err := os.WriteFile(htmlPath, []byte(html), 0o644); err != nil {
		slog.Error("write html", "err", err)
		os.Exit(1)
	}
	if err := os.WriteFile(mdPath, []byte(md), 0o644); err != nil {
		slog.Error("write md", "err", err)
		os.Exit(1)
	}
	// index.html = latest report
	if err := os.WriteFile(filepath.Join(outDir, "latest.html"), []byte(html), 0o644); err != nil {
		slog.Error("write latest", "err", err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(outDir, "index.html"), []byte(html), 0o644); err != nil {
		slog.Error("write index", "err", err)
		os.Exit(1)
	}

	// record the report run in ingest_run
	runID, err := db.StartRun(context.Background(), store.RunReport)
	if err == nil {
		db.FinishRun(context.Background(), runID, store.StatusSuccess, 0, 0, 0, 0, 0, 0, 0, 0, r.WeekStart)
	}

	// Telegram summary (env-driven; skip when unset)
	tg := &report.Telegram{
		Token:    os.Getenv("TELEGRAM_BOT_TOKEN"),
		ChatID:   os.Getenv("TELEGRAM_CHAT_ID"),
		ThreadID: os.Getenv("TELEGRAM_THREAD_ID"),
	}
	if tg.Enabled() {
		summary := report.TelegramSummary(r, *baseURL)
		if err := tg.SendSummary(context.Background(), summary); err != nil {
			slog.Warn("telegram push failed (non-fatal)", "err", err)
		} else {
			slog.Info("telegram pushed", "week", r.WeekLabel)
		}
	} else {
		slog.Info("telegram disabled (env unset)")
	}

	slog.Info("report generated", "week", r.WeekLabel, "new_jobs", r.NewJobs, "html", htmlPath)
}
