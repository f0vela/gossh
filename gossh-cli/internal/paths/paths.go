// Package paths locates gossh's data directory, which is shared with the
// gossh desktop app.
package paths

import (
	"os"
	"path/filepath"
)

// DataDir is ~/Library/Application Support/gossh on macOS (the user config
// dir elsewhere). GOSSH_DATA_DIR overrides it, for tests.
func DataDir() (string, error) {
	if d := os.Getenv("GOSSH_DATA_DIR"); d != "" {
		return d, nil
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "gossh"), nil
}
