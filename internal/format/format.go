package format

import (
	"fmt"
	"math"
	"time"

	"usaged/internal/snapshot"
)

// DefaultLocation is the display timezone for reset text.
// It defaults to America/Sao_Paulo (UTC−3, no DST in 2026) and is loaded
// once at package init. Callers normally pass config.TZ explicitly.
var DefaultLocation = func() *time.Location {
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		loc = time.FixedZone("UTC-3", -3*3600)
	}
	return loc
}()

// ResetTxt returns a human-friendly countdown text for a rate-limit reset time.
//
//   - zero reset → "--"
//   - reset <= now → "now"
//   - reset within 24 h → "15:04" in loc
//   - otherwise → 3-letter weekday of reset in loc (e.g. "Mon")
func ResetTxt(reset time.Time, now time.Time, loc *time.Location) string {
	if reset.IsZero() {
		return "--"
	}
	if !reset.After(now) {
		return "now"
	}
	if reset.Sub(now) < 24*time.Hour {
		return reset.In(loc).Format("15:04")
	}
	return reset.In(loc).Format("Mon")
}

// Tier maps (pct, status) to a qualitative tier string.
// nil pct or a status other than ok/stale → "off".
// Otherwise: <50 ok, <80 warn, >=80 crit.
//
// Tier is per-row cosmetics only and is NOT driven by the alert knobs in
// format.Alerts. Severity is the alert engine; Tier is the row colour. They
// serve different purposes and must not be conflated — see PROD-READY 2/10.
func Tier(pct *int, status string) string {
	if pct == nil {
		return "off"
	}
	if status != "ok" && status != "stale" {
		return "off"
	}
	v := *pct
	if v < 50 {
		return "ok"
	}
	if v < 80 {
		return "warn"
	}
	return "crit"
}

// Alerts holds the alert threshold configuration, threaded from the daemon's
// config into every provider constructor so there is exactly one source of
// truth for the warn percentages.
type Alerts struct {
	Warn5hPct     int // warn when a short-window (5h) quota row reaches this %
	WarnWeeklyPct int // warn when a weekly-window quota row reaches this %
}

// DefaultAlerts returns the standard alert thresholds: 70 % for 5h windows
// (recoverable in an afternoon) and 60 % for weekly windows (earlier notice).
func DefaultAlerts() Alerts {
	return Alerts{Warn5hPct: 70, WarnWeeklyPct: 60}
}

// WarnPctFor returns the warning percentage threshold for a row with window
// key k. "7d" rows use the weekly threshold; everything else — including
// rows with no window key (OpenRouter credit rows, Groq rate-limit rows) —
// uses the short-window (5h) threshold. This is a deliberate rule: a credit
// balance or rate-limit bucket has no weekly cadence to warn against early,
// so it takes the sooner threshold.
// NOTE: task 92 (OpenCode Go) will add a "30d" window key that MUST map to the
// weekly threshold, not the 5h one — a monthly cap you cannot recover from
// deserves the earlier warning. Add "30d" alongside "7d" then.
func (a Alerts) WarnPctFor(k string) int {
	if k == "7d" || k == "30d" {
		return a.WarnWeeklyPct
	}
	return a.Warn5hPct
}

// Severity computes the provider-level severity from status, rows, and the
// alert thresholds in alerts.
//
// Rules (in priority order):
//   - auth/error status → "crit" (hard fact)
//   - any quota row at ≥100 % → "crit" (fully exhausted)
//   - any quota row at or above its window-specific warn threshold → "warn"
//     (5h rows use Warn5hPct; 7d rows use WarnWeeklyPct)
//   - otherwise → "ok"
func Severity(status string, rows []snapshot.Row, alerts Alerts) string {
	if status == "auth" || status == "error" {
		return "crit"
	}
	for _, r := range rows {
		if r.Pct != nil && *r.Pct >= 100 {
			return "crit"
		}
	}
	for _, r := range rows {
		if r.Pct != nil && *r.Pct >= alerts.WarnPctFor(r.K) {
			return "warn"
		}
	}
	return "ok"
}

// Cents converts a dollar amount to integer cents, rounding half away from zero.
func Cents(v float64) int {
	return int(math.Round(v * 100))
}

// Money formats an integer-cents value as "$1.23" with no thousands separator.
// Negative amounts render as "-$1.23".
func Money(cents int) string {
	if cents < 0 {
		return fmt.Sprintf("-$%d.%02d", -cents/100, -cents%100)
	}
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}

// Credits formats a credit count. Whole numbers above 10 render without a
// decimal; values below 10 keep one decimal place (e.g. "5.3 cr", "178 cr").
func Credits(n float64) string {
	if n < 10 {
		return fmt.Sprintf("%.1f cr", n)
	}
	return fmt.Sprintf("%d cr", int(math.Round(n)))
}

// Age returns a compact age string: "just now", "12m", "3h", or "2d".
func Age(now, then int64) string {
	d := now - then
	if d < 0 {
		d = 0
	}
	switch {
	case d < 60:
		return "just now"
	case d < 3600:
		return fmt.Sprintf("%dm", d/60)
	case d < 86400:
		return fmt.Sprintf("%dh", d/3600)
	default:
		return fmt.Sprintf("%dd", d/86400)
	}
}
