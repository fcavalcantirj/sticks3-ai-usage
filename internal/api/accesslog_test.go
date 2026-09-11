package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"usaged/internal/config"
	"usaged/internal/creds"
	"usaged/internal/format"
	"usaged/internal/httpx"
	"usaged/internal/providers"
	"usaged/internal/sched"
)

// newCaptureHandler builds a test server identical to newFixtureServer but
// with a slog logger whose output is captured into logBuf. This lets tests
// inspect access-log output without touching the real log.
func newCaptureHandler(t *testing.T, dir string) (http.Handler, *bytes.Buffer) {
	t.Helper()
	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logBuf, nil))
	cfg := config.Config{
		Listen:      "127.0.0.1:0",
		Interval:    900 * time.Second,
		TZ:          testLoc,
		DeviceToken: "x",
	}
	client := &httpx.Client{HTTP: &http.Client{Transport: httpx.NewFixtureTransport(dir)}}
	runner := creds.FixtureRunner(dir)
	fetchers := []providers.Fetcher{
		providers.NewClaude(client, runner, "testuser", testLoc, format.DefaultAlerts()),
		providers.NewCodex(client, filepath.Join(dir, "codex_auth.json"), testLoc, format.DefaultAlerts()),
	}
	s := sched.NewScheduler(fetchers, cfg.Interval, "", func() time.Time { return fixedNow }, logger)
	s.PollOnce(context.Background())
	srv, err := New(s, cfg, "", logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv.Handler, logBuf
}

// newFixtureServerWithCapture wraps newCaptureHandler in an httptest.Server.
func newFixtureServerWithCapture(t *testing.T, dir string) (*httptest.Server, *bytes.Buffer) {
	t.Helper()
	handler, logBuf := newCaptureHandler(t, dir)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts, logBuf
}

// --- Tracker unit tests (injectable clock) ---

// TestClientTracker200_304Split verifies the per-client 200/304 counters
// are incremented correctly by record().
func TestClientTracker200_304Split(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	var now time.Time = base
	tr := &clientTracker{
		clients: make(map[string]*clientEntry),
		now:     func() time.Time { return now },
	}

	// First request: 200.
	tr.record("10.0.0.1", "", now, http.StatusOK, nil)
	// Second request: 304.
	now = now.Add(300 * time.Second)
	tr.record("10.0.0.1", "", now, http.StatusNotModified, nil)
	// Third request: 200.
	now = now.Add(300 * time.Second)
	tr.record("10.0.0.1", "", now, http.StatusOK, nil)

	st := tr.state("10.0.0.1")
	if st == nil {
		t.Fatal("state is nil")
	}
	if st.Count200 != 2 {
		t.Errorf("count_200 = %d, want 2", st.Count200)
	}
	if st.Count304 != 1 {
		t.Errorf("count_304 = %d, want 1", st.Count304)
	}
	if st.LastStatus != http.StatusOK {
		t.Errorf("last_status = %d, want 200", st.LastStatus)
	}
}

// TestClientTrackerIntervalFromTimestamps verifies that IntervalSec is the
// number of seconds between the last two requests, computed from real
// timestamps.
func TestClientTrackerIntervalFromTimestamps(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	var now time.Time = base
	tr := &clientTracker{
		clients: make(map[string]*clientEntry),
		now:     func() time.Time { return now },
	}

	tr.record("10.0.0.2", "", now, http.StatusOK, nil)
	now = now.Add(60 * time.Second)
	tr.record("10.0.0.2", "", now, http.StatusOK, nil)

	st := tr.state("10.0.0.2")
	if st == nil {
		t.Fatal("state is nil")
	}
	if st.IntervalSec != 60 {
		t.Errorf("interval_sec = %d, want 60", st.IntervalSec)
	}
}

// TestClientTrackerFirstRequestHasNoInterval verifies that the first request
// from a client produces an interval of 0 (no previous request to compare).
func TestClientTrackerFirstRequestHasNoInterval(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	tr := &clientTracker{
		clients: make(map[string]*clientEntry),
		now:     func() time.Time { return base },
	}

	tr.record("10.0.0.3", "", base, http.StatusOK, nil)
	st := tr.state("10.0.0.3")
	if st == nil {
		t.Fatal("state is nil")
	}
	if st.IntervalSec != 0 {
		t.Errorf("interval_sec = %d, want 0 on first request", st.IntervalSec)
	}
	if st.Count200 != 1 {
		t.Errorf("count_200 = %d, want 1", st.Count200)
	}
}

// TestClientTrackerUnknownClient verifies that a client with no requests
// returns nil state.
func TestClientTrackerUnknownClient(t *testing.T) {
	tr := newClientTracker(nil)
	if st := tr.state("nobody"); st != nil {
		t.Errorf("state = %+v, want nil", st)
	}
}

