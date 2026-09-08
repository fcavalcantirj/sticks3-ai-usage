package providers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"usaged/internal/format"
)

func TestOpenCodeProviderHappyPath(t *testing.T) {
	client := newFixtureClient("../../testdata/fixtures")
	p := NewOpenCode(client, "test-key", testLoc, format.DefaultAlerts())

	result, outcome := p.Fetch(context.Background(), testNow())

	if result.Status != "ok" {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if result.ID != "opencode:go" {
		t.Errorf("ID = %q, want opencode:go", result.ID)
	}
	if result.Label != "OpenCode Go" {
		t.Errorf("Label = %q, want OpenCode Go", result.Label)
	}
	if result.Kind != "plan" {
		t.Errorf("Kind = %q, want plan", result.Kind)
	}
	if result.Plan != "plan" {
		t.Errorf("Plan = %q, want plan", result.Plan)
	}
	if len(result.Rows) != 3 {
		t.Fatalf("len(Rows) = %d, want 3", len(result.Rows))
	}
	if !outcome.CooldownUntil.IsZero() {
		t.Error("CooldownUntil should be zero for happy path")
	}

	// Row 0: rolling → "5h", pct 5
	r0 := result.Rows[0]
	if r0.K != "5h" || r0.Label != "OCgo 5h" {
		t.Errorf("Row 0 = {K:%q Label:%q}, want 5h / OCgo 5h", r0.K, r0.Label)
	}
	if r0.Pct == nil || *r0.Pct != 5 {
		t.Errorf("Row 0 Pct = %v, want 5", r0.Pct)
	}
	if r0.Tier != "ok" {
		t.Errorf("Row 0 Tier = %q, want ok", r0.Tier)
	}
	// 2026-09-08T04:23:36.686Z in UTC-3 is Tue
	if r0.Txt != "Tue" {
		t.Errorf("Row 0 Txt = %q, want Tue", r0.Txt)
	}
	rt0, _ := time.Parse(time.RFC3339, "2026-09-08T04:23:36.686Z")
	if r0.ResetAt == nil || *r0.ResetAt != rt0.Unix() {
		t.Errorf("Row 0 ResetAt = %v, want %d", r0.ResetAt, rt0.Unix())
	}

	// Row 1: weekly → "7d", pct 2
	r1 := result.Rows[1]
	if r1.K != "7d" || r1.Label != "OCgo 7d" {
		t.Errorf("Row 1 = {K:%q Label:%q}, want 7d / OCgo 7d", r1.K, r1.Label)
	}
	if r1.Pct == nil || *r1.Pct != 2 {
		t.Errorf("Row 1 Pct = %v, want 2", r1.Pct)
	}
	if r1.Tier != "ok" {
		t.Errorf("Row 1 Tier = %q, want ok", r1.Tier)
	}
	// 2026-09-14T00:00:00.686Z in UTC-3 = Sep 13 (Sun) 21:00
	if r1.Txt != "Sun" {
		t.Errorf("Row 1 Txt = %q, want Sun", r1.Txt)
	}
	rt1, _ := time.Parse(time.RFC3339, "2026-09-14T00:00:00.686Z")
	if r1.ResetAt == nil || *r1.ResetAt != rt1.Unix() {
		t.Errorf("Row 1 ResetAt = %v, want %d", r1.ResetAt, rt1.Unix())
	}

	// Row 2: monthly → "30d", pct 1
	r2 := result.Rows[2]
	if r2.K != "30d" || r2.Label != "OCgo 30d" {
		t.Errorf("Row 2 = {K:%q Label:%q}, want 30d / OCgo 30d", r2.K, r2.Label)
	}
	if r2.Pct == nil || *r2.Pct != 1 {
		t.Errorf("Row 2 Pct = %v, want 1", r2.Pct)
	}
	if r2.Tier != "ok" {
		t.Errorf("Row 2 Tier = %q, want ok", r2.Tier)
	}
	// 2026-09-07T23:14:35.686Z in UTC-3 = Oct 7 (Wed) 20:14
	if r2.Txt != "Wed" {
		t.Errorf("Row 2 Txt = %q, want Wed", r2.Txt)
	}
	rt2, _ := time.Parse(time.RFC3339, "2026-10-07T23:14:35.686Z")
	if r2.ResetAt == nil || *r2.ResetAt != rt2.Unix() {
		t.Errorf("Row 2 ResetAt = %v, want %d", r2.ResetAt, rt2.Unix())
	}
}

func TestOpenCodeProviderBadStatus(t *testing.T) {
	// A window with status "error" must get tier "off" and nil Pct.
	inline := `{"usage":{"rolling":{"status":"error","percent":50,"resetsAt":"2026-09-08T04:23:36.686Z"},"weekly":{"status":"ok","percent":2,"resetsAt":"2026-09-14T00:00:00.686Z"},"monthly":{"status":"ok","percent":1,"resetsAt":"2026-10-07T23:14:35.686Z"}}}`
	routes, _ := json.Marshal(map[string]map[string]string{
		"GET opencode.ai/zen/go/v1/usage": {"inline": inline},
	})
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "routes.json"), routes, 0644)

	client := newFixtureClient(dir)
	p := NewOpenCode(client, "test-key", testLoc, format.DefaultAlerts())

	result, _ := p.Fetch(context.Background(), testNow())

	if result.Status != "ok" {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if len(result.Rows) != 3 {
		t.Fatalf("len(Rows) = %d, want 3", len(result.Rows))
	}

	// Row 0 (rolling): status "error" → tier "off", Pct nil
	r0 := result.Rows[0]
	if r0.Pct != nil {
		t.Errorf("Bad-status row Pct = %v, want nil", r0.Pct)
	}
	if r0.Tier != "off" {
		t.Errorf("Bad-status row Tier = %q, want off", r0.Tier)
	}
}

func TestOpenCodeProviderMalformedResetsAt(t *testing.T) {
	// A valid status but malformed resetsAt must leave ResetAt nil, never zero.
	inline := `{"usage":{"rolling":{"status":"ok","percent":5,"resetsAt":"not-a-date"},"weekly":{"status":"ok","percent":2,"resetsAt":"2026-09-14T00:00:00.686Z"},"monthly":{"status":"ok","percent":1,"resetsAt":"2026-10-07T23:14:35.686Z"}}}`
	routes, _ := json.Marshal(map[string]map[string]string{
		"GET opencode.ai/zen/go/v1/usage": {"inline": inline},
	})
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "routes.json"), routes, 0644)

	client := newFixtureClient(dir)
	p := NewOpenCode(client, "test-key", testLoc, format.DefaultAlerts())

	result, _ := p.Fetch(context.Background(), testNow())

	if result.Status != "ok" {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	// Row 0 (rolling): pct should still be set, ResetAt must be nil
	r0 := result.Rows[0]
	if r0.Pct == nil || *r0.Pct != 5 {
		t.Errorf("Malformed-reset row Pct = %v, want 5", r0.Pct)
	}
	if r0.ResetAt != nil {
		t.Errorf("Malformed-reset row ResetAt = %v, want nil (not zero)", r0.ResetAt)
	}
	if r0.Txt != "--" {
		t.Errorf("Malformed-reset row Txt = %q, want --", r0.Txt)
	}
}
