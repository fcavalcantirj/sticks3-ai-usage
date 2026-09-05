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
	"usaged/internal/stats"
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

// newFixtureHandlerCfg builds the auth-wrapped HTTP handler from a custom
// config. It runs one PollOnce so the scheduler has a snapshot available.
func newFixtureHandlerCfg(t *testing.T, dir string, cfg config.Config) (http.Handler, *sched.Scheduler, *httpx.Client) {
	t.Helper()
	client := &httpx.Client{HTTP: &http.Client{Transport: httpx.NewFixtureTransport(dir)}}
	runner := creds.FixtureRunner(dir)
	fetchers := []providers.Fetcher{
		providers.NewClaude(client, runner, "testuser", testLoc),
		providers.NewCodex(client, filepath.Join(dir, "codex_auth.json"), testLoc),
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	s := sched.NewScheduler(fetchers, cfg.Interval, "", func() time.Time { return fixedNow }, logger)
	s.PollOnce(context.Background())
	srv, err := New(s, cfg, "", logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv.Handler, s, client
}

func newFixtureHandler(t *testing.T, dir string) (http.Handler, *sched.Scheduler, *httpx.Client) {
	cfg := config.Config{
		Listen:      "127.0.0.1:0",
		Interval:    900 * time.Second,
		TZ:          testLoc,
		DeviceToken: "x",
	}
	return newFixtureHandlerCfg(t, dir, cfg)
}

func newFixtureServer(t *testing.T, dir string) (*httptest.Server, *sched.Scheduler, *httpx.Client) {
	t.Helper()
	handler, s, client := newFixtureHandler(t, dir)
	ts := httptest.NewServer(handler)
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

	// Quoted If-None-Match → 304, same ETag, empty body (no Content-Length assertion).
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
	if et2 := resp2.Header.Get("ETag"); et2 != etag {
		t.Errorf("304 ETag = %q, want %q", et2, etag)
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

// TestStatsETag serves /v1/stats from the stats-demo fixture and verifies
// ETag / 304 semantics: the first request returns 200 with a quoted ETag and
// JSON body, and a second request with a matching If-None-Match (quoted or
// bare) returns 304 with an empty body.
func TestStatsETag(t *testing.T) {
	dir := setupFixtures(t)
	ts, s, _ := newFixtureServer(t, dir)

	statsFixture := filepath.Join(fixturesRoot, "..", "scenarios", "stats-demo", "stats.json")
	if _, err := os.Stat(statsFixture); err != nil {
		t.Skipf("stats-demo fixture not found: %v", err)
	}
	s.LoadStatsReport(statsFixture)

	// First request → 200 with ETag and JSON body.
	resp1, err := http.Get(ts.URL + "/v1/stats")
	if err != nil {
		t.Fatalf("GET /v1/stats: %v", err)
	}
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp1.StatusCode)
	}
	etag := resp1.Header.Get("ETag")
	if len(etag) < 3 || etag[0] != '"' || etag[len(etag)-1] != '"' {
		t.Errorf("ETag = %q, want quoted hex of generated_at", etag)
	}
	if ct := resp1.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	var report stats.Report
	if err := json.Unmarshal(body1, &report); err != nil {
		t.Fatalf("decode stats: %v\n%s", err, body1)
	}
	if report.GeneratedAt == 0 {
		t.Error("generated_at is 0")
	}
	if len(report.Sources) != 2 {
		t.Errorf("sources = %d, want 2", len(report.Sources))
	}

	// Second request with matching If-None-Match → 304, same ETag, empty body.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/stats", nil)
	req.Header.Set("If-None-Match", etag)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotModified {
		t.Errorf("status = %d, want 304", resp2.StatusCode)
	}
	if etag2 := resp2.Header.Get("ETag"); etag2 != etag {
		t.Errorf("304 ETag = %q, want %q", etag2, etag)
	}
	body2, _ := io.ReadAll(resp2.Body)
	if len(body2) != 0 {
		t.Errorf("304 body = %q, want empty", body2)
	}

	// Bare (unquoted) If-None-Match → 304.
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/stats", nil)
	req2.Header.Set("If-None-Match", strings.Trim(etag, `"`))
	resp3, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusNotModified {
		t.Errorf("bare etag: status = %d, want 304", resp3.StatusCode)
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

func TestIndexHTML(t *testing.T) {
	dir := setupFixtures(t)
	ts, _, _ := newFixtureServer(t, dir)

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "AI Usage") {
		t.Errorf("GET / body missing 'AI Usage'")
	}
}

// TestStatsServedFromScan verifies that after a PollOnce with StatsCfg
// configured (REGRESSION 51), GET /v1/stats returns a report with non-empty
// sources — i.e. the scheduler's scan populates the served report rather than
// returning an empty sources map from a stale loaded file.
func TestStatsServedFromScan(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "127.0.0.1:0", Interval: 900 * time.Second,
		TZ: testLoc, DeviceToken: "x",
	}

	client := &httpx.Client{HTTP: &http.Client{Transport: httpx.NewFixtureTransport(dir)}}
	runner := creds.FixtureRunner(dir)
	fetchers := []providers.Fetcher{
		providers.NewClaude(client, runner, "testuser", testLoc),
		providers.NewCodex(client, filepath.Join(dir, "codex_auth.json"), testLoc),
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	s := sched.NewScheduler(fetchers, cfg.Interval, "", func() time.Time { return fixedNow }, logger)

	// Set StatsCfg BEFORE PollOnce so scanStats runs against fixture transcripts.
	statsDir := filepath.Join("..", "stats", "testdata", "transcripts")
	s.StatsCfg = stats.ScanConfig{
		TZ:        testLoc,
		ClaudeDir: filepath.Join(statsDir, "claude"),
		CodexDir:  filepath.Join(statsDir, "codex"),
	}
	s.PollOnce(context.Background())

	srv, err := New(s, cfg, "", logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/v1/stats")
	if err != nil {
		t.Fatalf("GET /v1/stats: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var report stats.Report
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatalf("decode stats: %v\n%s", err, body)
	}
	if len(report.Sources) == 0 {
		t.Errorf("served /v1/stats has empty sources — scan never populated the report")
	}
	if _, ok := report.Sources["claude_code"]; !ok {
		t.Errorf("served /v1/stats missing claude_code source")
	}
	if report.GeneratedAt == 0 {
		t.Error("generated_at is 0, want fresh timestamp from scan")
	}
}

// TestStatsScanErrorDoesNotWipeReport verifies that when the stats scan fails
// (non-existent transcript directory), the previously loaded report is preserved
// rather than overwritten with an empty sources map (REGRESSION 51 fix).
func TestStatsScanErrorDoesNotWipeReport(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "127.0.0.1:0", Interval: 900 * time.Second,
		TZ: testLoc, DeviceToken: "x",
	}

	client := &httpx.Client{HTTP: &http.Client{Transport: httpx.NewFixtureTransport(dir)}}
	runner := creds.FixtureRunner(dir)
	fetchers := []providers.Fetcher{
		providers.NewClaude(client, runner, "testuser", testLoc),
		providers.NewCodex(client, filepath.Join(dir, "codex_auth.json"), testLoc),
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	s := sched.NewScheduler(fetchers, cfg.Interval, "", func() time.Time { return fixedNow }, logger)
	s.StatsCfg = stats.ScanConfig{
		TZ:        testLoc,
		ClaudeDir: "/nonexistent/transcript/dir/claude",
		CodexDir:  "/nonexistent/transcript/dir/codex",
	}

	// Load a known-good report first.
	statsFixture := filepath.Join(fixturesRoot, "..", "scenarios", "stats-demo", "stats.json")
	if _, err := os.Stat(statsFixture); err != nil {
		t.Skipf("stats-demo fixture not found: %v", err)
	}
	s.LoadStatsReport(statsFixture)

	// PollOnce triggers scanStats, which will fail (bad dirs) but must NOT
	// wipe the loaded report.
	s.PollOnce(context.Background())

	report := s.CurrentStats()
	if report == nil {
		t.Fatal("CurrentStats returned nil — loaded report was wiped by scan error")
	}
	if len(report.Sources) == 0 {
		t.Error("loaded report's sources were wiped after scan error")
	}
}

// --- Auth middleware tests ---

func TestAuthLoopbackNoToken(t *testing.T) {
	dir := setupFixtures(t)
	handler, _, _ := newFixtureHandler(t, dir)

	req := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("loopback no token: status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("loopback no token: Content-Type = %q, want application/json", ct)
	}
}

func TestAuthLANNoToken(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "0.0.0.0:8765", Interval: 900 * time.Second, TZ: testLoc, DeviceToken: "x",
	}
	handler, _, _ := newFixtureHandlerCfg(t, dir, cfg)

	req := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	req.RemoteAddr = "192.168.0.77:5000"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("LAN no token: status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unauthorized") {
		t.Errorf("LAN no token: body = %q, want unauthorized", rec.Body.String())
	}
}

func TestAuthCorrectToken(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "0.0.0.0:8765", Interval: 900 * time.Second, TZ: testLoc, DeviceToken: "x",
	}
	handler, _, _ := newFixtureHandlerCfg(t, dir, cfg)

	// Header token
	req := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	req.RemoteAddr = "192.168.0.77:5000"
	req.Header.Set("X-Device-Token", "x")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("LAN with header token: status = %d, want 200", rec.Code)
	}

	// Query token
	req2 := httptest.NewRequest(http.MethodGet, "/v1/usage?token=x", nil)
	req2.RemoteAddr = "192.168.0.77:5000"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Errorf("LAN with query token: status = %d, want 200", rec2.Code)
	}
}

func TestAuthWrongToken(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "0.0.0.0:8765", Interval: 900 * time.Second, TZ: testLoc, DeviceToken: "x",
	}
	handler, _, _ := newFixtureHandlerCfg(t, dir, cfg)

	req := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	req.RemoteAddr = "192.168.0.77:5000"
	req.Header.Set("X-Device-Token", "wrong")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("LAN wrong token: status = %d, want 401", rec.Code)
	}
}