// --- HTTP integration tests ---

// TestDeviceEndpoint200 verifies that GET /v1/device returns the StickS3's
// device state with the correct status and counts. The /v1/usage request
// that seeds the tracker is made WITH the device User-Agent so it is tracked
// as the StickS3 device (ORDER #63). device_state is now served at its own
// endpoint (ORDER #66 task 67a) so it is never trapped inside an
// ETag-cached payload that is only present on 200 responses.
func TestDeviceEndpoint200(t *testing.T) {
	dir := setupFixtures(t)
	ts, _ := newFixtureServerWithCapture(t, dir)
	defer ts.Close()

	// Seed the tracker with a device request.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	req.Header.Set("User-Agent", "sticks3-usage/test1234")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// /v1/device must return the device state.
	devResp, err := http.Get(ts.URL + "/v1/device")
	if err != nil {
		t.Fatal(err)
	}
	defer devResp.Body.Close()
	if devResp.StatusCode != http.StatusOK {
		t.Fatalf("/v1/device status = %d, want 200", devResp.StatusCode)
	}

	var ds deviceState
	if err := json.NewDecoder(devResp.Body).Decode(&ds); err != nil {
		t.Fatalf("decode /v1/device: %v", err)
	}
	if ds.Count200 != 1 {
		t.Errorf("count_200 = %d, want 1", ds.Count200)
	}
	if ds.Count304 != 0 {
		t.Errorf("count_304 = %d, want 0", ds.Count304)
	}
	if ds.LastStatus != http.StatusOK {
		t.Errorf("last_status = %d, want 200", ds.LastStatus)
	}
	if ds.State != "connected" {
		t.Errorf("state = %q, want connected", ds.State)
	}
}

// TestDeviceEndpointNoETag verifies that GET /v1/device carries no ETag and
// no Cache-Control, so the browser always gets fresh device state (which
// changes every second: seconds_since). It must never be ETag-cached.
func TestDeviceEndpointNoETag(t *testing.T) {
	dir := setupFixtures(t)
	ts, _ := newFixtureServerWithCapture(t, dir)
	defer ts.Close()

	// Seed the tracker with a device request.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	req.Header.Set("User-Agent", "sticks3-usage/test1234")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	devResp, err := http.Get(ts.URL + "/v1/device")
	if err != nil {
		t.Fatal(err)
	}
	defer devResp.Body.Close()

	if etag := devResp.Header.Get("ETag"); etag != "" {
		t.Errorf("/v1/device ETag = %q, want absent (not cached)", etag)
	}
	if cc := devResp.Header.Get("Cache-Control"); cc != "" {
		t.Errorf("/v1/device Cache-Control = %q, want absent (not cached)", cc)
	}
}

// TestDeviceEndpointUnknownWhenNoDevice verifies that GET /v1/device returns
// {"state":"unknown"} (not an error) when no StickS3 client has ever checked
// in — "waiting" is a valid state, not a failure.
func TestDeviceEndpointUnknownWhenNoDevice(t *testing.T) {
	dir := setupFixtures(t)
	ts, _ := newFixtureServerWithCapture(t, dir)
	defer ts.Close()

	// Only a browser request — no device User-Agent.
	resp, err := http.Get(ts.URL + "/v1/device")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/v1/device status = %d, want 200", resp.StatusCode)
	}

	var ds struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ds); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ds.State != "unknown" {
		t.Errorf("state = %q, want unknown (no device has checked in)", ds.State)
	}
}

// TestDeviceEndpoint304Split verifies that the /v1/device endpoint reflects
// the correct 200/304 split on /v1/usage: a 200, a 304, then another 200
// should yield Count200=2, Count304=1 at /v1/device. The device UA is sent
// so the tracker sees the StickS3 (ORDER #63).
func TestDeviceEndpoint304Split(t *testing.T) {
	dir := setupFixtures(t)
	ts, _ := newFixtureServerWithCapture(t, dir)
	defer ts.Close()

	ua := "sticks3-usage/test1234"
	// First request → 200, device.
	req1, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	req1.Header.Set("User-Agent", ua)
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	etag := resp1.Header.Get("ETag")
	resp1.Body.Close()

	// Second request with matching If-None-Match → 304, device.
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	req2.Header.Set("User-Agent", ua)
	req2.Header.Set("If-None-Match", etag)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	if resp2.StatusCode != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", resp2.StatusCode)
	}
	resp2.Body.Close()

	// Third request with mismatched If-None-Match → 200, device.
	req3, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	req3.Header.Set("User-Agent", ua)
	req3.Header.Set("If-None-Match", `"deadbeef"`)
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp3.StatusCode)
	}
	resp3.Body.Close()

	// /v1/device should now show 200=2, 304=1.
	devResp, err := http.Get(ts.URL + "/v1/device")
	if err != nil {
		t.Fatal(err)
	}
	defer devResp.Body.Close()

	var ds deviceState
	if err := json.NewDecoder(devResp.Body).Decode(&ds); err != nil {
		t.Fatalf("decode /v1/device: %v", err)
	}
	if ds.Count200 != 2 {
		t.Errorf("count_200 = %d, want 2", ds.Count200)
	}
	if ds.Count304 != 1 {
		t.Errorf("count_304 = %d, want 1", ds.Count304)
	}
	if ds.LastStatus != http.StatusOK {
		t.Errorf("last_status = %d, want 200", ds.LastStatus)
	}
}

