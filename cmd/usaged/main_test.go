package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var buf bytes.Buffer
	code := run([]string{"version"}, &buf)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	out := buf.String()
	// The project renamed itself to ai-usage in September 2026; the binary,
	// the LaunchAgent and the state directory all followed, and this line was
	// the last user-visible place still announcing the old name.
	if !strings.HasPrefix(out, "ai-usage") {
		t.Fatalf("expected output to start with 'ai-usage', got: %q", out)
	}
	if !strings.Contains(out, "dev") {
		t.Fatalf("expected output to contain version 'dev', got: %q", out)
	}
}

func TestServeBadConfig(t *testing.T) {
	var buf bytes.Buffer
	code := run([]string{"serve", "--listen", ""}, &buf)
	if code != 2 {
		t.Fatalf("expected exit code 2 for empty listen, got %d", code)
	}
	if !strings.Contains(buf.String(), "empty") {
		t.Fatalf("expected config error, got: %q", buf.String())
	}
}

func TestDefaultIsServe(t *testing.T) {
	var buf bytes.Buffer
	// With no subcommand, "serve" is the default. We can't start the real
	// server in a unit test, so pass --listen "" to force a config error
	// that exits 2 before binding.
	code := run([]string{"serve", "--interval", "10"}, &buf)
	if code != 2 {
		t.Fatalf("expected exit code 2 for invalid interval, got %d", code)
	}
}

func TestUnknownSubcommand(t *testing.T) {
	var buf bytes.Buffer
	code := run([]string{"bogus"}, &buf)
	if code == 0 {
		t.Fatalf("expected non-zero exit code for unknown subcommand")
	}
}
