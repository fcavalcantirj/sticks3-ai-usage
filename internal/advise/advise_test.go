package advise

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"usaged/internal/snapshot"
)

var testNow = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

func iptr(n int) *int       { return &n }
func i64ptr(n int64) *int64 { return &n }

// row5h builds a 5h quota row with the given pct and a reset at now+duration.
func row5h(pct int, dur time.Duration) snapshot.Row {
	r := nowDur(testNow, dur)
	return snapshot.Row{K: "5h", Label: "5h", Pct: iptr(pct), ResetAt: i64ptr(r.Unix())}
}
func row7d(pct int, dur time.Duration) snapshot.Row {
	r := nowDur(testNow, dur)
	return snapshot.Row{K: "7d", Label: "7d", Pct: iptr(pct), ResetAt: i64ptr(r.Unix())}
}
func row30d(pct int, dur time.Duration) snapshot.Row {
	r := nowDur(testNow, dur)
	return snapshot.Row{K: "30d", Label: "30d", Pct: iptr(pct), ResetAt: i64ptr(r.Unix())}
}

func nowDur(now time.Time, dur time.Duration) time.Time { return now.Add(dur) }

func planProv(id, label string, rows ...snapshot.Row) snapshot.Provider {
	return snapshot.Provider{
		ID: id, Label: label, Kind: "plan", Status: "ok", Rows: rows,
	}
}

// stalePlanProv is a plan provider with status "stale".
func stalePlanProv(id, label string, rows ...snapshot.Row) snapshot.Provider {
	return snapshot.Provider{
		ID: id, Label: label, Kind: "plan", Status: "stale", Rows: rows,
	}
}

// nonPlanProv builds a credit/free/empty-kind provider that must be excluded.
func nonPlanProv(id, label, kind string, rows ...snapshot.Row) snapshot.Provider {
	if len(rows) == 0 {
		rows = []snapshot.Row{{K: "bal", Label: "BAL", Pct: nil, Txt: "$0.50", Tier: "ok", ResetAt: nil}}
	}
	return snapshot.Provider{
		ID: id, Label: label, Kind: kind, Status: "ok", Rows: rows,
	}
}

func approx(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s: got %.6f, want %.6f (tol %.4f)", name, got, want, tol)
	}
}

// --- Test 1: Reset lands INSIDE the horizon → full budget (headroom 100) ---

func TestRankResetInsideHorizon(t *testing.T) {
	// 5h resets in 12 minutes (< 4h horizon) → headroom 100, pace neutralised to 1.0.
	// 7d resets in 126h (> 4h) → headroom = 100-30 = 70, pace computed.
	p := planProv("claude", "Claude",
		row5h(25, 12*time.Minute), // inside horizon
		row7d(30, 126*time.Hour),  // outside horizon
	)
	out := Rank([]snapshot.Provider{p}, testNow, DefaultHorizon)

	if len(out.Recommendations) != 1 {
		t.Fatalf("recommendations: got %d, want 1", len(out.Recommendations))
	}
	rec := out.Recommendations[0]
	approx(t, "headroom", float64(rec.EffectiveHeadroomPct), 70, 0.01)
	approx(t, "pace", rec.PaceRatio, 1.2, 0.01) // 0.30 / (42/168) = 0.30/0.25 = 1.2
	approx(t, "score", rec.Score, 58.33, 0.01)  // 70 / 1.2 = 58.333...
	if out.Winner == nil || *out.Winner != "claude" {
		t.Errorf("winner: got %v, want claude", out.Winner)
	}
	if strings.Contains(rec.Reason, "(stale)") {
		t.Errorf("reason should not be stale: %q", rec.Reason)
	}
}

// --- Test 2: Reset lands just OUTSIDE the horizon → current remaining ---

