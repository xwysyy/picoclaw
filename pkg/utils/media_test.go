package utils

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsAudioFile(t *testing.T) {
	tests := []struct {
		name        string
		filename    string
		contentType string
		want        bool
	}{
		{name: "By extension (case-insensitive)", filename: "song.MP3", contentType: "", want: true},
		{name: "By audio/* content type", filename: "noext", contentType: "audio/wav", want: true},
		{name: "By application/ogg content type", filename: "noext", contentType: "application/ogg", want: true},
		{name: "Not audio", filename: "readme.txt", contentType: "application/octet-stream", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAudioFile(tc.filename, tc.contentType); got != tc.want {
				t.Fatalf("IsAudioFile(%q,%q)=%v want=%v", tc.filename, tc.contentType, got, tc.want)
			}
		})
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		in          string
		wantSuffix  string
		mustContain string
	}{
		{in: "../..evil.txt", wantSuffix: "evil.txt"},
		{in: "..\\\\evil.txt", wantSuffix: "evil.txt", mustContain: "_"},
		{in: "a/b/c.txt", wantSuffix: "c.txt"},
		{in: "foo..bar", wantSuffix: "foobar"},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := SanitizeFilename(tc.in)
			if got == "" {
				t.Fatalf("expected non-empty result")
			}
			if strings.Contains(got, "..") {
				t.Fatalf("expected '..' to be removed, got %q", got)
			}
			if strings.Contains(got, "/") || strings.Contains(got, "\\") {
				t.Fatalf("expected path separators to be removed, got %q", got)
			}
			if tc.wantSuffix != "" && !strings.HasSuffix(got, tc.wantSuffix) {
				t.Fatalf("expected suffix %q, got %q", tc.wantSuffix, got)
			}
			if tc.mustContain != "" && !strings.Contains(got, tc.mustContain) {
				t.Fatalf("expected to contain %q, got %q", tc.mustContain, got)
			}
		})
	}
}

func TestDownloadFile_Success_UsesHeadersAndSafeName(t *testing.T) {
	wantAuth := "Bearer test-token"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != wantAuth {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "payload")
	}))
	defer srv.Close()

	path := DownloadFile(srv.URL, "..evil.txt", DownloadOptions{
		Timeout: 500 * time.Millisecond,
		ExtraHeaders: map[string]string{
			"Authorization": wantAuth,
		},
		LoggerPrefix: "test",
	})
	if path == "" {
		t.Fatalf("expected non-empty path")
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	// Should be placed under the canonical X-Claw media temp directory.
	if dir := filepath.Dir(path); dir != MediaTempDir() {
		t.Fatalf("unexpected directory: %s", dir)
	}

	// Safe name should remove ".." from the basename, but keep extension.
	base := filepath.Base(path)
	if !strings.HasSuffix(base, "_evil.txt") {
		t.Fatalf("unexpected filename: %q", base)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(data) != "payload" {
		t.Fatalf("unexpected content: %q", string(data))
	}
}

func TestDownloadFile_Non200_ReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if got := DownloadFile(srv.URL, "x.txt", DownloadOptions{Timeout: 100 * time.Millisecond, LoggerPrefix: "test"}); got != "" {
		_ = os.Remove(got)
		t.Fatalf("expected empty path on error, got %q", got)
	}
}

func TestDownloadFile_InvalidURL_ReturnsEmpty(t *testing.T) {
	if got := DownloadFile("://bad", "x.txt", DownloadOptions{Timeout: 100 * time.Millisecond, LoggerPrefix: "test"}); got != "" {
		_ = os.Remove(got)
		t.Fatalf("expected empty path on invalid url, got %q", got)
	}
}

func TestDownloadFileSimple(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "simple")
	}))
	defer srv.Close()

	path := DownloadFileSimple(srv.URL, "simple.txt")
	if path == "" {
		t.Fatalf("expected non-empty path")
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(data) != "simple" {
		t.Fatalf("unexpected content: %q", string(data))
	}
}
