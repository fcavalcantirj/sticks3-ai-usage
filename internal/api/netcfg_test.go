package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"usaged/internal/config"
)

// --- Device network configuration test rig (task 79) ---

const (
	netcfgDashToken = "dash-token"
	netcfgSecret    = "correct-horse-battery-staple" // the value that must never escape
	netcfgLoopback  = "127.0.0.1:5000"
	netcfgLANPeer   = "192.168.0.99:5000"
	netcfgDeviceIP  = "192.168.0.136:5000"
	netcfgOtherIP   = "192.168.0.99:5000"
)

// netcfgRig wires the real netcfg handlers behind the real auth middleware with
// an injected clock and a capturable log, so expiry, re-delivery and "the
// password never reaches a log line" can all be exercised without a scheduler.
type netcfgRig struct {
	handler http.Handler
	netcfg  *netcfg
	logs    *bytes.Buffer
	now     time.Time
}

func newNetcfgRig(t *testing.T) *netcfgRig {
	t.Helper()
	rig := &netcfgRig{logs: &bytes.Buffer{}, now: fixedNow}
	logger := slog.New(slog.NewJSONHandler(rig.logs, nil))
	rig.netcfg = newNetcfg(func() time.Time { return rig.now }, logger)
	// The staging limiter is a human-click guard (5/min). Tests exercise many
	// rejected payloads in a row, which is exactly what it is meant to stop, so
	// they raise it; TestNetcfgStageRateLimited keeps the real value honest.
	rig.netcfg.stageLimit = newRateLimiter(1000, time.Minute)
	rig.netcfg.checkinLimit = newRateLimiter(1000, time.Minute)

	mux := http.NewServeMux()
	rig.netcfg.routes(mux)
	cfg := config.Config{Listen: "127.0.0.1:0", DeviceToken: netcfgDashToken}
	rig.handler = newAuth(cfg, nil, logger).middleware(mux)
	return rig
}

func (rig *netcfgRig) do(method, path, body, remote, token string) *httptest.ResponseRecorder {
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
	rig.handler.ServeHTTP(rec, req)
	return rec
}

// decode returns the JSON body of a recorder, failing the test if it is not
// JSON — a handler that answers with something else is itself the defect.
func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if rec.Body.Len() == 0 {
		return m
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return m
}

