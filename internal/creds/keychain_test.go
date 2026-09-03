package creds

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClaudeReadCreds(t *testing.T) {
	// Token expires 1 hour in the future (epoch milliseconds)
	futureMs := time.Now().Add(time.Hour).UnixMilli()
	kcJSON := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"sk-ant-oat01-test-token","refreshToken":"rt","expiresAt":%d,"refreshTokenExpiresAt":%d,"scopes":["s"],"subscriptionType":"max","rateLimitTier":"default_claude_max_20x"}}`, futureMs, futureMs)

	r := FakeRunner{
		Responses: map[string]FakeResponse{
			"security find-generic-password -s Claude Code-credentials -a user -w": {
				Stdout: []byte(kcJSON),
			},
		},
	}

	creds, err := ReadClaude(context.Background(), r, "user")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if creds.AccessToken != "sk-ant-oat01-test-token" {
		t.Errorf("AccessToken = %q", creds.AccessToken)
	}
	if creds.SubscriptionType != "max" {
		t.Errorf("SubscriptionType = %q", creds.SubscriptionType)
	}
	if creds.RateLimitTier != "default_claude_max_20x" {
		t.Errorf("RateLimitTier = %q", creds.RateLimitTier)
	}
	if !creds.ExpiresAt.After(time.Now()) {
		t.Errorf("ExpiresAt should be in the future: %v", creds.ExpiresAt)
	}
}

func TestClaudeNotLoggedIn(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		err    error
	}{
		{"could not be found", "SecKeychainSearchCopyNext: could not be found", errors.New("exit status 44")},
		{"exit status 44", "", errors.New("exit status 44")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := FakeRunner{
				Responses: map[string]FakeResponse{
					"security find-generic-password -s Claude Code-credentials -a user -w": {
						Stderr: []byte(tc.stderr),
						Err:    tc.err,
					},
				},
			}
			_, err := ReadClaude(context.Background(), r, "user")
			if !errors.Is(err, ErrNotLoggedIn) {
				t.Errorf("expected ErrNotLoggedIn, got: %v", err)
			}
		})
	}
}

func TestClaudeExpired(t *testing.T) {
	// Token expired 1 hour ago
	pastMs := time.Now().Add(-time.Hour).UnixMilli()
	kcJSON := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"sk-ant-oat01-expired-token","refreshToken":"rt","expiresAt":%d,"refreshTokenExpiresAt":%d,"scopes":["s"],"subscriptionType":"max","rateLimitTier":"default"}}`, pastMs, pastMs)

	r := FakeRunner{
		Responses: map[string]FakeResponse{
			"security find-generic-password -s Claude Code-credentials -a user -w": {
				Stdout: []byte(kcJSON),
			},
		},
	}

	creds, err := ReadClaude(context.Background(), r, "user")
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("expected ErrExpired, got: %v", err)
	}
	// The struct is still returned even when expired
	if creds.AccessToken != "sk-ant-oat01-expired-token" {
		t.Errorf("AccessToken should be populated even on expiry: got %q", creds.AccessToken)
	}
}

func TestClaudeUserAgent(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		err    error
		want   string
	}{
		{"normal version", "2.1.259 (Claude Code)", nil, "claude-code/2.1.259"},
		{"garbage", "garbage", nil, "claude-code/2.1.0"},
		{"empty", "", nil, "claude-code/2.1.0"},
		{"command error", "", errors.New("command not found"), "claude-code/2.1.0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := FakeRunner{
				Responses: map[string]FakeResponse{
					"claude --version": {
						Stdout: []byte(tc.stdout),
						Err:    tc.err,
					},
				},
			}
			got := ClaudeUserAgent(context.Background(), r)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClaudeFixtureRunner(t *testing.T) {
	r := FixtureRunner("testdata/fixtures")
	creds, err := ReadClaude(context.Background(), r, "anyuser")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if creds.AccessToken != "sk-ant-oat01-FIXTURE-NOT-A-REAL-TOKEN" {
		t.Errorf("AccessToken = %q", creds.AccessToken)
	}
	if creds.SubscriptionType != "max" {
		t.Errorf("SubscriptionType = %q", creds.SubscriptionType)
	}
	if creds.RateLimitTier != "default_claude_max_20x" {
		t.Errorf("RateLimitTier = %q", creds.RateLimitTier)
	}
	// Fixture expiry is far future (2100-01-01)
	if !creds.ExpiresAt.After(time.Now()) {
		t.Errorf("ExpiresAt should be in the future: %v", creds.ExpiresAt)
	}

	// UA via FixtureRunner
	ua := ClaudeUserAgent(context.Background(), r)
	if ua != "claude-code/2.1.259" {
		t.Errorf("UserAgent = %q, want claude-code/2.1.259", ua)
	}
}

func TestClaudeNoTokenInErrors(t *testing.T) {
	secretToken := "SECRET-TOKEN-VALUE-12345"

	// Case 1: security command fails; stdout (which contains the token) is NOT in the error
	r1 := FakeRunner{
		Responses: map[string]FakeResponse{
			"security find-generic-password -s Claude Code-credentials -a user -w": {
				Stdout: []byte(`{"claudeAiOauth":{"accessToken":"` + secretToken + `","expiresAt":1}}`),
				Stderr: []byte("security: cannot access /anything"),
				Err:    errors.New("exit status 1"),
			},
		},
	}
	_, err := ReadClaude(context.Background(), r1, "user")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secretToken) {
		t.Errorf("error string contains token: %s", err.Error())
	}

	// Case 2: security succeeds but JSON is invalid containing the token bytes
	r2 := FakeRunner{
		Responses: map[string]FakeResponse{
			"security find-generic-password -s Claude Code-credentials -a user -w": {
				Stdout: []byte(`{broken json ` + secretToken),
			},
		},
	}
	_, err = ReadClaude(context.Background(), r2, "user")
	if err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
	if strings.Contains(err.Error(), secretToken) {
		t.Errorf("error string contains token: %s", err.Error())
	}

	// Case 3: successful read — token must not appear in any wrapping error
	r3 := FakeRunner{
		Responses: map[string]FakeResponse{
			"security find-generic-password -s Claude Code-credentials -a user -w": {
				Stdout: []byte(`{"claudeAiOauth":{"accessToken":"` + secretToken + `","expiresAt":1}}`),
			},
		},
	}
	creds, err := ReadClaude(context.Background(), r3, "user")
	if err != nil {
		if strings.Contains(err.Error(), secretToken) {
			t.Errorf("error string contains token: %s", err.Error())
		}
	}
	// Even the returned creds should not be printed in any error context
	if strings.Contains(fmt.Sprintf("%v", creds), secretToken) {
		t.Error("creds struct should not expose the token in error context")
	}
}

// Guard: ensure error variables are exported and comparable.
func TestClaudeErrorSentinels(t *testing.T) {
	if ErrNotLoggedIn == nil || ErrExpired == nil {
		t.Fatal("error sentinels must be non-nil")
	}
	if ErrNotLoggedIn == ErrExpired {
		t.Fatal("ErrNotLoggedIn and ErrExpired must be distinct")
	}
}

func TestMain(m *testing.M) {
	// Ensure testdata/fixtures exists
	if _, err := os.Stat("testdata/fixtures/keychain.json"); err != nil {
		// If running from internal/creds, the fixtures dir may be at ../../
		if _, err2 := os.Stat("../../testdata/fixtures/keychain.json"); err2 == nil {
			os.Symlink("../../testdata/fixtures", "testdata/fixtures")
		}
	}
	os.Exit(m.Run())
}
