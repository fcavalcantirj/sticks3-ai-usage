package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"usaged/internal/config"
)

// --- Pairing test rig (task 78) ---

const (
	pairDashToken = "dash-token"
	pairDeviceIP  = "192.168.0.136:5000"
	pairOtherIP   = "192.168.0.99:5000"
	pairPublicIP  = "8.8.8.8:5000"
	pairLoopback  = "127.0.0.1:5000"
)

// pairRig wires the real pairing handlers behind the real auth middleware with
// an injected clock and a capturable log, so window expiry and "the token never
// reaches a log line" can both be exercised without a scheduler.
type pairRig struct {
	handler http.Handler
	pairing *pairing
	logs    *bytes.Buffer
	dir     string
	now     time.Time
}

func newPairRig(t *testing.T) *pairRig {
	t.Helper()
	rig := &pairRig{
		logs: &bytes.Buffer{},
		dir:  t.TempDir(),
		now:  fixedNow,
	}
	logger := slog.New(slog.NewJSONHandler(rig.logs, nil))
	rig.pairing = newPairing(filepath.Join(rig.dir, "devices.json"), func() time.Time { return rig.now }, logger)

	mux := http.NewServeMux()
	rig.pairing.routes(mux)
	cfg := config.Config{Listen: "127.0.0.1:0", DeviceToken: pairDashToken}
	rig.handler = newAuth(cfg, rig.pairing, logger).middleware(mux)
	return rig
}

// post issues a POST with a JSON body from remote, optionally with a token.
func (rig *pairRig) post(path, body, remote, token string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(http.MethodPost, path, nil)
	} else {
		req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = remote
	if token != "" {
		req.Header.Set("X-Device-Token", token)
	}
	rec := httptest.NewRecorder()
	rig.handler.ServeHTTP(rec, req)
	return rec
}

func (rig *pairRig) get(path, remote, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remote
	if token != "" {
		req.Header.Set("X-Device-Token", token)
	}
	rec := httptest.NewRecorder()
	rig.handler.ServeHTTP(rec, req)
	return rec
}

