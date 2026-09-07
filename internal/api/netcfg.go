package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// --- Device network configuration (task 79) ---
//
// The everyday case: change the device's Wi-Fi from the Settings page once the
// device is already reachable. The captive portal (task 77) bootstraps a device
// that has never joined anything; this maintains one that has.
//
// WHY THIS IS ITS OWN ENDPOINT AND NOT PART OF THE SNAPSHOT — do not "simplify"
// it back in. The /v1/usage body is hashed into rev and the device redraws only
// when rev changes, so device-bound configuration in that body would churn rev
// on every edit and force redraws, breaking the redraw-only-on-change invariant
// this whole project rests on. The unhashed `age` field is the precedent: data
// the device needs, deliberately kept OUTSIDE the hash. This channel therefore
// carries no ETag, is never cached, and never touches rev.
//
// THE DEVICE CHANNEL IS A POST, NOT A GET, AND THAT IS THE POINT. The staged
// record carries a Wi-Fi PASSWORD, and auth.go exempts loopback GETs from the
// token check — a GET would hand that password to any process on this Mac that
// can reach 127.0.0.1. A POST is mutating, so ORDER #54 applies unchanged and
// the token is required even from loopback. The same request also carries the
// device's report of the network it is on and its acknowledgement of the last
// change, which is what makes "applied once, not on every poll" enforceable.
//
// FOUR RULES THE PASSWORD OBEYS:
//  1. It is never returned by any GET — there is a test that walks every GET
//     route and greps the responses for it.
//  2. It is never logged. Lengths only, exactly like the provider keys.
//  3. It is never persisted. devices.json holds tokens this agent ISSUED; this
//     is a credential the USER owns, so it lives in memory for the length of
//     one change and dies with the process. A restart before the device
//     collects it loses the change, and the dashboard says so.
//  4. It leaves in exactly one place: the delivery half of
//     POST /v1/device/netcfg, to a caller that presented a device token.
//
// THE FAILURE PATH IS THE FEATURE. A change tells a working device to leave a
// working network, so the server half never lets it become a loop: a change is
// delivered at most netcfgMaxDeliveries times, is dropped the moment it is
// acknowledged (applied OR failed), expires on its own, and the device is told
// the fallback policy — previous credentials first, then the portal — in the
// payload rather than having to infer it.

// netcfgState is the lifecycle of one staged change. The first three are live
// states; the rest are terminal and are only ever seen on the `last` record.
type netcfgState string

const (
	netcfgIdle       netcfgState = "idle"       // nothing staged
	netcfgPending    netcfgState = "pending"    // staged, not yet collected
	netcfgDelivered  netcfgState = "delivered"  // the device has it, no ack yet
	netcfgApplied    netcfgState = "applied"    // the device joined the new network
	netcfgFailed     netcfgState = "failed"     // the device could not join and fell back
	netcfgStalled    netcfgState = "stalled"    // handed over too many times, never acked
	netcfgExpired    netcfgState = "expired"    // the device never collected it
	netcfgCancelled  netcfgState = "cancelled"  // withdrawn from the dashboard
	netcfgSuperseded netcfgState = "superseded" // replaced by a newer change
)

const (
	// netcfgChangeTTL outlives the slowest selectable poll interval (3600 s)
	// several times over, because the device collects the change on its own
	// schedule and a change that expires before it is even offered would look
	// exactly like a broken feature.
	netcfgChangeTTL = 2 * time.Hour

	// netcfgMaxDeliveries bounds the damage of a device that collects a change,
	// fails to join, and reboots before it can acknowledge: after three
	// hand-overs the agent stops offering it rather than feeding the same bad
	// network to a device stuck in a join loop.
	netcfgMaxDeliveries = 3

	// netcfgJoinTimeoutSec is how long the device should wait for the new
	// network before deciding the credentials are wrong and falling back.
	netcfgJoinTimeoutSec = 30

	// netcfgRedeliverAfter is the quiet period before a change already handed
	// over is offered again. A device that is mid-apply — joining, timing out,
	// falling back — may check in several times before it can acknowledge
	// anything, and without this window it would burn through its three
	// deliveries while it was doing exactly the right thing. It comfortably
	// covers a join attempt plus a fallback attempt.
	netcfgRedeliverAfter = 90 * time.Second

	// 802.11 caps an SSID at 32 bytes; WPA-PSK passphrases are 8-63 characters,
	// or exactly 64 hex digits for a raw pre-shared key.
	netcfgMaxSSIDLen = 32
	netcfgMinPassLen = 8
	netcfgMaxPassLen = 63
	netcfgPSKHexLen  = 64

	netcfgMaxBodyBytes = 4096
	netcfgMaxErrLen    = 120
	netcfgMaxIPLen     = 45 // an IPv6 textual address

	// netcfgStagesPerMinute matches the /v1/keys limiter — this is a human
	// clicking a button. netcfgCheckinsPerMinute matches the pairing claim
	// limit, leaving headroom above any poll rate the firmware might use.
	netcfgStagesPerMinute   = 5
	netcfgCheckinsPerMinute = 60
)

