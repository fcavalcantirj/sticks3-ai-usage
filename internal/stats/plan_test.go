package stats

import (
	"math"
	"strings"
	"testing"
)

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

// TestComputePlanCost_BRLPlusUSD verifies the main use case: two BRL plans and
// one USD plan, all plan pairs agree on 5.5 BRL/USD. The plan sum is the
// stated prices (1100+110+55 = 1265 BRL), and the equivalent in USD is
// 200+20+10 = 230.
func TestComputePlanCost_BRLPlusUSD(t *testing.T) {
	plans := map[string]PlanParams{
		"claude_code": {Cost: 1100, Currency: "BRL", CostUSD: 200, HasCostUSD: true, Label: "Max 20x"},
		"codex":       {Cost: 110, Currency: "BRL", CostUSD: 20, HasCostUSD: true, Label: "Plus"},
		"opencode:go": {Cost: 10, Currency: "USD", Label: "Pro"},
	}
	report := &Report{
		Sources: map[string]Source{
			"claude_code": {Month: Totals{Cost: 459.93}, Partial: true, UnpricedModels: []string{"<unknown>", "fugu"}},
			"codex":       {Month: Totals{Cost: 200}, Partial: true, UnpricedModels: []string{"codex-auto-review"}},
		},
	}

	pc := ComputePlanCost(plans, report)
	if pc == nil {
		t.Fatal("PlanCost should not be nil")
	}

	if pc.PrimaryCurrency != "BRL" {
		t.Errorf("PrimaryCurrency = %q, want BRL", pc.PrimaryCurrency)
	}
	wantPrimary := float64(1100) + 110 + (10 * 5.5)
	if pc.PrimaryTotal != wantPrimary {
		t.Errorf("PrimaryTotal = %.2f, want %.2f", pc.PrimaryTotal, wantPrimary)
	}
	if pc.SecondaryCurrency != "USD" {
		t.Errorf("SecondaryCurrency = %q, want USD", pc.SecondaryCurrency)
	}
	wantSecondary := float64(200 + 20 + 10)
	if pc.SecondaryTotal != wantSecondary {
		t.Errorf("SecondaryTotal = %.2f, want %.2f", pc.SecondaryTotal, wantSecondary)
	}
	if pc.ExchangeRate != 5.5 {
		t.Errorf("ExchangeRate = %.4f, want 5.5", pc.ExchangeRate)
	}
	if pc.RateDisagrees {
		t.Error("RateDisagrees should be false when rates agree")
	}

	// API-equivalent disclosure
	if math.Abs(pc.APICostMonth-659.93) > 0.01 {
		t.Errorf("APICostMonth = %.2f, want 659.93", pc.APICostMonth)
	}
	if !pc.APIPartial {
		t.Error("APIPartial should be true")
	}
	wantModels := []string{"<unknown>", "fugu", "codex-auto-review"}
	if len(pc.APIUnpricedModels) != len(wantModels) {
		t.Errorf("APIUnpricedModels = %v, want %v", pc.APIUnpricedModels, wantModels)
	}
}

// TestComputePlanCost_USDOnly verifies that a USD-only config shows USD as the
// primary currency with no secondary.
func TestComputePlanCost_USDOnly(t *testing.T) {
	plans := map[string]PlanParams{
		"claude_code": {Cost: 200, Currency: "USD", Label: "Max 20x"},
		"opencode:go": {Cost: 10, Currency: "USD", Label: "Pro"},
	}
	report := &Report{
		Sources: map[string]Source{
			"claude_code": {Month: Totals{Cost: 459.93}},
		},
	}

	pc := ComputePlanCost(plans, report)
	if pc == nil {
		t.Fatal("PlanCost should not be nil")
	}

	if pc.PrimaryCurrency != "USD" {
		t.Errorf("PrimaryCurrency = %q, want USD", pc.PrimaryCurrency)
	}
	wantPrimary := float64(200 + 10)
	if pc.PrimaryTotal != wantPrimary {
		t.Errorf("PrimaryTotal = %.2f, want %.2f", pc.PrimaryTotal, wantPrimary)
	}
	if pc.SecondaryCurrency != "" {
		t.Errorf("SecondaryCurrency = %q, want empty", pc.SecondaryCurrency)
	}
	if pc.SecondaryTotal != 0 {
		t.Errorf("SecondaryTotal = %.2f, want 0", pc.SecondaryTotal)
	}
	if pc.ExchangeRate != 0 {
		t.Errorf("ExchangeRate = %.4f, want 0", pc.ExchangeRate)
	}
}

