package sched

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"usaged/internal/format"
	"usaged/internal/providers"
	"usaged/internal/snapshot"
)

// Scheduler polls a set of provider Fetchers concurrently, merges their
// results into a single Snapshot, and persists state between runs.
type Scheduler struct {
	Fetchers  []providers.Fetcher
	Interval  time.Duration
	State     snapshot.State
	StatePath string
	Clock     func() time.Time
	Logger    *slog.Logger
	mu        sync.RWMutex // protects State
	pollMu    sync.Mutex   // serialises PollOnce/Refresh
}

// NewScheduler creates a Scheduler with sane defaults. If clock is nil,
// time.Now is used.
func NewScheduler(fetchers []providers.Fetcher, interval time.Duration, statePath string, clock func() time.Time, logger *slog.Logger) *Scheduler {
	if clock == nil {
		clock = time.Now
	}
	s := &Scheduler{
		Fetchers:  fetchers,
		Interval:  interval,
		StatePath: statePath,
		Clock:     clock,
		Logger:    logger,
	}
	s.mu.Lock()
	s.ensureMaps()
	s.mu.Unlock()
	return s
}

// ensureMaps initialises the Cooldowns and LastGood maps. Caller must hold mu.
func (s *Scheduler) ensureMaps() {
	if s.State.Cooldowns == nil {
		s.State.Cooldowns = make(map[string]int64)
	}
	if s.State.LastGood == nil {
		s.State.LastGood = make(map[string]snapshot.Provider)
	}
}

// PollOnce runs a single polling cycle across all fetchers.
func (s *Scheduler) PollOnce(ctx context.Context) {
	s.pollMu.Lock()
	defer s.pollMu.Unlock()
	s.pollOnce(ctx)
}

func (s *Scheduler) pollOnce(ctx context.Context) {
	now := s.Clock()
	nowUnix := now.Unix()

	s.mu.Lock()
	s.ensureMaps()
	s.mu.Unlock()

	type fetchResult struct {
		provider snapshot.Provider
		outcome  providers.Outcome
	}

	results := make(map[string]fetchResult)
	var wg sync.WaitGroup
	var resultsMu sync.Mutex

	for _, f := range s.Fetchers {
		wg.Add(1)
		go func(f providers.Fetcher) {
			defer wg.Done()

			id := f.ID()
			fctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()

			s.mu.RLock()
			cooldown := s.State.Cooldowns[id]
			lastGood, hasLastGood := s.State.LastGood[id]
			s.mu.RUnlock()

			var provider snapshot.Provider
			var outcome providers.Outcome

			if cooldown > nowUnix {
				// Cooldown active — reuse last-good block, mark stale.
				if hasLastGood {
					provider = lastGood
					provider.Status = "stale"
					provider.Msg = fmt.Sprintf("429 until %s", time.Unix(cooldown, 0).Format("15:04"))
					recomputeTiers(provider.Rows)
				} else {
					provider = snapshot.Provider{
						ID:        id,
						Status:    "stale",
						Msg:       fmt.Sprintf("429 until %s", time.Unix(cooldown, 0).Format("15:04")),
						FetchedAt: nowUnix,
					}
				}
			} else {
				provider, outcome = f.Fetch(fctx, now)

				if provider.Status == "ok" {
					s.mu.Lock()
					s.State.LastGood[id] = provider
					s.mu.Unlock()
				} else if hasLastGood {
					// Fetch failed but we have last-good data — serve it
					// with stale status and the error msg from the outcome.
					fetchMsg := provider.Msg
					provider = lastGood
					provider.Status = "stale"
					provider.Msg = fetchMsg
					recomputeTiers(provider.Rows)
				}

				if !outcome.CooldownUntil.IsZero() {
					s.mu.Lock()
					s.State.Cooldowns[id] = outcome.CooldownUntil.Unix()
					s.mu.Unlock()
				}
			}

			// A block whose data is older than 2×Interval is also stale.
			if provider.FetchedAt > 0 {
				if now.Sub(time.Unix(provider.FetchedAt, 0)) > 2*s.Interval {
					provider.Status = "stale"
				}
			}

			resultsMu.Lock()
			results[id] = fetchResult{provider: provider, outcome: outcome}
			resultsMu.Unlock()
		}(f)
	}

	wg.Wait()

	// Collect blocks in fetcher order, then sort into canonical order.
	var blockList []snapshot.Provider
	for _, f := range s.Fetchers {
		if r, ok := results[f.ID()]; ok {
			blockList = append(blockList, r.provider)
		}
	}

	ids := make([]string, len(blockList))
	for i, p := range blockList {
		ids[i] = p.ID
	}
	sortedIDs := snapshot.Order(ids)
	byID := make(map[string]snapshot.Provider, len(blockList))
	for _, p := range blockList {
		byID[p.ID] = p
	}
	ordered := make([]snapshot.Provider, 0, len(sortedIDs))
	for _, id := range sortedIDs {
		if p, ok := byID[id]; ok {
			ordered = append(ordered, p)
		}
	}

	s.mu.Lock()
	s.State.Snapshot.Apply(ordered, nowUnix)
	if s.StatePath != "" {
		if err := snapshot.Save(s.StatePath, s.State); err != nil {
			slog.Debug("sched: save state error", "err", err)
		}
	}
	s.mu.Unlock()
}

// recomputeTiers recalculates row tiers using "stale" as the status.
// Rows with nil pct (e.g. the balance row) keep their existing tier.
func recomputeTiers(rows []snapshot.Row) {
	for i := range rows {
		if rows[i].Pct != nil {
			rows[i].Tier = format.Tier(rows[i].Pct, "stale")
		}
	}
}

// Refresh triggers an immediate poll. If a poll is already running, this
// call is coalesced (dropped) to avoid overlapping polls and returns false.
// Otherwise it runs the poll synchronously and returns true.
func (s *Scheduler) Refresh() bool {
	if !s.pollMu.TryLock() {
		return false
	}
	defer s.pollMu.Unlock()
	s.pollOnce(context.Background())
	return true
}

// LoadState replaces the scheduler's in-memory state with the provided one.
// Called once at startup (before Run begins) to restore cooldowns and
// last-good blocks from the on-disk state file.
func (s *Scheduler) LoadState(state snapshot.State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.State = state
	s.ensureMaps()
}

// Run polls immediately, then on an Interval ticker with ±10% jitter until
// ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	s.PollOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.jitteredInterval()):
			s.PollOnce(ctx)
		}
	}
}

// jitteredInterval returns Interval ± 10%.
func (s *Scheduler) jitteredInterval() time.Duration {
	if s.Interval <= 0 {
		return 0
	}
	jitter := time.Duration((rand.Float64()*0.2 - 0.1) * float64(s.Interval))
	return s.Interval + jitter
}

// Current returns a copy of the current Snapshot under the read lock.
func (s *Scheduler) Current() snapshot.Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.State.Snapshot
}
