// Package stats scans local AI tool transcripts (Claude Code JSONL and Codex
// rollouts) and aggregates token usage, cost estimates, and daily heatmaps.
// The scanning logic is pure and host-tested; the callers (Scheduler, CLI, API)
// supply the root directories and an on-disk index for incremental re-scans.
package stats

import (
	"time"
)

// Tokens is a four-component token breakdown shared by every aggregation level.
type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`  // read from a prior response's cache
	CacheWrite int64 `json:"cache_write"` // fresh cache put (billed at input rate)
}

// Add accumulates b into a, returning the sum.
func (a Tokens) Add(b Tokens) Tokens {
	return Tokens{
		Input:      a.Input + b.Input,
		Output:     a.Output + b.Output,
		CacheRead:  a.CacheRead + b.CacheRead,
		CacheWrite: a.CacheWrite + b.CacheWrite,
	}
}

// Total returns the sum of all four components.
func (a Tokens) Total() int64 {
	return a.Input + a.Output + a.CacheRead + a.CacheWrite
}

// Totals combines token counts, cost, and request count at any grain
// (today, month, or model).
type Totals struct {
	Tokens   Tokens  `json:"tokens"`
	Cost     float64 `json:"cost"`     // USD, estimated
	Requests int     `json:"requests"` // number of API calls / turns
}

// Model aggregates token usage for one model within a source.
type Model struct {
	Model    string  `json:"model"`
	Tokens   Tokens  `json:"tokens"`
	Cost     float64 `json:"cost"`
	Requests int     `json:"requests"`
}

// Day aggregates token usage for one calendar day.
type Day struct {
	Date    string            `json:"date"` // "2026-09-03"
	Tokens  Tokens            `json:"tokens"`
	Cost    float64           `json:"cost"`
	ByModel map[string]Tokens `json:"by_model"`
}

// Peak records the day with the most tokens in the scan window.
type Peak struct {
	Date   string `json:"date"`
	Tokens int64  `json:"tokens"`
}

// Source aggregates stats for one transcript source (claude_code or codex).
type Source struct {
	Today      Totals  `json:"today"`
	Month      Totals  `json:"month"`
	Models     []Model `json:"models"` // sorted by month tokens desc
	Days       []Day   `json:"days"`   // last 182 days, oldest first
	ActiveDays int     `json:"active_days"`
	Peak       Peak    `json:"peak"`
}

// Report is the top-level stats report returned by Scan and served at
// /v1/stats. The Sources map is keyed by "claude_code" and "codex".
type Report struct {
	GeneratedAt int64             `json:"generated_at"` // unix s
	Sources     map[string]Source `json:"sources"`
}

// ScanConfig controls which directories to scan and the timezone for
// day bucketing. Zero/empty roots mean that source is skipped.
type ScanConfig struct {
	TZ        *time.Location
	ClaudeDir string // e.g. ~/.claude/projects/
	CodexDir  string // e.g. ~/.codex/sessions/
}

// FileIndex is the per-file incremental-scan metadata.
type FileIndex struct {
	Size   int64       `json:"size"`   // last scanned file size
	Mtime  int64       `json:"mtime"`  // last scanned modification time (unix s)
	Lines  int         `json:"lines"`  // lines processed so far
	Result *FileResult `json:"result"` // cached per-file aggregate (nil if not cached)
}

// Index maps file paths to their scan metadata.
type Index map[string]FileIndex

// FileResult is the aggregated result of scanning one transcript file.
// It caches the per-model and per-day token counts so that a second scan
// with no file changes can skip opening the file entirely.
type FileResult struct {
	Models map[string]ModelAgg `json:"models"` // model name → aggregated tokens/requests
	Days   map[string]DayAgg   `json:"days"`   // dayKey → aggregated tokens/requests
}

// ModelAgg holds the aggregated token counts and request count for one
// model within a single file.
type ModelAgg struct {
	Tokens Tokens `json:"tokens"`
	Reqs   int    `json:"reqs"`
}

// DayAgg holds the aggregated token counts for one calendar day within a
// single file. DayKey format is "YYYY-MM-DD".
type DayAgg struct {
	Tokens  Tokens            `json:"tokens"`
	Reqs    int               `json:"reqs"`
	ByModel map[string]Tokens `json:"by_model"` // model → tokens (for cost calc)
}
