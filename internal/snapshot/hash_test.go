package snapshot

import (
	"strings"
	"testing"
)

// goldenRev is the known rev of exampleSnapshot() providers, computed once
// and pinned so future refactors cannot silently change the hashing.
const goldenRev = "db2b7d8f"

func TestRevIdentical(t *testing.T) {
	pct := 19
	r := int64(1788411000)
	providers := []Provider{
		{ID: "claude", Status: "ok", Plan: "max_20x",
			Rows: []Row{
				{K: "5h", Pct: &pct, Txt: "05:09", Tier: "ok", ResetAt: &r},
			}},
	}
	a := Rev(providers)
	b := Rev(providers)
	if a != b {
		t.Errorf("Rev(a) = %q, Rev(b) = %q; want equal", a, b)
	}
}

func TestRevInvariantFields(t *testing.T) {
	// Changing only reset_at, msg, label, fetched_at → SAME rev
	pct := 19
	r1 := int64(100)
	r2 := int64(200)

	base := []Provider{
		{ID: "claude", Label: "Claude", Status: "ok", Plan: "max_20x", Msg: "", FetchedAt: 0,
			Rows: []Row{
				{K: "5h", Label: "LBL", Pct: &pct, Txt: "05:09", Tier: "ok", ResetAt: &r1},
			}},
	}
	changed := []Provider{
		{ID: "claude", Label: "Different", Status: "ok", Plan: "max_20x", Msg: "different msg", FetchedAt: 999,
			Rows: []Row{
				{K: "5h", Label: "LBL2", Pct: &pct, Txt: "05:09", Tier: "ok", ResetAt: &r2},
			}},
	}

	if Rev(base) != Rev(changed) {
		t.Error("rev should be invariant under changes to label, msg, fetched_at, reset_at")
	}
}

func TestRevChangesFields(t *testing.T) {
	// Changing pct, txt, tier, status, or plan → DIFFERENT rev
	base := []Provider{
		{ID: "claude", Status: "ok", Plan: "max_20x",
			Rows: []Row{
				{K: "5h", Pct: intPtr(19), Txt: "05:09", Tier: "ok"},
			}},
	}

	// pct changed
	pct2 := 20
	diffPct := []Provider{
		{ID: "claude", Status: "ok", Plan: "max_20x",
			Rows: []Row{{K: "5h", Pct: &pct2, Txt: "05:09", Tier: "ok"}}},
	}
	if Rev(base) == Rev(diffPct) {
		t.Error("rev should change when pct changes")
	}

	// txt changed
	diffTxt := []Provider{
		{ID: "claude", Status: "ok", Plan: "max_20x",
			Rows: []Row{{K: "5h", Pct: intPtr(19), Txt: "06:09", Tier: "ok"}}},
	}
	if Rev(base) == Rev(diffTxt) {
		t.Error("rev should change when txt changes")
	}

	// tier changed
	diffTier := []Provider{
		{ID: "claude", Status: "ok", Plan: "max_20x",
			Rows: []Row{{K: "5h", Pct: intPtr(19), Txt: "05:09", Tier: "warn"}}},
	}
	if Rev(base) == Rev(diffTier) {
		t.Error("rev should change when tier changes")
	}

	// status changed
	diffStatus := []Provider{
		{ID: "claude", Status: "stale", Plan: "max_20x",
			Rows: []Row{{K: "5h", Pct: intPtr(19), Txt: "05:09", Tier: "ok"}}},
	}
	if Rev(base) == Rev(diffStatus) {
		t.Error("rev should change when status changes")
	}

	// plan changed
	diffPlan := []Provider{
		{ID: "claude", Status: "ok", Plan: "max_10x",
			Rows: []Row{{K: "5h", Pct: intPtr(19), Txt: "05:09", Tier: "ok"}}},
	}
	if Rev(base) == Rev(diffPlan) {
		t.Error("rev should change when plan changes")
	}
}

func TestRevFormat(t *testing.T) {
	r := int64(0)
	providers := []Provider{
		{ID: "claude", Status: "ok", Plan: "max_20x",
			Rows: []Row{{K: "5h", Pct: intPtr(0), Txt: "00:00", Tier: "ok", ResetAt: &r}}},
	}
	rev := Rev(providers)
	if len(rev) != 8 {
		t.Errorf("rev length = %d, want 8", len(rev))
	}
	for _, c := range rev {
		if !strings.ContainsAny("0123456789abcdef", string(c)) {
			t.Errorf("rev %q contains non-lowercase-hex char %q", rev, c)
		}
	}
}

func TestRevGolden(t *testing.T) {
	snap := exampleSnapshot()
	rev := Rev(snap.Providers)
	if rev != goldenRev {
		t.Errorf("Rev(example) = %q, want %q", rev, goldenRev)
	}
}

func TestApplyNoChange(t *testing.T) {
	pct := 19
	r := int64(1788411000)
	providers := []Provider{
		{ID: "claude", Status: "ok", Plan: "max_20x",
			Rows: []Row{
				{K: "5h", Pct: &pct, Txt: "05:09", Tier: "ok", ResetAt: &r},
			}},
	}

	s := &Snapshot{V: 1}
	now := int64(1788414949)
	s.Apply(providers, now)

	if s.Seq != 1 {
		t.Errorf("after first Apply: Seq = %d, want 1", s.Seq)
	}
	firstRev := s.Rev
	firstGen := s.GeneratedAt

	// Apply same providers again — seq and generated_at should NOT change
	s.Apply(providers, now+100)
	if s.Seq != 1 {
		t.Errorf("after second Apply (no change): Seq = %d, want 1", s.Seq)
	}
	if s.Rev != firstRev {
		t.Errorf("rev changed on identical content: got %q, want %q", s.Rev, firstRev)
	}
	if s.GeneratedAt != firstGen {
		t.Errorf("GeneratedAt changed on identical content: got %d, want %d", s.GeneratedAt, firstGen)
	}
	if s.CheckedAt != now+100 {
		t.Errorf("CheckedAt = %d, want %d (should always update)", s.CheckedAt, now+100)
	}
}

func TestApplyChange(t *testing.T) {
	pct := 19
	r := int64(1788411000)
	providers := []Provider{
		{ID: "claude", Status: "ok", Plan: "max_20x",
			Rows: []Row{
				{K: "5h", Pct: &pct, Txt: "05:09", Tier: "ok", ResetAt: &r},
			}},
	}

	s := &Snapshot{V: 1}
	s.Apply(providers, 1000)
	if s.Seq != 1 {
		t.Errorf("after first Apply: Seq = %d, want 1", s.Seq)
	}
	if s.GeneratedAt != 1000 {
		t.Errorf("GeneratedAt = %d, want 1000", s.GeneratedAt)
	}

	// Change pct → rev changes → seq increments, generated_at updates
	pct2 := 25
	changed := []Provider{
		{ID: "claude", Status: "ok", Plan: "max_20x",
			Rows: []Row{
				{K: "5h", Pct: &pct2, Txt: "06:09", Tier: "warn", ResetAt: &r},
			}},
	}

	s.Apply(changed, 2000)
	if s.Seq != 2 {
		t.Errorf("after change Apply: Seq = %d, want 2", s.Seq)
	}
	if s.GeneratedAt != 2000 {
		t.Errorf("GeneratedAt = %d, want 2000", s.GeneratedAt)
	}
	if s.Rev == "" {
		t.Error("rev should be set after Apply")
	}
	if s.V != 1 {
		t.Errorf("V = %d, want 1", s.V)
	}
}
