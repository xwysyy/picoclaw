package utils

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDownloadToFile_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "hello")
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	path, err := DownloadToFile(context.Background(), srv.Client(), req, 0)
	if err != nil {
		t.Fatalf("DownloadToFile: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected file content: %q", string(data))
	}
}

func TestDownloadToFile_Non2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "nope")
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	path, err := DownloadToFile(context.Background(), srv.Client(), req, 0)
	if err == nil {
		t.Fatalf("expected error, got nil (path=%q)", path)
	}
	if path != "" {
		t.Fatalf("expected empty path on error, got %q", path)
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected error body snippet, got: %v", err)
	}
}

func TestDownloadToFile_MaxBytesExceeded_CleansUp(t *testing.T) {
	// Snapshot existing temp files for this prefix and assert we don't leak a new one.
	before, err := filepath.Glob(filepath.Join(os.TempDir(), "x-claw-dl-*"))
	if err != nil {
		t.Fatalf("glob before: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "0123456789") // 10 bytes
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	path, err := DownloadToFile(context.Background(), srv.Client(), req, 5)
	if err == nil {
		_ = os.Remove(path)
		t.Fatalf("expected error, got nil (path=%q)", path)
	}
	if path != "" {
		_ = os.Remove(path)
		t.Fatalf("expected empty path on error, got %q", path)
	}
	if !strings.Contains(err.Error(), "download too large") {
		t.Fatalf("unexpected error: %v", err)
	}

	after, err := filepath.Glob(filepath.Join(os.TempDir(), "x-claw-dl-*"))
	if err != nil {
		t.Fatalf("glob after: %v", err)
	}

	// Ensure we didn't leak new temp files.
	beforeSet := make(map[string]struct{}, len(before))
	for _, p := range before {
		beforeSet[p] = struct{}{}
	}
	for _, p := range after {
		if _, ok := beforeSet[p]; !ok {
			// Best-effort cleanup to avoid polluting /tmp if the test fails.
			_ = os.Remove(p)
			t.Fatalf("leaked temp file: %s", p)
		}
	}
}

func TestDownloadToFile_ContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the client cancels.
		<-r.Context().Done()
		// Avoid writing a response (client is gone).
		_ = w
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	path, err := DownloadToFile(ctx, srv.Client(), req, 0)
	if err == nil {
		_ = os.Remove(path)
		t.Fatalf("expected error, got nil (path=%q)", path)
	}
	if path != "" {
		_ = os.Remove(path)
		t.Fatalf("expected empty path on error, got %q", path)
	}
	if !strings.Contains(err.Error(), "request failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}
