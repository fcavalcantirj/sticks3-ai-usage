package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// --- Device pairing (task 78) ---
//
// A publishable firmware binary carries NO credentials: secrets.h is a
// compile-time header, so a baked-in device token ends up as a plaintext string
// inside firmware.bin. The device must therefore be GIVEN a token, and pairing
// is how it gets one:
//
//	1. Felipe opens a pairing window from the dashboard   POST /v1/pair/open
//	2. the device claims it and displays a 6-char code    POST /v1/pair/claim  → 202
//	3. Felipe types that code into the dashboard          POST /v1/pair/confirm
//	4. the device's next claim poll collects its token    POST /v1/pair/claim  → 200
//
// TWO DELIBERATELY DIFFERENT AUTH MODELS — READ THIS BEFORE "FIXING" EITHER ONE:
//
//   - POST /v1/pair/claim is DEVICE-FACING and necessarily UNAUTHENTICATED. An
//     unpaired device has no credential to present, so requiring one (the
//     ORDER #54 rule for mutating routes) makes the feature impossible. Its
//     authorisation is the PAIRING WINDOW: the request is refused unless Felipe
//     has opened a window from the token-protected dashboard, and THE OPEN
//     WINDOW IS THE HUMAN CONSENT the token would otherwise stand in for. On
//     top of that the handler requires a LAN peer (a private, non-loopback
//     address), rate-limits claims, admits exactly ONE device per window, and
//     closes the window on the first token hand-off or on timeout.
//   - POST /v1/pair/open and POST /v1/pair/confirm are DASHBOARD-FACING and
//     mutating, so ORDER #54 applies unchanged: the device token is required
//     even from loopback. GET /v1/pair follows the ordinary GET rule (loopback
//     exempt, LAN needs the token).
//
// The code exists ONLY on the device screen and the token travels ONLY to the
// device: GET /v1/pair returns neither, and neither is ever written to a log
// line — lengths only, exactly like the provider keys.

// Pairing states. "approved" is internal (the token is minted and waiting to be
// collected); the device is told "paired" on the response that carries it.
type pairState string

const (
	pairStateIdle     pairState = "idle"     // no window open
	pairStateOpen     pairState = "open"     // window open, no device has claimed it
	pairStateClaimed  pairState = "claimed"  // a device holds the slot and shows its code
	pairStateApproved pairState = "approved" // code confirmed, token waiting for pickup
	pairStatePaired   pairState = "paired"   // reported to the device on token hand-off
)

const (
	// pairCodeAlphabet has no O/0 and no I/1: the code is read off a 1.14"
	// screen and typed by hand. 32 symbols divide 256 evenly, so a byte from
	// crypto/rand maps onto a symbol with no modulo bias.
	pairCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	pairCodeLen      = 6

	// pairWindowTTL keeps the window short-lived — minutes, never open-ended.
	pairWindowTTL = 3 * time.Minute
	// pairPickupTTL is the grace given AFTER the code is confirmed, so a
	// confirmation in the last seconds of the window still leaves the device
	// time to poll for its token.
	pairPickupTTL = 60 * time.Second

	// pairMaxWrongCodes closes the window outright. Combined with the confirm
	// rate limit it caps a brute force at five guesses out of 32^6 (~1.07e9).
	pairMaxWrongCodes = 5
	// pairClaimsPerMinute leaves headroom above the device's own poll rate
	// (~20/min at a 3 s interval) without letting an unauthenticated endpoint
	// be hammered.
	pairClaimsPerMinute = 60
	// pairConfirmsPerMinute matches the /v1/keys limiter.
	pairConfirmsPerMinute = 5

	// pairTokenBytes yields a 32-hex-character per-device token (128 bits).
	// Per-device, never a shared secret: re-pairing the same device_id
	// replaces its token and retires the old one.
	pairTokenBytes = 16

	pairMaxBodyBytes = 4096
	pairMaxIDLen     = 64
	pairMaxNameLen   = 40
)

