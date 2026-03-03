package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConsoleHandler_LoopbackOnlyWhenNoAPIKey(t *testing.T) {
	ws := t.TempDir()
	h := NewConsoleHandler(ConsoleHandlerOptions{Workspace: ws})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/console/status", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestConsoleHandler_AllowsLoopbackWhenNoAPIKey(t *testing.T) {
	ws := t.TempDir()
	h := NewConsoleHandler(ConsoleHandlerOptions{Workspace: ws})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/console/status", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var payload map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&payload)
	if ok, _ := payload["ok"].(bool); !ok {
		t.Fatalf("expected ok=true, got %v", payload["ok"])
	}
}

func TestConsoleHandler_APIKeyRequiredWhenConfigured(t *testing.T) {
	ws := t.TempDir()
	h := NewConsoleHandler(ConsoleHandlerOptions{Workspace: ws, APIKey: "secret"})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/console/status", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestConsoleHandler_APIKeyBearerWorks(t *testing.T) {
	ws := t.TempDir()
	h := NewConsoleHandler(ConsoleHandlerOptions{Workspace: ws, APIKey: "secret"})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/console/status", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestConsoleHandler_FileDownloadAllowedPrefixes(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "cron"), 0o755); err != nil {
		t.Fatalf("mkdir cron: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "cron", "jobs.json"), []byte(`{"version":1,"jobs":[]}`), 0o644); err != nil {
		t.Fatalf("write jobs.json: %v", err)
	}

	h := NewConsoleHandler(ConsoleHandlerOptions{Workspace: ws})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/console/file?path=cron/jobs.json", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "\"jobs\"") {
		t.Fatalf("expected response body to contain cron json, got %q", rr.Body.String())
	}
}

func TestConsoleHandler_FileDownloadRejectsOutsideAllowedDirs(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "notes.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatalf("write notes.txt: %v", err)
	}
	h := NewConsoleHandler(ConsoleHandlerOptions{Workspace: ws})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/console/file?path=notes.txt", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestConsoleHandler_TraceListIncludesSessionKey(t *testing.T) {
	ws := t.TempDir()
	traceDir := filepath.Join(ws, ".picoclaw", "audit", "runs", "feishu_oc_test")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		t.Fatalf("mkdir trace dir: %v", err)
	}
	events := "{\"type\":\"run.start\",\"ts\":\"2026-03-03T00:00:00Z\",\"ts_ms\":0,\"run_id\":\"r1\",\"session_key\":\"feishu:oc_test\",\"channel\":\"feishu\",\"chat_id\":\"oc_test\"}\n" +
		"{\"type\":\"run.end\",\"ts\":\"2026-03-03T00:00:01Z\",\"ts_ms\":1,\"run_id\":\"r1\",\"session_key\":\"feishu:oc_test\"}\n"
	if err := os.WriteFile(filepath.Join(traceDir, "events.jsonl"), []byte(events), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}

	h := NewConsoleHandler(ConsoleHandlerOptions{Workspace: ws})
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/console/runs", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var payload struct {
		OK    bool               `json:"ok"`
		Items []traceSessionItem `json:"items"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if !payload.OK {
		t.Fatalf("expected ok=true")
	}
	if len(payload.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(payload.Items))
	}
	if payload.Items[0].SessionKey != "feishu:oc_test" {
		t.Fatalf("expected session_key, got %q", payload.Items[0].SessionKey)
	}
}