// Route paths. The dashboard half is /v1/netcfg; the device half lives under
// /v1/device/ beside GET /v1/device, which is the other endpoint that exists
// purely because its payload must stay out of the ETag-cached snapshot.
const (
	netcfgPath       = "/v1/netcfg"
	netcfgDevicePath = "/v1/device/netcfg"
)

// netcfgNotice and netcfgRecovery are server-owned copy rather than page text:
// GOLDEN_RULES #3 (smart API, dumb client) means a curl of this endpoint gets
// the complete answer, including the two things a user must be told before
// moving a working device to a different network.
const (
	netcfgNotice = "The device applies this on its NEXT check-in, not instantly — " +
		"allow up to one poll interval, plus a short offline gap while it re-joins."

	netcfgRecovery = "If the new password is wrong the device goes back to the network " +
		"it was on, and if that also fails it starts its own setup Wi-Fi. That setup " +
		"network shares a single radio channel with the join attempt, so any phone " +
		"connected to it is dropped each time the device retries — read the recovery " +
		"steps on the DEVICE SCREEN, not in a browser."
)

// netcfgChange is one staged change. It is NEVER marshalled: every response is
// built field by field so the password cannot leave by accident, the same
// discipline that gives pairedDevice its token-free pairedDeviceView.
type netcfgChange struct {
	ID        string
	DeviceID  string // "" until a device collects it, then bound to that device
	SSID      string
	Password  string // never logged, never in a GET, never written to disk
	Open      bool   // an open network: no password by design
	CreatedAt time.Time
	Expires   time.Time
	Delivered int
	LastSent  time.Time
}

// netcfgOutcome is what became of the previous change. It is kept after the
// change itself is gone so the dashboard can say what happened instead of
// silently returning to "nothing staged", which reads as a broken feature.
type netcfgOutcome struct {
	ID       string
	DeviceID string
	SSID     string
	State    netcfgState
	Error    string
	At       time.Time
}

// netcfgReport is the device's own account of the network it is on. The agent
// cannot observe this — it only ever sees an IP — so the device reports it.
type netcfgReport struct {
	DeviceID string
	SSID     string
	State    string // "connected", "portal", "joining", or "unknown"
	IP       string
	Secured  bool
	At       time.Time
}

// netcfg holds the single staged change plus the device's last report. One
// change at a time, for the same reason pairing has one window: a queue of
// network changes aimed at a device that may be offline is a way to strand it.
type netcfg struct {
	mu      sync.Mutex
	pending *netcfgChange
	last    *netcfgOutcome
	report  *netcfgReport

	now          func() time.Time
	logger       *slog.Logger
	stageLimit   *rateLimiter
	checkinLimit *rateLimiter
}

// newNetcfg builds the network-configuration channel. There is no path
// argument and no load: nothing here is persisted (rule 3 above).
func newNetcfg(now func() time.Time, logger *slog.Logger) *netcfg {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &netcfg{
		now:          now,
		logger:       logger,
		stageLimit:   newRateLimiter(netcfgStagesPerMinute, time.Minute),
		checkinLimit: newRateLimiter(netcfgCheckinsPerMinute, time.Minute),
	}
}

