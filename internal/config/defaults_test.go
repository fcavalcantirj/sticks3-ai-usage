package config

import "testing"

// TestPlanPresets: the published list prices are the same for everyone, so
// nobody should have to type them. The picker offers them; the numbers here
// come from anthropic.com and openai.com/business/pricing (BRL), read
// 2026-09-07.
func TestPlanPresets(t *testing.T) {
	byProvider := PlanPresets()

	claude := byProvider[ProviderClaude]
	if len(claude) == 0 {
		t.Fatal("no Claude plan presets")
	}
	want := map[string]struct {
		cost    float64
		costUSD float64
	}{
		"Pro":     {110, 20},
		"Max 5x":  {550, 100},
		"Max 20x": {1100, 200},
	}
	seen := map[string]bool{}
	for _, p := range claude {
		w, ok := want[p.Label]
		if !ok {
			continue
		}
		seen[p.Label] = true
		if p.Cost != w.cost {
			t.Errorf("Claude %s cost = %v, want %v BRL", p.Label, p.Cost, w.cost)
		}
		if p.Currency != "BRL" {
			t.Errorf("Claude %s currency = %q, want BRL", p.Label, p.Currency)
		}
		if !p.HasCostUSD || p.CostUSD != w.costUSD {
			t.Errorf("Claude %s cost_usd = %v (set=%v), want %v", p.Label, p.CostUSD, p.HasCostUSD, w.costUSD)
		}
	}
	for l := range want {
		if !seen[l] {
			t.Errorf("Claude preset %q missing", l)
		}
	}

	// Max 20x is exactly double Max 5x — the pricing page says "choose 5x or
	// 20x" from one "From R$550" figure, so this relationship is the check
	// that the table was not mistyped.
	var m5, m20 float64
	for _, p := range claude {
		if p.Label == "Max 5x" {
			m5 = p.Cost
		}
		if p.Label == "Max 20x" {
			m20 = p.Cost
		}
	}
	if m20 != m5*2 {
		t.Errorf("Max 20x (%v) should be double Max 5x (%v)", m20, m5)
	}

	if len(byProvider[ProviderCodex]) == 0 {
		t.Error("no ChatGPT/Codex plan presets")
	}
}

// TestPlanPresetsCoverTheDefaults: every built-in default plan must appear in
// the picker, or the settings page opens showing a plan the dropdown cannot
// reproduce.
func TestPlanPresetsCoverTheDefaults(t *testing.T) {
	presets := PlanPresets()
	for _, d := range DefaultProviders() {
		if d.Plan == nil {
			continue
		}
		found := false
		for _, p := range presets[d.ID] {
			if p.Label == d.Plan.Label && p.Cost == d.Plan.Cost && p.Currency == d.Plan.Currency {
				found = true
			}
		}
		if !found {
			t.Errorf("provider %s default plan %q (%v %s) is not in the presets",
				d.ID, d.Plan.Label, d.Plan.Cost, d.Plan.Currency)
		}
	}
}
