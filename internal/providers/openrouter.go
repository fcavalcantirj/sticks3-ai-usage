package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"usaged/internal/format"
	"usaged/internal/httpx"
	"usaged/internal/snapshot"
)

const (
	openrouterCreditsURL = "https://openrouter.ai/api/v1/credits"
	openrouterKeyURL     = "https://openrouter.ai/api/v1/key"
)

// openRouterProvider implements Fetcher for the OpenRouter credits + key
// endpoints. It fetches a key's balance (/credits) and per-key usage
// (/key), falling back to /key figures only when /credits is unavailable.
type openRouterProvider struct {
	client *httpx.Client
	id     string // "openrouter:main" | "openrouter:fallback"
	label  string // "OpenRouter main" | "OpenRouter fallback"
	key    string
}

// NewOpenRouter creates a Fetcher that polls the OpenRouter credits and key
// endpoints for a single API key.
func NewOpenRouter(client *httpx.Client, id string, label string, key string) Fetcher {
	return &openRouterProvider{
		client: client,
		id:     id,
		label:  label,
		key:    key,
	}
}

func (p *openRouterProvider) ID() string { return p.id }

// orPrefix derives the ≤6-char row-label prefix from the provider id so the
// two OpenRouter accounts are distinguishable on the StickS3 screen (where
// rows appear without the provider name): "openrouter:main" → "ORmain",
// "openrouter:fallback" → "ORfbk".
func orPrefix(id string) string {
	if strings.HasSuffix(id, "main") {
		return "ORmain"
	}
	return "ORfbk"
}

func (p *openRouterProvider) block(status, msg, plan string, rows []snapshot.Row, fetchedAt int64) snapshot.Provider {
	return snapshot.Provider{
		ID:        p.id,
		Label:     p.label,
		Plan:      plan,
		Status:    status,
		Msg:       msg,
		FetchedAt: fetchedAt,
		Rows:      rows,
	}
}

func (p *openRouterProvider) Fetch(ctx context.Context, now time.Time) (snapshot.Provider, Outcome) {
	headers := map[string]string{
		"Authorization": "Bearer " + p.key,
	}

	var rows []snapshot.Row
	plan := ""

	// 1. Fetch credits (balance). On 401/403 the key can't read credits; skip
	//    the bal row and fall back to /key figures only.
	slog.Debug("openrouter: fetching credits", "id", p.id)
	resp, err := p.client.Do(ctx, "GET", openrouterCreditsURL, headers, nil)
	if err != nil {
		slog.Debug("openrouter: network error on credits", "err", err)
		return p.block("error", "offline", "", nil, now.Unix()), Outcome{}
	}

	if resp.Status == http.StatusOK {
		var cr openrouterCreditsResponse
		if err := json.Unmarshal(resp.Body, &cr); err != nil {
			slog.Debug("openrouter: credits parse error", "err", err)
		} else if cr.Data.TotalCredits > 0 {
			balLeft := cr.Data.TotalCredits - cr.Data.TotalUsage
			pct := int(math.Round(100 * cr.Data.TotalUsage / cr.Data.TotalCredits))
			rows = append(rows, snapshot.Row{
				K:       "bal",
				Label:   orPrefix(p.id) + " bal",
				Pct:     &pct,
				Txt:     format.Money(format.Cents(balLeft)),
				Tier:    format.Tier(&pct, "ok"),
				ResetAt: nil,
			})
		}
	} else if resp.Status != http.StatusUnauthorized && resp.Status != http.StatusForbidden {
		slog.Debug("openrouter: credits fetch non-200", "status", resp.Status)
	}
	// 401/403 on credits: skip bal, continue to /key.

	// 2. Fetch key (daily usage + plan + optional limit).
	slog.Debug("openrouter: fetching key", "id", p.id)
	resp2, err := p.client.Do(ctx, "GET", openrouterKeyURL, headers, nil)
	if err != nil {
		slog.Debug("openrouter: network error on key", "err", err)
		return p.block("error", "offline", plan, rows, now.Unix()), Outcome{}
	}

	switch {
	case resp2.Status == http.StatusUnauthorized || resp2.Status == http.StatusForbidden:
		return p.block("auth", "bad key", "", nil, now.Unix()), Outcome{}
	case resp2.Status != http.StatusOK:
		return p.block("error", fmt.Sprintf("http %d", resp2.Status), plan, nil, now.Unix()), Outcome{}
	}

	var kr openrouterKeyResponse
	if err := json.Unmarshal(resp2.Body, &kr); err != nil {
		slog.Debug("openrouter: key parse error", "err", err)
		return p.block("error", "parse error", plan, nil, now.Unix()), Outcome{}
	}

	plan = "paid"
	if kr.Data.IsFreeTier {
		plan = "free"
	}

	// day row: daily spend in USD, no pct (balance-style).
	dayCents := format.Cents(kr.Data.UsageDaily)
	rows = append(rows, snapshot.Row{
		K:       "day",
		Label:   orPrefix(p.id) + " day",
		Pct:     nil,
		Txt:     format.Money(dayCents),
		Tier:    "ok",
		ResetAt: nil,
	})

	// lim row: only when limit is non-null (and limit_remaining is too).
	if kr.Data.Limit != nil && kr.Data.LimitRemaining != nil {
		limit := *kr.Data.Limit
		remaining := *kr.Data.LimitRemaining
		limPct := 0
		if limit > 0 {
			limPct = int(math.Round(100 * (limit - remaining) / limit))
		}
		rows = append(rows, snapshot.Row{
			K:       "lim",
			Label:   orPrefix(p.id) + " lim",
			Pct:     &limPct,
			Txt:     format.Money(format.Cents(remaining)),
			Tier:    format.Tier(&limPct, "ok"),
			ResetAt: nil,
		})
	}

	return p.block("ok", "", plan, rows, now.Unix()), Outcome{}
}

// --- response types ---

type openrouterCreditsResponse struct {
	Data struct {
		TotalCredits float64 `json:"total_credits"`
		TotalUsage   float64 `json:"total_usage"`
	} `json:"data"`
}

type openrouterKeyResponse struct {
	Data struct {
		Label          string   `json:"label"`
		Limit          *float64 `json:"limit"`
		LimitRemaining *float64 `json:"limit_remaining"`
		UsageDaily     float64  `json:"usage_daily"`
		IsFreeTier     bool     `json:"is_free_tier"`
	} `json:"data"`
}
