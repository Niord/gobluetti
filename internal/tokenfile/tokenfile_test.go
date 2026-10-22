package tokenfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Niord/gobluetti"
)

func TestSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil { //nolint:gosec // 0644 on purpose: Save must replace it with 0600
		t.Fatal(err)
	}
	want := gobluetti.Token{AccessToken: "acc", RefreshToken: "ref", Expiry: time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)}
	if err := Save(path, &want); err != nil {
		t.Fatalf("Save(%s) error = %v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("Save(%s) mode = %v, want 0600", path, mode)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("Save(%s) left %d files in the directory, want 1", path, len(entries))
	}
	got, err := Load(path)
	if err != nil || *got != want {
		t.Errorf("Load(%s) = %+v, %v, want %+v", path, got, err, want)
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(filepath.Join(dir, "missing.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Load(missing) error = %v, want fs.ErrNotExist", err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil {
		t.Error("Load(not json) error = nil, want error")
	}
}
