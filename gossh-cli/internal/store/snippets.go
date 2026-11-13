package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SnippetsFile sits next to the profile store and is encrypted with the same
// key: snippets often hold commands with hostnames, paths or tokens.
const SnippetsFile = "snippets.enc"

// Snippet is a saved piece of text to paste into an SSH session.
type Snippet struct {
	Name  string `json:"name"`
	Text  string `json:"text"`
	Enter bool   `json:"enter,omitempty"` // press Enter after pasting
}

func (s *SecureStore) snippetsPath() string {
	return filepath.Join(filepath.Dir(s.dataPath), SnippetsFile)
}

// LoadSnippets returns the saved snippets, or none when the file is absent.
func (s *SecureStore) LoadSnippets() ([]Snippet, error) {
	ciphertext, err := os.ReadFile(s.snippetsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("leyendo snippets: %w", err)
	}
	key, err := s.getOrCreateKey()
	if err != nil {
		return nil, err
	}
	plain, err := decrypt(ciphertext, key)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron descifrar los snippets: %w", err)
	}
	var out []Snippet
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, fmt.Errorf("parseando snippets: %w", err)
	}
	return out, nil
}

func (s *SecureStore) SaveSnippets(snippets []Snippet) error {
	key, err := s.getOrCreateKey()
	if err != nil {
		return err
	}
	plain, err := json.MarshalIndent(snippets, "", "  ")
	if err != nil {
		return err
	}
	enc, err := encrypt(plain, key)
	if err != nil {
		return err
	}
	tmp := strings.TrimSuffix(s.snippetsPath(), ".enc") + ".enc.tmp"
	if err := writePrivate(tmp, enc); err != nil {
		return fmt.Errorf("guardando snippets: %w", err)
	}
	return os.Rename(tmp, s.snippetsPath())
}
