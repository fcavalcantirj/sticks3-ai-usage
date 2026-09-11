package stats

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testTZ is the timezone used in fixtures (matches config.DefaultTZ).
var testTZ = mustLoadLocation("America/Sao_Paulo")

// fixedScanNow matches the fixture timestamps: "today" is 2026-09-03.
var fixedScanNow = time.Date(2026, 9, 3, 12, 0, 0, 0, testTZ)

func mustLoadLocation(tz string) *time.Location {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		panic(err)
	}
	return loc
}

// fixtureDir returns the path to internal/stats/testdata/transcripts.
func fixtureDir(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata", "transcripts")
}

// --- Tests ---

func TestScanClaudeCodeDedupe(t *testing.T) {
	dir := fixtureDir(t)
	s := NewScanner(testTZ)
	s.Clock = func() time.Time { return fixedScanNow }

	cfg := ScanConfig{
		TZ:        testTZ,
		ClaudeDir: filepath.Join(dir, "claude"),
	}

	report, _, err := s.Scan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	src, ok := report.Sources["claude_code"]
	if !ok {
		t.Fatal("missing claude_code source")
	}

	// Fixtures: msg-001 (1000 in + 2000 out), msg-002 (500 in + 300 out + 200 cache_w) deduped,
	// msg-003 on 2026-09-02 (800 in + 1200 out + 400 cache_r) — not today.
	// Today total (msg-001 + msg-002 once): input=1500, output=2300, cache_write=200
	wantTodayInput := int64(1500)
	wantTodayOutput := int64(2300)
	wantTodayCacheWrite := int64(200)

	if src.Today.Tokens.Input != wantTodayInput {
		t.Errorf("Today Input = %d, want %d (dedupe msg-002)", src.Today.Tokens.Input, wantTodayInput)
	}
	if src.Today.Tokens.Output != wantTodayOutput {
		t.Errorf("Today Output = %d, want %d", src.Today.Tokens.Output, wantTodayOutput)
	}
	if src.Today.Tokens.CacheWrite != wantTodayCacheWrite {
		t.Errorf("Today CacheWrite = %d, want %d", src.Today.Tokens.CacheWrite, wantTodayCacheWrite)
	}
	if src.Today.Requests != 2 {
		t.Errorf("Today Requests = %d, want 2", src.Today.Requests)
	}
}

func TestScanClaudeCodeModels(t *testing.T) {
	dir := fixtureDir(t)
	s := NewScanner(testTZ)
	s.Clock = func() time.Time { return fixedScanNow }

	cfg := ScanConfig{TZ: testTZ, ClaudeDir: filepath.Join(dir, "claude")}
	report, _, err := s.Scan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	src := report.Sources["claude_code"]
	if len(src.Models) != 2 {
		t.Fatalf("len(Models) = %d, want 2", len(src.Models))
	}

	// Models sorted by month tokens desc: claude-opus (10200+400cr) vs claude-sonnet (1500+2300+200cw)
	// opus month = 800+1200+0+400 = 2400; sonnet month = 1000+2000+500+300+200+0 = 4000
	// All fixture data is in Sep 2026 (within the month), so month = lifetime.
	// sonnet (4000) > opus (2400), sonnet should be first.
	if src.Models[0].Model != "claude-sonnet-4-20250514" {
		t.Errorf("Models[0] = %q, want claude-sonnet-4-20250514 (sorted by month tokens desc)", src.Models[0].Model)
	}
	if src.Models[1].Model != "claude-opus-4-20250514" {
		t.Errorf("Models[1] = %q, want claude-opus-4-20250514", src.Models[1].Model)
	}
}

func TestScanCodexDeltas(t *testing.T) {
	dir := fixtureDir(t)
	s := NewScanner(testTZ)
	s.Clock = func() time.Time { return fixedScanNow }

	cfg := ScanConfig{TZ: testTZ, CodexDir: filepath.Join(dir, "codex")}
	report, _, err := s.Scan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	src, ok := report.Sources["codex"]
	if !ok {
		t.Fatal("missing codex source")
	}

	// Fixtures:
	// gpt-4o: two events on 2026-09-03: (600 in + 150 out) + (100 in + 50 out) = 700 in + 200 out
	// o1: one event on 2026-09-02: 2000 in + 500 cr + 3000 out + 1000 reasoning out
	// Today only: gpt-4o → input=700, output=200
	// Month: gpt-4o (700 in, 200 out) + o1 (2000 in, 500 cr, 4000 out) = 2700 in, 500 cr, 4200 out
	if src.Today.Tokens.Input != 700 {
		t.Errorf("Today Input = %d, want 700", src.Today.Tokens.Input)
	}
	if src.Today.Tokens.Output != 200 {
		t.Errorf("Today Output = %d, want 200", src.Today.Tokens.Output)
	}
	if src.Today.Requests != 2 {
		t.Errorf("Today Requests = %d, want 2", src.Today.Requests)
	}

	// Month should include o1 (2026-09-02 is in the same month).
	if src.Month.Tokens.Output != 4200 {
		t.Errorf("Month Output = %d, want 4200", src.Month.Tokens.Output)
	}
}

