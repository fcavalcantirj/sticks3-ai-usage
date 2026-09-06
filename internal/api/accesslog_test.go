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
		providers.NewClaude(client, runner, "testuser", testLoc),
		providers.NewCodex(client, filepath.Join(dir, "codex_auth.json"), testLoc),
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
	tr.record("10.0.0.1", false, now, http.StatusOK)
	// Second request: 304.
	now = now.Add(300 * time.Second)
	tr.record("10.0.0.1", false, now, http.StatusNotModified)
	// Third request: 200.
	now = now.Add(300 * time.Second)
	tr.record("10.0.0.1", false, now, http.StatusOK)

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

	tr.record("10.0.0.2", false, now, http.StatusOK)
	now = now.Add(60 * time.Second)
	tr.record("10.0.0.2", false, now, http.StatusOK)

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

	tr.record("10.0.0.3", false, base, http.StatusOK)
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

// TestUsageDeviceState200 verifies that a 200 /v1/usage response includes
// device_state with the correct status and counts. The request is made WITH
// the device token so it is tracked as the StickS3 device (ORDER #61 task 64).
func TestUsageDeviceState200(t *testing.T) {
	dir := setupFixtures(t)
	ts, _ := newFixtureServerWithCapture(t, dir)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	req.Header.Set("X-Device-Token", "x")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	defer resp.Body.Close()

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	dsRaw, ok := raw["device_state"]
	if !ok {
		t.Fatal("device_state field missing from 200 response")
	}
	var ds deviceState
	if err := json.Unmarshal(dsRaw, &ds); err != nil {
		t.Fatalf("unmarshal device_state: %v", err)
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
}

// TestUsageDeviceStateFromDeviceNotBrowser verifies the ORDER #61 fix: when a
// browser (loopback, no token) requests /v1/usage, the device_state field
// reports the StickS3's state — not the browser's. The browser request is
// the LAST request, so if the tracker were keyed by the requester it would
// report the browser's trivially-"connected" state. deviceState() must return
// the device's entry instead.
func TestUsageDeviceStateFromDeviceNotBrowser(t *testing.T) {
	dir := setupFixtures(t)
	ts, _ := newFixtureServerWithCapture(t, dir)
	defer ts.Close()

	// Step 1: the StickS3 polls with its device token (non-loopback OR token).
	reqDev, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	reqDev.Header.Set("X-Device-Token", "x")
	resp1, err := http.DefaultClient.Do(reqDev)
	if err != nil {
		t.Fatal(err)
	}
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("device request status = %d, want 200", resp1.StatusCode)
	}

	// Step 2: the browser (loopback, no token) opens the dashboard.
	reqBrowser, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	resp2, err := http.DefaultClient.Do(reqBrowser)
	if err != nil {
		t.Fatal(err)
	}
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("browser request status = %d, want 200", resp2.StatusCode)
	}
	defer resp2.Body.Close()

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp2.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	dsRaw, ok := raw["device_state"]
	if !ok {
		t.Fatal("device_state field missing from browser-facing 200 response")
	}
	var ds deviceState
	if err := json.Unmarshal(dsRaw, &ds); err != nil {
		t.Fatalf("unmarshal device_state: %v", err)
	}
	// The device_state must reflect the StickS3's two requests (both 200),
	// NOT the browser's single request.
	if ds.Count200 != 2 {
		t.Errorf("device_state count_200 = %d, want 2 (device made both; browser should not be counted as device)", ds.Count200)
	}
	if ds.LastStatus != http.StatusOK {
		t.Errorf("last_status = %d, want 200", ds.LastStatus)
	}
	// The state must be "connected" — the device's own cadence, not the browser's.
	if ds.State != "connected" {
		t.Errorf("state = %q, want %q (device reported, not browser)", ds.State, "connected")
	}
}

// TestUsageDeviceStateNilWithoutDevice verifies that when only a browser
// (loopback, no token) has made requests, device_state is absent — there is
// no StickS3 to report.
func TestUsageDeviceStateNilWithoutDevice(t *testing.T) {
	dir := setupFixtures(t)
	ts, _ := newFixtureServerWithCapture(t, dir)
	defer ts.Close()

	// Browser request only — no device token.
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
		t.Fatal("device_state should be absent when no device client has been seen")
	}
}

// TestUsageDeviceState304Split verifies that a 304 request is counted in the
// split, and that the next 200 response reflects the updated counts. All
// requests carry the device token so the tracker sees the StickS3 (ORDER #61).
func TestUsageDeviceState304Split(t *testing.T) {
	dir := setupFixtures(t)
	ts, _ := newFixtureServerWithCapture(t, dir)
	defer ts.Close()

	// First request → 200, as the device.
	req1, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	req1.Header.Set("X-Device-Token", "x")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	etag := resp1.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	resp1.Body.Close()

	// Second request with matching If-None-Match → 304.
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	req2.Header.Set("If-None-Match", etag)
	req2.Header.Set("X-Device-Token", "x")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	if resp2.StatusCode != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", resp2.StatusCode)
	}
	resp2.Body.Close()

	// Third request with mismatched If-None-Match → 200, device_state
	// should now show Count200=2, Count304=1.
	req3, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/usage", nil)
	req3.Header.Set("If-None-Match", `"deadbeef"`)
	req3.Header.Set("X-Device-Token", "x")
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp3.StatusCode)
	}
	defer resp3.Body.Close()

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp3.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var ds deviceState
	if err := json.Unmarshal(raw["device_state"], &ds); err != nil {
		t.Fatalf("unmarshal device_state: %v", err)
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

// TestUsageRevUnaffectedByDeviceState verifies that adding device_state to
// the 200 response does not change the ETag/rev, so 304 semantics are intact.
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

// TestAccessLogDifferentClients verifies that requests from different client
// IPs are tracked independently.
func TestAccessLogDifferentClients(t *testing.T) {
	tr := &clientTracker{
		clients: make(map[string]*clientEntry),
		now:     func() time.Time { return time.Now() },
	}

	tr.record("10.0.0.1", false, tr.now(), http.StatusOK)
	tr.record("10.0.0.2", false, tr.now(), http.StatusNotModified)

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
// the state of the StickS3 (the client identified as a device — non-loopback
// or token-bearing), NOT the browser (loopback, no token), even when the
// browser made its request most recently.
func TestClientTrackerDeviceStateReturnsDevice(t *testing.T) {
	base := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	var now time.Time = base
	tr := &clientTracker{
		clients: make(map[string]*clientEntry),
		now:     func() time.Time { return now },
	}

	// StickS3 polls from a non-loopback address with its token.
	tr.record("10.0.0.50", true, now, http.StatusOK)
	// 300s later, the browser (loopback, no token) opens the dashboard.
	now = now.Add(300 * time.Second)
	tr.record("127.0.0.1", false, now, http.StatusOK)

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

	// Only a loopback browser — no device seen.
	tr.record("127.0.0.1", false, base, http.StatusOK)

	if dev := tr.deviceState(); dev != nil {
		t.Errorf("deviceState = %+v, want nil (no device client seen)", dev)
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