// Route paths. auth.go exempts pairClaimPath — and only that one — from the
// device-token requirement.
const (
	pairStatusPath  = "/v1/pair"
	pairOpenPath    = "/v1/pair/open"
	pairClaimPath   = "/v1/pair/claim"
	pairConfirmPath = "/v1/pair/confirm"
)

// pairedDevice is one device that completed pairing. Token is persisted (the
// agent must still recognise the device after a restart) but is NEVER put in a
// response body and NEVER logged — responses use pairedDeviceView instead.
type pairedDevice struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Token    string `json:"token"`
	IssuedAt int64  `json:"issued_at"`
	Peer     string `json:"peer,omitempty"`
}

// pairedDeviceView is the token-free projection of pairedDevice used in
// responses. It exists so no code path can accidentally marshal a token.
type pairedDeviceView struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	IssuedAt int64  `json:"issued_at"`
	Peer     string `json:"peer,omitempty"`
}

// pairedDeviceFile is the on-disk shape of the paired-device store.
type pairedDeviceFile struct {
	Devices map[string]pairedDevice `json:"devices"`
}

// pairing holds the single pairing window plus the persisted set of paired
// devices. There is deliberately only ONE window: a second device cannot pair
// while another is mid-flow, which is what makes "the open window is the
// consent" a meaningful statement.
type pairing struct {
	mu         sync.Mutex
	state      pairState
	code       string    // 6 chars, shown on the device screen only
	token      string    // minted at confirm, handed over once
	deviceID   string    // claimant id (MAC), may be empty
	deviceName string    // claimant's self-reported name
	peer       string    // claimant IP
	userAgent  string    // claimant User-Agent
	wrong      int       // wrong confirm attempts in this window
	expires    time.Time // window deadline

	devices map[string]pairedDevice
	path    string // devices.json, "" disables persistence (tests only)

	now          func() time.Time
	logger       *slog.Logger
	claimLimit   *rateLimiter
	confirmLimit *rateLimiter
}

// newPairing builds the pairing manager, loading any previously paired devices
// from path. A corrupt file is logged and ignored rather than fatal: an agent
// that refuses to start is worse than one that asks for a re-pair.
func newPairing(path string, now func() time.Time, logger *slog.Logger) *pairing {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	p := &pairing{
		state:        pairStateIdle,
		devices:      map[string]pairedDevice{},
		path:         path,
		now:          now,
		logger:       logger,
		claimLimit:   newRateLimiter(pairClaimsPerMinute, time.Minute),
		confirmLimit: newRateLimiter(pairConfirmsPerMinute, time.Minute),
	}
	if path == "" {
		logger.Warn("pairing: no state path; issued device tokens will not survive a restart")
		return p
	}
	devs, err := loadPairedDevices(path)
	if err != nil {
		logger.Warn("pairing: unreadable device file, starting with none", "path", path, "err", err)
	}
	for id, d := range devs {
		p.devices[id] = d
	}
	return p
}

// routes registers the four pairing endpoints. Only pairClaimPath is exempt
// from the token requirement in auth.go; the other three are not.
func (p *pairing) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+pairStatusPath, p.handleStatus)
	mux.HandleFunc("POST "+pairOpenPath, p.handleOpen)
	mux.HandleFunc("POST "+pairClaimPath, p.handleClaim)
	mux.HandleFunc("POST "+pairConfirmPath, p.handleConfirm)
}

// matchToken reports whether tok is a token this agent issued to a paired
// device. Every entry is compared even after a match, so the time taken does
// not reveal WHICH device matched — the same discipline as validToken.
func (p *pairing) matchToken(tok string) bool {
	if tok == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	match := 0
	for _, d := range p.devices {
		if len(d.Token) != len(tok) {
			continue
		}
		match |= subtle.ConstantTimeCompare([]byte(d.Token), []byte(tok))
	}
	return match == 1
}

