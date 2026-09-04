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

	// All providers present in fixtures are ok; placeholders are off (not
	// configured), which is normal and exits 0 (only auth/error exit 3).
	if code != 0 {
		t.Errorf("exit code = %d, want 0 (off is normal)", code)
	}

	// Column widths are sized from the data (ORDER #17): the provider column
	// expands to 19 chars for "OpenRouter fallback", so the padding differs
	// from the old fixed-width output.
	wantClaude := "Claude              CLAUDE 5h   19%  02:09   ok"
	wantCodex := "ChatGPT             GPT 5h     100%  23:13   ok"

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
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	// The production state file under $HOME must not exist; once uses a temp
	// path instead so it never clobbers real state.
	gotPath := filepath.Join(home, ".local", "state", "usaged", "state.json")
	if _, err := os.Stat(gotPath); !os.IsNotExist(err) {
		t.Errorf("state file %s should not exist, got err=%v", gotPath, err)
	}
}

// --- Golden tests (Task 26: providers integration) ---

var goldenFullProviders = []string{"claude", "codex", "openrouter:main", "openrouter:fallback", "groq"}

// providerIDsFrom returns the id of each provider in order.
func providerIDsFrom(ps []snapshot.Provider) []string {
	ids := make([]string, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
	}
	return ids
}

// findProvider returns a pointer to the provider with the given id, or nil.
func findProvider(ps []snapshot.Provider, id string) *snapshot.Provider {
	for i := range ps {
		if ps[i].ID == id {
			return &ps[i]
		}
	}
	return nil
}

// findRow returns a pointer to the row with the given key, or nil.
func findRow(p *snapshot.Provider, k string) *snapshot.Row {
	if p == nil {
		return nil
	}
	for i := range p.Rows {
		if p.Rows[i].K == k {
			return &p.Rows[i]
		}
	}
	return nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func setDummyKeys(t *testing.T) {
	t.Helper()
	t.Setenv("OPENROUTER_API_KEY", "x")
	t.Setenv("OPENROUTER_API_KEY_FALLBACK", "fx")
	t.Setenv("GROQ_API_KEY", "gx")
}

// TestGoldenFullSnapshot generates (on first run) and then verifies a golden
// snapshot file produced by `once --json` in fixtures mode with all dummy keys
// set. The clock is fixed by captureOnce so output is deterministic.
func TestGoldenFullSnapshot(t *testing.T) {
	setDummyKeys(t)

	out, _ := captureOnce(t, onceTestArgs("--json")...)

	goldenPath := "../../testdata/snapshots/example_full.json"
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		if !os.IsNotExist(err) {
			t.Fatalf("read golden: %v", err)
		}
		// First run: create the golden file and skip.
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(out), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Skipf("created %s; re-run to compare", goldenPath)
	}

	var snap, goldenSnap snapshot.Snapshot
	if err := json.Unmarshal([]byte(out), &snap); err != nil {
		t.Fatalf("unmarshal current output: %v\n%s", err, out)
	}
	if err := json.Unmarshal(golden, &goldenSnap); err != nil {
		t.Fatalf("unmarshal golden: %v\n%s", err, golden)
	}

	// Rev must be stable (excludes timestamps by design).
	if snap.Rev != goldenSnap.Rev {
		t.Errorf("rev changed: got %s, want %s", snap.Rev, goldenSnap.Rev)
	}

	// Canonical provider order.
	gotIDs := providerIDsFrom(snap.Providers)
	if !sameStrings(gotIDs, goldenFullProviders) {
		t.Errorf("provider order = %v, want %v", gotIDs, goldenFullProviders)
	}

	// openrouter:main has a bal row with txt "$0.07" (balance from fixtures).
	orMain := findProvider(snap.Providers, "openrouter:main")
	if orMain == nil {
		t.Fatal("provider openrouter:main not found")
	}
	bal := findRow(orMain, "bal")
	if bal == nil {
		t.Fatal("openrouter:main missing bal row")
	}
	if bal.Txt != "$0.07" {
		t.Errorf("openrouter:main bal txt = %q, want %q", bal.Txt, "$0.07")
	}

	// groq has a key row with txt "ok" (default mode, key valid).
	grq := findProvider(snap.Providers, "groq")
	if grq == nil {
		t.Fatal("provider groq not found")
	}
	key := findRow(grq, "key")
	if key == nil {
		t.Fatal("groq missing key row")
	}
	if key.Txt != "ok" {
		t.Errorf("groq key txt = %q, want %q", key.Txt, "ok")
	}
}

// TestGoldenRevStable asserts that running `once --json` twice with the same
// fixed clock and dummy keys yields the same rev.
func TestGoldenRevStable(t *testing.T) {
	setDummyKeys(t)

	out1, _ := captureOnce(t, onceTestArgs("--json")...)
	out2, _ := captureOnce(t, onceTestArgs("--json")...)

	var s1, s2 snapshot.Snapshot
	if err := json.Unmarshal([]byte(out1), &s1); err != nil {
		t.Fatalf("unmarshal first: %v", err)
	}
	if err := json.Unmarshal([]byte(out2), &s2); err != nil {
		t.Fatalf("unmarshal second: %v", err)
	}

	if s1.Rev != s2.Rev {
		t.Errorf("rev not stable across runs: first %s, second %s", s1.Rev, s2.Rev)
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
