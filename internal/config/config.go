package config

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultListen      = "0.0.0.0:8765"
	DefaultIntervalSec = 900
	DefaultStatePath   = "$HOME/.local/state/ai-usage/state.json"
	DefaultStatsPath   = "$HOME/.local/state/ai-usage/stats.json"
	DefaultTZ          = "America/Sao_Paulo"
	MinIntervalSec     = 300
)

// PlaceholderDeviceToken is the insecure placeholder that used to ship as the
// default DeviceToken. It is exported so the serve boundary (api.New) can
// reject it: a stale config must never silently bind 0.0.0.0 with a token
// that is published in this repository's history.
const PlaceholderDeviceToken = "change-me-32-chars"

// Config holds all runtime configuration for the usaged service.
type Config struct {
	Listen         string            // HTTP listen address
	Interval       time.Duration     // poll interval (must be >= 300s)
	DeviceToken    string            // token for non-loopback clients
	DeviceOTAPass  string            // device's ArduinoOTA password, sent over BLE; "" leaves OTA disarmed
	StatePath      string            // on-disk state file (with $HOME expanded)
	TZ             *time.Location    // display timezone for reset text
	OpenRouterKeys map[string]string // "main", "fallback"
	GroqKey        string
	GroqProbe      bool   // probe rate-limit headroom on Groq
	FixstDir       string // offline mode: serve everything from here
	FixturesDir    string // offline mode: serve everything from here
	Scenario       string // fixture scenario overlay (from testdata/scenarios/)
	LogLevel       slog.Level
	JSONOutput     bool   // once command: emit snapshot as indented JSON
	ClaudeDir      string // Claude Code transcript dir (default ~/.claude/projects/)
	CodexDir       string // Codex rollout dir (default ~/.codex/sessions/)
	CodexSource    string // codex data source: "http" (wham/usage) or "cli" (app-server)
	ClaudeSource   string // claude data source: "auto" (statusline+oauth fallback) or "oauth" or "statusline"
	StatsIndexPath string // on-disk stats index file (default ~/.local/state/ai-usage/stats-index.json)
	StatsPath      string // on-disk stats report (default ~/.local/state/ai-usage/stats.json)

	// Publish target (task 59): after every poll whose rev changed, PUT the
	// snapshot JSON to PublishURL with Authorization: Bearer <token>. Both
	// empty by default (publishing disabled).
	PublishURL   string
	PublishToken string

	// Alert thresholds (from YAML config file; used by snapshot formatting)
	AlertOpenRouterLowUSD float64
	AlertQuotaWarnPct     int

	// ConfigPath is the path of the YAML config file that was loaded, if any.
	// Used by the API server to persist runtime config changes (e.g. interval).
	ConfigPath string

	// ProviderConfigs from YAML: toggles, labels, and key_env names, keyed
	// by provider ID (e.g. "openrouter:main"). A provider with Enabled=false
	// is dropped from the snapshot entirely.
	ProviderConfigs map[string]YamlProvider
}

// DefaultConfigFile is the default path for the optional YAML config.
const DefaultConfigFile = "$HOME/.config/ai-usage/config.yaml"

