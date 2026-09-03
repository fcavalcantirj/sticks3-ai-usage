package creds

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCodexAuth writes a minimal auth.json to a temp dir and returns the path.
func writeCodexAuth(t *testing.T, auth map[string]any) string {
	t.Helper()
	data, err := json.Marshal(auth)
	if err != nil {
		t.Fatalf("marshal auth: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}
	return path
}

func TestCodexMakeJWTValid(t *testing.T) {
	claims := map[string]any{
		"exp": float64(time.Now().Add(time.Hour).Unix()),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_plan_type":  "plus",
			"chatgpt_account_id": "acct-claim-id",
		},
	}
	token := MakeJWT(claims)

	path := writeCodexAuth(t, map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"access_token":  token,
			"account_id":    "acct-direct-id",
			"refresh_token": "rt.1.test",
		},
	})

	creds, err := ReadCodex(path)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if creds.PlanType != "plus" {
		t.Errorf("PlanType = %q, want plus", creds.PlanType)
	}
	if creds.AccountID != "acct-direct-id" {
		t.Errorf("AccountID = %q, want acct-direct-id", creds.AccountID)
	}
	if creds.AccessToken != token {
		t.Errorf("AccessToken mismatch")
	}
}

func TestCodexMakeJWTExpired(t *testing.T) {
	claims := map[string]any{
		"exp": float64(time.Now().Add(-time.Second).Unix()),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_plan_type": "plus",
		},
	}
	token := MakeJWT(claims)

	path := writeCodexAuth(t, map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"access_token": token,
			"account_id":   "acct-id",
		},
	})

	creds, err := ReadCodex(path)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("expected ErrExpired, got: %v", err)
	}
	if creds.PlanType != "plus" {
		t.Errorf("PlanType should still be populated on expiry: got %q", creds.PlanType)
	}
}

func TestCodexWrongAuthMode(t *testing.T) {
	path := writeCodexAuth(t, map[string]any{
		"auth_mode": "apikey",
		"tokens": map[string]any{
			"access_token": "some.token.here",
			"account_id":   "acct-id",
		},
	})

	_, err := ReadCodex(path)
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("expected ErrNotLoggedIn, got: %v", err)
	}
}

func TestCodexAccountIDFallback(t *testing.T) {
	claims := map[string]any{
		"exp": float64(time.Now().Add(time.Hour).Unix()),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_plan_type":  "plus",
			"chatgpt_account_id": "fallback-acct-id",
		},
	}
	token := MakeJWT(claims)

	// tokens.account_id is empty — should fall back to the JWT claim
	path := writeCodexAuth(t, map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"access_token": token,
			"account_id":   "",
		},
	})

	creds, err := ReadCodex(path)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if creds.AccountID != "fallback-acct-id" {
		t.Errorf("AccountID = %q, want fallback-acct-id", creds.AccountID)
	}
}

func TestCodexMalformedJWT(t *testing.T) {
	path := writeCodexAuth(t, map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"access_token": "not-a-jwt",
			"account_id":   "acct-id",
		},
	})

	_, err := ReadCodex(path)
	if err == nil {
		t.Fatal("expected error for malformed JWT, got nil")
	}
	if errors.Is(err, ErrNotLoggedIn) || errors.Is(err, ErrExpired) {
		t.Errorf("malformed JWT should return a parse error, not sentinel: got %v", err)
	}
}

func TestCodexFixtureFile(t *testing.T) {
	credPath := "testdata/fixtures/codex_auth.json"
	if _, err := os.Stat(credPath); err != nil {
		credPath = "../../testdata/fixtures/codex_auth.json"
	}

	creds, err := ReadCodex(credPath)
	if err != nil {
		t.Fatalf("expected no error reading fixture, got: %v", err)
	}
	if creds.PlanType != "plus" {
		t.Errorf("PlanType = %q, want plus", creds.PlanType)
	}
	if !creds.Exp.After(time.Now()) {
		t.Errorf("fixture token should not be expired: Exp = %v", creds.Exp)
	}
	if creds.AccountID != "00000000-0000-4000-8000-000000000000" {
		t.Errorf("AccountID = %q, want placeholder UUID", creds.AccountID)
	}
}
