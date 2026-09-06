package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"usaged/internal/api"
	"usaged/internal/config"
	"usaged/internal/creds"
	"usaged/internal/sched"
	"usaged/internal/snapshot"
	"usaged/internal/stats"
)

// runServe implements the `usaged serve` subcommand: load config, build
// fetchers, restore on-disk state, start the scheduler poller and HTTP API,
// and shut down gracefully on SIGINT/SIGTERM.
func runServe(args []string, stdout io.Writer) int {
	cfg, err := config.Load(args, os.Getenv)
	if err != nil {
		fmt.Fprintln(stdout, err.Error())
		return 2
	}

	// JSON handler to stderr at the configured level. Restore on exit so
	// tests don't leak the handler to other packages.
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)
	defer slog.SetDefault(slog.Default())

	logger.Info("usaged serve", "cfg", cfg.Redacted())

	// Scenario overlay: copy base fixtures + scenario files into a temp dir.
	fixturesDir, cleanup := resolveFixturesDir(cfg)
	defer cleanup()
	cfg.FixturesDir = fixturesDir

	// In fixtures/scenario mode, suppress the real on-disk stats paths so the
	// scheduler does not overwrite the fixture report or scan real transcript
	// directories on its first poll.
	if fixturesDir != "" {
		cfg.StatsPath = ""
		cfg.StatsIndexPath = ""
		cfg.ClaudeDir = ""
		cfg.CodexDir = ""
	}

	fetchers := buildFetchers(cfg, creds.NewKeyStore())
	clock := time.Now
	s := sched.NewScheduler(fetchers, cfg.Interval, cfg.StatePath, clock, logger)

	// Configure snapshot publishing (task 59).
	s.PublishURL = cfg.PublishURL
	s.PublishToken = cfg.PublishToken
	s.StatsCfg = stats.ScanConfig{
		TZ:        cfg.TZ,
		ClaudeDir: cfg.ClaudeDir,
		CodexDir:  cfg.CodexDir,
	}
	s.StatsScanPath = cfg.StatsIndexPath
	s.StatsPath = cfg.StatsPath

	// Restore state from disk: load and start fresh on corrupt.
	if cfg.StatePath != "" {
		state, loadErr := snapshot.Load(cfg.StatePath)
		if loadErr != nil {
			logger.Warn("load state failed, starting fresh", "err", loadErr, "path", cfg.StatePath)
		} else {
			s.LoadState(state)
		}
	}

	// Load stats index and report from disk (none in fixtures mode).
	if cfg.StatsIndexPath != "" {
		s.LoadStatsIndex(cfg.StatsIndexPath)
	}
	if cfg.StatsPath != "" {
		s.LoadStatsReport(cfg.StatsPath)
	}
	// In fixtures/scenario mode, load the stats.json from the resolved fixtures
	// dir so /v1/stats can serve the demo report (if a scenario provides it).
	statsFixturePath := filepath.Join(fixturesDir, "stats.json")
	if fixturesDir != "" {
		if _, err := os.Stat(statsFixturePath); err == nil {
			s.LoadStatsReport(statsFixturePath)
		}
	}

	// Start the background poller.
	ctx, cancel := context.WithCancel(context.Background())
	go s.Run(ctx)

	// Build and start the HTTP API.
	httpSrv, err := api.New(s, cfg, cfg.ConfigPath, logger, api.WithKeyStore(creds.NewKeyStore()))
	if err != nil {
		logger.Error("http server init failed", "err", err)
		fmt.Fprintln(stdout, err.Error())
		cancel()
		return 2
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "err", err)
		}
		cancel()
		return 1
	case <-sigCh:
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			logger.Warn("graceful shutdown error", "err", err)
		}
		cancel()
		return 0
	}
}
