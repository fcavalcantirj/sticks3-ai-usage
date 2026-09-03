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
	if !strings.HasPrefix(out, "usaged") {
		t.Fatalf("expected output to start with 'usaged', got: %q", out)
	}
	if !strings.Contains(out, "dev") {
		t.Fatalf("expected output to contain version 'dev', got: %q", out)
	}
}

func TestServeNotImplemented(t *testing.T) {
	var buf bytes.Buffer
	code := run([]string{"serve"}, &buf)
	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(buf.String(), "not implemented") {
		t.Fatalf("expected 'not implemented' in output, got: %q", buf.String())
	}
}

func TestOnceNotImplemented(t *testing.T) {
	var buf bytes.Buffer
	code := run([]string{"once"}, &buf)
	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	if !strings.Contains(buf.String(), "not implemented") {
		t.Fatalf("expected 'not implemented' in output, got: %q", buf.String())
	}
}

func TestDefaultIsServe(t *testing.T) {
	var buf bytes.Buffer
	code := run([]string{}, &buf)
	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
}

func TestUnknownSubcommand(t *testing.T) {
	var buf bytes.Buffer
	code := run([]string{"bogus"}, &buf)
	if code == 0 {
		t.Fatalf("expected non-zero exit code for unknown subcommand")
	}
}
