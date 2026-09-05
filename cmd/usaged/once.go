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
	"strings"
	"time"

	"usaged/internal/config"
	"usaged/internal/creds"
	"usaged/internal/format"
	"usaged/internal/httpx"
	"usaged/internal/providers"
	"usaged/internal/sched"
	"usaged/internal/snapshot"
	"usaged/internal/stats"
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

	// Scenario overlay: copy base fixtures + scenario files into a temp dir.
	fixturesDir, cleanup := resolveFixturesDir(cfg)
	defer cleanup()
	cfg.FixturesDir = fixturesDir

	fetchers := buildFetchers(cfg, creds.NewKeyStore())
	statePath := statePathForOnce(cfg, args)

	ctx := context.Background()
	clock := nowFunc
	s := sched.NewScheduler(fetchers, cfg.Interval, statePath, clock, logger)
	s.PublishURL = cfg.PublishURL
	s.PublishToken = cfg.PublishToken
	s.StatsCfg = stats.ScanConfig{
		TZ:        cfg.TZ,
		ClaudeDir: cfg.ClaudeDir,
		CodexDir:  cfg.CodexDir,
	}
	s.PollOnce(ctx)

	snap := s.Current()
	snap.NextSec = int(cfg.Interval.Seconds()) // v1 contract: seconds to next poll

	if cfg.JSONOutput {
		return renderJSON(snap, stdout, logger)
	}
	format.RenderTable(snap, stdout, cfg.TZ)
	return exitCode(snap)
}

// buildFetchers assembles the provider fetcher list from config. Claude and
// Codex are always present; OpenRouter fetchers are registered when keys are
// set, otherwise static off blocks keep the canonical provider order. Groq
// arrives in a later task — keep its placeholder.
//
// Key resolution (ORDER #52 task 57): env var first (Felipe's .env keeps
// working), then the macOS Keychain as a fallback when no env var is set.
func buildFetchers(cfg config.Config, ks creds.KeyStore) []providers.Fetcher {
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

	// Claude: auto (statusline+oauth fallback), oauth, or statusline source.
	claudeOAuth := providers.NewClaude(client, runner, claudeUsername(), loc)
	claudeStatuslinePath := claudeStatuslinePath()
	switch cfg.ClaudeSource {
	case "statusline":
		fetchers = append(fetchers, providers.NewClaudeStatusline(claudeStatuslinePath, nil, loc))
	case "oauth":
		fetchers = append(fetchers, claudeOAuth)
	default: // auto
		fetchers = append(fetchers, providers.NewClaudeStatusline(claudeStatuslinePath, claudeOAuth, loc))
	}

	// Codex: HTTP (wham/usage) or CLI (app-server) source.
	switch cfg.CodexSource {
	case "cli":
		var cliRunner providers.CodexCLIRunner
		if cfg.FixturesDir != "" {
			cliRunner = providers.NewCodexFixtureRunner(cfg.FixturesDir)
		} else {
			cliRunner = providers.NewCodexCLIRunner()
		}
		fetchers = append(fetchers, providers.NewCodexCLI(cliRunner, loc))
	default:
		fetchers = append(fetchers, providers.NewCodex(client, authPath, loc))
	}

	// OpenRouter fetchers: real when keys are set (env first, then keychain),
	// static off blocks when neither source provides a key.
	for _, b := range []struct {
		id    string
		label string
		orKey string // key into cfg.OpenRouterKeys ("main"/"fallback")
	}{
		{"openrouter:main", "OpenRouter main", "main"},
		{"openrouter:fallback", "OpenRouter fallback", "fallback"},
	} {
		key := cfg.OpenRouterKeys[b.orKey]
		if key == "" && ks != nil {
			key, _, _ = ks.Get(context.Background(), b.id)
		}
		if key != "" {
			fetchers = append(fetchers, providers.NewOpenRouter(client, b.id, b.label, key))
		} else {
			fetchers = append(fetchers, newStaticFetcher(b.id, b.label, "no key"))
		}
	}

	// Groq: real fetcher when key set (env first, then keychain).
	groqKey := cfg.GroqKey
	if groqKey == "" && ks != nil {
		groqKey, _, _ = ks.Get(context.Background(), config.ProviderGroq)
	}
	if groqKey != "" {
		fetchers = append(fetchers, providers.NewGroq(client, groqKey, cfg.GroqProbeEnabled()))
	} else {
		fetchers = append(fetchers, newStaticFetcher("groq", "Groq", "no key"))
	}

	return fetchers
}

