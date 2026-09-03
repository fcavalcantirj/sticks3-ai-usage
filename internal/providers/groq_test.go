package providers

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGroqProviderDefaultMode(t *testing.T) {
	client := newFixtureClient("../../testdata/fixtures")
	p := NewGroq(client, "test-key", false)

	result, outcome := p.Fetch(context.Background(), testNow())

	if result.Status != "ok" {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if result.Plan != "on_demand" {
		t.Errorf("Plan = %q, want on_demand", result.Plan)
	}
	if result.ID != "groq" {
		t.Errorf("ID = %q, want groq", result.ID)
	}
	if result.Label != "Groq" {
		t.Errorf("Label = %q, want Groq", result.Label)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("len(Rows) = %d, want 1", len(result.Rows))
	}

	r0 := result.Rows[0]
	if r0.K != "key" || r0.Label != "GROQ key" {
		t.Errorf("Row 0 = {K:%q Label:%q}, want key / GROQ key", r0.K, r0.Label)
	}
	if r0.Pct != nil {
		t.Errorf("Row 0 Pct = %v, want nil", r0.Pct)
	}
	if r0.Txt != "ok" {
		t.Errorf("Row 0 Txt = %q, want ok", r0.Txt)
	}
	if r0.Tier != "ok" {
		t.Errorf("Row 0 Tier = %q, want ok", r0.Tier)
	}

	if !outcome.CooldownUntil.IsZero() {
		t.Error("CooldownUntil should be zero for happy path")
	}
}

func TestGroqProvider401(t *testing.T) {
	dir := t.TempDir()
	routes := `{"GET api.groq.com/openai/v1/models": {"status": 401}}`
	os.WriteFile(filepath.Join(dir, "routes.json"), []byte(routes), 0644)

	client := newFixtureClient(dir)
	p := NewGroq(client, "test-key", false)

	result, _ := p.Fetch(context.Background(), testNow())

	if result.Status != "auth" {
		t.Errorf("Status = %q, want auth", result.Status)
	}
	if result.Msg != "bad key" {
		t.Errorf("Msg = %q, want bad key", result.Msg)
	}
}

func TestGroqProviderProbeMode(t *testing.T) {
	client := newFixtureClient("../../testdata/fixtures")
	p := NewGroq(client, "test-key", true)

	result, _ := p.Fetch(context.Background(), testNow())

	if result.Status != "ok" {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if result.Plan != "on_demand" {
		t.Errorf("Plan = %q, want on_demand", result.Plan)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("len(Rows) = %d, want 2", len(result.Rows))
	}

	// Row 0: rpd pct 0 txt "500k"
	r0 := result.Rows[0]
	if r0.K != "rpd" || r0.Label != "GROQ rpd" {
		t.Errorf("Row 0 = {K:%q Label:%q}, want rpd / GROQ rpd", r0.K, r0.Label)
	}
	if r0.Pct == nil || *r0.Pct != 0 {
		t.Errorf("Row 0 Pct = %v, want 0", r0.Pct)
	}
	if r0.Txt != "500k" {
		t.Errorf("Row 0 Txt = %q, want 500k", r0.Txt)
	}

	// Row 1: tpm pct 0 txt "250k"
	r1 := result.Rows[1]
	if r1.K != "tpm" || r1.Label != "GROQ tpm" {
		t.Errorf("Row 1 = {K:%q Label:%q}, want tpm / GROQ tpm", r1.K, r1.Label)
	}
	if r1.Pct == nil || *r1.Pct != 0 {
		t.Errorf("Row 1 Pct = %v, want 0", r1.Pct)
	}
	if r1.Txt != "250k" {
		t.Errorf("Row 1 Txt = %q, want 250k", r1.Txt)
	}
}

func TestParseGroqResetDuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"172ms", 172 * time.Millisecond},
		{"2m59.56s", 179560 * time.Millisecond},
		{"7.66s", 7660 * time.Millisecond},
	}
	for _, tc := range cases {
		d, err := time.ParseDuration(tc.in)
		if err != nil {
			t.Errorf("ParseDuration(%q) error: %v", tc.in, err)
		} else if d != tc.want {
			t.Errorf("ParseDuration(%q) = %v, want %v", tc.in, d, tc.want)
		}
	}
}
