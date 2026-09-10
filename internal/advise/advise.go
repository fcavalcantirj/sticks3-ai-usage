package advise

import (
	"fmt"
	"math"
	"sort"
	"time"

	"usaged/internal/snapshot"
)

// DefaultHorizon is the fixed 4-hour decision window. Felipe decided the
// horizon is always 4 hours — there is no config knob for it.
const DefaultHorizon = 4 * time.Hour

// scorePaceThreshold is the neutral pace ratio. max(1.0, pace) means a pace at
// or below break-even costs nothing; only over-pacing (pace > 1) penalises the
// score, and never produces a negative number.
const scorePaceThreshold = 1.0

// paceCap bounds the computed pace when elapsed-fraction data is degenerate.
const paceCap = 99.9

// paceConfidenceFloor is the elapsed-fraction threshold above which pace is
// trusted at face value. Below it the pace is blended toward neutral (1.0) in
// proportion to how little of the window has actually been observed: a window
// that just reset has only a few minutes of consumption extrapolated across a
// full week, which is not a burn rate. At elapsed >= paceConfidenceFloor the
// blend is a no-op, reducing to today's unblended behaviour. This is a
// continuous blend, not a hard cutoff (task 101).
const paceConfidenceFloor = 0.10

// minElapsedFraction is the floor on elapsed-fraction when timeElapsed <= 0,
// which means the reset sits further out than a whole window — corrupt data
// that must not be scored as pristine.
const minElapsedFraction = 0.01

// Recommendation is one provider's ranked advice entry.
type Recommendation struct {
	ID                   string  `json:"id"`
	Label                string  `json:"label"`
	Score                float64 `json:"score"`
	PaceRatio            float64 `json:"pace_ratio"`
	EffectiveHeadroomPct int     `json:"effective_headroom_pct"`
	Reason               string  `json:"reason"`
	// Blocked is true when some window is at 100% — the provider cannot be
	// used right now even if it has horizon headroom. It is still ranked but
	// excluded from winner selection (task 109).
	Blocked bool `json:"blocked"`
	// BlockedForSec is the seconds until the earliest blocking window resets.
	// Zero when the reset is already past.
	BlockedForSec int `json:"blocked_for_sec"`
}

// Outcome is the full /v1/advise response. Winner is nil (JSON null) when no
// plan has positive headroom; Recommendations always lists every ranked plan.
type Outcome struct {
	Winner          *string          `json:"winner"`
	Recommendations []Recommendation `json:"recommendations"`
}

