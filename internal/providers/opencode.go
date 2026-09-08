package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"usaged/internal/format"
	"usaged/internal/httpx"
	"usaged/internal/snapshot"
)

const (
	opencodeID       = "opencode:go"
	opencodeLabel    = "OpenCode Go"
	opencodeUsageURL = "https://opencode.ai/zen/go/v1/usage"
)

// opencodeProvider implements Fetcher for the OpenCode Go usage endpoint.
// It fetches three rate-limit windows (rolling/weekly/monthly) and normalises
// them into snapshot rows with keys 5h/7d/30d.
type opencodeProvider struct {
	client *httpx.Client
	key    string
	loc    *time.Location
	alerts format.Alerts
}

// NewOpenCode creates a Fetcher that polls the OpenCode Go usage endpoint.
func NewOpenCode(client *httpx.Client, key string, loc *time.Location, alerts format.Alerts) Fetcher {
	return &opencodeProvider{
		client: client,
		key:    key,
		loc:    loc,
		alerts: alerts,
	}
}

func (p *opencodeProvider) ID() string { return opencodeID }

func (p *opencodeProvider) block(status, msg string, rows []snapshot.Row, fetchedAt int64) snapshot.Provider {
	return snapshot.Provider{
		ID:        opencodeID,
		Label:     opencodeLabel,
		Plan:      "plan",
		Kind:      "plan",
		Severity:  format.Severity(status, rows, p.alerts),
		Status:    status,
		Msg:       msg,
		FetchedAt: fetchedAt,
		Rows:      rows,
	}
}

func (p *opencodeProvider) Fetch(ctx context.Context, now time.Time) (snapshot.Provider, Outcome) {
	headers := map[string]string{
		"Authorization": "Bearer " + p.key,
	}

	slog.Debug("opencode: fetching usage")
	resp, err := p.client.Do(ctx, "GET", opencodeUsageURL, headers, nil)
	if err != nil {
		slog.Debug("opencode: network error", "err", err)
		msg := "api unreachable"
		if errors.Is(err, context.DeadlineExceeded) {
			msg = "api timeout"
		}
		return p.block("error", msg, nil, now.Unix()), Outcome{}
	}

	if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden {
		return p.block("auth", "bad key", nil, now.Unix()), Outcome{}
	}
	if resp.Status != http.StatusOK {
		return p.block("error", fmt.Sprintf("http %d", resp.Status), nil, now.Unix()), Outcome{}
	}

	var body opencodeUsageResponse
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		slog.Debug("opencode: parse error", "err", err)
		return p.block("error", "parse error", nil, now.Unix()), Outcome{}
	}

	rows := parseOpenCodeWindows(body, now, p.loc)
	return p.block("ok", "", rows, now.Unix()), Outcome{}
}

// opencodeUsageResponse is the top-level JSON returned by the OpenCode Go
// usage endpoint. The response has exactly three windows: rolling, weekly,
// monthly — each with status (string), percent (int) and resetsAt (RFC3339
// with milliseconds).
type opencodeUsageResponse struct {
	Usage struct {
		Rolling opencodeWindow `json:"rolling"`
		Weekly  opencodeWindow `json:"weekly"`
		Monthly opencodeWindow `json:"monthly"`
	} `json:"usage"`
}

type opencodeWindow struct {
	Status   string `json:"status"`
	Percent  int    `json:"percent"`
	ResetsAt string `json:"resetsAt"`
}

// parseOpenCodeWindows converts the three API windows into snapshot rows.
// rolling -> "5h", weekly -> "7d", monthly -> "30d".
// A window whose status is not "ok" gets tier "off" and a nil Pct rather
// than a fabricated number. A resetsAt parse failure leaves ResetAt nil.
func parseOpenCodeWindows(body opencodeUsageResponse, now time.Time, loc *time.Location) []snapshot.Row {
	windows := []struct {
		window opencodeWindow
		k      string
		label  string
	}{
		{body.Usage.Rolling, "5h", "OCgo 5h"},
		{body.Usage.Weekly, "7d", "OCgo 7d"},
		{body.Usage.Monthly, "30d", "OCgo 30d"},
	}

	var rows []snapshot.Row
	for _, w := range windows {
		var pct *int
		var resetAt *int64
		tier := "off"
		txt := "--"

		if w.window.Status == "ok" {
			pctVal := w.window.Percent
			pct = &pctVal
			tier = format.Tier(&pctVal, "ok")
		}

		if w.window.ResetsAt != "" {
			if rt, err := time.Parse(time.RFC3339, w.window.ResetsAt); err == nil {
				ua := rt.Unix()
				resetAt = &ua
				txt = format.ResetTxt(rt, now, loc)
			}
		}

		rows = append(rows, snapshot.Row{
			K:       w.k,
			Label:   w.label,
			Pct:     pct,
			Txt:     txt,
			Tier:    tier,
			ResetAt: resetAt,
		})
	}
	return rows
}