// TestScanCodexModelExtraction verifies that the Codex model id is correctly
// extracted from turn_context (payload.model) and thread_settings_applied
// (payload.thread_settings.model) events, and carried forward to token_count
// lines that only contain "unknown" as the model.
func TestScanCodexModelExtraction(t *testing.T) {
	codexDir := filepath.Join(fixtureDir(t), "test_models")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(codexDir) })

	lines := []string{
		// Session 1 uses gpt-4o (from thread_settings_applied + turn_context).
		`{"type":"event_msg","timestamp":"2026-09-03T10:00:00Z","payload":{"type":"thread_settings_applied","thread_settings":{"model":"gpt-4o"}}}`,
		`{"type":"event_msg","timestamp":"2026-09-03T10:01:00Z","payload":{"type":"turn_context","model":"gpt-4o"}}`,
		`{"type":"event_msg","timestamp":"2026-09-03T10:01:00Z","payload":{"type":"token_count","info":{"model":"unknown","last_token_usage":{"input_tokens":100,"cached_input_tokens":0,"output_tokens":50,"reasoning_output_tokens":0}}}}`,

		// Session 2 switches to o1 (from thread_settings_applied only).
		`{"type":"event_msg","timestamp":"2026-09-03T11:00:00Z","payload":{"type":"thread_settings_applied","thread_settings":{"model":"o1"}}}`,
		`{"type":"event_msg","timestamp":"2026-09-03T11:01:00Z","payload":{"type":"token_count","info":{"model":"unknown","last_token_usage":{"input_tokens":200,"cached_input_tokens":10,"output_tokens":80,"reasoning_output_tokens":0}}}}`,
	}
	dst := filepath.Join(codexDir, "two_models.jsonl")
	if err := os.WriteFile(dst, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewScanner(testTZ)
	s.Clock = func() time.Time { return fixedScanNow }
	cfg := ScanConfig{TZ: testTZ, CodexDir: codexDir}

	report, _, err := s.Scan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	src, ok := report.Sources["codex"]
	if !ok {
		t.Fatal("missing codex source")
	}

	// Both models should be tracked, resolved via aliases to priced entries.
	if len(src.Models) != 2 {
		t.Fatalf("len(Models) = %d, want 2", len(src.Models))
	}

	modelIDs := make(map[string]bool)
	for _, m := range src.Models {
		modelIDs[m.Model] = true
	}
	if !modelIDs["gpt-4o"] {
		t.Errorf("expected model gpt-4o in results, got %v", modelIDs)
	}
	if !modelIDs["o1"] {
		t.Errorf("expected model o1 in results, got %v", modelIDs)
	}

	// gpt-4o: 100 in + 50 out = 150 tokens
	// o1: 200 in + 10 cr + 80 out = 290 tokens
	// Total today: 440 tokens, 2 requests
	if src.Today.Tokens.Input != 300 {
		t.Errorf("Today Input = %d, want 300 (100+200)", src.Today.Tokens.Input)
	}
	if src.Today.Tokens.CacheRead != 10 {
		t.Errorf("Today CacheRead = %d, want 10", src.Today.Tokens.CacheRead)
	}
	if src.Today.Requests != 2 {
		t.Errorf("Today Requests = %d, want 2", src.Today.Requests)
	}
}

