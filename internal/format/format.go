package format

import (
	"fmt"
	"math"
	"time"
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