// handleOpen opens (or restarts) the pairing window. DASHBOARD-FACING: the
// auth middleware has already required the device token, even on loopback.
// Opening always discards any window in flight — clicking the button again is
// how a stuck pairing is abandoned.
func (p *pairing) handleOpen(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.resetLocked()
	p.state = pairStateOpen
	p.expires = p.now().Add(pairWindowTTL)
	p.logger.Info("pairing: window opened", "ttl_sec", int(pairWindowTTL.Seconds()))

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"state":          p.state,
		"expires_in_sec": p.expiresInSecLocked(),
		"code_len":       pairCodeLen,
	})
}

// handleStatus reports the window state for the dashboard. It returns NEITHER
// the pairing code NOR any token: the code lives on the device screen, which is
// what makes typing it proof of physical possession, and the token is the
// device's alone.
func (p *pairing) handleStatus(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tickLocked()

	resp := map[string]any{
		"state":          p.state,
		"expires_in_sec": p.expiresInSecLocked(),
		"code_len":       pairCodeLen,
		"window_ttl_sec": int(pairWindowTTL.Seconds()),
		"devices":        p.deviceViewsLocked(),
	}
	if p.state == pairStateClaimed || p.state == pairStateApproved {
		resp["device"] = map[string]any{
			"id":         p.deviceID,
			"name":       p.deviceName,
			"peer":       p.peer,
			"user_agent": p.userAgent,
		}
	}
	if p.state == pairStateClaimed {
		resp["attempts_left"] = pairMaxWrongCodes - p.wrong
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleClaim is the DEVICE-FACING endpoint and is UNAUTHENTICATED BY DESIGN —
// see the two auth models at the top of this file. An open window, a LAN peer
// and the rate limit are the whole authorisation, and the window admits one
// device and closes on the first token hand-off.
//
// The device polls this one endpoint throughout: 403 means "ask Felipe to open
// a window", 202 means "show this code", 200 carries the token exactly once.
func (p *pairing) handleClaim(w http.ResponseWriter, r *http.Request) {
	peer := peerIP(r)
	if !isLANPeer(peer) {
		// Never authenticated, so never reachable from off-LAN: loopback is
		// the Mac itself (which does not pair) and a public address is not a
		// StickS3 on this network.
		p.logger.Warn("pairing: claim refused, peer is not on the LAN", "peer", peer)
		writeJSON(w, http.StatusForbidden, map[string]any{
			"ok": false, "error": "pairing is only accepted from the local network",
		})
		return
	}
	if !p.claimLimit.allow() {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": "rate limit exceeded"})
		return
	}

	var body struct {
		Code     string `json:"code"`
		DeviceID string `json:"device_id"`
		Name     string `json:"name"`
	}
	if err := decodePairBody(r, &body); err != nil {
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
	code := normalizePairCode(body.Code)
	if code != "" && !validPairCode(code) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("code must be %d characters from %s", pairCodeLen, pairCodeAlphabet),
		})
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.tickLocked()

	switch p.state {
	case pairStateOpen:
		// The device may bring its own code (it has to display something) or
		// leave it out and let the agent mint one with crypto/rand.
		if code == "" {
			minted, err := newPairCode()
			if err != nil {
				p.logger.Error("pairing: generate code", "err", err)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "internal"})
				return
			}
			code = minted
		}
		p.state = pairStateClaimed
		p.code = code
		p.deviceID = deviceID
		p.deviceName = sanitizePairName(body.Name)
		p.peer = peer
		p.userAgent = r.Header.Get("User-Agent")
		p.logger.Info("pairing: device claimed the window",
			"peer", peer, "device_id", deviceID, "code_len", len(code))
		p.writeClaimedLocked(w)

	case pairStateClaimed:
		if !p.sameClaimantLocked(deviceID, peer) {
			p.writeBusyLocked(w, peer)
			return
		}
		// A re-poll always gets the code the dashboard is waiting for, so the
		// screen and the server can never disagree — including after a device
		// reboot, which would otherwise generate a second code.
		p.writeClaimedLocked(w)

	case pairStateApproved:
		if !p.sameClaimantLocked(deviceID, peer) {
			p.writeBusyLocked(w, peer)
			return
		}
		p.issueTokenLocked(w, peer)

	default: // pairStateIdle
		writeJSON(w, http.StatusForbidden, map[string]any{
			"ok":    false,
			"state": pairStateIdle,
			"error": "no pairing window is open — open one from the dashboard",
		})
	}
}

