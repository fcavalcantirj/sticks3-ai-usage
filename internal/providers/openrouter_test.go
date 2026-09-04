package providers

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRouterProviderHappyPath(t *testing.T) {
	client := newFixtureClient("../../testdata/fixtures")
	p := NewOpenRouter(client, "openrouter:main", "OpenRouter main", "test-key")

	result, outcome := p.Fetch(context.Background(), testNow())

	if result.Status != "ok" {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if result.Plan != "paid" {
		t.Errorf("Plan = %q, want paid", result.Plan)
	}
	if result.ID != "openrouter:main" {
		t.Errorf("ID = %q, want openrouter:main", result.ID)
	}
	if result.Label != "OpenRouter main" {
		t.Errorf("Label = %q, want OpenRouter main", result.Label)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("len(Rows) = %d, want 2", len(result.Rows))
	}

	// Row 0: bal pct 99 txt $0.07
	r0 := result.Rows[0]
	if r0.K != "bal" || r0.Label != "ORmain bal" {
		t.Errorf("Row 0 = {K:%q Label:%q}, want bal / ORmain bal", r0.K, r0.Label)
	}
	if r0.Pct == nil || *r0.Pct != 99 {
		t.Errorf("Row 0 Pct = %v, want 99", r0.Pct)
	}
	if r0.Txt != "$0.07" {
		t.Errorf("Row 0 Txt = %q, want $0.07", r0.Txt)
	}

	// Row 1: day pct nil txt $0.00
	r1 := result.Rows[1]
	if r1.K != "day" || r1.Label != "ORmain day" {
		t.Errorf("Row 1 = {K:%q Label:%q}, want day / ORmain day", r1.K, r1.Label)
	}
	if r1.Pct != nil {
		t.Errorf("Row 1 Pct = %v, want nil", r1.Pct)
	}
	if r1.Txt != "$0.00" {
		t.Errorf("Row 1 Txt = %q, want $0.00", r1.Txt)
	}

	if !outcome.CooldownUntil.IsZero() {
		t.Error("CooldownUntil should be zero for happy path")
	}
	if result.Kind != "credit" {
		t.Errorf("Kind = %q, want credit", result.Kind)
	}
	if result.Severity != "warn" {
		t.Errorf("Severity = %q, want warn (balance $0.07 < $1)", result.Severity)
	}
	if result.Msg != "low $0.07" {
		t.Errorf("Msg = %q, want 'low $0.07'", result.Msg)
	}
}

func TestOpenRouterProviderCredits403(t *testing.T) {
	dir := t.TempDir()

	// Override credits to 403; key still served from the default fixture.
	routes := `{"GET openrouter.ai/api/v1/credits": {"status": 403}}`
	os.WriteFile(filepath.Join(dir, "routes.json"), []byte(routes), 0644)

	// Copy the key fixture so the /key route resolves.
	copyFixture(t, dir, "openrouter_key.json")

	client := newFixtureClient(dir)
	p := NewOpenRouter(client, "openrouter:main", "OpenRouter main", "test-key")

	result, _ := p.Fetch(context.Background(), testNow())

	if result.Status != "ok" {
		t.Fatalf("Status = %q, want ok (key fallback)", result.Status)
	}
	if result.Plan != "paid" {
		t.Errorf("Plan = %q, want paid", result.Plan)
	}
	if result.Label != "OpenRouter main" {
		t.Errorf("Label = %q, want OpenRouter main", result.Label)
	}

	// bal row absent, day row present.
	if len(result.Rows) != 1 {
		t.Fatalf("len(Rows) = %d, want 1 (bal absent, day present)", len(result.Rows))
	}
	r0 := result.Rows[0]
	if r0.K != "day" {
		t.Errorf("Row 0 K = %q, want day", r0.K)
	}
	if r0.Txt != "$0.00" {
		t.Errorf("Row 0 Txt = %q, want $0.00", r0.Txt)
	}
}

func TestOpenRouterProviderKey401(t *testing.T) {
	dir := t.TempDir()

	// credits still served from fixture; key overridden to 401.
	routes := `{"GET openrouter.ai/api/v1/key": {"status": 401}}`
	os.WriteFile(filepath.Join(dir, "routes.json"), []byte(routes), 0644)
	copyFixture(t, dir, "openrouter_credits.json")

	client := newFixtureClient(dir)
	p := NewOpenRouter(client, "openrouter:main", "OpenRouter main", "test-key")

	result, _ := p.Fetch(context.Background(), testNow())

	if result.Status != "auth" {
		t.Errorf("Status = %q, want auth", result.Status)
	}
	if result.Msg != "bad key" {
		t.Errorf("Msg = %q, want bad key", result.Msg)
	}
}

func TestOpenRouterProviderFallbackLabel(t *testing.T) {
	client := newFixtureClient("../../testdata/fixtures")
	p := NewOpenRouter(client, "openrouter:fallback", "OpenRouter fallback", "fbk-key")

	result, _ := p.Fetch(context.Background(), testNow())

	if result.Status != "ok" {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if result.ID != "openrouter:fallback" {
		t.Errorf("ID = %q, want openrouter:fallback", result.ID)
	}
	if result.Label != "OpenRouter fallback" {
		t.Errorf("Label = %q, want OpenRouter fallback", result.Label)
	}
	if result.Plan != "paid" {
		t.Errorf("Plan = %q, want paid", result.Plan)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("len(Rows) = %d, want 2", len(result.Rows))
	}
	if result.Rows[0].Label != "ORfbk bal" {
		t.Errorf("bal Label = %q, want ORfbk bal", result.Rows[0].Label)
	}
	if result.Rows[1].Label != "ORfbk day" {
		t.Errorf("day Label = %q, want ORfbk day", result.Rows[1].Label)
	}
	if result.Kind != "credit" {
		t.Errorf("Kind = %q, want credit", result.Kind)
	}
	if result.Severity != "warn" {
		t.Errorf("Severity = %q, want warn (balance $0.07 < $1)", result.Severity)
	}
}

func TestOpenRouterProviderEmptyBalanceCrit(t *testing.T) {
	dir := t.TempDir()

	// Override credits to return a zero balance (empty → crit).
	zeroCredits := `{"data":{"total_credits":10.0,"total_usage":10.0}}`
	os.WriteFile(filepath.Join(dir, "zero_credits.json"), []byte(zeroCredits), 0644)
	routes := `{"GET openrouter.ai/api/v1/credits": {"file": "zero_credits.json"}}`
	os.WriteFile(filepath.Join(dir, "routes.json"), []byte(routes), 0644)
	copyFixture(t, dir, "openrouter_key.json")

	client := newFixtureClient(dir)
	p := NewOpenRouter(client, "openrouter:main", "OpenRouter main", "test-key")

	result, _ := p.Fetch(context.Background(), testNow())

	if result.Status != "ok" {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if result.Kind != "credit" {
		t.Errorf("Kind = %q, want credit", result.Kind)
	}
	if result.Severity != "crit" {
		t.Errorf("Severity = %q, want crit (balance $0.00)", result.Severity)
	}
	if result.Msg != "EMPTY - free blocked" {
		t.Errorf("Msg = %q, want 'EMPTY - free blocked'", result.Msg)
	}
}

// copyFixture copies a named fixture file from testdata/fixtures into dir.
func copyFixture(t *testing.T, dir, name string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("../../testdata/fixtures", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
}
