package creds

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// KeyStore stores and retrieves provider API keys in the macOS Keychain under
// service "usaged" and account = provider id (e.g. "openrouter:main"). It is
// the backend for the /v1/keys endpoints (ORDER #52 task 57).
//
// Key resolution order at poll time (documented in README):
//  1. The env var named by the provider's key_env (Felipe's .env keeps working).
//  2. The keychain entry here, used as a fallback when no env var is set.
//
// A key is NEVER written to YAML, NEVER returned by GET /v1/config, and NEVER
// logged. Tests inject a FakeKeyStore rather than touching the real keychain.
type KeyStore interface {
	// Get returns the key for id, or ("" , false, nil) when absent.
	Get(ctx context.Context, id string) (key string, ok bool, err error)
	// Set stores key for id, replacing any existing entry.
	Set(ctx context.Context, id, key string) error
	// Delete removes the key for id. Removing a missing key is not an error.
	Delete(ctx context.Context, id string) error
}

// keychainService is the Keychain Services service name used for provider keys.
const keychainService = "usaged"

// macOSKeyStore stores provider keys in the macOS Keychain via the `security`
// CLI. It is only constructed for the live server; tests use FakeKeyStore.
type macOSKeyStore struct{}

// NewKeyStore returns a KeyStore backed by the macOS Keychain.
func NewKeyStore() KeyStore {
	return macOSKeyStore{}
}

// Get runs `security find-generic-password -s usaged -a <id> -w`. A missing
// item (security exit 44) yields ("" , false, nil); other errors are returned.
func (k macOSKeyStore) Get(ctx context.Context, id string) (string, bool, error) {
	cmd := exec.CommandContext(ctx, "security", "find-generic-password",
		"-s", keychainService, "-a", id, "-w")
	out, err := cmd.Output()
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "could not be found") ||
			strings.Contains(msg, "exit status 44") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("keychain get %s: %w", id, err)
	}
	return strings.TrimSpace(string(out)), true, nil
}

// Set runs `security add-generic-password -U -s usaged -a <id> -w <key>`.
//
// THE KEY IS AN ARGUMENT, NOT STDIN, AND THAT IS NOT A STYLE CHOICE. This
// previously piped the key on stdin to keep it out of the process table, which
// reads well and does not work: `security` reads a bare -w from the TTY via
// readpassphrase(), never from stdin. With no terminal it prompts, gets
// nothing, and stores an EMPTY password while exiting 0 —
//
//	$ printf 'VALUE' | security add-generic-password -U -s probe -a x -w
//	password data for new item: retype password for new item: passwords don't match
//	$ security find-generic-password -s probe -a x -w
//	(empty)
//
// So every key ever set through the dashboard was silently discarded: the API
// answered ok, the page said "Key stored in Keychain", and key_state stayed
// not_set forever. Nothing caught it because every test used FakeKeyStore.
//
// THE TRADEOFF, STATED: an argument is visible in `ps` for the lifetime of the
// call. On a single-user Mac, for a few milliseconds, that is a far smaller
// problem than a credential store that does not store. TestMacOSKeyStoreRoundTrip
// exercises the real thing so this cannot regress into looking correct again.
func (k macOSKeyStore) Set(ctx context.Context, id, key string) error {
	cmd := exec.CommandContext(ctx, "security", "add-generic-password",
		"-U", "-s", keychainService, "-a", id, "-w", key)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("keychain set %s: %w: %s", id, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Delete runs `security delete-generic-password -s usaged -a <id>`. Deleting an
// absent item (exit 44) is treated as success.
func (k macOSKeyStore) Delete(ctx context.Context, id string) error {
	cmd := exec.CommandContext(ctx, "security", "delete-generic-password",
		"-s", keychainService, "-a", id)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := err.Error() + " " + string(out)
		if strings.Contains(msg, "could not be found") ||
			strings.Contains(msg, "exit status 44") {
			return nil
		}
		return fmt.Errorf("keychain delete %s: %w: %s", id, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// FakeKeyStore is an in-memory KeyStore for tests. It never touches the real
// keychain or the security CLI.
type FakeKeyStore struct {
	// Keys maps provider id to stored key.
	Keys map[string]string
}

// NewFakeKeyStore returns a ready-to-use FakeKeyStore.
func NewFakeKeyStore(keys map[string]string) *FakeKeyStore {
	if keys == nil {
		keys = map[string]string{}
	}
	return &FakeKeyStore{Keys: keys}
}

func (f *FakeKeyStore) Get(_ context.Context, id string) (string, bool, error) {
	k, ok := f.Keys[id]
	return k, ok, nil
}

func (f *FakeKeyStore) Set(_ context.Context, id, key string) error {
	f.Keys[id] = key
	return nil
}

func (f *FakeKeyStore) Delete(_ context.Context, id string) error {
	delete(f.Keys, id)
	return nil
}
