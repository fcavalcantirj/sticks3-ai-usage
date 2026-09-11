package stats

import (
	"os"
	"path/filepath"
	"regexp"
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

// TestIndexSchemaVersionTripwire fails when the Codex parser changes without
// the cache being invalidated.
//
// WHY THIS EXISTS. A cached FileResult is immortal: the scanner skips any file
// whose size and mtime still match the index, and a finished transcript never
// changes again. So when the parser gained turn_context /
// thread_settings_applied handling without bumping indexSchemaVersion, 20
// files kept the answer the OLD parser gave them — "<unknown>" — forever. The
// dashboard showed "partial: <unknown>" and silently left ~4.9M tokens out of
// the API-equivalent cost, because an unknown model cannot be priced.
//
// No test caught it, and no test could: every test parses fixtures, and the
// fixtures were always read by the current parser. The only durable guard is
// to make a parser change LOUD at review time.
//
// If this test fails: you changed how a transcript is read. Bump
// indexSchemaVersion so every cached result is discarded, then update the
// constants here.
func TestIndexSchemaVersionTripwire(t *testing.T) {
	const wantVersion = 4

	if indexSchemaVersion != wantVersion {
		t.Fatalf("indexSchemaVersion = %d, this tripwire knows %d — if you bumped it on purpose, update wantVersion and the payload list below",
			indexSchemaVersion, wantVersion)
	}

	// The payload types the Codex scanner acts on. Adding, removing or
	// renaming one changes what a cached result would have contained.
	want := []string{"turn_context", "thread_settings_applied", "token_count"}
	src, err := os.ReadFile("scan.go")
	if err != nil {
		t.Fatalf("read scan.go: %v", err)
	}
	got := regexp.MustCompile(`Payload\.Type [!=]= "([a-z_]+)"`).FindAllStringSubmatch(string(src), -1)
	seen := make(map[string]bool, len(got))
	for _, m := range got {
		seen[m[1]] = true
	}
	for _, w := range want {
		if !seen[w] {
			t.Errorf("scan.go no longer handles payload type %q — the parser changed; bump indexSchemaVersion (currently %d) so stale caches are discarded, then update this test",
				w, indexSchemaVersion)
		}
		delete(seen, w)
	}
	for extra := range seen {
		t.Errorf("scan.go handles a new payload type %q — the parser changed; bump indexSchemaVersion (currently %d) so stale caches are discarded, then add it here",
			extra, indexSchemaVersion)
	}
}
