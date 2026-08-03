// Command web serves weekly reports read-only (/, /w/{YYYY-Www}, /healthz,
// /metrics) from a read-only SQLite handle (docs/02 §4.4).
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/meirongdev/jobs-sg/internal/web"
)

func main() {
	var (
		dataDir = flag.String("data-dir", "/data", "directory holding jobs.db and report/")
		addr    = flag.String("addr", ":8080", "listen address")
	)
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	srv, err := web.New(*dataDir, time.Now)
	if err != nil {
		slog.Error("web init", "err", err)
		os.Exit(1)
	}
	defer srv.Close()

	s := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("web serving", "addr", *addr, "data_dir", *dataDir)
	if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("web serve", "err", err)
		os.Exit(1)
	}
}
