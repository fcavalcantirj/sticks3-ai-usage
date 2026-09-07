package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"usaged/internal/config"
)

// --- One-click device setup test rig ---

const (
	setupDashToken = "dash-token"
	// setupSecret stands in for anything a Provisioner might accidentally put
	// in an error string. It must never reach a response body or a log line.
	setupSecret    = "correct-horse-battery-staple"
	setupLoopback  = "127.0.0.1:5000"
	setupLANPeer   = "192.168.0.55:5000"
	setupDeviceOne = "1ADE0F1C-0000-4000-8000-0000000000D5"
	setupDeviceTwo = "2BEF1A2D-0000-4000-8000-0000000000AA"
)

// fakeProvisioner is a Provisioner that never touches a radio. gate, when set,
// blocks both methods until the test releases it, so "one operation at a time"
// and cancellation can be exercised without sleeping.
type fakeProvisioner struct {
	mu sync.Mutex

	gate chan struct{}

	found   []FoundDevice
	scanErr error

	provisionErr  error
	provisionAddr string
	provisions    int
	scans         int
}

func (f *fakeProvisioner) wait(ctx context.Context) error {
	f.mu.Lock()
	gate := f.gate
	f.mu.Unlock()
	if gate == nil {
		return nil
	}
	select {
	case <-gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeProvisioner) Scan(ctx context.Context) ([]FoundDevice, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scans++
	return f.found, f.scanErr
}

func (f *fakeProvisioner) Provision(ctx context.Context, addr string) error {
	if err := f.wait(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.provisions++
	f.provisionAddr = addr
	return f.provisionErr
}

// fakeProgressProvisioner is the OPTIONAL half of the interface: an
// implementation that narrates its own run.
type fakeProgressProvisioner struct {
	*fakeProvisioner
	steps []string
}

func (f *fakeProgressProvisioner) ProvisionProgress(ctx context.Context, addr string, report func(string)) error {
	for _, s := range f.steps {
		report(s)
	}
	return f.Provision(ctx, addr)
}

type setupRig struct {
	handler http.Handler
	setup   *setup
	fake    *fakeProvisioner
	logs    *bytes.Buffer
	now     time.Time
	ready   setupReadiness
}

func newSetupRig(t *testing.T, prov Provisioner, fake *fakeProvisioner) *setupRig {
	t.Helper()
	rig := &setupRig{
		logs:  &bytes.Buffer{},
		now:   fixedNow,
		fake:  fake,
		ready: setupReadiness{ProvidersReady: 3, ProvidersTotal: 5},
	}
	logger := slog.New(slog.NewJSONHandler(rig.logs, nil))
	rig.setup = newSetup(prov,
		func() setupReadiness { return rig.ready },
		func() time.Time { return rig.now },
		logger)
	// Both limiters guard a human clicking a button; tests click far faster
	// than a human can. TestSetupScanIsRateLimited keeps the real value honest.
	rig.setup.scanLimit = newRateLimiter(1000, time.Minute)
	rig.setup.provisionLimit = newRateLimiter(1000, time.Minute)

	mux := http.NewServeMux()
	rig.setup.routes(mux)
	cfg := config.Config{Listen: "127.0.0.1:0", DeviceToken: setupDashToken}
	rig.handler = newAuth(cfg, nil, logger).middleware(mux)
	return rig
}

// newDefaultSetupRig is the common case: a plain Provisioner that finds one
// unprovisioned device and provisions it successfully.
func newDefaultSetupRig(t *testing.T) *setupRig {
	t.Helper()
	fake := &fakeProvisioner{found: []FoundDevice{
		{Addr: setupDeviceOne, Name: "usaged-D534", RSSI: -48, MAC: "24:0a:c4:11:d5:34", Capacity: 512, Version: 1},
	}}
	return newSetupRig(t, fake, fake)
}

func (rig *setupRig) do(method, path, body, remote, token string) *httptest.ResponseRecorder {
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

// scan runs a full scan and waits for it to land.
func (rig *setupRig) scan(t *testing.T) map[string]any {
	t.Helper()
	rec := rig.do(http.MethodPost, setupScanPath, `{}`, setupLoopback, setupDashToken)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("scan: status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	rig.setup.wg.Wait()
	return rig.status(t)
}

// provision starts a run against addr and waits for it to finish.
func (rig *setupRig) provision(t *testing.T, addr string) map[string]any {
	t.Helper()
	rec := rig.do(http.MethodPost, setupProvisionPath, `{"addr":"`+addr+`"}`, setupLoopback, setupDashToken)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("provision: status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	rig.setup.wg.Wait()
	return rig.status(t)
}

func (rig *setupRig) status(t *testing.T) map[string]any {
	t.Helper()
	rec := rig.do(http.MethodGet, setupPath, "", setupLoopback, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	return decodeBody(t, rec)
}

func runBlock(t *testing.T, st map[string]any) map[string]any {
	t.Helper()
	run, _ := st["run"].(map[string]any)
	if run == nil {
		t.Fatalf("status carries no run block: %v", st)
	}
	return run
}

// --- Auth boundaries: ORDER #54 applies to BOTH actions ---

// TestSetupScanRequiresATokenEvenFromLoopback: a scan powers a radio, so it is
// mutating, so the token is required even from this Mac.
func TestSetupScanRequiresATokenEvenFromLoopback(t *testing.T) {
	rig := newDefaultSetupRig(t)

	rec := rig.do(http.MethodPost, setupScanPath, `{}`, setupLoopback, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated loopback scan: status = %d, want 401", rec.Code)
	}
	rig.setup.wg.Wait()
	if rig.fake.scans != 0 {
		t.Fatal("an unauthenticated request reached the Provisioner")
	}

	rec = rig.do(http.MethodPost, setupScanPath, `{}`, setupLoopback, setupDashToken)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("authenticated scan: status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestSetupProvisionRequiresATokenEvenFromLoopback: provisioning hands a device
// the keys to the network, which is the last thing that should be reachable
// from any process on this Mac without a token.
func TestSetupProvisionRequiresATokenEvenFromLoopback(t *testing.T) {
	rig := newDefaultSetupRig(t)
	rig.scan(t)

	rec := rig.do(http.MethodPost, setupProvisionPath, `{"addr":"`+setupDeviceOne+`"}`, setupLoopback, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated loopback provision: status = %d, want 401", rec.Code)
	}
	rig.setup.wg.Wait()
	if rig.fake.provisions != 0 {
		t.Fatal("an unauthenticated request reached the Provisioner")
	}

	rec = rig.do(http.MethodPost, setupProvisionPath, `{"addr":"`+setupDeviceOne+`"}`, setupLANPeer, setupDashToken)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("authenticated provision: status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestSetupStopRequiresAToken covers the third mutating route.
func TestSetupStopRequiresAToken(t *testing.T) {
	rig := newDefaultSetupRig(t)
	rec := rig.do(http.MethodDelete, setupPath, "", setupLoopback, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated stop: status = %d, want 401", rec.Code)
	}
}

// TestSetupStatusFollowsTheOrdinaryGETRule: loopback reads without a token (the
// dashboard works unauthenticated on localhost), the LAN does not.
func TestSetupStatusFollowsTheOrdinaryGETRule(t *testing.T) {
	rig := newDefaultSetupRig(t)

	if rec := rig.do(http.MethodGet, setupPath, "", setupLoopback, ""); rec.Code != http.StatusOK {
		t.Fatalf("loopback GET: status = %d, want 200", rec.Code)
	}
	if rec := rig.do(http.MethodGet, setupPath, "", setupLANPeer, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("LAN GET without a token: status = %d, want 401", rec.Code)
	}
}

// --- Discovery ---

func TestSetupScanOffersTheUnprovisionedDevice(t *testing.T) {
	fake := &fakeProvisioner{found: []FoundDevice{
		{Addr: setupDeviceTwo, Name: "usaged-AA01", RSSI: -70, Provisioned: true, Capacity: 512, Version: 1},
		{Addr: setupDeviceOne, Name: "usaged-D534", RSSI: -48, MAC: "24:0a:c4:11:d5:34", Capacity: 512, Version: 1},
	}}
	rig := newSetupRig(t, fake, fake)

	st := rig.scan(t)
	if st["state"] != string(setupIdle) {
		t.Errorf("state after a finished scan = %v, want idle", st["state"])
	}
	offer, _ := st["offer"].(map[string]any)
	if offer == nil {
		t.Fatalf("no offer after finding an unprovisioned device: %v", st)
	}
	if offer["addr"] != setupDeviceOne {
		t.Errorf("offer addr = %v, want the UNPROVISIONED device %s", offer["addr"], setupDeviceOne)
	}
	if offer["headline"] != setupOfferHeadline {
		t.Errorf("offer headline = %v, want %q", offer["headline"], setupOfferHeadline)
	}
	if st["headline"] != setupOfferHeadline {
		t.Errorf("headline = %v, want %q", st["headline"], setupOfferHeadline)
	}

	scan, _ := st["scan"].(map[string]any)
	found, _ := scan["found"].([]any)
	if len(found) != 2 {
		t.Fatalf("found %d devices, want 2", len(found))
	}
	first, _ := found[0].(map[string]any)
	if first["addr"] != setupDeviceOne {
		t.Errorf("found[0] = %v, want the unprovisioned device first", first["addr"])
	}
}

// TestSetupScanWithNothingNearbySaysSo: an empty scan is not an error, and the
// headline must tell the owner what to do about it.
func TestSetupScanWithNothingNearbySaysSo(t *testing.T) {
	fake := &fakeProvisioner{}
	rig := newSetupRig(t, fake, fake)

	st := rig.scan(t)
	if _, ok := st["offer"]; ok {
		t.Error("an empty scan produced an offer")
	}
	head, _ := st["headline"].(string)
	if !strings.Contains(head, "blue button") {
		t.Errorf("headline = %q, want it to tell the owner to press the blue button", head)
	}
}

// TestSetupHostileAdvertisingNameIsCleaned: an advertising name is written by
// whatever is in radio range, so it is the least trusted string in the system.
func TestSetupHostileAdvertisingNameIsCleaned(t *testing.T) {
	fake := &fakeProvisioner{found: []FoundDevice{
		{Addr: setupDeviceOne, Name: "usaged\r\n<script>alert(1)</script>" + strings.Repeat("x", 200)},
		{Addr: "not a valid addr!", Name: "usaged-BAD"},
		{Addr: "", Name: "nameless"},
	}}
	rig := newSetupRig(t, fake, fake)

	st := rig.scan(t)
	scan, _ := st["scan"].(map[string]any)
	found, _ := scan["found"].([]any)
	if len(found) != 1 {
		t.Fatalf("found %d devices, want 1 — an unusable address must be dropped", len(found))
	}
	name, _ := found[0].(map[string]any)["name"].(string)
	if strings.ContainsAny(name, "\r\n") {
		t.Errorf("name %q still carries control characters", name)
	}
	if len(name) > setupMaxNameLen {
		t.Errorf("name is %d bytes, want at most %d", len(name), setupMaxNameLen)
	}
}

// --- Provisioning: the happy path and its guards ---

func TestSetupProvisionSuccess(t *testing.T) {
	rig := newDefaultSetupRig(t)
	rig.scan(t)

	st := rig.provision(t, setupDeviceOne)
	run := runBlock(t, st)
	if run["state"] != string(runApplied) {
		t.Fatalf("run state = %v, want applied (%v)", run["state"], run)
	}
	if rig.fake.provisionAddr != setupDeviceOne {
		t.Errorf("Provisioner got addr %q, want %q", rig.fake.provisionAddr, setupDeviceOne)
	}
	msg, _ := run["message"].(string)
	if !strings.Contains(msg, "fetching from this Mac") {
		t.Errorf("success message = %q, want it to say the device is now fetching", msg)
	}
	if _, ok := run["error"]; ok {
		t.Error("a successful run carries an error field")
	}
}

// TestSetupProvisionRefusesAnAddressNobodySaw is rule 5: the address must come
// from a scan this agent actually ran.
func TestSetupProvisionRefusesAnAddressNobodySaw(t *testing.T) {
	rig := newDefaultSetupRig(t)
	rig.scan(t)

	rec := rig.do(http.MethodPost, setupProvisionPath, `{"addr":"`+setupDeviceTwo+`"}`, setupLoopback, setupDashToken)
	if rec.Code != http.StatusConflict {
		t.Fatalf("unknown addr: status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	rig.setup.wg.Wait()
	if rig.fake.provisions != 0 {
		t.Fatal("an unseen address reached the Provisioner")
	}
}

func TestSetupProvisionRefusesAMalformedAddress(t *testing.T) {
	rig := newDefaultSetupRig(t)
	rig.scan(t)

	rec := rig.do(http.MethodPost, setupProvisionPath, `{"addr":"not an address!"}`, setupLoopback, setupDashToken)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed addr: status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestSetupStaleScanRefusesProvision: a sighting three minutes old is not a
// sighting. The owner is told to look again rather than being connected to
// whatever now answers at that address.
func TestSetupStaleScanRefusesProvision(t *testing.T) {
	rig := newDefaultSetupRig(t)
	rig.scan(t)

	rig.now = rig.now.Add(setupScanTTL + time.Second)

	rec := rig.do(http.MethodPost, setupProvisionPath, `{"addr":"`+setupDeviceOne+`"}`, setupLoopback, setupDashToken)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale scan: status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	st := rig.status(t)
	if _, ok := st["offer"]; ok {
		t.Error("a stale scan still offers a device")
	}
	head, _ := st["headline"].(string)
	if !strings.Contains(head, "look again") {
		t.Errorf("headline = %q, want it to ask for a fresh scan", head)
	}
}

// TestSetupOneOperationAtATime is rule 4: one radio, one operation.
func TestSetupOneOperationAtATime(t *testing.T) {
	fake := &fakeProvisioner{gate: make(chan struct{})}
	rig := newSetupRig(t, fake, fake)

	rec := rig.do(http.MethodPost, setupScanPath, `{}`, setupLoopback, setupDashToken)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("first scan: status = %d, want 202", rec.Code)
	}
	if st := rig.status(t); st["state"] != string(setupScanning) {
		t.Fatalf("state during a scan = %v, want scanning", st["state"])
	}

	rec = rig.do(http.MethodPost, setupScanPath, `{}`, setupLoopback, setupDashToken)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second scan: status = %d, want 409", rec.Code)
	}
	rec = rig.do(http.MethodPost, setupProvisionPath, `{"addr":"`+setupDeviceOne+`"}`, setupLoopback, setupDashToken)
	if rec.Code != http.StatusConflict {
		t.Fatalf("provision during a scan: status = %d, want 409", rec.Code)
	}

	close(fake.gate)
	rig.setup.wg.Wait()
	if st := rig.status(t); st["state"] != string(setupIdle) {
		t.Fatalf("state after the scan = %v, want idle", st["state"])
	}
}

// TestSetupStopCancelsTheRun: the owner closing the dialog must stop the run,
// not leave a three-minute goroutine holding the radio.
func TestSetupStopCancelsTheRun(t *testing.T) {
	fake := &fakeProvisioner{found: []FoundDevice{{Addr: setupDeviceOne, Name: "usaged-D534"}}}
	rig := newSetupRig(t, fake, fake)
	rig.scan(t)

	fake.mu.Lock()
	fake.gate = make(chan struct{})
	fake.mu.Unlock()

	rec := rig.do(http.MethodPost, setupProvisionPath, `{"addr":"`+setupDeviceOne+`"}`, setupLoopback, setupDashToken)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("provision: status = %d, want 202", rec.Code)
	}
	rec = rig.do(http.MethodDelete, setupPath, "", setupLoopback, setupDashToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("stop: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	rig.setup.wg.Wait()

	st := rig.status(t)
	if st["state"] != string(setupIdle) {
		t.Errorf("state after stop = %v, want idle", st["state"])
	}
	run := runBlock(t, st)
	if run["state"] != string(runCancelled) {
		t.Errorf("run state after stop = %v, want cancelled", run["state"])
	}
}

func TestSetupStopWithNothingRunningIsAConflict(t *testing.T) {
	rig := newDefaultSetupRig(t)
	rec := rig.do(http.MethodDelete, setupPath, "", setupLoopback, setupDashToken)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stop with nothing running: status = %d, want 409", rec.Code)
	}
}

// --- Failure: the reason and what to do next ---

// TestSetupJoinFailedReadsAsASentenceAboutTheirNetwork is the case the owner
// will actually hit, and docs/BLE_PROVISIONING.md is explicit that it must
// never render as a code.
func TestSetupJoinFailedReadsAsASentenceAboutTheirNetwork(t *testing.T) {
	fake := &fakeProvisioner{
		found:        []FoundDevice{{Addr: setupDeviceOne, Name: "usaged-D534"}},
		provisionErr: &ProvisionFailure{DeviceCode: 24},
	}
	rig := newSetupRig(t, fake, fake)
	rig.scan(t)

	run := runBlock(t, rig.provision(t, setupDeviceOne))
	if run["state"] != string(runFailed) {
		t.Fatalf("run state = %v, want failed", run["state"])
	}
	reason, _ := run["error"].(string)
	if !strings.Contains(reason, "could not join your Wi-Fi") {
		t.Errorf("reason = %q, want a sentence about their network", reason)
	}
	next, _ := run["next"].(string)
	if !strings.Contains(next, "closer to the router") {
		t.Errorf("next = %q, want it to say what to do", next)
	}
	if code, _ := run["device_code"].(float64); int(code) != 24 {
		t.Errorf("device_code = %v, want 24", run["device_code"])
	}
}

// TestSetupFailureAlwaysSaysWhatToDoNext walks the classifications the owner
// can reach. "It failed" is not a diagnosis.
func TestSetupFailureAlwaysSaysWhatToDoNext(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string // a phrase the reason or the next step must contain
	}{
		{"bluetooth off", &ProvisionFailure{Stage: StageConnect}, "Press the blue button"},
		{"pairing refused", &ProvisionFailure{Stage: StagePair}, "passkey"},
		{"transfer lost", &ProvisionFailure{Stage: StageTransfer}, "starts over"},
		{"never applied", &ProvisionFailure{Stage: StageApply}, "device screen"},
		{"old firmware", &ProvisionFailure{DeviceCode: 3}, "firmware"},
		{"no wifi on this mac", &ProvisionFailure{DeviceCode: 21}, "Wi-Fi network"},
		{"password too short", &ProvisionFailure{DeviceCode: 19}, "8 characters"},
		{"nvs failure", &ProvisionFailure{DeviceCode: 23}, "storage"},
		{"central bug", &ProvisionFailure{DeviceCode: 13}, "bug in usaged"},
		{"unclassified", errors.New("something went wrong"), setupGenericNext},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeProvisioner{
				found:        []FoundDevice{{Addr: setupDeviceOne, Name: "usaged-D534"}},
				provisionErr: tc.err,
			}
			rig := newSetupRig(t, fake, fake)
			rig.scan(t)

			run := runBlock(t, rig.provision(t, setupDeviceOne))
			if run["state"] != string(runFailed) {
				t.Fatalf("run state = %v, want failed", run["state"])
			}
			reason, _ := run["error"].(string)
			next, _ := run["next"].(string)
			if reason == "" || next == "" {
				t.Fatalf("failure carries reason %q and next %q — both must be present", reason, next)
			}
			if !strings.Contains(reason+" "+next, tc.want) {
				t.Errorf("reason/next = %q / %q, want them to mention %q", reason, next, tc.want)
			}
		})
	}
}

// TestSetupScanFailureIsAFixedSentence: the adapter being off is the most
// common scan failure and must read as an instruction, not a stack trace.
func TestSetupScanFailureIsAFixedSentence(t *testing.T) {
	fake := &fakeProvisioner{scanErr: errors.New("bluetooth adapter unavailable: " + setupSecret)}
	rig := newSetupRig(t, fake, fake)

	st := rig.scan(t)
	scan, _ := st["scan"].(map[string]any)
	reason, _ := scan["error"].(string)
	if !strings.Contains(reason, "could not scan") {
		t.Errorf("scan error = %q, want the fixed sentence", reason)
	}
	next, _ := scan["next"].(string)
	if !strings.Contains(next, "Bluetooth") {
		t.Errorf("scan next = %q, want it to name Bluetooth", next)
	}
	if strings.Contains(rec2str(st), setupSecret) {
		t.Fatal("the Provisioner's error text reached the response")
	}
}

// --- The credential rule ---

// TestSetupNeverRendersAProvisionerErrorVerbatim is the whole reason every
// sentence in setup.go is fixed text. A Provisioner that puts a credential in
// an error string — which is exactly how credentials escape — must not be able
// to put it on the page or in the log.
func TestSetupNeverRendersAProvisionerErrorVerbatim(t *testing.T) {
	fake := &fakeProvisioner{
		found:        []FoundDevice{{Addr: setupDeviceOne, Name: "usaged-D534"}},
		provisionErr: errors.New("join failed: ssid=HomeNet psk=" + setupSecret),
	}
	rig := newSetupRig(t, fake, fake)
	rig.scan(t)

	st := rig.provision(t, setupDeviceOne)
	body := rec2str(st)
	if strings.Contains(body, setupSecret) {
		t.Fatalf("the Provisioner's error text reached the response:\n%s", body)
	}
	if strings.Contains(body, "psk=") {
		t.Fatalf("the Provisioner's error text reached the response:\n%s", body)
	}
	if strings.Contains(rig.logs.String(), setupSecret) {
		t.Fatalf("the Provisioner's error text reached the log:\n%s", rig.logs.String())
	}
	run := runBlock(t, st)
	if run["error"] != setupGenericReason {
		t.Errorf("reason = %v, want the generic sentence %q", run["error"], setupGenericReason)
	}
}

// TestSetupCarriesNoCredentialFields walks every response this channel can
// produce and proves none of them has a field that could hold a credential.
// The design reason it passes is that no credential ever enters this package:
// the Provisioner reads the Wi-Fi password and writes it to the device.
func TestSetupCarriesNoCredentialFields(t *testing.T) {
	rig := newDefaultSetupRig(t)
	rig.scan(t)
	st := rig.provision(t, setupDeviceOne)

	banned := []string{"password", "psk", "passphrase", "token", "secret", "ssid"}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch t2 := v.(type) {
		case map[string]any:
			for k, sub := range t2 {
				for _, b := range banned {
					if strings.Contains(strings.ToLower(k), b) {
						t.Errorf("%s%s is a credential-shaped field — this channel must carry none", prefix, k)
					}
				}
				walk(prefix+k+".", sub)
			}
		case []any:
			for _, sub := range t2 {
				walk(prefix, sub)
			}
		}
	}
	walk("", st)
}

// TestSetupProgressStepsAreWhitelisted: a Provisioner narrates its run with the
// Step* constants, and ANYTHING ELSE IS DROPPED — only text chosen in setup.go
// reaches the page.
func TestSetupProgressStepsAreWhitelisted(t *testing.T) {
	fake := &fakeProvisioner{found: []FoundDevice{{Addr: setupDeviceOne, Name: "usaged-D534"}}}
	prog := &fakeProgressProvisioner{
		fakeProvisioner: fake,
		steps: []string{
			StepConnecting,
			"password is " + setupSecret, // an implementation gone wrong
			StepPairing,
			StepApplying,
		},
	}
	rig := newSetupRig(t, prog, fake)
	rig.scan(t)

	st := rig.provision(t, setupDeviceOne)
	body := rec2str(st)
	if strings.Contains(body, setupSecret) {
		t.Fatalf("an unrecognised step reached the response:\n%s", body)
	}

	run := runBlock(t, st)
	steps, _ := run["steps"].([]any)
	if len(steps) != 3 {
		t.Fatalf("recorded %d steps, want the 3 recognised ones", len(steps))
	}
	first, _ := steps[0].(map[string]any)
	if first["step"] != StepConnecting {
		t.Errorf("steps[0] = %v, want %q", first["step"], StepConnecting)
	}
	second, _ := steps[1].(map[string]any)
	text, _ := second["text"].(string)
	if !strings.Contains(text, "six digits are on the device") {
		t.Errorf("the pairing step reads %q — it must point at the device screen", text)
	}
}

// TestSetupPasskeyNoticeIsAlwaysAvailable: macOS decides when it prompts, so
// the "the digits are on the device screen" line must be there for the whole
// run rather than only at the exact moment it becomes true.
func TestSetupPasskeyNoticeIsAlwaysAvailable(t *testing.T) {
	rig := newDefaultSetupRig(t)
	st := rig.status(t)
	notice, _ := st["passkey_notice"].(string)
	if !strings.Contains(notice, "6-digit") || !strings.Contains(notice, "device's own screen") {
		t.Errorf("passkey_notice = %q, want it to say the six digits are on the device screen", notice)
	}
}

// --- Prerequisites: the ordering the owner asked to be stated ---

func TestSetupStatesThePrerequisiteOrdering(t *testing.T) {
	rig := newDefaultSetupRig(t)
	st := rig.status(t)

	pre, _ := st["prerequisites"].(map[string]any)
	if pre == nil {
		t.Fatal("status carries no prerequisites block")
	}
	if pre["agent_running"] != true {
		t.Error("agent_running is not true on a request this agent answered")
	}
	if got, _ := pre["providers_ready"].(float64); int(got) != 3 {
		t.Errorf("providers_ready = %v, want 3", pre["providers_ready"])
	}
	if got, _ := pre["providers_total"].(float64); int(got) != 5 {
		t.Errorf("providers_total = %v, want 5", pre["providers_total"])
	}
	notice, _ := pre["notice"].(string)
	if !strings.Contains(notice, "BEFORE you set the stick up") {
		t.Errorf("notice = %q, want it to state the ordering", notice)
	}

	rig.ready = setupReadiness{ProvidersReady: 0, ProvidersTotal: 5}
	pre, _ = rig.status(t)["prerequisites"].(map[string]any)
	if pre["ready"] != false {
		t.Error("ready is true with no provider reporting")
	}
}

// --- No central wired ---

func TestSetupWithoutAProvisionerSaysSoInsteadOf404(t *testing.T) {
	rig := newSetupRig(t, nil, &fakeProvisioner{})

	st := rig.status(t)
	if st["available"] != false {
		t.Error("available is not false with no Provisioner wired")
	}
	head, _ := st["headline"].(string)
	if head != setupUnavailableText {
		t.Errorf("headline = %q, want %q", head, setupUnavailableText)
	}

	for _, path := range []string{setupScanPath, setupProvisionPath} {
		rec := rig.do(http.MethodPost, path, `{"addr":"`+setupDeviceOne+`"}`, setupLoopback, setupDashToken)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("POST %s: status = %d, want 503", path, rec.Code)
		}
	}
}

// --- Rate limiting: the real values, not the test rig's ---

func TestSetupScanIsRateLimited(t *testing.T) {
	rig := newDefaultSetupRig(t)
	rig.setup.scanLimit = newRateLimiter(setupScansPerMinute, time.Minute)

	limited := false
	for i := 0; i < setupScansPerMinute+2; i++ {
		rec := rig.do(http.MethodPost, setupScanPath, `{}`, setupLoopback, setupDashToken)
		rig.setup.wg.Wait()
		if rec.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Errorf("no scan was rate limited in %d attempts", setupScansPerMinute+2)
	}
}

// --- The routes are on the real server ---

// TestSetupRoutesAreRegisteredOnTheRealServer proves the wiring in server.go is
// real: a fresh Server answers /v1/setup rather than the JSON 404.
func TestSetupRoutesAreRegisteredOnTheRealServer(t *testing.T) {
	dir := setupFixtures(t)
	handler, _, _ := newFixtureHandler(t, dir)

	req := httptest.NewRequest(http.MethodGet, setupPath, nil)
	req.RemoteAddr = setupLoopback
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s on the real server: status = %d, want 200 (body %s)", setupPath, rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["available"] != false {
		t.Errorf("available = %v, want false — no Provisioner is wired in this build", body["available"])
	}
	pre, _ := body["prerequisites"].(map[string]any)
	if pre == nil {
		t.Fatal("the real server's status carries no prerequisites block")
	}
	if _, ok := pre["providers_total"].(float64); !ok {
		t.Errorf("providers_total is not a number: %v", pre["providers_total"])
	}
}

// rec2str renders a decoded body back to JSON for substring assertions.
func rec2str(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// codedError is an implementation's OWN error type that names the device's
// error number — the adapter-free path into the same classification.
type codedError struct{ code int }

func (e *codedError) Error() string            { return "bleprov: device refused the transfer" }
func (e *codedError) ProvisionDeviceCode() int { return e.code }

// TestSetupHonoursAnImplementationsOwnCodedError: internal/bleprov has its own
// error type, and one method on it is enough to get the precise sentence — no
// translation table at the wiring point, where a forgotten case would silently
// degrade every message to the generic one.
func TestSetupHonoursAnImplementationsOwnCodedError(t *testing.T) {
	fake := &fakeProvisioner{
		found:        []FoundDevice{{Addr: setupDeviceOne, Name: "usaged-D534"}},
		provisionErr: &codedError{code: 24},
	}
	rig := newSetupRig(t, fake, fake)
	rig.scan(t)

	run := runBlock(t, rig.provision(t, setupDeviceOne))
	if code, _ := run["device_code"].(float64); int(code) != 24 {
		t.Fatalf("device_code = %v, want 24", run["device_code"])
	}
	reason, _ := run["error"].(string)
	if !strings.Contains(reason, "could not join your Wi-Fi") {
		t.Errorf("reason = %q, want the JoinFailed sentence", reason)
	}
}
