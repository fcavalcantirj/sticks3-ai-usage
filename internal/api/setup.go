package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// --- One-click device setup over BLE (the dashboard half) ---
//
// THE GOAL, IN THE OWNER'S WORDS: "once we pair the sticks3 and mac via
// bluetooth, WHY DON'T THE MAC DAEMON SEND EVERYTHING? SSID AND PASSWORD,
// EVERYTHING... DAEMON IP, PORT... IT KNOWS."  The owner types NOTHING: they
// click once on this page and, if macOS asks, type the six digits the device
// is already showing them on its own screen.
//
// The wire contract is docs/BLE_PROVISIONING.md and the device's decoder is
// firmware/src/usage/bleprov.{h,cpp}. NEITHER IS IMPLEMENTED HERE. This file
// owns the two routes, the state machine behind them, and every sentence the
// dashboard shows. The BLE central lives in internal/bleprov and reaches this
// package as a Provisioner (see below), so the two can be wired together in
// cmd/usaged with no import cycle.
//
// FIVE RULES THIS CHANNEL OBEYS
//
//  1. NO CREDENTIAL EVER PASSES THROUGH HERE. The Wi-Fi SSID, the Wi-Fi
//     password, the agent's address and the device token are read and sent by
//     the Provisioner. Nothing on this side of the interface holds one, which
//     is the cheapest possible way to keep the rule that no credential value is
//     ever logged or rendered: there is nothing to leak.
//
//  2. EVERY SENTENCE THE PAGE SHOWS IS FIXED TEXT CHOSEN IN THIS FILE. A
//     Provisioner's error VALUE is never rendered and never logged — only its
//     classification (a *ProvisionFailure, whose fields are a number and an
//     enum string) selects a sentence. This is exactly the discipline
//     bleprov::errorText() and portal::rejectText() obey on the device, and for
//     the same reason: an error string is the easiest way for a credential to
//     escape. An implementation that returns a plain error gets the honest
//     generic sentence, never its own text.
//
//  3. THESE ROUTES ARE LOOPBACK-ONLY, NOT TOKEN-GATED — the one place ORDER
//     #54 does not apply. That rule (a device token even from loopback) made
//     this feature impossible: the flow exists to GIVE a device a token, and on
//     a fresh install none is configured, so every button returned 401 and
//     zero-config could not begin. Measured 2026-09-07 before the change: GET
//     /v1/setup 200, POST /v1/setup/scan 401.
//
//     What authorises a run is not a header. It is the BLE bond — LE Secure
//     Connections with MITM protection and a six-digit passkey the owner reads
//     off the DEVICE'S OWN SCREEN. The device refuses every unpaired write, and
//     that is verified on hardware, not assumed: the controller logs
//     GATT_INSUF_AUTHENTICATION. A caller on loopback already runs on the Mac
//     that holds the Wi-Fi password; the passkey is what decides WHICH device
//     receives it.
//
//     The LAN still needs the token, so a machine on the same Wi-Fi cannot make
//     this Mac provision a device of its choosing. See auth.go isSetupPath.
//
//  4. ONE OPERATION AT A TIME. A scan and a provisioning run share one radio,
//     and a second run against a half-provisioned device is a way to strand it.
//     A second request gets 409 with the current state, never a queue.
//
//  5. A DEVICE CAN ONLY BE PROVISIONED IF THIS AGENT SAW IT ADVERTISING, and
//     recently. The address must come from the last scan and that scan must be
//     fresh, so a stale page cannot aim the daemon at an address nobody has
//     seen.

// --- the interface internal/bleprov implements -------------------------------

// Provisioner is the BLE central: it finds StickS3s that are advertising the
// usaged provisioning service and hands one everything it needs to join the
// network and reach this agent.
//
// It is declared HERE, in the consumer, so internal/bleprov can import
// internal/api for these types while internal/api never imports internal/bleprov
// — no cycle, and the tests in this package drive a fake.
//
// Both methods are LONG-RUNNING and MUST honour ctx: a scan takes seconds, and
// a provisioning run is gated on a human reading six digits off a screen. The
// caller always supplies a deadline and cancels on the owner's "Stop".
//
// Neither method takes credentials. The implementation is what knows the Mac's
// SSID, its Wi-Fi password, this agent's address and port, and the device token
// to mint — that is the whole point of the design, and keeping those values out
// of this package is what makes rule 1 above checkable.
type Provisioner interface {
	// Scan returns the devices advertising the provisioning service, or an
	// error if the adapter is unavailable. An empty slice is not an error.
	Scan(ctx context.Context) ([]FoundDevice, error)

	// Provision connects to addr, pairs (which is what makes macOS prompt for
	// the passkey shown on the device screen), writes the record and waits for
	// the device to report that it saved and joined. It returns nil ONLY when
	// the device reported Applied.
	Provision(ctx context.Context, addr string) error
}

// ProgressProvisioner is OPTIONAL. An implementation that can narrate its own
// run — the central knows when it is connecting, when the passkey prompt is
// pending, and what the device's Status characteristic says — implements this
// as well, and the dashboard shows the steps as they happen. An implementation
// that does not is used through Provisioner alone and the page shows the coarse
// phases this package can observe on its own.
//
// report may be called from any goroutine and must be called with one of the
// Step* constants below; ANY OTHER VALUE IS IGNORED. That is deliberate: only
// text chosen in this file ever reaches the page (rule 2).
type ProgressProvisioner interface {
	Provisioner
	ProvisionProgress(ctx context.Context, addr string, report func(step string)) error
}