func TestRankResetOutsideHorizon(t *testing.T) {
	// 5h pct=25, resets in 4h30m (> 4h horizon) → headroom = 100-25 = 75.
	// pace = 0.25 / (0.5h/5h) = 0.25/0.1 = 2.5
	// score = 75 / max(1.0, 2.5) = 75/2.5 = 30.0
	p := planProv("codex", "ChatGPT",
		row5h(25, 4*time.Hour+30*time.Minute),
	)
	out := Rank([]snapshot.Provider{p}, testNow, DefaultHorizon)

	if len(out.Recommendations) != 1 {
		t.Fatalf("recommendations: got %d, want 1", len(out.Recommendations))
	}
	rec := out.Recommendations[0]
	if rec.EffectiveHeadroomPct != 75 {
		t.Errorf("headroom: got %d, want 75", rec.EffectiveHeadroomPct)
	}
	approx(t, "pace", rec.PaceRatio, 2.5, 0.01)
	approx(t, "score", rec.Score, 30.0, 0.01)
	if out.Winner == nil || *out.Winner != "codex" {
		t.Errorf("winner: got %v, want codex", out.Winner)
	}
}

// --- Test 3: Long window caps short (spec's live example) ---

func TestRankLongWindowCapsShort(t *testing.T) {
	// Claude's 5h resets in 12 min (inside horizon → headroom 100, pace 1.0).
	// 7d at 58% resets in 126h (outside → headroom 42, pace 2.32).
	// Binding = 7d (min headroom). Score = 42 / 2.32 = 18.10.
	// Also includes codex (0% both windows → score 100) and opencode:go to
	// verify the full spec ordering: codex > claude > opencode:go.
	codex := planProv("codex", "ChatGPT",
		row5h(0, 5*time.Hour),   // pct=0, reset in 5h (just outside horizon)
		row7d(0, 168*time.Hour), // pct=0, reset in 168h
	)
	claude := planProv("claude", "Claude",
		row5h(25, 12*time.Minute), // inside horizon
		row7d(58, 126*time.Hour),  // outside horizon
	)
	opencode := planProv("opencode:go", "OpenCode Go",
		row5h(18, 3*time.Hour+54*time.Minute), // 3.9h, inside horizon
		row7d(73, 118*time.Hour),              // outside horizon
		row30d(5, 720*time.Hour),              // 30d, outside horizon, low pct
	)

	out := Rank([]snapshot.Provider{codex, claude, opencode}, testNow, DefaultHorizon)

	if len(out.Recommendations) != 3 {
		t.Fatalf("recommendations: got %d, want 3", len(out.Recommendations))
	}

	// Ordering: codex (100) > claude (18.10) > opencode:go (11.01).
	if out.Recommendations[0].ID != "codex" {
		t.Errorf("rank[0]: got %s, want codex", out.Recommendations[0].ID)
	}
	if out.Recommendations[1].ID != "claude" {
		t.Errorf("rank[1]: got %s, want claude", out.Recommendations[1].ID)
	}
	if out.Recommendations[2].ID != "opencode:go" {
		t.Errorf("rank[2]: got %s, want opencode:go", out.Recommendations[2].ID)
	}

	// Claude's specific values (the binding window is 7d).
	claudeRec := out.Recommendations[1]
	if claudeRec.EffectiveHeadroomPct != 42 {
		t.Errorf("claude headroom: got %d, want 42", claudeRec.EffectiveHeadroomPct)
	}
	approx(t, "claude pace", claudeRec.PaceRatio, 2.32, 0.01) // 0.58/0.25 = 2.32
	approx(t, "claude score", claudeRec.Score, 18.10, 0.01)   // 42/2.32 = 18.103 → 18.10

	if out.Winner == nil || *out.Winner != "codex" {
		t.Errorf("winner: got %v, want codex", out.Winner)
	}
}

// --- Test 4: All plans exhausted → winner null, no credit text ---

