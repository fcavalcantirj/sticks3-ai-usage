package snapshot

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func intPtr(n int) *int {
	return &n
}

func int64Ptr(n int64) *int64 {
	return &n
}

// exampleSnapshot returns the snapshot from the v1 contract with concrete values.
func exampleSnapshot() Snapshot {
	pct5h := 19
	pct7d := 30
	pctFable := 20
	pctCodex5h := 100
	pctCodex7d := 31

	reset5h := int64(1788411000)
	reset7d := int64(1788768000)
	resetCodex5h := int64(1788401621)
	resetCodex7d := int64(1788969665)

	return Snapshot{
		V:           1,
		Seq:         1,
		Rev:         "a1b2c3d4",
		GeneratedAt: 1788414949,
		CheckedAt:   1788414949,
		NextSec:     900,
		Providers: []Provider{
			{
				ID:     "claude",
				Label:  "Claude",
				Plan:   "max_20x",
				Status: "ok",
				Msg:    "",
				Rows: []Row{
					{K: "5h", Label: "CLAUDE 5h", Pct: &pct5h, Txt: "05:09", Tier: "ok", ResetAt: &reset5h},
					{K: "7d", Label: "CLAUDE 7d", Pct: &pct7d, Txt: "Mon", Tier: "ok", ResetAt: &reset7d},
					{K: "7d:Fable", Label: "FABLE 7d", Pct: &pctFable, Txt: "Mon", Tier: "ok", ResetAt: &reset7d},
				},
			},
			{
				ID:     "codex",
				Label:  "ChatGPT",
				Plan:   "plus",
				Status: "ok",
				Msg:    "",
				Rows: []Row{
					{K: "5h", Label: "GPT 5h", Pct: &pctCodex5h, Txt: "23:13", Tier: "crit", ResetAt: &resetCodex5h},
					{K: "7d", Label: "GPT 7d", Pct: &pctCodex7d, Txt: "Tue", Tier: "ok", ResetAt: &resetCodex7d},
					{K: "bal", Label: "GPT bal", Pct: nil, Txt: "$178.10", Tier: "ok", ResetAt: nil},
				},
			},
		},
	}
}

// TestTypesGolden: golden encode/decode round-trip.
// Creates the golden file on first run; verifies byte-stable MarshalIndent thereafter.
func TestTypesGolden(t *testing.T) {
	goldenPath := "testdata/snapshots/example.json"

	if _, err := os.Stat(goldenPath); os.IsNotExist(err) {
		snap := exampleSnapshot()
		data, err := json.MarshalIndent(snap, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll("testdata/snapshots", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("created golden file %s", goldenPath)
		return
	}

	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}

	var snap Snapshot
	if err := json.Unmarshal(golden, &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.MarshalIndent(&snap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(golden, out) {
		t.Errorf("round-trip mismatch:\n got: %s\nwant: %s", out, golden)
	}
}

// TestTypesEncodeDecode — basic encode/decode round-trip.
func TestTypesEncodeDecode(t *testing.T) {
	snap := exampleSnapshot()
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Snapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.V != snap.V {
		t.Errorf("V: got %d, want %d", decoded.V, snap.V)
	}
	if len(decoded.Providers) != len(snap.Providers) {
		t.Fatalf("providers: got %d, want %d", len(decoded.Providers), len(snap.Providers))
	}
	if decoded.Providers[0].ID != "claude" {
		t.Errorf("first provider: got %q, want claude", decoded.Providers[0].ID)
	}
	if decoded.Providers[1].ID != "codex" {
		t.Errorf("second provider: got %q, want codex", decoded.Providers[1].ID)
	}
}

// TestTypesNilPctNull — nil *int and *int64 must encode as JSON null.
func TestTypesNilPctNull(t *testing.T) {
	snap := Snapshot{
		V: 1,
		Providers: []Provider{
			{
				ID:     "codex",
				Label:  "ChatGPT",
				Plan:   "plus",
				Status: "ok",
				Rows: []Row{
					{K: "bal", Label: "GPT bal", Pct: nil, Txt: "$178.10", Tier: "ok", ResetAt: nil},
				},
			},
		},
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, `"pct":null`) {
		t.Errorf("expected \"pct\":null in JSON, got: %s", s)
	}
	if !strings.Contains(s, `"reset_at":null`) {
		t.Errorf("expected \"reset_at\":null in JSON, got: %s", s)
	}
}

// TestValidateRejectsLongLabel — label > 10 chars.
func TestValidateRejectsLongLabel(t *testing.T) {
	snap := Snapshot{
		V: 1,
		Providers: []Provider{
			{
				ID:     "claude",
				Label:  "Claude",
				Plan:   "max_20x",
				Status: "ok",
				Rows: []Row{
					{K: "5h", Label: "TOO_LONG_LABEL", Pct: intPtr(50), Txt: "05:09", Tier: "warn", ResetAt: int64Ptr(1788411000)},
				},
			},
		},
	}
	err := snap.Validate()
	if err == nil {
		t.Fatal("expected error for 12+ char label")
	}
}

// TestValidateRejectsUnknownTier — tier not in ok|warn|crit|off.
func TestValidateRejectsUnknownTier(t *testing.T) {
	snap := Snapshot{
		V: 1,
		Providers: []Provider{
			{
				ID:     "claude",
				Label:  "Claude",
				Plan:   "max_20x",
				Status: "ok",
				Rows: []Row{
					{K: "5h", Label: "CLAUDE 5h", Pct: intPtr(50), Txt: "05:09", Tier: "unknown"},
				},
			},
		},
	}
	err := snap.Validate()
	if err == nil {
		t.Fatal("expected error for unknown tier")
	}
}

// TestValidateRejectsDuplicateIDs — two providers with same ID.
func TestValidateRejectsDuplicateIDs(t *testing.T) {
	snap := Snapshot{
		V: 1,
		Providers: []Provider{
			{ID: "claude", Label: "Claude", Status: "ok"},
			{ID: "claude", Label: "Claude2", Status: "ok"},
		},
	}
	err := snap.Validate()
	if err == nil {
		t.Fatal("expected error for duplicate provider ids")
	}
}