func TestAuthEmptyTokenLAN(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "0.0.0.0:8765", Interval: 900 * time.Second, TZ: testLoc, DeviceToken: "",
	}
	client := &httpx.Client{HTTP: &http.Client{Transport: httpx.NewFixtureTransport(dir)}}
	runner := creds.FixtureRunner(dir)
	fetchers := []providers.Fetcher{
		providers.NewClaude(client, runner, "testuser", testLoc),
		providers.NewCodex(client, filepath.Join(dir, "codex_auth.json"), testLoc),
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	s := sched.NewScheduler(fetchers, cfg.Interval, "", func() time.Time { return fixedNow }, logger)
	_, err := New(s, cfg, "", logger)
	if err == nil {
		t.Error("New with empty DeviceToken + non-loopback listen should return an error")
	}
}

// --- ORDER #54 / task 59 security gate tests ---

// TestMutatingRouteNoTokenLoopback401 verifies that POST /v1/keys from
// loopback with no token returns 401 (not 200, not 400). This is the core
// of ORDER #54: mutating routes require a token even from loopback.
func TestMutatingRouteNoTokenLoopback401(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "127.0.0.1:0", Interval: 900 * time.Second,
		TZ: testLoc, DeviceToken: "x",
	}
	ks := creds.NewFakeKeyStore(nil)
	handler := newHandlerWithKeyStore(t, dir, cfg, "", ks)

	req := httptest.NewRequest(http.MethodPost, "/v1/keys", strings.NewReader(`{"id":"groq","value":"gsk_test"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/keys loopback no token: status = %d, want 401", rec.Code)
	}
}

// TestMutatingRouteWrongToken401 verifies a wrong token is rejected.
func TestMutatingRouteWrongToken401(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "127.0.0.1:0", Interval: 900 * time.Second,
		TZ: testLoc, DeviceToken: "x",
	}
	ks := creds.NewFakeKeyStore(nil)
	handler := newHandlerWithKeyStore(t, dir, cfg, "", ks)

	req := httptest.NewRequest(http.MethodPost, "/v1/keys", strings.NewReader(`{"id":"groq","value":"gsk_test"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Device-Token", "wrong")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/keys wrong token: status = %d, want 401", rec.Code)
	}
}