func TestRankAllPlansExhausted(t *testing.T) {
	// Every plan window at 100%, resets outside the horizon → headroom 0.
	codex := planProv("codex", "ChatGPT",
		row5h(100, 4*time.Hour+30*time.Minute), // headroom 0, pace 10.0
		row7d(100, 126*time.Hour),              // headroom 0, pace 4.0
	)
	claude := planProv("claude", "Claude",
		row5h(100, 5*time.Hour),  // headroom 0
		row7d(100, 60*time.Hour), // headroom 0
	)
	// Credit provider must be entirely excluded.
	openrouter := nonPlanProv("openrouter:main", "OpenRouter main", "credit",
		snapshot.Row{K: "bal", Label: "OR bal", Pct: nil, Txt: "$0.50", Tier: "ok", ResetAt: nil},
	)

	out := Rank([]snapshot.Provider{codex, claude, openrouter}, testNow, DefaultHorizon)

	if out.Winner != nil {
		t.Errorf("winner: got %q, want nil (all plans exhausted)", *out.Winner)
	}
	if len(out.Recommendations) != 2 {
		t.Fatalf("recommendations: got %d, want 2 (codex + claude only)", len(out.Recommendations))
	}
	for _, rec := range out.Recommendations {
		if rec.EffectiveHeadroomPct != 0 {
			t.Errorf("%s headroom: got %d, want 0", rec.ID, rec.EffectiveHeadroomPct)
		}
	}

	// Serialised body must contain NO dollar sign and NO credit provider id.
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if strings.Contains(s, "$") {
		t.Errorf("response body contains a dollar sign: %s", s)
	}
	if strings.Contains(s, "openrouter:main") || strings.Contains(s, "openrouter:fallback") {
		t.Errorf("response body contains a credit provider id: %s", s)
	}
}

// --- Test 5: kind=="plan" positive filter + empty-kind rejection ---

func TestRankKindFilter(t *testing.T) {
	plan := planProv("codex", "ChatGPT", row5h(0, 5*time.Hour))
	credit := nonPlanProv("openrouter:main", "OR main", "credit")
	free := nonPlanProv("openrouter:fallback", "OR fallback", "free")
	emptyKind := nonPlanProv("groq", "Groq", "", row5h(50, 5*time.Hour))

	out := Rank([]snapshot.Provider{plan, credit, free, emptyKind}, testNow, DefaultHorizon)

	if len(out.Recommendations) != 1 {
		t.Fatalf("recommendations: got %d, want 1 (only plan providers)", len(out.Recommendations))
	}
	if out.Recommendations[0].ID != "codex" {
		t.Errorf("recommendation id: got %q, want codex", out.Recommendations[0].ID)
	}
}

// --- Test 6: Exact-key filtering — only "5h", "7d", "30d" count ---

func TestRankExactKeyFilter(t *testing.T) {
	// "7d:Fable" must NOT match "7d". "bal" and "rst" have nil Pct/ResetAt.
	claude := planProv("claude", "Claude",
		row5h(20, 4*time.Hour+30*time.Minute), // headroom 80, pace 2.0
		row7d(30, 126*time.Hour),              // headroom 70, pace 1.2
		snapshot.Row{
			K: "7d:Fable", Label: "FAB 7d", Pct: iptr(20), Txt: "Mon", Tier: "ok",
			ResetAt: i64ptr(testNow.Add(126 * time.Hour).Unix()),
		},
		snapshot.Row{K: "bal", Label: "BAL", Pct: nil, Txt: "$1.00", Tier: "ok", ResetAt: nil},
		snapshot.Row{K: "rst", Label: "RST", Pct: nil, Txt: "1 reset", Tier: "ok", ResetAt: nil},
	)

	out := Rank([]snapshot.Provider{claude}, testNow, DefaultHorizon)

	if len(out.Recommendations) != 1 {
		t.Fatal("expected 1 recommendation")
	}
	rec := out.Recommendations[0]
	// Binding = 7d (headroom 70), not "7d:Fable" (which would give headroom 80 at 20%).
	if rec.EffectiveHeadroomPct != 70 {
		t.Errorf("headroom: got %d, want 70 (7d binding, not 7d:Fable)", rec.EffectiveHeadroomPct)
	}
	// Verify no $ appears in the JSON output (bal row was excluded).
	body, _ := json.Marshal(out)
	if strings.Contains(string(body), "$") {
		t.Errorf("response contains $ from excluded bal row: %s", body)
	}
}

// --- Test 7: Divergent A/B scoritest (headroom-vs-pace tradeoff) ---
//
// A: headroom 60, pace 3.0 → Score = 60/3.0 = 20
// B: headroom 40, pace 0.85 → Score = 40/max(1.0, 0.85) = 40/1.0 = 40
//
// Lower headroom + under-pace beats higher headroom + over-pace. This pins
// the headroom-vs-pace tradeoff, which every candidate formula must pass
// differently.

