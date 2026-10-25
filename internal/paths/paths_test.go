package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRuntimeDirIsPrivate(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", base)
	// A pre-existing, world-readable directory is made private.
	if err := os.MkdirAll(filepath.Join(base, "focus"), 0o777); err != nil {
		t.Fatal(err)
	}
	dir, err := RuntimeDir()
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(dir)
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("runtime dir is not private: %v", info.Mode().Perm())
	}
}

func TestWriteFileAtomicReplacesPlantedSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "state.json")
	if err := os.Symlink(victim, target); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if err := WriteFileAtomic(target, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(victim); string(data) != "original" {
		t.Fatalf("the symlink target was overwritten: %q", data)
	}
	if info, _ := os.Lstat(target); info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("expected a regular 0600 file, got %v", info.Mode())
	}
}
