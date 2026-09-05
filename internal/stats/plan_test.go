package stats

import "testing"

func TestApplyPlanValues_USDPlanWithRatio(t *testing.T) {
	report := &Report{
		Sources: map[string]Source{
			"claude_code": {
				Month:  Totals{Cost: 459.93},
				Billed: false,
				Plan:   "Max",
			},
		},
	}
	plans := map[string]PlanParams{
		"claude_code": {
			Cost:     200.00,
			Currency: "USD",
			Label:    "Max 20x",
		},
	}
	ApplyPlanValues(report, plans)

	src := report.Sources["claude_code"]
	if src.PlanValue == nil {
		t.Fatal("PlanValue should be set for claude_code")
	}
	pv := *src.PlanValue
	if pv.Cost != 200.00 {
		t.Errorf("Cost = %.2f, want 200.00", pv.Cost)
	}
	if !pv.HasRatio {
		t.Error("HasRatio should be true for USD plan")
	}
	wantRatio := 459.93 / 200.00
	if pv.Ratio != wantRatio {
		t.Errorf("Ratio = %.4f, want %.4f", pv.Ratio, wantRatio)
	}
	if pv.Warn {
		t.Error("Warn should be false when ratio > 1.0")
	}
	if pv.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", pv.Currency)
	}
	if pv.Label != "Max 20x" {
		t.Errorf("Label = %q, want Max 20x", pv.Label)
	}
}

func TestApplyPlanValues_BRLWithoutCostUSD(t *testing.T) {
	report := &Report{
		Sources: map[string]Source{
			"codex": {
				Month:  Totals{Cost: 100.00},
				Billed: false,
				Plan:   "Plus",
			},
		},
	}
	plans := map[string]PlanParams{
		"codex": {
			Cost:     110.00,
			Currency: "BRL",
			Label:    "Plus",
		},
	}
	ApplyPlanValues(report, plans)

	src := report.Sources["codex"]
	if src.PlanValue == nil {
		t.Fatal("PlanValue should be set for codex")
	}
	pv := *src.PlanValue
	if pv.HasRatio {
		t.Error("HasRatio should be false for BRL plan without cost_usd")
	}
	if pv.Warn {
		t.Error("Warn should be false when HasRatio is false")
	}
	if pv.Currency != "BRL" {
		t.Errorf("Currency = %q, want BRL", pv.Currency)
	}
}

func TestApplyPlanValues_BRLWithCostUSD(t *testing.T) {
	report := &Report{
		Sources: map[string]Source{
			"codex": {
				Month:  Totals{Cost: 200.00},
				Billed: false,
				Plan:   "Plus",
			},
		},
	}
	plans := map[string]PlanParams{
		"codex": {
			Cost:       110.00,
			Currency:   "BRL",
			CostUSD:    20.00,
			HasCostUSD: true,
			Label:      "Plus",
		},
	}
	ApplyPlanValues(report, plans)

	src := report.Sources["codex"]
	if src.PlanValue == nil {
		t.Fatal("PlanValue should be set for codex")
	}
	pv := *src.PlanValue
	if !pv.HasRatio {
		t.Error("HasRatio should be true when cost_usd is provided")
	}
	wantRatio := 200.00 / 20.00
	if pv.Ratio != wantRatio {
		t.Errorf("Ratio = %.4f, want %.4f", pv.Ratio, wantRatio)
	}
}

func TestApplyPlanValues_RatioBelowOne(t *testing.T) {
	report := &Report{
		Sources: map[string]Source{
			"claude_code": {
				Month:  Totals{Cost: 50.00},
				Billed: false,
				Plan:   "Max",
			},
		},
	}
	plans := map[string]PlanParams{
		"claude_code": {
			Cost:     200.00,
			Currency: "USD",
			Label:    "Max 20x",
		},
	}
	ApplyPlanValues(report, plans)

	src := report.Sources["claude_code"]
	pv := src.PlanValue
	if pv == nil {
		t.Fatal("PlanValue should be set")
	}
	if !pv.Warn {
		t.Error("Warn should be true when ratio < 1.0")
	}
	if pv.Ratio >= 1.0 {
		t.Errorf("Ratio = %.4f, want < 1.0", pv.Ratio)
	}
}

func TestApplyPlanValues_PartialKeepsPlanValue(t *testing.T) {
	report := &Report{
		Sources: map[string]Source{
			"claude_code": {
				Month:   Totals{Cost: 100.00},
				Billed:  false,
				Plan:    "Max",
				Partial: true,
			},
		},
	}
	plans := map[string]PlanParams{
		"claude_code": {
			Cost:     200.00,
			Currency: "USD",
			Label:    "Max 20x",
		},
	}
	ApplyPlanValues(report, plans)

	src := report.Sources["claude_code"]
	if src.PlanValue == nil {
		t.Fatal("PlanValue should be set even when partial")
	}
	if !src.PlanValue.HasRatio {
		t.Error("HasRatio should still be true for partial USD plan")
	}
}

func TestApplyPlanValues_NoPlanConfig(t *testing.T) {
	report := &Report{
		Sources: map[string]Source{
			"openrouter:main": {
				Month: Totals{Cost: 5.00},
			},
		},
	}
	// No plans map entry for openrouter:main
	ApplyPlanValues(report, map[string]PlanParams{})

	src := report.Sources["openrouter:main"]
	if src.PlanValue != nil {
		t.Error("PlanValue should be nil when no plan config is provided")
	}
}

func TestApplyPlanValues_ZeroCostPlan(t *testing.T) {
	report := &Report{
		Sources: map[string]Source{
			"claude_code": {
				Month: Totals{Cost: 100.00},
			},
		},
	}
	plans := map[string]PlanParams{
		"claude_code": {
			Cost:     0,
			Currency: "USD",
			Label:    "Free",
		},
	}
	ApplyPlanValues(report, plans)

	src := report.Sources["claude_code"]
	if src.PlanValue != nil {
		t.Error("PlanValue should be nil when plan cost is 0")
	}
}

func TestApplyPlanValues_NilReport(t *testing.T) {
	// Should not panic
	ApplyPlanValues(nil, map[string]PlanParams{
		"claude_code": {Cost: 200, Currency: "USD", Label: "Max"},
	})
}

func TestApplyPlanValues_ClaudeIDMapsToSourceName(t *testing.T) {
	// The source name in the report is "claude_code", but the plan config
	// might be keyed by provider ID "claude". The caller handles the mapping,
	// but verify ApplyPlanValues works correctly when keyed by source name.
	report := &Report{
		Sources: map[string]Source{
			"claude_code": {Month: Totals{Cost: 100.00}, Billed: false, Plan: "Max"},
		},
	}
	plans := map[string]PlanParams{
		"claude_code": {Cost: 200, Currency: "USD", Label: "Max 20x"},
	}
	ApplyPlanValues(report, plans)

	src := report.Sources["claude_code"]
	if src.PlanValue == nil {
		t.Fatal("PlanValue should be set")
	}
	if src.PlanValue.Ratio != 0.5 {
		t.Errorf("Ratio = %.4f, want 0.5", src.PlanValue.Ratio)
	}
}