func TestRankDivergentAB(t *testing.T) {
	// Provider A: 7d pct=40, resets in 145.6h.
	//   headroom = 100-40 = 60 (outside horizon)
	//   pace = 0.40 / (22.4h/168h) = 0.40/0.13333 = 3.0
	//   Score = 60 / max(1.0, 3.0) = 60/3.0 = 20.0
	a := planProv("alpha", "Alpha",
		row7d(40, 145*time.Hour+36*time.Minute), // 145.6h
	)

	// Provider B: 7d pct=60, resets in 50h.
	//   headroom = 100-60 = 40 (outside horizon)
	//   pace = 0.60 / (118h/168h) = 0.60/0.7024 = 0.8542
	//   Score = 40 / max(1.0, 0.8542) = 40/1.0 = 40.0
	b := planProv("beta", "Beta",
		row7d(60, 50*time.Hour),
	)

	out := Rank([]snapshot.Provider{a, b}, testNow, DefaultHorizon)

	if out.Winner == nil || *out.Winner != "beta" {
		t.Fatalf("winner: got %v, want beta (lower headroom + under-pace beats over-pace)", out.Winner)
	}
	if len(out.Recommendations) != 2 {
		t.Fatalf("recommendations: got %d, want 2", len(out.Recommendations))
	}

	// Verify ranking: beta first (40), alpha second (20).
	if out.Recommendations[0].ID != "beta" {
		t.Errorf("rank[0]: got %s, want beta", out.Recommendations[0].ID)
	}
	if out.Recommendations[1].ID != "alpha" {
		t.Errorf("rank[1]: got %s, want alpha", out.Recommendations[1].ID)
	}

	betaRec := out.Recommendations[0]
	alphaRec := out.Recommendations[1]

	// A: headroom 60, pace 3.0, score 20.
	if alphaRec.EffectiveHeadroomPct != 60 {
		t.Errorf("alpha headroom: got %d, want 60", alphaRec.EffectiveHeadroomPct)
	}
	approx(t, "alpha pace", alphaRec.PaceRatio, 3.0, 0.001)
	approx(t, "alpha score", alphaRec.Score, 20.0, 0.01)

	// B: headroom 40, pace < 1.0, score 40.
	if betaRec.EffectiveHeadroomPct != 40 {
		t.Errorf("beta headroom: got %d, want 40", betaRec.EffectiveHeadroomPct)
	}
	if betaRec.PaceRatio >= scorePaceThreshold {
		t.Errorf("beta pace: got %.4f, want < 1.0 (under-pace costs nothing)", betaRec.PaceRatio)
	}
	approx(t, "beta score", betaRec.Score, 40.0, 0.01)
}

// --- Test 8: Score is rounded to two decimal places ---

func TestRankScoreTwoDecimals(t *testing.T) {
	claude := planProv("claude", "Claude",
		row5h(25, 12*time.Minute),
		row7d(58, 126*time.Hour),
	)
	out := Rank([]snapshot.Provider{claude}, testNow, DefaultHorizon)

	for _, rec := range out.Recommendations {
		// Score must not have more than 2 decimal places.
		if math.Abs(rec.Score-math.Round(rec.Score*100)/100) > 1e-9 {
			t.Errorf("score %v has > 2 decimal places", rec.Score)
		}
		// Reason must show pace with exactly 2 decimal places.
		// e.g. "pace 2.32x" — the %x format specifier produces "2.32".
		if !strings.Contains(rec.Reason, "pace ") {
			t.Errorf("reason missing pace: %q", rec.Reason)
		}
		// Extract the pace value from the reason and check 2-decimal format.
		idx := strings.Index(rec.Reason, "pace ")
		if idx < 0 {
			t.Fatal("reason missing 'pace '")
		}
		rest := rec.Reason[idx+5:]
		xIdx := strings.Index(rest, "x")
		if xIdx < 0 {
			t.Fatal("reason missing 'x' after pace")
		}
		paceStr := rest[:xIdx]
		// Must contain exactly one decimal point with 2 digits after it.
		if !strings.Contains(paceStr, ".") {
			t.Errorf("pace in reason not 2-decimal format: %q", paceStr)
		}
		parts := strings.Split(paceStr, ".")
		if len(parts[1]) != 2 {
			t.Errorf("pace in reason has %d decimal places, want 2: %q", len(parts[1]), paceStr)
		}
	}
}

