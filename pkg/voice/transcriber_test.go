package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xwysyy/X-Claw/pkg/config"
)

// Ensure GroqTranscriber satisfies the Transcriber interface at compile time.
var _ Transcriber = (*GroqTranscriber)(nil)

func TestGroqTranscriber_IsAvailable(t *testing.T) {
	if NewGroqTranscriber("").IsAvailable() {
		t.Fatal("expected empty api key transcriber to be unavailable")
	}
	if !NewGroqTranscriber("k").IsAvailable() {
		t.Fatal("expected non-empty api key transcriber to be available")
	}
}

func TestGroqTranscriberName(t *testing.T) {
	tr := NewGroqTranscriber("sk-test")
	if got := tr.Name(); got != "groq" {
		t.Errorf("Name() = %q, want %q", got, "groq")
	}
}

func TestDetectTranscriber(t *testing.T) {
	t.Setenv("GROQ_KEY", "sk-groq-env")

	tests := []struct {
		name       string
		cfg        *config.Config
		wantNil    bool
		wantName   string
		wantAPIKey string
	}{
		{
			name:    "nil config",
			cfg:     nil,
			wantNil: true,
		},
		{
			name:    "empty config",
			cfg:     &config.Config{},
			wantNil: true,
		},
		{
			name: "groq provider key (inline)",
			cfg: &config.Config{
				Providers: config.ProvidersConfig{
					Groq: config.ProviderConfig{APIKey: config.SecretRef{Inline: "sk-groq-direct"}},
				},
			},
			wantName:   "groq",
			wantAPIKey: "sk-groq-direct",
		},
		{
			name: "groq provider key (env ref)",
			cfg: &config.Config{
				Providers: config.ProvidersConfig{
					Groq: config.ProviderConfig{APIKey: config.SecretRef{Env: "GROQ_KEY"}},
				},
			},
			wantName:   "groq",
			wantAPIKey: "sk-groq-env",
		},
		{
			name: "groq provider key (missing env) returns nil",
			cfg: &config.Config{
				Providers: config.ProvidersConfig{
					Groq: config.ProviderConfig{APIKey: config.SecretRef{Env: "MISSING_GROQ_KEY"}},
				},
			},
			wantNil: true,
		},
		{
			name: "groq via model list",
			cfg: &config.Config{
				ModelList: []config.ModelConfig{
					{ModelName: "openai", Model: "openai/gpt-4o", APIKey: config.SecretRef{Inline: "sk-openai"}},
					{ModelName: "groq", Model: "groq/llama-3.3-70b", APIKey: config.SecretRef{Inline: "sk-groq-model"}},
				},
			},
			wantName:   "groq",
			wantAPIKey: "sk-groq-model",
		},
		{
			name: "groq model list entry without key is skipped",
			cfg: &config.Config{
				ModelList: []config.ModelConfig{
					{ModelName: "groq", Model: "groq/llama-3.3-70b"},
				},
			},
			wantNil: true,
		},
		{
			name: "provider key takes priority over model list",
			cfg: &config.Config{
				Providers: config.ProvidersConfig{
					Groq: config.ProviderConfig{APIKey: config.SecretRef{Inline: "sk-groq-direct"}},
				},
				ModelList: []config.ModelConfig{
					{ModelName: "groq", Model: "groq/llama-3.3-70b", APIKey: config.SecretRef{Inline: "sk-groq-model"}},
				},
			},
			wantName:   "groq",
			wantAPIKey: "sk-groq-direct",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := DetectTranscriber(tc.cfg)
			if tc.wantNil {
				if tr != nil {
					t.Errorf("DetectTranscriber() = %v, want nil", tr)
				}
				return
			}
			if tr == nil {
				t.Fatal("DetectTranscriber() = nil, want non-nil")
			}
			if got := tr.Name(); got != tc.wantName {
				t.Errorf("Name() = %q, want %q", got, tc.wantName)
			}

			if tc.wantAPIKey != "" {
				gt, ok := tr.(*GroqTranscriber)
				if !ok {
					t.Fatalf("DetectTranscriber() = %T, want *GroqTranscriber", tr)
				}
				if got := gt.apiKey; got != tc.wantAPIKey {
					t.Errorf("resolved api key = %q, want %q", got, tc.wantAPIKey)
				}
			}
		})
	}
}