// TokenIssuerAware is OPTIONAL, and is how a Provisioner gets the one thing it
// cannot mint for itself: a device token this agent will actually ACCEPT.
//
// bleprov.MintToken() produces 128 bits of entropy, which is a token nothing
// recognises. What makes a token real is being RECORDED in the paired-device
// store, and that store is unexported — New returns an *http.Server, so
// cmd/usaged never holds a *Server and cannot reach it. So the Server pushes
// the capability instead: a Provisioner that implements this is handed a
// mint-and-record function during New.
//
// The BLE path needs it because it is the inverse of the HTTP one: the device
// is GIVEN its token over a bonded GATT link before it has ever joined Wi-Fi,
// so it can never POST /v1/pair/claim to collect one.
//
// deviceID keys the paired-device record — the advertised "usaged-XXXX"
// identity is the natural choice, since one device has one such name however
// it is set up. name is a human label and may be empty.
type TokenIssuerAware interface {
	SetTokenIssuer(issue func(deviceID, name string) (string, error))
}

// NetworkNamer is OPTIONAL: an implementation that can say which Wi-Fi network
// this Mac is on. The dashboard uses it to PRE-FILL the network name instead of
// asking someone to type a name their own computer already knows.
//
// An SSID is not a credential — the access point broadcasts it continuously to
// anyone listening — so unlike everything else the Provisioner handles, it is
// safe to render. The password is not, and is never returned here.
//
// Confident is false when macOS refused to disclose the association and the
// name is a best guess from the preferred-network list (macOS 14+ redacts the
// SSID for a process without Location Services authorisation). The page says so
// rather than presenting a guess as an observation.
type NetworkNamer interface {
	CurrentNetwork(ctx context.Context) (ssid string, confident bool)
}

// FoundDevice is one StickS3 seen in a scan. Every string in it was broadcast
// by something in radio range, so it is treated as untrusted input and cleaned
// before it is stored, logged or rendered.
type FoundDevice struct {
	// Addr is the opaque address the central connects to — a CoreBluetooth
	// peripheral UUID on macOS. It is the handle the dashboard sends back.
	Addr string
	// Name is the advertised local name, e.g. "usaged-D534".
	Name string
	// RSSI is the signal strength in dBm (negative; closer to zero is nearer).
	RSSI int
	// MAC comes from the Info characteristic, which is readable without
	// pairing. Empty when Info was not read.
	MAC string
	// Provisioned is Info's flags bit 0: the device already holds a record.
	Provisioned bool
	// Version and Capacity come from Info too. Capacity 0 means Info was not
	// read, NOT that the device has no buffer.
	Version  int
	Capacity int
}

// ProvisionFailure is the classification of a failed run. A Provisioner returns
// one (bare or wrapped) so the dashboard can say what went wrong and what to do
// next WITHOUT any implementation text reaching the page.
//
// It carries no free text on purpose — see rule 2. Error() itself returns the
// fixed sentence this file would render, so a plain %v of it is safe too.
type ProvisionFailure struct {
	// DeviceCode is the device's own error number from
	// docs/BLE_PROVISIONING.md section 7 (bleprov::Error), or 0 when the run
	// failed before the device could answer.
	DeviceCode int
	// Stage names WHERE a run failed when DeviceCode is 0: one of the Stage*
	// constants. An unknown stage renders the generic sentence.
	Stage string
}

func (e *ProvisionFailure) Error() string { return provisionReason(e) }

// ProvisionCoded is the second, adapter-free way for a Provisioner to be
// precise about a device refusal: an error type of its own that can name the
// device's error number implements this one method and is classified exactly
// like a *ProvisionFailure. It exists so internal/bleprov's own error type does
// not have to be translated at the wiring point, where a forgotten case would
// silently degrade every message to the generic sentence.
type ProvisionCoded interface {
	// ProvisionDeviceCode returns the device's error number from
	// docs/BLE_PROVISIONING.md section 7 (bleprov::Error), or 0 when the device
	// never answered.
	ProvisionDeviceCode() int
}

// The steps a ProgressProvisioner may report. Anything else is ignored.
const (
	StepConnecting  = "connecting"
	StepReadingInfo = "reading_info"
	StepPairing     = "pairing" // THE passkey moment — the six digits are on the device
	StepSending     = "sending"
	StepApplying    = "applying"
)

// The stages a run can fail at when the device itself never answered.
const (
	StageScan     = "scan"     // the adapter is off, or the scan itself failed
	StageConnect  = "connect"  // could not reach the device
	StagePair     = "pair"     // bonding did not complete
	StageTransfer = "transfer" // the record did not land
	StageApply    = "apply"    // the device took it and never reported Applied
)

// --- state -------------------------------------------------------------------

// setupPhase is what the channel is doing right now. The OUTCOME of the last
// run is kept separately, in setupRun, so a finished run still explains itself
// after the phase has gone back to idle.
type setupPhase string

