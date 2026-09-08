package format

import (
	"testing"
	"time"

	"usaged/internal/snapshot"
)

// saoPaulo is a fixed UTC-3 zone (no DST in 2026), matching config.DefaultTZ.
var saoPaulo = time.FixedZone("America/Sao_Paulo", -3*3600)

func TestResetTxtFutureTime(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 9, 3, 2, 9, 59, 0, time.UTC) // 02:09:59Z → 23:09 in UTC-3
	got := ResetTxt(reset, now, saoPaulo)
	if got != "23:09" {
		t.Errorf("ResetTxt = %q, want 23:09", got)
	}
}

func TestResetTxtWeekday(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 9, 7, 7, 59, 59, 0, time.UTC) // Mon in UTC-3
	got := ResetTxt(reset, now, saoPaulo)
	if got != "Mon" {
		t.Errorf("ResetTxt = %q, want Mon", got)
	}
}

func TestResetTxtPast(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	got := ResetTxt(reset, now, saoPaulo)
	if got != "now" {
		t.Errorf("ResetTxt = %q, want now", got)
	}
}

func TestResetTxtZero(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	got := ResetTxt(time.Time{}, now, saoPaulo)
	if got != "--" {
		t.Errorf("ResetTxt(zero) = %q, want --", got)
	}
}

func TestTierBoundaries(t *testing.T) {
	tests := []struct {
		pct  int
		want string
	}{
		{49, "ok"},
		{50, "warn"},
		{79, "warn"},
		{80, "crit"},
	}
	for _, tc := range tests {
		got := Tier(intPtr(tc.pct), "ok")
		if got != tc.want {
			t.Errorf("Tier(%d, ok) = %q, want %q", tc.pct, got, tc.want)
		}
	}
}

func TestTierOff(t *testing.T) {
	// nil pct
	if got := Tier(nil, "ok"); got != "off" {
		t.Errorf("Tier(nil, ok) = %q, want off", got)
	}
	// bad status
	if got := Tier(intPtr(80), "error"); got != "off" {
		t.Errorf("Tier(80, error) = %q, want off", got)
	}
	// stale + low pct still ok
	if got := Tier(intPtr(49), "stale"); got != "ok" {
		t.Errorf("Tier(49, stale) = %q, want ok", got)
	}
}

func TestSeverityWindowSpecificThresholds(t *testing.T) {
	alerts := DefaultAlerts() // Warn5hPct=70, WarnWeeklyPct=60
	pct62 := 62

	// 7d row at 62% → warn (weekly threshold 60, 62 >= 60)
	row7d := []snapshot.Row{{K: "7d", Label: "CLAUDE 7d", Pct: &pct62, Tier: "ok", Txt: "Mon"}}
	if got := Severity("ok", row7d, alerts); got != "warn" {
		t.Errorf("Severity(7d@62%%) = %q, want warn (weekly threshold 60)", got)
	}

	// 5h row at 62% → ok (5h threshold 70, 62 < 70)
	row5h := []snapshot.Row{{K: "5h", Label: "CLAUDE 5h", Pct: &pct62, Tier: "ok", Txt: "15:04"}}
	if got := Severity("ok", row5h, alerts); got != "ok" {
		t.Errorf("Severity(5h@62%%) = %q, want ok (5h threshold 70)", got)
	}
}

func TestSeverityCritAt100(t *testing.T) {
	alerts := DefaultAlerts()
	pct100 := 100
	row := []snapshot.Row{{K: "5h", Label: "X", Pct: &pct100, Tier: "crit", Txt: "now"}}
	if got := Severity("ok", row, alerts); got != "crit" {
		t.Errorf("Severity(100%%) = %q, want crit", got)
	}
}

func TestSeverityAuthErrorIsCrit(t *testing.T) {
	alerts := DefaultAlerts()
	row := []snapshot.Row{{K: "5h", Label: "X", Pct: nil, Tier: "off", Txt: "run claude"}}
	for _, status := range []string{"auth", "error"} {
		if got := Severity(status, row, alerts); got != "crit" {
			t.Errorf("Severity(%q) = %q, want crit", status, got)
		}
	}
}

func TestWarnPctFor(t *testing.T) {
	a := Alerts{Warn5hPct: 70, WarnWeeklyPct: 60}
	if a.WarnPctFor("7d") != 60 {
		t.Errorf("WarnPctFor(\"7d\") = %d, want 60", a.WarnPctFor("7d"))
	}
	if a.WarnPctFor("5h") != 70 {
		t.Errorf("WarnPctFor(\"5h\") = %d, want 70", a.WarnPctFor("5h"))
	}
	// Rows with no window key (OpenRouter credit, Groq rate) take the short-window threshold
	if a.WarnPctFor("key") != 70 {
		t.Errorf("WarnPctFor(\"key\") = %d, want 70", a.WarnPctFor("key"))
	}
	// "30d" maps to the WEEKLY threshold (not 5h) — OpenCode Go monthly caps
	// are unrecoverable, so they deserve the earlier warning.
	if a.WarnPctFor("30d") != 60 {
		t.Errorf("WarnPctFor(\"30d\") = %d, want 60", a.WarnPctFor("30d"))
	}
}

func TestMoney(t *testing.T) {
	tests := []struct {
		cents int
		want  string
	}{
		{7, "$0.07"},
		{17810, "$178.10"},
		{-7, "-$0.07"},
		{100000, "$1000.00"},
	}
	for _, tc := range tests {
		got := Money(tc.cents)
		if got != tc.want {
			t.Errorf("Money(%d) = %q, want %q", tc.cents, got, tc.want)
		}
	}
}

func TestCents(t *testing.T) {
	got := Cents(9.929127902)
	if got != 993 {
		t.Errorf("Cents(9.929127902) = %d, want 993", got)
	}
	if got := Cents(-9.929127902); got != -993 {
		t.Errorf("Cents(-9.929127902) = %d, want -993", got)
	}
}

func TestAge(t *testing.T) {
	now := int64(1000)
	tests := []struct {
		then int64
		want string
	}{
		{1000, "just now"}, // d=0
		{280, "12m"},       // d=720s = 12m
		{-9800, "3h"},      // d=10800s = 3h
		{-171800, "2d"},    // d=172800s = 2d
		{940, "1m"},        // d=60s = 1m
	}
	for _, tc := range tests {
		got := Age(now, tc.then)
		if got != tc.want {
			t.Errorf("Age(%d, %d) = %q, want %q", now, tc.then, got, tc.want)
		}
	}
}

func TestDefaultLocation(t *testing.T) {
	if DefaultLocation == nil {
		t.Fatal("DefaultLocation must not be nil")
	}
	// Verify offset is UTC-3 (no DST in 2026)
	_, offset := time.Date(2026, 7, 1, 0, 0, 0, 0, DefaultLocation).Zone()
	if offset != -3*3600 {
		t.Errorf("DefaultLocation offset = %d, want %d (UTC-3)", offset, -3*3600)
	}
}

func intPtr(v int) *int { return &v }
