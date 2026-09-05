package stats

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// indexSchemaVersion is bumped whenever the scan parsing logic changes in a
// way that could invalidate cached FileResults.  When a saved index has a
// different version, LoadIndex discards the entire cache so the next scan
// re-parses from scratch.
const indexSchemaVersion = 3

// indexVersionKey is a sentinel entry stored under this key in the Index map
// to record the schema version.  It is never a real file path.
const indexVersionKey = "__schema_version__"

// LoadIndex reads the stats index from path. Returns an empty Index (not an
// error) when the file does not exist.  Returns an error and an empty Index
// when the file exists but is corrupt.  If the saved index's schema version
// does not match indexSchemaVersion, the cache is silently discarded (empty
// Index returned) so a parser fix is always picked up on the next scan.
func LoadIndex(path string) (Index, error) {
	var idx Index
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(Index), nil
		}
		return make(Index), err
	}
	if err := json.Unmarshal(data, &idx); err != nil {
		return make(Index), fmt.Errorf("corrupt stats index %s: %w", path, err)
	}
	// Schema version check: discard the cache if the version doesn't match.
	fi, ok := idx[indexVersionKey]
	if !ok || fi.Lines != indexSchemaVersion {
		return make(Index), nil
	}
	// Strip the sentinel so callers see only real file entries.
	delete(idx, indexVersionKey)
	return idx, nil
}

// SaveReport writes the stats report atomically to path. The report is
// persisted beside state.json so the API can serve stale data immediately on
// startup before the first poll completes.
func SaveReport(path string, report *Report) error {
	if report == nil {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create stats dir %s: %w", dir, err)
	}

	data, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshal stats report: %w", err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("write stats report %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename stats report %s -> %s: %w", tmpPath, path, err)
	}
	return nil
}

// LoadReport reads the stats report from path. Returns nil, nil when the file
// does not exist. Returns an error and nil when the file exists but is corrupt.
func LoadReport(path string) (*Report, error) {
	var report Report
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("corrupt stats report %s: %w", path, err)
	}
	return &report, nil
}

// SaveIndex writes the stats index atomically to path.
func SaveIndex(path string, idx Index) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create stats index dir %s: %w", dir, err)
	}

	// Stamp the current schema version so a future parser change can detect
	// and discard stale caches.
	idx[indexVersionKey] = FileIndex{Lines: indexSchemaVersion}

	data, err := json.Marshal(idx)
	if err != nil {
		return fmt.Errorf("marshal stats index: %w", err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("write stats index %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename stats index %s -> %s: %w", tmpPath, path, err)
	}
	return nil
}
