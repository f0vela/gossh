//go:build darwin

package store

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// `security` exit status for errSecItemNotFound.
const keychainItemNotFound = 44

func readKeychainKey(string) (string, error) { return readKeychainKeyFor(KeyringService) }

// readKeychainKeyFor reads the key through the `security` CLI. macOS may ask
// once to allow it (choose "Always Allow") because the item was created by
// the desktop app.
func readKeychainKeyFor(service string) (string, error) {
	out, err := runSecurity(nil, "find-generic-password", "-s", service, "-a", KeyringUser, "-w")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == keychainItemNotFound {
			return "", errKeyNotFound
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func writeKeychainKey(_ string, encoded string) error {
	return writeKeychainKeyFor(KeyringService, encoded)
}

// writeKeychainKeyFor adds the item through `security -i`, which reads the
// command from stdin: the key never appears in a process argument list.
func writeKeychainKeyFor(service, encoded string) error {
	cmd := "add-generic-password -U -s " + service + " -a " + KeyringUser + " -w " + encoded + "\n"
	_, err := runSecurity(strings.NewReader(cmd), "-i")
	return err
}

// runSecurity runs the `security` CLI with a deadline, so a Keychain
// prompt nobody answers cannot hang the app.
func runSecurity(stdin *strings.Reader, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "security", args...)
	if stdin != nil {
		c.Stdin = stdin
	}
	return c.Output()
}
