package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"usaged/internal/format"
	"usaged/internal/snapshot"
)

// claudeStatuslineTTL is how long a statusline file is considered fresh.
const claudeStatuslineTTL = 10 * time.Minute

// statuslineFile is the default path written by scripts/statusline-tee.sh.
const statuslineFile = ".local/state/usaged/claude-statusline.json"

// claudeStatuslineProvider reads Claude rate-limit data from the
// statusline-tee file (written by scripts/statusline-tee.sh on every Claude
// Code statusline invocation). When the file is missing, stale, or parse
// fails, it falls back to the OAuth fetcher if one is configured.
type claudeStatuslineProvider struct {
	sourceFile string
	fallback   Fetcher
	loc        *time.Location
}

// NewClaudeStatusline returns a Fetcher that prefers the statusline file
// when it is younger than claudeStatuslineTTL, falling back to `fallback`
// (an OAuth claudeProvider) when it is stale or absent. If fallback is nil
// and the file is unusable, an error block is returned.
func NewClaudeStatusline(sourceFile string, fallback Fetcher, loc *time.Location) Fetcher {
	return &claudeStatuslineProvider{
		sourceFile: sourceFile,
		fallback:   fallback,
		loc:        loc,
	}
}

func (p *claudeStatuslineProvider) ID() string { return claudeID }

func (p *claudeStatuslineProvider) Fetch(ctx context.Context, now time.Time) (snapshot.Provider, Outcome) {
	if p.sourceFile == "" {
		if p.fallback != nil {
			return p.fallback.Fetch(ctx, now)
		}
		return claudeBlock("error", "no claude source", "", nil, now.Unix()), Outcome{}
	}

	data, err := os.ReadFile(p.sourceFile)
	if err != nil {
		slog.Debug("claude-statusline: file read", "err", err)
		if p.fallback != nil {
			return p.fallback.Fetch(ctx, now)
		}
		return claudeBlock("error", "no statusline data", "", nil, now.Unix()), Outcome{}
	}

	fi, err := os.Stat(p.sourceFile)
	if err != nil || now.Sub(fi.ModTime()) > claudeStatuslineTTL {
		slog.Debug("claude-statusline: file stale or stat failed", "err", err)
		if p.fallback != nil {
			return p.fallback.Fetch(ctx, now)
		}
		return claudeBlock("error", "statusline stale", "", nil, now.Unix()), Outcome{}
	}

	body, err := parseStatuslineData(data)
	if err != nil {
		slog.Debug("claude-statusline: parse error", "err", err)
		if p.fallback != nil {
			return p.fallback.Fetch(ctx, now)
		}
		return claudeBlock("error", "parse error", "", nil, now.Unix()), Outcome{}
	}

	rows := parseStatuslineRows(body, now, p.loc)
	plan := body.PlanType
	return claudeBlock("ok", "", plan, rows, now.Unix()), Outcome{}
}

// --- statusline file types ---

// statuslineData is the content of ~/.local/state/usaged/claude-statusline.json.
// It is the rate_limits block from Claude Code's statusline stdin, plus an
// optional plan field written by the tee script.
type statuslineData struct {
	RateLimits statuslineRateLimits `json:"rate_limits"`
	PlanType   string               `json:"plan_type,omitempty"`
}

type statuslineRateLimits struct {
	FiveHour statuslineWindow `json:"five_hour"`
	SevenDay statuslineWindow `json:"seven_day"`
}

type statuslineWindow struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"` // epoch seconds
}

// parseStatuslineData unmarshals the statusline file.
func parseStatuslineData(data []byte) (statuslineData, error) {
	var body statuslineData
	if err := json.Unmarshal(data, &body); err != nil {
		return statuslineData{}, fmt.Errorf("unmarshal statusline: %w", err)
	}
	return body, nil
}

// parseStatuslineRows converts the statusline data into snapshot rows,
// mirroring the format produced by parseClaudeFallback.
func parseStatuslineRows(body statuslineData, now time.Time, loc *time.Location) []snapshot.Row {
	var rows []snapshot.Row

	// five_hour
	pct5 := int(body.RateLimits.FiveHour.UsedPercentage)
	resetAt5 := body.RateLimits.FiveHour.ResetsAt
	tier5 := format.Tier(&pct5, "ok")
	txt5 := format.ResetTxt(time.Unix(resetAt5, 0), now, loc)
	rows = append(rows, snapshot.Row{K: "5h", Label: "CLAUDE 5h", Pct: &pct5, Txt: txt5, Tier: tier5, ResetAt: &resetAt5})

	// seven_day
	pct7 := int(body.RateLimits.SevenDay.UsedPercentage)
	resetAt7 := body.RateLimits.SevenDay.ResetsAt
	tier7 := format.Tier(&pct7, "ok")
	txt7 := format.ResetTxt(time.Unix(resetAt7, 0), now, loc)
	rows = append(rows, snapshot.Row{K: "7d", Label: "CLAUDE 7d", Pct: &pct7, Txt: txt7, Tier: tier7, ResetAt: &resetAt7})

	return rows
}
