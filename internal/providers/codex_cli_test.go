package providers

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"usaged/internal/format"
)

// fakeCodexRunner implements CodexCLIRunner by returning a pre-canned stdout.
type fakeCodexRunner struct {
	stdout []byte
	err    error
}

func (f fakeCodexRunner) RunWithStdin(_ context.Context, _ []byte, _ string, _ ...string) ([]byte, error) {
	return f.stdout, f.err
}

func TestCodexCliProviderHappyPath(t *testing.T) {
	// Mirrors TestCodexProviderHappyPath but sourced from the app-server
	// fixture: 5h pct=0 ok, 7d pct=100 crit, no balance row (hasCredits=false),
	// plus a reset row (availableCount=2).
	fixture, err := os.ReadFile("../../testdata/fixtures/codex_appserver.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	runner := fakeCodexRunner{stdout: fixture}
	p := NewCodexCLI(runner, testLoc, format.DefaultAlerts())
	now := testNow()

	result, outcome := p.Fetch(context.Background(), now)

	if result.Status != "ok" {
		t.Errorf("Status = %q, want ok", result.Status)
	}
	if result.Plan != "plus" {
		t.Errorf("Plan = %q, want plus", result.Plan)
	}
	if result.Kind != "plan" {
		t.Errorf("Kind = %q, want plan", result.Kind)
	}
	if result.Severity != "crit" {
		t.Errorf("Severity = %q, want crit (GPT 7d pct=100 >= 100)", result.Severity)
	}
	if result.ID != "codex" {
		t.Errorf("ID = %q, want codex", result.ID)
	}
	if len(result.Rows) != 3 {
		t.Fatalf("len(Rows) = %d, want 3", len(result.Rows))
	}

	// Row 0: primary 5h / 18000s / pct 0 / ok (sorted first by window size)
	r0 := result.Rows[0]
	if r0.K != "5h" || r0.Label != "GPT 5h" || *r0.Pct != 0 || r0.Tier != "ok" {
		t.Errorf("Row 0 = {K:%q Label:%q Pct:%v Tier:%q}", r0.K, r0.Label, *r0.Pct, r0.Tier)
	}
	if r0.ResetAt == nil || *r0.ResetAt != 1788605284 {
		t.Errorf("Row 0 ResetAt = %v, want 1788605284", r0.ResetAt)
	}

	// Row 1: secondary 7d / 604800s / pct 100 / crit
	r1 := result.Rows[1]
	if r1.K != "7d" || r1.Label != "GPT 7d" || *r1.Pct != 100 || r1.Tier != "crit" {
		t.Errorf("Row 1 = {K:%q Label:%q Pct:%v Tier:%q}", r1.K, r1.Label, *r1.Pct, r1.Tier)
	}
	if r1.ResetAt == nil || *r1.ResetAt != 1788969665 {
		t.Errorf("Row 1 ResetAt = %v, want 1788969665", r1.ResetAt)
	}

	// Row 2: reset credits (availableCount=2)
	r2 := result.Rows[2]
	if r2.K != "rst" || r2.Label != "GPT rst" || r2.Pct != nil || r2.Txt != "2 resets" {
		t.Errorf("Row 2 = {K:%q Label:%q Pct:%v Txt:%q}", r2.K, r2.Label, r2.Pct, r2.Txt)
	}

	// No balance row: hasCredits=false
	for _, r := range result.Rows {
		if r.K == "bal" {
			t.Error("should not have a balance row when hasCredits=false")
		}
	}

	if !outcome.CooldownUntil.IsZero() {
		t.Error("CooldownUntil should be zero for happy path")
	}
}

func TestCodexCliProviderRunnerError(t *testing.T) {
	runner := fakeCodexRunner{err: errors.New("exec: codex not found")}
	p := NewCodexCLI(runner, testLoc, format.DefaultAlerts())

	result, outcome := p.Fetch(context.Background(), testNow())

	if result.Status != "error" {
		t.Errorf("Status = %q, want error", result.Status)
	}
	if result.Severity != "crit" {
		t.Errorf("Severity = %q, want crit", result.Severity)
	}
	if result.Msg != "api unreachable" {
		t.Errorf("Msg = %q, want api unreachable", result.Msg)
	}
	if result.Plan != "" {
		t.Errorf("Plan = %q, want empty", result.Plan)
	}
	if len(result.Rows) != 0 {
		t.Errorf("len(Rows) = %d, want 0", len(result.Rows))
	}
	if !outcome.CooldownUntil.IsZero() {
		t.Error("CooldownUntil should be zero")
	}
}

func TestCodexCliProviderParseError(t *testing.T) {
	runner := fakeCodexRunner{stdout: []byte("not valid json-rpc\n")}
	p := NewCodexCLI(runner, testLoc, format.DefaultAlerts())

	result, _ := p.Fetch(context.Background(), testNow())

	if result.Status != "error" {
		t.Errorf("Status = %q, want error", result.Status)
	}
	if result.Msg != "parse error" {
		t.Errorf("Msg = %q, want parse error", result.Msg)
	}
}

func TestCodexCliProviderSkippedWindow(t *testing.T) {
	// A window with limit_window_seconds == 0 is skipped by parseCodexRows.
	// Here both windows have 0 duration, so only the reset row survives.
	body := `{"jsonrpc":"2.0","id":1,"result":{"rateLimits":{"primary":{"usedPercent":50,"windowDurationMins":0,"resetsAt":0},"secondary":{"usedPercent":0,"windowDurationMins":0,"resetsAt":0},"credits":{"hasCredits":false,"unlimited":false,"balance":"0"},"planType":"plus"},"rateLimitResetCredits":{"availableCount":0,"applicableAvailableCount":0}}}`
	runner := fakeCodexRunner{stdout: []byte(body)}
	p := NewCodexCLI(runner, testLoc, format.DefaultAlerts())

	result, _ := p.Fetch(context.Background(), testNow())

	if result.Status != "ok" {
		t.Fatalf("Status = %q, want ok", result.Status)
	}
	if len(result.Rows) != 0 {
		t.Errorf("len(Rows) = %d, want 0 (all windows zero-duration, no credits, no resets)", len(result.Rows))
	}
}

func TestCodexCliProviderTimeoutClassified(t *testing.T) {
	runner := fakeCodexRunner{
		err: context.DeadlineExceeded,
	}
	p := NewCodexCLI(runner, testLoc, format.DefaultAlerts())

	result, _ := p.Fetch(context.Background(), testNow())

	if result.Status != "error" {
		t.Errorf("Status = %q, want error", result.Status)
	}
	if result.Msg != "api timeout" {
		t.Errorf("Msg = %q, want api timeout (DeadlineExceeded)", result.Msg)
	}
}

func TestCodexAppServerStdin(t *testing.T) {
	stdin := codexAppServerStdin()
	lines := strings.Split(strings.TrimSpace(string(stdin)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if !strings.Contains(lines[0], "\"method\":\"initialize\"") {
		t.Errorf("line 0 = %q, want initialize", lines[0])
	}
	if !strings.Contains(lines[1], "\"method\":\"account/rateLimits/read\"") {
		t.Errorf("line 1 = %q, want account/rateLimits/read", lines[1])
	}
}
