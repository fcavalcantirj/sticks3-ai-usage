package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClientUserAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "test-agent/1.0" {
			t.Errorf("User-Agent = %q, want test-agent/1.0", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want application/json", got)
		}
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: &http.Client{}, UserAgent: "test-agent/1.0"}
	resp, err := c.Do(context.Background(), "GET", srv.URL, nil, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp.Status != 200 {
		t.Errorf("status = %d, want 200", resp.Status)
	}
}

func TestClientBodyCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return 300 KiB — more than MaxBodyBytes
		w.Write(bytes.Repeat([]byte("a"), 300*1024))
	}))
	defer srv.Close()

	c := &Client{HTTP: &http.Client{}, UserAgent: "test"}
	resp, err := c.Do(context.Background(), "GET", srv.URL, nil, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(resp.Body) != MaxBodyBytes {
		t.Errorf("body length = %d, want %d (capped)", len(resp.Body), MaxBodyBytes)
	}
}

func TestClientTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: &http.Client{}, UserAgent: "test"}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := c.Do(ctx, "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got: %v", err)
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "120")

	d, ok := RetryAfter(h, time.Now())
	if !ok {
		t.Fatal("expected ok=true")
	}
	if d != 120*time.Second {
		t.Errorf("duration = %v, want 120s", d)
	}
}

func TestRetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	future := now.Add(200 * time.Second)

	h := http.Header{}
	h.Set("Retry-After", future.Format(http.TimeFormat))

	d, ok := RetryAfter(h, now)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if d != 200*time.Second {
		t.Errorf("duration = %v, want 200s", d)
	}
}

func TestRetryAfterMissing(t *testing.T) {
	h := http.Header{}
	d, ok := RetryAfter(h, time.Now())
	if ok {
		t.Error("expected ok=false for missing header")
	}
	if d != 0 {
		t.Errorf("duration = %v, want 0", d)
	}
}

func TestFixtureTransportClaude(t *testing.T) {
	transport := NewFixtureTransport("../../testdata/fixtures")
	req, _ := http.NewRequest("GET", "https://api.anthropic.com/api/oauth/usage", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("completion_tokens")) && !bytes.Contains(body, []byte("usage")) {
		t.Errorf("body does not look like a Claude usage response")
	}
}

func TestFixtureTransportOverride429(t *testing.T) {
	dir := t.TempDir()

	// Write a fixture file to serve on override
	os.WriteFile(filepath.Join(dir, "override_body.json"), []byte(`{"error":"rate limited"}`), 0644)

	// routes.json: override the Claude route to return 429 with Retry-After
	routes := map[string]map[string]any{
		"GET api.anthropic.com/api/oauth/usage": {
			"file":    "override_body.json",
			"status":  http.StatusTooManyRequests,
			"headers": map[string]string{"Retry-After": "120"},
		},
	}
	routesData, err := json.Marshal(routes)
	if err != nil {
		t.Fatalf("marshal routes: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "routes.json"), routesData, 0644)

	transport := NewFixtureTransport(dir)
	req, _ := http.NewRequest("GET", "https://api.anthropic.com/api/oauth/usage", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", resp.StatusCode)
	}

	d, ok := RetryAfter(resp.Header, time.Now())
	if !ok {
		t.Fatal("expected Retry-After header to be parseable")
	}
	if d != 120*time.Second {
		t.Errorf("Retry-After duration = %v, want 120s", d)
	}
}