// --- Test 9: Single plan provider wins by default ---

func TestRankSingleProvider(t *testing.T) {
	codex := planProv("codex", "ChatGPT",
		row5h(10, 3*time.Hour),   // inside horizon → headroom 100
		row7d(20, 100*time.Hour), // outside horizon → headroom 80
	)
	out := Rank([]snapshot.Provider{codex}, testNow, DefaultHorizon)

	if out.Winner == nil || *out.Winner != "codex" {
		t.Errorf("winner: got %v, want codex", out.Winner)
	}
	rec := out.Recommendations[0]
	if rec.EffectiveHeadroomPct != 80 {
		t.Errorf("headroom: got %d, want 80 (binding 7d)", rec.EffectiveHeadroomPct)
	}
	// 7d pace: 0.20 / ((168-100)/168) = 0.20/(68/168) = 0.20/0.4048 = 0.494
	approx(t, "pace", rec.PaceRatio, 0.494, 0.01)
	approx(t, "score", rec.Score, 80.0, 0.01) // 80 / max(1.0, 0.494) = 80/1.0 = 80
	if !strings.Contains(rec.Reason, "codex") == false {
		// Reason should explain why it wins (headroom, pace, binding).
	}
	// Reason should mention the binding window.
	if !strings.Contains(rec.Reason, "binding 7d") {
		t.Errorf("reason should name binding window: %q", rec.Reason)
	}
}

// --- Test 10: Nil Pct / nil ResetAt rows are skipped ---

func TestRankNilPctResetAt(t *testing.T) {
	p := planProv("codex", "ChatGPT",
		// Usable 7d row.
		row7d(30, 100*time.Hour),
		// Nil Pct — must be skipped.
		snapshot.Row{K: "5h", Label: "5h", Pct: nil, Txt: "ok", Tier: "ok", ResetAt: nil},
		// Nil ResetAt — must be skipped.
		snapshot.Row{K: "5h", Label: "5h2", Pct: iptr(20), Txt: "ok", Tier: "ok", ResetAt: nil},
	)

	out := Rank([]snapshot.Provider{p}, testNow, DefaultHorizon)

	if len(out.Recommendations) != 1 {
		t.Fatalf("recommendations: got %d, want 1", len(out.Recommendations))
	}
	rec := out.Recommendations[0]
	// Only the 7d row was usable: pct=30, reset in 100h → headroom 70.
	if rec.EffectiveHeadroomPct != 70 {
		t.Errorf("headroom: got %d, want 70 (only 7d row counted)", rec.EffectiveHeadroomPct)
	}
}

// --- Test 11: Stale provider is ranked but flagged in the reason ---

func TestRankStaleProviderFlagged(t *testing.T) {
	// A stale provider whose windows have reset in the past must NOT get full
	// budget credit. It gets headroom = 100-pct, pace 1.0, and "(stale)" in
	// the reason. It must NOT beat a healthy provider.
	stale := stalePlanProv("claude", "Claude",
		row5h(30, -1*time.Hour), // reset 1h ago → past, no credit
		row7d(40, -2*time.Hour), // reset 2h ago → past, no credit
	)
	// 5h: headroom = 100-30 = 70, pace 1.0
	// 7d: headroom = 100-40 = 60, pace 1.0
	// Binding = 7d (60), pace = 1.0, score = 60/1.0 = 60.
	healthy := planProv("codex", "ChatGPT",
		row5h(10, 3*time.Hour),   // inside horizon → headroom 100, pace 1.0
		row7d(20, 100*time.Hour), // outside → headroom 80, pace ~0.494
	)
	// healthy: binding = 7d (80), pace = 0.494, score = 80/1.0 = 80.

	out := Rank([]snapshot.Provider{stale, healthy}, testNow, DefaultHorizon)

	if out.Winner == nil || *out.Winner != "codex" {
		t.Errorf("winner: got %v, want codex (stale must not win)", out.Winner)
	}

	// Find the stale recommendation and verify it's flagged.
	var staleRec Recommendation
	for _, rec := range out.Recommendations {
		if rec.ID == "claude" {
			staleRec = rec
		}
	}
	if !strings.Contains(staleRec.Reason, "(stale)") {
		t.Errorf("stale provider reason missing (stale): %q", staleRec.Reason)
	}
}

