// Package tokenfile reads and writes the token files of the gobluetti commands.
package tokenfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Niord/gobluetti"
)

// DefaultPath returns ~/.config/gobluetti/token.json. It is the same on every
// OS, unlike os.UserConfigDir, so the documented path works on macOS too.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".config", "gobluetti", "token.json"), err
}

// Encode returns tok as indented JSON with a trailing newline.
func Encode(tok *gobluetti.Token) []byte {
	data, _ := json.MarshalIndent(tok, "", "  ") //nolint:gosec // storing the token is this package's job; a Token always marshals
	return append(data, '\n')
}

// Load reads a token file. A missing file gives an error matching fs.ErrNotExist.
func Load(path string) (*gobluetti.Token, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tok gobluetti.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, fmt.Errorf("%s is not a token file: %w", path, err)
	}
	return &tok, nil
}

// Save replaces path with tok, mode 0600, so a failure never leaves a partial file.
func Save(path string, tok *gobluetti.Token) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".gobluetti-token-*") // created with mode 0600
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }() // fails harmlessly after a successful rename
	_, err = f.Write(Encode(tok))
	if err := errors.Join(err, f.Sync(), f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