const (
	setupIdle         setupPhase = "idle"
	setupScanning     setupPhase = "scanning"
	setupProvisioning setupPhase = "provisioning"
)

// setupRunState is the outcome of one provisioning run.
type setupRunState string

const (
	runRunning   setupRunState = "running"
	runApplied   setupRunState = "applied"
	runFailed    setupRunState = "failed"
	runCancelled setupRunState = "cancelled"
)

const (
	// setupScanTimeout bounds a scan. tinygo.org/x/bluetooth scans until it is
	// stopped, so the deadline is what ends it; 12 s is comfortably longer than
	// the 6 s that found 60 devices on this Mac.
	setupScanTimeout = 12 * time.Second

	// setupProvisionTimeout must outlast a human reading six digits off a
	// 1.14" screen and typing them into a macOS prompt. Three minutes is the
	// same budget the pairing window gets, for the same reason.
	setupProvisionTimeout = 3 * time.Minute

	// setupScanTTL is how long a scan result may be used. A device that was in
	// range five minutes ago may be asleep, moved or already set up, and
	// provisioning against a stale list is how a daemon ends up talking to the
	// wrong thing.
	setupScanTTL = 3 * time.Minute

	// setupMaxFound bounds what one scan can put on the page. The advertising
	// names are written by whatever is in radio range.
	setupMaxFound = 16
	// setupMaxNameLen matches pairMaxNameLen: a cosmetic, cleaned string.
	setupMaxNameLen = 40
	// setupMaxSteps bounds the progress list a Provisioner can build up.
	setupMaxSteps = 24

	// Both limiters guard a human clicking a button, like /v1/keys and the
	// pairing confirm. A scan gets a little more room because the page runs one
	// on its own when the Settings tab is first opened.
	setupScansPerMinute      = 10
	setupProvisionsPerMinute = 5
)

// Route paths. The status route is a GET beside the two mutating actions, the
// same shape /v1/netcfg uses.
const (
	setupPath          = "/v1/setup"
	setupScanPath      = "/v1/setup/scan"
	setupProvisionPath = "/v1/setup/provision"
)

// setupReadiness is the answer to "is this Mac ready to set a device up?" — the
// ORDERING the owner asked for. The device is a MIRROR of this agent: it shows
// what usaged already knows, so usaged must be running and the providers must
// be configured BEFORE a stick is set up, or the owner ends up looking at a
// device that works perfectly and displays nothing.
type setupReadiness struct {
	ProvidersReady int // enabled providers currently reporting
	ProvidersTotal int // enabled providers configured
}

// setupScan is the result of the last scan.
type setupScan struct {
	Found  []FoundDevice
	Reason string // fixed sentence when the scan failed; "" when it did not
	Next   string // what to do about that failure
	At     time.Time
}

// setupRun is one provisioning run, current or last.
type setupRun struct {
	Addr       string
	Name       string
	State      setupRunState
	Step       string   // the last honoured Step* constant, "" before the first
	Steps      []string // the honoured steps in order
	StepAt     []time.Time
	Reason     string // fixed sentence, only when State is failed
	Next       string // what to do next, only when State is failed
	DeviceCode int    // the device's error number, 0 when it never answered
	StartedAt  time.Time
	EndedAt    time.Time
}

// setup is the whole channel: one scan result, one run, one operation at a
// time. Nothing here is persisted — a scan is a snapshot of the radio and a run
// is over when the process ends.
type setup struct {
	mu    sync.Mutex
	phase setupPhase
	scan  *setupScan
	run   *setupRun

	// gen rises on every operation so a goroutine that finishes late cannot
	// write over the run that replaced it.
	gen    int64
	cancel context.CancelFunc

	// wg tracks the in-flight goroutine. Tests wait on it instead of sleeping.
	wg sync.WaitGroup

	prov  Provisioner // nil when no central is wired: the routes say so
	ready func() setupReadiness

	now            func() time.Time
	logger         *slog.Logger
	scanLimit      *rateLimiter
	provisionLimit *rateLimiter
}

// newSetup builds the setup channel. prov may be nil (no BLE central wired),
// in which case the routes answer "not available" rather than 404 — the
// dashboard can then explain itself instead of showing a dead button.
func newSetup(prov Provisioner, ready func() setupReadiness, now func() time.Time, logger *slog.Logger) *setup {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	if ready == nil {
		ready = func() setupReadiness { return setupReadiness{} }
	}
	return &setup{
		phase:          setupIdle,
		prov:           prov,
		ready:          ready,
		now:            now,
		logger:         logger,
		scanLimit:      newRateLimiter(setupScansPerMinute, time.Minute),
		provisionLimit: newRateLimiter(setupProvisionsPerMinute, time.Minute),
	}
}

// routes registers the three endpoints. The GET follows the ordinary rule
// (loopback exempt, LAN needs the token); the two POSTs and the DELETE are
// mutating, so the token is required even from loopback.
func (s *setup) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+setupPath, s.handleStatus)
	mux.HandleFunc("DELETE "+setupPath, s.handleStop)
	mux.HandleFunc("POST "+setupScanPath, s.handleScan)
	mux.HandleFunc("POST "+setupProvisionPath, s.handleProvision)
}

