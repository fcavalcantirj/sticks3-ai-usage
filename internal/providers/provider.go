package providers

import (
	"context"
	"time"

	snapshot "usaged/internal/snapshot"
)

// Fetcher is implemented by each provider's usage fetcher.
type Fetcher interface {
	// ID returns the provider identifier (e.g. "claude").
	ID() string
	// Fetch returns the normalised provider block and an Outcome.
	Fetch(ctx context.Context, now time.Time) (snapshot.Provider, Outcome)
}

// Outcome is the non-snapshot result of a Fetch.
type Outcome struct {
	Err           error
	CooldownUntil time.Time // zero = no cooldown
}