// writeClaimedLocked answers a claim that is waiting for Felipe to confirm.
func (p *pairing) writeClaimedLocked(w http.ResponseWriter) {
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":             true,
		"state":          pairStateClaimed,
		"code":           p.code,
		"expires_in_sec": p.expiresInSecLocked(),
	})
}

// writeBusyLocked refuses a second device while another holds the window.
func (p *pairing) writeBusyLocked(w http.ResponseWriter, peer string) {
	p.logger.Warn("pairing: claim refused, window held by another device", "peer", peer, "holder", p.peer)
	writeJSON(w, http.StatusConflict, map[string]any{
		"ok":    false,
		"state": p.state,
		"error": "another device is already pairing in this window",
	})
}

// issueTokenLocked hands the minted token to the claimant exactly once, records
// the device, and closes the window. The record is persisted BEFORE the token
// leaves: a token the agent cannot remember across a restart would leave the
// device holding a credential nothing accepts.
func (p *pairing) issueTokenLocked(w http.ResponseWriter, peer string) {
	id := p.deviceID
	if id == "" {
		// No self-reported id: key the record by the address it paired from.
		id = peer
	}
	rec := pairedDevice{
		ID:       id,
		Name:     p.deviceName,
		Token:    p.token,
		IssuedAt: p.now().Unix(),
		Peer:     peer,
	}
	prev, had := p.devices[id]
	p.devices[id] = rec
	if err := p.persistLocked(); err != nil {
		if had {
			p.devices[id] = prev
		} else {
			delete(p.devices, id)
		}
		p.logger.Error("pairing: persist paired device", "device_id", id, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok": false, "error": "could not persist the paired device",
		})
		return
	}

	token := p.token
	p.logger.Info("pairing: token issued", "device_id", id, "peer", peer, "token_len", len(token))
	p.resetLocked() // single use: the window closes on the first success

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"state":     pairStatePaired,
		"device_id": id,
		"token":     token,
	})
}

// handleConfirm verifies the code Felipe read off the device screen and mints
// the device's token. DASHBOARD-FACING: the auth middleware has already
// required the device token, even on loopback (ORDER #54).
func (p *pairing) handleConfirm(w http.ResponseWriter, r *http.Request) {
	if !p.confirmLimit.allow() {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": "rate limit exceeded"})
		return
	}

	var body struct {
		Code string `json:"code"`
	}
	if err := decodePairBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	code := normalizePairCode(body.Code)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.tickLocked()

	if p.state != pairStateClaimed {
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":    false,
			"state": p.state,
			"error": "no device is waiting to be paired — open a window and wait for the code to appear on the device",
		})
		return
	}

	// Constant-time, like the device-token comparison in auth.go. A length
	// mismatch makes ConstantTimeCompare return 0 without comparing bytes.
	if subtle.ConstantTimeCompare([]byte(code), []byte(p.code)) != 1 {
		p.wrong++
		left := pairMaxWrongCodes - p.wrong
		p.logger.Warn("pairing: wrong code", "attempts_left", left)
		if left <= 0 {
			p.resetLocked()
			writeJSON(w, http.StatusForbidden, map[string]any{
				"ok": false, "state": pairStateIdle,
				"error": "too many wrong codes — the pairing window was closed",
			})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":            false,
			"error":         "that code does not match the one on the device screen",
			"attempts_left": left,
		})
		return
	}

	token, err := newDeviceToken()
	if err != nil {
		p.logger.Error("pairing: generate device token", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "internal"})
		return
	}
	p.token = token
	p.state = pairStateApproved
	// Restart the clock for pickup so a confirmation in the window's last
	// seconds still leaves the device time to poll.
	p.expires = p.now().Add(pairPickupTTL)
	p.logger.Info("pairing: code confirmed", "device_id", p.deviceID, "token_len", len(token))

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"state":          p.state,
		"device_id":      p.deviceID,
		"expires_in_sec": p.expiresInSecLocked(),
	})
}

