package providers

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"usaged/internal/snapshot"
)

// writeStatuslineFixture writes the statusline fixture to a temp file with a
// modification time set to `age` ago from now. Returns the file path.
func writeStatuslineFixture(t *testing.T, now time.Time, age time.Duration) string {
	t.Helper()
	fixture, err := os.ReadFile("../../testdata/fixtures/statusline_stdin.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-statusline.json")
	if err := os.WriteFile(path, fixture, 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	mtime := now.Add(-age)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return path
}

func TestStatuslineProviderHappyPath(t *testing.T) {
	path := writeStatuslineFixture(t, testNow(), 1*time.Minute) // fresh
	p := NewClaudeStatusline(path, nil, testLoc)
	now := testNow()

	result, outcome := p.Fetch(context.Background(), now)

	if result.Status != "ok" {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if result.Plan != "plus" {
		t.Errorf("Plan = %q, want plus", result.Plan)
	}
	if result.Kind != "plan" {
		t.Errorf("Kind = %q, want plan", result.Kind)
	}
	if result.ID != "claude" {
		t.Errorf("ID = %q, want claude", result.ID)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("len(Rows) = %d, want 2", len(result.Rows))
	}

	// Row 0: 5h pct=23 (23.5 rounds to 23) tier=ok
	r0 := result.Rows[0]
	if r0.K != "5h" || r0.Label != "CLAUDE 5h" || *r0.Pct != 23 || r0.Tier != "ok" {
		t.Errorf("Row 0 = {K:%q Label:%q Pct:%v Tier:%q}", r0.K, r0.Label, *r0.Pct, r0.Tier)
	}
	if r0.ResetAt == nil || *r0.ResetAt != 1738425600 {
		t.Errorf("Row 0 ResetAt = %v, want 1738425600", r0.ResetAt)
	}

	// Row 1: 7d pct=41 (41.2 rounds to 41) tier=ok
	r1 := result.Rows[1]
	if r1.K != "7d" || r1.Label != "CLAUDE 7d" || *r1.Pct != 41 || r1.Tier != "ok" {
		t.Errorf("Row 1 = {K:%q Label:%q Pct:%v Tier:%q}", r1.K, r1.Label, *r1.Pct, r1.Tier)
	}
	if r1.ResetAt == nil || *r1.ResetAt != 1738857600 {
		t.Errorf("Row 1 ResetAt = %v, want 1738857600", r1.ResetAt)
	}

	if !outcome.CooldownUntil.IsZero() {
		t.Error("CooldownUntil should be zero")
	}
}

func TestStatuslineProviderStaleFallback(t *testing.T) {
	path := writeStatuslineFixture(t, testNow(), 15*time.Minute) // past TTL

	// Fake fallback that returns a known block.
	fallback := &fakeClaudeFetcher{
		result: claudeBlock("ok", "", "max", nil, testNow().Unix()),
	}
	p := NewClaudeStatusline(path, fallback, testLoc)

	result, _ := p.Fetch(context.Background(), testNow())

	if result.Status != "ok" {
		t.Errorf("Status = %q, want ok (fallback)", result.Status)
	}
	if result.Plan != "max" {
		t.Errorf("Plan = %q, want max (from fallback)", result.Plan)
	}
	if result.Label != "Claude" {
		t.Errorf("Label = %q, want Claude (from fallback)", result.Label)
	}
}

type fakeClaudeFetcher struct {
	result snapshot.Provider
}

func (f *fakeClaudeFetcher) ID() string { return "claude" }
func (f *fakeClaudeFetcher) Fetch(_ context.Context, _ time.Time) (snapshot.Provider, Outcome) {
	return f.result, Outcome{}
}
