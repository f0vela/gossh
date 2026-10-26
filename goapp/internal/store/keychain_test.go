package store

import (
	"os"
	"os/exec"
	"testing"

	"github.com/egoist/mygo"
)

// Touches the real login Keychain (with a throwaway service), so it only
// runs on request: TUISSH_KEYCHAIN_TEST=1 go test ./internal/store -run Keychain
func TestKeychainLegacyMigration(t *testing.T) {
	if os.Getenv("TUISSH_KEYCHAIN_TEST") == "" {
		t.Skip("set TUISSH_KEYCHAIN_TEST=1 to run against the login Keychain")
	}
	const svc = "tuissh-migration-test"
	cleanup := func() { _ = exec.Command("security", "delete-generic-password", "-s", svc, "-a", KeyringUser).Run() }
	cleanup()
	defer cleanup()

	// Missing everywhere: not found, nothing created.
	if _, err := readKeychainKeyFor(svc); err != errKeyNotFound {
		t.Fatalf("want errKeyNotFound, got %v", err)
	}

	// Legacy item made by the CLI.
	if err := exec.Command("security", "add-generic-password", "-s", svc, "-a", KeyringUser, "-w", "bGVnYWN5LWtleQ==").Run(); err != nil {
		t.Fatal(err)
	}
	got, err := readKeychainKeyFor(svc)
	if err != nil || got != "bGVnYWN5LWtleQ==" {
		t.Fatalf("legacy read: %q %v", got, err)
	}
	// Now it must live in the native store, owned by this process.
	b, err := mygo.Secrets.Service(svc).Get(KeyringUser)
	if err != nil || string(b) != "bGVnYWN5LWtleQ==" {
		t.Fatalf("native read after migration: %q %v", b, err)
	}
	if got, err := readKeychainKeyFor(svc); err != nil || got != "bGVnYWN5LWtleQ==" {
		t.Fatalf("second read: %q %v", got, err)
	}
}