// TestMutatingRoute401BeforeBodyRead verifies the 401 is returned before the
// handler reads the body — an unauthenticated caller with garbage JSON gets 401,
// not 400, so they learn nothing about the payload shape.
func TestMutatingRoute401BeforeBodyRead(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "127.0.0.1:0", Interval: 900 * time.Second,
		TZ: testLoc, DeviceToken: "x",
	}
	ks := creds.NewFakeKeyStore(nil)
	handler := newHandlerWithKeyStore(t, dir, cfg, "", ks)

	req := httptest.NewRequest(http.MethodPost, "/v1/keys", strings.NewReader(`{not valid json`))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/keys no token garbage body: status = %d, want 401", rec.Code)
	}
}

// TestMutatingRouteNonJSONContentType415 verifies that a mutating request with
// a valid token but a non-JSON Content-Type (and a non-empty body) gets 415.
func TestMutatingRouteNonJSONContentType415(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "127.0.0.1:0", Interval: 900 * time.Second,
		TZ: testLoc, DeviceToken: "x",
	}
	ks := creds.NewFakeKeyStore(nil)
	handler := newHandlerWithKeyStore(t, dir, cfg, "", ks)

	req := httptest.NewRequest(http.MethodPost, "/v1/keys", strings.NewReader(`{"id":"groq","value":"gsk_test"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Device-Token", "x")
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("POST /v1/keys non-JSON CT: status = %d, want 415", rec.Code)
	}
}

// TestPutConfigNoTokenLoopback401 verifies PUT /v1/config requires a token
// from loopback.
func TestPutConfigNoTokenLoopback401(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "127.0.0.1:0", Interval: 900 * time.Second,
		TZ: testLoc, DeviceToken: "x",
	}
	handler := newHandlerWithKeyStore(t, dir, cfg, "", creds.NewFakeKeyStore(nil))

	req := httptest.NewRequest(http.MethodPut, "/v1/config/interval", strings.NewReader(`{"interval_sec":600}`))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("PUT /v1/config/interval no token: status = %d, want 401", rec.Code)
	}
}

// TestPostRefreshNoTokenLoopback401 verifies POST /v1/refresh requires a token
// from loopback.
func TestPostRefreshNoTokenLoopback401(t *testing.T) {
	dir := setupFixtures(t)
	handler, _, _ := newFixtureHandler(t, dir)

	req := httptest.NewRequest(http.MethodPost, "/v1/refresh", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/refresh no token: status = %d, want 401", rec.Code)
	}
}

// TestDeleteKeyNoTokenLoopback401 verifies DELETE /v1/keys requires a token
// from loopback, and that a zero-length body does not trigger the 415 path.
func TestDeleteKeyNoTokenLoopback401(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "127.0.0.1:0", Interval: 900 * time.Second,
		TZ: testLoc, DeviceToken: "x",
	}
	ks := creds.NewFakeKeyStore(nil)
	handler := newHandlerWithKeyStore(t, dir, cfg, "", ks)

	req := httptest.NewRequest(http.MethodDelete, "/v1/keys?id=groq", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("DELETE /v1/keys no token: status = %d, want 401", rec.Code)
	}
}

// --- Refresh endpoint tests ---

func TestRefreshUnchanged(t *testing.T) {
	dir := setupFixtures(t)
	handler, _, _ := newFixtureHandler(t, dir)

	// Baseline seq after initial PollOnce.
	snap0 := getRefresh(t, handler)

	// Refresh with unchanged data — seq must not move.
	snap1 := getRefresh(t, handler)
	if snap1.Seq != snap0.Seq {
		t.Errorf("seq changed on identical refresh: %d -> %d", snap0.Seq, snap1.Seq)
	}
}

func TestRefreshSeqIncrement(t *testing.T) {
	dir := setupFixtures(t)
	handler, _, client := newFixtureHandler(t, dir)

	// Baseline.
	snap0 := getRefresh(t, handler)

	// Mutate routes.json so Claude returns 401 (auth), then swap the transport
	// so the cached fixture table picks up the new routes.
	routes := `{"GET api.anthropic.com/api/oauth/usage": {"status": 401}}`
	if err := os.WriteFile(filepath.Join(dir, "routes.json"), []byte(routes), 0o644); err != nil {
		t.Fatal(err)
	}
	client.HTTP.Transport = httpx.NewFixtureTransport(dir)

	// Refresh → data changed → seq should increment.
	snap1 := getRefresh(t, handler)
	if snap1.Seq != snap0.Seq+1 {
		t.Errorf("seq after fixture change = %d, want %d", snap1.Seq, snap0.Seq+1)
	}
	if snap0.Rev == snap1.Rev {
		t.Error("rev did not change after fixture override")
	}
}

// getRefresh POSTs /v1/refresh (from loopback) and returns the decoded snapshot.
func getRefresh(t *testing.T, handler http.Handler) snapshot.Snapshot {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/refresh", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Device-Token", "x")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/refresh: status = %d, want 200", rec.Code)
	}
	var snap snapshot.Snapshot
	if err := json.NewDecoder(rec.Body).Decode(&snap); err != nil {
		t.Fatalf("decode refresh response: %v\n%s", err, rec.Body.String())
	}
	return snap
}

// --- Config endpoint tests (task 54: interval selector persistence) ---

// TestConfigGetInterval verifies GET /v1/config returns the current interval_sec
// and listen address without exposing secrets.
func TestConfigGetInterval(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen:      "127.0.0.1:0",
		Interval:    600 * time.Second,
		TZ:          testLoc,
		DeviceToken: "x",
	}
	handler, _, _ := newFixtureHandlerCfg(t, dir, cfg)

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/config: status = %d, want 200", rec.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["interval_sec"] != float64(600) {
		t.Errorf("interval_sec = %v, want 600", resp["interval_sec"])
	}
	if resp["listen"] != "127.0.0.1:0" {
		t.Errorf("listen = %v, want 127.0.0.1:0", resp["listen"])
	}
}

// TestConfigSetInterval verifies PUT /v1/config/interval updates the interval
// at runtime and the new value is reflected in the next GET /v1/config.
func TestConfigSetInterval(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen:      "127.0.0.1:0",
		Interval:    900 * time.Second,
		TZ:          testLoc,
		DeviceToken: "x",
	}
	handler, _, _ := newFixtureHandlerCfg(t, dir, cfg)

	// PUT with sec=600.
	req := httptest.NewRequest(http.MethodPut, "/v1/config/interval", strings.NewReader(`{"interval_sec":600}`))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Device-Token", "x")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /v1/config/interval: status = %d, want 200", rec.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode PUT response: %v", err)
	}
	if resp["ok"] != true {
		t.Errorf("ok = %v, want true", resp["ok"])
	}
	if resp["interval_sec"] != float64(600) {
		t.Errorf("interval_sec = %v, want 600", resp["interval_sec"])
	}

	// GET /v1/config should now reflect 600.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req2.RemoteAddr = "127.0.0.1:12345"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	var cfg2 map[string]any
	if err := json.NewDecoder(rec2.Body).Decode(&cfg2); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if cfg2["interval_sec"] != float64(600) {
		t.Errorf("GET interval_sec = %v, want 600 after PUT", cfg2["interval_sec"])
	}
}

// TestConfigSetIntervalTooLow verifies PUT /v1/config/interval rejects values
// below MinIntervalSec (300) with a 400.
func TestConfigSetIntervalTooLow(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen:      "127.0.0.1:0",
		Interval:    900 * time.Second,
		TZ:          testLoc,
		DeviceToken: "x",
	}
	handler, _, _ := newFixtureHandlerCfg(t, dir, cfg)

	req := httptest.NewRequest(http.MethodPut, "/v1/config/interval", strings.NewReader(`{"interval_sec":200}`))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Device-Token", "x")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("PUT /v1/config/interval sec=200: status = %d, want 400", rec.Code)
	}
}

// TestConfigSetFullRoundTrip verifies PUT /vv1/config writes the full editable
// config to the YAML file atomically and the next GET reflects it.
func TestConfigSetFullRoundTrip(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen:      "127.0.0.1:0",
		Interval:    900 * time.Second,
		TZ:          testLoc,
		DeviceToken: "x",
	}
	configPath := filepath.Join(dir, "config.yaml")
	initial := "interval_sec: 900\n"
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	handler := buildTestHandlerWithConfigPath(t, dir, cfg, configPath)

	payload := `{"interval_sec":600,"listen":"127.0.0.1:0","tz":"America/Sao_Paulo","alerts":{"openrouter_low_usd":1.0},"providers":[{"id":"openrouter:main","enabled":true,"label":"OR Main","key_env":"OPENROUTER_API_KEY","probe":false}]}`

	req := httptest.NewRequest(http.MethodPut, "/v1/config", strings.NewReader(payload))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Device-Token", "x")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /v1/config: status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}

	// Verify the file was written.
	written, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	if !strings.Contains(string(written), "interval_sec: 600") {
		t.Errorf("config file missing interval_sec: 600, got:\n%s", written)
	}
	if !strings.Contains(string(written), "key_env: OPENROUTER_API_KEY") {
		t.Errorf("config file missing key_env, got:\n%s", written)
	}

	// Verify a backup was created.
	files, _ := filepath.Glob(configPath + ".bak.*")
	if len(files) == 0 {
		t.Error("no backup file created")
	}

	// Verify GET reflects the change.
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	resp, err := http.Get(ts.URL + "/v1/config")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var cfg2 map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&cfg2); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if cfg2["interval_sec"] != float64(600) {
		t.Errorf("GET interval_sec = %v, want 600", cfg2["interval_sec"])
	}
}

// TestConfigSetRejectsKeyValue verifies PUT /v1/config rejects a payload
// containing a literal API key value.
func TestConfigSetRejectsKeyValue(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen:      "127.0.0.1:0",
		Interval:    900 * time.Second,
		TZ:          testLoc,
		DeviceToken: "x",
	}
	configPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(configPath, []byte("interval_sec: 900\n"), 0o600)
	handler := buildTestHandlerWithConfigPath(t, dir, cfg, configPath)

	payload := `{"interval_sec":600,"providers":[{"id":"openrouter:main","key":"sk-or-v1-actual-secret-key"}]}`
	req := httptest.NewRequest(http.MethodPut, "/v1/config", strings.NewReader(payload))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Device-Token", "x")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("PUT with literal key: status = %d, want 400", rec.Code)
	}

	// The file should be unchanged.
	written, _ := os.ReadFile(configPath)
	if strings.Contains(string(written), "600") {
		t.Error("config file was modified despite validation failure")
	}
}

// TestConfigSetInvalidInterval verifies PUT /v1/config rejects an interval
// below the minimum.
func TestConfigSetInvalidInterval(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen:      "127.0.0.1:0",
		Interval:    900 * time.Second,
		TZ:          testLoc,
		DeviceToken: "x",
	}
	configPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(configPath, []byte("interval_sec: 900\n"), 0o600)
	handler := buildTestHandlerWithConfigPath(t, dir, cfg, configPath)

	payload := `{"interval_sec":200}`
	req := httptest.NewRequest(http.MethodPut, "/v1/config", strings.NewReader(payload))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Device-Token", "x")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("PUT with interval 200: status = %d, want 400", rec.Code)
	}
}

// TestConfigSetRejectedPayloadLeavesFileUnchanged verifies that any validation
// failure leaves the existing config file byte-identical.
func TestConfigSetRejectedPayloadLeavesFileUnchanged(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen:      "127.0.0.1:0",
		Interval:    900 * time.Second,
		TZ:          testLoc,
		DeviceToken: "x",
	}
	configPath := filepath.Join(dir, "config.yaml")
	original := "interval_sec: 900\nlisten: 127.0.0.1:0\n"
	os.WriteFile(configPath, []byte(original), 0o600)
	handler := buildTestHandlerWithConfigPath(t, dir, cfg, configPath)

	// Send a payload missing provider id.
	payload := `{"providers":[{"label":"test"}]}`
	req := httptest.NewRequest(http.MethodPut, "/v1/config", strings.NewReader(payload))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Device-Token", "x")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("PUT with missing provider id: status = %d, want 400", rec.Code)
	}

	// File should be unchanged.
	written, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != original {
		t.Errorf("config file changed: got %q, want %q", string(written), original)
	}
}

// buildTestHandlerWithConfigPath creates a test handler with a specific config
// path for testing config persistence.
func buildTestHandlerWithConfigPath(t *testing.T, dir string, cfg config.Config, configPath string) http.Handler {
	t.Helper()
	client := &httpx.Client{HTTP: &http.Client{Transport: httpx.NewFixtureTransport(dir)}}
	runner := creds.FixtureRunner(dir)
	fetchers := []providers.Fetcher{
		providers.NewClaude(client, runner, "testuser", testLoc),
		providers.NewCodex(client, filepath.Join(dir, "codex_auth.json"), testLoc),
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	s := sched.NewScheduler(fetchers, cfg.Interval, "", func() time.Time { return fixedNow }, logger)
	s.PollOnce(context.Background())
	srv, err := New(s, cfg, configPath, logger, WithKeyStore(creds.NewFakeKeyStore(nil)), WithGetenv(func(k string) string {
		if k == "OPENROUTER_API_KEY" {
			return "env-key-main"
		}
		return os.Getenv(k)
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv.Handler
}

// --- Task 57 config & key endpoint tests ---

// TestConfigGetProviders renders all five providers even with no config.yaml.
func TestConfigGetProviders(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "127.0.0.1:0", Interval: 900 * time.Second,
		TZ: testLoc, DeviceToken: "x",
	}
	handler := buildTestHandlerWithConfigPath(t, dir, cfg, "")

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/config: status = %d", rec.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	provs, ok := resp["providers"].([]any)
	if !ok || len(provs) == 0 {
		t.Fatalf("providers = %v, want 5", provs)
	}
	if len(provs) != 5 {
		t.Errorf("providers = %d, want 5", len(provs))
	}
	// Verify canonical order.
	expected := []string{"claude", "codex", "openrouter:main", "openrouter:fallback", "groq"}
	for i, e := range expected {
		p := provs[i].(map[string]any)
		if p["id"] != e {
			t.Errorf("providers[%d].id = %v, want %s", i, p["id"], e)
		}
	}
}

// TestConfigGetNeverReturnsKey verifies GET /v1/config never includes a raw key value.
func TestConfigGetNeverReturnsKey(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "127.0.0.1:0", Interval: 900 * time.Second,
		TZ: testLoc, DeviceToken: "x",
	}
	ks := creds.NewFakeKeyStore(map[string]string{"openrouter:main": "sk-or-v1-secret"})
	handler := newHandlerWithKeyStore(t, dir, cfg, "", ks)

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "sk-or-v1-secret") {
		t.Errorf("GET /v1/config leaked a key value: %s", body)
	}
	// key_state should be "set" (from keychain) but value must not appear.
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	provs := resp["providers"].([]any)
	orMain := provs[2].(map[string]any)
	if orMain["key_state"] != "set" {
		t.Errorf("key_state = %v, want set (from keychain)", orMain["key_state"])
	}
}

// TestConfigEnvBeatsKeychain verifies env var takes priority over keychain for key state.
func TestConfigEnvBeatsKeychain(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "127.0.0.1:0", Interval: 900 * time.Second,
		TZ: testLoc, DeviceToken: "x",
	}
	ks := creds.NewFakeKeyStore(map[string]string{"openrouter:main": "keychain-key"})
	handler := newHandlerWithKeyStore(t, dir, cfg, "", ks, WithGetenv(func(k string) string {
		if k == "OPENROUTER_API_KEY" {
			return "env-key"
		}
		return ""
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	provs := resp["providers"].([]any)
	orMain := provs[2].(map[string]any)
	if orMain["key_state"] != "set" {
		t.Errorf("key_state = %v, want set", orMain["key_state"])
	}
	if orMain["key_env"] != "OPENROUTER_API_KEY" {
		t.Errorf("key_env = %v, want OPENROUTER_API_KEY", orMain["key_env"])
	}
}

// TestConfigSetPlanBlock verifies plan cost values round-trip through PUT /v1/config.
func TestConfigSetPlanBlock(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "127.0.0.1:0", Interval: 900 * time.Second,
		TZ: testLoc, DeviceToken: "x",
	}
	configPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(configPath, []byte("interval_sec: 900\n"), 0o600)
	handler := newHandlerWithKeyStore(t, dir, cfg, configPath, creds.NewFakeKeyStore(nil))

	// PUT with a plan block for claude.
	payload := `{"interval_sec":600,"listen":"127.0.0.1:0","tz":"America/Sao_Paulo","alerts":{"openrouter_low_usd":1.0,"quota_warn_pct":90},"providers":[{"id":"claude","enabled":true,"label":"Claude","plan":{"cost":200,"currency":"USD","label":"Max 20x","cost_usd":200}}]}`
	req := httptest.NewRequest(http.MethodPut, "/v1/config", strings.NewReader(payload))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Device-Token", "x")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /v1/config: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Verify the plan was persisted to the file.
	written, _ := os.ReadFile(configPath)
	if !strings.Contains(string(written), "cost: 200") {
		t.Errorf("config file missing plan cost: %s", written)
	}
	if !strings.Contains(string(written), "cost_usd: 200") {
		t.Errorf("config file missing cost_usd: %s", written)
	}

	// GET should reflect the plan block.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req2.RemoteAddr = "127.0.0.1:12345"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	var resp map[string]any
	json.NewDecoder(rec2.Body).Decode(&resp)
	provs := resp["providers"].([]any)
	claude := provs[0].(map[string]any)
	plan := claude["plan"].(map[string]any)
	if plan["cost"] != float64(200) {
		t.Errorf("plan.cost = %v, want 200", plan["cost"])
	}
	if plan["currency"] != "USD" {
		t.Errorf("plan.currency = %v, want USD", plan["currency"])
	}
	if plan["label"] != "Max 20x" {
		t.Errorf("plan.label = %v, want Max 20x", plan["label"])
	}
}

// TestKeyEndpointsLoopbackOnly verifies POST/DELETE /v1/keys reject non-loopback.
func TestKeyEndpointsLoopbackOnly(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "0.0.0.0:8765", Interval: 900 * time.Second,
		TZ: testLoc, DeviceToken: "x",
	}
	ks := creds.NewFakeKeyStore(nil)
	handler := newHandlerWithKeyStore(t, dir, cfg, "", ks)

	// POST from non-loopback → 403 (auth passes with token, then loopback check).
	req := httptest.NewRequest(http.MethodPost, "/v1/keys", strings.NewReader(`{"id":"groq","value":"gsk_test"}`))
	req.RemoteAddr = "192.168.0.5:1234"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Device-Token", "x")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST /v1/keys from LAN: status = %d, want 403", rec.Code)
	}

	// DELETE from non-loopback → 403.
	req2 := httptest.NewRequest(http.MethodDelete, "/v1/keys?id=groq", nil)
	req2.RemoteAddr = "192.168.0.5:1234"
	req2.Header.Set("X-Device-Token", "x")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Errorf("DELETE /v1/keys from LAN: status = %d, want 403", rec2.Code)
	}
}

// TestKeySetKeyStoresInKeychain verifies POST /v1/keys writes to the injected
// fake keychain, not to YAML.
func TestKeySetKeyStoresInKeychain(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "127.0.0.1:0", Interval: 900 * time.Second,
		TZ: testLoc, DeviceToken: "x",
	}
	ks := creds.NewFakeKeyStore(nil)
	configPath := filepath.Join(dir, "config.yaml")
	handler := newHandlerWithKeyStore(t, dir, cfg, configPath, ks)

	req := httptest.NewRequest(http.MethodPost, "/v1/keys", strings.NewReader(`{"id":"groq","value":"gsk_test_key_123"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Device-Token", "x")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/keys: status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Verify the key is in the keychain, not in the YAML file.
	val, ok, _ := ks.Get(context.Background(), "groq")
	if !ok || val != "gsk_test_key_123" {
		t.Errorf("keychain groq = %q (ok=%v), want gsk_test_key_123", val, ok)
	}
	if data, _ := os.ReadFile(configPath); strings.Contains(string(data), "gsk_test") {
		t.Error("API key leaked into config.yaml")
	}

	// GET /v1/config should show key_state set but never the value.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req2.RemoteAddr = "127.0.0.1:12345"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if strings.Contains(rec2.Body.String(), "gsk_test_key_123") {
		t.Error("GET /v1/config leaked the key value")
	}
}

// TestKeyDeleteRemovesFromKeychain verifies DELETE /v1/keys removes from keychain.
func TestKeyDeleteRemovesFromKeychain(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "127.0.0.1:0", Interval: 900 * time.Second,
		TZ: testLoc, DeviceToken: "x",
	}
	ks := creds.NewFakeKeyStore(map[string]string{"groq": "gsk_existing"})
	handler := newHandlerWithKeyStore(t, dir, cfg, "", ks)

	// DELETE the key.
	req := httptest.NewRequest(http.MethodDelete, "/v1/keys?id=groq", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Device-Token", "x")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /v1/keys: status = %d", rec.Code)
	}

	// Verify it's gone.
	_, ok, _ := ks.Get(context.Background(), "groq")
	if ok {
		t.Error("key still in keychain after DELETE")
	}
}

