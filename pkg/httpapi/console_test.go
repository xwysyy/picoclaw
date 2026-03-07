package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xwysyy/X-Claw/pkg/session"
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

func TestConsoleHandler_UI_LoopbackOnlyWhenNoAPIKey(t *testing.T) {
	ws := t.TempDir()
	h := NewConsoleHandler(ConsoleHandlerOptions{Workspace: ws})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/console/", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestConsoleHandler_UI_AllowsRemoteWhenAPIKeyConfigured(t *testing.T) {
	ws := t.TempDir()
	h := NewConsoleHandler(ConsoleHandlerOptions{Workspace: ws, APIKey: "secret"})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/console/", nil)
	req.RemoteAddr = "203.0.113.10:1234"
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
	traceDir := filepath.Join(ws, ".x-claw", "audit", "runs", "feishu_oc_test")
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

func TestConsoleHandler_SessionsList(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	meta := `{"key":"feishu:oc_test","summary":"hello","created":"2026-03-03T00:00:00Z","updated":"2026-03-03T00:00:01Z","messages_count":1}`
	if err := os.WriteFile(filepath.Join(ws, "sessions", "feishu_oc_test.meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatalf("write session meta: %v", err)
	}
	events := "{\"type\":\"session.message\",\"id\":\"e1\",\"ts\":\"2026-03-03T00:00:00Z\",\"ts_ms\":0,\"session_key\":\"feishu:oc_test\"}\n"
	if err := os.WriteFile(filepath.Join(ws, "sessions", "feishu_oc_test.jsonl"), []byte(events), 0o644); err != nil {
		t.Fatalf("write session events: %v", err)
	}

	h := NewConsoleHandler(ConsoleHandlerOptions{Workspace: ws})
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/console/sessions", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var payload struct {
		OK    bool              `json:"ok"`
		Items []sessionListItem `json:"items"`
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
	if payload.Items[0].Key != "feishu:oc_test" {
		t.Fatalf("expected key, got %q", payload.Items[0].Key)
	}
	if payload.Items[0].File != "sessions/feishu_oc_test.meta.json" {
		t.Fatalf("unexpected file path: %q", payload.Items[0].File)
	}
	if payload.Items[0].EventsFile != "sessions/feishu_oc_test.jsonl" {
		t.Fatalf("unexpected events file path: %q", payload.Items[0].EventsFile)
	}
}

func TestConsoleHandler_Tail(t *testing.T) {
	ws := t.TempDir()
	traceDir := filepath.Join(ws, ".x-claw", "audit", "runs", "feishu_oc_test")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		t.Fatalf("mkdir trace: %v", err)
	}
	eventsPath := filepath.Join(traceDir, "events.jsonl")
	body := "{\"n\":1}\n{\"n\":2}\n"
	if err := os.WriteFile(eventsPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}

	h := NewConsoleHandler(ConsoleHandlerOptions{Workspace: ws})
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/console/tail?path=.x-claw/audit/runs/feishu_oc_test/events.jsonl&lines=1", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var payload struct {
		OK    bool     `json:"ok"`
		Lines []string `json:"lines"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if !payload.OK {
		t.Fatalf("expected ok=true")
	}
	if len(payload.Lines) != 1 || strings.TrimSpace(payload.Lines[0]) != "{\"n\":2}" {
		t.Fatalf("unexpected tail lines: %#v", payload.Lines)
	}
}

type recordSender struct {
	calls       int
	lastChannel string
	lastTo      string
	lastContent string
	err         error
}

func (s *recordSender) SendToChannel(_ context.Context, channelName, chatID, content string) error {
	s.calls++
	s.lastChannel = channelName
	s.lastTo = chatID
	s.lastContent = content
	return s.err
}

func TestNotifyHandler_MethodNotAllowed(t *testing.T) {
	h := NewNotifyHandler(NotifyHandlerOptions{Sender: &recordSender{}})
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/notify", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
}

func TestNotifyHandler_LoopbackOnlyWhenNoAPIKey(t *testing.T) {
	h := NewNotifyHandler(NotifyHandlerOptions{
		Sender: &recordSender{},
		LastActive: func() (string, string) {
			return "feishu", "oc_test"
		},
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/api/notify", strings.NewReader(`{"content":"hi"}`))
	req.RemoteAddr = "203.0.113.10:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestNotifyHandler_APIKeyRequiredWhenConfigured(t *testing.T) {
	h := NewNotifyHandler(NotifyHandlerOptions{
		Sender: &recordSender{},
		APIKey: "secret",
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/api/notify", strings.NewReader(`{"content":"hi"}`))
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestNotifyHandler_UsesLastActiveWhenOmitted(t *testing.T) {
	sender := &recordSender{}
	h := NewNotifyHandler(NotifyHandlerOptions{
		Sender: sender,
		LastActive: func() (string, string) {
			return "feishu", "oc_test"
		},
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/api/notify", strings.NewReader(`{"content":"done"}`))
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if sender.calls != 1 {
		t.Fatalf("expected 1 send call, got %d", sender.calls)
	}
	if sender.lastChannel != "feishu" || sender.lastTo != "oc_test" || sender.lastContent != "done" {
		t.Fatalf("unexpected send args: %q %q %q", sender.lastChannel, sender.lastTo, sender.lastContent)
	}
}

func TestNotifyHandler_ChannelProvidedToFromLastActiveSameChannel(t *testing.T) {
	sender := &recordSender{}
	h := NewNotifyHandler(NotifyHandlerOptions{
		Sender: sender,
		LastActive: func() (string, string) {
			return "feishu", "oc_last"
		},
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/api/notify", strings.NewReader(`{"channel":"feishu","content":"ok"}`))
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if sender.lastTo != "oc_last" {
		t.Fatalf("expected last active to be used, got %q", sender.lastTo)
	}
}

func TestNotifyHandler_ChannelProvidedToMissingDifferentLastActiveChannel(t *testing.T) {
	h := NewNotifyHandler(NotifyHandlerOptions{
		Sender: &recordSender{},
		LastActive: func() (string, string) {
			return "telegram", "123"
		},
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/api/notify", strings.NewReader(`{"channel":"feishu","content":"ok"}`))
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	var resp notifyResponse
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp.OK {
		t.Fatalf("expected ok=false, got ok=true")
	}
}

func TestResumeLastTaskHandler_Timeout(t *testing.T) {
	h := NewResumeLastTaskHandler(ResumeLastTaskHandlerOptions{
		Timeout: 20 * time.Millisecond,
		Resume: func(_ctx context.Context) (any, string, error) {
			time.Sleep(50 * time.Millisecond)
			return nil, "", nil
		},
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/api/resume_last_task", strings.NewReader(`{}`))
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504, got %d", rr.Code)
	}
	var resp resumeLastTaskResponse
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	if resp.OK {
		t.Fatalf("expected ok=false, got ok=true")
	}
	if resp.Error != "resume timeout" {
		t.Fatalf("expected error=%q, got %q", "resume timeout", resp.Error)
	}
}

func TestNotifyHandler_InvalidJSONTrailingGarbage(t *testing.T) {
	sender := &recordSender{}
	h := NewNotifyHandler(NotifyHandlerOptions{
		Sender: sender,
		LastActive: func() (string, string) {
			return "feishu", "oc_test"
		},
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/api/notify", strings.NewReader(`{"content":"done"} {}`))
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	if sender.calls != 0 {
		t.Fatalf("expected 0 send calls, got %d", sender.calls)
	}
	var resp notifyResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != "invalid json body" {
		t.Fatalf("expected invalid json body error, got %q", resp.Error)
	}
}

func TestResumeLastTaskHandler_InvalidJSONTrailingGarbage(t *testing.T) {
	called := 0
	h := NewResumeLastTaskHandler(ResumeLastTaskHandlerOptions{
		Resume: func(_ctx context.Context) (any, string, error) {
			called++
			return map[string]any{"id": "r1"}, "ok", nil
		},
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/api/resume_last_task", strings.NewReader(`{} {}`))
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	if called != 0 {
		t.Fatalf("expected resume not to be called, got %d", called)
	}
	var resp resumeLastTaskResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != "invalid json body" {
		t.Fatalf("expected invalid json body error, got %q", resp.Error)
	}
}

func TestSessionModelHandler_SetGetClear(t *testing.T) {
	sm := session.NewSessionManager("")
	h := NewSessionModelHandler(SessionModelHandlerOptions{
		APIKey:   "k",
		Sessions: sm,
		Enabled:  true,
	})

	// Unauthenticated request should be rejected.
	{
		req := httptest.NewRequest(http.MethodPost, "http://example.com/api/session_model", strings.NewReader(`{}`))
		req.RemoteAddr = "127.0.0.1:1234"
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rr.Code)
		}
	}

	// Set override.
	{
		req := httptest.NewRequest(http.MethodPost, "http://example.com/api/session_model", strings.NewReader(`{"session_key":"s1","model":"m1","ttl_minutes":5}`))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-API-Key", "k")
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rr.Code)
		}
		var resp sessionModelResponse
		_ = json.NewDecoder(rr.Body).Decode(&resp)
		if !resp.OK {
			t.Fatalf("expected ok=true, got ok=false (err=%q)", resp.Error)
		}
		if resp.SessionKey != "s1" {
			t.Fatalf("expected session_key=%q, got %q", "s1", resp.SessionKey)
		}
		if !resp.HasOverride || resp.Model != "m1" {
			t.Fatalf("expected override model=%q, got has=%v model=%q", "m1", resp.HasOverride, resp.Model)
		}
		if resp.ExpiresAt == "" || resp.ExpiresAtMS == nil || *resp.ExpiresAtMS <= 0 {
			t.Fatalf("expected expires_at populated, got expires_at=%q expires_at_ms=%v", resp.ExpiresAt, resp.ExpiresAtMS)
		}
	}

	// Get override.
	{
		req := httptest.NewRequest(http.MethodGet, "http://example.com/api/session_model?session_key=s1", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-API-Key", "k")
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rr.Code)
		}
		var resp sessionModelResponse
		_ = json.NewDecoder(rr.Body).Decode(&resp)
		if !resp.OK {
			t.Fatalf("expected ok=true, got ok=false (err=%q)", resp.Error)
		}
		if !resp.HasOverride || resp.Model != "m1" {
			t.Fatalf("expected override model=%q, got has=%v model=%q", "m1", resp.HasOverride, resp.Model)
		}
	}

	// Clear override.
	{
		req := httptest.NewRequest(http.MethodPost, "http://example.com/api/session_model", strings.NewReader(`{"session_key":"s1","model":"clear"}`))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-API-Key", "k")
		rr := httptest.NewRecorder()

		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rr.Code)
		}
		var resp sessionModelResponse
		_ = json.NewDecoder(rr.Body).Decode(&resp)
		if !resp.OK {
			t.Fatalf("expected ok=true, got ok=false (err=%q)", resp.Error)
		}
		if resp.HasOverride || resp.Model != "" {
			t.Fatalf("expected override cleared, got has=%v model=%q", resp.HasOverride, resp.Model)
		}
	}
}

func TestSessionModelHandler_InvalidJSONTrailingGarbage(t *testing.T) {
	sm := session.NewSessionManager("")
	h := NewSessionModelHandler(SessionModelHandlerOptions{
		APIKey:   "k",
		Sessions: sm,
		Enabled:  true,
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/api/session_model", strings.NewReader(`{"session_key":"s1","model":"m1"} {}`))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-API-Key", "k")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	var resp sessionModelResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != "invalid json body" {
		t.Fatalf("expected invalid json body error, got %q", resp.Error)
	}
	if _, ok := sm.EffectiveModelOverride("s1"); ok {
		t.Fatal("expected no override to be persisted on invalid json body")
	}
}
