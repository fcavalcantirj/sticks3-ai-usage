package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

// envFrom returns a getenv function backed by the given map.
func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(nil, envFrom(map[string]string{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Listen != "0.0.0.0:8765" {
		t.Errorf("Listen = %q, want 0.0.0.0:8765", cfg.Listen)
	}
	if cfg.Interval != 900*time.Second {
		t.Errorf("Interval = %v, want 900s", cfg.Interval)
	}
	if cfg.DeviceToken != "change-me-32-chars" {
		t.Errorf("DeviceToken = %q", cfg.DeviceToken)
	}
	if cfg.GroqProbe {
		t.Error("GroqProbe should default to false")
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelInfo)
	}
	if cfg.TZ == nil {
		t.Error("TZ should not be nil")
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	env := map[string]string{
		"USAGED_LISTEN":               "127.0.0.1:9999",
		"USAGED_INTERVAL_SEC":         "600",
		"USAGED_DEVICE_TOKEN":         "my-device-token",
		"USAGED_STATE":                "/tmp/test/state.json",
		"USAGED_TZ":                   "UTC",
		"OPENROUTER_API_KEY":          "or-main-key",
		"OPENROUTER_API_KEY_FALLBACK": "or-fb-key",
		"GROQ_API_KEY":                "groq-secret",
		"USAGED_GROQ_PROBE":           "1",
		"USAGED_LOG_LEVEL":            "debug",
	}
	cfg, err := Load(nil, envFrom(env))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Listen != "127.0.0.1:9999" {
		t.Errorf("Listen = %q", cfg.Listen)
	}
	if cfg.Interval != 600*time.Second {
		t.Errorf("Interval = %v, want 600s", cfg.Interval)
	}
	if cfg.TZ.String() != "UTC" {
		t.Errorf("TZ = %q, want UTC", cfg.TZ.String())
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelDebug)
	}
	if cfg.GroqProbe != true {
		t.Error("GroqProbe should be true")
	}
	if cfg.OpenRouterKeys["main"] != "or-main-key" {
		t.Errorf("OpenRouterKeys[main] = %q", cfg.OpenRouterKeys["main"])
	}
	if cfg.OpenRouterKeys["fallback"] != "or-fb-key" {
		t.Errorf("OpenRouterKeys[fallback] = %q", cfg.OpenRouterKeys["fallback"])
	}
}

func TestLoadFlagOverridesEnv(t *testing.T) {
	env := map[string]string{
		"USAGED_LISTEN":       "127.0.0.1:9999",
		"USAGED_INTERVAL_SEC": "600",
		"USAGED_STATE":        "$HOME/.local/state/usaged/state.json",
		"USAGED_TZ":           "America/Sao_Paulo",
	}
	cfg, err := Load(
		[]string{"--listen", "0.0.0.0:8080", "--interval", "450"},
		envFrom(env),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Listen != "0.0.0.0:8080" {
		t.Errorf("Listen = %q, want 0.0.0.0:8080 (flag)", cfg.Listen)
	}
	if cfg.Interval != 450*time.Second {
		t.Errorf("Interval = %v, want 450s (flag)", cfg.Interval)
	}
}

func TestLoadIntervalTooLow(t *testing.T) {
	env := map[string]string{
		"USAGED_INTERVAL_SEC": "100",
	}
	_, err := Load(nil, envFrom(env))
	if err == nil {
		t.Fatal("expected error for interval < 300s, got nil")
	}
	if !strings.Contains(err.Error(), "300") {
		t.Errorf("error should mention 300, got: %v", err)
	}
}

func TestLoadFlagIntervalTooLow(t *testing.T) {
	_, err := Load(
		[]string{"--interval", "200"},
		envFrom(map[string]string{}),
	)
	if err == nil {
		t.Fatal("expected error for flag interval < 300s")
	}
}

func TestLoadHomeExpansion(t *testing.T) {
	env := map[string]string{
		"HOME":         "/tmp/testhome",
		"USAGED_STATE": "$HOME/.local/state/usaged/state.json",
	}
	cfg, err := Load(nil, envFrom(env))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.StatePath != "/tmp/testhome/.local/state/usaged/state.json" {
		t.Errorf("StatePath = %q, want expanded path", cfg.StatePath)
	}
}

func TestLoadDefaultHomeExpansion(t *testing.T) {
	// Default StatePath contains $HOME; should be expanded even without USAGED_STATE
	env := map[string]string{
		"HOME": "/tmp/testhome",
	}
	cfg, err := Load(nil, envFrom(env))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(cfg.StatePath, "/tmp/testhome") {
		t.Errorf("StatePath = %q, want it to start with /tmp/testhome", cfg.StatePath)
	}
	if strings.Contains(cfg.StatePath, "$HOME") {
		t.Errorf("StatePath still contains $HOME: %q", cfg.StatePath)
	}
}

func TestLoadInvalidTZ(t *testing.T) {
	env := map[string]string{
		"USAGED_TZ": "Not/A/Timezone",
	}
	_, err := Load(nil, envFrom(env))
	if err == nil {
		t.Fatal("expected error for invalid timezone")
	}
}

func TestLoadFlagFixtures(t *testing.T) {
	cfg, err := Load(
		[]string{"--fixtures", "testdata/fixtures"},
		envFrom(map[string]string{}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.FixturesDir != "testdata/fixtures" {
		t.Errorf("FixturesDir = %q", cfg.FixturesDir)
	}
}

func TestRedactedNoKeys(t *testing.T) {
	env := map[string]string{
		"USAGED_DEVICE_TOKEN":         "super-secret-token-value",
		"OPENROUTER_API_KEY":          "sk-or-v1-secret-main-key",
		"OPENROUTER_API_KEY_FALLBACK": "sk-or-v1-secret-fb-key",
		"GROQ_API_KEY":                "gsk_secret_groq_key",
	}
	cfg, err := Load(nil, envFrom(env))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	redacted := cfg.Redacted()
	blob := strings.Join([]string{
		redacted["device_token"].(string),
		redacted["groq_key"].(string),
	}, " ")
	keys, _ := redacted["openrouter_keys"].(map[string]string)
	for _, v := range keys {
		blob += " " + v
	}

	for _, secret := range []string{
		"super-secret-token-value",
		"sk-or-v1-secret-main-key",
		"sk-or-v1-secret-fb-key",
		"gsk_secret_groq_key",
	} {
		if strings.Contains(blob, secret) {
			t.Errorf("redacted output contains secret %q", secret)
		}
	}
	// Check that redacted values have the expected format
	if !strings.HasPrefix(blob, "set(len=") {
		t.Errorf("redacted value should start with 'set(len=', got %q", blob)
	}
}
