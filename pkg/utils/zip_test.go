package utils

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractZipFile_ExtractsFilesAndDirs(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "test.zip")
	targetDir := filepath.Join(tmpDir, "out")

	zipFile, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}

	zw := zip.NewWriter(zipFile)

	// Explicit directory entry.
	if _, err := zw.Create("dir/"); err != nil {
		t.Fatalf("create dir entry: %v", err)
	}
	// Nested file.
	w1, err := zw.Create("dir/hello.txt")
	if err != nil {
		t.Fatalf("create file entry: %v", err)
	}
	if _, err := w1.Write([]byte("hello")); err != nil {
		t.Fatalf("write file entry: %v", err)
	}
	// Root file.
	w2, err := zw.Create("root.txt")
	if err != nil {
		t.Fatalf("create root entry: %v", err)
	}
	if _, err := w2.Write([]byte("root")); err != nil {
		t.Fatalf("write root entry: %v", err)
	}

	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := zipFile.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}

	if err := ExtractZipFile(zipPath, targetDir); err != nil {
		t.Fatalf("ExtractZipFile: %v", err)
	}

	got1, err := os.ReadFile(filepath.Join(targetDir, "dir", "hello.txt"))
	if err != nil {
		t.Fatalf("read extracted file: %v", err)
	}
	if string(got1) != "hello" {
		t.Fatalf("unexpected nested file content: %q", string(got1))
	}

	got2, err := os.ReadFile(filepath.Join(targetDir, "root.txt"))
	if err != nil {
		t.Fatalf("read extracted root file: %v", err)
	}
	if string(got2) != "root" {
		t.Fatalf("unexpected root file content: %q", string(got2))
	}
}

func TestExtractZipFile_InvalidZip(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "not-a-zip.bin")
	if err := os.WriteFile(zipPath, []byte("not zip"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	err := ExtractZipFile(zipPath, filepath.Join(tmpDir, "out"))
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid ZIP") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExtractZipFile_RejectsPathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "traversal.zip")

	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("../evil.txt")
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	_, _ = w.Write([]byte("evil"))

	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}

	err = ExtractZipFile(zipPath, filepath.Join(tmpDir, "out"))
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExtractZipFile_RejectsAbsolutePath(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "abs.zip")

	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("/abs.txt")
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	_, _ = w.Write([]byte("abs"))

	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}

	err = ExtractZipFile(zipPath, filepath.Join(tmpDir, "out"))
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExtractZipFile_RejectsSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "symlink.zip")

	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(f)

	h := &zip.FileHeader{
		Name:   "link",
		Method: zip.Store,
	}
	h.SetMode(os.ModeSymlink | 0o777)
	w, err := zw.CreateHeader(h)
	if err != nil {
		t.Fatalf("create header: %v", err)
	}
	_, _ = w.Write([]byte("target"))

	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}

	err = ExtractZipFile(zipPath, filepath.Join(tmpDir, "out"))
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExtractZipFile_RejectsOversizedEntry(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "big.zip")

	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(f)

	// extractSingleFile enforces a 5MB max.
	const max = 5 * 1024 * 1024
	tooBig := bytes.Repeat([]byte("a"), max+1)

	w, err := zw.Create("big.bin")
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if _, err := w.Write(tooBig); err != nil {
		t.Fatalf("write entry: %v", err)
	}

	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}

	err = ExtractZipFile(zipPath, filepath.Join(tmpDir, "out"))
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "too large") && !strings.Contains(err.Error(), "exceeds max size") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExtractSingleFile_FailsWhenDestDirMissing(t *testing.T) {
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "one.zip")

	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("file.txt")
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	_, _ = w.Write([]byte("x"))
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}

	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("open zip reader: %v", err)
	}
	defer reader.Close()
	if len(reader.File) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(reader.File))
	}

	// Intentionally point at a directory that doesn't exist: os.Create should fail.
	destPath := filepath.Join(tmpDir, "missing-parent", "file.txt")
	err = extractSingleFile(reader.File[0], destPath)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to create file") {
		t.Fatalf("unexpected error: %v", err)
	}
}
