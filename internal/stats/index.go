package stats

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// LoadIndex reads the stats index from path. Returns an empty Index (not an
// error) when the file does not exist. Returns an error and an empty Index
// when the file exists but is corrupt.
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
