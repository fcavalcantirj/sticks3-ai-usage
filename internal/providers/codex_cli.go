package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"usaged/internal/snapshot"
)

// CodexCLIRunner runs the codex app-server and returns its stdout. Unlike
// creds.Runner it accepts stdin input, which the app-server needs for the
// JSON-RPC initialize handshake + method call.
type CodexCLIRunner interface {
	RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error)
}

// realCodexCLIRunner execs the real `codex` binary.
type realCodexCLIRunner struct{}

func (realCodexCLIRunner) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("codex app-server: %w: %s", err, stderr.String())
	}
	return stdout, nil
}

// NewCodexCLIRunner returns a runner that execs the real codex binary.
func NewCodexCLIRunner() CodexCLIRunner {
	return realCodexCLIRunner{}
}

// fixtureCodexRunner reads the app-server response from a fixture file instead
// of running the real binary. Used in `usaged once` fixture mode.
type fixtureCodexRunner struct {
	dir string
}

func (f fixtureCodexRunner) RunWithStdin(_ context.Context, _ []byte, _ string, _ ...string) ([]byte, error) {
	return os.ReadFile(filepath.Join(f.dir, "codex_appserver.json"))
}

// NewCodexFixtureRunner returns a CodexCLIRunner that serves fixture data.
func NewCodexFixtureRunner(dir string) CodexCLIRunner {
	return fixtureCodexRunner{dir: dir}
}

// --- app-server JSON-RPC types (camelCase, as emitted by codex app-server) ---

// codexRPCMessage is one JSON-RPC 2.0 line from the app-server's stdout.
type codexRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id"`     // nil for server-initiated notifications
	Method  string          `json:"method"` // non-empty for requests / notifications
	Result  json.RawMessage `json:"result"` // present for responses
}

// codexRPCResult is the structure of the result field for id:1.
type codexRPCResult struct {
	RateLimits codexAppServerRateLimits `json:"rateLimits"`
	Reset      codexAppServerReset      `json:"rateLimitResetCredits"`
}

type codexAppServerRateLimits struct {
	Primary   codexAppServerWindow  `json:"primary"`
	Secondary codexAppServerWindow  `json:"secondary"`
	Credits   codexAppServerCredits `json:"credits"`
	PlanType  string                `json:"planType"`
}

type codexAppServerWindow struct {
	UsedPercent        int   `json:"usedPercent"`
	WindowDurationMins int   `json:"windowDurationMins"`
	ResetsAt           int64 `json:"resetsAt"`
}

type codexAppServerCredits struct {
	HasCredits bool   `json:"hasCredits"`
	Unlimited  bool   `json:"unlimited"`
	Balance    string `json:"balance"`
}

type codexAppServerReset struct {
	AvailableCount           int `json:"availableCount"`
	ApplicableAvailableCount int `json:"applicableAvailableCount"`
}

// --- conversion: app-server response → codexUsageResponse (wham/usage layout) ---

// convertAppServer maps the app-server's result into the codexUsageResponse
// struct that parseCodexRows already understands.
func convertAppServer(result codexRPCResult) codexUsageResponse {
	rl := result.RateLimits
	return codexUsageResponse{
		PlanType: rl.PlanType,
		RateLimit: codexRateLimit{
			PrimaryWindow: codexWindow{
				UsedPercent:        rl.Primary.UsedPercent,
				LimitWindowSeconds: rl.Primary.WindowDurationMins * 60,
				ResetAt:            rl.Primary.ResetsAt,
			},
			SecondaryWindow: codexWindow{
				UsedPercent:        rl.Secondary.UsedPercent,
				LimitWindowSeconds: rl.Secondary.WindowDurationMins * 60,
				ResetAt:            rl.Secondary.ResetsAt,
			},
		},
		Credits: codexCredits{
			HasCredits: rl.Credits.HasCredits,
			Unlimited:  rl.Credits.Unlimited,
			Balance:    rl.Credits.Balance,
		},
		ResetCredits: codexResetCredits{
			AvailableCount:           result.Reset.AvailableCount,
			ApplicableAvailableCount: result.Reset.ApplicableAvailableCount,
		},
	}
}

// parseCodexAppServer scans the app-server stdout for newline-delimited
// JSON-RPC messages and returns the result of the id:1 request.
func parseCodexAppServer(stdout []byte) (codexUsageResponse, error) {
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	// Response lines can be long; raise the scanner buffer.
	scanner.Buffer(make([]byte, 0, 1<<20), 4<<20)

	var found bool
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var msg codexRPCMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue // skip unparseable lines
		}

		if msg.ID == nil {
			continue // server notification, not a response to our request
		}
		if *msg.ID != 1 {
			continue // not our rateLimits/read response
		}

		var result codexRPCResult
		if err := json.Unmarshal(msg.Result, &result); err != nil {
			return codexUsageResponse{}, fmt.Errorf("parse app-server result: %w", err)
		}
		found = true
		return convertAppServer(result), nil
	}
	if err := scanner.Err(); err != nil {
		return codexUsageResponse{}, fmt.Errorf("scan app-server output: %w", err)
	}
	if !found {
		return codexUsageResponse{}, errors.New("no id:1 response in app-server output")
	}
	return codexUsageResponse{}, nil
}

// codexAppServerStdin builds the JSON-RPC lines that the app-server expects
// on stdin: an initialize handshake (id:0) followed by the rateLimits/read
// request (id:1).
func codexAppServerStdin() []byte {
	lines := []string{
		`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"usaged","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"account/rateLimits/read"}`,
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// --- codexCliProvider ---

// codexCliProvider fetches Codex rate-limit data via the `codex app-server`
// JSON-RPC transport instead of the wham/usage HTTP endpoint. The app-server
// handles auth internally (reading ~/.codex/auth.json) so no authPath is
// needed.
type codexCliProvider struct {
	runner CodexCLIRunner
	loc    *time.Location
}

// NewCodexCLI returns a Codex fetcher backed by the codex app-server.
func NewCodexCLI(runner CodexCLIRunner, loc *time.Location) Fetcher {
	return &codexCliProvider{runner: runner, loc: loc}
}

func (p *codexCliProvider) ID() string { return codexID }

func (p *codexCliProvider) Fetch(ctx context.Context, now time.Time) (snapshot.Provider, Outcome) {
	slog.Debug("codex-cli: fetching via app-server")

	stdin := codexAppServerStdin()
	stdout, err := p.runner.RunWithStdin(
		ctx, stdin, "codex",
		"-s", "read-only", "-a", "never", "app-server", "--listen", "stdio://",
	)
	if err != nil {
		slog.Debug("codex-cli: runner error", "err", err)
		msg := "api unreachable"
		if errors.Is(err, context.DeadlineExceeded) {
			msg = "api timeout"
		}
		return codexBlock("error", msg, "", nil, now.Unix()), Outcome{}
	}

	body, err := parseCodexAppServer(stdout)
	if err != nil {
		slog.Debug("codex-cli: parse error", "err", err)
		return codexBlock("error", "parse error", "", nil, now.Unix()), Outcome{}
	}

	rows := parseCodexRows(body, now, p.loc)
	return codexBlock("ok", "", body.PlanType, rows, now.Unix()), Outcome{}
}
