package api

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- BLE provisioning test rig ---
//
// The HTTP pairing window endpoints were removed on 2026-09-08 (task 87: the
// firmware half was never compiled in). pairing.go now backs only BLE
// provisioning via issueDeviceToken and matchToken. These tests cover that store.

type pairRig struct {
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
	return rig
}

// --- Token store (kept: BLE provisioning needs these) ---

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

// TestIssueDeviceTokenRecordsAndAuthenticates covers the token path BLE needs.
//
// BLE provisioning mints and records a per-device token over an encrypted GATT
// link BEFORE the device is on Wi-Fi. The token must be RECORDED — a credential
// nothing accepts is worse than none.
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