// routes registers the four endpoints. The three dashboard routes follow the
// ordinary auth rules (loopback-exempt GET, token-always mutations); the device
// route is a POST precisely so it inherits the token-always rule.
func (n *netcfg) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+netcfgPath, n.handleStatus)
	mux.HandleFunc("PUT "+netcfgPath, n.handleStage)
	mux.HandleFunc("DELETE "+netcfgPath, n.handleCancel)
	mux.HandleFunc("POST "+netcfgDevicePath, n.handleCheckin)
}

// handleStatus reports the device's current network and the staged change for
// the dashboard. It returns NO password under any circumstance — not the one
// staged here, and certainly not one the device is using.
func (n *netcfg) handleStatus(w http.ResponseWriter, _ *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.tickLocked()

	resp := map[string]any{
		"state":          n.stateLocked(),
		"change_ttl_sec": int(netcfgChangeTTL.Seconds()),
		"max_deliveries": netcfgMaxDeliveries,
		"notice":         netcfgNotice,
		"recovery":       netcfgRecovery,
		"device":         n.reportViewLocked(),
		"pending":        n.pendingViewLocked(),
		"last":           n.lastViewLocked(),
	}
	// key_state / key_source convention (ORDER #66 task 67c): say whether a
	// password exists and WHERE it lives, never what it is. "device" means the
	// value is in the device's own NVS and this dashboard has never held it.
	resp["password_state"], resp["password_source"] = n.passwordLabelsLocked()

	noStore(w)
	writeJSON(w, http.StatusOK, resp)
}

// handleStage accepts a new Wi-Fi target from the dashboard. Every field is
// validated BEFORE anything is mutated, so an invalid payload leaves the staged
// change — and the device — exactly as they were.
func (n *netcfg) handleStage(w http.ResponseWriter, r *http.Request) {
	if !n.stageLimit.allow() {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": "rate limit exceeded"})
		return
	}

	var body struct {
		SSID     string `json:"ssid"`
		Password string `json:"password"`
		Open     bool   `json:"open"`
		DeviceID string `json:"device_id"`
	}
	if err := decodeNetcfgBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	ssid := strings.TrimSpace(body.SSID)
	if !validSSID(ssid) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("ssid must be 1-%d characters and contain no control characters", netcfgMaxSSIDLen),
		})
		return
	}
	if body.Open {
		if body.Password != "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"ok": false, "error": "an open network takes no password",
			})
			return
		}
	} else if !validWiFiPassword(body.Password) {
		// The length is named, the value never is.
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok": false,
			"error": fmt.Sprintf("password must be %d-%d characters, or exactly %d hex digits for a raw PSK — "+
				"tick \"open network\" for a network with no password",
				netcfgMinPassLen, netcfgMaxPassLen, netcfgPSKHexLen),
		})
		return
	}
	deviceID := sanitizePairID(body.DeviceID)
	if body.DeviceID != "" && deviceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok": false, "error": "device_id must be up to 64 characters of letters, digits, . : - _",
		})
		return
	}

	id, err := newNetcfgID()
	if err != nil {
		n.logger.Error("netcfg: generate change id", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "internal"})
		return
	}

	now := n.now()
	n.mu.Lock()
	defer n.mu.Unlock()
	n.tickLocked()

	// A second change replaces the first, and the replaced one is recorded so
	// the page can explain a late acknowledgement that no longer applies.
	if n.pending != nil {
		n.recordOutcomeLocked(n.pending, netcfgSuperseded, "")
	}

	n.pending = &netcfgChange{
		ID:        id,
		DeviceID:  deviceID,
		SSID:      ssid,
		Password:  body.Password,
		Open:      body.Open,
		CreatedAt: now,
		Expires:   now.Add(netcfgChangeTTL),
	}
	// Lengths only — the same rule the provider keys and the device tokens obey.
	n.logger.Info("netcfg: change staged",
		"change_id", id, "device_id", deviceID,
		"ssid_len", len(ssid), "pass_len", len(body.Password), "open", body.Open)

	resp := map[string]any{
		"ok":       true,
		"state":    n.stateLocked(),
		"pending":  n.pendingViewLocked(),
		"notice":   netcfgNotice,
		"recovery": netcfgRecovery,
	}
	resp["password_state"], resp["password_source"] = n.passwordLabelsLocked()
	noStore(w)
	writeJSON(w, http.StatusOK, resp)
}