// TestDeviceEndpointOtaArmed verifies that /v1/device reports the DEVICE's own
// OTA armed state (from the ?ota_armed= query parameter), NOT the daemon's
// config (task 115).  A device armed via NVS seeding or BLE partial update must
// report armed=true regardless of whether this daemon provisioned it.
//
// A request with NO ?ota_armed= at all reports NOTHING (task 116): the field is
// omitted from the JSON rather than serialised as false, because "we have never
// been told" is not "disarmed" — a browser, a curl, or firmware older than task
// 115 all land here, and a bare false about an armed stick is the falsehood this
// whole pair of tasks exists to remove.
func TestDeviceEndpointOtaArmed(t *testing.T) {
	dir := setupFixtures(t)

	for _, tc := range []struct {
		name      string
		otaParam  string // value of ?ota_armed= in the /v1/usage request
		wantArmed *bool  // nil = the field must be ABSENT from the response
	}{
		{"armed", "1", boolPtr(true)},
		{"disarmed", "0", boolPtr(false)},
		{"missing_param_reports_nothing", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, _, _ := newFixtureHandlerCfg(t, dir, config.Config{
				Listen:      "127.0.0.1:0",
				Interval:    900 * time.Second,
				TZ:          testLoc,
				DeviceToken: "x",
			})
			ts := httptest.NewServer(handler)
			t.Cleanup(ts.Close)

			url := ts.URL + "/v1/usage"
			if tc.otaParam != "" {
				url += "?ota_armed=" + tc.otaParam
			}
			req, _ := http.NewRequest(http.MethodGet, url, nil)
			req.Header.Set("User-Agent", "sticks3-usage/test1234")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()

			devResp, err := http.Get(ts.URL + "/v1/device")
			if err != nil {
				t.Fatal(err)
			}
			defer devResp.Body.Close()

			body, err := io.ReadAll(devResp.Body)
			if err != nil {
				t.Fatal(err)
			}
			var ds deviceState
			if err := json.Unmarshal(body, &ds); err != nil {
				t.Fatalf("decode /v1/device: %v", err)
			}
			switch {
			case tc.wantArmed == nil:
				if ds.OtaArmed != nil {
					t.Errorf("ota_armed = %v, want absent (query param %q)", *ds.OtaArmed, tc.otaParam)
				}
				// Absent must mean ABSENT ON THE WIRE, not a JSON false: the
				// dashboard distinguishes the two, so the serialisation is the
				// contract, not just the Go value.
				if strings.Contains(string(body), "ota_armed") {
					t.Errorf("ota_armed present in JSON, want omitted: %s", body)
				}
			case ds.OtaArmed == nil:
				t.Errorf("ota_armed absent, want %v (query param %q)", *tc.wantArmed, tc.otaParam)
			case *ds.OtaArmed != *tc.wantArmed:
				t.Errorf("ota_armed = %v, want %v (query param %q)", *ds.OtaArmed, *tc.wantArmed, tc.otaParam)
			}
		})
	}
}

