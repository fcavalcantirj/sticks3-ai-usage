package snapshot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	pct := 50
	original := State{
		Snapshot: Snapshot{
			V:           1,
			Seq:         3,
			Rev:         "abc12345",
			GeneratedAt: 1000,
			CheckedAt:   2000,
			NextSec:     900,
			Providers: []Provider{
				{ID: "claude", Label: "Claude", Status: "ok",
					Rows: []Row{{K: "5h", Label: "CLAUDE 5h", Pct: &pct, Txt: "05:09", Tier: "ok"}}},
			},
		},
		Cooldowns: map[string]int64{"claude": 1000},
		LastGood:  map[string]Provider{"claude": {ID: "claude", Status: "ok"}},
	}

	if err := Save(path, original); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Snapshot.V != 1 {
		t.Errorf("V: got %d, want 1", loaded.Snapshot.V)
	}
	if loaded.Snapshot.Seq != 3 {
		t.Errorf("Seq: got %d, want 3", loaded.Snapshot.Seq)
	}
	if loaded.Snapshot.Rev != "abc12345" {
		t.Errorf("Rev: got %q, want abc12345", loaded.Snapshot.Rev)
	}
	if len(loaded.Cooldowns) != 1 || loaded.Cooldowns["claude"] != 1000 {
		t.Errorf("Cooldowns: %v", loaded.Cooldowns)
	}
	if loaded.LastGood["claude"].Status != "ok" {
		t.Errorf("LastGood: %v", loaded.LastGood)
	}
	if len(loaded.Snapshot.Providers) != 1 || loaded.Snapshot.Providers[0].ID != "claude" {
		t.Errorf("Providers: %v", loaded.Snapshot.Providers)
	}
}

func TestStateMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.json")

	st, err := Load(path)
	if err != nil {
		t.Fatalf("Load on missing file should return nil error, got: %v", err)
	}
	if st.Snapshot.Seq != 0 {
		t.Errorf("empty state should have Seq=0, got %d", st.Snapshot.Seq)
	}
	if st.Snapshot.Rev != "" {
		t.Errorf("empty state should have empty Rev, got %q", st.Snapshot.Rev)
	}
}

func TestStateCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	if err := os.WriteFile(path, []byte("not json{{{"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Load(path)
	if err == nil {
		t.Fatal("expected error for corrupt file, got nil")
	}
	if st.Snapshot.Seq != 0 {
		t.Errorf("corrupt state should return empty state, got Seq=%d", st.Snapshot.Seq)
	}
}

func TestStateParentDirCreated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "nested", "state.json")

	st := State{
		Snapshot:  Snapshot{V: 1, Rev: "deadbeef"},
		Cooldowns: map[string]int64{},
		LastGood:  map[string]Provider{},
	}
	if err := Save(path, st); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file should exist after Save: %v", err)
	}
}

func TestStateSaveInterrupted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	tmpPath := path + ".tmp"

	// Simulate a previous interrupted save: garbage in the .tmp file
	if err := os.WriteFile(tmpPath, []byte("garbage{{{broken"), 0o600); err != nil {
		t.Fatal(err)
	}

	st := State{
		Snapshot:  Snapshot{V: 1, Rev: "deadbeef"},
		Cooldowns: map[string]int64{"claude": 500},
	}
	if err := Save(path, st); err != nil {
		t.Fatalf("Save after interrupted: %v", err)
	}

	// .tmp should be gone (renamed to path)
	if _, err := os.Stat(tmpPath); err == nil {
		t.Error(".tmp file should not exist after successful save")
	}

	// Main file should be valid
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load after interrupted save: %v", err)
	}
	if loaded.Snapshot.Rev != "deadbeef" {
		t.Errorf("Rev: got %q, want deadbeef", loaded.Snapshot.Rev)
	}
	if loaded.Cooldowns["claude"] != 500 {
		t.Errorf("Cooldowns: %v", loaded.Cooldowns)
	}
}

func TestStateFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	if err := Save(path, State{Snapshot: Snapshot{V: 1}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode: %v, want 0600", info.Mode().Perm())
	}
}