// tickLocked closes an expired window. Every entry point calls it, so an
// expired code is indistinguishable from no window at all.
func (p *pairing) tickLocked() {
	if p.state == pairStateIdle {
		return
	}
	if !p.expires.IsZero() && !p.now().Before(p.expires) {
		p.logger.Info("pairing: window expired", "state", string(p.state))
		p.resetLocked()
	}
}

// resetLocked clears the window, including the code and any minted token.
func (p *pairing) resetLocked() {
	p.state = pairStateIdle
	p.code = ""
	p.token = ""
	p.deviceID = ""
	p.deviceName = ""
	p.peer = ""
	p.userAgent = ""
	p.wrong = 0
	p.expires = time.Time{}
}

// expiresInSecLocked returns whole seconds left in the window, never negative.
func (p *pairing) expiresInSecLocked() int {
	if p.state == pairStateIdle || p.expires.IsZero() {
		return 0
	}
	d := p.expires.Sub(p.now())
	if d <= 0 {
		return 0
	}
	return int(d / time.Second)
}

// sameClaimantLocked reports whether a poll comes from the device that holds
// the window: by its self-reported id when it gave one, otherwise by address.
func (p *pairing) sameClaimantLocked(deviceID, peer string) bool {
	if p.deviceID != "" {
		return deviceID == p.deviceID
	}
	return peer == p.peer
}

// deviceViewsLocked returns the paired devices without their tokens.
func (p *pairing) deviceViewsLocked() []pairedDeviceView {
	out := make([]pairedDeviceView, 0, len(p.devices))
	for _, d := range p.devices {
		out = append(out, pairedDeviceView{ID: d.ID, Name: d.Name, IssuedAt: d.IssuedAt, Peer: d.Peer})
	}
	// Stable order so the dashboard list does not shuffle between polls.
	sortPairedViews(out)
	return out
}

// sortPairedViews orders devices by issue time (newest first), then by id.
func sortPairedViews(v []pairedDeviceView) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && lessPairedView(v[j], v[j-1]); j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

func lessPairedView(a, b pairedDeviceView) bool {
	if a.IssuedAt != b.IssuedAt {
		return a.IssuedAt > b.IssuedAt
	}
	return a.ID < b.ID
}

// persistLocked writes devices.json atomically (temp file + rename), 0600 in a
// 0700 directory — the same discipline as snapshot.Save.
func (p *pairing) persistLocked() error {
	if p.path == "" {
		return nil
	}
	dir := filepath.Dir(p.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create device dir %s: %w", dir, err)
	}
	data, err := json.Marshal(pairedDeviceFile{Devices: p.devices})
	if err != nil {
		return fmt.Errorf("marshal devices: %w", err)
	}
	tmpPath := p.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("write devices %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, p.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename devices %s -> %s: %w", tmpPath, p.path, err)
	}
	return nil
}

// loadPairedDevices reads devices.json. A missing file is not an error.
func loadPairedDevices(path string) (map[string]pairedDevice, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f pairedDeviceFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("corrupt device file %s: %w", path, err)
	}
	return f.Devices, nil
}

// pairedDevicesPath puts devices.json beside the snapshot state file, so the
// tokens this agent issues live with the rest of its runtime state. An empty
// state path disables persistence, which is a test configuration and never a
// served one — newPairing logs a warning when it happens.
func pairedDevicesPath(statePath string) string {
	if statePath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(statePath), "devices.json")
}