// TestDeviceEndpointFromDeviceNotBrowser verifies the ORDER #63 fix: when a
// browser with a device token but no device User-Agent requests /v1/usage
// (e.g. a loopback curl carrying the token), it must NOT be classified as the
// device.  Only the User-Agent "sticks3-usage/" marks a client as the device.
// The device and browser must come from DIFFERENT IPs — the device on its LAN
// address, the browser on loopback — so the browser's curl UA cannot overwrite
// the device's flag on a shared entry.
//
// device_state now lives at GET /v1/device (ORDER #66 task 67a), so we verify
// that endpoint reports the StickS3's state, NOT the browser's (which is the
// last request on /v1/usage but must never appear at /v1/device).
func TestDeviceEndpointFromDeviceNotBrowser(t *testing.T) {
	dir := setupFixtures(t)
	handler, _, _ := newFixtureHandler(t, dir)

	// Step 1: the StickS3 polls from its LAN IP with its device User-Agent.
	reqDev := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	reqDev.RemoteAddr = "192.168.0.136:1234"
	reqDev.Header.Set("User-Agent", "sticks3-usage/test1234")
	reqDev.Header.Set("X-Device-Token", "x")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, reqDev)
	if rec1.Code != http.StatusOK {
		t.Fatalf("device request status = %d, want 200", rec1.Code)
	}

	// Step 2: the browser opens the dashboard from loopback — no device UA.
	reqBrowser := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	reqBrowser.RemoteAddr = "127.0.0.1:12345"
	reqBrowser.Header.Set("X-Device-Token", "x")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, reqBrowser)
	if rec2.Code != http.StatusOK {
		t.Fatalf("browser request status = %d, want 200", rec2.Code)
	}

	// GET /v1/device must report the StickS3's state, not the browser's.
	devReq := httptest.NewRequest(http.MethodGet, "/v1/device", nil)
	devReq.RemoteAddr = "127.0.0.1:12345"
	devRec := httptest.NewRecorder()
	handler.ServeHTTP(devRec, devReq)
	if devRec.Code != http.StatusOK {
		t.Fatalf("/v1/device status = %d, want 200", devRec.Code)
	}

	var ds deviceState
	if err := json.Unmarshal(devRec.Body.Bytes(), &ds); err != nil {
		t.Fatalf("decode /v1/device: %v", err)
	}
	// The device_state must reflect the StickS3's single request (200),
	// NOT the browser's — they are distinct clients by IP+UA.
	if ds.Count200 != 1 {
		t.Errorf("count_200 = %d, want 1 (device made one; browser not counted as device)", ds.Count200)
	}
	if ds.State != "connected" {
		t.Errorf("state = %q, want %q (device reported, not browser)", ds.State, "connected")
	}
}

// TestConfigKeySourceEnv verifies that GET /v1/config returns key_source="env:VAR"
// when a provider's key is available via env var (ORDER #66 task 67c).
func TestConfigKeySourceEnv(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "127.0.0.1:0", Interval: 900 * time.Second,
		TZ: testLoc, DeviceToken: "x",
	}
	ks := creds.NewFakeKeyStore(nil)
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
	json.NewDecoder(rec.Body).Decode(&resp)
	provs := resp["providers"].([]any)
	orMain := provs[2].(map[string]any) // openrouter:main
	if orMain["key_source"] != "env:OPENROUTER_API_KEY" {
		t.Errorf("key_source = %v, want env:OPENROUTER_API_KEY", orMain["key_source"])
	}
}

// TestConfigKeySourceKeychain verifies that GET /v1/config returns
// key_source="keychain" when the key is in usaged's Keychain (not env, not OAuth).
func TestConfigKeySourceKeychain(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "127.0.0.1:0", Interval: 900 * time.Second,
		TZ: testLoc, DeviceToken: "x",
	}
	ks := creds.NewFakeKeyStore(map[string]string{"openrouter:main": "keychain-key"})
	handler := newHandlerWithKeyStore(t, dir, cfg, "", ks, WithGetenv(func(k string) string {
		return "" // no env var set
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	provs := resp["providers"].([]any)
	orMain := provs[2].(map[string]any)
	if orMain["key_source"] != "keychain" {
		t.Errorf("key_source = %v, want keychain", orMain["key_source"])
	}
	if orMain["key_state"] != "set" {
		t.Errorf("key_state = %v, want set", orMain["key_state"])
	}
}

// TestConfigKeySourceOAuth verifies that GET /v1/config returns the correct
// key_source for OAuth/self-hosted providers: "claude-code" for Claude and
// "codex" for Codex (ORDER #66 task 67c). These credentials live outside
// usaged's Keychain.
func TestConfigKeySourceOAuth(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen: "127.0.0.1:0", Interval: 900 * time.Second,
		TZ: testLoc, DeviceToken: "x",
	}
	ks := creds.NewFakeKeyStore(nil)
	handler := newHandlerWithKeyStore(t, dir, cfg, "", ks)

	req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var resp map[string]any
	json.NewDecoder(rec.Body).Decode(&resp)
	provs := resp["providers"].([]any)
	claude := provs[0].(map[string]any) // claude
	codex := provs[1].(map[string]any)  // codex

	// Claude's fixture returns a valid response, so the credential is present
	// via Claude Code's own Keychain entry — key_source must be "claude-code".
	if claude["key_source"] != "claude-code" {
		t.Errorf("claude key_source = %v, want claude-code", claude["key_source"])
	}
	if claude["key_state"] != "set" {
		t.Errorf("claude key_state = %v, want set", claude["key_state"])
	}

	// Codex's fixture is valid, so key_source must be "codex" (auth.json file).
	if codex["key_source"] != "codex" {
		t.Errorf("codex key_source = %v, want codex", codex["key_source"])
	}
	if codex["key_state"] != "set" {
		t.Errorf("codex key_state = %v, want set", codex["key_state"])
	}
}

// TestUsageDeviceStateNilWithoutDevice verifies that when only a browser
// (no device User-Agent) has made requests, device_state is absent — there is
// no StickS3 to report, even if the browser carries the device token.
// ORDER #63: a token-bearing loopback curl must NOT be reported as the device.
func TestUsageDeviceStateNilWithoutDevice(t *testing.T) {
	dir := setupFixtures(t)
	ts, _ := newFixtureServerWithCapture(t, dir)
	defer ts.Close()

	// Browser/curl request only — has the token but no device User-Agent.
	resp, err := http.Get(ts.URL + "/v1/usage")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := raw["device_state"]; ok {
		t.Fatal("device_state should be absent when no device UA client has been seen")
	}
}

// TestUsageDeviceStateCurlNotDevice verifies ORDER #63's core fix: a
// token-bearing loopback curl (which has the device token but a non-device
// User-Agent) must NOT be classified as the StickS3 device, so
// device_state is absent even though the token is valid.
func TestUsageDeviceStateCurlNotDevice(t *testing.T) {
	dir := setupFixtures(t)
	ts, _ := newFixtureServerWithCapture(t, dir)
	defer ts.Close()

	// A curl from loopback with the device token but a curl User-Agent.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	req.Header.Set("X-Device-Token", "x")
	req.Header.Set("User-Agent", "curl/8.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (loopback bypasses token check)", resp.StatusCode)
	}

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := raw["device_state"]; ok {
		t.Fatal("device_state should be absent: curl UA is not the StickS3")
	}
}

