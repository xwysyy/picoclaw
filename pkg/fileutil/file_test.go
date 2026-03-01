package fileutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileAtomic_CreatesAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "file.txt")

	if err := WriteFileAtomic(target, []byte("hello"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic(create) error: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("content mismatch: got %q want %q", string(got), "hello")
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat error: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm mismatch: got %o want %o", info.Mode().Perm(), 0o600)
	}

	// Overwrite existing file with different content and perms.
	if err := WriteFileAtomic(target, []byte("world"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic(overwrite) error: %v", err)
	}

	got, err = os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(got) != "world" {
		t.Fatalf("content mismatch: got %q want %q", string(got), "world")
	}

	info, err = os.Stat(target)
	if err != nil {
		t.Fatalf("Stat error: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("perm mismatch: got %o want %o", info.Mode().Perm(), 0o644)
	}

	// The temp file should not be left behind in the destination directory.
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatalf("ReadDir error: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("unexpected temp file left behind: %s", e.Name())
		}
	}
}

func TestWriteFileAtomic_ErrorWhenTargetIsDirectory_CleansTempFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("MkdirAll(target) error: %v", err)
	}

	err := WriteFileAtomic(target, []byte("data"), 0o600)
	if err == nil {
		t.Fatal("expected error when target path is a directory, got nil")
	}

	info, statErr := os.Stat(target)
	if statErr != nil {
		t.Fatalf("Stat(target) error: %v", statErr)
	}
	if !info.IsDir() {
		t.Fatalf("expected target to remain a directory after error, got mode=%v", info.Mode())
	}

	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("ReadDir error: %v", readErr)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("unexpected temp file left behind: %s", e.Name())
		}
	}
}

func TestWriteFileAtomic_ErrorWhenParentIsFile(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(parent, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile(parent) error: %v", err)
	}

	target := filepath.Join(parent, "child.txt")
	err := WriteFileAtomic(target, []byte("data"), 0o600)
	if err == nil {
		t.Fatal("expected error when parent path is a file, got nil")
	}
	if _, statErr := os.Stat(target); statErr == nil {
		t.Fatalf("expected target not to exist; statErr=%v", statErr)
	}
}