// handleStatus is everything the dashboard needs to render the card: whether
// the feature is wired, what the prerequisites look like, what the last scan
// found, and how the run is going. It discloses nothing — rule 1 leaves it
// nothing to disclose.
func (s *setup) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	noStore(w)
	view := s.statusViewLocked()
	// The Mac's own network name, so the dashboard can pre-fill it. Looked up
	// per request rather than cached: someone moves between networks, and a
	// stale name here would be sent to a device that then cannot join.
	if namer, ok := s.prov.(NetworkNamer); ok && namer != nil {
		if ssid, confident := namer.CurrentNetwork(r.Context()); ssid != "" {
			view["network"] = map[string]any{"ssid": ssid, "confident": confident}
		}
	}
	writeJSON(w, http.StatusOK, view)
}

// handleScan starts a scan. It returns immediately with the state: a scan takes
// seconds and the server's WriteTimeout is 10 s, so a synchronous handler would
// be racing its own deadline. The page polls GET /v1/setup for the result.
func (s *setup) handleScan(w http.ResponseWriter, r *http.Request) {
	if s.prov == nil {
		s.writeUnavailable(w)
		return
	}
	if !s.scanLimit.allow() {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": "rate limit exceeded"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != setupIdle {
		s.writeBusyLocked(w)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), setupScanTimeout)
	s.phase = setupScanning
	s.cancel = cancel
	s.gen++
	gen := s.gen
	s.logger.Info("setup: scanning for devices", "timeout_sec", int(setupScanTimeout.Seconds()))

	s.wg.Add(1)
	go s.runScan(ctx, cancel, gen)

	noStore(w)
	writeJSON(w, http.StatusAccepted, s.okViewLocked())
}

// runScan performs the scan and records what it found. It NEVER logs or stores
// the error value it got back — only the stage it failed at.
func (s *setup) runScan(ctx context.Context, cancel context.CancelFunc, gen int64) {
	defer s.wg.Done()
	defer cancel()

	found, err := s.prov.Scan(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != gen {
		return // superseded or stopped: this result is no longer wanted
	}
	s.phase = setupIdle
	s.cancel = nil

	if err != nil {
		fail := classifyFailure(err, StageScan)
		s.scan = &setupScan{
			Reason: provisionReason(fail),
			Next:   provisionNext(fail),
			At:     s.now(),
		}
		s.logger.Warn("setup: scan failed", "stage", fail.Stage, "device_code", fail.DeviceCode)
		return
	}

	clean := cleanFound(found)
	s.scan = &setupScan{Found: clean, At: s.now()}
	s.logger.Info("setup: scan finished", "found", len(clean), "candidates", len(candidates(clean)))
}

// handleProvision starts a run against one device found by the last scan.
//
// The address is checked against that scan (rule 5) BEFORE anything is started,
// so the daemon cannot be aimed at an address nobody has seen, and a page left
// open from yesterday gets a clear "look again" instead of a connection attempt.
func (s *setup) handleProvision(w http.ResponseWriter, r *http.Request) {
	if s.prov == nil {
		s.writeUnavailable(w)
		return
	}
	if !s.provisionLimit.allow() {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": "rate limit exceeded"})
		return
	}

	var body struct {
		Addr string `json:"addr"`
	}
	if err := decodeSetupBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	addr := sanitizePairID(body.Addr)
	if addr == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok": false, "error": "addr must be up to 64 characters of letters, digits, . : - _",
		})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != setupIdle {
		s.writeBusyLocked(w)
		return
	}
	if s.scan == nil || s.staleLocked() {
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":    false,
			"state": s.phase,
			"error": "look for the device again first — this agent has no recent sighting of it",
		})
		return
	}
	dev, ok := s.deviceLocked(addr)
	if !ok {
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":    false,
			"state": s.phase,
			"error": "that device was not in the last scan — look again",
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), setupProvisionTimeout)
	s.phase = setupProvisioning
	s.cancel = cancel
	s.gen++
	gen := s.gen
	s.run = &setupRun{
		Addr:      dev.Addr,
		Name:      dev.Name,
		State:     runRunning,
		StartedAt: s.now(),
	}
	// The device identity is not a secret — the name is broadcast in every
	// advertising packet and the MAC is in Info, which is readable unpaired.
	// Nothing else about this run is loggable, and nothing else is logged.
	s.logger.Info("setup: provisioning device",
		"name", dev.Name, "mac", dev.MAC, "timeout_sec", int(setupProvisionTimeout.Seconds()))

	s.wg.Add(1)
	go s.runProvision(ctx, cancel, gen, dev.Addr)

	noStore(w)
	writeJSON(w, http.StatusAccepted, s.okViewLocked())
}