// TestScanCodexRealModels verifies model extraction with real Codex rollout
// model names (ORDER #42a / #43 / #44 BUG 48). Real Codex data emits
// turn_context as a TOP-LEVEL type (line.type == "turn_context", not nested
// inside event_msg). gpt-5.5, gpt-5.6-sol and fugu-ultra are priced via the
// openai/ canonical entries; bare fugu is explicitly unpriced.
func TestScanCodexRealModels(t *testing.T) {
	codexDir := filepath.Join(fixtureDir(t), "test_real_models")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(codexDir) })

	lines := []string{
		// gpt-5.6-sol session — turn_context is a top-level type in real Codex data.
		`{"type":"event_msg","timestamp":"2026-09-03T09:00:00Z","payload":{"type":"thread_settings_applied","thread_settings":{"model":"gpt-5.6-sol"}}}`,
		`{"type":"turn_context","timestamp":"2026-09-03T09:01:00Z","payload":{"model":"gpt-5.6-sol"}}`,
		`{"type":"event_msg","timestamp":"2026-09-03T09:01:00Z","payload":{"type":"token_count","info":{"model":"unknown","last_token_usage":{"input_tokens":500,"cached_input_tokens":0,"output_tokens":100,"reasoning_output_tokens":0}}}}`,

		// gpt-5.5 session
		`{"type":"event_msg","timestamp":"2026-09-03T10:00:00Z","payload":{"type":"thread_settings_applied","thread_settings":{"model":"gpt-5.5"}}}`,
		`{"type":"turn_context","timestamp":"2026-09-03T10:01:00Z","payload":{"model":"gpt-5.5"}}`,
		`{"type":"event_msg","timestamp":"2026-09-03T10:01:00Z","payload":{"type":"token_count","info":{"model":"unknown","last_token_usage":{"input_tokens":600,"cached_input_tokens":0,"output_tokens":150,"reasoning_output_tokens":0}}}}`,

		// fugu-ultra session (priced via openai/fugu-ultra)
		`{"type":"event_msg","timestamp":"2026-09-03T11:00:00Z","payload":{"type":"thread_settings_applied","thread_settings":{"model":"fugu-ultra"}}}`,
		`{"type":"turn_context","timestamp":"2026-09-03T11:01:00Z","payload":{"model":"fugu-ultra"}}`,
		`{"type":"event_msg","timestamp":"2026-09-03T11:01:00Z","payload":{"type":"token_count","info":{"model":"unknown","last_token_usage":{"input_tokens":222728819,"cached_input_tokens":0,"output_tokens":50000,"reasoning_output_tokens":0}}}}`,
	}
	dst := filepath.Join(codexDir, "real_models.jsonl")
	if err := os.WriteFile(dst, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewScanner(testTZ)
	s.Clock = func() time.Time { return fixedScanNow }
	cfg := ScanConfig{TZ: testTZ, CodexDir: codexDir}

	report, _, err := s.Scan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	src, ok := report.Sources["codex"]
	if !ok {
		t.Fatal("missing codex source")
	}

	// All three priced models should appear.
	modelIDs := make(map[string]bool)
	for _, m := range src.Models {
		modelIDs[m.Model] = true
	}
	if !modelIDs["gpt-5.6-sol"] {
		t.Errorf("expected model gpt-5.6-sol in results, got %v", modelIDs)
	}
	if !modelIDs["gpt-5.5"] {
		t.Errorf("expected model gpt-5.5 in results, got %v", modelIDs)
	}
	if !modelIDs["fugu-ultra"] {
		t.Errorf("expected model fugu-ultra in results, got %v", modelIDs)
	}

	// Verify non-zero costs (ORDER #42a: previously showed $0 for these).
	var solCost, gpt55Cost, fuguCost float64
	for _, m := range src.Models {
		switch m.Model {
		case "gpt-5.6-sol":
			solCost = m.Cost
		case "gpt-5.5":
			gpt55Cost = m.Cost
		case "fugu-ultra":
			fuguCost = m.Cost
		}
	}
	if solCost == 0 {
		t.Error("gpt-5.6-sol cost should be non-zero")
	}
	if gpt55Cost == 0 {
		t.Error("gpt-5.5 cost should be non-zero")
	}
	if fuguCost == 0 {
		t.Error("fugu-ultra cost should be non-zero (had 222,728,819 input tokens)")
	}

	// Codex is subscription-billed (ChatGPT Plus), not per-token.
	if src.Billed {
		t.Error("Codex Billed should be false (Plus subscription)")
	}
	if src.Plan != "Plus" {
		t.Errorf("Codex Plan = %q, want Plus", src.Plan)
	}
}

func TestScanDayBucketingTZ(t *testing.T) {
	// Use UTC instead of São Paulo to verify TZ affects day bucketing.
	dir := fixtureDir(t)
	s := NewScanner(time.UTC)
	s.Clock = func() time.Time {
		return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	}

	cfg := ScanConfig{TZ: time.UTC, ClaudeDir: filepath.Join(dir, "claude")}
	report, _, err := s.Scan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	src := report.Sources["claude_code"]

	// In UTC, the 2026-09-03 events are "today" (Sep 3), and the 2026-09-02
	// event is yesterday — not in today but in the month.
	// In São Paulo (UTC-3), 2026-09-03T10:00Z = 07:00 local (still Sep 3),
	// 2026-09-02T20:00Z = 17:00 Sep 2 local (not today).
	// With UTC, 2026-09-03T10:00Z and T11:00Z are today, T20:00Z on Sep 2 is yesterday.
	// Same result. Let's test with a TZ where Sep 3 T10:00 becomes Sep 4.
	s2 := NewScanner(mustLoadLocation("Asia/Tokyo")) // UTC+9
	s2.Clock = func() time.Time {
		return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	}

	cfg2 := ScanConfig{TZ: mustLoadLocation("Asia/Tokyo"), ClaudeDir: filepath.Join(dir, "claude")}
	report2, _, err := s2.Scan(context.Background(), cfg2, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	src2 := report2.Sources["claude_code"]

	// In Tokyo (UTC+9): 2026-09-03T10:00Z = Sep 3 19:00 Tokyo,
	// 2026-09-02T20:00Z = Sep 3 05:00 Tokyo → so msg-003 moves to "today" in Tokyo.
	// Today input = 1000 (msg-001) + 500 (msg-002) + 800 (msg-003) = 2300.
	if src2.Today.Tokens.Input != 2300 {
		t.Errorf("Tokyo Today Input = %d, want 2300 (msg-003 shifts to today at UTC+9)", src2.Today.Tokens.Input)
	}

	// In São Paulo, msg-003 is on Sep 2 → not today.
	if src.Today.Tokens.Input != 1500 {
		t.Errorf("SAO Today Input = %d, want 1500 (msg-003 not today)", src.Today.Tokens.Input)
	}
}

func TestScanIncrementalAppend(t *testing.T) {
	dir := fixtureDir(t)
	claudeDir := filepath.Join(dir, "claude")

	// First scan: empty index.
	s := NewScanner(testTZ)
	s.Clock = func() time.Time { return fixedScanNow }
	cfg := ScanConfig{TZ: testTZ, ClaudeDir: claudeDir}

	_, idx1, err := s.Scan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}

	// Should have indexed the sample file.
	if len(idx1) != 1 {
		t.Fatalf("after first scan: len(idx) = %d, want 1", len(idx1))
	}

	// Second scan: same files unchanged → no file entries updated, same totals.
	report2, idx2, err := s.Scan(context.Background(), cfg, idx1)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}

	src := report2.Sources["claude_code"]
	if src.Today.Tokens.Input != 1500 {
		t.Errorf("second scan Today Input = %d, want 1500 (unchanged file should still be counted)", src.Today.Tokens.Input)
	}

	// The index should still have 1 entry.
	if len(idx2) != 1 {
		t.Errorf("after second scan: len(idx) = %d, want 1", len(idx2))
	}
}