// TestComputePlanCost_RateDisagrees verifies that when plan-pair rates disagree
// beyond tolerance, conversion is suppressed and the offending plans are omitted.
func TestComputePlanCost_RateDisagrees(t *testing.T) {
	plans := map[string]PlanParams{
		"claude_code": {Cost: 1100, Currency: "BRL", CostUSD: 200, HasCostUSD: true, Label: "Max 20x"}, // 5.5
		"codex":       {Cost: 110, Currency: "BRL", CostUSD: 18, HasCostUSD: true, Label: "Plus"},      // 6.11 — disagrees
		"opencode:go": {Cost: 10, Currency: "USD", Label: "Pro"},
	}
	report := &Report{
		Sources: map[string]Source{
			"claude_code": {Month: Totals{Cost: 459.93}},
			"codex":       {Month: Totals{Cost: 200}},
		},
	}

	pc := ComputePlanCost(plans, report)
	if pc == nil {
		t.Fatal("PlanCost should not be nil")
	}

	if !pc.RateDisagrees {
		t.Error("RateDisagrees should be true when rates differ")
	}
	if pc.SecondaryCurrency != "" {
		t.Errorf("SecondaryCurrency = %q, want empty (conversion suppressed)", pc.SecondaryCurrency)
	}
	if pc.SecondaryTotal != 0 {
		t.Errorf("SecondaryTotal = %.2f, want 0 (conversion suppressed)", pc.SecondaryTotal)
	}
	// Primary total includes all BRL plans; the USD plan is omitted from
	// conversion but BRL plans are always included.
	if pc.PrimaryTotal != float64(1210) {
		t.Errorf("PrimaryTotal = %.2f, want 1210", pc.PrimaryTotal)
	}
	if len(pc.OmittedPlans) != 1 {
		t.Errorf("OmittedPlans = %v, want exactly 1 element", pc.OmittedPlans)
	}
	omitted := pc.OmittedPlans[0]
	if omitted != "Plus" && omitted != "Max 20x" {
		t.Errorf("OmittedPlans[0] = %q, want either Plus or Max 20x (non-deterministic baseline)", omitted)
	}
}

// TestComputePlanCost_NoCostUSD verifies that when no plan pairs carry cost_usd,
// conversion is suppressed and secondary-currency plans are omitted from the
// primary total.
func TestComputePlanCost_NoCostUSD(t *testing.T) {
	plans := map[string]PlanParams{
		"claude_code": {Cost: 1100, Currency: "BRL", Label: "Max 20x"},
		"codex":       {Cost: 110, Currency: "BRL", Label: "Plus"},
		"opencode:go": {Cost: 10, Currency: "USD", Label: "Pro"},
	}
	report := &Report{
		Sources: map[string]Source{
			"claude_code": {Month: Totals{Cost: 459.93}},
		},
	}

	pc := ComputePlanCost(plans, report)
	if pc == nil {
		t.Fatal("PlanCost should not be nil")
	}

	if pc.PrimaryCurrency != "BRL" {
		t.Errorf("PrimaryCurrency = %q, want BRL", pc.PrimaryCurrency)
	}
	if pc.PrimaryTotal != float64(1210) {
		t.Errorf("PrimaryTotal = %.2f, want 1210 (BRL plans only, USD omitted)", pc.PrimaryTotal)
	}
	if pc.SecondaryCurrency != "" {
		t.Errorf("SecondaryCurrency = %q, want empty", pc.SecondaryCurrency)
	}
	if !pc.RateDisagrees {
		t.Error("RateDisagrees should be true when no rate can be derived")
	}
	if len(pc.OmittedPlans) != 1 || pc.OmittedPlans[0] != "Pro" {
		t.Errorf("OmittedPlans = %v, want [Pro]", pc.OmittedPlans)
	}
}

// TestComputePlanCost_NilReport verifies the function does not panic when the
// report is nil (it still computes the plan sum, but API costs are zero).
func TestComputePlanCost_NilReport(t *testing.T) {
	plans := map[string]PlanParams{
		"claude_code": {Cost: 200, Currency: "USD", Label: "Max 20x"},
	}

	pc := ComputePlanCost(plans, nil)
	if pc == nil {
		t.Fatal("PlanCost should not be nil")
	}
	if pc.APICostToday != 0 {
		t.Errorf("APICostToday = %.2f, want 0", pc.APICostToday)
	}
	if pc.APICostMonth != 0 {
		t.Errorf("APICostMonth = %.2f, want 0", pc.APICostMonth)
	}
	if pc.APIPartial {
		t.Error("APIPartial should be false with nil report")
	}
}

// TestComputePlanCost_EmptyPlans verifies that empty plans returns nil.
func TestComputePlanCost_EmptyPlans(t *testing.T) {
	report := &Report{
		Sources: map[string]Source{
			"claude_code": {Month: Totals{Cost: 100}},
		},
	}
	pc := ComputePlanCost(map[string]PlanParams{}, report)
	if pc != nil {
		t.Errorf("PlanCost should be nil for empty plans, got %+v", pc)
	}
}