// runProvision drives the Provisioner and records the outcome. A Provisioner
// that implements ProgressProvisioner narrates itself; one that does not is
// simply run to completion.
func (s *setup) runProvision(ctx context.Context, cancel context.CancelFunc, gen int64, addr string) {
	defer s.wg.Done()
	defer cancel()

	err := s.callProvisioner(ctx, gen, addr)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != gen || s.run == nil {
		return // superseded or stopped
	}
	s.phase = setupIdle
	s.cancel = nil
	s.run.EndedAt = s.now()

	if err == nil {
		s.run.State = runApplied
		s.logger.Info("setup: device provisioned", "name", s.run.Name)
		return
	}
	if errors.Is(err, context.Canceled) {
		s.run.State = runCancelled
		s.logger.Info("setup: run stopped", "name", s.run.Name)
		return
	}

	fail := classifyFailure(err, "")
	s.run.State = runFailed
	s.run.Reason = provisionReason(fail)
	s.run.Next = provisionNext(fail)
	s.run.DeviceCode = fail.DeviceCode
	// The error VALUE is not logged, only its classification (rule 2). The
	// Provisioner logs its own detail under its own rules.
	s.logger.Warn("setup: provisioning failed",
		"name", s.run.Name, "stage", fail.Stage, "device_code", fail.DeviceCode)
}

// callProvisioner runs the Provisioner and turns a PANIC INTO AN ERROR.
//
// A PROVISIONER MUST NOT BE ABLE TO KILL THE AGENT. runProvision runs in its
// own goroutine, where an unrecovered panic takes the whole process down — and
// on 2026-09-07 one did: the BLE library panicked on a zero device it had
// itself handed back, launchd restarted the daemon, and the dashboard showed
// the run simply vanish with no error at all. Every quota reading went with it.
//
// Returning an error rather than recovering at the call site keeps the outcome
// on the one path that records it, so a panic reaches the owner exactly like
// any other failed run. internal/bleprov recovers too; this is the belt to that
// brace, because Provisioner is a public interface and the next implementation
// will not remember.
func (s *setup) callProvisioner(ctx context.Context, gen int64, addr string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("setup: provisioner panicked", "recovered", fmt.Sprint(r))
			err = &ProvisionFailure{Stage: StageApply}
		}
	}()
	if p, ok := s.prov.(ProgressProvisioner); ok {
		return p.ProvisionProgress(ctx, addr, func(step string) { s.recordStep(gen, step) })
	}
	return s.prov.Provision(ctx, addr)
}

// recordStep honours a progress report from the Provisioner. An unrecognised
// step is DROPPED rather than shown: only text chosen in this file reaches the
// page.
func (s *setup) recordStep(gen int64, step string) {
	if setupStepText(step) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != gen || s.run == nil || s.run.State != runRunning {
		return
	}
	s.run.Step = step
	if len(s.run.Steps) < setupMaxSteps {
		s.run.Steps = append(s.run.Steps, step)
		s.run.StepAt = append(s.run.StepAt, s.now())
	}
}

// handleStop cancels whatever is in flight. It is what the owner's "Stop" does,
// and it is the daemon-side half of the ABORT the wire contract describes for
// "the owner closed the dialog".
func (s *setup) handleStop(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.phase == setupIdle || s.cancel == nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok": false, "state": s.phase, "error": "nothing is running",
		})
		return
	}
	was := s.phase
	s.cancel()
	s.cancel = nil
	s.phase = setupIdle
	// Retire the goroutine's claim on this run so a late result cannot revive
	// a card the owner has already dismissed.
	s.gen++
	if was == setupProvisioning && s.run != nil && s.run.State == runRunning {
		s.run.State = runCancelled
		s.run.EndedAt = s.now()
	}
	s.logger.Info("setup: stopped", "was", string(was))

	noStore(w)
	writeJSON(w, http.StatusOK, s.okViewLocked())
}

// writeUnavailable is the answer when no BLE central is wired into this build.
// 503 rather than 404: the route exists, the capability does not.
func (s *setup) writeUnavailable(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"ok":        false,
		"available": false,
		"error":     setupUnavailableText,
	})
}

// writeBusyLocked refuses a second operation while one is in flight (rule 4).
func (s *setup) writeBusyLocked(w http.ResponseWriter) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"ok":    false,
		"state": s.phase,
		"error": "one thing at a time — a scan or a setup is already running",
	})
}

func (s *setup) okViewLocked() map[string]any {
	v := s.statusViewLocked()
	v["ok"] = true
	return v
}

// --- the view ---------------------------------------------------------------

// setupNotice is the ORDERING the owner asked for, and it is server-owned copy
// rather than page text for the same reason GOLDEN_RULES #3 means a curl of
// this endpoint gets the complete answer, including the one thing a first-time
// user must be told BEFORE they set a device up.
const setupNotice = "Set this Mac up first. usaged must be running and your AI providers " +
	"configured here BEFORE you set the stick up — the stick is a mirror: it only ever " +
	"shows what this Mac already reports."

// setupPasskeyNotice is shown for the whole run, not only at the pairing step:
// macOS decides when to prompt, and a notice that appears only at the exact
// moment is a notice the owner misses.
const setupPasskeyNotice = "If macOS asks for a 6-digit passkey, those six digits are on the " +
	"device's own screen. Type them exactly; nothing has to be typed anywhere else."

const setupUnavailableText = "Bluetooth setup is not available in this build of usaged."