// handleCancel withdraws a staged change. It is the escape hatch for a typo
// spotted before the device polls, which on a 900 s interval is most of them.
func (n *netcfg) handleCancel(w http.ResponseWriter, _ *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.tickLocked()

	if n.pending == nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok": false, "state": n.stateLocked(), "error": "no change is waiting to be cancelled",
		})
		return
	}
	// A change already handed to the device cannot be recalled — the device is
	// acting on it right now — so say that rather than implying it was undone.
	delivered := n.pending.Delivered > 0
	n.logger.Info("netcfg: change cancelled", "change_id", n.pending.ID, "delivered", n.pending.Delivered)
	n.recordOutcomeLocked(n.pending, netcfgCancelled, "")
	n.pending = nil

	resp := map[string]any{"ok": true, "state": n.stateLocked(), "last": n.lastViewLocked()}
	if delivered {
		resp["note"] = "The device had already collected this change and may still apply it."
	}
	noStore(w)
	writeJSON(w, http.StatusOK, resp)
}

// handleCheckin is the DEVICE-FACING half: one POST that reports the network
// the device is on, acknowledges the previous change, and collects the next
// one. It is the ONLY place a staged password is disclosed, and the auth
// middleware has already required a device token — even from loopback, because
// this is a mutating method (ORDER #54).
func (n *netcfg) handleCheckin(w http.ResponseWriter, r *http.Request) {
	if !n.checkinLimit.allow() {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": "rate limit exceeded"})
		return
	}

	var body struct {
		DeviceID string `json:"device_id"`
		SSID     string `json:"ssid"`
		State    string `json:"state"`
		IP       string `json:"ip"`
		Secured  *bool  `json:"secured"`
		Ack      *struct {
			ChangeID string `json:"change_id"`
			Result   string `json:"result"`
			Error    string `json:"error"`
		} `json:"ack"`
	}
	if err := decodeNetcfgBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	deviceID := sanitizePairID(body.DeviceID)
	if body.DeviceID != "" && deviceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok": false, "error": "device_id must be up to 64 characters of letters, digits, . : - _",
		})
		return
	}

	now := n.now()
	n.mu.Lock()
	defer n.mu.Unlock()
	n.tickLocked()

	n.report = &netcfgReport{
		DeviceID: deviceID,
		SSID:     netcfgClean(body.SSID, netcfgMaxSSIDLen),
		State:    netcfgReportState(body.State),
		IP:       netcfgCleanIP(body.IP),
		Secured:  body.Secured == nil || *body.Secured,
		At:       now,
	}

	ackAccepted := false
	if body.Ack != nil {
		ackAccepted = n.applyAckLocked(body.Ack.ChangeID, body.Ack.Result, body.Ack.Error)
	}

	change, awaitingAck := n.takeForDeliveryLocked(deviceID)
	resp := map[string]any{"ok": true, "ack_accepted": ackAccepted}
	if awaitingAck {
		// Honest on the wire: there IS a change, the device already has it, and
		// the agent is waiting to be told how it went.
		resp["awaiting_ack"] = true
	}
	if change != nil {
		// THE ONE PLACE THE PASSWORD LEAVES THIS PROCESS. The caller presented
		// a device token to get here, and the fallback policy travels with it
		// so a device that cannot join knows what to do without asking again.
		resp["pending"] = true
		resp["change"] = map[string]any{
			"change_id": change.ID,
			"ssid":      change.SSID,
			"password":  change.Password,
			"open":      change.Open,
			"fallback": map[string]any{
				"previous_first":   true,
				"then":             "portal",
				"join_timeout_sec": netcfgJoinTimeoutSec,
			},
		}
	} else {
		resp["pending"] = false
	}

	noStore(w)
	writeJSON(w, http.StatusOK, resp)
}

// applyAckLocked closes out a change the device reports on. An acknowledgement
// for anything other than the change currently staged is ignored — a late ack
// for a superseded change must not delete its replacement.
func (n *netcfg) applyAckLocked(changeID, result, errMsg string) bool {
	if n.pending == nil || changeID == "" || changeID != n.pending.ID {
		return false
	}
	state := netcfgFailed
	if strings.EqualFold(strings.TrimSpace(result), "applied") {
		state = netcfgApplied
	}
	msg := netcfgClean(errMsg, netcfgMaxErrLen)
	n.logger.Info("netcfg: change acknowledged",
		"change_id", n.pending.ID, "device_id", n.pending.DeviceID,
		"result", string(state), "err", msg)
	n.recordOutcomeLocked(n.pending, state, msg)
	// Cleared whatever the result. A failed change is NOT retried: the device
	// has already fallen back, and re-offering it would send a working device
	// back to a network it just proved it cannot join.
	n.pending = nil
	return true
}

