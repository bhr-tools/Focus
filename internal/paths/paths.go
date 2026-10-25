// Package paths locates the per-user files shared by the daemon and the CLI.
package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// RuntimeDir returns a directory private to the current user, creating it
// with mode 0700: $XDG_RUNTIME_DIR/focus, else ~/.local/state/focus. Shared
// directories such as /tmp are never used, so other local users can neither
// read these files nor plant them in advance.
func RuntimeDir() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", errors.New("no private directory: neither XDG_RUNTIME_DIR nor a home directory is available")
		}
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, "focus")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", fmt.Errorf("make %s private: %w", dir, err)
		}
	}
	return dir, nil
}

// File returns the path of name inside RuntimeDir.
func File(name string) (string, error) {
	dir, err := RuntimeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// WriteFileAtomic writes data to a fresh, randomly named file next to path
// (mode 0600) and renames it over path. Rename replaces a planted symlink
// instead of following it.
func WriteFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
