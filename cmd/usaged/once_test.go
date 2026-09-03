package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"usaged/internal/snapshot"
)

// fixedOnceNow is the clock used for deterministic `once` output. It matches
// the fixtures (claude_usage.json resets ~2026-09-03) and the providers test
// clock (testNow).
var fixedOnceNow = time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)

func onceTestArgs(extra ...string) []string {
	args := []string{"once", "--fixtures", "../../testdata/fixtures", "--tz", "America/Sao_Paulo"}
	return append(args, extra...)
}

// captureOnce runs the once command in-process with a fixed clock and returns
// the stdout output and exit code.
func captureOnce(t *testing.T, args ...string) (string, int) {
	t.Helper()
	prev := nowFunc
	nowFunc = func() time.Time { return fixedOnceNow }
	defer func() { nowFunc = prev }()

	var buf bytes.Buffer
	code := run(args, &buf)
	return buf.String(), code
}

func TestOnceTableFixtures(t *testing.T) {
	out, code := captureOnce(t, onceTestArgs()...)

	// All providers present in fixtures are ok; placeholders are off, so the
	// process exits 3 per the cron alerting contract.
	if code != 3 {
		t.Errorf("exit code = %d, want 3 (off providers present)", code)
	}

	wantClaude := "Claude     CLAUDE 5h    19%  02:09    ok"
	wantCodex := "ChatGPT    GPT 5h      100%  23:13    ok"

	for _, line := range strings.Split(out, "\n") {
		t.Logf("LINE: %q", line)
	}

	if !containsLine(out, wantClaude) {
		t.Errorf("table missing exact line:\n  want: %q", wantClaude)
	}
	if !containsLine(out, wantCodex) {
		t.Errorf("table missing exact line:\n  want: %q", wantCodex)
	}
	if !strings.Contains(out, "FABLE 7d") {
		t.Errorf("table missing FABLE 7d row:\n%s", out)
	}
	if !strings.Contains(out, "GPT 7d") {
		t.Errorf("table missing GPT 7d row:\n%s", out)
	}
	if !strings.HasPrefix(tableFooter(out), "rev=") {
		t.Errorf("table footer does not start with rev=:\n%s", out)
	}
}

func TestOnceJSONFixtures(t *testing.T) {
	out, _ := captureOnce(t, onceTestArgs("--json")...)

	var s snapshot.Snapshot
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("JSON parse error: %v\n%s", err, out)
	}
	if s.V != 1 {
		t.Errorf("v = %d, want 1", s.V)
	}
	if len(s.Rev) != 8 {
		t.Errorf("rev len = %d, want 8 (%q)", len(s.Rev), s.Rev)
	}
	if !isHex(s.Rev) {
		t.Errorf("rev %q is not 8 hex chars", s.Rev)
	}
	if len(s.Providers) == 0 || s.Providers[0].ID != "claude" {
		t.Errorf("providers[0].id = %q, want claude", providerIDOr(s))
	}
}

func TestOnceStateTempByDefault(t *testing.T) {
	// Without --state, once must not touch ~/.local/state/usaged/state.json.
	prev := nowFunc
	nowFunc = func() time.Time { return fixedOnceNow }
	defer func() { nowFunc = prev }()

	var buf bytes.Buffer
	home := t.TempDir()
	t.Setenv("HOME", home)
	code := run(onceTestArgs(), &buf)
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}

	// The production state file under $HOME must not exist; once uses a temp
	// path instead so it never clobbers real state.
	gotPath := filepath.Join(home, ".local", "state", "usaged", "state.json")
	if _, err := os.Stat(gotPath); !os.IsNotExist(err) {
		t.Errorf("state file %s should not exist, got err=%v", gotPath, err)
	}
}

// helpers

func containsLine(s, want string) bool {
	for _, line := range strings.Split(s, "\n") {
		if line == want {
			return true
		}
	}
	return false
}

func tableFooter(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

func isHex(s string) bool {
	if len(s) != 8 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func providerIDOr(s snapshot.Snapshot) string {
	if len(s.Providers) > 0 {
		return s.Providers[0].ID
	}
	return "<none>"
}