// TestUsageRevUnaffectedByDeviceState verifies that device_state being moved
// to /v1/device does not change the ETag/rev on /v1/usage, so 304 semantics
// are intact.
func TestUsageRevUnaffectedByDeviceState(t *testing.T) {
	dir := setupFixtures(t)
	ts, _ := newFixtureServerWithCapture(t, dir)
	defer ts.Close()

	resp1, err := http.Get(ts.URL + "/v1/usage")
	if err != nil {
		t.Fatalf("GET /v1/usage: %v", err)
	}
	defer resp1.Body.Close()
	etag1 := resp1.Header.Get("ETag")

	if etag1 == "" {
		t.Fatal("no ETag on first request")
	}

	// Second request with matching If-None-Match → 304 (rev unchanged).
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	req2.Header.Set("If-None-Match", etag1)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusNotModified {
		t.Errorf("status = %d, want 304 (rev must be unchanged)", resp2.StatusCode)
	}
	if et2 := resp2.Header.Get("ETag"); et2 != etag1 {
		t.Errorf("304 ETag = %q, want %q (rev must not change with device_state)", et2, etag1)
	}
	body, _ := io.ReadAll(resp2.Body)
	if len(body) != 0 {
		t.Errorf("304 body = %d bytes, want 0 (no device_state in 304)", len(body))
	}
}

// TestUsageAgeField verifies the ORDER #65 age field: on a 200 it is present
// and positive (server-computed seconds since checked_at), and on a 304 there
// is no body (so no age).  Age is outside the Snapshot hash — it does not
// affect the ETag/rev.
func TestUsageAgeField(t *testing.T) {
	dir := setupFixtures(t)
	ts, _ := newFixtureServerWithCapture(t, dir)
	defer ts.Close()

	// 200 response: age must be present and >= 0.
	resp1, err := http.Get(ts.URL + "/v1/usage")
	if err != nil {
		t.Fatalf("GET /v1/usage: %v", err)
	}
	defer resp1.Body.Close()

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp1.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ageRaw, ok := raw["age"]
	if !ok {
		t.Fatal("age field missing from 200 response")
	}
	var age uint32
	if err := json.Unmarshal(ageRaw, &age); err != nil {
		t.Fatalf("unmarshal age: %v", err)
	}
	// age is unsigned; it must parse as a non-negative integer.
	_ = age

	etag1 := resp1.Header.Get("ETag")
	if etag1 == "" {
		t.Fatal("no ETag on first request")
	}

	// 304 response: no body, no age field.
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	req2.Header.Set("If-None-Match", etag1)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode != http.StatusNotModified {
		t.Errorf("status = %d, want 304", resp2.StatusCode)
	}
	if len(body2) != 0 {
		t.Errorf("304 body = %d bytes, want 0 (no age in 304)", len(body2))
	}
}

