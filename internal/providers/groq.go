package providers

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"usaged/internal/format"
	"usaged/internal/httpx"
	"usaged/internal/snapshot"
)

const (
	groqID         = "groq"
	groqLabel      = "Groq"
	groqModelsURL  = "https://api.groq.com/openai/v1/models"
	groqChatURL    = "https://api.groq.com/openai/v1/chat/completions"
	groqProbeModel = "openai/gpt-oss-20b"
)

// groqProvider implements Fetcher for the Groq API. In default mode it only
// validates the API key via GET /models. In probe mode (cfg.GroqProbe) it
// additionally POSTs a 1-token completion and parses the rate-limit headers
// into rpd/tpm rows.
type groqProvider struct {
	client *httpx.Client
	key    string
	probe  bool
}

// NewGroq creates a Fetcher for the Groq provider. When probe is true the
// provider additionally POSTs a 1-token completion to read rate-limit
// headroom from the response headers.
func NewGroq(client *httpx.Client, key string, probe bool) Fetcher {
	return &groqProvider{
		client: client,
		key:    key,
		probe:  probe,
	}
}

func (p *groqProvider) ID() string { return groqID }

func (p *groqProvider) block(status, msg, plan string, rows []snapshot.Row, fetchedAt int64) snapshot.Provider {
	// Groq defaults to kind "free" (no per-request cost to the user on the
	// free tier); if the probe ever reports a paid plan, the Fetch method
	// overrides this to "credit".
	kind := "free"
	severity := format.Severity(status, rows)
	if plan == "paid" {
		kind = "credit"
	}
	return snapshot.Provider{
		ID:        groqID,
		Label:     groqLabel,
		Plan:      plan,
		Kind:      kind,
		Severity:  severity,
		Status:    status,
		Msg:       msg,
		FetchedAt: fetchedAt,
		Rows:      rows,
	}
}

func (p *groqProvider) Fetch(ctx context.Context, now time.Time) (snapshot.Provider, Outcome) {
	headers := map[string]string{
		"Authorization": "Bearer " + p.key,
	}

	// GET /models proves the key (200 → valid, 401 → bad key).
	slog.Debug("groq: fetching models")
	resp, err := p.client.Do(ctx, "GET", groqModelsURL, headers, nil)
	if err != nil {
		slog.Debug("groq: network error", "err", err)
		return p.block("error", "offline", "", nil, now.Unix()), Outcome{}
	}

	if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden {
		return p.block("auth", "bad key", "", nil, now.Unix()), Outcome{}
	}
	if resp.Status != http.StatusOK {
		return p.block("error", fmt.Sprintf("http %d", resp.Status), "", nil, now.Unix()), Outcome{}
	}

	plan := "on_demand"

	if !p.probe {
		// Default mode: just one row showing the key is valid.
		rows := []snapshot.Row{
			{K: "key", Label: "GROQ key", Pct: nil, Txt: "ok", Tier: "ok", ResetAt: nil},
		}
		return p.block("ok", "", plan, rows, now.Unix()), Outcome{}
	}

	// Probe mode: POST a 1-token completion and parse the rate-limit headers.
	slog.Debug("groq: probing rate limits")
	body := fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"."}],"max_tokens":1}`, groqProbeModel)
	resp2, err := p.client.Do(ctx, "POST", groqChatURL, headers, []byte(body))
	if err != nil {
		slog.Debug("groq: probe network error", "err", err)
		return p.block("error", "offline", plan, nil, now.Unix()), Outcome{}
	}
	if resp2.Status != http.StatusOK {
		return p.block("error", fmt.Sprintf("http %d", resp2.Status), plan, nil, now.Unix()), Outcome{}
	}

	rows := parseGroqRateLimits(resp2.Header)
	return p.block("ok", "", plan, rows, now.Unix()), Outcome{}
}

// parseGroqRateLimits parses x-ratelimit-* headers from a Groq response into
// snapshot rows. rpd = requests per day, tpm = tokens per minute.
//
// Header semantics (verified by live probing, 31 endpoints): the x-ratelimit-
// limit/remaining-requests headers are RPD (requests per day, a leaky bucket)
// and the x-ratelimit-limit/remaining-tokens headers are TPM (tokens per minute,
// also a leaky bucket). The reset-* headers are REFILL time (seconds until the
// bucket refills), NOT a window boundary — so do not treat them as a daily reset
// time.
func parseGroqRateLimits(h http.Header) []snapshot.Row {
	var rows []snapshot.Row

	limitReq := parseHeaderInt(h.Get("x-ratelimit-limit-requests"))
	remainingReq := parseHeaderInt(h.Get("x-ratelimit-remaining-requests"))
	if limitReq > 0 {
		pct := int(math.Round(100 * float64(limitReq-remainingReq) / float64(limitReq)))
		rows = append(rows, snapshot.Row{
			K:       "rpd",
			Label:   "GROQ rpd",
			Pct:     &pct,
			Txt:     shortenNum(remainingReq),
			Tier:    format.Tier(&pct, "ok"),
			ResetAt: nil,
		})
	}

	limitTok := parseHeaderInt(h.Get("x-ratelimit-limit-tokens"))
	remainingTok := parseHeaderInt(h.Get("x-ratelimit-remaining-tokens"))
	if limitTok > 0 {
		pct := int(math.Round(100 * float64(limitTok-remainingTok) / float64(limitTok)))
		rows = append(rows, snapshot.Row{
			K:       "tpm",
			Label:   "GROQ tpm",
			Pct:     &pct,
			Txt:     shortenNum(remainingTok),
			Tier:    format.Tier(&pct, "ok"),
			ResetAt: nil,
		})
	}

	return rows
}

// parseHeaderInt parses an integer header value, returning 0 on error.
func parseHeaderInt(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// shortenNum shortens large numbers for display: 499999 → "500k".
func shortenNum(n int) string {
	if n >= 1000 {
		return strconv.Itoa((n+500)/1000) + "k"
	}
	return strconv.Itoa(n)
}
