package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"usaged/internal/creds"
	"usaged/internal/httpx"
)

// writeCodexAuthFixture writes an auth.json to a temp dir and returns the path.
func writeCodexAuthFixture(t *testing.T, auth map[string]any) string {
	t.Helper()
	data, err := json.Marshal(auth)
	if err != nil {
		t.Fatalf("marshal auth: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}
	return path
}

func TestCodexProviderHappyPath(t *testing.T) {
	authPath := "../../testdata/fixtures/codex_auth.json"
	client := newFixtureClient("../../testdata/fixtures")
	p := NewCodex(client, authPath, testLoc)

	result, outcome := p.Fetch(context.Background(), testNow())

	if result.Status != "ok" {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if result.Plan != "plus" {
		t.Errorf("Plan = %q, want plus", result.Plan)
	}
	if result.Kind != "plan" {
		t.Errorf("Kind = %q, want plan", result.Kind)
	}
	if result.Severity != "crit" {
		t.Errorf("Severity = %q, want crit (GPT 5h pct=100 >= 95)", result.Severity)
	}
	if result.ID != "codex" {
		t.Errorf("ID = %q, want codex", result.ID)
	}
	if len(result.Rows) != 3 {
		t.Fatalf("len(Rows) = %d, want 3", len(result.Rows))
	}

	// Row 0: primary 5h / pct 100 / crit
	r0 := result.Rows[0]
	if r0.K != "5h" || r0.Label != "GPT 5h" || *r0.Pct != 100 || r0.Tier != "crit" {
		t.Errorf("Row 0 = {K:%q Label:%q Pct:%v Tier:%q}", r0.K, r0.Label, *r0.Pct, r0.Tier)
	}
	if r0.ResetAt == nil {
		t.Error("Row 0 ResetAt should not be nil")
	}

	// Row 1: secondary 7d / pct 31 / ok
	r1 := result.Rows[1]
	if r1.K != "7d" || r1.Label != "GPT 7d" || *r1.Pct != 31 || r1.Tier != "ok" {
		t.Errorf("Row 1 = {K:%q Label:%q Pct:%v Tier:%q}", r1.K, r1.Label, *r1.Pct, r1.Tier)
	}

	// Row 2: balance / pct null / 178 cr (credits count, not dollars)
	r2 := result.Rows[2]
	if r2.K != "bal" || r2.Label != "GPT cr" || r2.Pct != nil || r2.Txt != "178 cr" {
		t.Errorf("Row 2 = {K:%q Label:%q Pct:%v Txt:%q}", r2.K, r2.Label, r2.Pct, r2.Txt)
	}

	if !outcome.CooldownUntil.IsZero() {
		t.Error("CooldownUntil should be zero for happy path")
	}
}

func TestCodexProviderExpiredJWTNoHTTP(t *testing.T) {
	claims := map[string]any{
		"exp": float64(testNow().Add(-time.Second).Unix()),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_plan_type": "plus",
		},
	}
	token := creds.MakeJWT(claims)
	path := writeCodexAuthFixture(t, map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"access_token": token,
			"account_id":   "acct-id",
		},
	})

	ct := &spyTransport{}
	client := &httpx.Client{HTTP: &http.Client{Transport: ct}}
	p := NewCodex(client, path, testLoc)

	result, outcome := p.Fetch(context.Background(), testNow())

	if ct.called {
		t.Error("transport should NOT be called for expired JWT")
	}
	if result.Status != "auth" {
		t.Errorf("Status = %q, want auth", result.Status)
	}
	if result.Msg != "run codex" {
		t.Errorf("Msg = %q, want run codex", result.Msg)
	}
	if !outcome.CooldownUntil.IsZero() {
		t.Error("CooldownUntil should be zero")
	}
}