func TestScanIncrementalAppendOnly(t *testing.T) {
	dir := fixtureDir(t)
	claudeDir := filepath.Join(dir, "test_inc")

	// Create a temp copy of the claude fixture.
	src := filepath.Join(dir, "claude", "sample.jsonl")
	dst := filepath.Join(claudeDir, "inc.jsonl")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(claudeDir) })

	s := NewScanner(testTZ)
	s.Clock = func() time.Time { return fixedScanNow }
	cfg := ScanConfig{TZ: testTZ, ClaudeDir: claudeDir}

	// First scan.
	report1, idx1, err := s.Scan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	src1 := report1.Sources["claude_code"]
	total1 := src1.Today.Tokens.Total()

	// Append a new line.
	extra := `{"type":"assistant","timestamp":"2026-09-03T15:00:00Z","requestId":"req-999","message":{"id":"msg-999","model":"claude-sonnet-4-20250514","usage":{"input_tokens":100,"output_tokens":100,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}` + "\n"
	// Make a copy of data to avoid aliasing issues with append.
	appended := make([]byte, len(data)+len(extra))
	copy(appended, data)
	copy(appended[len(data):], extra)
	if err := os.WriteFile(dst, appended, 0o644); err != nil {
		t.Fatal(err)
	}

	// Second scan: should read only the appended line (incremental seek),
	// merging the cached FileResult with the delta — total = cached + 200.
	report2, idx2, err := s.Scan(context.Background(), cfg, idx1)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	src2 := report2.Sources["claude_code"]
	total2 := src2.Today.Tokens.Total()

	// The second scan returns the full total: cached (total1) + appended delta (200).
	if total2 != total1+200 {
		t.Errorf("incremental total: total2=%d, want %d (cached %d + delta 200)", total2, total1+200, total1)
	}

	// The appended delta alone is 200 tokens (100 in + 100 out).
	delta := total2 - total1
	if delta != 200 {
		t.Errorf("incremental delta: %d, want 200 (only appended line read)", delta)
	}

	// Index should reflect the new size.
	fi1 := idx1[dst]
	fi2 := idx2[dst]
	if fi2.Size <= fi1.Size {
		t.Errorf("index size not updated: old=%d new=%d", fi1.Size, fi2.Size)
	}

	// The cached FileResult should be preserved in the new index.
	if fi2.Result == nil {
		t.Error("cached FileResult should be non-nil after incremental scan")
	}
}

func TestScanCostZeroForUnknownModel(t *testing.T) {
	dir := fixtureDir(t)

	// Create a temp fixture with an unknown model.
	claudeDir := filepath.Join(dir, "test_unknown")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	unknownJSONL := `{"type":"assistant","timestamp":"2026-09-03T10:00:00Z","requestId":"req-x","message":{"id":"msg-x","model":"claude-unknown-model-xyz","usage":{"input_tokens":1000,"output_tokens":2000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}` + "\n"
	dst := filepath.Join(claudeDir, "unknown.jsonl")
	if err := os.WriteFile(dst, []byte(unknownJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(claudeDir) })

	s := NewScanner(testTZ)
	s.Clock = func() time.Time { return fixedScanNow }
	cfg := ScanConfig{TZ: testTZ, ClaudeDir: claudeDir}

	report, _, err := s.Scan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	src := report.Sources["claude_code"]
	if src.Today.Cost != 0 {
		t.Errorf("Today Cost = %f, want 0 (unknown model has no price)", src.Today.Cost)
	}
	// But tokens should still be counted.
	if src.Today.Tokens.Input != 1000 {
		t.Errorf("Today Input = %d, want 1000", src.Today.Tokens.Input)
	}
}

