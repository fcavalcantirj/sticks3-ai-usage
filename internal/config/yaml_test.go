package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTempYAML writes text to a temp file and returns its path.
func writeTempYAML(t *testing.T, text string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseYAMLFullExample(t *testing.T) {
	text := `interval_sec: 600
listen: "0.0.0.0:8765"
device_token: "my-token"
tz: "UTC"
alerts:
  openrouter_low_usd: 2.00
  quota_warn_pct: 90
providers:
  - id: claude
    enabled: true
  - id: openrouter:main
    enabled: true
    label: "OpenRouter main"
    key_env: OPENROUTER_API_KEY
  - id: groq
    enabled: false
    probe: true
    key_env: GROQ_API_KEY
`
	fc, err := ParseYAML(text)
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if fc.IntervalSec != 600 {
		t.Errorf("IntervalSec = %d, want 600", fc.IntervalSec)
	}
	if fc.Listen != "0.0.0.0:8765" {
		t.Errorf("Listen = %q", fc.Listen)
	}
	if fc.DeviceToken != "my-token" {
		t.Errorf("DeviceToken = %q", fc.DeviceToken)
	}
	if fc.TZ != "UTC" {
		t.Errorf("TZ = %q", fc.TZ)
	}
	if fc.Alerts["openrouter_low_usd"] != 2.00 {
		t.Errorf("Alerts[openrouter_low_usd] = %v", fc.Alerts["openrouter_low_usd"])
	}
	if fc.Alerts["quota_warn_pct"] != 90 {
		t.Errorf("Alerts[quota_warn_pct] = %v", fc.Alerts["quota_warn_pct"])
	}
	if len(fc.Providers) != 3 {
		t.Fatalf("len(Providers) = %d, want 3", len(fc.Providers))
	}
	if fc.Providers[0].ID != "claude" {
		t.Errorf("Providers[0].ID = %q", fc.Providers[0].ID)
	}
	if !fc.Providers[0].Enabled {
		t.Error("Providers[0].Enabled should default to true")
	}
	if fc.Providers[1].Label != "OpenRouter main" {
		t.Errorf("Providers[1].Label = %q", fc.Providers[1].Label)
	}
	if fc.Providers[1].KeyEnv != "OPENROUTER_API_KEY" {
		t.Errorf("Providers[1].KeyEnv = %q", fc.Providers[1].KeyEnv)
	}
	if fc.Providers[2].Enabled {
		t.Error("Providers[2].Enabled should be false")
	}
	if !fc.Providers[2].Probe {
		t.Error("Providers[2].Probe should be true")
	}
}

func TestParseYAMLRejectsTab(t *testing.T) {
	text := "interval_sec:\t900\n"
	_, err := ParseYAML(text)
	if err == nil {
		t.Fatal("expected error for tab, got nil")
	}
	if !strings.Contains(err.Error(), "tab") {
		t.Errorf("error should mention tab, got: %v", err)
	}
}

func TestParseYAMLRejectsUnknownKey(t *testing.T) {
	text := "bogus_key: 100\n"
	_, err := ParseYAML(text)
	if err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
	if !strings.Contains(err.Error(), "unknown key") {
		t.Errorf("error should mention unknown key, got: %v", err)
	}
}

func TestParseYAMLRejectsLiteralKeyField(t *testing.T) {
	text := `providers:
  - id: test
    key: sk-some-key-here
`
	_, err := ParseYAML(text)
	if err == nil {
		t.Fatal("expected error for literal 'key' field, got nil")
	}
}

func TestParseYAMLRejectsLiteralKeyEnvValue(t *testing.T) {
	text := `providers:
  - id: test
    key_env: sk-or-v1-secret
`
	_, err := ParseYAML(text)
	if err == nil {
		t.Fatal("expected error for sk- prefix in key_env, got nil")
	}
	if !strings.Contains(err.Error(), "sk-") {
		t.Errorf("error should mention sk-, got: %v", err)
	}
}

func TestParseYAMLRejectsGskKeyEnvValue(t *testing.T) {
	text := `providers:
  - id: groq
    key_env: gsk_secret_value
`
	_, err := ParseYAML(text)
	if err == nil {
		t.Fatal("expected error for gsk_ prefix in key_env, got nil")
	}
}

func TestParseYAMLRejectsBadIndentation(t *testing.T) {
	text := `alerts:
   openrouter_low_usd: 1.00
`
	_, err := ParseYAML(text)
	if err == nil {
		t.Fatal("expected error for 3-space indent (not multiple of 2), got nil")
	}
}

func TestParseYAMLRejectsUnknownAlertKey(t *testing.T) {
	text := `alerts:
  bogus_alert: 50
`
	_, err := ParseYAML(text)
	if err == nil {
		t.Fatal("expected error for unknown alert key, got nil")
	}
	if !strings.Contains(err.Error(), "unknown alert key") {
		t.Errorf("error should mention unknown alert key, got: %v", err)
	}
}

func TestParseYAMLRejectsTruncatedProviderList(t *testing.T) {
	text := `providers:
  - id: claude
`
	_, err := ParseYAML(text)
	// This should parse successfully — a provider with just id is valid.
	if err != nil {
		t.Fatalf("unexpected error for minimal provider: %v", err)
	}
}

func TestParseYAMLRejectsBadProviderField(t *testing.T) {
	text := `providers:
  - id: claude
    bogus_field: "oops"
`
	_, err := ParseYAML(text)
	if err == nil {
		t.Fatal("expected error for unknown provider field, got nil")
	}
	if !strings.Contains(err.Error(), "unknown provider field") {
		t.Errorf("error should mention unknown provider field, got: %v", err)
	}
}

func TestParseYAMLEmptyString(t *testing.T) {
	fc, err := ParseYAML("")
	if err != nil {
		t.Fatalf("ParseYAML(empty): %v", err)
	}
	if len(fc.Providers) != 0 {
		t.Errorf("len(Providers) = %d, want 0", len(fc.Providers))
	}
}

func TestParseYAMLCommentsOnly(t *testing.T) {
	text := `# just a comment
# another comment
`
	fc, err := ParseYAML(text)
	if err != nil {
		t.Fatalf("ParseYAML(comments only): %v", err)
	}
	if fc.IntervalSec != 0 {
		t.Errorf("IntervalSec = %d, want 0 (unset)", fc.IntervalSec)
	}
}

func TestParseYAMLInlineComment(t *testing.T) {
	text := `interval_sec: 900  # polling interval
listen: "0.0.0.0:8765" # address
`
	fc, err := ParseYAML(text)
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if fc.IntervalSec != 900 {
		t.Errorf("IntervalSec = %d, want 900", fc.IntervalSec)
	}
	if fc.Listen != "0.0.0.0:8765" {
		t.Errorf("Listen = %q, want 0.0.0.0:8765", fc.Listen)
	}
}

func TestLoadFileOverridesDefaults(t *testing.T) {
	text := `interval_sec: 450
listen: "127.0.0.1:7000"
`
	path := writeTempYAML(t, text)
	cfg, err := Load([]string{"--config", path}, envFrom(map[string]string{}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if int(cfg.Interval.Seconds()) != 450 {
		t.Errorf("Interval = %v, want 450s (from file)", cfg.Interval)
	}
	if cfg.Listen != "127.0.0.1:7000" {
		t.Errorf("Listen = %q, want 127.0.0.1:7000 (from file)", cfg.Listen)
	}
}

func TestLoadEnvOverridesFile(t *testing.T) {
	text := `interval_sec: 450
listen: "127.0.0.1:7000"
`
	path := writeTempYAML(t, text)
	env := map[string]string{
		"USAGED_INTERVAL_SEC": "600",
		"USAGED_LISTEN":       "0.0.0.0:9999",
		"USAGED_DEVICE_TOKEN": "test-token",
	}
	cfg, err := Load([]string{"--config", path}, envFrom(env))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if int(cfg.Interval.Seconds()) != 600 {
		t.Errorf("Interval = %v, want 600s (env overrides file)", cfg.Interval)
	}
	if cfg.Listen != "0.0.0.0:9999" {
		t.Errorf("Listen = %q, want 0.0.0.0:9999 (env overrides file)", cfg.Listen)
	}
}

func TestLoadFlagOverridesEnvAndFile(t *testing.T) {
	text := `interval_sec: 450
listen: "127.0.0.1:7000"
`
	path := writeTempYAML(t, text)
	env := map[string]string{
		"USAGED_INTERVAL_SEC": "600",
		"USAGED_LISTEN":       "0.0.0.0:9999",
		"USAGED_DEVICE_TOKEN": "test-token",
	}
	cfg, err := Load(
		[]string{"--config", path, "--listen", "0.0.0.0:8080", "--interval", "300"},
		envFrom(env),
	)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != "0.0.0.0:8080" {
		t.Errorf("Listen = %q, want 0.0.0.0:8080 (flag overrides all)", cfg.Listen)
	}
	if int(cfg.Interval.Seconds()) != 300 {
		t.Errorf("Interval = %v, want 300s (flag overrides all)", cfg.Interval)
	}
}

func TestLoadFileEnabledFalseDropsProvider(t *testing.T) {
	text := `providers:
  - id: groq
    enabled: false
    key_env: GROQ_API_KEY
`
	path := writeTempYAML(t, text)
	cfg, err := Load([]string{"--config", path}, envFrom(map[string]string{"USAGED_DEVICE_TOKEN": "test-token"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	groq, ok := cfg.ProviderConfigs["groq"]
	if !ok {
		t.Fatal("groq should be in ProviderConfigs")
	}
	if groq.Enabled {
		t.Error("groq.Enabled should be false from file")
	}
}

func TestLoadFileAlertThresholds(t *testing.T) {
	text := `alerts:
  openrouter_low_usd: 0.50
  quota_warn_pct: 80
`
	path := writeTempYAML(t, text)
	cfg, err := Load([]string{"--config", path}, envFrom(map[string]string{"USAGED_DEVICE_TOKEN": "test-token"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AlertOpenRouterLowUSD != 0.50 {
		t.Errorf("AlertOpenRouterLowUSD = %v, want 0.50", cfg.AlertOpenRouterLowUSD)
	}
	if cfg.AlertQuotaWarnPct != 80 {
		t.Errorf("AlertQuotaWarnPct = %d, want 80", cfg.AlertQuotaWarnPct)
	}
}

func TestLoadInvalidFileErrors(t *testing.T) {
	text := `bogus_key: 100
`
	path := writeTempYAML(t, text)
	_, err := Load([]string{"--config", path}, envFrom(map[string]string{}))
	if err == nil {
		t.Fatal("expected error for invalid config file")
	}
	if !strings.Contains(err.Error(), "config file") {
		t.Errorf("error should mention config file, got: %v", err)
	}
}

func TestLoadMissingFileSkips(t *testing.T) {
	// No --config flag, and default path doesn't exist in test env
	cfg, err := Load(nil, envFrom(map[string]string{"HOME": "/nonexistent-home-xyz", "USAGED_DEVICE_TOKEN": "test-token"}))
	if err != nil {
		t.Fatalf("Load without config file: %v", err)
	}
	// Should get defaults
	if cfg.Interval != 900*time.Second {
		t.Errorf("Interval = %v, want 900s", cfg.Interval)
	}
}

// ---- Plan block parsing tests ----

func TestParseYAMLPlanBlockUSD(t *testing.T) {
	text := `providers:
  - id: claude
    plan:
      cost: 200.00
      currency: USD
      label: "Max 20x"
`
	fc, err := ParseYAML(text)
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if len(fc.Providers) != 1 {
		t.Fatalf("len(Providers) = %d, want 1", len(fc.Providers))
	}
	p := fc.Providers[0]
	if p.ID != "claude" {
		t.Errorf("ID = %q, want claude", p.ID)
	}
	if p.Plan == nil {
		t.Fatal("Plan should not be nil")
	}
	if p.Plan.Cost != 200.00 {
		t.Errorf("Plan.Cost = %.2f, want 200.00", p.Plan.Cost)
	}
	if p.Plan.Currency != "USD" {
		t.Errorf("Plan.Currency = %q, want USD", p.Plan.Currency)
	}
	if p.Plan.Label != "Max 20x" {
		t.Errorf("Plan.Label = %q, want Max 20x", p.Plan.Label)
	}
	if p.Plan.HasCostUSD {
		t.Error("Plan.HasCostUSD should be false when cost_usd is absent")
	}
}

func TestParseYAMLPlanBlockBRLWithCostUSD(t *testing.T) {
	text := `providers:
  - id: codex
    plan:
      cost: 110.00
      currency: BRL
      cost_usd: 20.00
      label: "Plus"
`
	fc, err := ParseYAML(text)
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	p := fc.Providers[0]
	if p.Plan == nil {
		t.Fatal("Plan should not be nil")
	}
	if p.Plan.Cost != 110.00 {
		t.Errorf("Plan.Cost = %.2f, want 110.00", p.Plan.Cost)
	}
	if p.Plan.Currency != "BRL" {
		t.Errorf("Plan.Currency = %q, want BRL", p.Plan.Currency)
	}
	if !p.Plan.HasCostUSD {
		t.Error("Plan.HasCostUSD should be true when cost_usd is present")
	}
	if p.Plan.CostUSD != 20.00 {
		t.Errorf("Plan.CostUSD = %.2f, want 20.00", p.Plan.CostUSD)
	}
	if p.Plan.Label != "Plus" {
		t.Errorf("Plan.Label = %q, want Plus", p.Plan.Label)
	}
}

func TestParseYAMLPlanBlockBRLWithoutCostUSD(t *testing.T) {
	text := `providers:
  - id: codex
    plan:
      cost: 110.00
      currency: BRL
      label: "Plus"
`
	fc, err := ParseYAML(text)
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	p := fc.Providers[0]
	if p.Plan == nil {
		t.Fatal("Plan should not be nil")
	}
	if p.Plan.HasCostUSD {
		t.Error("Plan.HasCostUSD should be false when cost_usd is absent")
	}
}

func TestParseYAMLPlanBlockRejectsInlineValue(t *testing.T) {
	text := `providers:
  - id: claude
    plan: "bad"
`
	_, err := ParseYAML(text)
	if err == nil {
		t.Fatal("expected error for inline plan value")
	}
}

func TestParseYAMLPlanBlockEmpty(t *testing.T) {
	text := `providers:
  - id: claude
    plan:
`
	_, err := ParseYAML(text)
	if err == nil {
		t.Fatal("expected error for empty plan block")
	}
	if !strings.Contains(err.Error(), "no fields") {
		t.Errorf("error should mention 'no fields', got: %v", err)
	}
}

func TestParseYAMLPlanBlockUnknownField(t *testing.T) {
	text := `providers:
  - id: claude
    plan:
      bogus_field: 100
`
	_, err := ParseYAML(text)
	if err == nil {
		t.Fatal("expected error for unknown plan field")
	}
}

func TestLoadPlanFromConfigFile(t *testing.T) {
	text := `providers:
  - id: claude
    plan:
      cost: 200
      currency: USD
      label: "Max 20x"
  - id: codex
    plan:
      cost: 110
      currency: BRL
      label: "Plus"
`
	path := writeTempYAML(t, text)
	cfg, err := Load([]string{"--config", path}, envFrom(map[string]string{"USAGED_DEVICE_TOKEN": "test-token"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	claude, ok := cfg.ProviderConfigs["claude"]
	if !ok {
		t.Fatal("claude should be in ProviderConfigs")
	}
	if claude.Plan == nil {
		t.Fatal("claude Plan should be set")
	}
	if claude.Plan.Currency != "USD" {
		t.Errorf("claude Plan.Currency = %q, want USD", claude.Plan.Currency)
	}
	codex, ok := cfg.ProviderConfigs["codex"]
	if !ok {
		t.Fatal("codex should be in ProviderConfigs")
	}
	if codex.Plan == nil {
		t.Fatal("codex Plan should be set")
	}
	if codex.Plan.Currency != "BRL" {
		t.Errorf("codex Plan.Currency = %q, want BRL", codex.Plan.Currency)
	}
}