// --- Test 12: Determinism — same input produces identical output ---

func TestRankDeterministic(t *testing.T) {
	claude := planProv("claude", "Claude",
		row5h(25, 12*time.Minute),
		row7d(58, 126*time.Hour),
	)
	codex := planProv("codex", "ChatGPT",
		row5h(0, 5*time.Hour),
		row7d(0, 168*time.Hour),
	)

	out1 := Rank([]snapshot.Provider{codex, claude}, testNow, DefaultHorizon)
	out2 := Rank([]snapshot.Provider{codex, claude}, testNow, DefaultHorizon)

	b1, _ := json.Marshal(out1)
	b2, _ := json.Marshal(out2)
	if string(b1) != string(b2) {
		t.Errorf("non-deterministic output:\n%s\n%s", b1, b2)
	}
}

// --- Test 13: Tie-break binds the LONGER window (message 30 ruling) ---

func TestRankTieBreakLongerWindow(t *testing.T) {
	// Two windows with identical headroom (pct=50 → headroom 50) but different
	// paces. The longer window (7d) MUST bind, not the shorter (5h), because on
	// an exact headroom tie the longer window constrains a 4h decision.
	//
	// 5h: pct=50, resets in 14500s (just outside 14400s horizon).
	//   pace = 0.50 / (3500/18000) = 0.50/0.1944 = 2.5714
	// 7d: pct=50, resets in 100000s (well outside horizon).
	//   pace = 0.50 / (504800/604800) = 0.50/0.8346 = 0.5991
	//
	// With the WRONG tie-break (5h binds first): score = 50/max(1,2.571) = 19.45
	// With the CORRECT tie-break (7d binds):     score = 50/max(1,0.599) = 50.00
	p := planProv("alpha", "Alpha",
		row5h(50, 14500*time.Second),
		row7d(50, 100000*time.Second),
	)
	out := Rank([]snapshot.Provider{p}, testNow, DefaultHorizon)

	if len(out.Recommendations) != 1 {
		t.Fatalf("recommendations: got %d, want 1", len(out.Recommendations))
	}
	rec := out.Recommendations[0]

	if rec.EffectiveHeadroomPct != 50 {
		t.Errorf("headroom: got %d, want 50", rec.EffectiveHeadroomPct)
	}
	// The 7d window must be the binding one (longer window on tie).
	if !strings.Contains(rec.Reason, "binding 7d") {
		t.Errorf("reason should bind 7d (longer), got: %q", rec.Reason)
	}
	// Binding pace must be 7d's ~0.599, NOT 5h's ~2.571.
	approx(t, "pace", rec.PaceRatio, 0.599, 0.01)
	// Score = 50 / max(1.0, 0.599) = 50.00 (under-pace costs nothing).
	approx(t, "score", rec.Score, 50.0, 0.01)
	// Explicitly verify it does NOT match the wrong tie-break.
	if math.Abs(rec.PaceRatio-2.5714) < 0.1 {
		t.Errorf("pace %v matches WRONG tie-break (5h should not bind)", rec.PaceRatio)
	}
}

// --- Test 14: Confidence blend boundary cases (task 101) ---
//
// The blend shrinks pace toward neutral (1.0) when the binding window is too
// young to be reliable. confidence = min(1.0, elapsedFraction / 0.10).
// paceEffective = 1.0 + (paceRaw - 1.0) * confidence.
//
// We hold pct=50 (usedFraction = 0.50) fixed and use a single 7d window
// (604800 s) so the elapsed fraction spans 0→0.05→0.10→0.50 without crossing
// the 4 h horizon or hitting the 99.9 cap.

