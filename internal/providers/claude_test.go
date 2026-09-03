package providers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"usaged/internal/creds"
	"usaged/internal/httpx"
)

var testLoc = time.FixedZone("America/Sao_Paulo", -3*3600)

func testNow() time.Time {
	return time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC) // 2026-09-02T21:00:00-03:00
}

// newClaudeRunner returns a FakeRunner with valid (or expired) Claude creds.
func newClaudeRunner(token string, expired bool) creds.FakeRunner {
	expiresAt := time.Now().Add(time.Hour).UnixMilli()
	if expired {
		expiresAt = time.Now().Add(-time.Hour).UnixMilli()
	}
	kcJSON := fmt.Sprintf(
		`{"claudeAiOauth":{"accessToken":"%s","refreshToken":"rt","expiresAt":%d,"refreshTokenExpiresAt":%d,"scopes":["s"],"subscriptionType":"max","rateLimitTier":"default_claude_max_20x"}}`,
		token, expiresAt, expiresAt,
	)
	return creds.FakeRunner{
		Responses: map[string]creds.FakeResponse{
			"security find-generic-password -s Claude Code-credentials -a testuser -w": {
				Stdout: []byte(kcJSON),
			},
			"claude --version": {
				Stdout: []byte("2.1.259 (Claude Code)"),
			},
		},
	}
}

// newFixtureClient creates an httpx.Client backed by the fixture transport.
func newFixtureClient(dir string) *httpx.Client {
	return &httpx.Client{
		HTTP: &http.Client{Transport: httpx.NewFixtureTransport(dir)},
	}
}

// spyTransport records whether RoundTrip was called and returns an error.
type spyTransport struct {
	called bool
}

func (s *spyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.called = true
	return nil, fmt.Errorf("transport should not be called")
}

// --- Tests ---