// newPairCode returns a fresh pairing code from crypto/rand — never math/rand,
// which would make the code predictable from another code.
func newPairCode() (string, error) {
	buf := make([]byte, pairCodeLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	out := make([]byte, pairCodeLen)
	for i, b := range buf {
		out[i] = pairCodeAlphabet[int(b)%len(pairCodeAlphabet)]
	}
	return string(out), nil
}

// newDeviceToken returns a fresh per-device token: 128 bits from crypto/rand,
// hex-encoded so the firmware can store and send it without escaping.
func newDeviceToken() (string, error) {
	buf := make([]byte, pairTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// normalizePairCode uppercases the typed code and drops the separators a human
// tends to add. It does not validate — validPairCode does.
func normalizePairCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		if r == ' ' || r == '-' || r == '_' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// validPairCode reports whether s is exactly pairCodeLen symbols of the
// unambiguous alphabet.
func validPairCode(s string) bool {
	if len(s) != pairCodeLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(pairCodeAlphabet, rune(s[i])) {
			return false
		}
	}
	return true
}

// sanitizePairID accepts a device id of letters, digits and . : - _ up to 64
// characters. Anything else returns "" — the id reaches a log line, and an
// unauthenticated endpoint must not be able to write arbitrary bytes there.
func sanitizePairID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > pairMaxIDLen {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == ':', c == '-', c == '_':
		default:
			return ""
		}
	}
	return s
}

// sanitizePairName trims a self-reported device name to something printable and
// short. Unlike the id it is cosmetic, so it is cleaned rather than rejected.
func sanitizePairName(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if b.Len() >= pairMaxNameLen {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// decodePairBody reads a small JSON body into dst. An empty body is allowed:
// a claim without a code is valid, and the dashboard's open call has no body.
func decodePairBody(r *http.Request, dst any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, pairMaxBodyBytes))
	if err != nil {
		return errors.New("invalid JSON body")
	}
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return errors.New("invalid JSON body")
	}
	return nil
}

// isLANPeer reports whether host (a bare IP as produced by peerIP) is a private
// LAN address, and explicitly NOT loopback. The claim endpoint is
// unauthenticated, so it is confined to the network the device is actually on.
func isLANPeer(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() {
		return false
	}
	return ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// issueDeviceToken mints a per-device token, records it in the paired-device
// store, and returns it. It is the BLE path's equivalent of issueTokenLocked.
//
// WHY A SECOND ENTRY POINT EXISTS. issueTokenLocked is welded to an
// http.ResponseWriter because the HTTP flow's whole job is to answer a device
// that asked. BLE inverts that: the daemon hands the device its token over an
// encrypted GATT link BEFORE the device has ever been on Wi-Fi, so there is no
// request to answer and no /v1/pair/claim the device could reach. What must
// still happen — and is the entire reason this is not just newDeviceToken() —
// is the RECORDING. A token this agent has not persisted is a credential
// nothing accepts: the device would join, present it, and be refused forever.
//
// It deliberately does NOT open, consume or close the pairing window. The
// window exists so an unauthenticated LAN claim can be authorised by a human;
// a BLE transfer is already authorised by a bonded, MITM-protected link and a
// six-digit passkey the owner read off the device's own screen. Coupling the
// two would mean the owner had to open a window on the dashboard to do the
// thing the dashboard button already does.
//
// The token value is returned to the caller and never logged.
func (p *pairing) issueDeviceToken(deviceID, name string) (string, error) {
	if deviceID == "" {
		// Keying by "" would file every device under one record and silently
		// replace the previous device's token with the new one.
		return "", errors.New("pairing: a device id is required to issue a token")
	}

	token, err := newDeviceToken()
	if err != nil {
		return "", err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	rec := pairedDevice{
		ID:       deviceID,
		Name:     name,
		Token:    token,
		IssuedAt: p.now().Unix(),
		Peer:     "ble",
	}
	prev, had := p.devices[deviceID]
	p.devices[deviceID] = rec
	if err := p.persistLocked(); err != nil {
		// Roll back, exactly as issueTokenLocked does: a token in memory but
		// not on disk stops working at the next restart, which is worse than
		// failing now.
		if had {
			p.devices[deviceID] = prev
		} else {
			delete(p.devices, deviceID)
		}
		p.logger.Error("pairing: persist BLE-paired device", "device_id", deviceID, "err", err)
		return "", err
	}

	p.logger.Info("pairing: token issued over BLE",
		"device_id", deviceID, "token_len", len(token))
	return token, nil
}