// takeForDeliveryLocked returns the change this device should apply, and
// whether one is already in its hands awaiting an acknowledgement. It binds an
// unaddressed change to the first device that collects it, so a second device
// on the LAN cannot pick up a change meant for the first.
func (n *netcfg) takeForDeliveryLocked(deviceID string) (*netcfgChange, bool) {
	if n.pending == nil {
		return nil, false
	}
	if n.pending.DeviceID != "" && n.pending.DeviceID != deviceID {
		return nil, false
	}
	if n.pending.Delivered > 0 && n.now().Sub(n.pending.LastSent) < netcfgRedeliverAfter {
		// Already handed over and still inside the quiet period: the device is
		// working on it. Saying "nothing new" is the correct instruction.
		return nil, true
	}
	if n.pending.Delivered >= netcfgMaxDeliveries {
		// Handed over the maximum number of times and never acknowledged: the
		// device is most likely rebooting mid-apply. Stop feeding it.
		n.logger.Warn("netcfg: change stalled, no acknowledgement",
			"change_id", n.pending.ID, "delivered", n.pending.Delivered)
		n.recordOutcomeLocked(n.pending, netcfgStalled, "the device never confirmed it applied this change")
		n.pending = nil
		return nil, false
	}
	if n.pending.DeviceID == "" && deviceID != "" {
		n.pending.DeviceID = deviceID
	}
	n.pending.Delivered++
	n.pending.LastSent = n.now()
	n.logger.Info("netcfg: change delivered",
		"change_id", n.pending.ID, "device_id", n.pending.DeviceID,
		"ssid_len", len(n.pending.SSID), "delivery", n.pending.Delivered)
	return n.pending, false
}

// tickLocked retires a change the device never came for. Every entry point
// calls it, so an expired change is indistinguishable from none at all.
func (n *netcfg) tickLocked() {
	if n.pending == nil {
		return
	}
	if !n.pending.Expires.IsZero() && !n.now().Before(n.pending.Expires) {
		n.logger.Info("netcfg: change expired", "change_id", n.pending.ID, "delivered", n.pending.Delivered)
		n.recordOutcomeLocked(n.pending, netcfgExpired, "")
		n.pending = nil
	}
}

// recordOutcomeLocked stores what became of a change. The password is not
// copied: an outcome outlives the change and must not extend the credential's
// lifetime by even one field.
func (n *netcfg) recordOutcomeLocked(c *netcfgChange, state netcfgState, errMsg string) {
	n.last = &netcfgOutcome{
		ID:       c.ID,
		DeviceID: c.DeviceID,
		SSID:     c.SSID,
		State:    state,
		Error:    errMsg,
		At:       n.now(),
	}
}

// stateLocked is the live state of the channel, which is the staged change's
// state or "idle" when nothing is staged.
func (n *netcfg) stateLocked() netcfgState {
	if n.pending == nil {
		return netcfgIdle
	}
	if n.pending.Delivered > 0 {
		return netcfgDelivered
	}
	return netcfgPending
}

// pendingViewLocked is the token-free, password-free projection of the staged
// change. It reports the SSID (which every access point broadcasts anyway) and
// says nothing about the password beyond whether one is set.
func (n *netcfg) pendingViewLocked() map[string]any {
	if n.pending == nil {
		return nil
	}
	return map[string]any{
		"change_id":      n.pending.ID,
		"device_id":      n.pending.DeviceID,
		"ssid":           n.pending.SSID,
		"open":           n.pending.Open,
		"created_at":     n.pending.CreatedAt.Unix(),
		"expires_in_sec": n.expiresInSecLocked(),
		"delivered":      n.pending.Delivered,
	}
}

