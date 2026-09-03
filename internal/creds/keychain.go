package creds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Sentinel errors. Callers check these with errors.Is to decide how to
// mark the provider status (auth vs. error).
var (
	ErrNotLoggedIn = errors.New("claude: not logged in")
	ErrExpired     = errors.New("claude: token expired")
)

// ClaudeCreds holds the fields needed to call the Anthropic OAuth usage API.
// The refresh token is deliberately NOT included — it is never read by usaged.
type ClaudeCreds struct {
	AccessToken      string
	ExpiresAt        time.Time
	SubscriptionType string
	RateLimitTier    string
}

// Internal JSON shape of the Keychain item "Claude Code-credentials".
type keychainJSON struct {
	ClaudeAiOauth struct {
		AccessToken           string   `json:"accessToken"`
		RefreshToken          string   `json:"refreshToken"`
		ExpiresAt             int64    `json:"expiresAt"` // epoch milliseconds
		RefreshTokenExpiresAt int64    `json:"refreshTokenExpiresAt"`
		Scopes                []string `json:"scopes"`
		SubscriptionType      string   `json:"subscriptionType"`
		RateLimitTier         string   `json:"rateLimitTier"`
	} `json:"claudeAiOauth"`
}

// ReadClaude reads the Claude Code Keychain item via Runner r.
// Returns ErrNotLoggedIn when the Keychain item is missing (security exit 44
// or stderr contains "could not be found"). Returns ErrExpired when the
// access token has expired — the struct is still returned so callers can
// report the status, but the token must NOT be used.
func ReadClaude(ctx context.Context, r Runner, username string) (ClaudeCreds, error) {
	stdout, stderr, err := r.Run(ctx, "security",
		"find-generic-password", "-s", "Claude Code-credentials",
		"-a", username, "-w")

	if err != nil {
		stderrStr := string(stderr)
		if strings.Contains(stderrStr, "could not be found") ||
			strings.Contains(err.Error(), "exit status 44") {
			return ClaudeCreds{}, ErrNotLoggedIn
		}
		return ClaudeCreds{}, fmt.Errorf("security command failed: %w", err)
	}

	var kc keychainJSON
	if err := json.Unmarshal(stdout, &kc); err != nil {
		return ClaudeCreds{}, fmt.Errorf("parse keychain JSON: %w", err)
	}

	creds := ClaudeCreds{
		AccessToken:      kc.ClaudeAiOauth.AccessToken,
		ExpiresAt:        time.UnixMilli(kc.ClaudeAiOauth.ExpiresAt),
		SubscriptionType: kc.ClaudeAiOauth.SubscriptionType,
		RateLimitTier:    kc.ClaudeAiOauth.RateLimitTier,
	}

	if !creds.ExpiresAt.After(time.Now()) {
		return creds, ErrExpired
	}

	return creds, nil
}

// String implements fmt.Stringer. It redacts the access token so that the
// struct is safe to log or stringify — per house rule, never print token
// values; log only lengths and a 7-char prefix.
func (c ClaudeCreds) String() string {
	prefix := c.AccessToken
	if len(prefix) > 7 {
		prefix = prefix[:7]
	}
	return fmt.Sprintf("ClaudeCreds{AccessToken:%s(len=%d),ExpiresAt:%v,SubscriptionType:%s,RateLimitTier:%s}",
		prefix, len(c.AccessToken), c.ExpiresAt, c.SubscriptionType, c.RateLimitTier)
}

var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+`)

// ClaudeUserAgent builds the User-Agent header for the Anthropic OAuth usage
// API. It runs `claude --version` and extracts the first whitespace-delimited
// token; if it matches ^\d+\.\d+\.\d+, the result is "claude-code/<version>",
// otherwise "claude-code/2.1.0".
// The caller should cache this for the process lifetime.
func ClaudeUserAgent(ctx context.Context, r Runner) string {
	stdout, _, err := r.Run(ctx, "claude", "--version")
	if err != nil {
		return "claude-code/2.1.0"
	}

	fields := strings.Fields(strings.TrimSpace(string(stdout)))
	if len(fields) == 0 {
		return "claude-code/2.1.0"
	}

	version := fields[0]
	if versionPattern.MatchString(version) {
		return "claude-code/" + version
	}

	return "claude-code/2.1.0"
}
