package main

// bleprov.go — the wiring point between internal/bleprov (the radio) and
// internal/api (the HTTP routes the dashboard drives).
//
// It exists because the two halves are deliberately kept apart. internal/api
// declares the Provisioner interface it consumes and takes NO credentials, so
// that "no credential value reaches the HTTP layer" is a property you can check
// by reading one file rather than a rule you have to trust. internal/bleprov
// knows the radio and the macOS credential sources but nothing about routes.
// Everything that has to know both lives here, and it is small on purpose.
//
// WHAT THIS FILE DECIDES, AND WHY EACH DECISION IS NOT OBVIOUS:
//
//   * The device is sent a REAL agent address, not an empty Host. The protocol
//     reads an empty Host as "discover the agent over mDNS", and the firmware
//     does ship a discovery module — but hal/sticks3/discover.cpp is NOT
//     REFERENCED by main.cpp, so on the firmware that exists today it is
//     compiled and then discarded by the linker. Worse, hal/sticks3/creds.cpp
//     returns usage::provision::complete(), which requires ssid AND host AND
//     token; a record with no host reads back as UNPROVISIONED on the next
//     boot, so a device provisioned that way would reboot straight back into
//     setup, forever. Sending the address the daemon actually answers on is
//     what makes the round trip terminate.
//
//   * The token is minted through internal/api, not through
//     bleprov.MintToken. Entropy is the easy half; a token the agent has not
//     RECORDED is a credential nothing accepts, and the device would join,
//     present it, and be refused for the rest of its life.
//
//   * Nothing here logs a credential. The Record and Credentials types both
//     redact themselves through LogValue, and this file only ever logs
//     lengths, names an access point already broadcasts, and fixed sentences.

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"usaged/internal/api"
	"usaged/internal/bleprov"
	"usaged/internal/config"
)

const (
	// bleScanWindow is how long a scan listens. The dashboard's own scan
	// timeout is 12 s (api.setupScanTimeout), so this must finish comfortably
	// inside it or the route gives up on its own adapter.
	bleScanWindow = 6 * time.Second

	// bleGatherTimeout bounds the credential gather. The keychain read can
	// raise an authorisation dialog, and an unanswered dialog blocks the
	// `security` tool indefinitely — measured: a 30 s bounded run exited 124
	// with no output. The Gatherer bounds its own calls too; this is the belt
	// to that pair of braces.
	bleGatherTimeout = 45 * time.Second
)

// bleProvisioner adapts *bleprov.Central to api.ProgressProvisioner.
//
// It implements api.TokenIssuerAware, so internal/api hands it the
// mint-and-record function during api.New — the pairing store is unexported and
// api.New returns an *http.Server, so this is the only way to reach it.
type bleProvisioner struct {
	central  *bleprov.Central
	gatherer *bleprov.Gatherer
	otaPass  string
	logger   *slog.Logger

	mu    sync.RWMutex
	issue func(deviceID, name string) (string, error)
	// names remembers what each address advertised, so a provisioned device is
	// recorded under "ai-usage-68B8" rather than a raw CoreBluetooth UUID that
	// means nothing to the owner. ProvisionProgress is handed only an address.
	names map[string]string
}

// newBLEProvisioner builds the adapter. It does NOT touch the radio: powering
// the adapter up is deferred to the first scan, so a Mac with Bluetooth off
// costs nothing at boot and the dashboard reports it as an unavailable adapter
// rather than a failed start.
func newBLEProvisioner(cfg config.Config, logger *slog.Logger) *bleProvisioner {
	if logger == nil {
		logger = slog.Default()
	}
	return &bleProvisioner{
		central:  bleprov.NewCentral(logger),
		gatherer: bleprov.NewGatherer(cfg.Listen, logger),
		otaPass:  cfg.DeviceOTAPass,
		logger:   logger,
	}
}

