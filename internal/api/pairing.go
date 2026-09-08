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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// --- Device pairing (BLE provisioning only) ---
//
// A publishable firmware binary carries NO credentials: secrets.h is a
// compile-time header, so a baked-in device token ends up as plaintext inside
// firmware.bin. The device must therefore be GIVEN a token, and BLE pairing is
// how it gets one: the agent mints a per-device token over an encrypted GATT
// link (proven on hardware at 15:53 on 2026-09-07) and records it in
// devices.json alongside the snapshot state.
//
// issueDeviceToken (below) is the sole public entry point. The HTTP pairing
// window endpoints (POST /v1/pair/open, POST /v1/pair/claim, POST /v1/pair/confirm,
// GET /v1/pair) were removed on 2026-09-08 because the firmware half was never
// compiled in — see AUDIT #9/#4 and task 88. Keep pairing.go: BLE provisioning
// depends on matchToken and issueDeviceToken.

const (
	// pairTokenBytes yields a 32-hex-character per-device token (128 bits).
	// Per-device, never a shared secret: re-pairing the same device_id
	// replaces its token and retires the old one.
	pairTokenBytes = 16

	pairMaxBodyBytes = 4096
	pairMaxIDLen     = 64
)

// pairedDevice is one device that completed pairing. Token is persisted (the
// agent must still recognise the device after a restart) but is NEVER put in a
// response body and NEVER logged.
type pairedDevice struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Token    string `json:"token"`
	IssuedAt int64  `json:"issued_at"`
	Peer     string `json:"peer,omitempty"`
}

// pairedDeviceFile is the on-disk shape of the paired-device store.
type pairedDeviceFile struct {
	Devices map[string]pairedDevice `json:"devices"`
}

// pairing holds the persisted set of paired devices. The HTTP pairing window
// fields were removed on 2026-09-08 (task 87): this struct now backs only BLE
// provisioning via issueDeviceToken and matchToken.
type pairing struct {
	mu      sync.Mutex
	devices map[string]pairedDevice
	path    string // devices.json, "" disables persistence (tests only)

	now    func() time.Time
	logger *slog.Logger
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
		devices: map[string]pairedDevice{},
		path:    path,
		now:     now,
		logger:  logger,
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

// newDeviceToken returns a fresh per-device token: 128 bits from crypto/rand,
// hex-encoded so the firmware can store and send it without escaping.
func newDeviceToken() (string, error) {
	buf := make([]byte, pairTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return hex.EncodeToString(buf), nil
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

// decodePairBody reads a small JSON body into dst. An empty body is allowed.
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

// issueDeviceToken mints a per-device token, records it in the paired-device
// store, and returns it. It is the BLE path's token issuer.
//
// WHY THIS IS NOT JUST newDeviceToken(). The token must be RECORDED. A token
// this agent has not persisted is a credential nothing accepts: the device
// would join, present it, and be refused forever.
//
// BLE provisioning mints and records the token over an encrypted GATT link
// BEFORE the device has ever been on Wi-Fi, so there is no HTTP request to
// answer. The token value is returned to the caller and never logged.
func (p *pairing) issueDeviceToken(deviceID, name string) (string, error) {
	if deviceID == "" {
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