// windowDurations maps exact Row.K values to their window length. Only these
// three keys participate in advise ranking — exact-key match, no prefix or
// substring matching, so "7d:Fable" and "bal" never qualify.
var windowDurations = map[string]time.Duration{
	"5h":  5 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

// windowResult holds per-window computed data for a provider.
type windowResult struct {
	key       string
	pct       int
	resetAt   time.Time
	windowLen time.Duration
	headroom  int
	pace      float64
	stale     bool
	blocked   bool // pct == 100: gates immediate use
}

// Rank computes the provider ranking from snapshot data. It is pure — no I/O —
// so it is host-testable with fixed clocks.
//
// Ranking rules:
//  1. A window that resets inside the horizon (0 < timeUntilReset <= horizon)
//     is time-weighted: waitFrac = untilReset / horizon; headroom =
//     (100-pct)*waitFrac + 100*(1-waitFrac), pace neutralised to 1.0 — the
//     reset erases the consumption history the pace was measured over.
//  2. A window whose reset is at or before now (past or missing) gets NO reset
//     credit: raw 100-pct headroom, pace 1.0, staleness named in the reason.
//  3. Otherwise (resets outside the horizon): headroom = 100 - pct, pace =
//     used_fraction / elapsed_fraction with elapsed clamped to [0.01, 1.0],
//     then blended toward 1.0 when elapsed < 10% (confidence blend — see
//     task 101). PaceRatio reports the effective (blended) pace.
//  4. A window at pct=100 is BLOCKED — it gates immediate use. A blocked
//     provider is excluded from winner selection while a usable one exists,
//     but still appears in the ranked table with its real numbers (task 109).
//  5. Provider-level: headroom = min across windows (binding cap), pace =
//     the binding window's own pace (not the max — a non-binding window's
//     pace is irrelevant to the decision).
//  6. Score = EffectiveHeadroomPct (headroom only). Pace is displayed but does
//     not affect ranking — pace is trajectory, not capacity (task 109).
//  7. Sort: Score desc, PaceRatio asc, provider ID asc (deterministic ties).
func Rank(providers []snapshot.Provider, now time.Time, horizon time.Duration) Outcome {
	outcome := Outcome{}

	type ranked struct {
		rec Recommendation
	}
	var rankedProv []ranked

	for _, p := range providers {
		// Credit providers are excluded entirely: on a credit bal row, Pct is
		// percent USED of the prepaid total (openrouter.go:
		// 100*TotalUsage/TotalCredits), not a headroom — never compare it
		// against one.
		if p.Kind != "plan" {
			continue
		}
		if p.Status != "ok" && p.Status != "stale" {
			continue
		}

		windows, ok := computeWindows(p, now, horizon)
		if !ok {
			continue
		}

		headroom, binding := aggregate(windows)
		score := float64(headroom)

		// A provider is BLOCKED when any window is at 100% — it cannot be used
		// right now even if other windows show horizon capacity (task 109).
		// blockedForSec tracks the earliest blocking window's reset; this is the
		// key the reason names and the value the card displays.
		provBlocked, blockedKey, blockedForSec := computeBlocked(windows, now)

		rec := Recommendation{
			ID:                   p.ID,
			Label:                p.Label,
			Score:                score,
			PaceRatio:            binding.pace,
			EffectiveHeadroomPct: headroom,
			Reason:               reason(headroom, binding, p.Status, blockedKey, blockedForSec),
			Blocked:              provBlocked,
			BlockedForSec:        blockedForSec,
		}

		rankedProv = append(rankedProv, ranked{
			rec: rec,
		})
	}

	// Decision sort: Score desc, PaceRatio asc, provider ID asc (deterministic
	// ties — snapshot.Rev() hashes an ordered array, so non-determinism would
	// churn rev on every poll).
	sort.SliceStable(rankedProv, func(i, j int) bool {
		a, b := rankedProv[i], rankedProv[j]
		if a.rec.Score != b.rec.Score {
			return a.rec.Score > b.rec.Score
		}
		if a.rec.PaceRatio != b.rec.PaceRatio {
			return a.rec.PaceRatio < b.rec.PaceRatio
		}
		return a.rec.ID < b.rec.ID
	})

	outcome.Recommendations = make([]Recommendation, len(rankedProv))
	for i, rp := range rankedProv {
		outcome.Recommendations[i] = rp.rec
	}

	// Winner (task 109): the top-ranked NON-BLOCKED provider with positive
	// headroom. A blocked provider (some window at 100%) cannot be used now —
	// it is skipped. If every provider with positive headroom is blocked, the
	// winner is the one that frees soonest. When no provider has positive
	// headroom, winner stays nil.
	var winnerID string
	winnerFound := false
	for _, rp := range rankedProv {
		if !rp.rec.Blocked && rp.rec.EffectiveHeadroomPct > 0 {
			winnerID = rp.rec.ID
			winnerFound = true
			break
		}
	}
	if !winnerFound {
		var bestIdx = -1
		for i, rp := range rankedProv {
			if rp.rec.Blocked && rp.rec.EffectiveHeadroomPct > 0 && rp.rec.BlockedForSec > 0 {
				if bestIdx < 0 || rp.rec.BlockedForSec < rankedProv[bestIdx].rec.BlockedForSec {
					bestIdx = i
				}
			}
		}
		if bestIdx >= 0 {
			winnerID = rankedProv[bestIdx].rec.ID
			winnerFound = true
		}
	}
	if winnerFound {
		outcome.Winner = &winnerID
		// WHEN THE TOP-SCORING PROVIDER IS BLOCKED, SAY BOTH THINGS: name the
		// usable winner and mention the blocked one that would otherwise win.
		if rankedProv[0].rec.Blocked && rankedProv[0].rec.ID != winnerID {
			for i, rp := range rankedProv {
				if rp.rec.ID == winnerID {
					outcome.Recommendations[i].Reason += fmt.Sprintf(" — %s is out for %s, then it is the stronger pick",
						rankedProv[0].rec.Label, formatShortDuration(rankedProv[0].rec.BlockedForSec))
					break
				}
			}
		}
		// If the winner is itself blocked (all-blocked fallback), flag it as
		// the soonest-to-free option — the BLOCKED suffix already names the
		// time to reset, so this just explains the selection.
		for i, rp := range rankedProv {
			if rp.rec.ID == winnerID && rp.rec.Blocked {
				outcome.Recommendations[i].Reason += " — soonest to free"
				break
			}
		}
	}

	return outcome
}

// computeWindows extracts quota windows from a provider's rows using exact-key
// matching on Row.K. Returns false when no usable window rows exist.
func computeWindows(p snapshot.Provider, now time.Time, horizon time.Duration) ([]windowResult, bool) {
	var windows []windowResult
	for _, r := range p.Rows {
		windowLen, ok := windowDurations[r.K]
		if !ok {
			continue
		}
		if r.Pct == nil || r.ResetAt == nil {
			continue
		}

		pct := *r.Pct
		resetAt := time.Unix(*r.ResetAt, 0)
		untilReset := resetAt.Sub(now)

		var w windowResult
		w.key = r.K
		w.pct = pct
		w.resetAt = resetAt
		w.windowLen = windowLen

		switch {
		case untilReset <= 0:
			// Past or absent reset — no reset credit, stale data.
			w.headroom = 100 - pct
			w.pace = scorePaceThreshold // 1.0 neutral
			w.stale = true
		case untilReset <= horizon:
			// Resets inside the horizon — time-weighted headroom credit
			// (task 109). A window that resets late in the horizon is worth
			// less than one that resets immediately. Pace stays neutral.
			waitFrac := float64(untilReset) / float64(horizon)
			w.headroom = int(math.Round(float64(100-pct)*waitFrac + 100*(1-waitFrac)))
			w.pace = scorePaceThreshold // 1.0 neutral
		default:
			// Resets outside the horizon — current remaining is the cap.
			w.headroom = 100 - pct
			w.pace = computePace(pct, windowLen, untilReset)
		}

		w.blocked = pct == 100
		windows = append(windows, w)
	}
	return windows, len(windows) > 0
}

// computePace calculates quota_used_fraction / elapsed_fraction, with the
// elapsed fraction clamped to [minElapsedFraction, 1.0] and the result capped
// at paceCap. When timeElapsed <= 0 (reset beyond a full window — corrupt
// data), the floor elapsed fraction keeps pace finite rather than zero.
//
// After the raw pace is computed, a confidence blend shrinks it toward 1.0
// (scorePaceThreshold) when the window is young: a freshly reset window has
// only a tiny sample of consumption, so extrapolating it across the full
// window produces a misleadingly large pace. The blend is continuous and
// reaches full strength (confidence = 1.0) once paceConfidenceFloor (10%) of
// the window has elapsed — at that point it reduces to the raw pace.
//
// NOTE (task 109): this blend now serves only the DISPLAYED pace ratio. It
// no longer affects the score, which is headroom only. Task 101 was a
// reliability fix for the pace number; once pace stopped ranking, 101 became
// non-load-bearing — it only keeps the displayed pace sane after a reset.
func computePace(pct int, windowLen, untilReset time.Duration) float64 {
	timeElapsed := windowLen - untilReset
	elapsedFraction := float64(timeElapsed) / float64(windowLen)
	if elapsedFraction < minElapsedFraction {
		elapsedFraction = minElapsedFraction
	}
	if elapsedFraction > 1.0 {
		elapsedFraction = 1.0
	}
	usedFraction := float64(pct) / 100.0
	paceRaw := usedFraction / elapsedFraction
	if paceRaw > paceCap {
		paceRaw = paceCap
	}
	// Blend raw pace toward neutral for windows too young to be reliable.
	confidence := math.Min(1.0, elapsedFraction/paceConfidenceFloor)
	paceEffective := scorePaceThreshold + (paceRaw-scorePaceThreshold)*confidence
	return paceEffective
}

// aggregate reduces per-window results to provider-level metrics.
// headroom = min (binding cap); binding = the min-headroom window (its pace
// is the provider's pace ratio). On an exact headroom tie the LONGER window
// binds — message 30 — because a longer window is the one that actually
// constrains a 4-hour decision when both caps are equal.
func aggregate(windows []windowResult) (int, windowResult) {
	binding := windows[0]
	for i := 1; i < len(windows); i++ {
		w := windows[i]
		if w.headroom < binding.headroom ||
			(w.headroom == binding.headroom && w.windowLen > binding.windowLen) {
			binding = w
		}
	}
	return binding.headroom, binding
}

// reason builds the one-line explanation for a recommendation. Pace is shown
// as context, not as a ranking factor (task 109). Stale windows are flagged.
// A blocked provider (some window at 100%) gets a "BLOCKED" suffix naming the
// gating window and when it frees.
func reason(headroom int, binding windowResult, status string, blockedKey string, blockedForSec int) string {
	paceStr := fmt.Sprintf("%.2f", binding.pace)
	staleTag := ""
	if status == "stale" || binding.stale {
		staleTag = " (stale)"
	}
	var base string
	if headroom <= 0 {
		base = fmt.Sprintf("all windows exhausted%s (pace %sx)", staleTag, paceStr)
	} else {
		base = fmt.Sprintf("headroom %d%%, binding %s%s (pace %sx)", headroom, binding.key, staleTag, paceStr)
	}
	if blockedKey != "" {
		base += fmt.Sprintf(" — BLOCKED: %s at 100%%, frees in %s", blockedKey, formatShortDuration(blockedForSec))
	}
	return base
}

// computeBlocked scans a provider's windows for any at pct=100 (blocked). It
// returns whether the provider is blocked, the key of the earliest-resetting
// blocking window, and how many seconds until that window resets (0 if the reset
// is already past). The earliest reset is the one that gates immediate use.
func computeBlocked(windows []windowResult, now time.Time) (bool, string, int) {
	var blockedKey string
	var blockedForSec int
	for _, w := range windows {
		if w.pct != 100 {
			continue
		}
		secs := int(w.resetAt.Sub(now).Seconds())
		if secs > 0 {
			if blockedKey == "" || secs < blockedForSec {
				blockedKey = w.key
				blockedForSec = secs
			}
		} else if blockedKey == "" {
			// Reset already past — record the key but no time.
			blockedKey = w.key
		}
	}
	return blockedKey != "", blockedKey, blockedForSec
}

// formatShortDuration renders seconds as a compact "XhYm" or "Xm" string.
func formatShortDuration(sec int) string {
	if sec <= 0 {
		return "unknown"
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}
