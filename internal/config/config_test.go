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
	cfg, err := Load(nil, envFrom(map[string]string{
		"USAGED_DEVICE_TOKEN": "test-token",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Listen != "0.0.0.0:8765" {
		t.Errorf("Listen = %q, want 0.0.0.0:8765", cfg.Listen)
	}
	if cfg.Interval != 900*time.Second {
		t.Errorf("Interval = %v, want 900s", cfg.Interval)
	}
	if cfg.DeviceToken != "test-token" {
		t.Errorf("DeviceToken = %q, want test-token (from env, no insecure default)", cfg.DeviceToken)
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
	if cfg.CodexSource != "http" {
		t.Errorf("CodexSource = %q, want http", cfg.CodexSource)
	}
	if cfg.ClaudeSource != "auto" {
		t.Errorf("ClaudeSource = %q, want auto", cfg.ClaudeSource)
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
		"USAGED_CODEX_SOURCE":         "cli",
		"USAGED_CLAUDE_SOURCE":        "oauth",
		"USAGED_PUBLISH_URL":          "https://example.com/snapshot",
		"USAGED_PUBLISH_TOKEN":        "publish-secret",
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
	if cfg.GroqKey != "groq-secret" {
		t.Errorf("GroqKey = %q", cfg.GroqKey)
	}
	if cfg.CodexSource != "cli" {
		t.Errorf("CodexSource = %q, want cli", cfg.CodexSource)
	}
	if cfg.ClaudeSource != "oauth" {
		t.Errorf("ClaudeSource = %q, want oauth", cfg.ClaudeSource)
	}
	if cfg.PublishURL != "https://example.com/snapshot" {
		t.Errorf("PublishURL = %q", cfg.PublishURL)
	}
	if cfg.PublishToken != "publish-secret" {
		t.Errorf("PublishToken = %q", cfg.PublishToken)
	}
}

func TestLoadPublishDefaultsEmpty(t *testing.T) {
	cfg, err := Load(nil, envFrom(map[string]string{
		"USAGED_DEVICE_TOKEN": "test-token",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PublishURL != "" {
		t.Errorf("PublishURL = %q, want empty by default", cfg.PublishURL)
	}
	if cfg.PublishToken != "" {
		t.Errorf("PublishToken = %q, want empty by default", cfg.PublishToken)
	}
}

func TestLoadFlagOverridesEnv(t *testing.T) {
	env := map[string]string{
		"USAGED_LISTEN":       "127.0.0.1:9999",
		"USAGED_INTERVAL_SEC": "600",
		"USAGED_STATE":        "$HOME/.local/state/usaged/state.json",
		"USAGED_TZ":           "America/Sao_Paulo",
		"USAGED_DEVICE_TOKEN": "test-token",
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
		"HOME":                "/tmp/testhome",
		"USAGED_STATE":        "$HOME/.local/state/usaged/state.json",
		"USAGED_DEVICE_TOKEN": "test-token",
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
		"HOME":                "/tmp/testhome",
		"USAGED_DEVICE_TOKEN": "test-token",
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

func TestLoadInvalidCodexSource(t *testing.T) {
	env := map[string]string{
		"USAGED_CODEX_SOURCE": "invalid",
	}
	_, err := Load(nil, envFrom(env))
	if err == nil {
		t.Fatal("expected error for invalid CodexSource")
	}
	if !strings.Contains(err.Error(), "USAGED_CODEX_SOURCE") {
		t.Errorf("error should mention USAGED_CODEX_SOURCE, got: %v", err)
	}
}

func TestLoadInvalidClaudeSource(t *testing.T) {
	env := map[string]string{
		"USAGED_CLAUDE_SOURCE": "invalid",
	}
	_, err := Load(nil, envFrom(env))
	if err == nil {
		t.Fatal("expected error for invalid ClaudeSource")
	}
	if !strings.Contains(err.Error(), "USAGED_CLAUDE_SOURCE") {
		t.Errorf("error should mention USAGED_CLAUDE_SOURCE, got: %v", err)
	}
}

func TestLoadFlagFixtures(t *testing.T) {
	cfg, err := Load(
		[]string{"--fixtures", "testdata/fixtures"},
		envFrom(map[string]string{"USAGED_DEVICE_TOKEN": "test-token"}),
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

// --- SECURITY (ORDER #43 BUG 47) ---------------------------------------------

func TestLoadRejectsNonLoopbackEmptyToken(t *testing.T) {
	_, err := Load(nil, envFrom(map[string]string{
		"USAGED_LISTEN": "0.0.0.0:8765",
	}))
	if err == nil {
		t.Fatal("expected error for non-loopback listen with empty token")
	}
	if !strings.Contains(err.Error(), "device token required") {
		t.Errorf("error should mention device token required, got: %v", err)
	}
}

func TestLoadRejectsNonLoopbackPlaceholderToken(t *testing.T) {
	_, err := Load(nil, envFrom(map[string]string{
		"USAGED_LISTEN":       "0.0.0.0:8765",
		"USAGED_DEVICE_TOKEN": "change-me-32-chars",
	}))
	if err == nil {
		t.Fatal("expected error for placeholder token on non-loopback")
	}
	if !strings.Contains(err.Error(), "placeholder") {
		t.Errorf("error should mention placeholder, got: %v", err)
	}
}

func TestLoadLoopbackEmptyTokenOK(t *testing.T) {
	cfg, err := Load(nil, envFrom(map[string]string{
		"USAGED_LISTEN": "127.0.0.1:8765",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DeviceToken != "" {
		t.Errorf("DeviceToken = %q, want empty on loopback", cfg.DeviceToken)
	}
}

func TestLoadNonLoopbackWithRealTokenOK(t *testing.T) {
	cfg, err := Load(nil, envFrom(map[string]string{
		"USAGED_LISTEN":       "0.0.0.0:8765",
		"USAGED_DEVICE_TOKEN": "real-secret-token",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DeviceToken != "real-secret-token" {
		t.Errorf("DeviceToken = %q, want real-secret-token", cfg.DeviceToken)
	}
}
