package snapshot

import (
	"fmt"
	"sort"
	"unicode/utf8"
)

// canonicalProviderOrder is the default display order used when no
// user-chosen order is configured. It is also the SET of known provider ids:
// ids not in this list are rejected by Validate. Providers may appear in any
// order in a snapshot; the user controls display order via config/ProviderOrder.
// Missing ids are allowed (a subset is valid); unknown ids are rejected.
var canonicalProviderOrder = []string{
	"claude", "codex", "openrouter:main", "openrouter:fallback", "groq", "opencode:go",
}

var validStatuses = map[string]bool{
	"ok": true, "stale": true, "auth": true, "error": true, "off": true,
}

var validTiers = map[string]bool{
	"ok": true, "warn": true, "crit": true, "off": true,
}

var validKinds = map[string]bool{
	"plan": true, "credit": true, "free": true,
}

var validSeverities = map[string]bool{
	"ok": true, "warn": true, "crit": true,
}

// Row is a single usage metric row within a provider block.
type Row struct {
	K       string `json:"k"`
	Label   string `json:"label"`
	Pct     *int   `json:"pct"` // nil → JSON null (no omitempty)
	Txt     string `json:"txt"`
	Tier    string `json:"tier"`
	ResetAt *int64 `json:"reset_at"` // nil → JSON null (no omitempty)
}

// Provider is a single provider's usage block.
type Provider struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Plan      string `json:"plan"`
	Kind      string `json:"kind"`       // plan|credit|free — hashed into rev
	Severity  string `json:"severity"`   // ok|warn|crit — hashed into rev
	Status    string `json:"status"`     // ok|stale|auth|error|off
	Msg       string `json:"msg"`        // ≤24 chars, human hint
	FetchedAt int64  `json:"fetched_at"` // serialised for the web page; NOT part of the hash
	Rows      []Row  `json:"rows"`
}

// Snapshot is the ONE v1 contract every client uses.
type Snapshot struct {
	V           int        `json:"v"`
	Seq         int64      `json:"seq"`          // +1 only when rev changes
	Rev         string     `json:"rev"`          // 8 hex chars
	GeneratedAt int64      `json:"generated_at"` // unix s of last CHANGE
	CheckedAt   int64      `json:"checked_at"`   // unix s of last poll attempt
	NextSec     int        `json:"next_sec"`     // 900
	Providers   []Provider `json:"providers"`    // sorted in user-chosen display order (default: canonical)
}

// Validate enforces the v1 contract rules.
func (s *Snapshot) Validate() error {
	if s.V != 1 {
		return fmt.Errorf("v must be 1, got %d", s.V)
	}

	seen := make(map[string]bool)

	for _, p := range s.Providers {
		if seen[p.ID] {
			return fmt.Errorf("duplicate provider id: %s", p.ID)
		}
		seen[p.ID] = true

		// Known-id check against the canonical set (order is NOT enforced —
		// the user may choose any display order via config.ProviderOrder).
		known := false
		for _, id := range canonicalProviderOrder {
			if p.ID == id {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("unknown provider id: %s", p.ID)
		}

		if !validStatuses[p.Status] {
			return fmt.Errorf("invalid status %q for provider %s", p.Status, p.ID)
		}
		if !validKinds[p.Kind] {
			return fmt.Errorf("invalid kind %q for provider %s", p.Kind, p.ID)
		}
		if !validSeverities[p.Severity] {
			return fmt.Errorf("invalid severity %q for provider %s", p.Severity, p.ID)
		}
		if utf8.RuneCountInString(p.Msg) > 24 {
			return fmt.Errorf("msg for provider %s exceeds 24 chars (%d)", p.ID, utf8.RuneCountInString(p.Msg))
		}

		for _, r := range p.Rows {
			if utf8.RuneCountInString(r.Label) > 10 {
				return fmt.Errorf("label %q for provider %s exceeds 10 chars", r.Label, p.ID)
			}
			if utf8.RuneCountInString(r.Txt) > 11 {
				return fmt.Errorf("txt %q for provider %s exceeds 11 chars", r.Txt, p.ID)
			}
			if !validTiers[r.Tier] {
				return fmt.Errorf("invalid tier %q for provider %s", r.Tier, p.ID)
			}
		}
	}

	return nil
}

// Order returns ids sorted into the user-chosen display order. If order is
// non-empty, ids are arranged to match it (preserving input order for ids not
// in the list). If order is empty or nil, the canonicalProviderOrder default
// is used instead. Unknown ids (not in canonicalProviderOrder) are placed at
// the end, preserving input order.
func Order(ids []string, order []string) []string {
	pos := make(map[string]int, len(canonicalProviderOrder))
	ref := order
	if len(ref) == 0 {
		ref = canonicalProviderOrder
	}
	for i, id := range ref {
		pos[id] = i
	}

	sorted := make([]string, len(ids))
	copy(sorted, ids)

	sort.SliceStable(sorted, func(i, j int) bool {
		pi, ok1 := pos[sorted[i]]
		if !ok1 {
			pi = len(ref)
		}
		pj, ok2 := pos[sorted[j]]
		if !ok2 {
			pj = len(ref)
		}
		return pi < pj
	})

	return sorted
}
