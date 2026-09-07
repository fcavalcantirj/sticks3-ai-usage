//go:build !darwin

package bleprov

// central_other.go — the honest answer off macOS.
//
// The provisioning topology is "the device advertises, the Mac connects", and
// the credential sourcing this feature exists to do is macOS-only anyway. There
// is no partial implementation here for the same reason there is none in
// creds_other.go: half a setup path is worse than a clear refusal.

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// ErrAdapterUnavailable exists on every platform so callers can branch on it
// without build tags of their own.
var ErrAdapterUnavailable = errors.New("bleprov: the Bluetooth adapter is unavailable")

// Peripheral is one device seen advertising the provisioning service.
type Peripheral struct {
	Address string    `json:"address"`
	Name    string    `json:"name"`
	RSSI    int16     `json:"rssi"`
	Seen    time.Time `json:"seen"`
}

// Phase is where a provisioning run has got to.
type Phase string

const (
	PhaseConnecting  Phase = "connecting"
	PhaseIdentifying Phase = "identifying"
	PhasePairing     Phase = "pairing"
	PhaseSending     Phase = "sending"
	PhaseCommitting  Phase = "committing"
	PhaseApplying    Phase = "applying"
	PhaseDone        Phase = "done"
)

// Progress is one step forward, reported to the caller's callback.
type Progress struct {
	Phase  Phase   `json:"phase"`
	Note   string  `json:"note"`
	Sent   int     `json:"sent"`
	Total  int     `json:"total"`
	Info   *Info   `json:"info,omitempty"`
	Status *Status `json:"status,omitempty"`
}

// Result is what a successful run learned.
type Result struct {
	Info      Info          `json:"info"`
	Status    Status        `json:"status"`
	ChunkData int           `json:"chunk_data"`
	Chunks    int           `json:"chunks"`
	Stream    int           `json:"stream_bytes"`
	Elapsed   time.Duration `json:"elapsed"`
}

// Central is the same handle as on macOS, and refuses every radio call.
type Central struct {
	PairTimeout  time.Duration
	ApplyTimeout time.Duration
}

// NewCentral builds a Central that reports ErrUnsupportedOS.
func NewCentral(*slog.Logger) *Central { return &Central{} }

// Enable is not implemented off macOS.
func (c *Central) Enable() error { return ErrUnsupportedOS }

// Scan is not implemented off macOS.
func (c *Central) Scan(context.Context, time.Duration) ([]Peripheral, error) {
	return nil, ErrUnsupportedOS
}

// Provision is not implemented off macOS.
func (c *Central) Provision(context.Context, string, Record, func(Progress)) (*Result, error) {
	return nil, ErrUnsupportedOS
}
