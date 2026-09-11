package main

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"usaged/internal/api"
	"usaged/internal/bleprov"
	"usaged/internal/config"
)

// TestBleStageForFieldError pins the classification of a LOCAL refusal.
//
// v0.3.0's installer minted a 64-character OTA password against a 63-byte wire
// limit, so Encode rejected every record before any radio traffic. The error
// was a *bleprov.FieldError — which exists precisely so the dashboard can use
// one switch for local and remote refusals — but bleStageFor dropped it into
// default. The owner got "Setup did not finish. Press the blue button on the
// device", six times, about a device that was never contacted.
func TestBleStageForFieldError(t *testing.T) {
	err := bleStageFor(&bleprov.FieldError{Field: "ota_password", Code: bleprov.ErrFieldTooLong})

	var fail *api.ProvisionFailure
	if !errors.As(err, &fail) {
		t.Fatalf("a FieldError must classify, got %#v", err)
	}
	if got, want := fail.DeviceCode, int(bleprov.ErrFieldTooLong); got != want {
		t.Errorf("device code = %d, want %d", got, want)
	}
	// The rendered sentence must name the refusal, not send the owner to press
	// a button on a device that was never contacted.
	if got := err.Error(); strings.Contains(strings.ToLower(got), "blue button") || got == "" {
		t.Errorf("still renders the generic sentence: %q", got)
	} else {
		t.Logf("renders: %q", got)
	}
}

// TestOverlongOtaPassIsDropped verifies that a password the wire cannot carry
// costs the owner OTA, never the whole setup. Sending it fails every
// provisioning run; truncating it would leave the Mac and the device holding
// different passwords and break OTA a second, quieter way. Dropping it
// provisions the device OTA-disarmed, which is the documented default for an
// unset password anyway — and says so loudly in the log.
func TestOverlongOtaPassIsDropped(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	tooLong := strings.Repeat("a", bleprov.MaxOtaPass+1)
	p := newBLEProvisioner(config.Config{DeviceOTAPass: tooLong}, logger)

	if p.otaPass != "" {
		t.Errorf("otaPass = %d chars, want it dropped", len(p.otaPass))
	}
	if !strings.Contains(logBuf.String(), "too long to provision") {
		t.Errorf("the drop must be logged; got:\n%s", logBuf.String())
	}
	if strings.Contains(logBuf.String(), tooLong) {
		t.Error("the password value itself must never be logged")
	}
}

// TestUsableOtaPassSurvives guards the other direction: a legal password must
// reach the record untouched, or devices would silently never be armed.
func TestUsableOtaPassSurvives(t *testing.T) {
	ok := strings.Repeat("b", bleprov.MaxOtaPass)
	p := newBLEProvisioner(config.Config{DeviceOTAPass: ok}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if p.otaPass != ok {
		t.Errorf("a %d-char password was not passed through", len(ok))
	}
}