func TestScanCostKnownModel(t *testing.T) {
	dir := fixtureDir(t)
	s := NewScanner(testTZ)
	s.Clock = func() time.Time { return fixedScanNow }
	cfg := ScanConfig{TZ: testTZ, ClaudeDir: filepath.Join(dir, "claude")}

	report, _, err := s.Scan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	src, ok := report.Sources["claude_code"]
	if !ok {
		t.Fatalf("missing claude_code source in cost test")
	}
	// Today: msg-001 (1000 in + 2000 out, claude-sonnet-4-20250514) +
	//        msg-002 deduped (500 in + 300 out + 200 cw, claude-sonnet-4-20250514)
	// Price for sonnet (Anthropic 2026-09-04): input=3, output=15, cache_read=0.30, cache_write=3.75 (all USD per 1M tokens)
	// Cost = (1500*3 + 2300*15 + 0*0.30 + 200*3.75) / 1e6
	// = (4500 + 34500 + 0 + 750) / 1e6 = 39750 / 1e6 = 0.039750
	wantCost := (float64(1500)*3 + float64(2300)*15 + float64(200)*3.75) / 1e6
	if src.Today.Cost != wantCost {
		t.Errorf("Today Cost = %f, want %f", src.Today.Cost, wantCost)
	}
}

func TestScanDaysListOldestFirst(t *testing.T) {
	dir := fixtureDir(t)
	s := NewScanner(testTZ)
	s.Clock = func() time.Time { return fixedScanNow }
	cfg := ScanConfig{TZ: testTZ, ClaudeDir: filepath.Join(dir, "claude")}

	report, _, err := s.Scan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	src := report.Sources["claude_code"]
	// Should have days for Sep 2 and Sep 3 (in São Paulo TZ).
	if len(src.Days) < 2 {
		t.Fatalf("len(Days) = %d, want >= 2", len(src.Days))
	}
	// Oldest first.
	if src.Days[0].Date > src.Days[1].Date {
		t.Errorf("Days not sorted oldest-first: [0]=%s [1]=%s", src.Days[0].Date, src.Days[1].Date)
	}

	// Find Sep 2 day.
	for _, d := range src.Days {
		if d.Date == "2026-09-02" {
			if d.Tokens.Input != 800 {
				t.Errorf("Sep 2 Input = %d, want 800", d.Tokens.Input)
			}
			if d.Tokens.CacheRead != 400 {
				t.Errorf("Sep 2 CacheRead = %d, want 400", d.Tokens.CacheRead)
			}
		}
	}
}

func TestScanReportGeneratedAt(t *testing.T) {
	dir := fixtureDir(t)
	s := NewScanner(testTZ)
	s.Clock = func() time.Time { return fixedScanNow }
	cfg := ScanConfig{TZ: testTZ, ClaudeDir: filepath.Join(dir, "claude")}

	report, _, err := s.Scan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	expected := fixedScanNow.Unix()
	if report.GeneratedAt != expected {
		t.Errorf("GeneratedAt = %d, want %d", report.GeneratedAt, expected)
	}
}

func TestScanSkipOldFiles(t *testing.T) {
	// With SkipOld=true, files older than MaxAge are skipped.
	dir := fixtureDir(t)
	s := NewScanner(testTZ)
	s.Clock = func() time.Time { return fixedScanNow }
	s.SkipOld = true

	// The fixtures are from 2026-09-03, which is within MaxAge of fixedScanNow.
	cfg := ScanConfig{TZ: testTZ, ClaudeDir: filepath.Join(dir, "claude")}

	report, _, err := s.Scan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	src := report.Sources["claude_code"]
	if src.Today.Tokens.Input == 0 {
		t.Error("SkipOld should not skip files within MaxAge")
	}
}

