package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"usaged/internal/snapshot"
)

// setupScenario copies ALL base fixture files into a temp dir (not just the
// 4-file subset used by setupFixtures), then overlays scenario files from
// testdata/scenarios/<name>/ on top, and returns an httptest.Server backed by
// the fixture handler.
func setupScenario(t *testing.T, name string) *httptest.Server {
	t.Helper()
	dir := t.TempDir()

	// Copy every file from the base fixtures directory.
	entries, err := os.ReadDir(fixturesRoot)
	if err != nil {
		t.Fatalf("read fixtures root: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(fixturesRoot, e.Name()))
		if err != nil {
			t.Fatalf("read fixture %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", e.Name(), err)
		}
	}

	// Overlay scenario files from testdata/scenarios/<name>/.
	scenarioDir := filepath.Join("..", "..", "testdata", "scenarios", name)
	sEntries, err := os.ReadDir(scenarioDir)
	if err != nil {
		t.Fatalf("read scenario %s: %v", name, err)
	}
	for _, e := range sEntries {
		if e.IsDir() {
			continue
		}
		src := filepath.Join(scenarioDir, e.Name())
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read scenario file %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o644); err != nil {
			t.Fatalf("write scenario file %s: %v", e.Name(), err)
		}
	}

	handler, _, _ := newFixtureHandler(t, dir)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

// providerStatus finds the provider with the given id in a snapshot and
// returns its status and message.
func providerStatus(t *testing.T, snap *snapshot.Snapshot, id string) (status, msg string) {
	t.Helper()
	for _, p := range snap.Providers {
		if p.ID == id {
			return p.Status, p.Msg
		}
	}
	t.Fatalf("provider %q not found in snapshot", id)
	return "", ""
}

// fetchSnapshot GETs /v1/usage from ts and returns the decoded snapshot.
func fetchSnapshot(t *testing.T, ts *httptest.Server) snapshot.Snapshot {
	t.Helper()
	resp, err := http.Get(ts.URL + "/v1/usage")
	if err != nil {
		t.Fatalf("GET /v1/usage: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /v1/usage status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	var snap snapshot.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	return snap
}

// fetchRaw GETs /v1/usage with an optional If-None-Match header and returns
// the raw response (caller must close Body).
func fetchRaw(t *testing.T, ts *httptest.Server, etag string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/usage: %v", err)
	}
	return resp
}

// --- Scenario tests ---

// TestScenarioClaude401 asserts that a 401 from Claude's usage endpoint produces
// a provider block with status "auth" and msg "run claude".
func TestScenarioClaude401(t *testing.T) {
	ts := setupScenario(t, "claude-401")
	snap := fetchSnapshot(t, ts)

	status, msg := providerStatus(t, &snap, "claude")
	if status != "auth" {
		t.Errorf("claude-401: claude status = %q, want %q", status, "auth")
	}
	if msg != "run claude" {
		t.Errorf("claude-401: claude msg = %q, want %q", msg, "run claude")
	}
}

// TestScenarioClaude429 asserts that a 429 from Claude's usage endpoint produces
// a provider block with status "error" and a cooldown message "429 until HH:MM".
func TestScenarioClaude429(t *testing.T) {
	ts := setupScenario(t, "claude-429")
	snap := fetchSnapshot(t, ts)

	status, msg := providerStatus(t, &snap, "claude")
	if status != "error" {
		t.Errorf("claude-429: claude status = %q, want %q", status, "error")
	}
	if !strings.Contains(msg, "429 until") {
		t.Errorf("claude-429: claude msg = %q, want '429 until ...'", msg)
	}
}

// TestScenarioCodexExpired asserts that an expired Codex JWT produces a provider
// block with status "auth" and msg "run codex".
func TestScenarioCodexExpired(t *testing.T) {
	ts := setupScenario(t, "codex-expired")
	snap := fetchSnapshot(t, ts)

	status, msg := providerStatus(t, &snap, "codex")
	if status != "auth" {
		t.Errorf("codex-expired: codex status = %q, want %q", status, "auth")
	}
	if msg != "run codex" {
		t.Errorf("codex-expired: codex msg = %q, want %q", msg, "run codex")
	}
}

// TestScenarioAllDown asserts that when every provider endpoint returns 503,
// all providers report status "error" or "stale" with an api unreachable/timeout or
// http-503 message.
func TestScenarioAllDown(t *testing.T) {
	ts := setupScenario(t, "all-down")
	snap := fetchSnapshot(t, ts)

	for _, p := range snap.Providers {
		if p.ID != "claude" && p.ID != "codex" {
			continue
		}
		if p.Status != "error" && p.Status != "stale" {
			t.Errorf("all-down: %s status = %q, want error or stale", p.ID, p.Status)
		}
		if p.Msg != "api unreachable" && p.Msg != "api timeout" && p.Msg != "http 503" {
			t.Errorf("all-down: %s msg = %q, want api unreachable/api timeout or http 503", p.ID, p.Msg)
		}
	}
}

// TestScenarioNotFound asserts that unknown non-GET routes return a JSON 404
// with a "not found" message. (GET requests to unknown paths fall through to
// the index handler per the SPA routing design; POST/PUT etc. hit the catch-all.)
func TestScenarioNotFound(t *testing.T) {
	ts := setupScenario(t, "claude-401")

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/nonexistent", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "not found") {
		t.Errorf("body = %q, want 'not found'", string(body))
	}
}

// TestScenarioCacheControl asserts that the dashboard and JSON usage endpoints
// serve Cache-Control: no-store so clients always revalidate.
func TestScenarioCacheControl(t *testing.T) {
	ts := setupScenario(t, "claude-401")

	for _, path := range []string{"/", "/v1/usage"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		cc := resp.Header.Get("Cache-Control")
		resp.Body.Close()
		if cc != "no-store" {
			t.Errorf("%s Cache-Control = %q, want no-store", path, cc)
		}
	}
}

// TestScenarioETagStability asserts that /v1/usage returns an ETag and that
// a repeated request with If-None-Match returns 304 with an empty body.
func TestScenarioETagStability(t *testing.T) {
	ts := setupScenario(t, "claude-401")

	resp1 := fetchRaw(t, ts, "")
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", resp1.StatusCode)
	}
	etag := resp1.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag on first request")
	}

	resp2 := fetchRaw(t, ts, etag)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotModified {
		t.Errorf("second request status = %d, want 304", resp2.StatusCode)
	}
	if et2 := resp2.Header.Get("ETag"); et2 != etag {
		t.Errorf("304 ETag = %q, want %q", et2, etag)
	}
	body, _ := io.ReadAll(resp2.Body)
	if len(body) != 0 {
		t.Errorf("304 body = %q, want empty", body)
	}
}

// TestScenarioDashboardRenders asserts that GET / returns the dashboard HTML
// containing "AI Usage" as text/html with no-store.
func TestScenarioDashboardRenders(t *testing.T) {
	ts := setupScenario(t, "claude-401")

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET / status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / Content-Type = %q, want text/html", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "AI Usage") {
		t.Error("GET / body missing 'AI Usage'")
	}
}

// TestScenarioStaticFetcherOff asserts that provider slots without keys
// (OpenRouter, Groq placeholders) appear as "off" with msg "no key".
func TestScenarioStaticFetcherOff(t *testing.T) {
	ts := setupScenario(t, "claude-401")
	snap := fetchSnapshot(t, ts)

	for _, p := range snap.Providers {
		switch p.ID {
		case "openrouter:main", "openrouter:fallback", "groq":
			if p.Status != "off" {
				t.Errorf("%s status = %q, want off", p.ID, p.Status)
			}
			if p.Msg != "no key" {
				t.Errorf("%s msg = %q, want 'no key'", p.ID, p.Msg)
			}
		}
	}
}
