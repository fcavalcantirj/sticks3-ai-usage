package creds

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// CodexCreds holds the fields needed to call the ChatGPT wham/usage API.
// The access token is never logged — callers should rely on String-like
// redaction or avoid printing the struct directly.
type CodexCreds struct {
	AccessToken string
	AccountID   string
	PlanType    string
	Exp         time.Time
	LastRefresh time.Time
}

// Internal JSON shape of ~/.codex/auth.json.
type codexAuthJSON struct {
	AuthMode     string `json:"auth_mode"`
	OPENAIAPIKey string `json:"OPENAI_API_KEY"`
	Tokens       struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
	LastRefresh string `json:"last_refresh"`
}

// decodeCodexJWT decodes the payload segment (second dot-segment) of a JWT.
// It strips padding and uses base64.RawURLEncoding, tolerating both padded
// and unpadded input.
func decodeCodexJWT(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("codex: malformed JWT (expected at least 2 segments, got %d)", len(parts))
	}
	payload := strings.TrimRight(parts[1], "=")
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("codex: decode JWT payload: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(data, &claims); err != nil {
		return nil, fmt.Errorf("codex: parse JWT claims: %w", err)
	}
	return claims, nil
}

// ReadCodex reads Codex/ChatGPT credentials from an auth.json file.
// If path is empty, it defaults to $HOME/.codex/auth.json.
//
// Returns ErrNotLoggedIn when the file is missing, auth_mode is not "chatgpt",
// or the access token is empty. Returns ErrExpired when the token's exp claim
// is in the past — the struct is still returned so callers can report the
// status, but the token must NOT be used.
//
// In fixture mode the caller passes the full path to the fixture file, e.g.
// ReadCodex("testdata/fixtures/codex_auth.json").
func ReadCodex(path string) (CodexCreds, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return CodexCreds{}, ErrNotLoggedIn
		}
		path = home + "/.codex/auth.json"
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return CodexCreds{}, ErrNotLoggedIn
	}

	var auth codexAuthJSON
	if err := json.Unmarshal(data, &auth); err != nil {
		return CodexCreds{}, fmt.Errorf("codex: parse auth.json: %w", err)
	}

	if auth.AuthMode != "chatgpt" || auth.Tokens.AccessToken == "" {
		return CodexCreds{}, ErrNotLoggedIn
	}

	claims, err := decodeCodexJWT(auth.Tokens.AccessToken)
	if err != nil {
		return CodexCreds{}, err
	}

	var exp time.Time
	if expNum, ok := claims["exp"].(float64); ok {
		exp = time.Unix(int64(expNum), 0)
	}

	var lastRefresh time.Time
	if auth.LastRefresh != "" {
		if t, err := time.Parse(time.RFC3339, auth.LastRefresh); err == nil {
			lastRefresh = t
		}
	}

	// account_id: prefer tokens.account_id, fall back to JWT claim
	accountID := auth.Tokens.AccountID
	if accountID == "" {
		if authClaim, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
			if cid, ok := authClaim["chatgpt_account_id"].(string); ok {
				accountID = cid
			}
		}
	}

	// plan_type from JWT claim (optional)
	planType := ""
	if authClaim, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if pt, ok := authClaim["chatgpt_plan_type"].(string); ok {
			planType = pt
		}
	}

	creds := CodexCreds{
		AccessToken: auth.Tokens.AccessToken,
		AccountID:   accountID,
		PlanType:    planType,
		Exp:         exp,
		LastRefresh: lastRefresh,
	}

	if !creds.Exp.After(time.Now()) {
		return creds, ErrExpired
	}

	return creds, nil
}