// statusViewLocked builds the whole card in one object. Every sentence in it is
// fixed text from this file (rule 2).
func (s *setup) statusViewLocked() map[string]any {
	ready := s.ready()
	v := map[string]any{
		"available":             s.prov != nil,
		"state":                 string(s.phase),
		"headline":              s.headlineLocked(),
		"passkey_notice":        setupPasskeyNotice,
		"scan_ttl_sec":          int(setupScanTTL.Seconds()),
		"scan_timeout_sec":      int(setupScanTimeout.Seconds()),
		"provision_timeout_sec": int(setupProvisionTimeout.Seconds()),
		"prerequisites": map[string]any{
			// This process answered the request, so the first prerequisite is
			// met by definition. Saying so is not padding: it is half of the
			// ordering the owner asked to see stated.
			"agent_running":   true,
			"providers_ready": ready.ProvidersReady,
			"providers_total": ready.ProvidersTotal,
			"ready":           ready.ProvidersReady > 0,
			"notice":          setupNotice,
		},
		"scan": s.scanViewLocked(),
		"run":  s.runViewLocked(),
	}
	if offer := s.offerLocked(); offer != nil {
		v["offer"] = offer
	}
	return v
}

func (s *setup) scanViewLocked() map[string]any {
	if s.scan == nil {
		return nil
	}
	found := make([]map[string]any, 0, len(s.scan.Found))
	for _, d := range s.scan.Found {
		e := map[string]any{
			"addr":        d.Addr,
			"name":        d.Name,
			"rssi":        d.RSSI,
			"provisioned": d.Provisioned,
		}
		if d.MAC != "" {
			e["mac"] = d.MAC
		}
		if d.Capacity > 0 {
			e["version"] = d.Version
			e["capacity"] = d.Capacity
		}
		found = append(found, e)
	}
	v := map[string]any{
		"at":            s.scan.At.Unix(),
		"seconds_since": int64(s.now().Sub(s.scan.At).Seconds()),
		"stale":         s.staleLocked(),
		"found":         found,
		"candidates":    len(candidates(s.scan.Found)),
	}
	if s.scan.Reason != "" {
		v["error"] = s.scan.Reason
		v["next"] = s.scan.Next
	}
	return v
}

func (s *setup) runViewLocked() map[string]any {
	if s.run == nil {
		return nil
	}
	steps := make([]map[string]any, 0, len(s.run.Steps))
	for i, st := range s.run.Steps {
		e := map[string]any{"step": st, "text": setupStepText(st)}
		if i < len(s.run.StepAt) {
			e["at"] = s.run.StepAt[i].Unix()
		}
		steps = append(steps, e)
	}
	v := map[string]any{
		"addr":        s.run.Addr,
		"name":        s.run.Name,
		"state":       string(s.run.State),
		"started_at":  s.run.StartedAt.Unix(),
		"elapsed_sec": s.runElapsedLocked(),
		"message":     s.runMessageLocked(),
		"steps":       steps,
	}
	if s.run.Step != "" {
		v["step"] = s.run.Step
		v["step_text"] = setupStepText(s.run.Step)
	}
	if !s.run.EndedAt.IsZero() {
		v["finished_at"] = s.run.EndedAt.Unix()
	}
	if s.run.State == runFailed {
		v["error"] = s.run.Reason
		v["next"] = s.run.Next
		if s.run.DeviceCode > 0 {
			// The number is the stable half of the contract
			// (docs/BLE_PROVISIONING.md section 7); the sentence above is what
			// the owner reads.
			v["device_code"] = s.run.DeviceCode
		}
	}
	return v
}

// runMessageLocked is the one-line state of the run, in the owner's terms.
func (s *setup) runMessageLocked() string {
	switch s.run.State {
	case runRunning:
		if t := setupStepText(s.run.Step); t != "" {
			return t
		}
		return "Setting the device up…"
	case runApplied:
		return "Done. The device joined your Wi-Fi, has a token of its own, and is " +
			"fetching from this Mac now."
	case runCancelled:
		return "Stopped. The device was left as it was; you can set it up again."
	default:
		return s.run.Reason
	}
}

func (s *setup) runElapsedLocked() int {
	end := s.run.EndedAt
	if end.IsZero() {
		end = s.now()
	}
	d := end.Sub(s.run.StartedAt)
	if d < 0 {
		return 0
	}
	return int(d / time.Second)
}

// offerLocked is the ONE device the page puts its single button on: the nearest
// unprovisioned StickS3 from a fresh scan. Choosing it here rather than on the
// page is GOLDEN_RULES #3 — a curl gets the same answer the dashboard acts on.
func (s *setup) offerLocked() map[string]any {
	if s.phase != setupIdle || s.scan == nil || s.staleLocked() {
		return nil
	}
	c := candidates(s.scan.Found)
	if len(c) == 0 {
		return nil
	}
	return map[string]any{
		"addr":     c[0].Addr,
		"name":     c[0].Name,
		"headline": setupOfferHeadline,
	}
}

const setupOfferHeadline = "New StickS3 found — set it up?"

