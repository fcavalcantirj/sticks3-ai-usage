package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"usaged/internal/creds"
	"usaged/internal/format"
	"usaged/internal/httpx"
	"usaged/internal/snapshot"
)

const (
	claudeID    = "claude"
	claudeLabel = "Claude"
	claudeURL   = "https://api.anthropic.com/api/oauth/usage"
	claudeBeta  = "oauth-2025-04-20"
)

// claudeProvider implements Fetcher for the Claude Code OAuth usage endpoint.
type claudeProvider struct {
	client   *httpx.Client
	runner   creds.Runner
	username string
	loc      *time.Location
	ua       string
	uaSet    bool
}

// NewClaude creates a Fetcher that polls the Claude Code usage endpoint.
func NewClaude(client *httpx.Client, runner creds.Runner, username string, loc *time.Location) Fetcher {
	return &claudeProvider{
		client:   client,
		runner:   runner,
		username: username,
		loc:      loc,
	}
}

func (p *claudeProvider) ID() string { return claudeID }

// stripClaudeTier removes the "default_claude_" prefix from the rate-limit
// tier returned by the Keychain item.
func stripClaudeTier(tier string) string {
	return strings.TrimPrefix(tier, "default_claude_")
}

// claudeProviderBlock is a convenience builder for the non-rows fields.
func (p *claudeProvider) block(status, msg, plan string, rows []snapshot.Row, fetchedAt int64) snapshot.Provider {
	return snapshot.Provider{
		ID:        claudeID,
		Label:     claudeLabel,
		Plan:      plan,
		Status:    status,
		Msg:       msg,
		FetchedAt: fetchedAt,
		Rows:      rows,
	}
}

func (p *claudeProvider) Fetch(ctx context.Context, now time.Time) (snapshot.Provider, Outcome) {
	// Read credentials fresh on every poll.
	c, err := creds.ReadClaude(ctx, p.runner, p.username)

	// Both auth errors suppress the HTTP call.
	if errors.Is(err, creds.ErrNotLoggedIn) {
		slog.Debug("claude: not logged in")
		return p.block("auth", "run claude", stripClaudeTier(c.RateLimitTier), nil, now.Unix()), Outcome{}
	}
	if errors.Is(err, creds.ErrExpired) {
		slog.Debug("claude: token expired")
		return p.block("auth", "run claude", stripClaudeTier(c.RateLimitTier), nil, now.Unix()), Outcome{}
	}
	if err != nil {
		slog.Debug("claude: cred read error", "err", err)
		return p.block("error", "offline", "", nil, now.Unix()), Outcome{}
	}

	// Cache the User-Agent on first use.
	if !p.uaSet {
		p.ua = creds.ClaudeUserAgent(ctx, p.runner)
		p.uaSet = true
		slog.Debug("claude: cached user-agent")
	}

	plan := stripClaudeTier(c.RateLimitTier)

	headers := map[string]string{
		"Authorization":  "Bearer " + c.AccessToken,
		"anthropic-beta": claudeBeta,
		"User-Agent":     p.ua,
	}

	slog.Debug("claude: fetching usage")
	resp, err := p.client.Do(ctx, "GET", claudeURL, headers, nil)
	if err != nil {
		slog.Debug("claude: network error", "err", err)
		return p.block("error", "offline", plan, nil, now.Unix()), Outcome{}
	}

	slog.Debug("claude: response", "status", resp.Status)

	switch {
	case resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden:
		return p.block("auth", "run claude", plan, nil, now.Unix()), Outcome{}
	case resp.Status == http.StatusTooManyRequests:
		cooldown, ok := httpx.RetryAfter(resp.Header, now)
		if !ok {
			cooldown = 300 * time.Second
		}
		until := now.Add(cooldown)
		return p.block("error", fmt.Sprintf("429 until %s", until.In(p.loc).Format("15:04")), plan, nil, now.Unix()),
			Outcome{CooldownUntil: until}
	case resp.Status != http.StatusOK:
		return p.block("error", fmt.Sprintf("http %d", resp.Status), plan, nil, now.Unix()), Outcome{}
	}

	// Parse the 200 response body.
	var body claudeUsageResponse
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		slog.Debug("claude: parse error", "err", err)
		return p.block("error", fmt.Sprintf("http %d", resp.Status), plan, nil, now.Unix()), Outcome{}
	}

	var rows []snapshot.Row
	if len(body.Limits) > 0 {
		rows = parseClaudeLimits(body.Limits, now, p.loc)
	}
	// Fallback to five_hour / seven_day when limits is absent or produced no rows.
	if len(rows) == 0 {
		rows = parseClaudeFallback(body, now, p.loc)
	}

	return p.block("ok", "", plan, rows, now.Unix()), Outcome{}
}

