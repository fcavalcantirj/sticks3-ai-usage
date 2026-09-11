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

// IsZero reports whether no tokens of any kind were recorded.
func (t Tokens) IsZero() bool {
	return t.Input == 0 && t.Output == 0 && t.CacheRead == 0 && t.CacheWrite == 0
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
	Model       string  `json:"model"`
	Tokens      Tokens  `json:"tokens"`       // lifetime across the scan window
	Cost        float64 `json:"cost"`         // lifetime cost (USD)
	Requests    int     `json:"requests"`     // lifetime request count
	TokensToday Tokens  `json:"tokens_today"` // windowed to the current day
	TokensMonth Tokens  `json:"tokens_month"` // windowed to the current month
	CostMonth   float64 `json:"cost_month"`   // windowed month cost (USD)
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
	Today          Totals   `json:"today"`
	Month          Totals   `json:"month"`
	Models         []Model  `json:"models"` // sorted by month tokens desc
	Days           []Day    `json:"days"`   // last 182 days, oldest first
	ActiveDays     int      `json:"active_days"`
	Peak           Peak     `json:"peak"`
	UnpricedModels []string `json:"unpriced_models,omitempty"` // model IDs with unknown prices (lifetime)
	Partial        bool     `json:"partial,omitempty"`         // true if any model is unpriced
	// Windowed: the unpriced models that actually carry tokens in that window.
	// A tile must name only what IT is missing — the lifetime union was once
	// printed under "Cost today" naming a model with zero tokens today.
	UnpricedToday []string `json:"unpriced_today,omitempty"`
	UnpricedMonth []string `json:"unpriced_month,omitempty"`

	// Billed indicates whether the source's cost is a real per-token charge
	// (true) or a subscription-equivalent estimate (false). Cliques on a
	// Plus/Max plan are billed via subscription, so their cost is labelled
	// "API-equiv" in the CLI rather than shown as a bare dollar amount.
	Billed bool `json:"billed"`
	// Plan is the subscription tier name for unbilled sources (e.g. "Max",
	// "Plus"), used in the CLI label "(Plan plan)" instead of a bare dollar.
	Plan string `json:"plan,omitempty"`
	// PlanValue is non-nil when a subscription plan is configured for this
	// source (via YAML plan: block). Carries the plan cost, currency, and the
	// API-equivalent ratio. Populated by ApplyPlanValues, not by the scanner.
	PlanValue *PlanValue `json:"plan_value,omitempty"`
}

// PlanValue is the subscription-plan value analysis for a source. It is
// populated by ApplyPlanValues (called by the CLI and API server) after the
// stats scan, using the plan block from the YAML config.
type PlanValue struct {
	Cost     float64 `json:"cost"`               // monthly plan cost in original currency
	Currency string  `json:"currency"`           // e.g. "USD", "BRL"
	CostUSD  float64 `json:"cost_usd,omitempty"` // manual USD equivalent (0 if absent)
	Label    string  `json:"label"`              // e.g. "Max 20x", "Plus"
	Ratio    float64 `json:"ratio"`              // API-equiv month cost / plan cost (USD)
	HasRatio bool    `json:"has_ratio"`          // true when the ratio can be computed
	Warn     bool    `json:"warn"`               // true when ratio < 1.0 (plan not paying for itself)
}

// PlanParams is the plan configuration used to compute a PlanValue. It is
// the config-package PlanConfig converted to primitives, so the stats package
// stays free of config imports.
type PlanParams struct {
	Cost       float64
	Currency   string
	CostUSD    float64
	HasCostUSD bool
	Label      string
}

// PlanCost is the subscription-plan cost summary computed server-side from
// the YAML plan parameters. The large tile value (PrimaryTotal) is the sum of
// stated plan prices in the primary currency (the one most plan providers use);
// the small tile value (SecondaryTotal) is the same sum converted to the
// secondary currency using an exchange rate derived from plan pairs that carry
// both a local cost and a manual cost_usd. When those pairs disagree beyond
// tolerance, conversion is suppressed and the disagreeing plans are named in
// OmittedPlans.
//
// APICostToday/APICostMonth are the old equivalent-API estimates (from scanned
// local transcripts), surfaced below the plan sum with partial disclosure.
type PlanCost struct {
	PrimaryCurrency   string   `json:"primary_currency"`              // e.g. "BRL"
	PrimaryTotal      float64  `json:"primary_total"`                 // sum of plan costs in primary currency
	SecondaryCurrency string   `json:"secondary_currency,omitempty"`  // e.g. "USD"; omitted when one-currency config
	SecondaryTotal    float64  `json:"secondary_total,omitempty"`     // plan sum converted to secondary currency
	ExchangeRate      float64  `json:"exchange_rate,omitempty"`       // primary per USD (e.g. 5.5 BRL/USD)
	RateDisagrees     bool     `json:"rate_disagrees,omitempty"`      // true when plan-pair rates conflict
	OmittedPlans      []string `json:"omitted_plans,omitempty"`       // plan labels omitted from conversion on disagreement
	APICostToday      float64  `json:"api_cost_today"`                // equivalent-API estimate for today (USD)
	APICostMonth      float64  `json:"api_cost_month"`                // equivalent-API estimate for month (USD)
	APIPartial        bool     `json:"api_partial,omitempty"`         // true when any scanned source has unpriced models
	APIUnpricedModels []string `json:"api_unpriced_models,omitempty"` // model IDs with unknown prices (lifetime)
	// Per-window, so each cost tile annotates itself with its own gaps.
	APIUnpricedToday []string `json:"api_unpriced_today,omitempty"`
	APIUnpricedMonth []string `json:"api_unpriced_month,omitempty"`
	APIPartialToday  bool     `json:"api_partial_today,omitempty"`
	APIPartialMonth  bool     `json:"api_partial_month,omitempty"`
}

// Report is the top-level stats report returned by Scan and served at
// /v1/stats. The Sources map is keyed by "claude_code" and "codex".
type Report struct {
	GeneratedAt int64             `json:"generated_at"` // unix s
	Sources     map[string]Source `json:"sources"`
	PlanCost    *PlanCost         `json:"plan_cost,omitempty"`
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

	// LastModel is the model in effect at the END of the bytes scanned so far.
	//
	// A Codex scan resumes by SEEKING to the previous file size, so an
	// appended chunk begins mid-session — and the model is declared on
	// turn_context lines that were in the PREVIOUS chunk. Without carrying it
	// across, every token_count before the next turn_context was filed as
	// "<unknown>": measured 2026-09-11, 4,181,625 tokens of a live session.
	// It is the model id only, never a credential.
	LastModel string `json:"last_model,omitempty"`
	// LastDayKey is the day bucket in effect at that same point, carried for
	// the same reason — a resumed chunk would otherwise date its first rows
	// from the token_count line rather than the turn that produced them.
	LastDayKey string `json:"last_day_key,omitempty"`
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
