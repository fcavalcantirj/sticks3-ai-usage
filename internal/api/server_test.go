package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"usaged/internal/config"
	"usaged/internal/creds"
	"usaged/internal/httpx"
	"usaged/internal/providers"
	"usaged/internal/sched"
	"usaged/internal/snapshot"
)

// testLoc is the display timezone matching config.DefaultTZ.
var testLoc = time.FixedZone("America/Sao_Paulo", -3*3600)

// fixedNow matches the fixtures (resets around 2026-09-03) and the providers test
// clock, giving deterministic reset-text and rev.
var fixedNow = time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)

// fixtureFiles copied into each test's sandbox dir so routes.json can be
// mutated freely without touching the shared fixtures.
var fixtureFiles = []string{
	"claude_usage.json",
	"codex_usage.json",
	"codex_auth.json",
	"keychain.json",
}

const fixturesRoot = "../../testdata/fixtures"

// setupFixtures copies the shared fixture files into a fresh temp dir and
// returns its path.
func setupFixtures(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range fixtureFiles {
		data, err := os.ReadFile(filepath.Join(fixturesRoot, f))
		if err != nil {
			t.Fatalf("read fixture %s: %v", f, err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), data, 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", f, err)
		}
	}
	return dir
}

// newFixtureServer builds a scheduler fed by the fixture transport, runs one
// poll, and returns a test HTTP server plus the scheduler and shared client
// (the client is returned so tests can swap its transport for rev-change tests).
func newFixtureServer(t *testing.T, dir string) (*httptest.Server, *sched.Scheduler, *httpx.Client) {
	t.Helper()
	client := &httpx.Client{HTTP: &http.Client{Transport: httpx.NewFixtureTransport(dir)}}
	runner := creds.FixtureRunner(dir)
	fetchers := []providers.Fetcher{
		providers.NewClaude(client, runner, "testuser", testLoc),
		providers.NewCodex(client, filepath.Join(dir, "codex_auth.json"), testLoc),
	}
	cfg := config.Config{
		Listen:      "127.0.0.1:0",
		Interval:    900 * time.Second,
		TZ:          testLoc,
		DeviceToken: "x",
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	s := sched.NewScheduler(fetchers, cfg.Interval, "", func() time.Time { return fixedNow }, logger)
	s.PollOnce(context.Background())
	srv := New(s, cfg, logger)
	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)
	return ts, s, client
}

