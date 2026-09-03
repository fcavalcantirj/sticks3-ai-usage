package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"usaged/internal/config"
	"usaged/internal/creds"
	"usaged/internal/httpx"
	"usaged/internal/providers"
	"usaged/internal/sched"
	"usaged/internal/snapshot"
)

// nowFunc is the clock used by the once command. Overridden in tests for
// deterministic output; defaults to time.Now in production.
var nowFunc = time.Now

// runOnce implements the `usaged once` subcommand: it builds the fetcher list
// from config, runs a single scheduler poll, and prints the snapshot as a
// human table (default) or indented JSON (--json). It exits 0 when every
// provider is ok/stale, 3 when any is auth/error/off so a cron can alert.
func runOnce(args []string, stdout io.Writer) int {
	cfg, err := config.Load(args, os.Getenv)
	if err != nil {
		fmt.Fprint(stdout, err.Error()+"\n")
		return 2
	}

	// Logging: JSON handler to stderr at the configured level. Restored on exit
	// so tests don't leak the handler to other packages.
	prevLogger := slog.Default()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)
	defer slog.SetDefault(prevLogger)

	logger.Info("usaged once",
		"fixtures", cfg.FixturesDir,
		"interval_sec", int(cfg.Interval.Seconds()),
		"tz", cfg.TZ.String(),
		"json", cfg.JSONOutput,
	)

	fetchers := buildFetchers(cfg)
	statePath := statePathForOnce(cfg, args)

	ctx := context.Background()
	clock := nowFunc
	s := sched.NewScheduler(fetchers, cfg.Interval, statePath, clock, logger)
	s.PollOnce(ctx)

	snap := s.Current()
	snap.NextSec = int(cfg.Interval.Seconds()) // v1 contract: seconds to next poll

	if cfg.JSONOutput {
		return renderJSON(snap, stdout, logger)
	}
	renderTable(snap, stdout, cfg.TZ)
	return exitCode(snap)
}

// buildFetchers assembles the provider fetcher list from config. Claude and
// Codex are always present; OpenRouter/Groq have not arrived yet, so they are
// registered as placeholder off blocks to keep the canonical provider order.
func buildFetchers(cfg config.Config) []providers.Fetcher {
	client := &httpx.Client{UserAgent: "usaged/0.1"}

	var runner creds.Runner
	if cfg.FixturesDir != "" {
		client.HTTP = &http.Client{Transport: httpx.NewFixtureTransport(cfg.FixturesDir)}
		runner = creds.FixtureRunner(cfg.FixturesDir)
	} else {
		runner = creds.ExecRunner{}
	}

	// In fixture mode the codex auth file lives alongside the other fixtures;
	// in live mode ReadCodex("") defaults to ~/.codex/auth.json.
	var authPath string
	if cfg.FixturesDir != "" {
		authPath = filepath.Join(cfg.FixturesDir, "codex_auth.json")
	}

	loc := cfg.TZ
	var fetchers []providers.Fetcher
	fetchers = append(fetchers, providers.NewClaude(client, runner, claudeUsername(), loc))
	fetchers = append(fetchers, providers.NewCodex(client, authPath, loc))

	// Placeholder off blocks for providers with no fetcher yet.
	for _, b := range []struct{ id, label string }{
		{"openrouter:main", "OpenRouter"},
		{"openrouter:fallback", "OpenRouter"},
		{"groq", "Groq"},
	} {
		fetchers = append(fetchers, newStaticFetcher(b.id, b.label, "no key"))
	}

	return fetchers
}

// staticFetcher returns a fixed provider block without any I/O. Used for
// provider slots whose real fetchers are implemented in later tasks.
type staticFetcher struct {
	provider snapshot.Provider
}

func newStaticFetcher(id, label, msg string) *staticFetcher {
	return &staticFetcher{
		provider: snapshot.Provider{
			ID:     id,
			Label:  label,
			Status: "off",
			Msg:    msg,
		},
	}
}

func (f *staticFetcher) ID() string { return f.provider.ID }

func (f *staticFetcher) Fetch(_ context.Context, now time.Time) (snapshot.Provider, providers.Outcome) {
	p := f.provider
	p.FetchedAt = now.Unix()
	return p, providers.Outcome{}
}

// claudeUsername is the macOS account used for the Keychain lookup
// (-a flag of `security find-generic-password`). FixtureRunner ignores it;
// in live mode it falls back to the OS user.
func claudeUsername() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

// statePathForOnce returns the explicitly-configured state path when the user
// passed --state or USAGED_STATE, otherwise a fresh temp file so `once` never
// clobbers the production state.
func statePathForOnce(cfg config.Config, args []string) string {
	if os.Getenv("USAGED_STATE") != "" {
		return cfg.StatePath
	}
	for i, a := range args {
		if a == "--state" && i+1 < len(args) {
			return cfg.StatePath
		}
		if strings.HasPrefix(a, "--state=") {
			return cfg.StatePath
		}
	}
	f, err := os.CreateTemp("", "usaged-once-*.json")
	if err != nil {
		return cfg.StatePath
	}
	path := f.Name()
	f.Close()
	return path
}

// renderTable writes the human-readable usage table.
func renderTable(snap snapshot.Snapshot, w io.Writer, loc *time.Location) {
	fmt.Fprintln(w, "PROVIDER   ROW        USED   RESETS   STATUS")
	for _, p := range snap.Providers {
		for _, r := range p.Rows {
			pct := "--"
			if r.Pct != nil {
				pct = strconv.Itoa(*r.Pct)
			}
			fmt.Fprintf(w, "%-10s %-10s %4s%%  %-8s %s\n", p.Label, r.Label, pct, r.Txt, p.Status)
		}
	}
	asOf := time.Unix(snap.CheckedAt, 0).In(loc).Format("15:04")
	fmt.Fprintf(w, "rev=%s seq=%d as of %s\n", snap.Rev, snap.Seq, asOf)
}

// renderJSON writes the snapshot as indented JSON.
func renderJSON(snap snapshot.Snapshot, stdout io.Writer, logger *slog.Logger) int {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		logger.Error("marshal snapshot", "err", err)
		fmt.Fprintf(stdout, "json marshal: %v\n", err)
		return 2
	}
	fmt.Fprintln(stdout, string(data))
	return 0
}

// exitCode is 0 when every provider is ok/stale, 3 when any is auth/error/off.
func exitCode(snap snapshot.Snapshot) int {
	for _, p := range snap.Providers {
		switch p.Status {
		case "auth", "error", "off":
			return 3
		}
	}
	return 0
}