// stage puts a valid change and fails the test if it is not accepted.
func (rig *netcfgRig) stage(t *testing.T, ssid, pass string) string {
	t.Helper()
	body := `{"ssid":"` + ssid + `","password":"` + pass + `"}`
	rec := rig.do(http.MethodPut, netcfgPath, body, netcfgLoopback, netcfgDashToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("stage: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	pending, _ := decodeBody(t, rec)["pending"].(map[string]any)
	if pending == nil {
		t.Fatalf("stage: response carries no pending block: %s", rec.Body.String())
	}
	id, _ := pending["change_id"].(string)
	if id == "" {
		t.Fatalf("stage: no change_id in %s", rec.Body.String())
	}
	return id
}

// checkin posts a device check-in and returns the decoded body.
func (rig *netcfgRig) checkin(t *testing.T, body, remote string) map[string]any {
	t.Helper()
	rec := rig.do(http.MethodPost, netcfgDevicePath, body, remote, netcfgDashToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("checkin: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	return decodeBody(t, rec)
}

func (rig *netcfgRig) status(t *testing.T) map[string]any {
	t.Helper()
	rec := rig.do(http.MethodGet, netcfgPath, "", netcfgLoopback, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	return decodeBody(t, rec)
}

// --- Auth boundaries ---

// TestNetcfgDeviceChannelIsNotAGET is the whole reason the device half is a
// POST. auth.go exempts loopback GETs from the token check, so a GET carrying
// the staged password would hand it to any process on this Mac. The POST is
// mutating, so ORDER #54 requires the token even from loopback.
func TestNetcfgDeviceChannelIsNotAGET(t *testing.T) {
	rig := newNetcfgRig(t)
	rig.stage(t, "HomeNet", netcfgSecret)

	// No GET route exists at the device path at all.
	rec := rig.do(http.MethodGet, netcfgDevicePath, "", netcfgLANPeer, "")
	if rec.Code == http.StatusOK {
		t.Fatalf("GET %s answered 200 — the device channel must not be a GET", netcfgDevicePath)
	}
	if strings.Contains(rec.Body.String(), netcfgSecret) {
		t.Fatal("GET on the device path disclosed the staged password")
	}

	// And the POST is refused without a token, even from the LAN.
	rec = rig.do(http.MethodPost, netcfgDevicePath, `{"device_id":"aabb"}`, netcfgLANPeer, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated the LAN check-in: status = %d, want 401", rec.Code)
	}
	if strings.Contains(rec.Body.String(), netcfgSecret) {
		t.Fatal("a 401 response disclosed the staged password")
	}
}

// TestNetcfgStageRequiresToken proves the dashboard half follows ORDER #54:
// mutating routes need the token even on the LAN.
func TestNetcfgStageRequiresToken(t *testing.T) {
	rig := newNetcfgRig(t)
	for _, tc := range []struct{ method, body string }{
		{http.MethodPut, `{"ssid":"HomeNet","password":"` + netcfgSecret + `"}`},
		{http.MethodDelete, ""},
	} {
		rec := rig.do(tc.method, netcfgPath, tc.body, netcfgLANPeer, "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: status = %d, want 401", tc.method, netcfgPath, rec.Code)
		}
	}
	if st := rig.status(t)["state"]; st != string(netcfgIdle) {
		t.Errorf("state = %v after refused writes, want idle", st)
	}
}

// TestNetcfgStageRejectsFormPost is the CSRF half auth.go enforces: a
// cross-site form POST cannot carry Content-Type: application/json.
func TestNetcfgStageRejectsFormPost(t *testing.T) {
	rig := newNetcfgRig(t)
	req := httptest.NewRequest(http.MethodPut, netcfgPath,
		strings.NewReader(`{"ssid":"HomeNet","password":"`+netcfgSecret+`"}`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Device-Token", netcfgDashToken)
	req.RemoteAddr = netcfgLoopback
	rec := httptest.NewRecorder()
	rig.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("form-encoded stage: status = %d, want 415", rec.Code)
	}
}

// --- The pending-change lifecycle ---

// TestNetcfgAppliedOnceThenCleared is the core contract: the device collects
// the change once, acknowledges it, and is never told to apply it again.
func TestNetcfgAppliedOnceThenCleared(t *testing.T) {
	rig := newNetcfgRig(t)
	id := rig.stage(t, "HomeNet", netcfgSecret)

	// First check-in collects it, password and fallback policy included.
	body := rig.checkin(t, `{"device_id":"aabbccddeeff","ssid":"OldNet","state":"connected"}`, netcfgDeviceIP)
	if body["pending"] != true {
		t.Fatalf("first check-in: pending = %v, want true (%v)", body["pending"], body)
	}
	change, _ := body["change"].(map[string]any)
	if change == nil {
		t.Fatal("first check-in carried no change block")
	}
	if change["change_id"] != id {
		t.Errorf("change_id = %v, want %s", change["change_id"], id)
	}
	if change["ssid"] != "HomeNet" {
		t.Errorf("ssid = %v, want HomeNet", change["ssid"])
	}
	if change["password"] != netcfgSecret {
		t.Errorf("the device was not given the password it needs to join")
	}
	fb, _ := change["fallback"].(map[string]any)
	if fb == nil || fb["previous_first"] != true || fb["then"] != "portal" {
		t.Errorf("fallback policy = %v, want previous credentials first then the portal", fb)
	}

	// A second check-in inside the quiet period says "you already have it".
	body = rig.checkin(t, `{"device_id":"aabbccddeeff","ssid":"OldNet","state":"joining"}`, netcfgDeviceIP)
	if body["pending"] != false {
		t.Errorf("re-poll during apply: pending = %v, want false", body["pending"])
	}
	if body["awaiting_ack"] != true {
		t.Errorf("re-poll during apply: awaiting_ack = %v, want true", body["awaiting_ack"])
	}

	// The acknowledgement clears it for good.
	body = rig.checkin(t,
		`{"device_id":"aabbccddeeff","ssid":"HomeNet","state":"connected","ack":{"change_id":"`+id+`","result":"applied"}}`,
		netcfgDeviceIP)
	if body["ack_accepted"] != true {
		t.Fatalf("ack_accepted = %v, want true", body["ack_accepted"])
	}
	if body["pending"] != false {
		t.Errorf("after ack: pending = %v, want false", body["pending"])
	}

	// Not even after the quiet period expires.
	rig.now = rig.now.Add(10 * time.Minute)
	body = rig.checkin(t, `{"device_id":"aabbccddeeff","ssid":"HomeNet","state":"connected"}`, netcfgDeviceIP)
	if body["pending"] != false {
		t.Errorf("an applied change came back: pending = %v, want false", body["pending"])
	}

	st := rig.status(t)
	if st["state"] != string(netcfgIdle) {
		t.Errorf("state = %v, want idle", st["state"])
	}
	last, _ := st["last"].(map[string]any)
	if last == nil || last["state"] != string(netcfgApplied) || last["ssid"] != "HomeNet" {
		t.Errorf("last = %v, want an applied record for HomeNet", last)
	}
}

// TestNetcfgFailedAckIsNotRetried is the failure path. A device that could not
// join has already fallen back; re-offering the change would send it away from
// the working network all over again.
func TestNetcfgFailedAckIsNotRetried(t *testing.T) {
	rig := newNetcfgRig(t)
	id := rig.stage(t, "TypoNet", netcfgSecret)
	rig.checkin(t, `{"device_id":"aabbccddeeff","ssid":"OldNet","state":"connected"}`, netcfgDeviceIP)

	body := rig.checkin(t,
		`{"device_id":"aabbccddeeff","ssid":"OldNet","state":"connected","ack":{"change_id":"`+id+
			`","result":"failed","error":"auth failed, reason 15"}}`, netcfgDeviceIP)
	if body["ack_accepted"] != true || body["pending"] != false {
		t.Fatalf("failed ack: %v", body)
	}

	rig.now = rig.now.Add(30 * time.Minute)
	if body := rig.checkin(t, `{"device_id":"aabbccddeeff","ssid":"OldNet","state":"connected"}`, netcfgDeviceIP); body["pending"] != false {
		t.Error("a failed change was offered again — the device would leave a working network twice")
	}

	last, _ := rig.status(t)["last"].(map[string]any)
	if last == nil || last["state"] != string(netcfgFailed) {
		t.Fatalf("last = %v, want a failed record", last)
	}
	if last["error"] != "auth failed, reason 15" {
		t.Errorf("last.error = %v, want the device's own reason so the page can explain it", last["error"])
	}
}

// TestNetcfgStallsAfterMaxDeliveries covers the device that collects a change
// and reboots before it can acknowledge: the agent stops feeding it rather
// than holding it in a join loop for ever.
func TestNetcfgStallsAfterMaxDeliveries(t *testing.T) {
	rig := newNetcfgRig(t)
	rig.stage(t, "HomeNet", netcfgSecret)

	for i := 0; i < netcfgMaxDeliveries; i++ {
		body := rig.checkin(t, `{"device_id":"aabbccddeeff","state":"joining"}`, netcfgDeviceIP)
		if body["pending"] != true {
			t.Fatalf("delivery %d: pending = %v, want true", i+1, body["pending"])
		}
		rig.now = rig.now.Add(netcfgRedeliverAfter + time.Second)
	}

	body := rig.checkin(t, `{"device_id":"aabbccddeeff","state":"joining"}`, netcfgDeviceIP)
	if body["pending"] != false {
		t.Fatalf("delivery %d: pending = %v, want false (stalled)", netcfgMaxDeliveries+1, body["pending"])
	}
	last, _ := rig.status(t)["last"].(map[string]any)
	if last == nil || last["state"] != string(netcfgStalled) {
		t.Fatalf("last = %v, want a stalled record", last)
	}
}

// TestNetcfgExpires retires a change the device never came for, so the page
// stops promising something that will not happen.
func TestNetcfgExpires(t *testing.T) {
	rig := newNetcfgRig(t)
	rig.stage(t, "HomeNet", netcfgSecret)

	rig.now = rig.now.Add(netcfgChangeTTL + time.Second)
	if body := rig.checkin(t, `{"device_id":"aabbccddeeff"}`, netcfgDeviceIP); body["pending"] != false {
		t.Error("an expired change was still delivered")
	}
	st := rig.status(t)
	if st["state"] != string(netcfgIdle) {
		t.Errorf("state = %v, want idle", st["state"])
	}
	last, _ := st["last"].(map[string]any)
	if last == nil || last["state"] != string(netcfgExpired) {
		t.Fatalf("last = %v, want an expired record", last)
	}
}

// TestNetcfgCancel withdraws a change before the device polls — on a 900 s
// interval that covers most typos.
func TestNetcfgCancel(t *testing.T) {
	rig := newNetcfgRig(t)

	rec := rig.do(http.MethodDelete, netcfgPath, "", netcfgLoopback, netcfgDashToken)
	if rec.Code != http.StatusConflict {
		t.Fatalf("cancel with nothing staged: status = %d, want 409", rec.Code)
	}

	rig.stage(t, "HomeNet", netcfgSecret)
	rec = rig.do(http.MethodDelete, netcfgPath, "", netcfgLoopback, netcfgDashToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if body := rig.checkin(t, `{"device_id":"aabbccddeeff"}`, netcfgDeviceIP); body["pending"] != false {
		t.Error("a cancelled change was still delivered")
	}
	last, _ := rig.status(t)["last"].(map[string]any)
	if last == nil || last["state"] != string(netcfgCancelled) {
		t.Fatalf("last = %v, want a cancelled record", last)
	}
}

// TestNetcfgSupersededByNewerChange: staging again replaces the first change,
// and a late acknowledgement for the replaced one must not delete its
// replacement.
func TestNetcfgSupersededByNewerChange(t *testing.T) {
	rig := newNetcfgRig(t)
	first := rig.stage(t, "FirstNet", netcfgSecret)
	second := rig.stage(t, "SecondNet", netcfgSecret)
	if first == second {
		t.Fatal("staging twice reused the change id")
	}

	body := rig.checkin(t,
		`{"device_id":"aabbccddeeff","ack":{"change_id":"`+first+`","result":"applied"}}`, netcfgDeviceIP)
	if body["ack_accepted"] != false {
		t.Errorf("ack for a superseded change: ack_accepted = %v, want false", body["ack_accepted"])
	}
	change, _ := body["change"].(map[string]any)
	if change == nil || change["change_id"] != second {
		t.Fatalf("the newer change was not delivered: %v", body)
	}
	last, _ := rig.status(t)["last"].(map[string]any)
	if last == nil || last["state"] != string(netcfgSuperseded) || last["ssid"] != "FirstNet" {
		t.Errorf("last = %v, want a superseded record for FirstNet", last)
	}
}

// TestNetcfgBindsToTheFirstDevice: an unaddressed change belongs to whichever
// device collects it, and no second device may also act on it.
func TestNetcfgBindsToTheFirstDevice(t *testing.T) {
	rig := newNetcfgRig(t)
	rig.stage(t, "HomeNet", netcfgSecret)

	if body := rig.checkin(t, `{"device_id":"aaaaaaaaaaaa"}`, netcfgDeviceIP); body["pending"] != true {
		t.Fatalf("first device did not collect the change: %v", body)
	}
	rig.now = rig.now.Add(netcfgRedeliverAfter + time.Second)

	body := rig.checkin(t, `{"device_id":"bbbbbbbbbbbb"}`, netcfgOtherIP)
	if body["pending"] != false {
		t.Fatal("a second device was handed a change bound to the first")
	}
	if strings.Contains(rig.logs.String(), netcfgSecret) {
		t.Fatal("the password reached a log line")
	}
}

// --- Validation: an invalid payload changes nothing ---

func TestNetcfgInvalidPayloadChangesNothing(t *testing.T) {
	rig := newNetcfgRig(t)
	id := rig.stage(t, "HomeNet", netcfgSecret)

	// Built with json.Marshal so the control character is a real one rather
	// than an escape sequence a reader has to trust.
	ctrlSSID, err := json.Marshal(map[string]string{"ssid": "Home\tNet", "password": netcfgSecret})
	if err != nil {
		t.Fatalf("marshal control-character payload: %v", err)
	}

	bad := []struct {
		name string
		body string
	}{
		{"empty ssid", `{"ssid":"","password":"` + netcfgSecret + `"}`},
		{"ssid too long", `{"ssid":"` + strings.Repeat("x", netcfgMaxSSIDLen+1) + `","password":"` + netcfgSecret + `"}`},
		{"ssid with a control character", string(ctrlSSID)},
		{"password too short", `{"ssid":"HomeNet","password":"short"}`},
		{"password too long", `{"ssid":"HomeNet","password":"` + strings.Repeat("y", netcfgMaxPassLen+1) + `"}`},
		{"open network with a password", `{"ssid":"HomeNet","open":true,"password":"` + netcfgSecret + `"}`},
		{"missing password", `{"ssid":"HomeNet"}`},
		{"bad device_id", `{"ssid":"HomeNet","password":"` + netcfgSecret + `","device_id":"not a mac!"}`},
		{"not JSON", `{"ssid":`},
		{"empty body", ``},
	}
	for _, tc := range bad {
		rec := rig.do(http.MethodPut, netcfgPath, tc.body, netcfgLoopback, netcfgDashToken)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body %s)", tc.name, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), netcfgSecret) {
			t.Errorf("%s: the error message echoed the password back", tc.name)
		}
	}

	// Nothing moved: the original change is still the one staged, still
	// undelivered, and it is what the device collects.
	st := rig.status(t)
	pending, _ := st["pending"].(map[string]any)
	if pending == nil || pending["change_id"] != id || pending["ssid"] != "HomeNet" {
		t.Fatalf("pending = %v, want the original HomeNet change %s", pending, id)
	}
	if pending["delivered"] != float64(0) {
		t.Errorf("delivered = %v, want 0", pending["delivered"])
	}
	change, _ := rig.checkin(t, `{"device_id":"aabbccddeeff"}`, netcfgDeviceIP)["change"].(map[string]any)
	if change == nil || change["change_id"] != id {
		t.Fatalf("the device collected %v, want the original change", change)
	}
}

// TestNetcfgInvalidCheckinChangesNothing: a malformed device check-in must not
// consume, bind or clear the staged change.
func TestNetcfgInvalidCheckinChangesNothing(t *testing.T) {
	rig := newNetcfgRig(t)
	id := rig.stage(t, "HomeNet", netcfgSecret)

	// An empty body is a bad request on this route: every field the device
	// sends is needed, so silence must not read as "nothing to report".
	for _, body := range []string{`{"device_id":"not a mac!"}`, `{"ssid":`, ``} {
		rec := rig.do(http.MethodPost, netcfgDevicePath, body, netcfgDeviceIP, netcfgDashToken)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("check-in %q: status = %d, want 400", body, rec.Code)
		}
	}

	pending, _ := rig.status(t)["pending"].(map[string]any)
	if pending == nil || pending["change_id"] != id || pending["delivered"] != float64(0) {
		t.Fatalf("pending = %v, want the untouched change %s", pending, id)
	}
}

// TestNetcfgOpenNetwork: a network with no password is possible but must be
// asked for explicitly, so an empty password box is never mistaken for one.
func TestNetcfgOpenNetwork(t *testing.T) {
	rig := newNetcfgRig(t)
	rec := rig.do(http.MethodPut, netcfgPath, `{"ssid":"CafeWifi","open":true}`, netcfgLoopback, netcfgDashToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("open network: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	st := rig.status(t)
	if st["password_state"] != "not_set" || st["password_source"] != "pending-change" {
		t.Errorf("password labels = %v/%v, want not_set/pending-change", st["password_state"], st["password_source"])
	}
	change, _ := rig.checkin(t, `{"device_id":"aabbccddeeff"}`, netcfgDeviceIP)["change"].(map[string]any)
	if change == nil || change["open"] != true || change["password"] != "" {
		t.Fatalf("open change = %v, want open with an empty password", change)
	}
}

// TestNetcfgHexPSKAccepted: a raw 64-hex-digit pre-shared key is a valid
// credential even though it is longer than a passphrase may be.
func TestNetcfgHexPSKAccepted(t *testing.T) {
	rig := newNetcfgRig(t)
	psk := strings.Repeat("ab", 32)
	rec := rig.do(http.MethodPut, netcfgPath, `{"ssid":"HomeNet","password":"`+psk+`"}`, netcfgLoopback, netcfgDashToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("64-hex PSK: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestNetcfgStageRateLimited keeps the real limiter honest — newNetcfgRig
// raises it, so this test builds the manager with its shipped value.
func TestNetcfgStageRateLimited(t *testing.T) {
	rig := newNetcfgRig(t)
	rig.netcfg.stageLimit = newRateLimiter(netcfgStagesPerMinute, time.Minute)
	body := `{"ssid":"HomeNet","password":"` + netcfgSecret + `"}`
	for i := 0; i < netcfgStagesPerMinute; i++ {
		if rec := rig.do(http.MethodPut, netcfgPath, body, netcfgLoopback, netcfgDashToken); rec.Code != http.StatusOK {
			t.Fatalf("stage %d: status = %d, want 200", i+1, rec.Code)
		}
	}
	if rec := rig.do(http.MethodPut, netcfgPath, body, netcfgLoopback, netcfgDashToken); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("stage %d: status = %d, want 429", netcfgStagesPerMinute+1, rec.Code)
	}
}

// --- The device's own report ---

// TestNetcfgDeviceReport: the agent only ever sees an IP, so the network the
// device is on is something the device tells it. The labels follow the
// key_state / key_source convention: whether a password exists, and where the
// value actually lives.
func TestNetcfgDeviceReport(t *testing.T) {
	rig := newNetcfgRig(t)

	st := rig.status(t)
	if st["device"] != nil {
		t.Errorf("device = %v before any check-in, want null", st["device"])
	}
	if st["password_state"] != "not_set" || st["password_source"] != "none" {
		t.Errorf("password labels = %v/%v, want not_set/none", st["password_state"], st["password_source"])
	}

	rig.checkin(t,
		`{"device_id":"aabbccddeeff","ssid":"OldNet","state":"connected","ip":"192.168.0.136","secured":true}`,
		netcfgDeviceIP)
	rig.now = rig.now.Add(12 * time.Second)

	st = rig.status(t)
	dev, _ := st["device"].(map[string]any)
	if dev == nil {
		t.Fatal("device is null after a check-in")
	}
	if dev["ssid"] != "OldNet" || dev["state"] != "connected" || dev["ip"] != "192.168.0.136" {
		t.Errorf("device = %v, want OldNet/connected/192.168.0.136", dev)
	}
	if dev["seconds_since"] != float64(12) {
		t.Errorf("seconds_since = %v, want 12", dev["seconds_since"])
	}
	// The device has a password for that network; the dashboard has never held
	// it, and the source says exactly that.
	if st["password_state"] != "set" || st["password_source"] != "device" {
		t.Errorf("password labels = %v/%v, want set/device", st["password_state"], st["password_source"])
	}
}

// TestNetcfgDeviceReportIsSanitised: the device is the least trusted writer in
// the system and its strings reach both a log line and a web page.
func TestNetcfgDeviceReportIsSanitised(t *testing.T) {
	rig := newNetcfgRig(t)
	// json.Marshal so the control character in the SSID is a real one.
	report, err := json.Marshal(map[string]any{
		"device_id": "aabbccddeeff",
		"ssid":      "Bad\tNet",
		"state":     "nonsense",
		"ip":        "not-an-ip",
	})
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	rig.checkin(t, string(report), netcfgDeviceIP)
	dev, _ := rig.status(t)["device"].(map[string]any)
	if dev == nil {
		t.Fatal("device is null after a check-in")
	}
	if dev["ssid"] != "BadNet" {
		t.Errorf("ssid = %q, want the control character stripped", dev["ssid"])
	}
	if dev["state"] != "unknown" {
		t.Errorf("state = %v, want unknown for an unrecognised value", dev["state"])
	}
	if _, ok := dev["ip"]; ok {
		t.Errorf("ip = %v, want it dropped when it does not parse", dev["ip"])
	}
}

// --- The password never escapes ---

// TestNetcfgPasswordNeverLogged: lengths only, exactly like the provider keys
// and the issued device tokens.
func TestNetcfgPasswordNeverLogged(t *testing.T) {
	rig := newNetcfgRig(t)
	id := rig.stage(t, "HomeNet", netcfgSecret)
	rig.checkin(t, `{"device_id":"aabbccddeeff"}`, netcfgDeviceIP)
	rig.checkin(t,
		`{"device_id":"aabbccddeeff","ack":{"change_id":"`+id+`","result":"applied"}}`, netcfgDeviceIP)

	logs := rig.logs.String()
	if strings.Contains(logs, netcfgSecret) {
		t.Fatalf("the password reached the log:\n%s", logs)
	}
	if !strings.Contains(logs, `"pass_len":`) {
		t.Errorf("the staging log records no pass_len — lengths are what makes the rule checkable:\n%s", logs)
	}
}

// TestNetcfgPasswordIsWriteOnlyThroughEveryGET walks every GET route on the
// REAL server with a change staged and proves the password appears in none of
// them. This is the test that would have caught a "just add it to /v1/config".
func TestNetcfgPasswordIsWriteOnlyThroughEveryGET(t *testing.T) {
	dir := setupFixtures(t)
	handler, _, _ := newFixtureHandler(t, dir)

	req := httptest.NewRequest(http.MethodPut, netcfgPath,
		strings.NewReader(`{"ssid":"HomeNet","password":"`+netcfgSecret+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Device-Token", "x")
	req.RemoteAddr = netcfgLoopback
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("stage on the real server: status = %d (body %s)", rec.Code, rec.Body.String())
	}

	gets := []string{
		"/", "/index.html", "/healthz",
		"/v1/usage", "/v1/usage.txt", "/v1/device", "/v1/stats",
		"/v1/config", netcfgPath,
	}
	for _, path := range gets {
		for _, remote := range []string{netcfgLoopback, netcfgDeviceIP} {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			r.RemoteAddr = remote
			r.Header.Set("X-Device-Token", "x")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if strings.Contains(w.Body.String(), netcfgSecret) {
				t.Errorf("GET %s from %s disclosed the Wi-Fi password", path, remote)
			}
		}
	}
}

// --- The invariant this endpoint exists to protect ---

// TestNetcfgNeverTouchesRev is the reason this is a separate endpoint. If the
// change rode in the snapshot, rev would move and the device would redraw —
// breaking redraw-only-on-change, which is what the whole product rests on.
func TestNetcfgNeverTouchesRev(t *testing.T) {
	dir := setupFixtures(t)
	handler, _, _ := newFixtureHandler(t, dir)

	usage := func(inm string) (int, string, string) {
		r := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
		r.RemoteAddr = netcfgLoopback
		if inm != "" {
			r.Header.Set("If-None-Match", inm)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code, w.Header().Get("ETag"), w.Body.String()
	}

	_, before, bodyBefore := usage("")

	req := httptest.NewRequest(http.MethodPut, netcfgPath,
		strings.NewReader(`{"ssid":"HomeNet","password":"`+netcfgSecret+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Device-Token", "x")
	req.RemoteAddr = netcfgLoopback
	handler.ServeHTTP(httptest.NewRecorder(), req)

	code, after, bodyAfter := usage("")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/usage after staging: status = %d", code)
	}
	if before != after {
		t.Errorf("staging a Wi-Fi change moved the snapshot ETag: %s -> %s", before, after)
	}
	if bodyBefore != bodyAfter {
		t.Error("staging a Wi-Fi change altered the snapshot body")
	}
	if code, _, _ := usage(before); code != http.StatusNotModified {
		t.Errorf("conditional GET after staging: status = %d, want 304", code)
	}
}

// TestNetcfgCarriesNoETag: a conditional GET on this channel would be answered
// with a 304 and no body, and a device that got one would silently apply
// nothing. Neither half may ever carry a validator.
func TestNetcfgCarriesNoETag(t *testing.T) {
	rig := newNetcfgRig(t)
	rig.stage(t, "HomeNet", netcfgSecret)

	rec := rig.do(http.MethodGet, netcfgPath, "", netcfgLoopback, "")
	if etag := rec.Header().Get("ETag"); etag != "" {
		t.Errorf("GET %s carries ETag %q — this channel must never be conditional", netcfgPath, etag)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("GET %s Cache-Control = %q, want no-store", netcfgPath, cc)
	}

	rec = rig.do(http.MethodPost, netcfgDevicePath, `{"device_id":"aabbccddeeff"}`, netcfgDeviceIP, netcfgDashToken)
	if etag := rec.Header().Get("ETag"); etag != "" {
		t.Errorf("POST %s carries ETag %q", netcfgDevicePath, etag)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("POST %s Cache-Control = %q, want no-store", netcfgDevicePath, cc)
	}
}

// TestNetcfgStatusExplainsTheLatencyAndTheRecovery: the two things a user must
// be told are server-owned (GOLDEN_RULES #3), so curl gets them too.
func TestNetcfgStatusExplainsTheLatencyAndTheRecovery(t *testing.T) {
	rig := newNetcfgRig(t)
	st := rig.status(t)
	notice, _ := st["notice"].(string)
	recovery, _ := st["recovery"].(string)
	if !strings.Contains(notice, "NEXT check-in") {
		t.Errorf("notice = %q, must say the change is not instant", notice)
	}
	if !strings.Contains(recovery, "DEVICE SCREEN") {
		t.Errorf("recovery = %q, must send the user to the device screen — the portal drops any phone on it", recovery)
	}
}

// --- Pure validators ---

func TestValidSSID(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"HomeNet", true},
		{"a", true},
		{strings.Repeat("x", netcfgMaxSSIDLen), true},
		{"café-2.4G", true},
		{"", false},
		{strings.Repeat("x", netcfgMaxSSIDLen+1), false},
		{"Home\tNet", false},
		{"Home\x00Net", false},
	}
	for _, tc := range cases {
		if got := validSSID(tc.in); got != tc.want {
			t.Errorf("validSSID(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestValidWiFiPassword(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{strings.Repeat("a", netcfgMinPassLen), true},
		{strings.Repeat("a", netcfgMaxPassLen), true},
		{strings.Repeat("ab", 32), true}, // 64 hex digits: a raw PSK
		{"", false},
		{strings.Repeat("a", netcfgMinPassLen-1), false},
		{strings.Repeat("z", netcfgPSKHexLen), false}, // exactly 64 chars but not hex
		{strings.Repeat("a", 65), false},              // longer than any PSK
		{"pass\nword12", false},
	}
	for _, tc := range cases {
		if got := validWiFiPassword(tc.in); got != tc.want {
			t.Errorf("validWiFiPassword(len %d) = %v, want %v", len(tc.in), got, tc.want)
		}
	}
}
