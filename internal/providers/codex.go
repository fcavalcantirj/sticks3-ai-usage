package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"usaged/internal/creds"
	"usaged/internal/format"
	"usaged/internal/httpx"
	"usaged/internal/snapshot"
)

const (
	codexID    = "codex"
	codexLabel = "ChatGPT"
	codexURL   = "https://chatgpt.com/backend-api/wham/usage"
)

type codexProvider struct {
	client   *httpx.Client
	authPath string
	loc      *time.Location
}

func NewCodex(client *httpx.Client, authPath string, loc *time.Location) Fetcher {
	return &codexProvider{
		client:   client,
		authPath: authPath,
		loc:      loc,
	}
}

func (p *codexProvider) ID() string { return codexID }

// codexBlock returns a canonical codex snapshot.Provider block. Shared by the
// HTTP (wham/usage) and CLI (app-server) fetchers so they produce identical
// output fields.
func codexBlock(status, msg, plan string, rows []snapshot.Row, fetchedAt int64) snapshot.Provider {
	return snapshot.Provider{
		ID:        codexID,
		Label:     codexLabel,
		Plan:      plan,
		Kind:      "plan",
		Severity:  format.Severity(status, rows),
		Status:    status,
		Msg:       msg,
		FetchedAt: fetchedAt,
		Rows:      rows,
	}
}

func (p *codexProvider) Fetch(ctx context.Context, now time.Time) (snapshot.Provider, Outcome) {
	c, err := creds.ReadCodex(p.authPath)

	if errors.Is(err, creds.ErrNotLoggedIn) {
		slog.Debug("codex: not logged in")
		return codexBlock("auth", "run codex", c.PlanType, nil, now.Unix()), Outcome{}
	}
	if errors.Is(err, creds.ErrExpired) {
		slog.Debug("codex: token expired")
		return codexBlock("auth", "run codex", c.PlanType, nil, now.Unix()), Outcome{}
	}
	if err != nil {
		slog.Debug("codex: cred read error", "err", err)
		return codexBlock("error", "cred read", "", nil, now.Unix()), Outcome{}
	}

	plan := c.PlanType

	headers := map[string]string{
		"Authorization":      "Bearer " + c.AccessToken,
		"ChatGPT-Account-Id": c.AccountID,
		"User-Agent":         "codex-cli",
	}

	slog.Debug("codex: fetching usage")
	resp, err := p.client.Do(ctx, "GET", codexURL, headers, nil)
	if err != nil {
		slog.Debug("codex: network error", "err", err)
		msg := "api unreachable"
		if errors.Is(err, context.DeadlineExceeded) {
			msg = "api timeout"
		}
		return codexBlock("error", msg, plan, nil, now.Unix()), Outcome{}
	}

	slog.Debug("codex: response", "status", resp.Status)

	switch {
	case resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden:
		return codexBlock("auth", "run codex", plan, nil, now.Unix()), Outcome{}
	case resp.Status == http.StatusTooManyRequests:
		cooldown, ok := httpx.RetryAfter(resp.Header, now)
		if !ok {
			cooldown = 300 * time.Second
		}
		until := now.Add(cooldown)
		return codexBlock("error", fmt.Sprintf("429 until %s", until.In(p.loc).Format("15:04")), plan, nil, now.Unix()),
			Outcome{CooldownUntil: until}
	case resp.Status != http.StatusOK:
		return codexBlock("error", fmt.Sprintf("http %d", resp.Status), plan, nil, now.Unix()), Outcome{}
	}

	var body codexUsageResponse
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		slog.Debug("codex: parse error", "err", err)
		return codexBlock("error", fmt.Sprintf("http %d", resp.Status), plan, nil, now.Unix()), Outcome{}
	}

	rows := parseCodexRows(body, now, p.loc)
	return codexBlock("ok", "", body.PlanType, rows, now.Unix()), Outcome{}
}

// --- response parsing ---

type codexUsageResponse struct {
	PlanType     string            `json:"plan_type"`
	RateLimit    codexRateLimit    `json:"rate_limit"`
	Credits      codexCredits      `json:"credits"`
	ResetCredits codexResetCredits `json:"rate_limit_reset_credits"`
}