// TestAccessLogNoToken verifies that the device token never appears in the
// slog access-log output for a /v1/usage request.
func TestAccessLogNoToken(t *testing.T) {
	dir := setupFixtures(t)
	ts, logBuf := newFixtureServerWithCapture(t, dir)
	defer ts.Close()

	token := "super-secret-token-12345"
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	req.Header.Set("X-Device-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	logOutput := logBuf.String()
	if strings.Contains(logOutput, token) {
		t.Errorf("token value found in access log:\n%s", logOutput)
	}
	// Verify the access log entry IS present (status was logged).
	if !strings.Contains(logOutput, "/v1/usage") {
		t.Errorf("access log entry not found in log output:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, `"status":200`) {
		t.Errorf("status 200 not found in log output:\n%s", logOutput)
	}
}

// TestAccessLogUserAgent verifies ORDER #65: the User-Agent header value is
// included in the slog access-log line so isDevice classification can be
// debugged on the wire (the Arduino HTTPClient core silently drops
// addHeader("User-Agent"), so the log is the only proof the firmware sent it).
func TestAccessLogUserAgent(t *testing.T) {
	dir := setupFixtures(t)
	ts, logBuf := newFixtureServerWithCapture(t, dir)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	req.Header.Set("User-Agent", "sticks3-usage/abc1234")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "sticks3-usage/abc1234") {
		t.Errorf("user_agent not found in access log:\n%s", logOutput)
	}
}

// TestAccessLogOtaArmed verifies that the access log records the device's OTA
// state as a TRI-STATE (task 116): "armed", "disarmed", or "unknown" when the
// request carried no ?ota_armed= at all.  It used to log a bare boolean, so a
// browser — or firmware older than task 115 — produced "ota_armed":false, which
// reads as a measurement of the device when it is in fact the absence of one.
func TestAccessLogOtaArmed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		want  string
	}{
		{"no_param_is_unknown", "", `"ota_armed":"unknown"`},
		{"armed", "?ota_armed=1", `"ota_armed":"armed"`},
		{"disarmed", "?ota_armed=0", `"ota_armed":"disarmed"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := setupFixtures(t)
			ts, logBuf := newFixtureServerWithCapture(t, dir)
			defer ts.Close()

			req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage"+tc.query, nil)
			req.Header.Set("User-Agent", "sticks3-usage/abc1234")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()

			logOutput := logBuf.String()
			if !strings.Contains(logOutput, tc.want) {
				t.Errorf("%s not found in access log:\n%s", tc.want, logOutput)
			}
			if strings.Contains(logOutput, `"ota_armed":false`) {
				t.Errorf("access log still writes a bare boolean:\n%s", logOutput)
			}
		})
	}
}

// boolPtr returns a pointer to b, for the tri-state OTA expectations above.
func boolPtr(b bool) *bool { return &b }

// TestAccessLogDifferentClients verifies that requests from different client
// IPs are tracked independently.
func TestAccessLogDifferentClients(t *testing.T) {
	tr := &clientTracker{
		clients: make(map[string]*clientEntry),
		now:     func() time.Time { return time.Now() },
	}

	tr.record("10.0.0.1", "", tr.now(), http.StatusOK, nil)
	tr.record("10.0.0.2", "", tr.now(), http.StatusNotModified, nil)

	st1 := tr.state("10.0.0.1")
	st2 := tr.state("10.0.0.2")
	if st1 == nil || st2 == nil {
		t.Fatal("missing client state")
	}
	if st1.Count200 != 1 || st1.Count304 != 0 {
		t.Errorf("client 1: count_200=%d count_304=%d, want 1/0", st1.Count200, st1.Count304)
	}
	if st2.Count200 != 0 || st2.Count304 != 1 {
		t.Errorf("client 2: count_200=%d count_304=%d, want 0/1", st2.Count200, st2.Count304)
	}
}

// TestClientTrackerDeviceStateReturnsDevice verifies that deviceState() returns
// the state of the StickS3 (the client identified by User-Agent prefix
// "sticks3-usage/"), NOT a browser or token-bearing loopback curl — even when
// the browser made its request most recently.  This is the ORDER #63 fix.
func TestClientTrackerDeviceStateReturnsDevice(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	var now time.Time = base
	tr := &clientTracker{
		clients: make(map[string]*clientEntry),
		now:     func() time.Time { return now },
	}

	// StickS3 polls with its identifying User-Agent.
	tr.record("10.0.0.50", "sticks3-usage/abcd1234", now, http.StatusOK, nil)
	// 300s later, the browser opens the dashboard (no device UA).
	now = now.Add(300 * time.Second)
	tr.record("127.0.0.1", "Mozilla/5.0", now, http.StatusOK, nil)

	// The browser's own state is trivially "connected" (just requested).
	browserState := tr.state("127.0.0.1")
	if browserState == nil {
		t.Fatal("browser state is nil")
	}

	// deviceState() must return the DEVICE's entry, not the browser's.
	dev := tr.deviceState()
	if dev == nil {
		t.Fatal("deviceState is nil — expected the StickS3's state")
	}
	if dev.ClientAddr != "10.0.0.50" {
		t.Errorf("deviceState addr = %q, want %q (the StickS3, not the browser)", dev.ClientAddr, "10.0.0.50")
	}
	if dev.Count200 != 1 || dev.Count304 != 0 {
		t.Errorf("deviceState counts: 200=%d 304=%d, want 1/0", dev.Count200, dev.Count304)
	}
}

// TestClientTrackerDeviceStateNilWithoutDevice verifies that deviceState()
// returns nil when only loopback clients (browsers) have ever made requests.
func TestClientTrackerDeviceStateNilWithoutDevice(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	tr := &clientTracker{
		clients: make(map[string]*clientEntry),
		now:     func() time.Time { return base },
	}

	// Only a loopback browser — no device UA.
	tr.record("127.0.0.1", "Mozilla/5.0", base, http.StatusOK, nil)

	if dev := tr.deviceState(); dev != nil {
		t.Errorf("deviceState = %+v, want nil (no device client seen)", dev)
	}
}

// TestClientTrackerIsDeviceNotLatched verifies ORDER #65: isDevice is
// re-evaluated from the CURRENT User-Agent on every request, not latched
// from a prior request.  A client that was once the device (UA
// "sticks3-usage/...") but later sends a curl UA must NOT remain classified
// as the device — this prevents a stale entry from poisoning deviceState().
func TestClientTrackerIsDeviceNotLatched(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	tr := &clientTracker{
		clients: make(map[string]*clientEntry),
		now:     func() time.Time { return base },
	}

	// First request: the StickS3 identifies itself.
	tr.record("10.0.0.50", "sticks3-usage/abcd1234", base, http.StatusOK, nil)
	if tr.deviceState() == nil {
		t.Fatal("expected deviceState to return the StickS3 after first request")
	}

	// Later request from the SAME IP with a different UA (e.g. a debug curl).
	tr.record("10.0.0.50", "curl/8.0", base.Add(300*time.Second), http.StatusOK, nil)

	// The IP is the same, but the UA is no longer the device's — deviceState
	// must be nil because isDevice is re-evaluated, not latched.
	if dev := tr.deviceState(); dev != nil {
		t.Errorf("deviceState = %+v, want nil (isDevice must not latch)", dev)
	}
}

// --- computeDeviceState unit tests ---

// TestComputeDeviceStateConnected verifies that a client within the expected
// polling window is reported as "connected".
func TestComputeDeviceStateConnected(t *testing.T) {
	// interval 300s, last seen 120s ago → well within 3×300=900s.
	if got := computeDeviceState(300, 120); got != "connected" {
		t.Errorf("computeDeviceState(300, 120) = %q, want %q", got, "connected")
	}
}

// TestComputeDeviceStateAbsent verifies that a client absent beyond 3× the
// observed interval is reported as "absent".
func TestComputeDeviceStateAbsent(t *testing.T) {
	// interval 300s, last seen 1000s ago → exceeds 3×300=900.
	if got := computeDeviceState(300, 1000); got != "absent" {
		t.Errorf("computeDeviceState(300, 1000) = %q, want %q", got, "absent")
	}
}

// TestComputeDeviceStateBoundary verifies the boundary: exactly 3× the
// interval is still "connected", 3×+1 is "absent".
func TestComputeDeviceStateBoundary(t *testing.T) {
	if got := computeDeviceState(300, 900); got != "connected" {
		t.Errorf("300, 900 = %q, want connected", got)
	}
	if got := computeDeviceState(300, 901); got != "absent" {
		t.Errorf("300, 901 = %q, want absent", got)
	}
}

// TestComputeDeviceStateFallbackInterval verifies that when no interval has
// been observed yet (first request), the 600s default is used.
func TestComputeDeviceStateFallbackInterval(t *testing.T) {
	// No interval observed, last seen 60s ago → connected.
	if got := computeDeviceState(0, 60); got != "connected" {
		t.Errorf("computeDeviceState(0, 60) = %q, want %q", got, "connected")
	}
	// No interval observed, last seen 1801s ago → exceeds 3×600=1800 → absent.
	if got := computeDeviceState(0, 1801); got != "absent" {
		t.Errorf("computeDeviceState(0, 1801) = %q, want %q", got, "absent")
	}
}

// TestComputeDeviceStateJustSeen verifies that a freshly-seen client is
// always "connected" regardless of the expected interval.
func TestComputeDeviceStateJustSeen(t *testing.T) {
	if got := computeDeviceState(300, 0); got != "connected" {
		t.Errorf("computeDeviceState(300, 0) = %q, want %q", got, "connected")
	}
}

// TestAccessLogAgeS verifies that age_s query parameter is logged when
// present, and that its presence/absence does not affect the ETag/304 path.
// ORDER #65: the firmware sends its RTC-computed effective age as ?age_s=<n>
// on /v1/usage so the server can prove deep sleep occurred by the gap in the
// numbers.
func TestAccessLogAgeS(t *testing.T) {
	dir := setupFixtures(t)
	ts, logBuf := newFixtureServerWithCapture(t, dir)
	defer ts.Close()

	ua := "sticks3-usage/test1234"

	// Request with age_s=42.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage?age_s=42", nil)
	req.Header.Set("User-Agent", ua)
	req.Header.Set("X-Device-Token", "x")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	etag := resp.Header.Get("ETag")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Verify age_s appears in the access log.
	logLine := logBuf.String()
	if !strings.Contains(logLine, `"age_s":"42"`) {
		t.Errorf("access log missing age_s=42; got: %s", logLine)
	}
	if !strings.Contains(logLine, `"user_agent":"`+ua+`"`) {
		t.Errorf("access log missing user_agent; got: %s", logLine)
	}

	// Now send a 304 with age_s — must still log age_s and stay 304.
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage?age_s=132", nil)
	req2.Header.Set("User-Agent", ua)
	req2.Header.Set("If-None-Match", etag)
	req2.Header.Set("X-Device-Token", "x")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotModified {
		t.Fatalf("304 request status = %d, want 304", resp2.StatusCode)
	}

	// Verify the 304 access log also has age_s=132.
	logLine2 := logBuf.String()
	if !strings.Contains(logLine2, `"age_s":"132"`) {
		t.Errorf("access log missing age_s=132 on 304; got: %s", logLine2)
	}
}

// TestAccessLogNoAgeS verifies that when age_s is absent the access log does
// not contain the age_s field (it is only logged when present).
func TestAccessLogNoAgeS(t *testing.T) {
	dir := setupFixtures(t)
	ts, logBuf := newFixtureServerWithCapture(t, dir)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	req.Header.Set("User-Agent", "curl/8.7.1")
	req.Header.Set("X-Device-Token", "x")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	logLine := logBuf.String()
	if strings.Contains(logLine, `"age_s"`) {
		t.Errorf("access log should not contain age_s when absent; got: %s", logLine)
	}
}

// TestLogAccessWithAgeRecordsServerAgeAndDrift locks the contract that made the
// freshness pipeline verifiable: the access line must carry BOTH the device's
// claimed age and the server's authoritative age for the same instant, plus
// their difference. Two false "offset" defects were filed on 2026-09-06 because
// the baseline had to be reconstructed by hand; drift_s removes that step.
func TestLogAccessWithAgeRecordsServerAgeAndDrift(t *testing.T) {
	var buf bytes.Buffer
	srv := &Server{logger: slog.New(slog.NewJSONHandler(&buf, nil))}
	req := httptest.NewRequest(http.MethodGet, "/v1/usage?age_s=608", nil)
	req.RemoteAddr = "192.168.0.136:50000"
	req.Header.Set("User-Agent", "sticks3-usage/abc1234")

	srv.logAccessWithAge(req, http.StatusNotModified, "608", 600)

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("log line is not JSON: %v", err)
	}
	if got["age_s"] != "608" {
		t.Errorf("age_s = %v, want \"608\"", got["age_s"])
	}
	if got["server_age_s"] != float64(600) {
		t.Errorf("server_age_s = %v, want 600", got["server_age_s"])
	}
	if got["drift_s"] != float64(8) {
		t.Errorf("drift_s = %v, want 8 (608-600)", got["drift_s"])
	}
	// The token must never reach a log line, on any path.
	if bytes.Contains(buf.Bytes(), []byte("X-Device-Token")) {
		t.Error("access log must never carry the device token")
	}
}

// A device that sends a malformed age must still be logged, without a bogus
// drift — silently dropping the line would hide a misbehaving device.
func TestLogAccessWithAgeMalformedAgeHasNoDrift(t *testing.T) {
	var buf bytes.Buffer
	srv := &Server{logger: slog.New(slog.NewJSONHandler(&buf, nil))}
	req := httptest.NewRequest(http.MethodGet, "/v1/usage?age_s=nonsense", nil)
	req.RemoteAddr = "192.168.0.136:50000"

	srv.logAccessWithAge(req, http.StatusOK, "nonsense", 42)

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("log line is not JSON: %v", err)
	}
	if got["age_s"] != "nonsense" {
		t.Errorf("age_s = %v, want the raw value preserved", got["age_s"])
	}
	if _, ok := got["drift_s"]; ok {
		t.Error("drift_s must be absent when the device age does not parse")
	}
}