func TestClaudeProviderHappyPath(t *testing.T) {
	runner := newClaudeRunner("sk-ant-oat01-TEST-TOKEN-12345", false)
	client := newFixtureClient("../../testdata/fixtures")
	p := NewClaude(client, runner, "testuser", testLoc)

	result, outcome := p.Fetch(context.Background(), testNow())

	if result.Status != "ok" {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if result.Msg != "" {
		t.Errorf("Msg = %q, want empty", result.Msg)
	}
	if result.Plan != "max_20x" {
		t.Errorf("Plan = %q, want max_20x", result.Plan)
	}
	if len(result.Rows) != 3 {
		t.Fatalf("len(Rows) = %d, want 3", len(result.Rows))
	}

	// Row 0: session → 5h / CLAUDE 5h / 19 / ok / 02:09
	r0 := result.Rows[0]
	if r0.K != "5h" || r0.Label != "CLAUDE 5h" || *r0.Pct != 19 || r0.Tier != "ok" || r0.Txt != "02:09" {
		t.Errorf("Row 0 = {K:%q Label:%q Pct:%v Tier:%q Txt:%q}", r0.K, r0.Label, *r0.Pct, r0.Tier, r0.Txt)
	}
	if r0.ResetAt == nil {
		t.Error("Row 0 ResetAt should not be nil")
	}

	// Row 1: weekly_all → 7d / CLAUDE 7d / 30 / ok / Mon
	r1 := result.Rows[1]
	if r1.K != "7d" || r1.Label != "CLAUDE 7d" || *r1.Pct != 30 || r1.Tier != "ok" || r1.Txt != "Mon" {
		t.Errorf("Row 1 = {K:%q Label:%q Pct:%v Tier:%q Txt:%q}", r1.K, r1.Label, *r1.Pct, r1.Tier, r1.Txt)
	}

	// Row 2: weekly_scoped → 7d:Fable / FABLE 7d / 20 / ok / Mon
	r2 := result.Rows[2]
	if r2.K != "7d:Fable" || r2.Label != "FABLE 7d" || *r2.Pct != 20 || r2.Tier != "ok" || r2.Txt != "Mon" {
		t.Errorf("Row 2 = {K:%q Label:%q Pct:%v Tier:%q Txt:%q}", r2.K, r2.Label, *r2.Pct, r2.Tier, r2.Txt)
	}

	if !outcome.CooldownUntil.IsZero() {
		t.Error("CooldownUntil should be zero for happy path")
	}
	if result.ID == "" {
		t.Error("Provider ID should be set")
	}
}

func TestClaudeProviderExpiredCredsNoHTTP(t *testing.T) {
	runner := newClaudeRunner("sk-ant-oat01-TEST-TOKEN-12345", true) // expired
	ct := &spyTransport{}
	client := &httpx.Client{HTTP: &http.Client{Transport: ct}}
	p := NewClaude(client, runner, "testuser", testLoc)

	result, outcome := p.Fetch(context.Background(), testNow())

	if ct.called {
		t.Error("transport should NOT be called for expired creds")
	}
	if result.Status != "auth" {
		t.Errorf("Status = %q, want auth", result.Status)
	}
	if result.Msg != "run claude" {
		t.Errorf("Msg = %q, want run claude", result.Msg)
	}
	if result.Plan != "max_20x" {
		t.Errorf("Plan = %q, want max_20x (from creds)", result.Plan)
	}
	if !outcome.CooldownUntil.IsZero() {
		t.Error("CooldownUntil should be zero")
	}
}

func TestClaudeProviderNotLoggedInNoHTTP(t *testing.T) {
	runner := creds.FakeRunner{
		Responses: map[string]creds.FakeResponse{
			"security find-generic-password -s Claude Code-credentials -a testuser -w": {
				Stderr: []byte("SecKeychainSearchCopyNext: could not be found"),
				Err:    errors.New("exit status 44"),
			},
		},
	}
	ct := &spyTransport{}
	client := &httpx.Client{HTTP: &http.Client{Transport: ct}}
	p := NewClaude(client, runner, "testuser", testLoc)

	result, outcome := p.Fetch(context.Background(), testNow())

	if ct.called {
		t.Error("transport should NOT be called for not-logged-in")
	}
	if result.Status != "auth" {
		t.Errorf("Status = %q, want auth", result.Status)
	}
	if result.Msg != "run claude" {
		t.Errorf("Msg = %q, want run claude", result.Msg)
	}
	if result.Plan != "" {
		t.Errorf("Plan = %q, want empty", result.Plan)
	}
	if !outcome.CooldownUntil.IsZero() {
		t.Error("CooldownUntil should be zero")
	}
}

func TestClaudeProvider401(t *testing.T) {
	dir := t.TempDir()
	routes := `{"GET api.anthropic.com/api/oauth/usage": {"status": 401}}`
	os.WriteFile(filepath.Join(dir, "routes.json"), []byte(routes), 0644)

	runner := newClaudeRunner("sk-ant-oat01-TEST-TOKEN-12345", false)
	client := newFixtureClient(dir)
	p := NewClaude(client, runner, "testuser", testLoc)

	result, outcome := p.Fetch(context.Background(), testNow())

	if result.Status != "auth" {
		t.Errorf("Status = %q, want auth", result.Status)
	}
	if result.Msg != "run claude" {
		t.Errorf("Msg = %q, want run claude", result.Msg)
	}
	if result.Plan != "max_20x" {
		t.Errorf("Plan = %q, want max_20x", result.Plan)
	}
	if !outcome.CooldownUntil.IsZero() {
		t.Error("CooldownUntil should be zero")
	}
}

func TestClaudeProvider429WithCooldown(t *testing.T) {
	dir := t.TempDir()
	routes := `{"GET api.anthropic.com/api/oauth/usage": {"status": 429, "headers": {"Retry-After": "120"}}}`
	os.WriteFile(filepath.Join(dir, "routes.json"), []byte(routes), 0644)

	runner := newClaudeRunner("sk-ant-oat01-TEST-TOKEN-12345", false)
	client := newFixtureClient(dir)
	p := NewClaude(client, runner, "testuser", testLoc)

	now := testNow()
	result, outcome := p.Fetch(context.Background(), now)

	if result.Status != "error" {
		t.Errorf("Status = %q, want error", result.Status)
	}
	wantMsg := "429 until 21:02" // now+120s in UTC-3 = 00:02:00Z → 21:02-03:00
	if result.Msg != wantMsg {
		t.Errorf("Msg = %q, want %q", result.Msg, wantMsg)
	}
	if result.Plan != "max_20x" {
		t.Errorf("Plan = %q, want max_20x", result.Plan)
	}
	if outcome.CooldownUntil.IsZero() {
		t.Fatal("CooldownUntil should be set")
	}
	wantCooldown := now.Add(120 * time.Second)
	if !outcome.CooldownUntil.Equal(wantCooldown) {
		t.Errorf("CooldownUntil = %v, want %v", outcome.CooldownUntil, wantCooldown)
	}
}

func TestClaudeProviderLimitsLess(t *testing.T) {
	dir := t.TempDir()

	// Fixture without limits[] — only five_hour / seven_day fallback.
	emptyBody := `{"five_hour":{"utilization":19.0,"resets_at":"2026-09-03T05:09:59.932682+00:00"},"seven_day":{"utilization":30.0,"resets_at":"2026-09-07T07:59:59.932707+00:00"}}`
	os.WriteFile(filepath.Join(dir, "nolimits.json"), []byte(emptyBody), 0644)

	routes := `{"GET api.anthropic.com/api/oauth/usage": {"file": "nolimits.json"}}`
	os.WriteFile(filepath.Join(dir, "routes.json"), []byte(routes), 0644)

	runner := newClaudeRunner("sk-ant-oat01-TEST-TOKEN-12345", false)
	client := newFixtureClient(dir)
	p := NewClaude(client, runner, "testuser", testLoc)

	result, _ := p.Fetch(context.Background(), testNow())

	if result.Status != "ok" {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("len(Rows) = %d, want 2", len(result.Rows))
	}

	r0 := result.Rows[0]
	if r0.K != "5h" || r0.Label != "CLAUDE 5h" || *r0.Pct != 19 {
		t.Errorf("Row 0 = {K:%q Label:%q Pct:%v}", r0.K, r0.Label, *r0.Pct)
	}

	r1 := result.Rows[1]
	if r1.K != "7d" || r1.Label != "CLAUDE 7d" || *r1.Pct != 30 {
		t.Errorf("Row 1 = {K:%q Label:%q Pct:%v}", r1.K, r1.Label, *r1.Pct)
	}
}

func TestClaudeProviderNoTokenInLogs(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	defer slog.SetDefault(old)
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	knownToken := "sk-ant-oat01-UNIQUE-TOKEN-9999"
	runner := newClaudeRunner(knownToken, false)
	client := newFixtureClient("../../testdata/fixtures")
	p := NewClaude(client, runner, "testuser", testLoc)

	result, _ := p.Fetch(context.Background(), testNow())

	if result.Status != "ok" {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if strings.Contains(buf.String(), knownToken) {
		t.Errorf("access token found in logs:\n%s", buf.String())
	}
	// Also check the "Bearer" prefix is not logged
	if strings.Contains(buf.String(), "Bearer "+knownToken) {
		t.Error("Authorization header value found in logs")
	}
}
