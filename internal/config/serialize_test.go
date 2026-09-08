package config

import (
	"strings"
	"testing"
)

func TestSerializeYAMLRoundTrip(t *testing.T) {
	fc := FileConfig{
		IntervalSec: 600,
		Listen:      "0.0.0.0:8765",
		TZ:          "America/Sao_Paulo",
		Alerts: map[string]float64{
			"openrouter_low_usd":    1.0,
			"quota_warn_5h_pct":     70.0,
			"quota_warn_weekly_pct": 60.0,
		},
		Providers: []YamlProvider{
			{
				ID:      "openrouter:main",
				Enabled: true,
				Label:   "OR Main",
				KeyEnv:  "OPENROUTER_API_KEY",
			},
			{
				ID:      "openrouter:fallback",
				Enabled: false,
				Label:   "OR Fallback",
				KeyEnv:  "OPENROUTER_API_KEY_FALLBACK",
			},
		},
	}

	out, err := SerializeYAML(fc)
	if err != nil {
		t.Fatalf("SerializeYAML: %v", err)
	}

	// Round-trip: the serialized YAML should parse back to the same config.
	parsed, err := ParseYAML(out)
	if err != nil {
		t.Fatalf("ParseYAML round-trip: %v\n%s", err, out)
	}
	if parsed.IntervalSec != 600 {
		t.Errorf("round-trip IntervalSec = %d, want 600", parsed.IntervalSec)
	}
	if parsed.Listen != "0.0.0.0:8765" {
		t.Errorf("round-trip Listen = %q, want 0.0.0.0:8765", parsed.Listen)
	}
	if parsed.TZ != "America/Sao_Paulo" {
		t.Errorf("round-trip TZ = %q, want America/Sao_Paulo", parsed.TZ)
	}
	if len(parsed.Providers) != 2 {
		t.Fatalf("round-trip Providers = %d, want 2", len(parsed.Providers))
	}
	if parsed.Providers[0].ID != "openrouter:main" {
		t.Errorf("round-trip Providers[0].ID = %q", parsed.Providers[0].ID)
	}
	if parsed.Providers[1].Enabled != false {
		t.Errorf("round-trip Providers[1].Enabled = true, want false")
	}
	if parsed.Alerts["quota_warn_5h_pct"] != 70 {
		t.Errorf("round-trip Alerts[quota_warn_5h_pct] = %v, want 70", parsed.Alerts["quota_warn_5h_pct"])
	}
	if parsed.Alerts["quota_warn_weekly_pct"] != 60 {
		t.Errorf("round-trip Alerts[quota_warn_weekly_pct] = %v, want 60", parsed.Alerts["quota_warn_weekly_pct"])
	}
}

func TestSerializeYAMLPlanBlock(t *testing.T) {
	plan := PlanConfig{
		Cost:       200,
		Currency:   "USD",
		CostUSD:    200,
		HasCostUSD: true,
		Label:      "Max (20x)",
	}
	fc := FileConfig{
		Providers: []YamlProvider{
			{ID: "claude", Plan: &plan},
		},
	}
	out, err := SerializeYAML(fc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "plan:") {
		t.Error("missing plan: block")
	}
	if !strings.Contains(out, "cost: 200") {
		t.Error("missing cost: 200")
	}
	if !strings.Contains(out, "cost_usd: 200") {
		t.Error("missing cost_usd: 200")
	}
	if !strings.Contains(out, "label: \"Max (20x)\"") {
		t.Error("missing label")
	}

	// Round-trip
	parsed, err := ParseYAML(out)
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if len(parsed.Providers) != 1 || parsed.Providers[0].Plan == nil {
		t.Fatal("plan not parsed")
	}
	if parsed.Providers[0].Plan.Cost != 200 {
		t.Errorf("Cost = %v, want 200", parsed.Providers[0].Plan.Cost)
	}
}

func TestSerializeYAMLBRLNoCostUSD(t *testing.T) {
	// BRL without cost_usd should not emit cost_usd (has_ratio will be false).
	fc := FileConfig{
		Providers: []YamlProvider{
			{
				ID: "codex",
				Plan: &PlanConfig{
					Cost:       110,
					Currency:   "BRL",
					HasCostUSD: false,
					Label:      "Plus",
				},
			},
		},
	}
	out, err := SerializeYAML(fc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "cost_usd") {
		t.Error("BRL without cost_usd should not emit cost_usd")
	}

	// Round-trip should parse back without cost_usd.
	parsed, err := ParseYAML(out)
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if parsed.Providers[0].Plan.HasCostUSD {
		t.Error("HasCostUSD should be false after round-trip")
	}
}

func TestSerializeYAMLNoProviders(t *testing.T) {
	fc := FileConfig{
		IntervalSec: 900,
	}
	out, err := SerializeYAML(fc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "providers:") {
		t.Error("should not emit providers: when empty")
	}
}
