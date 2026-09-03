package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// MaxBodyBytes is the maximum response body the client will read.
const MaxBodyBytes = 256 * 1024

// DefaultTimeout is applied when the caller's context has no deadline.
const DefaultTimeout = 10 * time.Second

// Client wraps http.Client with a fixed User-Agent and sensible defaults.
// It never retries; callers are responsible for retry/backoff.
type Client struct {
	HTTP      *http.Client
	UserAgent string
}

// Response is the normalised, fully-read result of an HTTP call.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Do issues a request with a 10 s default timeout, sets Accept: application/json
// and the client User-Agent, and reads up to MaxBodyBytes of the response body.
func (c *Client) Do(ctx context.Context, method, url string, headers map[string]string, body []byte) (Response, error) {
	// Apply a default timeout when the caller's context has no deadline.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return Response{}, err
	}

	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, MaxBodyBytes)
	respBody, err := io.ReadAll(limited)
	if err != nil {
		return Response{}, err
	}

	return Response{
		Status: resp.StatusCode,
		Header: resp.Header,
		Body:   respBody,
	}, nil
}

// RetryAfter parses the Retry-After response header. It accepts either an
// integer number of seconds or an HTTP-date. Returns the duration from now
// until the retry time, and true if the header was present and parseable.
func RetryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	ra := h.Get("Retry-After")
	if ra == "" {
		return 0, false
	}

	// Integer seconds.
	if seconds, err := strconv.Atoi(ra); err == nil {
		return time.Duration(seconds) * time.Second, true
	}

	// HTTP-date.
	t, err := http.ParseTime(ra)
	if err != nil {
		return 0, false
	}

	d := t.Sub(now)
	if d < 0 {
		d = 0
	}
	return d, true
}

// --- Fixture transport ---

// fixtureEntry describes what a fixture transport returns for a route.
// File is read from the fixture dir; Inline is used verbatim when File
// is empty. Status defaults to 200 when 0.
type fixtureEntry struct {
	File    string            `json:"file"`
	Inline  string            `json:"inline"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
}

// defaultFixtureTable maps "METHOD host/path" to a fixture entry.
func defaultFixtureTable() map[string]fixtureEntry {
	return map[string]fixtureEntry{
		"GET api.anthropic.com/api/oauth/usage":  {File: "claude_usage.json"},
		"GET chatgpt.com/backend-api/wham/usage": {File: "codex_usage.json"},
		"GET openrouter.ai/api/v1/credits":       {File: "openrouter_credits.json"},
		"GET openrouter.ai/api/v1/key":           {File: "openrouter_key.json"},
		"GET api.groq.com/openai/v1/models":      {Inline: `{"data":[]}`},
		"POST api.groq.com/openai/v1/chat/completions": {
			Inline: `{"usage":{"total_tokens":73}}`,
			Headers: map[string]string{
				"x-ratelimit-limit-requests":     "500000",
				"x-ratelimit-remaining-requests": "499999",
				"x-ratelimit-limit-tokens":       "250000",
				"x-ratelimit-remaining-tokens":   "249927",
				"x-ratelimit-reset-requests":     "172ms",
				"x-ratelimit-reset-tokens":       "17ms",
			},
		},
	}
}

// fixtureTransport implements http.RoundTripper by serving fixture files.
type fixtureTransport struct {
	dir   string
	table map[string]fixtureEntry
}

// NewFixtureTransport returns an http.RoundTripper that serves fixture files
// from dir. The default routes serve the standard fixture files; an optional
// dir/routes.json override file can change the file, status, or headers per
// route. Unknown routes return 404 with an empty JSON body.
func NewFixtureTransport(dir string) http.RoundTripper {
	ft := &fixtureTransport{
		dir:   dir,
		table: defaultFixtureTable(),
	}

	routesPath := filepath.Join(dir, "routes.json")
	if data, err := os.ReadFile(routesPath); err == nil {
		var overrides map[string]fixtureEntry
		if err := json.Unmarshal(data, &overrides); err == nil {
			for k, v := range overrides {
				ft.table[k] = v
			}
		}
	}

	return ft
}

func (f *fixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// http.RoundTripper contract: don't modify the request
	req = req.Clone(req.Context())

	key := req.Method + " " + req.URL.Host + req.URL.Path
	entry, ok := f.table[key]
	if !ok {
		return f.newJSONResponse(http.StatusNotFound, http.Header{}, []byte("{}")), nil
	}

	status := entry.Status
	if status == 0 {
		status = http.StatusOK
	}

	var body []byte
	if entry.File != "" {
		filePath := filepath.Join(f.dir, entry.File)
		var err error
		body, err = os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("fixture transport: read %s: %w", entry.File, err)
		}
	} else if entry.Inline != "" {
		body = []byte(entry.Inline)
	} else {
		body = []byte("{}")
	}

	headers := make(http.Header)
	for k, v := range entry.Headers {
		headers.Set(k, v)
	}

	return f.newJSONResponse(status, headers, body), nil
}

func (f *fixtureTransport) newJSONResponse(status int, headers http.Header, body []byte) *http.Response {
	resp := &http.Response{
		Status:        http.StatusText(status),
		StatusCode:    status,
		Header:        headers,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       nil,
	}
	if resp.Header.Get("Content-Type") == "" {
		resp.Header.Set("Content-Type", "application/json")
	}
	return resp
}