// SetTokenIssuer satisfies api.TokenIssuerAware.
func (p *bleProvisioner) SetTokenIssuer(issue func(deviceID, name string) (string, error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.issue = issue
}

func (p *bleProvisioner) tokenIssuer() func(string, string) (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.issue
}

// Scan satisfies api.Provisioner.
//
// FoundDevice.Provisioned is left FALSE for every result, and that is a known
// limitation rather than an oversight: the flag lives in the Info
// characteristic, Central.Scan never connects, and reading Info would mean a
// connect-and-disconnect per device inside the dashboard's 12 s scan budget.
// The consequence is that api.candidates() treats an already-configured stick
// as a candidate, so the page may offer to re-provision one. That is a
// re-provision the owner asked for by clicking, not a destructive surprise —
// but it is why the dashboard says which device it is about to write to.
func (p *bleProvisioner) Scan(ctx context.Context) ([]api.FoundDevice, error) {
	found, err := p.central.Scan(ctx, bleScanWindow)
	if err != nil {
		return nil, err
	}
	out := make([]api.FoundDevice, 0, len(found))
	for _, f := range found {
		out = append(out, api.FoundDevice{
			Addr: f.Address,
			Name: f.Name,
			RSSI: int(f.RSSI),
		})
	}
	p.mu.Lock()
	if p.names == nil {
		p.names = map[string]string{}
	}
	for _, f := range found {
		if f.Name != "" {
			p.names[f.Address] = f.Name
		}
	}
	p.mu.Unlock()

	p.logger.Info("ble setup: scan complete", "devices", len(out))
	return out, nil
}

// Provision satisfies api.Provisioner for an implementation that cannot narrate
// itself. It is never the path taken — ProvisionProgress below is preferred by
// internal/api whenever it is present — but the interface requires it and a
// panic-or-nil stub would be a trap for the next reader.
func (p *bleProvisioner) Provision(ctx context.Context, addr string) error {
	return p.ProvisionProgress(ctx, addr, func(string) {})
}

// ProvisionProgress satisfies api.ProgressProvisioner: gather everything the
// device needs, mint it a token this agent will accept, and hand the lot over
// the bonded link.
//
// It returns nil ONLY when the device itself reported Applied — stored AND
// joined. That contract belongs to Central.Provision and is not softened here.
func (p *bleProvisioner) ProvisionProgress(ctx context.Context, addr string, report func(step string)) error {
	gatherCtx, cancel := context.WithTimeout(ctx, bleGatherTimeout)
	defer cancel()

	// SkipToken because the token this package mints is not recordable — see
	// the file comment. We fill Record.Token ourselves, below.
	creds, err := p.gatherer.Gather(gatherCtx, bleprov.GatherOptions{SkipToken: true})
	if err != nil {
		return err
	}
	defer creds.Wipe()

	for _, w := range creds.Warnings {
		// Warnings are fixed sentences or a name the access point already
		// broadcasts — never a credential. Surfaced so a redacted SSID or a
		// refused keychain is visible in the log rather than silently shaping
		// what got sent.
		p.logger.Warn("ble setup: " + w)
	}
	p.logger.Info("ble setup: credentials gathered", "creds", creds)

	rec := bleprov.Record{
		SSID:     creds.SSID,
		Password: creds.Password,
		Host:     creds.Host,
		Port:     creds.Port,
		OTAPass:  p.otaPass,
	}
	defer rec.Wipe()

	if rec.Host == "" {
		// Refuse rather than ship a record the device cannot come back from.
		// See the file comment: complete() needs a host, so a hostless record
		// reads back as unprovisioned and the device re-enters setup forever.
		return &api.ProvisionFailure{Stage: api.StageTransfer}
	}

	// The token is minted per device and recorded before it is sent. The device
	// is keyed by the identity it advertises, which is the same "usaged-XXXX"
	// name the captive portal AP uses — one device, one name, however it is set
	// up.
	deviceID, deviceName := p.deviceIdentity(addr)
	if issue := p.tokenIssuer(); issue != nil {
		token, err := issue(deviceID, deviceName)
		if err != nil {
			return err
		}
		rec.Token = token
	} else {
		// No issuer wired: send no token. The protocol reads that as "get one
		// by pairing", which is a legal state — but on today's firmware
		// hal/sticks3/pair.cpp is unreferenced too, so the device would never
		// collect one. Loud, because it is a wiring bug and not a user error.
		p.logger.Error("ble setup: no token issuer wired; the device will be sent no token")
	}

	res, err := p.central.Provision(ctx, addr, rec, func(pr bleprov.Progress) {
		if step := bleStepFor(pr.Phase); step != "" {
			report(step)
		}
	})
	if err != nil {
		return bleStageFor(err)
	}
	p.logger.Info("ble setup: device applied the record",
		"chunks", res.Chunks, "stream_bytes", res.Stream, "elapsed", res.Elapsed)
	return nil
}

// deviceIdentity derives the paired-device key and its human label. The address
// is the key — stable for the life of the bond on macOS, which is what the
// paired-device store needs. The name comes from the last scan, so the
// dashboard shows "ai-usage-68B8" instead of a CoreBluetooth UUID.
func (p *bleProvisioner) deviceIdentity(addr string) (id, name string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return addr, p.names[addr]
}

// bleStepFor maps the central's own phases onto the fixed step vocabulary
// internal/api will render. ANY OTHER VALUE IS IGNORED by that package, so an
// unmapped phase is dropped rather than shown — which is why the mapping is
// explicit and total rather than a string cast.
func bleStepFor(ph bleprov.Phase) string {
	switch ph {
	case bleprov.PhaseConnecting:
		return api.StepConnecting
	case bleprov.PhaseIdentifying:
		return api.StepReadingInfo
	case bleprov.PhasePairing:
		return api.StepPairing
	case bleprov.PhaseSending, bleprov.PhaseCommitting:
		return api.StepSending
	case bleprov.PhaseApplying:
		return api.StepApplying
	default:
		// PhaseDone has no step: the run is over and internal/api renders the
		// outcome, not another step.
		return ""
	}
}

// CurrentNetwork satisfies api.NetworkNamer: the Wi-Fi network this Mac is on,
// so the dashboard can pre-fill it instead of asking someone to type a name
// their own computer already knows.
//
// An SSID is broadcast by the access point continuously, so it is the one thing
// the Gatherer produces that is safe to render. The password is never returned
// here and never reaches the page.
//
// confident is false when macOS redacted the association and the name is a
// guess from the preferred-network list — the caller is expected to say so
// rather than present it as fact.
func (p *bleProvisioner) CurrentNetwork(ctx context.Context) (string, bool) {
	iface, err := p.gatherer.WiFiInterface(ctx)
	if err != nil {
		return "", false
	}
	ssid, _, err := p.gatherer.CurrentSSID(ctx, iface)
	if ssid == "" {
		return "", false
	}
	return ssid, err == nil
}

// bleStageFor tags a transport failure with the STAGE it happened at, so the
// dashboard can say something true instead of the generic sentence.
//
// Device errors are left alone: *bleprov.DeviceError implements
// api.ProvisionCoded and carries the device's own error number, which
// internal/api renders far better than a stage could. This maps only the
// failures that happen on THIS side, before or instead of the device ever
// answering — where a stage is all there is to say.
func bleStageFor(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, bleprov.ErrAdapterUnavailable), errors.Is(err, bleprov.ErrNoDevice):
		return &api.ProvisionFailure{Stage: api.StageScan}
	case errors.Is(err, bleprov.ErrBondLost), errors.Is(err, bleprov.ErrPairingTimeout):
		return &api.ProvisionFailure{Stage: api.StagePair}
	case errors.Is(err, bleprov.ErrPanicked):
		return &api.ProvisionFailure{Stage: api.StageConnect}
	default:
		// Includes *bleprov.DeviceError, which api classifies by its own code.
		return err
	}
}
