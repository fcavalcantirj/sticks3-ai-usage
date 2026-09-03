package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// State persists across process restarts: the last snapshot, per-provider
// cooldown deadlines, and the last-good provider blocks for stale backoff.
type State struct {
	Snapshot  Snapshot            `json:"snapshot"`
	Cooldowns map[string]int64    `json:"cooldowns"` // provider id → unix s deadline
	LastGood  map[string]Provider `json:"last_good"` // provider id → last ok block
}

// Load reads state from path. Returns an empty State (not an error)
// when the file does not exist. Returns an error AND an empty State
// when the file exists but is corrupt — caller logs and starts fresh.
func Load(path string) (State, error) {
	var st State
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("corrupt state file %s: %w", path, err)
	}
	return st, nil
}

// Save writes state atomically: write to path+".tmp", then rename.
// Parent directories are created with 0700; the file is written with 0600.
func Save(path string, st State) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir %s: %w", dir, err)
	}

	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("write state %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename state %s -> %s: %w", tmpPath, path, err)
	}

	return nil
}
