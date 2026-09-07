package mdns

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestLiveResponder joins the real mDNS group on this machine's real
// interfaces, announces, and then asks macOS's own resolver to look the name
// up. It is skipped unless USAGED_LIVE=1 — it puts packets on the founder's
// LAN, which the unit tests never do.
//
// WHY IT EXISTS. Every unit test in this package is a pure function over
// bytes, because Go's multicast socket cannot hear itself and a real round
// trip on one host is impossible without the loopback fix in sockopt_unix.go.
// This is the test that proves the fix, the join, the interface selection and
// the wire format all work together against a resolver nobody in this package
// wrote — which is the only kind of evidence this project accepts for
// anything that talks to another process.
//
//	USAGED_LIVE=1 go test -run TestLiveResponder -v ./internal/mdns/
func TestLiveResponder(t *testing.T) {
	if os.Getenv("USAGED_LIVE") != "1" {
		t.Skip("USAGED_LIVE=1 not set; skipping live test")
	}
	if _, err := exec.LookPath("dns-sd"); err != nil {
		t.Skip("dns-sd not available; this check is macOS-only")
	}

	// A port that is deliberately NOT 8765: the whole point is to prove the
	// advertised port comes from the listen address.
	const listen = "0.0.0.0:8799"

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	r := Start(listen, logger)
	defer r.Close()
	t.Logf("responder log:\n%s", logBuf.String())
	if !r.Enabled() {
		t.Fatalf("responder did not start: %s", logBuf.String())
	}

	// Let the first announcements go out before asking.
	time.Sleep(500 * time.Millisecond)

	t.Run("host lookup", func(t *testing.T) {
		out := runDNSSD(t, 6*time.Second, "-G", "v4", r.svc.hostName())
		if !strings.Contains(out, strings.TrimSuffix(r.svc.hostName(), ".")) {
			t.Errorf("dns-sd -G v4 did not resolve %s:\n%s", r.svc.hostName(), out)
		}
		var found bool
		for _, c := range r.conns {
			if strings.Contains(out, c.ip.String()) {
				found = true
			}
		}
		if !found {
			t.Errorf("resolved address is none of this machine's: %s", out)
		}
	})

	t.Run("service browse", func(t *testing.T) {
		out := runDNSSD(t, 6*time.Second, "-B", ServiceType)
		if !strings.Contains(out, r.svc.Instance) {
			t.Errorf("dns-sd -B %s did not find instance %q:\n%s", ServiceType, r.svc.Instance, out)
		}
	})

	t.Run("service resolve advertises the configured port", func(t *testing.T) {
		out := runDNSSD(t, 6*time.Second, "-L", r.svc.Instance, ServiceType)
		if !strings.Contains(out, ":8799") {
			t.Errorf("dns-sd -L did not report port 8799 (the configured port):\n%s", out)
		}
		if strings.Contains(out, ":8765") {
			t.Errorf("dns-sd -L reported 8765; the port is being hardcoded somewhere:\n%s", out)
		}
	})
}

// runDNSSD runs dns-sd for a bounded time and returns whatever it printed.
// dns-sd never exits on its own — it is a browser, not a query tool — so it is
// always killed by the timeout and its error is expected.
func runDNSSD(t *testing.T, d time.Duration, args ...string) string {
	t.Helper()
	cmd := exec.Command("dns-sd", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start dns-sd: %v", err)
	}
	timer := time.AfterFunc(d, func() { _ = cmd.Process.Kill() })
	defer timer.Stop()
	_ = cmd.Wait()
	t.Logf("dns-sd %s:\n%s", strings.Join(args, " "), out.String())
	return out.String()
}