func TestUsageETag(t *testing.T) {
	dir := setupFixtures(t)
	ts, _, _ := newFixtureServer(t, dir)

	resp, err := http.Get(ts.URL + "/v1/usage")
	if err != nil {
		t.Fatalf("GET /v1/usage: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	if len(etag) != 10 || etag[0] != '"' || etag[9] != '"' {
		t.Errorf("ETag = %q, want quoted 8-hex", etag)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var s snapshot.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if s.V != 1 {
		t.Errorf("v = %d, want 1", s.V)
	}
	if s.Providers[0].ID != "claude" {
		t.Errorf("providers[0].id = %q, want claude", s.Providers[0].ID)
	}
}

func TestUsageNotModified(t *testing.T) {
	dir := setupFixtures(t)
	ts, _, _ := newFixtureServer(t, dir)

	resp1, err := http.Get(ts.URL + "/v1/usage")
	if err != nil {
		t.Fatal(err)
	}
	etag := resp1.Header.Get("ETag")
	resp1.Body.Close()
	if etag == "" {
		t.Fatal("no ETag on initial request")
	}

	// Quoted If-None-Match → 304, empty body, Content-Length: 0.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	req.Header.Set("If-None-Match", etag)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotModified {
		t.Errorf("status = %d, want 304", resp2.StatusCode)
	}
	if cl := resp2.Header.Get("Content-Length"); cl != "0" {
		t.Errorf("304 Content-Length = %q, want 0", cl)
	}
	body, _ := io.ReadAll(resp2.Body)
	if len(body) != 0 {
		t.Errorf("304 body = %q, want empty", body)
	}

	// Bare (unquoted) If-None-Match → 304.
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	req2.Header.Set("If-None-Match", strings.Trim(etag, `"`))
	resp3, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusNotModified {
		t.Errorf("bare etag: status = %d, want 304", resp3.StatusCode)
	}

	// Weak etag → 304.
	req3, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	req3.Header.Set("If-None-Match", "W/"+etag)
	resp4, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusNotModified {
		t.Errorf("weak etag: status = %d, want 304", resp4.StatusCode)
	}

	// Mismatched etag → 200.
	req4, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	req4.Header.Set("If-None-Match", `"deadbeef"`)
	resp5, err := http.DefaultClient.Do(req4)
	if err != nil {
		t.Fatal(err)
	}
	defer resp5.Body.Close()
	if resp5.StatusCode != http.StatusOK {
		t.Errorf("mismatched etag: status = %d, want 200", resp5.StatusCode)
	}
}

func TestUsageAfterRevChange(t *testing.T) {
	dir := setupFixtures(t)
	ts, s, client := newFixtureServer(t, dir)

	// Baseline rev.
	resp1, err := http.Get(ts.URL + "/v1/usage")
	if err != nil {
		t.Fatal(err)
	}
	rev1 := resp1.Header.Get("ETag")
	resp1.Body.Close()

	// Mutate routes.json so Claude returns 401 (auth), then swap the transport
	// so the (cached) fixture table picks up the new routes, and Refresh.
	routes := `{"GET api.anthropic.com/api/oauth/usage": {"status": 401}}`
	if err := os.WriteFile(filepath.Join(dir, "routes.json"), []byte(routes), 0o644); err != nil {
		t.Fatal(err)
	}
	client.HTTP.Transport = httpx.NewFixtureTransport(dir)
	s.Refresh()

	resp2, err := http.Get(ts.URL + "/v1/usage")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	rev2 := resp2.Header.Get("ETag")

	if rev1 == rev2 {
		t.Errorf("rev did not change after 401 fixture: %q == %q", rev1, rev2)
	}
	if rev2 == "" {
		t.Fatal("no ETag after rev change")
	}
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 after rev change", resp2.StatusCode)
	}

	// The new snapshot should reflect a non-ok Claude block.
	var snap snapshot.Snapshot
	if err := json.NewDecoder(resp2.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var claude snapshot.Provider
	for _, p := range snap.Providers {
		if p.ID == "claude" {
			claude = p
		}
	}
	if claude.Status == "ok" {
		t.Errorf("claude status = ok, want non-ok after 401 fixture; rev=%s", rev2)
	}
}

func TestHealthz(t *testing.T) {
	dir := setupFixtures(t)
	ts, _, _ := newFixtureServer(t, dir)

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var h map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		t.Fatalf("decode healthz: %v", err)
	}
	if h["ok"] != true {
		t.Errorf("ok = %v, want true", h["ok"])
	}
	for _, k := range []string{"seq", "rev", "checked_at", "uptime_sec"} {
		if _, ok := h[k]; !ok {
			t.Errorf("healthz missing key %q", k)
		}
	}
}

func TestUsageTXT(t *testing.T) {
	dir := setupFixtures(t)
	ts, _, _ := newFixtureServer(t, dir)

	resp, err := http.Get(ts.URL + "/v1/usage.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "CLAUDE 5h") {
		t.Errorf("/v1/usage.txt missing CLAUDE 5h:\n%s", body)
	}
	if !strings.Contains(string(body), "GPT 7d") {
		t.Errorf("/v1/usage.txt missing GPT 7d:\n%s", body)
	}
	if !strings.Contains(string(body), "rev=") {
		t.Errorf("/v1/usage.txt missing rev= footer:\n%s", body)
	}
}
