package stats

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIndexSchemaVersionCurrent verifies that a freshly saved index with the
// current schema version loads back correctly.
func TestIndexSchemaVersionCurrent(t *testing.T) {
	dir := t.TempDir()
	idxPath := filepath.Join(dir, "index.json")

	idx := Index{
		"some_file.jsonl": FileIndex{Size: 100, Mtime: 123, Lines: 10},
	}
	if err := SaveIndex(idxPath, idx); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadIndex(idxPath)
	if err != nil {
		t.Fatal(err)
	}

	// Current version: cache should be loaded (sentinel key stripped).
	if len(loaded) != 1 {
		t.Errorf("expected 1 entry after schema version match, got %d", len(loaded))
	}
	fi, ok := loaded["some_file.jsonl"]
	if !ok {
		t.Fatal("missing some_file.jsonl entry")
	}
	if fi.Size != 100 {
		t.Errorf("Size = %d, want 100", fi.Size)
	}
	// Sentinel key must not leak into the loaded index.
	if _, ok := loaded[indexVersionKey]; ok {
		t.Error("schema version sentinel should not appear in loaded index")
	}
}

// TestIndexSchemaVersionDiscarded verifies that an older schema version
// causes the entire cache to be discarded (ORDER #44).
func TestIndexSchemaVersionDiscarded(t *testing.T) {
	dir := t.TempDir()
	idxPath := filepath.Join(dir, "index.json")

	// Manually write an index with an OLD schema version so SaveIndex
	// (which stamps the current version) is not used.
	oldJSON := `{"__schema_version__":{"lines":1},"some_file.jsonl":{"size":100,"mtime":123,"lines":10}}`
	if err := os.WriteFile(idxPath, []byte(oldJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadIndex(idxPath)
	if err != nil {
		t.Fatal(err)
	}

	// Schema mismatch: cache should be discarded entirely.
	if len(loaded) != 0 {
		t.Errorf("expected empty index after schema version mismatch, got %d entries: %v",
			len(loaded), loaded)
	}
}

// TestIndexMissingVersionDiscarded verifies that an index with NO schema
// version field at all (e.g. an index saved by a pre-version binary) is
// treated as stale and discarded.
func TestIndexMissingVersionDiscarded(t *testing.T) {
	dir := t.TempDir()
	idxPath := filepath.Join(dir, "index.json")

	// An index with no __schema_version__ key.
	oldJSON := `{"some_file.jsonl":{"size":100,"mtime":123,"lines":10}}`
	if err := os.WriteFile(idxPath, []byte(oldJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadIndex(idxPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(loaded) != 0 {
		t.Errorf("expected empty index when version is missing, got %d entries",
			len(loaded))
	}
}
