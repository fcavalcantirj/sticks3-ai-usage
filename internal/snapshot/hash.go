package snapshot

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// Rev computes the canonical 8-hex-char revision hash over the provider
// blocks. Only id, status, plan, and the row tuple [k, pct, txt, tier]
// participate — reset_at, msg, label, fetched_at, checked_at and
// generated_at are deliberately excluded so that countdowns and
// last-fetched timestamps never change rev.
func Rev(providers []Provider) string {
	arr := make([]interface{}, len(providers))
	for i, p := range providers {
		rows := make([]interface{}, len(p.Rows))
		for j, r := range p.Rows {
			rows[j] = []interface{}{r.K, r.Pct, r.Txt, r.Tier}
		}
		arr[i] = []interface{}{p.ID, p.Status, p.Plan, rows}
	}

	data, err := json.Marshal(arr)
	if err != nil {
		// Should never happen: only strings, ints, and nil pointers.
		panic(fmt.Sprintf("snapshot hash marshal: %v", err))
	}

	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h[:4]) // first 4 bytes = 8 hex chars
}

// Apply replaces the provider blocks and updates seq/rev/generated_at.
// rev is recomputed; if it changed, Seq is incremented, Rev and
// GeneratedAt are updated. CheckedAt is always updated. NextSec is
// preserved. V is set to 1.
func (s *Snapshot) Apply(newProviders []Provider, now int64) {
	rev := Rev(newProviders)
	if rev != s.Rev {
		s.Seq++
		s.Rev = rev
		s.GeneratedAt = now
	}
	s.CheckedAt = now
	s.Providers = newProviders
	s.V = 1
	// s.NextSec preserved
}