func TestPaceConfidenceBlendBoundaries(t *testing.T) {
	// elapsed 0% — floor (0.01). paceRaw = 50.0, confidence = 0.1.
	// paceEffective = 1.0 + 49.0*0.1 = 5.9.
	p0 := planProv("p0", "P0", row7d(50, 168*time.Hour))
	out0 := Rank([]snapshot.Provider{p0}, testNow, DefaultHorizon)
	r0 := out0.Recommendations[0]
	approx(t, "0%: pace", r0.PaceRatio, 5.9, 0.01)
	approx(t, "0%: score", r0.Score, 50.0/5.9, 0.01)

	// elapsed 5% — confidence = 0.5 (half-weighted).
	// paceRaw = 10.0, paceEffective = 1.0 + 9.0*0.5 = 5.5.
	// 5% of 7d = 8.4 h → reset in 168 h − 8.4 h = 159.6 h (outside horizon).
	p5 := planProv("p5", "P5", row7d(50, 159*time.Hour+36*time.Minute))
	out5 := Rank([]snapshot.Provider{p5}, testNow, DefaultHorizon)
	r5 := out5.Recommendations[0]
	approx(t, "5%: pace", r5.PaceRatio, 5.5, 0.01)
	approx(t, "5%: score", r5.Score, 50.0/5.5, 0.01)

	// elapsed 10% — confidence = 1.0 (full pace, blend is identity).
	// paceRaw = 5.0, paceEffective = 5.0.
	// 10% of 7d = 16.8 h → reset in 151.2 h.
	p10 := planProv("p10", "P10", row7d(50, 151*time.Hour+12*time.Minute))
	out10 := Rank([]snapshot.Provider{p10}, testNow, DefaultHorizon)
	r10 := out10.Recommendations[0]
	approx(t, "10%: pace", r10.PaceRatio, 5.0, 0.01)
	approx(t, "10%: score", r10.Score, 50.0/5.0, 0.01)

	// elapsed 50% — confidence = 1.0 (unchanged from pre-blend behaviour).
	// paceRaw = 1.0, paceEffective = 1.0 → max(1.0, 1.0) = 1.0 → score = 50.
	// 50% of 7d = 84 h → reset in 84 h (still outside the 4 h horizon).
	p50 := planProv("p50", "P50", row7d(50, 84*time.Hour))
	out50 := Rank([]snapshot.Provider{p50}, testNow, DefaultHorizon)
	r50 := out50.Recommendations[0]
	approx(t, "50%: pace", r50.PaceRatio, 1.0, 0.01)
	approx(t, "50%: score", r50.Score, 50.0, 0.01)
}

// --- Test 15: Frozen-fixture regression — codex at 0% used stays score 100 ---
//
// From the 2026-09-08 live table: codex both windows 0% used. paceRaw = 0
// regardless of elapsed, so paceEffective = 1.0 + (0−1.0)*confidence ≤ 1.0,
// max(1.0, …) = 1.0, and the score is 100. The blend must not disturb this.
func TestPaceConfidenceZeroUsedUnchanged(t *testing.T) {
	codex := planProv("codex", "ChatGPT",
		row5h(0, 5*time.Hour),   // elapsed floor 0.01, paceRaw 0
		row7d(0, 168*time.Hour), // elapsed floor 0.01, paceRaw 0
	)
	out := Rank([]snapshot.Provider{codex}, testNow, DefaultHorizon)
	rec := out.Recommendations[0]
	approx(t, "headroom", float64(rec.EffectiveHeadroomPct), 100, 0.01)
	// raw pace 0 → blended 0.9 → max(1.0, 0.9) = 1.0 → score 100.
	approx(t, "pace", rec.PaceRatio, 0.9, 0.01)
	approx(t, "score", rec.Score, 100.0, 0.01)
}

// --- Test 16: Live-fix scenario — codex 7d just reset, winner flips (task 101) ---
//
// Reproduces the 2026-09-09 capture at now=1788978007 where codex's 7d window
// had just reset (pct=16, ~1.6% elapsed → raw pace ≈ 9.92) and claude's 7d at
// 34.7% elapsed (raw pace ≈ 1.93) was ranked higher. Pre-blend codex scored
// 8.47 (loser to claude's 17.10); the confidence blend shrinks codex's pace to
// ≈ 2.44 and flips the winner to codex.

