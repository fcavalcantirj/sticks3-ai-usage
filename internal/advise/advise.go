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
}

// Rank computes the provider ranking from snapshot data. It is pure — no I/O —
// so it is host-testable with fixed clocks.
//
// Ranking rules:
//  1. A window that resets inside the horizon (0 < timeUntilReset <= horizon)
//     is worth its full budget (headroom 100, pace neutralised to 1.0) — the
//     reset erases the consumption history the pace was measured over.
//  2. A window whose reset is at or before now (past or missing) gets NO reset
//     credit: raw 100-pct headroom, pace 1.0, staleness named in the reason.
//  3. Otherwise (resets outside the horizon): headroom = 100 - pct, pace =
//     used_fraction / elapsed_fraction with elapsed clamped to [0.01, 1.0].
//  4. Provider-level: headroom = min across windows (binding cap), pace =
//     the binding window's own pace (not the max — a non-binding window's
//     pace is irrelevant to the decision).
//  5. Score = headroom / max(1.0, bindingPace). Only over-pacing penalises.
//  6. Sort: Score desc, PaceRatio asc, binding-window reset time asc.
func Rank(providers []snapshot.Provider, now time.Time, horizon time.Duration) Outcome {
	outcome := Outcome{}

	type ranked struct {
		rec       Recommendation
		bindingAt time.Time
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
		score := math.Round(float64(headroom)/math.Max(scorePaceThreshold, binding.pace)*100) / 100

		rec := Recommendation{
			ID:                   p.ID,
			Label:                p.Label,
			Score:                score,
			PaceRatio:            binding.pace,
			EffectiveHeadroomPct: headroom,
			Reason:               reason(headroom, binding, p.Status),
		}

		rankedProv = append(rankedProv, ranked{
			rec:       rec,
			bindingAt: binding.resetAt,
		})
	}

	// Decision sort: Score desc, PaceRatio asc, binding-window reset time asc
	// (sooner reset → recovers faster → ranks higher on ties).
	sort.SliceStable(rankedProv, func(i, j int) bool {
		a, b := rankedProv[i], rankedProv[j]
		if a.rec.Score != b.rec.Score {
			return a.rec.Score > b.rec.Score
		}
		if a.rec.PaceRatio != b.rec.PaceRatio {
			return a.rec.PaceRatio < b.rec.PaceRatio
		}
		return a.bindingAt.Before(b.bindingAt)
	})

	outcome.Recommendations = make([]Recommendation, len(rankedProv))
	for i, rp := range rankedProv {
		outcome.Recommendations[i] = rp.rec
	}

	// Winner: top-ranked with positive headroom. When no plan has positive
	// headroom, winner stays nil — the ranked plans remain listed at zero.
	if len(rankedProv) > 0 && rankedProv[0].rec.EffectiveHeadroomPct > 0 {
		id := rankedProv[0].rec.ID
		outcome.Winner = &id
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
			// Resets inside the horizon — full budget, pace neutralised.
			w.headroom = 100
			w.pace = scorePaceThreshold // 1.0 neutral
		default:
			// Resets outside the horizon — current remaining is the cap.
			w.headroom = 100 - pct
			w.pace = computePace(pct, windowLen, untilReset)
		}

		windows = append(windows, w)
	}
	return windows, len(windows) > 0
}

// computePace calculates quota_used_fraction / elapsed_fraction, with the
// elapsed fraction clamped to [minElapsedFraction, 1.0] and the result capped
// at paceCap. When timeElapsed <= 0 (reset beyond a full window — corrupt
// data), the floor elapsed fraction keeps pace finite rather than zero.
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
	pace := usedFraction / elapsedFraction
	if pace > paceCap {
		pace = paceCap
	}
	return pace
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

// reason builds the one-line explanation for a recommendation. Pace is always
// two-decimal. Stale windows (past/absent reset) are flagged.
func reason(headroom int, binding windowResult, status string) string {
	paceStr := fmt.Sprintf("%.2f", binding.pace)
	staleTag := ""
	if status == "stale" || binding.stale {
		staleTag = " (stale)"
	}
	if headroom <= 0 {
		return fmt.Sprintf("all windows exhausted, pace %sx%s", paceStr, staleTag)
	}
	return fmt.Sprintf("headroom %d%%, pace %sx, binding %s%s", headroom, paceStr, binding.key, staleTag)
}