// open opens a pairing window from the dashboard.
func (rig *pairRig) open(t *testing.T) {
	t.Helper()
	rec := rig.post(pairOpenPath, `{}`, pairLoopback, pairDashToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("open window: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// claim posts a device claim and returns the decoded body.
func (rig *pairRig) claim(t *testing.T, body, remote string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := rig.post(pairClaimPath, body, remote, "")
	var m map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("claim: decode body %q: %v", rec.Body.String(), err)
		}
	}
	return rec, m
}

// pairOnce runs the whole flow and returns the code and the issued token.
func (rig *pairRig) pairOnce(t *testing.T) (code, token string) {
	t.Helper()
	rig.open(t)

	rec, body := rig.claim(t, `{"device_id":"aabbccddeeff","name":"sticks3"}`, pairDeviceIP)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("claim: status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	code, _ = body["code"].(string)
	if !validPairCode(code) {
		t.Fatalf("claim returned code %q, not a valid pairing code", code)
	}

	conf := rig.post(pairConfirmPath, `{"code":"`+code+`"}`, pairLoopback, pairDashToken)
	if conf.Code != http.StatusOK {
		t.Fatalf("confirm: status = %d, want 200 (body %s)", conf.Code, conf.Body.String())
	}

	rec, body = rig.claim(t, `{"device_id":"aabbccddeeff"}`, pairDeviceIP)
	if rec.Code != http.StatusOK {
		t.Fatalf("token pickup: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	token, _ = body["token"].(string)
	if len(token) != pairTokenBytes*2 {
		t.Fatalf("issued token has length %d, want %d hex chars", len(token), pairTokenBytes*2)
	}
	return code, token
}

// --- The flow ---

func TestPairingSuccessfulClaim(t *testing.T) {
	rig := newPairRig(t)
	_, token := rig.pairOnce(t)

	if !rig.pairing.matchToken(token) {
		t.Error("issued token is not accepted by matchToken")
	}
	if rig.pairing.state != pairStateIdle {
		t.Errorf("state after pairing = %q, want idle (single-use window)", rig.pairing.state)
	}

	// The device record must be on disk before the token leaves, or a restart
	// would leave the device holding a credential nothing accepts.
	path := filepath.Join(rig.dir, "devices.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("devices.json not written: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("devices.json mode = %v, want 0600", info.Mode().Perm())
	}

	// A second agent process reading the same file must accept the token.
	reloaded := newPairing(path, func() time.Time { return rig.now }, slog.New(slog.NewJSONHandler(rig.logs, nil)))
	if !reloaded.matchToken(token) {
		t.Error("issued token is not accepted after reloading devices.json")
	}
}

func TestPairingClaimRefusedWithoutWindow(t *testing.T) {
	rig := newPairRig(t)

	rec, body := rig.claim(t, `{"device_id":"aabbccddeeff"}`, pairDeviceIP)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("claim with no window: status = %d, want 403", rec.Code)
	}
	if s, _ := body["state"].(string); s != string(pairStateIdle) {
		t.Errorf("state = %q, want idle", s)
	}
	if !strings.Contains(rec.Body.String(), "no pairing window is open") {
		t.Errorf("body = %q, want the open-a-window hint", rec.Body.String())
	}
}

func TestPairingExpiredCode(t *testing.T) {
	rig := newPairRig(t)
	rig.open(t)

	rec, body := rig.claim(t, `{"device_id":"aabbccddeeff"}`, pairDeviceIP)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("claim: status = %d, want 202", rec.Code)
	}
	code, _ := body["code"].(string)

	// One second past the window: the code is gone, and confirming it is
	// indistinguishable from never having opened a window.
	rig.now = rig.now.Add(pairWindowTTL + time.Second)

	conf := rig.post(pairConfirmPath, `{"code":"`+code+`"}`, pairLoopback, pairDashToken)
	if conf.Code != http.StatusConflict {
		t.Errorf("confirm after expiry: status = %d, want 409 (body %s)", conf.Code, conf.Body.String())
	}
	rec, _ = rig.claim(t, `{"device_id":"aabbccddeeff"}`, pairDeviceIP)
	if rec.Code != http.StatusForbidden {
		t.Errorf("claim after expiry: status = %d, want 403", rec.Code)
	}
}

func TestPairingApprovedTokenExpires(t *testing.T) {
	rig := newPairRig(t)
	rig.open(t)

	_, body := rig.claim(t, `{"device_id":"aabbccddeeff"}`, pairDeviceIP)
	code, _ := body["code"].(string)
	if conf := rig.post(pairConfirmPath, `{"code":"`+code+`"}`, pairLoopback, pairDashToken); conf.Code != http.StatusOK {
		t.Fatalf("confirm: status = %d, want 200", conf.Code)
	}

	// A device that never comes back for its token must not be able to collect
	// it later: the pickup grace expires too.
	rig.now = rig.now.Add(pairPickupTTL + time.Second)
	rec, _ := rig.claim(t, `{"device_id":"aabbccddeeff"}`, pairDeviceIP)
	if rec.Code != http.StatusForbidden {
		t.Errorf("pickup after grace: status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	if len(rig.pairing.devices) != 0 {
		t.Errorf("devices = %d, want 0 — an uncollected token must never be recorded", len(rig.pairing.devices))
	}
}

func TestPairingReusedCode(t *testing.T) {
	rig := newPairRig(t)
	code, token := rig.pairOnce(t)

	// The window closed on the first hand-off, so the same code buys nothing.
	conf := rig.post(pairConfirmPath, `{"code":"`+code+`"}`, pairLoopback, pairDashToken)
	if conf.Code != http.StatusConflict {
		t.Errorf("re-confirm: status = %d, want 409 (body %s)", conf.Code, conf.Body.String())
	}
	rec, replay := rig.claim(t, `{"device_id":"aabbccddeeff"}`, pairDeviceIP)
	if rec.Code != http.StatusForbidden {
		t.Errorf("replayed claim: status = %d, want 403", rec.Code)
	}
	if got, _ := replay["token"].(string); got != "" {
		t.Error("a replayed claim returned a token")
	}
	if !rig.pairing.matchToken(token) {
		t.Error("the already-issued token stopped working")
	}
}

func TestPairingWrongCode(t *testing.T) {
	rig := newPairRig(t)
	rig.open(t)

	_, body := rig.claim(t, `{"device_id":"aabbccddeeff"}`, pairDeviceIP)
	code, _ := body["code"].(string)

	wrong := "AAAAAA"
	if wrong == code {
		wrong = "BBBBBB"
	}
	rec := rig.post(pairConfirmPath, `{"code":"`+wrong+`"}`, pairLoopback, pairDashToken)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong code: status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if left, _ := m["attempts_left"].(float64); int(left) != pairMaxWrongCodes-1 {
		t.Errorf("attempts_left = %v, want %d", m["attempts_left"], pairMaxWrongCodes-1)
	}

	// The window survives a typo: the right code still works.
	if conf := rig.post(pairConfirmPath, `{"code":"`+code+`"}`, pairLoopback, pairDashToken); conf.Code != http.StatusOK {
		t.Errorf("correct code after a typo: status = %d, want 200", conf.Code)
	}
}

func TestPairingWrongCodesCloseWindow(t *testing.T) {
	rig := newPairRig(t)
	rig.open(t)

	_, body := rig.claim(t, `{"device_id":"aabbccddeeff"}`, pairDeviceIP)
	code, _ := body["code"].(string)

	var last *httptest.ResponseRecorder
	for i := 0; i < pairMaxWrongCodes; i++ {
		last = rig.post(pairConfirmPath, `{"code":"ZZZZZZ"}`, pairLoopback, pairDashToken)
	}
	if last.Code != http.StatusForbidden {
		t.Fatalf("last wrong code: status = %d, want 403 (body %s)", last.Code, last.Body.String())
	}
	if rig.pairing.state != pairStateIdle {
		t.Errorf("state = %q, want idle after %d wrong codes", rig.pairing.state, pairMaxWrongCodes)
	}
	// With the window closed, even the right code is worthless.
	if conf := rig.post(pairConfirmPath, `{"code":"`+code+`"}`, pairLoopback, pairDashToken); conf.Code == http.StatusOK {
		t.Error("the correct code still worked after the window was closed")
	}
}

// --- Rate limiting ---

func TestPairingConfirmRateLimit(t *testing.T) {
	rig := newPairRig(t)
	rig.open(t)
	rig.claim(t, `{"device_id":"aabbccddeeff"}`, pairDeviceIP)

	for i := 0; i < pairConfirmsPerMinute; i++ {
		if rec := rig.post(pairConfirmPath, `{"code":"ZZZZZZ"}`, pairLoopback, pairDashToken); rec.Code == http.StatusTooManyRequests {
			t.Fatalf("confirm %d was rate-limited too early", i+1)
		}
	}
	rec := rig.post(pairConfirmPath, `{"code":"ZZZZZZ"}`, pairLoopback, pairDashToken)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("confirm %d: status = %d, want 429", pairConfirmsPerMinute+1, rec.Code)
	}
}

func TestPairingClaimRateLimit(t *testing.T) {
	rig := newPairRig(t)
	rig.open(t)

	for i := 0; i < pairClaimsPerMinute; i++ {
		rec := rig.post(pairClaimPath, `{"device_id":"aabbccddeeff"}`, pairDeviceIP, "")
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("claim %d was rate-limited too early", i+1)
		}
	}
	rec := rig.post(pairClaimPath, `{"device_id":"aabbccddeeff"}`, pairDeviceIP, "")
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("claim %d: status = %d, want 429", pairClaimsPerMinute+1, rec.Code)
	}
}