func TestPaceConfidenceLiveFix(t *testing.T) {
	now := time.Unix(1788978007, 0).UTC()
	horizon := DefaultHorizon

	// 7 d = 604 800 s. 1.6% elapsed → timeElapsed ≈ 9 751 s → untilReset ≈ 595 049 s.
	codexReset7d := time.Duration(595049) * time.Second
	// 7 d, 1.6% elapsed: paceRaw = 0.16 / 0.01613 ≈ 9.92.
	// confidence = 0.01613 / 0.10 = 0.1613.
	// paceEffective = 1.0 + (9.92−1.0) × 0.1613 ≈ 2.44.
	// score = 84 / 2.44 ≈ 34.4.
	codex := planProv("codex", "ChatGPT",
		row5hAt(now, 100, 2*time.Hour+30*time.Minute), // 5h pct=100, resets inside horizon → full budget
		row7dAt(now, 16, codexReset7d),
	)

	// claude 5h pct=5 resets inside horizon → full budget.
	// claude 7d pct=67, ~34.7% elapsed: paceRaw ≈ 1.93, confidence 1.0 → unchanged.
	claude := planProv("claude", "Claude",
		row5hAt(now, 5, 30*time.Minute),                // inside horizon
		row7dAt(now, 67, 109*time.Hour+42*time.Minute), // ~34.7% elapsed
	)

	// opencode:go 7d pct=99, ~39.5% elapsed: paceRaw ≈ 2.51, confidence 1.0.
	opencode := planProv("opencode:go", "OpenCode Go",
		row7dAt(now, 99, 101*time.Hour+37*time.Minute),
	)

	providers := []snapshot.Provider{claude, codex, opencode}
	out := Rank(providers, now, horizon)

	if out.Winner == nil || *out.Winner != "codex" {
		t.Fatalf("winner: got %v, want codex (pace blend should flip the winner)", out.Winner)
	}
	if len(out.Recommendations) != 3 {
		t.Fatalf("recommendations: got %d, want 3", len(out.Recommendations))
	}

	// Ranking: codex > claude > opencode:go.
	if out.Recommendations[0].ID != "codex" {
		t.Errorf("rank[0]: got %s, want codex", out.Recommendations[0].ID)
	}
	if out.Recommendations[1].ID != "claude" {
		t.Errorf("rank[1]: got %s, want claude", out.Recommendations[1].ID)
	}
	if out.Recommendations[2].ID != "opencode:go" {
		t.Errorf("rank[2]: got %s, want opencode:go", out.Recommendations[2].ID)
	}

	codexRec := out.Recommendations[0]
	claudeRec := out.Recommendations[1]
	ocRec := out.Recommendations[2]

	// codex: headroom 84, effective pace ≈ 2.44 (NOT 9.92), score ≈ 34.4.
	if codexRec.EffectiveHeadroomPct != 84 {
		t.Errorf("codex headroom: got %d, want 84", codexRec.EffectiveHeadroomPct)
	}
	approx(t, "codex pace", codexRec.PaceRatio, 2.44, 0.05)
	approx(t, "codex score", codexRec.Score, 34.48, 0.5)

	// claude: headroom 33, pace ≈ 1.93 (elapsed > 10%, blend is identity), score ≈ 17.10.
	if claudeRec.EffectiveHeadroomPct != 33 {
		t.Errorf("claude headroom: got %d, want 33", claudeRec.EffectiveHeadroomPct)
	}
	approx(t, "claude pace", claudeRec.PaceRatio, 1.93, 0.02)
	approx(t, "claude score", claudeRec.Score, 17.10, 0.05)

	// opencode:go: headroom 1, pace ≈ 2.51 (elapsed > 10%, unchanged), score ≈ 0.40.
	if ocRec.EffectiveHeadroomPct != 1 {
		t.Errorf("opencode:go headroom: got %d, want 1", ocRec.EffectiveHeadroomPct)
	}
	approx(t, "opencode:go pace", ocRec.PaceRatio, 2.51, 0.05)
	approx(t, "opencode:go score", ocRec.Score, 0.40, 0.05)
}

// row5hAt / row7dAt build a quota row at a given reference time (instead of the
// package-level testNow) with a reset at now+dur. Needed for tests that use a
// different clock than testNow.
func row5hAt(now time.Time, pct int, dur time.Duration) snapshot.Row {
	r := now.Add(dur)
	return snapshot.Row{K: "5h", Label: "5h", Pct: iptr(pct), ResetAt: i64ptr(r.Unix())}
}
func row7dAt(now time.Time, pct int, dur time.Duration) snapshot.Row {
	r := now.Add(dur)
	return snapshot.Row{K: "7d", Label: "7d", Pct: iptr(pct), ResetAt: i64ptr(r.Unix())}
}