func (n *netcfg) lastViewLocked() map[string]any {
	if n.last == nil {
		return nil
	}
	v := map[string]any{
		"change_id": n.last.ID,
		"device_id": n.last.DeviceID,
		"ssid":      n.last.SSID,
		"state":     string(n.last.State),
		"at":        n.last.At.Unix(),
	}
	if n.last.Error != "" {
		v["error"] = n.last.Error
	}
	return v
}

func (n *netcfg) reportViewLocked() map[string]any {
	if n.report == nil {
		return nil
	}
	v := map[string]any{
		"device_id":     n.report.DeviceID,
		"ssid":          n.report.SSID,
		"state":         n.report.State,
		"secured":       n.report.Secured,
		"last_report":   n.report.At.Unix(),
		"seconds_since": int64(n.now().Sub(n.report.At).Seconds()),
	}
	if n.report.IP != "" {
		v["ip"] = n.report.IP
	}
	return v
}

// passwordLabelsLocked follows the key_state / key_source convention: whether a
// password exists, and WHERE the value actually lives. It never implies the
// dashboard owns one.
func (n *netcfg) passwordLabelsLocked() (state, source string) {
	if n.pending != nil {
		if n.pending.Open {
			return "not_set", "pending-change"
		}
		return "set", "pending-change"
	}
	if n.report != nil && n.report.Secured {
		return "set", "device"
	}
	if n.report != nil {
		return "not_set", "device"
	}
	return "not_set", "none"
}

// expiresInSecLocked returns whole seconds left on the staged change.
func (n *netcfg) expiresInSecLocked() int {
	if n.pending == nil || n.pending.Expires.IsZero() {
		return 0
	}
	d := n.pending.Expires.Sub(n.now())
	if d <= 0 {
		return 0
	}
	return int(d / time.Second)
}

// noStore marks a response uncacheable. These endpoints deliberately carry NO
// ETag: an ETag here would invite a conditional GET, and a 304 has no body, so
// a device that got one would silently apply nothing.
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

// validSSID reports whether s is a usable 802.11 SSID: 1-32 BYTES (not runes —
// the standard counts octets) and free of control characters, which cannot be
// typed into a join request and usually mean a mangled copy-paste.
func validSSID(s string) bool {
	if s == "" || len(s) > netcfgMaxSSIDLen {
		return false
	}
	return !hasControlChars(s)
}

// validWiFiPassword reports whether s is a WPA passphrase (8-63 printable
// characters) or a raw 64-hex-digit PSK. The value is only ever measured and
// classified here — never logged, never echoed.
func validWiFiPassword(s string) bool {
	if len(s) == netcfgPSKHexLen && isHexString(s) {
		return true
	}
	if len(s) < netcfgMinPassLen || len(s) > netcfgMaxPassLen {
		return false
	}
	return !hasControlChars(s)
}

func hasControlChars(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func isHexString(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return len(s) > 0
}

// netcfgReportState maps the device's self-reported state onto the three values
// the dashboard renders. Anything unrecognised becomes "unknown" rather than
// reaching the page verbatim.
func netcfgReportState(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "connected":
		return "connected"
	case "portal":
		return "portal"
	case "joining":
		return "joining"
	default:
		return "unknown"
	}
}

// netcfgClean trims free text reported by the device to something printable and
// short. Everything here reaches a log line and a web page, and the device is
// the least trusted writer in the system.
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

// netcfgCleanIP keeps a reported address only if it parses as one. A device is
// free to omit it; it must not be free to put arbitrary text on the page.
func netcfgCleanIP(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > netcfgMaxIPLen {
		return ""
	}
	if net.ParseIP(s) == nil {
		return ""
	}
	return s
}

// newNetcfgID returns an opaque change id. crypto/rand rather than a counter so
// a device cannot guess the id of a change it was not given and acknowledge it
// out from under the device that was.
func newNetcfgID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// decodeNetcfgBody reads a small JSON body into dst, mirroring decodePairBody.
// An empty body is a bad request here: every route that uses it needs fields.
func decodeNetcfgBody(r *http.Request, dst any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, netcfgMaxBodyBytes))
	if err != nil {
		return errors.New("invalid JSON body")
	}
	if len(raw) == 0 {
		return errors.New("invalid JSON body")
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return errors.New("invalid JSON body")
	}
	return nil
}
