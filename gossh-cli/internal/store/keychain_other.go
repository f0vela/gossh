//go:build !darwin

package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Outside macOS there is no keychain integration: the key lives in a 0600
// file next to the store.
func keyFile(dataPath string) string { return filepath.Join(filepath.Dir(dataPath), "data-key") }

func readKeychainKey(dataPath string) (string, error) {
	b, err := os.ReadFile(keyFile(dataPath))
	if errors.Is(err, os.ErrNotExist) {
		return "", errKeyNotFound
	}
	return strings.TrimSpace(string(b)), err
}

func writeKeychainKey(dataPath, encoded string) error {
	return WritePrivate(keyFile(dataPath), []byte(encoded))
}