// TestKeyRateLimited verifies the key endpoints are rate-limited.
func TestKeyRateLimited(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "127.0.0.1:0", Interval: 900 * time.Second,
		TZ: testLoc, DeviceToken: "x",
	}
	ks := creds.NewFakeKeyStore(nil)
	handler := newHandlerWithKeyStore(t, dir, cfg, "", ks)

	// Fire 5 requests (limit is 5/min), the 6th should be 429.
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/keys", strings.NewReader(`{"id":"groq","value":"gsk_x"}`))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("X-Device-Token", "x")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d", i, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/keys", strings.NewReader(`{"id":"groq","value":"gsk_x"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Device-Token", "x")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("6th request: status = %d, want 429", rec.Code)
	}
}

// TestConfigInvalidThresholdRejected verifies PUT /v0/config rejects invalid
// alert thresholds with a field-specific error.
func TestConfigInvalidThresholdRejected(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "127.0.0.1:0", Interval: 900 * time.Second,
		TZ: testLoc, DeviceToken: "x",
	}
	configPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(configPath, []byte("interval_sec: 900\n"), 0o600)
	handler := newHandlerWithKeyStore(t, dir, cfg, configPath, creds.NewFakeKeyStore(nil))

	// quota_warn_pct must be 50-100; 10 is invalid.
	payload := `{"interval_sec":600,"alerts":{"quota_warn_pct":10,"openrouter_low_usd":1.0}}`
	req := httptest.NewRequest(http.MethodPut, "/v1/config", strings.NewReader(payload))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Device-Token", "x")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("PUT invalid threshold: status = %d, want 400", rec.Code)
	}
	// File should be unchanged.
	written, _ := os.ReadFile(configPath)
	if strings.Contains(string(written), "600") {
		t.Error("config file was modified despite validation failure")
	}
}

