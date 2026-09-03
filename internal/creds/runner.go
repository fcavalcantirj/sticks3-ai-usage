package creds

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Runner executes an external command and returns its stdout, stderr, and error.
// All credential reads and version probes go through this interface so tests
// can substitute fixtures (see FakeRunner and FixtureRunner).
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout []byte, stderr []byte, err error)
}

// ExecRunner runs real commands with a 10-second timeout.
type ExecRunner struct{}

func (e ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	return stdout, stderr.Bytes(), err
}

// FakeResponse is the canned reply for a FakeRunner key.
type FakeResponse struct {
	Stdout []byte
	Stderr []byte
	Err    error
}

// FakeRunner returns pre-canned responses keyed by "name args...".
type FakeRunner struct {
	Responses map[string]FakeResponse
}

func (f FakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	key := name + " " + strings.Join(args, " ")
	if resp, ok := f.Responses[key]; ok {
		return resp.Stdout, resp.Stderr, resp.Err
	}
	return nil, nil, fmt.Errorf("FakeRunner: no response for %q", key)
}

// FixtureRunner implements Runner by reading fixture files from dir.
// It serves the keychain.json fixture for security calls and a fixed
// version string for `claude --version`.
func FixtureRunner(dir string) Runner {
	return fixtureRunner{dir: dir}
}

type fixtureRunner struct{ dir string }

func (f fixtureRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	switch name {
	case "security":
		data, err := os.ReadFile(f.dir + "/keychain.json")
		return data, nil, err
	case "claude":
		return []byte("2.1.259 (Claude Code)"), nil, nil
	default:
		return nil, nil, fmt.Errorf("FixtureRunner: unhandled command %q", name)
	}
}