// resolveFixturesDir resolves the fixtures directory, applying a scenario
// overlay when cfg.Scenario is set. The overlay copies base fixtures into a
// temp directory, then copies scenario files from testdata/scenarios/<name>/
// on top — so routes.json, codex_auth.json, etc. can be overridden without
// touching the base fixtures. Returns the resolved dir and a cleanup func.
func resolveFixturesDir(cfg config.Config) (string, func()) {
	if cfg.FixturesDir == "" || cfg.Scenario == "" {
		return cfg.FixturesDir, func() {}
	}
	scenarioDir := filepath.Join("testdata", "scenarios", cfg.Scenario)
	tmpDir, err := os.MkdirTemp("", "usaged-scenario-*")
	if err != nil {
		slog.Warn("scenario overlay failed, using base fixtures", "err", err)
		return cfg.FixturesDir, func() {}
	}

	// CopyFS returns an error when the target file already exists, so we copy
	// files one-by-one using os.CopyFS into the root for a fresh temp dir, then
	// use copyFile (which overwrites) for the scenario overlay to replace
	// routes.json, codex_auth.json, etc.
	if err := os.CopyFS(tmpDir, os.DirFS(cfg.FixturesDir)); err != nil {
		slog.Warn("scenario base copy failed, using base fixtures", "err", err)
		os.RemoveAll(tmpDir)
		return cfg.FixturesDir, func() {}
	}
	if _, err := os.Stat(scenarioDir); err == nil {
		if err := overwriteCopyDir(tmpDir, scenarioDir); err != nil {
			slog.Warn("scenario overlay copy failed, using base+scenario", "err", err)
			// Don't fail — keep what we have (base + partial overlay)
		}
	}
	return tmpDir, func() { os.RemoveAll(tmpDir) }
}

// overwriteCopyDir copies all regular files from srcDir into dstDir,
// overwriting any existing files with the same name.
func overwriteCopyDir(dstDir, srcDir string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		src := filepath.Join(srcDir, e.Name())
		dst := filepath.Join(dstDir, e.Name())
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}
	return nil
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

// claudeStatuslinePath returns the path to the statusline tee file.
// In fixture mode, an empty path tells the statusline provider to skip the
// file and use its OAuth fallback (so fixture-mode tests exercise the OAuth
// path as before).
func claudeStatuslinePath() string {
	if os.Getenv("USAGED_FIXTURES") != "" || os.Getenv("FIXTURES") != "" {
		return ""
	}
	home := os.Getenv("HOME")
	if home == "" {
		if u, err := user.Current(); err == nil && u.HomeDir != "" {
			home = u.HomeDir
		}
	}
	return expandHomePath(home, ".local/state/usaged/claude-statusline.json")
}

func expandHomePath(home, path string) string {
	if home != "" {
		path = strings.ReplaceAll(path, "~", home)
		path = strings.ReplaceAll(path, "$HOME", home)
	}
	return path
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

// exitCode is 0 when every provider is ok/stale/off (off = not configured,
// normal), 3 when any is auth or error so a cron can alert.
func exitCode(snap snapshot.Snapshot) int {
	for _, p := range snap.Providers {
		switch p.Status {
		case "auth", "error":
			return 3
		}
	}
	return 0
}