func TestScanUnpricedModelsAndSynthetic(t *testing.T) {
	// Create a temp fixture with known-priced, unknown, and <synthetic> models.
	claudeDir := filepath.Join(fixtureDir(t), "test_pricing")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(claudeDir) })

	lines := []string{
		// Priced model (claude-sonnet-4-20250514)
		`{"type":"assistant","timestamp":"2026-09-03T10:00:00Z","requestId":"req-1","message":{"id":"msg-1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":100,"output_tokens":200,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
		// Unknown model
		`{"type":"assistant","timestamp":"2026-09-03T11:00:00Z","requestId":"req-2","message":{"id":"msg-2","model":"claude-unknown-model-xyz","usage":{"input_tokens":50,"output_tokens":100,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
		// <synthetic> — must be excluded from models and cost
		`{"type":"assistant","timestamp":"2026-09-03T12:00:00Z","requestId":"req-3","message":{"id":"msg-3","model":"<synthetic>","usage":{"input_tokens":1000,"output_tokens":2000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
	}
	dst := filepath.Join(claudeDir, "pricing.jsonl")
	if err := os.WriteFile(dst, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewScanner(testTZ)
	s.Clock = func() time.Time { return fixedScanNow }
	cfg := ScanConfig{TZ: testTZ, ClaudeDir: claudeDir}

	report, _, err := s.Scan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	src := report.Sources["claude_code"]

	// <synthetic> must be excluded from the model table.
	for _, m := range src.Models {
		if m.Model == "<synthetic>" {
			t.Error("<synthetic> should be excluded from models list")
		}
	}
	if len(src.Models) != 2 {
		t.Errorf("len(Models) = %d, want 2 (priced + unknown, no <synthetic>)", len(src.Models))
	}

	// The unknown model must be tracked in UnpricedModels.
	found := false
	for _, um := range src.UnpricedModels {
		if um == "claude-unknown-model-xyz" {
			found = true
		}
	}
	if !found {
		t.Errorf("unpriced models = %v, want to contain claude-unknown-model-xyz", src.UnpricedModels)
	}

	// Partial must be true.
	if !src.Partial {
		t.Error("Partial should be true when unpriced models exist")
	}

	// Cost should only include the priced model (100 in + 200 out at $3/$15/MTok).
	// = (100*3 + 200*15) / 1e6 = (300 + 3000) / 1e6 = 0.0033
	wantCost := (float64(100)*3 + float64(200)*15) / 1e6
	if src.Today.Cost != wantCost {
		t.Errorf("Today Cost = %f, want %f", src.Today.Cost, wantCost)
	}
}

func TestScanAliasModels(t *testing.T) {
	// Verify that bare aliases like "opus" and "sonnet" map to current family defaults.
	_, ok := LookupPrice("opus")
	if !ok {
		t.Error("alias 'opus' should map to a priced model")
	}
	_, ok = LookupPrice("sonnet")
	if !ok {
		t.Error("alias 'sonnet' should map to a priced model")
	}
}

// TestScanCostReconciliation is the ORDER #45 regression test: it pins the
// invariant that month.cost equals the sum of per-day costs AND the sum of
// per-model costs. This is the three-way agreement that silently broke when
// the carry-forward fix landed in one aggregation path but not the other.
func TestScanCostReconciliation(t *testing.T) {
	codexDir := filepath.Join(fixtureDir(t), "test_cost_recon")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(codexDir) })

	// Two days, two priced models via turn_context carry-forward.
	lines := []string{
		// Sep 2 — gpt-5.6-sol: 500 in + 100 out
		`{"type":"turn_context","timestamp":"2026-09-02T10:00:00Z","payload":{"model":"gpt-5.6-sol"}}`,
		`{"type":"event_msg","timestamp":"2026-09-02T10:01:00Z","payload":{"type":"token_count","info":{"model":"unknown","last_token_usage":{"input_tokens":500,"cached_input_tokens":0,"output_tokens":100,"reasoning_output_tokens":0}}}}`,
		// Sep 3 — gpt-5.5: 600 in + 150 out
		`{"type":"turn_context","timestamp":"2026-09-03T10:00:00Z","payload":{"model":"gpt-5.5"}}`,
		`{"type":"event_msg","timestamp":"2026-09-03T10:01:00Z","payload":{"type":"token_count","info":{"model":"unknown","last_token_usage":{"input_tokens":600,"cached_input_tokens":0,"output_tokens":150,"reasoning_output_tokens":0}}}}`,
	}
	dst := filepath.Join(codexDir, "recon.jsonl")
	if err := os.WriteFile(dst, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewScanner(testTZ)
	s.Clock = func() time.Time { return fixedScanNow }
	cfg := ScanConfig{TZ: testTZ, CodexDir: codexDir}
	report, _, err := s.Scan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	src := report.Sources["codex"]

	// Sum of per-day costs (src.Days[].Cost).
	dayCostSum := 0.0
	for _, d := range src.Days {
		dayCostSum += d.Cost
	}

	// Sum of per-model costs (src.Models[].CostMonth — month-windowed).
	modelCostSum := 0.0
	for _, m := range src.Models {
		modelCostSum += m.CostMonth
	}

	// The three must agree: month.cost == sum per-day == sum per-model.
	if src.Month.Cost == 0 {
		t.Fatal("Month.Cost is 0 — ORDER #45 BUG: day buckets not resolving model for cost")
	}
	if src.Today.Cost == 0 {
		t.Fatal("Today.Cost is 0 — ORDER #45 BUG")
	}
	if diff := src.Month.Cost - dayCostSum; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("Month.Cost (%.6f) != sum of per-day costs (%.6f) — gap=%e",
			src.Month.Cost, dayCostSum, diff)
	}
	if diff := src.Month.Cost - modelCostSum; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("Month.Cost (%.6f) != sum of per-model costs (%.6f) — gap=%e",
			src.Month.Cost, modelCostSum, diff)
	}

	// Today cost should come from only the Sep 3 day.
	for _, d := range src.Days {
		if d.Date == "2026-09-03" && d.Cost == 0 {
			t.Errorf("Sep 3 day Cost is 0 — model carry-forward not applied to day buckets")
		}
	}
}

// TestScanModelWindowedVsLifetime verifies that a model with usage on a day
// outside the month window (Aug 15) and a day inside the month but not today
// (Sep 2) produces different lifetime and month-windowed figures. The model
// claude-sonnet-4-20250514 has price input=3, output=15 USD/MTok.
func TestScanModelWindowedVsLifetime(t *testing.T) {
	// fixedScanNow = 2026-09-03 12:00 São Paulo.
	// monthBoundary = 2026-09-01 (start of month, São Paulo TZ).
	// Aug 15 is outside the month; Sep 2 is inside the month but not today.
	dir := t.TempDir()
	lines := []string{
		// Aug 15 — outside the month window.
		`{"type":"event_msg","timestamp":"2026-08-15T09:00:00Z","payload":{"type":"turn_context","model":"claude-sonnet-4-20250514"}}`,
		`{"type":"event_msg","timestamp":"2026-08-15T10:00:00Z","payload":{"type":"token_count","info":{"model":"unknown","last_token_usage":{"input_tokens":1000,"cached_input_tokens":0,"output_tokens":500,"reasoning_output_tokens":0}}}}`,
		// Sep 2 — inside the month window, not today.
		`{"type":"event_msg","timestamp":"2026-09-02T09:00:00Z","payload":{"type":"turn_context","model":"claude-sonnet-4-20250514"}}`,
		`{"type":"event_msg","timestamp":"2026-09-02T10:00:00Z","payload":{"type":"token_count","info":{"model":"unknown","last_token_usage":{"input_tokens":500,"cached_input_tokens":0,"output_tokens":200,"reasoning_output_tokens":0}}}}`,
	}
	dst := filepath.Join(dir, "windowed.jsonl")
	if err := os.WriteFile(dst, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewScanner(testTZ)
	s.Clock = func() time.Time { return fixedScanNow }
	cfg := ScanConfig{TZ: testTZ, CodexDir: dir}
	report, _, err := s.Scan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	src, ok := report.Sources["codex"]
	if !ok {
		t.Fatal("missing codex source in report")
	}
	if len(src.Models) != 1 {
		t.Fatalf("len(Models) = %d, want 1", len(src.Models))
	}
	m := src.Models[0]
	if m.Model != "claude-sonnet-4-20250514" {
		t.Fatalf("Model = %q, want claude-sonnet-4-20250514", m.Model)
	}

	// Lifetime: Aug 15 (1000 in + 500 out) + Sep 2 (500 in + 200 out) = 1500/700.
	// Month: Sep 2 only (500 in + 200 out) = 500/200. Today: none (Sep 3 has no data).
	if m.Tokens.Input != 1500 {
		t.Errorf("lifetime input = %d, want 1500", m.Tokens.Input)
	}
	if m.TokensMonth.Input != 500 {
		t.Errorf("month input = %d, want 500", m.TokensMonth.Input)
	}
	if m.TokensToday.Input != 0 {
		t.Errorf("today input = %d, want 0", m.TokensToday.Input)
	}

	// Cost: input=3, output=15 USD/MTok (claude-sonnet-4-20250514).
	// Lifetime: (1500*3 + 700*15) / 1e6 = 15000/1e6 = 0.015
	// Month:    (500*3 + 200*15) / 1e6  = 4500/1e6 = 0.0045
	wantLifetimeCost := 0.015
	wantMonthCost := 0.0045
	if m.Cost != wantLifetimeCost {
		t.Errorf("lifetime cost = %.6f, want %.6f", m.Cost, wantLifetimeCost)
	}
	if m.CostMonth != wantMonthCost {
		t.Errorf("month cost = %.6f, want %.6f", m.CostMonth, wantMonthCost)
	}

	// The core invariant: lifetime and month must differ for this model.
	if m.Tokens.Total() == m.TokensMonth.Total() {
		t.Error("lifetime tokens == month tokens — windowing not working for this model")
	}
	if m.Cost == m.CostMonth {
		t.Error("lifetime cost == month cost — windowing not working for this model")
	}
}

// TestScanCodexLongLine pins the 64 KiB cliff.
//
// bufio.Scanner's DEFAULT maximum token size is 64 KiB. A longer line makes
// Scan() stop and report ErrTooLong through sc.Err() only — which nothing
// checked — so the remainder of the file was silently discarded and cached as
// if complete. Measured on real data 2026-09-11: 76 of 141 Codex transcripts
// carried a line over 64 KiB (longest 9,906,699 bytes), and the dashboard
// reported "Codex today: 21,993 tokens" for a day holding 24,378,806 input
// tokens. Cost is derived from these numbers, so the money was wrong too.
//
// A session_meta line carrying Codex's full system prompt is the usual
// culprit, which is why the fixture puts the long line FIRST.
func TestScanCodexLongLine(t *testing.T) {
	codexDir := filepath.Join(fixtureDir(t), "test_longline")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(codexDir) })

	// Comfortably past the default cap, and past a naive 1 MiB bump too.
	huge := strings.Repeat("x", 3*1024*1024)
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-09-03T09:59:00Z","payload":{"type":"session_meta","base_instructions":{"text":"` + huge + `"}}}`,
		`{"type":"event_msg","timestamp":"2026-09-03T10:01:00Z","payload":{"type":"turn_context","model":"gpt-4o"}}`,
		`{"type":"event_msg","timestamp":"2026-09-03T10:01:00Z","payload":{"type":"token_count","info":{"model":"unknown","last_token_usage":{"input_tokens":100,"cached_input_tokens":0,"output_tokens":50,"reasoning_output_tokens":0}}}}`,
	}
	dst := filepath.Join(codexDir, "longline.jsonl")
	if err := os.WriteFile(dst, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewScanner(testTZ)
	s.Clock = func() time.Time { return fixedScanNow }
	report, _, err := s.Scan(context.Background(), ScanConfig{TZ: testTZ, CodexDir: codexDir}, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	src, ok := report.Sources["codex"]
	if !ok {
		t.Fatal("missing codex source")
	}

	// Everything after the long line must still be counted.
	var found bool
	for _, m := range src.Models {
		if m.Model == "gpt-4o" {
			found = true
			if m.Tokens.Input != 100 || m.Tokens.Output != 50 {
				t.Errorf("gpt-4o tokens = %+v, want input 100 / output 50", m.Tokens)
			}
			if m.Requests != 1 {
				t.Errorf("gpt-4o requests = %d, want 1", m.Requests)
			}
		}
	}
	if !found {
		t.Fatal("the token_count AFTER a 3 MiB line was dropped — the scanner is truncating files again")
	}
}

// TestScanCodexIncrementalCarriesModel pins the carry-forward across a resumed
// read.
//
// A Codex scan resumes by SEEKING to the previous file size, so an appended
// chunk starts mid-session and the turn_context naming the model is in the
// chunk before. currentModel used to start empty on every incremental read, so
// each token_count up to the next turn_context was filed as "<unknown>" — a
// bucket that can never be priced, and which then survives in the cache.
// Measured on a live session 2026-09-11: 4,181,625 tokens misfiled that way,
// and the dashboard read "partial: <unknown>" because of it.
func TestScanCodexIncrementalCarriesModel(t *testing.T) {
	codexDir := filepath.Join(fixtureDir(t), "test_incr_model")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(codexDir) })
	dst := filepath.Join(codexDir, "live.jsonl")

	first := `{"type":"turn_context","timestamp":"2026-09-03T10:00:00Z","payload":{"model":"gpt-4o"}}` + "\n" +
		`{"type":"event_msg","timestamp":"2026-09-03T10:01:00Z","payload":{"type":"token_count","info":{"model":"unknown","last_token_usage":{"input_tokens":100,"cached_input_tokens":0,"output_tokens":50,"reasoning_output_tokens":0}}}}` + "\n"
	if err := os.WriteFile(dst, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewScanner(testTZ)
	s.Clock = func() time.Time { return fixedScanNow }
	cfg := ScanConfig{TZ: testTZ, CodexDir: codexDir}

	_, idx1, err := s.Scan(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}

	// The session continues: more usage, and NO new turn_context — exactly what
	// an appended chunk of a running session looks like.
	appended := first + `{"type":"event_msg","timestamp":"2026-09-03T10:02:00Z","payload":{"type":"token_count","info":{"model":"unknown","last_token_usage":{"input_tokens":700,"cached_input_tokens":0,"output_tokens":300,"reasoning_output_tokens":0}}}}` + "\n"
	if err := os.WriteFile(dst, []byte(appended), 0o644); err != nil {
		t.Fatal(err)
	}

	report2, _, err := s.Scan(context.Background(), cfg, idx1)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	src := report2.Sources["codex"]

	for _, m := range src.Models {
		if m.Model == "<unknown>" {
			t.Fatalf("appended usage was filed as <unknown> (%+v) — the model did not carry across the resume", m.Tokens)
		}
	}
	var got Tokens
	for _, m := range src.Models {
		if m.Model == "gpt-4o" {
			got = m.Tokens
		}
	}
	if got.Input != 800 || got.Output != 350 {
		t.Errorf("gpt-4o tokens = %+v, want input 800 / output 350 (both chunks)", got)
	}
	if len(src.UnpricedModels) != 0 {
		t.Errorf("unpriced = %v, want none", src.UnpricedModels)
	}
}