// headlineLocked is the sentence at the top of the card. It is the single place
// that decides what the owner is being asked to do next.
func (s *setup) headlineLocked() string {
	if s.prov == nil {
		return setupUnavailableText
	}
	switch s.phase {
	case setupScanning:
		return "Looking for a StickS3 nearby…"
	case setupProvisioning:
		if s.run != nil && s.run.Name != "" {
			return "Setting up " + s.run.Name + "…"
		}
		return "Setting the device up…"
	}
	if s.scan == nil {
		return "Press the blue button on a new StickS3 so it wakes up, then look for it."
	}
	if s.scan.Reason != "" {
		return s.scan.Reason
	}
	if s.staleLocked() {
		return "That scan is a few minutes old — look again before setting a device up."
	}
	c := candidates(s.scan.Found)
	switch {
	case len(c) == 1:
		return setupOfferHeadline
	case len(c) > 1:
		return "More than one new StickS3 is nearby — pick the one you are holding."
	case len(s.scan.Found) > 0:
		return "The StickS3 nearby is already set up. Hold its blue button to let it be set up again."
	default:
		return "No new StickS3 nearby. Press the blue button on the device and look again."
	}
}

func (s *setup) staleLocked() bool {
	return s.scan == nil || s.now().Sub(s.scan.At) > setupScanTTL
}

// deviceLocked finds a device from the last scan by address.
func (s *setup) deviceLocked(addr string) (FoundDevice, bool) {
	if s.scan == nil {
		return FoundDevice{}, false
	}
	for _, d := range s.scan.Found {
		if d.Addr == addr {
			return d, true
		}
	}
	return FoundDevice{}, false
}

// --- pure helpers ------------------------------------------------------------

// cleanFound sanitises and orders a scan result. EVERY string here was
// broadcast by something in radio range, so the name is cleaned exactly like a
// device's self-reported name and the address must survive the same character
// rule as a device id — an entry that does not is dropped rather than rendered.
func cleanFound(in []FoundDevice) []FoundDevice {
	out := make([]FoundDevice, 0, len(in))
	seen := map[string]bool{}
	for _, d := range in {
		addr := sanitizePairID(d.Addr)
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		out = append(out, FoundDevice{
			Addr:        addr,
			Name:        netcfgClean(d.Name, setupMaxNameLen),
			RSSI:        d.RSSI,
			MAC:         sanitizePairID(d.MAC),
			Provisioned: d.Provisioned,
			Version:     d.Version,
			Capacity:    d.Capacity,
		})
		if len(out) == setupMaxFound {
			break
		}
	}
	// Unprovisioned first (those are the ones worth offering), then the
	// strongest signal, which on this page means "the one in your hand".
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Provisioned != out[j].Provisioned {
			return !out[i].Provisioned
		}
		if out[i].RSSI != out[j].RSSI {
			return out[i].RSSI > out[j].RSSI
		}
		return out[i].Addr < out[j].Addr
	})
	return out
}

// candidates are the devices worth offering: the ones that say they hold no
// record yet. A device that is already provisioned is left alone — re-running
// setup on a working device is how a working device stops working.
func candidates(found []FoundDevice) []FoundDevice {
	out := make([]FoundDevice, 0, len(found))
	for _, d := range found {
		if !d.Provisioned {
			out = append(out, d)
		}
	}
	return out
}

// classifyFailure turns any error into the classification the page renders. A
// Provisioner that returns a *ProvisionFailure gets the precise sentence; one
// that returns anything else gets the honest generic one and its text is
// discarded (rule 2).
func classifyFailure(err error, fallbackStage string) *ProvisionFailure {
	var pf *ProvisionFailure
	if errors.As(err, &pf) && pf != nil {
		return pf
	}
	var pc ProvisionCoded
	if errors.As(err, &pc) && pc != nil {
		return &ProvisionFailure{DeviceCode: pc.ProvisionDeviceCode(), Stage: fallbackStage}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &ProvisionFailure{Stage: stageTimeout}
	}
	return &ProvisionFailure{Stage: fallbackStage}
}

// stageTimeout is internal: a Provisioner reports the stage it failed at, and
// this package recognises its own deadline separately.
const stageTimeout = "timeout"

// provisionReason is the sentence the owner reads. It is chosen ENTIRELY from
// the classification — never from the error's own text — which is the same rule
// bleprov::errorText() obeys on the device.
//
// The device codes are docs/BLE_PROVISIONING.md section 7, whose numbers are
// stable across firmware versions precisely so this switch can exist.
func provisionReason(f *ProvisionFailure) string {
	if f == nil {
		return setupGenericReason
	}
	switch f.DeviceCode {
	case 0:
		// No answer from the device: the stage is all we know.
		switch f.Stage {
		case StageScan:
			return "This Mac could not scan for devices."
		case StageConnect:
			return "Could not connect to the device."
		case StagePair:
			return "Pairing did not complete."
		case StageTransfer:
			return "The settings did not reach the device."
		case StageApply:
			return "The device took the settings but never confirmed it applied them."
		case stageTimeout:
			return "Setup ran out of time."
		default:
			return setupGenericReason
		}
	case 3, 14:
		return "This device's firmware is older than usaged and does not understand the settings it was sent."
	case 5, 17:
		return "The settings did not fit this device."
	case 16, 21, 22:
		return "This Mac is not on a Wi-Fi network it can share with the device."
	case 18:
		return "Your network name or password contains a character the device cannot store."
	case 19:
		return "Your Wi-Fi password is shorter than 8 characters, which the device's radio refuses outright."
	case 23:
		return "The device could not save the settings to its own storage."
	case 24:
		// The one the owner will actually hit, and the doc is explicit that it
		// must read as a sentence about their network, never as a code.
		return "The device could not join your Wi-Fi — the password was wrong, or the device is too far from the router."
	case 6, 8, 10, 12:
		return "The settings did not arrive intact."
	default:
		// 1, 2, 4, 7, 9, 11, 13, 15, 20 and anything unknown: usaged sent
		// something the device refused. Say so plainly — it is not the owner's
		// network and no amount of retrying their password will help.
		return "usaged sent something this device refused. That is a bug in usaged, not in your network."
	}
}

