package providers

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"testing"
	"time"

	"usaged/internal/creds"
	"usaged/internal/format"
	"usaged/internal/httpx"
	"usaged/internal/snapshot"
)

// TestLiveClaude runs the real Claude provider against the live Keychain item
// "Claude Code-credentials" and the real Anthropic OAuth usage endpoint.
// It is skipped unless USAGED_LIVE=1 — the founder only runs these.
func TestLiveClaude(t *testing.T) {
	if os.Getenv("USAGED_LIVE") != "1" {
		t.Skip("USAGED_LIVE=1 not set; skipping live test")
	}

	u, err := user.Current()
	if err != nil {
		t.Skipf("skip: cannot resolve OS user: %v", err)
	}

	// Real transport (no fixture transport), real ExecRunner (real Keychain).
	client := &httpx.Client{UserAgent: "usaged/0.1"}
	runner := creds.ExecRunner{}
	p := NewClaude(client, runner, u.Username, testLoc)

	// Use a 30s context — the scheduler uses 20s, the httpx default is 10s.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, outcome := p.Fetch(ctx, testNow())

	if result.Status != "ok" {
		t.Fatalf("Claude live status = %q, want ok (msg=%s); outcome=%v",
			result.Status, result.Msg, outcome)
	}
	if result.ID != "claude" {
		t.Errorf("ID = %q, want claude", result.ID)
	}
	if len(result.Rows) < 2 || len(result.Rows) > 3 {
		t.Fatalf("len(Rows) = %d, want 2–3", len(result.Rows))
	}
	for _, r := range result.Rows {
		if r.Pct != nil {
			pct := *r.Pct
			if pct < 0 || pct > 100 {
				t.Errorf("row %q pct = %d, want 0–100", r.Label, pct)
			}
		}
	}

	printTable(t, "Claude (live)", result)
}

// TestLiveCodex runs the real Codex provider against ~/.codex/auth.json and the
// real ChatGPT wham/usage endpoint. It is skipped unless USAGED_LIVE=1.
func TestLiveCodex(t *testing.T) {
	if os.Getenv("USAGED_LIVE") != "1" {
		t.Skip("USAGED_LIVE=1 not set; skipping live test")
	}

	// Real transport, real auth.json path (defaults to ~/.codex/auth.json).
	client := &httpx.Client{UserAgent: "usaged/0.1"}
	p := NewCodex(client, "", testLoc)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, outcome := p.Fetch(ctx, testNow())

	if result.Status != "ok" {
		t.Fatalf("Codex live status = %q, want ok (msg=%s); outcome=%v",
			result.Status, result.Msg, outcome)
	}
	if result.ID != "codex" {
		t.Errorf("ID = %q, want codex", result.ID)
	}
	if len(result.Rows) < 2 || len(result.Rows) > 3 {
		t.Fatalf("len(Rows) = %d, want 2–3", len(result.Rows))
	}
	for _, r := range result.Rows {
		if r.Pct != nil {
			pct := *r.Pct
			if pct < 0 || pct > 100 {
				t.Errorf("row %q pct = %d, want 0–100", r.Label, pct)
			}
		}
	}

	printTable(t, "Codex (live)", result)
}

// printTable renders a snapshot.Snapshot with the single provider to stdout
// so the founder can eyeball the live numbers against claude.ai / chatgpt.com.
func printTable(t *testing.T, header string, p snapshot.Provider) {
	t.Helper()
	snap := snapshot.Snapshot{
		V:         1,
		Providers: []snapshot.Provider{p},
	}
	fmt.Println(header)
	format.RenderTable(snap, os.Stdout, testLoc)
}