// newHandlerWithKeyStore creates a test handler with an injected keychain and
// optional WithGetenv option. All other options use the defaults.
func newHandlerWithKeyStore(t *testing.T, dir string, cfg config.Config, configPath string, ks creds.KeyStore, opts ...Option) http.Handler {
	t.Helper()
	client := &httpx.Client{HTTP: &http.Client{Transport: httpx.NewFixtureTransport(dir)}}
	runner := creds.FixtureRunner(dir)
	fetchers := []providers.Fetcher{
		providers.NewClaude(client, runner, "testuser", testLoc),
		providers.NewCodex(client, filepath.Join(dir, "codex_auth.json"), testLoc),
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	s := sched.NewScheduler(fetchers, cfg.Interval, "", func() time.Time { return fixedNow }, logger)
	s.PollOnce(context.Background())
	allOpts := append([]Option{WithKeyStore(ks)}, opts...)
	// Deduplicate: if WithGetenv was also passed, use it.
	hasGetenv := false
	for _, o := range opts {
		var s2 Server
		o(&s2)
		if s2.getenv != nil {
			hasGetenv = true
		}
	}
	if !hasGetenv {
		allOpts = append(allOpts, WithGetenv(func(k string) string {
			if k == "OPENROUTER_API_KEY" {
				return "env-key-main"
			}
			return os.Getenv(k)
		}))
	}
	srv, err := New(s, cfg, configPath, logger, allOpts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv.Handler
}