func TestGroqTranscriber_Transcribe_FileNotFound(t *testing.T) {
	tr := NewGroqTranscriber("k")
	_, err := tr.Transcribe(context.Background(), "/path/does/not/exist.wav")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to open audio file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGroqTranscriber_Transcribe_Success_RequestShapeAndParsing(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "audio.wav")
	audioContent := []byte("dummy-audio")
	if err := os.WriteFile(audioPath, audioContent, 0o644); err != nil {
		t.Fatalf("WriteFile(audio) error: %v", err)
	}

	type captured struct {
		method      string
		path        string
		auth        string
		contentType string
		model       string
		responseFmt string
		fileName    string
		fileBytes   []byte
		disposition string
	}

	var cap captured
	var handlerErr error

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handlerErr != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		cap.method = r.Method
		cap.path = r.URL.Path
		cap.auth = r.Header.Get("Authorization")
		cap.contentType = r.Header.Get("Content-Type")

		mediaType, _, err := mime.ParseMediaType(cap.contentType)
		if err != nil {
			handlerErr = fmt.Errorf("parse content-type: %w", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if mediaType != "multipart/form-data" {
			handlerErr = fmt.Errorf("expected multipart/form-data, got %q", mediaType)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if err := r.ParseMultipartForm(2 << 20); err != nil {
			handlerErr = fmt.Errorf("ParseMultipartForm: %w", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		cap.model = r.FormValue("model")
		cap.responseFmt = r.FormValue("response_format")

		f, hdr, err := r.FormFile("file")
		if err != nil {
			handlerErr = fmt.Errorf("FormFile(file): %w", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer f.Close()

		cap.fileName = hdr.Filename
		cap.disposition = hdr.Header.Get("Content-Disposition")
		cap.fileBytes, err = io.ReadAll(f)
		if err != nil {
			handlerErr = fmt.Errorf("read multipart file: %w", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(TranscriptionResponse{
			Text:     "hello world",
			Language: "en",
			Duration: 1.25,
		})
	}))
	defer srv.Close()

	tr := NewGroqTranscriber("secret")
	tr.apiBase = srv.URL
	tr.httpClient = srv.Client()

	resp, err := tr.Transcribe(context.Background(), audioPath)
	if err != nil {
		t.Fatalf("Transcribe error: %v", err)
	}
	if resp == nil {
		t.Fatalf("expected non-nil response")
	}
	if resp.Text != "hello world" || resp.Language != "en" || resp.Duration != 1.25 {
		t.Fatalf("unexpected response: %#v", resp)
	}

	if handlerErr != nil {
		t.Fatalf("server handler validation failed: %v", handlerErr)
	}

	if cap.method != http.MethodPost {
		t.Fatalf("method mismatch: got %q want %q", cap.method, http.MethodPost)
	}
	if cap.path != "/audio/transcriptions" {
		t.Fatalf("path mismatch: got %q want %q", cap.path, "/audio/transcriptions")
	}
	if cap.auth != "Bearer secret" {
		t.Fatalf("auth header mismatch: got %q want %q", cap.auth, "Bearer secret")
	}
	if cap.model != "whisper-large-v3" {
		t.Fatalf("model field mismatch: got %q want %q", cap.model, "whisper-large-v3")
	}
	if cap.responseFmt != "json" {
		t.Fatalf("response_format mismatch: got %q want %q", cap.responseFmt, "json")
	}
	if cap.fileName != "audio.wav" {
		t.Fatalf("uploaded filename mismatch: got %q want %q", cap.fileName, "audio.wav")
	}
	if string(cap.fileBytes) != string(audioContent) {
		t.Fatalf("uploaded file mismatch: got %q want %q", string(cap.fileBytes), string(audioContent))
	}
	if cap.disposition != "" && !strings.Contains(cap.disposition, "form-data") {
		t.Fatalf("expected content-disposition to indicate form-data; got %q", cap.disposition)
	}
}

func TestGroqTranscriber_Transcribe_APINon200(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "audio.wav")
	if err := os.WriteFile(audioPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile(audio) error: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "bad request")
	}))
	defer srv.Close()

	tr := NewGroqTranscriber("k")
	tr.apiBase = srv.URL
	tr.httpClient = srv.Client()

	_, err := tr.Transcribe(context.Background(), audioPath)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "API error (status 400)") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("expected error to include response body, got: %v", err)
	}
}

func TestGroqTranscriber_Transcribe_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "audio.wav")
	if err := os.WriteFile(audioPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile(audio) error: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "{not json")
	}))
	defer srv.Close()

	tr := NewGroqTranscriber("k")
	tr.apiBase = srv.URL
	tr.httpClient = srv.Client()

	_, err := tr.Transcribe(context.Background(), audioPath)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to unmarshal response") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGroqTranscriber_Transcribe_ContextCanceled(t *testing.T) {
	// Ensure the request respects context cancellation and returns promptly.
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "audio.wav")
	if err := os.WriteFile(audioPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile(audio) error: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Never reply; client should cancel.
		<-r.Context().Done()
	}))
	defer srv.Close()

	tr := NewGroqTranscriber("k")
	tr.apiBase = srv.URL
	tr.httpClient = srv.Client()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tr.Transcribe(ctx, audioPath)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("expected context cancellation error, got: %v", err)
	}
}