const setupGenericReason = "Setup did not finish."

// provisionNext is what to do about it. Every failure gets one: "it failed" is
// not a diagnosis, and the owner is holding the device.
func provisionNext(f *ProvisionFailure) string {
	if f == nil {
		return setupGenericNext
	}
	switch f.DeviceCode {
	case 0:
		switch f.Stage {
		case StageScan:
			return "Turn Bluetooth on in System Settings, then look again."
		case StageConnect:
			return "Press the blue button to wake the device, keep it within a few metres of this Mac, and try again."
		case StagePair:
			return "Try again. macOS asks for a 6-digit passkey and those digits are on the device's screen — type them exactly."
		case StageTransfer:
			return "Try again — setup starts over from the beginning, which is normal and takes a few seconds."
		case StageApply:
			return "Look at the device screen: it will say whether it joined. Then try again."
		case stageTimeout:
			return "Press the blue button to wake the device and try again, and answer the macOS passkey prompt when it appears."
		default:
			return setupGenericNext
		}
	case 3, 14:
		return "Update the device's firmware, then set it up again."
	case 16, 21, 22:
		return "Join this Mac to the Wi-Fi network you want the device on, then look again."
	case 18:
		return "Check the network name and password on this Mac for stray characters, then try again."
	case 19:
		return "The device needs a network whose password is 8 characters or longer, or an open network."
	case 23:
		return "Try again. If it keeps failing, the device's storage is faulty and it needs re-flashing."
	case 24:
		return "Check that this Mac is on the network you want the device on, move the device closer to the router, and try again."
	case 6, 8, 10, 12:
		return "Try again — setup starts over from the beginning, which is normal."
	default:
		return "Nothing on your network needs changing. Please report this, with the code above."
	}
}

const setupGenericNext = "Press the blue button on the device to wake it, then try again."

// setupStepText is the sentence for one progress step, and the whitelist that
// decides which reported steps are honoured at all: a step this function does
// not know is dropped, so nothing a Provisioner invents reaches the page.
func setupStepText(step string) string {
	switch step {
	case StepConnecting:
		return "Connecting to the device…"
	case StepReadingInfo:
		return "Reading what the device says about itself…"
	case StepPairing:
		// The passkey moment, in the words requirement 2 asks for.
		return "Pairing — if macOS asks for a passkey, the six digits are on the device's screen."
	case StepSending:
		return "Sending your Wi-Fi and this Mac's address to the device…"
	case StepApplying:
		return "The device is saving the settings and joining your Wi-Fi…"
	default:
		return ""
	}
}

// decodeSetupBody reads a small JSON body into dst. An empty body is allowed:
// the scan route has none.
// decodeSetupBody delegates to the pairing decoder, which already caps the body
// at pairMaxBodyBytes (4096). A second constant of the same value here was dead
// weight that read as if the limit were enforced separately.
func decodeSetupBody(r *http.Request, dst any) error {
	return decodePairBody(r, dst)
}

// --- readiness, from the Server -----------------------------------------------

// providerReadiness counts the enabled providers that are actually reporting.
// It reads the snapshot rather than the Keychain on purpose: this is polled
// while a run is in flight, and a Keychain lookup per provider per poll would
// spawn a process every two seconds to answer a question the last poll already
// answered.
func (s *Server) providerReadiness() setupReadiness {
	enabled := map[string]bool{}
	total := 0
	for _, p := range s.cfg.EffectiveProviders() {
		if p.Enabled {
			enabled[p.ID] = true
			total++
		}
	}
	ready := 0
	for _, p := range s.sched.Current().Providers {
		if !enabled[p.ID] {
			continue
		}
		// "ok" and "stale" both mean a credential is present and the provider
		// answered at some point; "off", "auth" and "error" do not.
		if p.Status == "ok" || p.Status == "stale" {
			ready++
		}
	}
	return setupReadiness{ProvidersReady: ready, ProvidersTotal: total}
}

// WithProvisioner injects the BLE central used by the device-setup routes.
// cmd/usaged passes internal/bleprov's implementation; a nil Provisioner (the
// default) leaves the routes answering "not available", which is what a build
// without Bluetooth support should say.
func WithProvisioner(p Provisioner) Option {
	return func(s *Server) { s.provisioner = p }
}

// noStore marks a response uncacheable. (Moved here from netcfg.go — still used
// by the setup routes, which also must never be conditional.)
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

// netcfgClean trims free text reported by the device to something printable and
// short. Everything here reaches a log line and a web page, and the device is
// the least trusted writer in the system. (Moved here from netcfg.go when that
// endpoint group was deleted in task 90 — setup.go is its only remaining caller.)
func netcfgClean(s string, max int) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if b.Len()+len(string(r)) > max {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}
