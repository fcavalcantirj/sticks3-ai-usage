package config

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os/user"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultListen      = "0.0.0.0:8765"
	DefaultIntervalSec = 900
	DefaultDeviceToken = "change-me-32-chars"
	DefaultStatePath   = "$HOME/.local/state/usaged/state.json"
	DefaultTZ          = "America/Sao_Paulo"
	MinIntervalSec     = 300
)

// Config holds all runtime configuration for the usaged service.
type Config struct {
	Listen         string            // HTTP listen address
	Interval       time.Duration     // poll interval (must be >= 300s)
	DeviceToken    string            // token for non-loopback clients
	StatePath      string            // on-disk state file (with $HOME expanded)
	TZ             *time.Location    // display timezone for reset text
	OpenRouterKeys map[string]string // "main", "fallback"
	GroqKey        string
	GroqProbe      bool   // probe rate-limit headroom on Groq
	FixturesDir    string // offline mode: serve everything from here
	LogLevel       slog.Level
}

// Load reads environment variables (via getenv), then overrides with flags
// parsed from args. Flags are only applied when explicitly set.
func Load(args []string, getenv func(string) string) (Config, error) {
	cfg := Config{
		OpenRouterKeys: map[string]string{},
	}

	// Defaults
	cfg.Listen = DefaultListen
	cfg.Interval = time.Duration(DefaultIntervalSec) * time.Second
	cfg.DeviceToken = DefaultDeviceToken
	cfg.StatePath = DefaultStatePath
	cfg.GroqProbe = false
	cfg.LogLevel = slog.LevelInfo

	tz, err := time.LoadLocation(DefaultTZ)
	if err != nil {
		tz = time.UTC
	}
	cfg.TZ = tz

	// Environment variables
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

	// Flags override env vars
	fs := flag.NewFlagSet("usaged", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	flagListen := fs.String("listen", "", "HTTP listen address")
	flagInterval := fs.Int("interval", 0, "poll interval in seconds")
	flagState := fs.String("state", "", "state file path")
	flagFixtures := fs.String("fixtures", "", "fixtures directory for offline mode")
	flagTZ := fs.String("tz", "", "timezone for display (e.g. America/Sao_Paulo)")

	if err := fs.Parse(args); err != nil {
		return cfg, err
	}

	// flag.Visit only calls fn for flags that were explicitly set on the
	// command line (not those left at their default).
	setFlags := map[string]bool{}
	fs.Visit(func(f *flag.Flag) {
		setFlags[f.Name] = true
	})

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
	if setFlags["tz"] {
		loc, err := time.LoadLocation(*flagTZ)
		if err != nil {
			return cfg, fmt.Errorf("--tz %q: %w", *flagTZ, err)
		}
		cfg.TZ = loc
	}

	// Expand $HOME
	home := getenv("HOME")
	if home == "" {
		home = osUserHome()
	}
	if home != "" {
		cfg.StatePath = strings.ReplaceAll(cfg.StatePath, "$HOME", home)
	}

	// Validation
	if cfg.Interval < time.Duration(MinIntervalSec)*time.Second {
		return cfg, fmt.Errorf("interval must be >= %d seconds, got %v", MinIntervalSec, cfg.Interval)
	}
	if cfg.Listen == "" {
		return cfg, fmt.Errorf("listen address must not be empty")
	}

	return cfg, nil
}

// Redacted returns a map safe for logging: all secret values are
// replaced with "set(len=N)" where N is the secret's length.
func (c Config) Redacted() map[string]any {
	m := map[string]any{
		"listen":       c.Listen,
		"interval_sec": int(c.Interval.Seconds()),
		"device_token": fmt.Sprintf("set(len=%d)", len(c.DeviceToken)),
		"state_path":   c.StatePath,
		"tz":           c.TZ.String(),
		"groq_probe":   c.GroqProbe,
		"log_level":    c.LogLevel.String(),
		"fixtures_dir": c.FixturesDir,
	}
	if c.OpenRouterKeys != nil {
		keys := make(map[string]string, len(c.OpenRouterKeys))
		for k, v := range c.OpenRouterKeys {
			keys[k] = fmt.Sprintf("set(len=%d)", len(v))
		}
		m["openrouter_keys"] = keys
	}
	m["groq_key"] = fmt.Sprintf("set(len=%d)", len(c.GroqKey))
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
