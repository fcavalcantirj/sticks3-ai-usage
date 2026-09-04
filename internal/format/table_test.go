package format

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"usaged/internal/snapshot"
)

// fixedOnceNow matches the fixtures and cli tests for deterministic reset text.
var tableTestNow = time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)

func tableTestLoc() *time.Location {
	return time.FixedZone("America/Sao_Paulo", -3*3600)
}

// TestRenderTableDynamicColumns is the ORDER #17 pinning test: a provider
// label longer than the old fixed 10-char column ("OpenRouter fallback", 19
// chars) must not push subsequent columns out of alignment. Every data row
// must share the same RESETS column start position.
func TestRenderTableDynamicColumns(t *testing.T) {
	pct19 := 19
	pct100 := 100

	snap := snapshot.Snapshot{
		V:         1,
		Seq:       1,
		Rev:       "deadbeef",
		CheckedAt: tableTestNow.Unix(),
		NextSec:   900,
		Providers: []snapshot.Provider{
			{
				ID:     "claude",
				Label:  "Claude",
				Status: "ok",
				Rows: []snapshot.Row{
					{K: "5h", Label: "CLAUDE 5h", Pct: &pct19, Txt: "02:09", Tier: "ok"},
				},
			},
			{
				ID:     "openrouter:fallback",
				Label:  "OpenRouter fallback",
				Status: "ok",
				Rows: []snapshot.Row{
					{K: "bal", Label: "ORfbk bal", Pct: nil, Txt: "$0.00", Tier: "ok"},
				},
			},
			{
				ID:     "codex",
				Label:  "ChatGPT",
				Status: "ok",
				Rows: []snapshot.Row{
					{K: "5h", Label: "GPT 5h", Pct: &pct100, Txt: "23:13", Tier: "crit"},
				},
			},
		},
	}

	var buf bytes.Buffer
	RenderTable(snap, &buf, tableTestLoc())
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")

	// First line is the header, last line is the footer (rev=...).
	if len(lines) < 3 {
		t.Fatalf("too few lines: %d", len(lines))
	}
	header := lines[0]
	footer := lines[len(lines)-1]

	if !strings.HasPrefix(footer, "rev=deadbeef") {
		t.Errorf("footer = %q, want prefix rev=deadbeef", footer)
	}

	// Collect the data rows (skip header and footer).
	dataLines := lines[1 : len(lines)-1]
	if len(dataLines) != 3 {
		t.Fatalf("got %d data rows, want 3", len(dataLines))
	}

	// The provider column must be wide enough for "OpenRouter fallback" (19 chars).
	// Verify by checking the start position of the ROW column in the header.
	// "PROVIDER" is 8 chars; the ROW column should start at position providerW + 1.
	// With providerW = 19, ROW starts at position 20.
	rowStart := strings.Index(header, "ROW")
	if rowStart != 20 {
		t.Errorf("ROW header starts at position %d, want 20 (providerW=19+1)", rowStart)
	}

	// Every data row's ROW column must start at the same position as the header.
	// Find where "CLAUDE"/"ORfbk"/"GPT" ends to locate the ROW column start.
	for i, line := range dataLines {
		// The ROW label always starts right after provider padding + separator.
		// For "Claude" (6 chars) padded to 19 + 1 space = 20.
		// For "OpenRouter fallback" (19 chars) + 1 space = 20.
		// For "ChatGPT" (8 chars) padded to 19 + 1 space = 20.
		rowField := extractColumn(line, rowStart, 10)
		if strings.TrimSpace(rowField) == "" {
			t.Errorf("data row %d (%q): ROW column empty at position %d", i, line, rowStart)
		}
	}

	// The RESETS column must start at the same position for every data row,
	// regardless of whether the provider label was short or long.
	// Format: providerW + 1 + rowW + 1 + usedW + 2  →  19 + 1 + 10 + 1 + 4 + 2 = 37
	resetsStart := 37
	for i, line := range dataLines {
		if len(line) < resetsStart {
			t.Errorf("data row %d too short: %q", i, line)
			continue
		}
		field := extractColumn(line, resetsStart, 7)
		if strings.TrimSpace(field) == "" {
			t.Errorf("data row %d (%q): RESETS column empty at position %d", i, line, resetsStart)
		}
	}

	// The "OpenRouter fallback" line must contain the full label without
	// truncation or wrapping.
	offLine := dataLines[1]
	if !strings.HasPrefix(offLine, "OpenRouter fallback ORfbk bal") {
		t.Errorf("fallback row does not start with full label:\n  got:  %q", offLine)
	}
}

// extractColumn returns the substring [start, start+width) of s.
func extractColumn(s string, start, width int) string {
	end := start + width
	if end > len(s) {
		end = len(s)
	}
	if start > len(s) {
		return ""
	}
	return s[start:end]
}