// --- The two auth models ---

func TestPairingClaimRefusedOffLAN(t *testing.T) {
	rig := newPairRig(t)
	rig.open(t)

	// A public address is never a StickS3 on this network...
	rec := rig.post(pairClaimPath, `{"device_id":"aabbccddeeff"}`, pairPublicIP, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("public peer: status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "local network") {
		t.Errorf("public peer body = %q, want the LAN-only message", rec.Body.String())
	}
	// ...and neither is loopback, which is the Mac itself.
	if rec := rig.post(pairClaimPath, `{"device_id":"aabbccddeeff"}`, pairLoopback, ""); rec.Code != http.StatusForbidden {
		t.Errorf("loopback peer: status = %d, want 403", rec.Code)
	}
	if rig.pairing.state != pairStateOpen {
		t.Errorf("state = %q, want open — a refused claim must not take the slot", rig.pairing.state)
	}
}

// TestPairingClaimIsUnauthenticated pins the DEVICE-FACING auth model: an
// unpaired device has no credential, so the claim endpoint must work without
// one. If this test fails because someone "secured" the endpoint, pairing is
// broken, not fixed.
func TestPairingClaimIsUnauthenticated(t *testing.T) {
	rig := newPairRig(t)
	rig.open(t)

	rec := rig.post(pairClaimPath, `{"device_id":"aabbccddeeff"}`, pairDeviceIP, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("unauthenticated claim: status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestPairingDashboardEndpointsRequireToken pins the DASHBOARD-FACING auth
// model: open and confirm are mutating routes, so ORDER #54 applies even on
// loopback.
func TestPairingDashboardEndpointsRequireToken(t *testing.T) {
	rig := newPairRig(t)

	for _, path := range []string{pairOpenPath, pairConfirmPath} {
		if rec := rig.post(path, `{}`, pairOtherIP, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s without token from the LAN: status = %d, want 401", path, rec.Code)
		}
		if rec := rig.post(path, `{}`, pairDeviceIP, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s with a wrong token: status = %d, want 401", path, rec.Code)
		}
	}
	if rec := rig.get(pairStatusPath, pairDeviceIP, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET %s from the LAN without a token: status = %d, want 401", pairStatusPath, rec.Code)
	}
}

// TestPairingClaimRejectsFormPost keeps the cross-site form defence on the one
// endpoint that has no token to lose.
func TestPairingClaimRejectsFormPost(t *testing.T) {
	rig := newPairRig(t)
	rig.open(t)

	req := httptest.NewRequest(http.MethodPost, pairClaimPath, strings.NewReader("device_id=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = pairDeviceIP
	rec := httptest.NewRecorder()
	rig.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form-encoded claim: status = %d, want 415", rec.Code)
	}
}

// --- One device per window ---

func TestPairingSecondDeviceRefused(t *testing.T) {
	rig := newPairRig(t)
	rig.open(t)

	if rec, _ := rig.claim(t, `{"device_id":"aabbccddeeff"}`, pairDeviceIP); rec.Code != http.StatusAccepted {
		t.Fatalf("first claim: status = %d, want 202", rec.Code)
	}
	rec, _ := rig.claim(t, `{"device_id":"001122334455"}`, pairOtherIP)
	if rec.Code != http.StatusConflict {
		t.Errorf("second device: status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

// --- What must never leave the agent ---

// TestPairingStatusNeverLeaksCodeOrToken: the code lives on the DEVICE screen,
// which is what makes typing it proof of physical possession. A dashboard that
// could read the code would make the whole confirmation step theatre.
func TestPairingStatusNeverLeaksCodeOrToken(t *testing.T) {
	rig := newPairRig(t)
	rig.open(t)

	_, body := rig.claim(t, `{"device_id":"aabbccddeeff","name":"sticks3"}`, pairDeviceIP)
	code, _ := body["code"].(string)

	status := rig.get(pairStatusPath, pairLoopback, "")
	if status.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200", pairStatusPath, status.Code)
	}
	if strings.Contains(status.Body.String(), code) {
		t.Errorf("GET %s leaked the pairing code", pairStatusPath)
	}

	if conf := rig.post(pairConfirmPath, `{"code":"`+code+`"}`, pairLoopback, pairDashToken); conf.Code != http.StatusOK {
		t.Fatalf("confirm: status = %d, want 200", conf.Code)
	}
	rig.pairing.mu.Lock()
	token := rig.pairing.token
	rig.pairing.mu.Unlock()

	status = rig.get(pairStatusPath, pairLoopback, "")
	if strings.Contains(status.Body.String(), token) {
		t.Errorf("GET %s leaked the issued token", pairStatusPath)
	}

	// After pickup the device list is public, but tokens are not part of it.
	rig.claim(t, `{"device_id":"aabbccddeeff"}`, pairDeviceIP)
	status = rig.get(pairStatusPath, pairLoopback, "")
	if strings.Contains(status.Body.String(), token) {
		t.Errorf("the paired-device list leaked the issued token")
	}
	if !strings.Contains(status.Body.String(), "aabbccddeeff") {
		t.Errorf("the paired-device list is missing the device id: %s", status.Body.String())
	}
}

// TestPairingTokenNeverLogged is the log-line half of the same rule: pairing
// logs lengths, never values — the discipline the access log already follows.
func TestPairingTokenNeverLogged(t *testing.T) {
	rig := newPairRig(t)
	code, token := rig.pairOnce(t)

	// Exercise the authenticated path too: the middleware must not log the
	// token it just accepted.
	rig.get(pairStatusPath, pairDeviceIP, token)

	logs := rig.logs.String()
	if logs == "" {
		t.Fatal("no log output captured — the test would pass vacuously")
	}
	if strings.Contains(logs, token) {
		t.Errorf("the issued device token appears in a log line")
	}
	if strings.Contains(logs, code) {
		t.Errorf("the pairing code appears in a log line")
	}
	if !strings.Contains(logs, "token_len") {
		t.Errorf("expected a token_len field in the logs, got: %s", logs)
	}
}

// --- Input handling ---

func TestPairingRejectsMalformedCode(t *testing.T) {
	rig := newPairRig(t)
	rig.open(t)

	// O and 0 are not in the alphabet precisely because they are read off a
	// small screen.
	for _, bad := range []string{"O0O0O0", "ABC", "ABCDEFG", "ABC 2!"} {
		rec := rig.post(pairClaimPath, `{"code":"`+bad+`"}`, pairDeviceIP, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("claim with code %q: status = %d, want 400", bad, rec.Code)
		}
	}
}

func TestPairingRejectsMalformedDeviceID(t *testing.T) {
	rig := newPairRig(t)
	rig.open(t)

	rec := rig.post(pairClaimPath, `{"device_id":"a b\nc"}`, pairDeviceIP, "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("claim with a malformed device_id: status = %d, want 400", rec.Code)
	}
}

func TestPairingDeviceMintsCodeWhenNoneOffered(t *testing.T) {
	rig := newPairRig(t)
	rig.open(t)

	rec, body := rig.claim(t, `{"device_id":"aabbccddeeff"}`, pairDeviceIP)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("claim: status = %d, want 202", rec.Code)
	}
	code, _ := body["code"].(string)
	if !validPairCode(code) {
		t.Errorf("minted code %q is not valid", code)
	}
}

func TestPairingAcceptsDeviceOfferedCode(t *testing.T) {
	rig := newPairRig(t)
	rig.open(t)

	rec, body := rig.claim(t, `{"device_id":"aabbccddeeff","code":"h7k-29p"}`, pairDeviceIP)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("claim: status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	// Lower case and separators are normalised, because that is how a human
	// types what the screen shows.
	if got, _ := body["code"].(string); got != "H7K29P" {
		t.Errorf("code = %q, want H7K29P", got)
	}
	// And the typed form is accepted back with the same forgiveness.
	if conf := rig.post(pairConfirmPath, `{"code":" h7k-29p "}`, pairLoopback, pairDashToken); conf.Code != http.StatusOK {
		t.Errorf("confirm with the typed form: status = %d, want 200 (body %s)", conf.Code, conf.Body.String())
	}
}

func TestNewPairCodeAlphabet(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		code, err := newPairCode()
		if err != nil {
			t.Fatalf("newPairCode: %v", err)
		}
		if len(code) != pairCodeLen {
			t.Fatalf("code %q has length %d, want %d", code, len(code), pairCodeLen)
		}
		if strings.ContainsAny(code, "O0I1") {
			t.Fatalf("code %q contains an ambiguous character", code)
		}
		if !validPairCode(code) {
			t.Fatalf("code %q is outside the alphabet", code)
		}
		seen[code] = true
	}
	if len(seen) < 190 {
		t.Errorf("only %d distinct codes out of 200 — the generator looks predictable", len(seen))
	}
}

func TestNewDeviceTokenIsDistinct(t *testing.T) {
	a, err := newDeviceToken()
	if err != nil {
		t.Fatalf("newDeviceToken: %v", err)
	}
	b, err := newDeviceToken()
	if err != nil {
		t.Fatalf("newDeviceToken: %v", err)
	}
	if a == b {
		t.Error("two device tokens came back identical")
	}
	if len(a) != pairTokenBytes*2 {
		t.Errorf("token length = %d, want %d", len(a), pairTokenBytes*2)
	}
}

func TestIsLANPeer(t *testing.T) {
	cases := map[string]bool{
		"192.168.0.136": true,
		"10.1.2.3":      true,
		"172.16.4.5":    true,
		"169.254.10.1":  true,
		"127.0.0.1":     false,
		"::1":           false,
		"8.8.8.8":       false,
		"":              false,
		"not-an-ip":     false,
	}
	for host, want := range cases {
		if got := isLANPeer(host); got != want {
			t.Errorf("isLANPeer(%q) = %v, want %v", host, got, want)
		}
	}
}

// --- Integration through api.New ---

// TestPairedTokenAuthenticatesUsage runs the whole flow against the real server
// and then uses the issued token the way the firmware will: a LAN GET
// /v1/usage that must no longer be rejected.
func TestPairedTokenAuthenticatesUsage(t *testing.T) {
	dir := setupFixtures(t)
	cfg := config.Config{
		Listen:      "0.0.0.0:8765",
		Interval:    900 * time.Second,
		TZ:          testLoc,
		DeviceToken: "x",
		StatePath:   filepath.Join(t.TempDir(), "state.json"),
	}
	handler, _, _ := newFixtureHandlerCfg(t, dir, cfg)

	do := func(method, path, body, remote, token string) *httptest.ResponseRecorder {
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.RemoteAddr = remote
		if token != "" {
			req.Header.Set("X-Device-Token", token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := do(http.MethodPost, pairOpenPath, `{}`, pairLoopback, "x"); rec.Code != http.StatusOK {
		t.Fatalf("open: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	rec := do(http.MethodPost, pairClaimPath, `{"device_id":"aabbccddeeff"}`, pairDeviceIP, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("claim: status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	var claimed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &claimed); err != nil {
		t.Fatalf("decode claim: %v", err)
	}
	code, _ := claimed["code"].(string)

	if rec := do(http.MethodPost, pairConfirmPath, `{"code":"`+code+`"}`, pairLoopback, "x"); rec.Code != http.StatusOK {
		t.Fatalf("confirm: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	rec = do(http.MethodPost, pairClaimPath, `{"device_id":"aabbccddeeff"}`, pairDeviceIP, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("pickup: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var paired map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &paired); err != nil {
		t.Fatalf("decode pickup: %v", err)
	}
	token, _ := paired["token"].(string)
	if token == "" {
		t.Fatal("pickup returned no token")
	}

	// The point of the whole task: this token, which was never in the binary,
	// now authenticates the device.
	if rec := do(http.MethodGet, "/v1/usage", "", pairDeviceIP, token); rec.Code != http.StatusOK {
		t.Errorf("GET /v1/usage with the issued token: status = %d, want 200", rec.Code)
	}
	// The configured token keeps working, and a random one still does not.
	if rec := do(http.MethodGet, "/v1/usage", "", pairDeviceIP, "x"); rec.Code != http.StatusOK {
		t.Errorf("GET /v1/usage with the configured token: status = %d, want 200", rec.Code)
	}
	if rec := do(http.MethodGet, "/v1/usage", "", pairDeviceIP, strings.Repeat("a", len(token))); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/usage with a forged token: status = %d, want 401", rec.Code)
	}

	// The devices file lands beside the snapshot state file.
	if _, err := os.Stat(pairedDevicesPath(cfg.StatePath)); err != nil {
		t.Errorf("devices.json not written beside the state file: %v", err)
	}
}

// TestIssueDeviceTokenRecordsAndAuthenticates covers the token path BLE needs.
//
// The HTTP claim flow cannot serve BLE: the device is handed its token BEFORE
// it is on Wi-Fi, so it can never POST /v1/pair/claim to collect one. But a
// token this agent has not RECORDED is a credential nothing accepts — the
// device would join, present it, and be refused forever. So the BLE path needs
// a mint-and-record that is not coupled to an HTTP response.
func TestIssueDeviceTokenRecordsAndAuthenticates(t *testing.T) {
	rig := newPairRig(t)

	tok, err := rig.pairing.issueDeviceToken("usaged-D534", "StickS3")
	if err != nil {
		t.Fatalf("issueDeviceToken: %v", err)
	}
	if len(tok) != pairTokenBytes*2 {
		t.Errorf("token length = %d, want %d hex chars", len(tok), pairTokenBytes*2)
	}
	// The whole point: the agent must now accept it.
	if !rig.pairing.matchToken(tok) {
		t.Error("matchToken(issued token) = false, want true — the token was not recorded")
	}
	if rig.pairing.matchToken("not-the-token") {
		t.Error("matchToken(wrong token) = true, want false")
	}

	// It must survive a restart, like every other issued token.
	reloaded := newPairing(filepath.Join(rig.dir, "devices.json"),
		func() time.Time { return rig.now }, slog.New(slog.NewJSONHandler(rig.logs, nil)))
	if !reloaded.matchToken(tok) {
		t.Error("token did not survive a reload of the paired-device store")
	}

	// Never logged.
	if strings.Contains(rig.logs.String(), tok) {
		t.Error("the issued token value reached the log")
	}
}

// TestIssueDeviceTokenIsPerDevice: a second device gets its own token and does
// not evict the first. Two StickS3s on one Mac is an ordinary case.
func TestIssueDeviceTokenIsPerDevice(t *testing.T) {
	rig := newPairRig(t)

	a, err := rig.pairing.issueDeviceToken("usaged-AAAA", "")
	if err != nil {
		t.Fatalf("issueDeviceToken(a): %v", err)
	}
	b, err := rig.pairing.issueDeviceToken("usaged-BBBB", "")
	if err != nil {
		t.Fatalf("issueDeviceToken(b): %v", err)
	}
	if a == b {
		t.Fatal("two devices were issued the same token")
	}
	if !rig.pairing.matchToken(a) || !rig.pairing.matchToken(b) {
		t.Error("both tokens must remain valid")
	}
}

// TestIssueDeviceTokenRequiresID: an empty id would key every device to the
// same record and silently overwrite the previous one's token.
func TestIssueDeviceTokenRequiresID(t *testing.T) {
	rig := newPairRig(t)
	if _, err := rig.pairing.issueDeviceToken("", "StickS3"); err == nil {
		t.Error("issueDeviceToken(\"\") = nil error, want a refusal")
	}
}
