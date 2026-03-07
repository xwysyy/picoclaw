package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type closeErrorFile struct {
	*os.File
}

func (f *closeErrorFile) Close() error {
	_ = f.File.Close()
	return errors.New("boom on close")
}

func TestCopyFile_CloseFailureReturnsZeroAndRemovesDestination(t *testing.T) {
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "src.txt")
	if err := os.WriteFile(srcPath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	outPath := filepath.Join(srcDir, "dst.txt")

	origOpen := copyFileOpenDestination
	copyFileOpenDestination = func(name string, flag int, perm os.FileMode) (copyFileDestination, error) {
		f, err := os.OpenFile(name, flag, perm)
		if err != nil {
			return nil, err
		}
		return &closeErrorFile{File: f}, nil
	}
	defer func() { copyFileOpenDestination = origOpen }()

	n, err := copyFile(outPath, srcPath, 0o600)
	if err == nil {
		t.Fatal("expected close failure")
	}
	if !strings.Contains(err.Error(), "close destination") {
		t.Fatalf("expected wrapped close error, got %v", err)
	}
	if n != 0 {
		t.Fatalf("expected zero bytes on close failure, got %d", n)
	}
	if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected destination cleanup on close failure, stat err=%v", statErr)
	}
}