// Load reads configuration in precedence order: defaults, then an optional
// YAML file (--config or the default path), then environment variables,
// then command-line flags. Only explicitly-set flags override.
func Load(args []string, getenv func(string) string) (Config, error) {
	cfg := Config{
		OpenRouterKeys:  map[string]string{},
		ProviderConfigs: map[string]YamlProvider{},
	}

	// Defaults
	cfg.Listen = DefaultListen
	cfg.Interval = time.Duration(DefaultIntervalSec) * time.Second
	cfg.DeviceToken = ""
	cfg.StatePath = DefaultStatePath
	cfg.StatsPath = DefaultStatsPath
	cfg.GroqProbe = false
	cfg.LogLevel = slog.LevelInfo
	cfg.AlertOpenRouterLowUSD = 1.00 // sensible default for OpenRouter low-balance warning
	cfg.AlertQuotaWarnPct = 95
	cfg.CodexSource = "http"
	cfg.ClaudeSource = "auto"

	tz, err := time.LoadLocation(DefaultTZ)
	if err != nil {
		tz = time.UTC
	}
	cfg.TZ = tz

	// Expand $HOME early (needed for default config path and path defaults).
	home := getenv("HOME")
	if home == "" {
		home = osUserHome()
	}

	// Flags (parsed early so --config can locate the YAML file).
	fs := flag.NewFlagSet("usaged", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	flagConfig := fs.String("config", "", "config file path (default: ~/.config/usaged/config.yaml)")
	flagListen := fs.String("listen", "", "HTTP listen address")
	flagInterval := fs.Int("interval", 0, "poll interval in seconds")
	flagState := fs.String("state", "", "state file path")
	flagFixtures := fs.String("fixtures", "", "fixtures directory for offline mode")
	flagScenario := fs.String("scenario", "", "fixture scenario overlay (from testdata/scenarios/)")
	flagTZ := fs.String("tz", "", "timezone for display (e.g. America/Sao_Paulo)")
	flagJSON := fs.Bool("json", false, "once only: emit the snapshot as indented JSON")

	if err := fs.Parse(args); err != nil {
		return cfg, err
	}

	setFlags := map[string]bool{}
	fs.Visit(func(f *flag.Flag) {
		setFlags[f.Name] = true
	})

	// --- File config (below env, below flags in precedence) ---
	// Two separate questions, and conflating them was a bug: WHERE the config
	// file lives, and whether one exists YET.
	//
	// ConfigPath is recorded either way, because a settings change has to have
	// somewhere to go. Previously it was left empty when no file existed, and
	// internal/api gates every persist on `s.configPath != ""` — so on a fresh
	// install, where no file exists by definition, every Save answered ok:true
	// and wrote nothing. That is how "Saved" came to mean nothing.
	configPath := ""
	if setFlags["config"] {
		configPath = *flagConfig
	} else {
		configPath = strings.ReplaceAll(DefaultConfigFile, "$HOME", home)
	}
	cfg.ConfigPath = configPath

	// Only READ it if it is actually there. An absent default config is the
	// normal state, not an error; an absent --config the user named IS one.
	_, statErr := os.Stat(configPath)
	readIt := statErr == nil || setFlags["config"]

	if readIt {
		text, readErr := os.ReadFile(configPath)
		if readErr != nil {
			return cfg, fmt.Errorf("config file %s: %w", configPath, readErr)
		}
		fc, parseErr := ParseYAML(string(text))
		if parseErr != nil {
			return cfg, fmt.Errorf("config file %s: %w", configPath, parseErr)
		}
		applyFileConfig(&cfg, &fc, home)
		cfg.ConfigPath = configPath
	}

	// Environment variables (override file)
	if v := getenv("USAGED_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := getenv("USAGED_INTERVAL_SEC"); v != "" {
		sec, err := parseInt(v)
		if err != nil {
			return cfg, fmt.Errorf("USAGED_INTERVAL_SEC %q: %w", v, err)
		}
		cfg.Interval = time.Duration(sec) * time.Second
	}
	if v := getenv("USAGED_DEVICE_TOKEN"); v != "" {
		cfg.DeviceToken = v
	}
	// The device's own ArduinoOTA password, handed to it over BLE during
	// zero-config provisioning. Unset leaves OTA disarmed on the device, which
	// is the correct default: arming remote flashing is the owner's deliberate
	// act, not something a first boot should assume.
	if v := getenv("USAGED_DEVICE_OTA_PASS"); v != "" {
		cfg.DeviceOTAPass = v
	}
	if v := getenv("USAGED_STATE"); v != "" {
		cfg.StatePath = v
	}
	if v := getenv("USAGED_TZ"); v != "" {
		loc, err := time.LoadLocation(v)
		if err != nil {
			return cfg, fmt.Errorf("USAGED_TZ %q: %w", v, err)
		}
		cfg.TZ = loc
	}
	if v := getenv("OPENROUTER_API_KEY"); v != "" {
		cfg.OpenRouterKeys["main"] = v
	}
	if v := getenv("OPENROUTER_API_KEY_FALLBACK"); v != "" {
		cfg.OpenRouterKeys["fallback"] = v
	}
	if v := getenv("GROQ_API_KEY"); v != "" {
		cfg.GroqKey = v
	}
	if v := getenv("USAGED_GROQ_PROBE"); v != "" {
		probe, err := parseBool(v)
		if err != nil {
			return cfg, fmt.Errorf("USAGED_GROQ_PROBE %q: %w", v, err)
		}
		cfg.GroqProbe = probe
	}
	if v := getenv("USAGED_LOG_LEVEL"); v != "" {
		level, err := parseLogLevel(v)
		if err != nil {
			return cfg, err
		}
		cfg.LogLevel = level
	}
	if v := getenv("USAGED_CLAUDE_DIR"); v != "" {
		cfg.ClaudeDir = expandHome(home, v)
	}
	if v := getenv("USAGED_CODEX_DIR"); v != "" {
		cfg.CodexDir = expandHome(home, v)
	}
	if v := getenv("USAGED_CODEX_SOURCE"); v != "" {
		if v != "http" && v != "cli" {
			return cfg, fmt.Errorf("USAGED_CODEX_SOURCE %q: want \"http\" or \"cli\"", v)
		}
		cfg.CodexSource = v
	}
	if v := getenv("USAGED_CLAUDE_SOURCE"); v != "" {
		if v != "auto" && v != "oauth" && v != "statusline" {
			return cfg, fmt.Errorf("USAGED_CLAUDE_SOURCE %q: want \"auto\" | \"oauth\" | \"statusline\"", v)
		}
		cfg.ClaudeSource = v
	}
	if v := getenv("USAGED_STATS_INDEX"); v != "" {
		cfg.StatsIndexPath = expandHome(home, v)
	}
	if v := getenv("USAGED_STATS_PATH"); v != "" {
		cfg.StatsPath = expandHome(home, v)
	}
	if v := getenv("USAGED_PUBLISH_URL"); v != "" {
		cfg.PublishURL = v
	}
	if v := getenv("USAGED_PUBLISH_TOKEN"); v != "" {
		cfg.PublishToken = v
	}

	// Flags override env
	if setFlags["config"] {
		_ = *flagConfig // already consumed above
	}
	if setFlags["listen"] {
		cfg.Listen = *flagListen
	}
	if setFlags["interval"] {
		cfg.Interval = time.Duration(*flagInterval) * time.Second
	}
	if setFlags["state"] {
		cfg.StatePath = *flagState
	}
	if setFlags["fixtures"] {
		cfg.FixturesDir = *flagFixtures
	}
	if setFlags["scenario"] {
		cfg.Scenario = *flagScenario
	}
	if setFlags["tz"] {
		loc, err := time.LoadLocation(*flagTZ)
		if err != nil {
			return cfg, fmt.Errorf("--tz %q: %w", *flagTZ, err)
		}
		cfg.TZ = loc
	}
	if setFlags["json"] {
		cfg.JSONOutput = *flagJSON
	}

	// Expand $HOME in path defaults
	if home != "" {
		cfg.StatePath = strings.ReplaceAll(cfg.StatePath, "$HOME", home)
	}
	if home != "" {
		cfg.StatsPath = strings.ReplaceAll(cfg.StatsPath, "$HOME", home)
	}
	// Defaults for stats dirs (after home is known).
	if cfg.ClaudeDir == "" {
		cfg.ClaudeDir = expandHome(home, "~/.claude/projects/")
	}
	if cfg.CodexDir == "" {
		cfg.CodexDir = expandHome(home, "~/.codex/sessions/")
	}
	if cfg.StatsIndexPath == "" {
		cfg.StatsIndexPath = expandHome(home, "~/.local/state/ai-usage/stats-index.json")
	}

	// Validation
	if cfg.Interval < time.Duration(MinIntervalSec)*time.Second {
		return cfg, fmt.Errorf("interval must be >= %d seconds, got %v", MinIntervalSec, cfg.Interval)
	}
	if cfg.Listen == "" {
		return cfg, fmt.Errorf("listen address must not be empty")
	}

	// SECURITY (ORDER #43 BUG 47) is enforced at the SERVE boundary, in
	// api.New — not here. Load is called by `once`, `stats` and `config`
	// too, and none of them bind a socket; refusing to parse config for a
	// read-only subcommand made the skill's own documented fallback
	// (`usaged once`) fail from any shell that had not sourced .env.

	return cfg, nil
}

// applyFileConfig copies values from the parsed FileConfig into the Config.
// Only non-zero/non-empty values from the file are applied; env vars and
// flags (applied later by the caller) will override these.
func applyFileConfig(cfg *Config, fc *FileConfig, home string) {
	if fc.IntervalSec > 0 {
		cfg.Interval = time.Duration(fc.IntervalSec) * time.Second
	}
	if fc.Listen != "" {
		cfg.Listen = fc.Listen
	}
	if fc.DeviceToken != "" {
		cfg.DeviceToken = fc.DeviceToken
	}
	if fc.TZ != "" {
		if loc, err := time.LoadLocation(fc.TZ); err == nil {
			cfg.TZ = loc
		}
	}
	if len(fc.Alerts) > 0 {
		if v, ok := fc.Alerts["openrouter_low_usd"]; ok {
			cfg.AlertOpenRouterLowUSD = v
		}
		if v, ok := fc.Alerts["quota_warn_pct"]; ok {
			cfg.AlertQuotaWarnPct = int(v)
		}
	}
	for _, p := range fc.Providers {
		cfg.ProviderConfigs[p.ID] = p
	}
}

// Redacted returns a map safe for logging: all secret values are
// replaced with "set(len=N)" where N is the secret's length.
func (c Config) Redacted() map[string]any {
	m := map[string]any{
		"listen":          c.Listen,
		"interval_sec":    int(c.Interval.Seconds()),
		"device_token":    fmt.Sprintf("set(len=%d)", len(c.DeviceToken)),
		"device_ota_pass": fmt.Sprintf("set(len=%d)", len(c.DeviceOTAPass)),
		"state_path":      c.StatePath,
		"stats_path":      c.StatsPath,
		"tz":              c.TZ.String(),
		"groq_probe":      c.GroqProbe,
		"log_level":       c.LogLevel.String(),
		"fixtures_dir":    c.FixturesDir,
		"scenario":        c.Scenario,
		"alerts": map[string]any{
			"openrouter_low_usd": c.AlertOpenRouterLowUSD,
			"quota_warn_pct":     c.AlertQuotaWarnPct,
		},
	}
	provConfigs := make(map[string]map[string]any, len(c.ProviderConfigs))
	for id, p := range c.ProviderConfigs {
		provConfigs[id] = map[string]any{
			"enabled": p.Enabled,
			"label":   p.Label,
			"key_env": p.KeyEnv,
			"probe":   p.Probe,
		}
		if p.Plan != nil {
			provConfigs[id]["plan"] = map[string]any{
				"cost":         p.Plan.Cost,
				"currency":     p.Plan.Currency,
				"label":        p.Plan.Label,
				"has_cost_usd": p.Plan.HasCostUSD,
			}
		}
	}
	m["provider_configs"] = provConfigs
	if c.OpenRouterKeys != nil {
		keys := make(map[string]string, len(c.OpenRouterKeys))
		for k, v := range c.OpenRouterKeys {
			keys[k] = fmt.Sprintf("set(len=%d)", len(v))
		}
		m["openrouter_keys"] = keys
	}
	m["groq_key"] = fmt.Sprintf("set(len=%d)", len(c.GroqKey))
	m["codex_source"] = c.CodexSource
	m["claude_source"] = c.ClaudeSource
	m["publish_url"] = c.PublishURL
	m["publish_token"] = fmt.Sprintf("set(len=%d)", len(c.PublishToken))
	return m
}

func parseLogLevel(s string) (slog.Level, error) {
	switch strings.ToUpper(s) {
	case "DEBUG":
		return slog.LevelDebug, nil
	case "INFO":
		return slog.LevelInfo, nil
	case "WARN":
		return slog.LevelWarn, nil
	case "ERROR":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level: %s", s)
	}
}

func parseInt(s string) (int, error) {
	return strconv.Atoi(s)
}

func parseBool(s string) (bool, error) {
	return strconv.ParseBool(s)
}

func osUserHome() string {
	if u, err := user.Current(); err == nil {
		return u.HomeDir
	}
	return ""
}

// expandHome replaces ~ or $HOME with the given home directory.
func expandHome(home, path string) string {
	if path == "" {
		return ""
	}
	if home == "" {
		home = osUserHome()
	}
	if home != "" {
		path = strings.ReplaceAll(path, "~", home)
		path = strings.ReplaceAll(path, "$HOME", home)
	}
	return path
}