// TestComputePlanCost_PartialSurfacesUnpriced verifies that the partial flag
// and unpriced models from scanned sources are surfaced on the PlanCost.
func TestComputePlanCost_PartialSurfacesUnpriced(t *testing.T) {
	plans := map[string]PlanParams{
		"claude_code": {Cost: 200, Currency: "USD", Label: "Max 20x"},
	}
	report := &Report{
		Sources: map[string]Source{
			"claude_code": {
				Month:          Totals{Cost: 400},
				Partial:        true,
				UnpricedModels: []string{"<unknown>", "fugu"},
			},
		},
	}

	pc := ComputePlanCost(plans, report)
	if pc == nil {
		t.Fatal("PlanCost should not be nil")
	}
	if !pc.APIPartial {
		t.Error("APIPartial should be true")
	}
	wantModels := []string{"<unknown>", "fugu"}
	if len(pc.APIUnpricedModels) != len(wantModels) {
		t.Errorf("APIUnpricedModels = %v, want %v", pc.APIUnpricedModels, wantModels)
	}
}

// TestComputePlanCost_TodayAndMonthSame verifies that the plan sum is the same
// for both today and month (subscription is monthly, not daily accruing). The
// API costs differ, but the plan total is fixed.
func TestComputePlanCost_TodayAndMonthSame(t *testing.T) {
	plans := map[string]PlanParams{
		"claude_code": {Cost: 200, Currency: "USD", Label: "Max 20x"},
	}
	report := &Report{
		Sources: map[string]Source{
			"claude_code": {
				Today: Totals{Cost: 50},
				Month: Totals{Cost: 459.93},
			},
		},
	}

	pc := ComputePlanCost(plans, report)
	if pc == nil {
		t.Fatal("PlanCost should not be nil")
	}
	// The plan sum appears as PrimaryTotal regardless of today/month.
	if pc.PrimaryTotal != float64(200) {
		t.Errorf("PrimaryTotal = %.2f, want 200", pc.PrimaryTotal)
	}
	if pc.APICostToday != 50 {
		t.Errorf("APICostToday = %.2f, want 50", pc.APICostToday)
	}
	if pc.APICostMonth != 459.93 {
		t.Errorf("APICostMonth = %.2f, want 459.93", pc.APICostMonth)
	}
}

// TestComputePlanCost_WindowedUnpriced pins the per-window disclosure.
//
// Both cost tiles used to be handed the same lifetime union, so "Cost today"
// named models that contributed nothing today. Measured on the live daemon
// 2026-09-11: fugu, 0 input tokens today and 1,142,586 that month, printed
// under the today tile.
func TestComputePlanCost_WindowedUnpriced(t *testing.T) {
	plans := map[string]PlanParams{
		"codex": {Cost: 110, Currency: "BRL", CostUSD: 20, HasCostUSD: true, Label: "Plus"},
	}
	report := &Report{
		Sources: map[string]Source{
			"codex": {
				Today:          Totals{Cost: 5},
				Month:          Totals{Cost: 100},
				Partial:        true,
				UnpricedModels: []string{"codex-auto-review", "fugu"},
				// fugu ran this month but not today.
				UnpricedToday: []string{"codex-auto-review"},
				UnpricedMonth: []string{"codex-auto-review", "fugu"},
			},
		},
	}

	pc := ComputePlanCost(plans, report)
	if pc == nil {
		t.Fatal("PlanCost should not be nil")
	}
	if got, want := strings.Join(pc.APIUnpricedToday, ","), "codex-auto-review"; got != want {
		t.Errorf("APIUnpricedToday = %q, want %q", got, want)
	}
	if got, want := strings.Join(pc.APIUnpricedMonth, ","), "codex-auto-review,fugu"; got != want {
		t.Errorf("APIUnpricedMonth = %q, want %q", got, want)
	}
	if !pc.APIPartialToday || !pc.APIPartialMonth {
		t.Errorf("partial flags = %v/%v, want both true", pc.APIPartialToday, pc.APIPartialMonth)
	}

	// A source whose windows are fully priced must not claim to be partial in
	// them, even when its lifetime total is partial.
	report.Sources["codex"] = Source{
		Today: Totals{Cost: 5}, Month: Totals{Cost: 100},
		Partial: true, UnpricedModels: []string{"an-old-model"},
	}
	pc = ComputePlanCost(plans, report)
	if pc.APIPartialToday || pc.APIPartialMonth {
		t.Errorf("windows flagged partial with no windowed gaps: %v/%v", pc.APIPartialToday, pc.APIPartialMonth)
	}
	if !pc.APIPartial {
		t.Error("the lifetime flag should still be true")
	}
}
