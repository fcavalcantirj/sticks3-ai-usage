package creds

import (
	"context"
	"os/exec"
	"runtime"
	"testing"
)

// TestMacOSKeyStoreRoundTrip exercises the REAL keychain, not the fake.
//
// Every other test in this package uses FakeKeyStore, so macOSKeyStore itself
// was never run — and it did not work. Set piped the key to `security` on
// stdin, which reads a bare -w from the TTY instead, so it stored an EMPTY
// password and exited 0. The dashboard reported "Key stored in Keychain",
// key_state stayed not_set forever, and no test had an opinion because no test
// ever touched the real implementation.
//
// A store that silently does not store is the worst possible failure for a
// credential store, so this round-trips through the actual `security` binary.
// It uses its own service name and cleans up after itself.
func TestMacOSKeyStoreRoundTrip(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS keychain only")
	}
	if _, err := exec.LookPath("security"); err != nil {
		t.Skip("security(1) not available")
	}

	const id = "usaged-selftest"
	const want = "sk-or-v1-round-trip-value"

	ks := macOSKeyStore{}
	ctx := context.Background()
	t.Cleanup(func() { _ = ks.Delete(ctx, id) })

	if err := ks.Set(ctx, id, want); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, ok, err := ks.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get reported the key absent immediately after Set")
	}
	if got != want {
		t.Fatalf("round trip lost the value: got %q (len %d), want len %d", got, len(got), len(want))
	}

	if err := ks.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := ks.Get(ctx, id); ok {
		t.Error("the key is still present after Delete")
	}
}