type codexRateLimit struct {
	PrimaryWindow   codexWindow `json:"primary_window"`
	SecondaryWindow codexWindow `json:"secondary_window"`
}

type codexWindow struct {
	UsedPercent        int   `json:"used_percent"`
	LimitWindowSeconds int   `json:"limit_window_seconds"`
	ResetAt            int64 `json:"reset_at"`
}

type codexCredits struct {
	HasCredits bool   `json:"has_credits"`
	Unlimited  bool   `json:"unlimited"`
	Balance    string `json:"balance"`
}

// codexResetCredits is the rate_limit_reset_credits block: when
// available_count > 0, ChatGPT owes the user a free reset of their daily
// and 5-hour usage counters. We only READ it — never POST to consume.
type codexResetCredits struct {
	AvailableCount           int `json:"available_count"`
	ApplicableAvailableCount int `json:"applicable_available_count"`
}

// parseCodexRows converts the wham/usage response into snapshot rows.
func parseCodexRows(body codexUsageResponse, now time.Time, loc *time.Location) []snapshot.Row {
	var rows []snapshot.Row

	// Collect both windows; identification is by limit_window_seconds,
	// never by primary/secondary position. Sort by seconds so 5h always
	// precedes 7d regardless of which window is primary.
	windows := []codexWindow{body.RateLimit.PrimaryWindow, body.RateLimit.SecondaryWindow}
	sort.Slice(windows, func(i, j int) bool {
		return windows[i].LimitWindowSeconds < windows[j].LimitWindowSeconds
	})

	for _, w := range windows {
		if w.LimitWindowSeconds == 0 {
			continue
		}

		var k, label string
		switch w.LimitWindowSeconds {
		case 18000:
			k = "5h"
			label = "GPT 5h"
		case 604800:
			k = "7d"
			label = "GPT 7d"
		default:
			k = fmt.Sprintf("%ds", w.LimitWindowSeconds)
			label = fmt.Sprintf("GPT %dh", w.LimitWindowSeconds/3600)
		}

		pct := w.UsedPercent
		resetAt := w.ResetAt
		resetTime := time.Unix(resetAt, 0)
		txt := format.ResetTxt(resetTime, now, loc)
		tier := format.Tier(&pct, "ok")

		rows = append(rows, snapshot.Row{
			K:       k,
			Label:   label,
			Pct:     &pct,
			Txt:     txt,
			Tier:    tier,
			ResetAt: &resetAt,
		})
	}

	// Credits balance row. ChatGPT balance is a CREDIT COUNT, not dollars
	// (chatgpt.com Settings shows e.g. '123 credits left'). The API returns
	// the balance as a decimal string; round to the nearest whole credit,
	// keeping one decimal only when below 10.
	if body.Credits.HasCredits && !body.Credits.Unlimited {
		balance, err := strconv.ParseFloat(body.Credits.Balance, 64)
		if err != nil {
			slog.Debug("codex: parse balance error", "err", err)
			balance = 0
		}
		rows = append(rows, snapshot.Row{
			K:       "bal",
			Label:   "GPT cr",
			Pct:     nil,
			Txt:     format.Credits(balance),
			Tier:    "ok",
			ResetAt: nil,
		})
	}

	// Reset-credits row: ChatGPT owes a free usage reset when
	// available_count > 0. The label is 'GPT rst'; txt is '<n> reset' or
	// '<n> resets'. NEVER POST to the consume endpoint.
	if body.ResetCredits.AvailableCount > 0 {
		count := body.ResetCredits.AvailableCount
		resetTxt := "reset"
		if count != 1 {
			resetTxt = "resets"
		}
		rows = append(rows, snapshot.Row{
			K:       "rst",
			Label:   "GPT rst",
			Pct:     nil,
			Txt:     fmt.Sprintf("%d %s", count, resetTxt),
			Tier:    "ok",
			ResetAt: nil,
		})
	}

	return rows
}
