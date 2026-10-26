// Package settings persists the app's preferences as JSON next to the
// encrypted profile store.
package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Settings are the security preferences. Both default to off so existing
// users see no change until they opt in.
type Settings struct {
	// RequireOnLaunch locks the app until the user authenticates, and
	// keeps the encrypted store unopened until then.
	RequireOnLaunch bool `json:"require_on_launch"`
	// RequireForCredentials asks for authentication before using or
	// revealing saved credentials: connecting, editing, exporting.
	RequireForCredentials bool `json:"require_for_credentials"`
}

type Store struct{ path string }

func New() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, "Library", "Application Support", "tuissh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(dir, "settings.json")}, nil
}

func WithPath(path string) *Store { return &Store{path: path} }

// Load returns the saved settings, or defaults when none exist. A corrupt
// file is an error, not silently reset: that would switch protections off.
func (s *Store) Load() (Settings, error) {
	var st Settings
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return Settings{}, err
	}
	return st, nil
}

func (s *Store) Save(st Settings) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
