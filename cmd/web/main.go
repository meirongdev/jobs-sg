// Command web serves the job-seeker site read-only (/, /tech, /pay,
// /w/{YYYY-Www}, /ops, /healthz) from a read-only SQLite handle (docs/02 §4.4),
// and exposes /metrics on a second listener that the public route never reaches.
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
		dataDir     = flag.String("data-dir", "/data", "directory holding jobs.db and report/")
		addr        = flag.String("addr", ":8080", "listen address for the public site")
		metricsAddr = flag.String("metrics-addr", ":9090", "listen address for /metrics (not routed publicly)")
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

	site := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	metrics := &http.Server{
		Addr:              *metricsAddr,
		Handler:           srv.MetricsHandler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Either listener failing takes the process down. A pod that serves pages
	// but silently stopped exposing /metrics looks healthy while every alert in
	// docs/04 §3.2 goes blind — the exact silent failure this project treats as
	// worse than a crash. Buffered so the surviving goroutine cannot leak.
	errc := make(chan error, 2)
	serve := func(name string, s *http.Server) {
		slog.Info("listening", "server", name, "addr", s.Addr)
		if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("serve failed", "server", name, "addr", s.Addr, "err", err)
			errc <- err
			return
		}
		errc <- nil
	}
	go serve("metrics", metrics)
	go serve("site", site)

	slog.Info("web serving", "addr", *addr, "metrics_addr", *metricsAddr, "data_dir", *dataDir)
	if err := <-errc; err != nil {
		os.Exit(1)
	}
}
