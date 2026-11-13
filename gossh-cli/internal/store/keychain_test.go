//go:build darwin

package store

import (
	"os"
	"os/exec"
	"testing"
)

// Touches the real login Keychain (with a throwaway service), so it only
// runs on request: GOSSH_KEYCHAIN_TEST=1 go test ./internal/store -run Keychain
func TestKeychainRoundTrip(t *testing.T) {
	if os.Getenv("GOSSH_KEYCHAIN_TEST") == "" {
		t.Skip("set GOSSH_KEYCHAIN_TEST=1 to run against the login Keychain")
	}
	const svc = "gossh-keychain-test"
	cleanup := func() { _ = exec.Command("security", "delete-generic-password", "-s", svc, "-a", KeyringUser).Run() }
	cleanup()
	defer cleanup()

	if _, err := readKeychainKeyFor(svc); err != errKeyNotFound {
		t.Fatalf("want errKeyNotFound, got %v", err)
	}
	if err := writeKeychainKeyFor(svc, "bGVnYWN5LWtleQ=="); err != nil {
		t.Fatal(err)
	}
	got, err := readKeychainKeyFor(svc)
	if err != nil || got != "bGVnYWN5LWtleQ==" {
		t.Fatalf("read: %q %v", got, err)
	}
}