// --- response parsing ---

type claudeUsageResponse struct {
	Limits   []claudeLimit `json:"limits"`
	FiveHour claudeWindow  `json:"five_hour"`
	SevenDay claudeWindow  `json:"seven_day"`
}

type claudeLimit struct {
	Kind     string       `json:"kind"`
	Percent  float64      `json:"percent"`
	ResetsAt string       `json:"resets_at"`
	Scope    *claudeScope `json:"scope"`
}

type claudeScope struct {
	Model struct {
		ID          *string `json:"id"`
		DisplayName string  `json:"display_name"`
	} `json:"model"`
}

type claudeWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

// parseClaudeLimits converts the limits[] array into snapshot rows.
func parseClaudeLimits(limits []claudeLimit, now time.Time, loc *time.Location) []snapshot.Row {
	var rows []snapshot.Row
	for _, l := range limits {
		resetTime, err := time.Parse(time.RFC3339Nano, l.ResetsAt)
		if err != nil {
			slog.Debug("claude: parse resets_at error", "err", err)
			continue
		}
		resetAt := resetTime.Unix()
		pct := int(l.Percent)
		tier := format.Tier(&pct, "ok")
		txt := format.ResetTxt(resetTime, now, loc)

		var row snapshot.Row
		switch l.Kind {
		case "session":
			row = snapshot.Row{K: "5h", Label: "CLAUDE 5h", Pct: &pct, Txt: txt, Tier: tier, ResetAt: &resetAt}
		case "weekly_all":
			row = snapshot.Row{K: "7d", Label: "CLAUDE 7d", Pct: &pct, Txt: txt, Tier: tier, ResetAt: &resetAt}
		case "weekly_scoped":
			displayName := ""
			if l.Scope != nil {
				displayName = l.Scope.Model.DisplayName
			}
			k := "7d:" + displayName
			label := strings.ToUpper(displayName)
			if len(label) > 6 {
				label = label[:6]
			}
			label += " 7d"
			row = snapshot.Row{K: k, Label: label, Pct: &pct, Txt: txt, Tier: tier, ResetAt: &resetAt}
		default:
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

// parseClaudeFallback converts five_hour / seven_day into rows when limits[]
// is absent or empty.
func parseClaudeFallback(body claudeUsageResponse, now time.Time, loc *time.Location) []snapshot.Row {
	var rows []snapshot.Row

	// five_hour
	pct := int(math.Round(body.FiveHour.Utilization))
	resetTime, _ := time.Parse(time.RFC3339Nano, body.FiveHour.ResetsAt)
	resetAt := resetTime.Unix()
	tier := format.Tier(&pct, "ok")
	txt := format.ResetTxt(resetTime, now, loc)
	rows = append(rows, snapshot.Row{K: "5h", Label: "CLAUDE 5h", Pct: &pct, Txt: txt, Tier: tier, ResetAt: &resetAt})

	// seven_day
	pct7 := int(math.Round(body.SevenDay.Utilization))
	resetTime7, _ := time.Parse(time.RFC3339Nano, body.SevenDay.ResetsAt)
	resetAt7 := resetTime7.Unix()
	tier7 := format.Tier(&pct7, "ok")
	txt7 := format.ResetTxt(resetTime7, now, loc)
	rows = append(rows, snapshot.Row{K: "7d", Label: "CLAUDE 7d", Pct: &pct7, Txt: txt7, Tier: tier7, ResetAt: &resetAt7})

	return rows
}