func TestCodexProviderNotLoggedInNoHTTP(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "nonexistent.json")
	ct := &spyTransport{}
	client := &httpx.Client{HTTP: &http.Client{Transport: ct}}
	p := NewCodex(client, authPath, testLoc)

	result, outcome := p.Fetch(context.Background(), testNow())

	if ct.called {
		t.Error("transport should NOT be called for not-logged-in")
	}
	if result.Status != "auth" {
		t.Errorf("Status = %q, want auth", result.Status)
	}
	if result.Msg != "run codex" {
		t.Errorf("Msg = %q, want run codex", result.Msg)
	}
	if result.Plan != "" {
		t.Errorf("Plan = %q, want empty", result.Plan)
	}
	if !outcome.CooldownUntil.IsZero() {
		t.Error("CooldownUntil should be zero")
	}
}

func TestCodexProviderSwappedWindows(t *testing.T) {
	dir := t.TempDir()
	// primary (604800=7d) and secondary (18000=5h) swapped relative to the real fixture
	swappedBody := `{"rate_limit":{"primary_window":{"used_percent":31,"limit_window_seconds":604800,"reset_at":1788969665},"secondary_window":{"used_percent":100,"limit_window_seconds":18000,"reset_at":1788401621}},"credits":{"has_credits":true,"unlimited":false,"balance":"178.1026300000"},"plan_type":"plus"}`
	os.WriteFile(filepath.Join(dir, "swapped.json"), []byte(swappedBody), 0644)
	routes := `{"GET chatgpt.com/backend-api/wham/usage": {"file": "swapped.json"}}`
	os.WriteFile(filepath.Join(dir, "routes.json"), []byte(routes), 0644)

	authPath := "../../testdata/fixtures/codex_auth.json"
	client := newFixtureClient(dir)
	p := NewCodex(client, authPath, testLoc)

	result, _ := p.Fetch(context.Background(), testNow())

	if result.Status != "ok" {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if result.Kind != "plan" {
		t.Errorf("Kind = %q, want plan", result.Kind)
	}
	if result.Severity != "crit" {
		t.Errorf("Severity = %q, want crit (GPT 5h pct=100 >= 95)", result.Severity)
	}
	if len(result.Rows) != 3 {
		t.Fatalf("len(Rows) = %d, want 3", len(result.Rows))
	}

	// Even though primary is now 7d and secondary is 5h, output sorts 5h first
	r0 := result.Rows[0]
	if r0.K != "5h" || r0.Label != "GPT 5h" || *r0.Pct != 100 {
		t.Errorf("Row 0 = {K:%q Label:%q Pct:%v}, want 5h GPT 5h 100", r0.K, r0.Label, *r0.Pct)
	}

	r1 := result.Rows[1]
	if r1.K != "7d" || r1.Label != "GPT 7d" || *r1.Pct != 31 {
		t.Errorf("Row 1 = {K:%q Label:%q Pct:%v}, want 7d GPT 7d 31", r1.K, r1.Label, *r1.Pct)
	}
}

func TestCodexProvider401(t *testing.T) {
	dir := t.TempDir()
	routes := `{"GET chatgpt.com/backend-api/wham/usage": {"status": 401}}`
	os.WriteFile(filepath.Join(dir, "routes.json"), []byte(routes), 0644)

	authPath := "../../testdata/fixtures/codex_auth.json"
	client := newFixtureClient(dir)
	p := NewCodex(client, authPath, testLoc)

	result, outcome := p.Fetch(context.Background(), testNow())

	if result.Status != "auth" {
		t.Errorf("Status = %q, want auth", result.Status)
	}
	if result.Severity != "crit" {
		t.Errorf("Severity = %q, want crit (auth status)", result.Severity)
	}
	if result.Kind != "plan" {
		t.Errorf("Kind = %q, want plan", result.Kind)
	}
	if result.Msg != "run codex" {
		t.Errorf("Msg = %q, want run codex", result.Msg)
	}
	if result.Plan != "plus" {
		t.Errorf("Plan = %q, want plus", result.Plan)
	}
	if !outcome.CooldownUntil.IsZero() {
		t.Error("CooldownUntil should be zero")
	}
}
