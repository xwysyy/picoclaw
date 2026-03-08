package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xwysyy/X-Claw/pkg/session"
)

func readStreamLine(t *testing.T, lines <-chan string, timeout time.Duration) string {
	t.Helper()
	select {
	case line := <-lines:
		return line
	case <-time.After(timeout):
		t.Fatal("timed out waiting for stream line")
		return ""
	}
}

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

func TestConsoleHandler_StatusCronSummaryIncludesSessionTraceHints(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "cron"), 0o755); err != nil {
		t.Fatalf("mkdir cron: %v", err)
	}
	store := `{"version":1,"jobs":[{"id":"job-1","name":"traceable cron","enabled":true,"schedule":{"kind":"every","everyMs":60000},"payload":{"kind":"agent_turn","message":"work","deliver":false,"channel":"feishu","to":"oc_test"},"state":{"lastStatus":"ok","lastDurationMs":42,"lastSessionKey":"cron-job-1","runHistory":[{"runId":"run-1","startedAtMs":1000,"finishedAtMs":1042,"durationMs":42,"status":"ok","sessionKey":"cron-job-1","output":"done"}]},"createdAtMs":1000,"updatedAtMs":1042}]}`
	if err := os.WriteFile(filepath.Join(ws, "cron", "jobs.json"), []byte(store), 0o644); err != nil {
		t.Fatalf("write jobs.json: %v", err)
	}

	h := NewConsoleHandler(ConsoleHandlerOptions{Workspace: ws})
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/console/status", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var payload map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	cronSummary, ok := payload["cron"].(map[string]any)
	if !ok {
		t.Fatalf("expected cron summary object, got %#v", payload["cron"])
	}
	jobStates, ok := cronSummary["jobStates"].([]any)
	if !ok || len(jobStates) != 1 {
		t.Fatalf("expected 1 cron job state, got %#v", cronSummary["jobStates"])
	}
	jobState, ok := jobStates[0].(map[string]any)
	if !ok {
		t.Fatalf("expected cron job state object, got %#v", jobStates[0])
	}
	if got := jobState["lastSessionKey"]; got != "cron-job-1" {
		t.Fatalf("lastSessionKey = %#v, want %q", got, "cron-job-1")
	}
	runHistory, ok := jobState["runHistory"].([]any)
	if !ok || len(runHistory) != 1 {
		t.Fatalf("expected 1 runHistory item, got %#v", jobState["runHistory"])
	}
	runItem, ok := runHistory[0].(map[string]any)
	if !ok {
		t.Fatalf("expected runHistory object, got %#v", runHistory[0])
	}
	if got := runItem["sessionKey"]; got != "cron-job-1" {
		t.Fatalf("runHistory.sessionKey = %#v, want %q", got, "cron-job-1")
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

func TestConsoleHandler_SessionsListSkipsCorruptJSONL(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	meta := `{"key":"feishu:oc_test","summary":"hello","created":"2026-03-03T00:00:00Z","updated":"2026-03-03T00:00:01Z","messages_count":1}`
	if err := os.WriteFile(filepath.Join(ws, "sessions", "feishu_oc_test.meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatalf("write session meta: %v", err)
	}
	broken := strings.Join([]string{
		`{"type":"session.message","id":"e1","ts":"2026-03-03T00:00:00Z","ts_ms":0,"session_key":"feishu:oc_test"}`,
		`{"type":"session.message","id":"e2","ts":"2026-03-03T00:00:01Z","ts_ms":1,"session_key":"feishu:oc_test"`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(ws, "sessions", "feishu_oc_test.jsonl"), []byte(broken), 0o644); err != nil {
		t.Fatalf("write broken session events: %v", err)
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
	if len(payload.Items) != 0 {
		t.Fatalf("expected corrupt session to be skipped, got %d items", len(payload.Items))
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

func TestConsoleHandler_FileDownloadDoesNotExposeInternalErrors(t *testing.T) {
	oldStat := consoleFileStat
	consoleFileStat = func(string) (os.FileInfo, error) {
		return nil, errors.New("simulated stat failure")
	}
	t.Cleanup(func() {
		consoleFileStat = oldStat
	})

	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "cron"), 0o755); err != nil {
		t.Fatalf("mkdir cron: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "cron", "jobs.json"), []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatalf("write jobs.json: %v", err)
	}

	h := NewConsoleHandler(ConsoleHandlerOptions{Workspace: ws})
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/console/file?path=cron/jobs.json", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "simulated stat failure") {
		t.Fatalf("expected stable error surface, got %q", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "failed to access file") {
		t.Fatalf("expected generic file error, got %q", rr.Body.String())
	}
}

func TestConsoleHandler_TailDoesNotExposeInternalErrors(t *testing.T) {
	oldTailLines := consoleTailLines
	consoleTailLines = func(string, int, int64) ([]string, bool, error) {
		return nil, false, errors.New("simulated tail failure")
	}
	t.Cleanup(func() {
		consoleTailLines = oldTailLines
	})

	ws := t.TempDir()
	traceDir := filepath.Join(ws, ".x-claw", "audit", "runs", "feishu_oc_test")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		t.Fatalf("mkdir trace: %v", err)
	}
	eventsPath := filepath.Join(traceDir, "events.jsonl")
	if err := os.WriteFile(eventsPath, []byte("line-one\n"), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}

	h := NewConsoleHandler(ConsoleHandlerOptions{Workspace: ws})
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/console/tail?path=.x-claw/audit/runs/feishu_oc_test/events.jsonl&lines=1", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "simulated tail failure") {
		t.Fatalf("expected stable error surface, got %q", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "failed to read file") {
		t.Fatalf("expected generic tail error, got %q", rr.Body.String())
	}
}

func TestConsoleHandler_TailInternalErrorUsesStableMessage(t *testing.T) {
	ws := t.TempDir()
	traceDir := filepath.Join(ws, ".x-claw", "audit", "runs", "feishu_oc_test")
	eventsDir := filepath.Join(traceDir, "events.jsonl")
	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		t.Fatalf("mkdir trace: %v", err)
	}

	h := NewConsoleHandler(ConsoleHandlerOptions{Workspace: ws})
	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/console/tail?path=.x-claw/audit/runs/feishu_oc_test/events.jsonl&lines=1", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(strings.ToLower(body), "directory") {
		t.Fatalf("expected stable error surface, got %q", body)
	}
	if !strings.Contains(body, "failed to read file") {
		t.Fatalf("expected stable error message, got %q", body)
	}
}

func TestConsoleHandler_StreamFollowsRotate(t *testing.T) {
	oldKeepAlive := consoleStreamKeepAliveInterval
	oldStat := consoleStreamStatInterval
	oldSleep := consoleStreamIdleSleep
	consoleStreamKeepAliveInterval = 100 * time.Millisecond
	consoleStreamStatInterval = 50 * time.Millisecond
	consoleStreamIdleSleep = 10 * time.Millisecond
	t.Cleanup(func() {
		consoleStreamKeepAliveInterval = oldKeepAlive
		consoleStreamStatInterval = oldStat
		consoleStreamIdleSleep = oldSleep
	})

	ws := t.TempDir()
	traceDir := filepath.Join(ws, ".x-claw", "audit", "runs", "feishu_oc_test")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		t.Fatalf("mkdir trace: %v", err)
	}
	path := filepath.Join(traceDir, "events.jsonl")
	if err := os.WriteFile(path, []byte("line-one\n"), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}

	h := NewConsoleHandler(ConsoleHandlerOptions{Workspace: ws})
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/console/stream?path=.x-claw/audit/runs/feishu_oc_test/events.jsonl&tail=1", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	lines := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				lines <- line
			}
		}
	}()

	if got := readStreamLine(t, lines, time.Second); got != "line-one" {
		t.Fatalf("first line = %q, want %q", got, "line-one")
	}
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatalf("rotate old file: %v", err)
	}
	if err := os.WriteFile(path, []byte("line-two\n"), 0o644); err != nil {
		t.Fatalf("write rotated file: %v", err)
	}

	if got := readStreamLine(t, lines, 2*time.Second); got != "line-two" {
		t.Fatalf("rotated line = %q, want %q", got, "line-two")
	}
	cancel()
}

func TestConsoleHandler_StreamFollowsTruncate(t *testing.T) {
	oldKeepAlive := consoleStreamKeepAliveInterval
	oldStat := consoleStreamStatInterval
	oldSleep := consoleStreamIdleSleep
	consoleStreamKeepAliveInterval = 100 * time.Millisecond
	consoleStreamStatInterval = 50 * time.Millisecond
	consoleStreamIdleSleep = 10 * time.Millisecond
	t.Cleanup(func() {
		consoleStreamKeepAliveInterval = oldKeepAlive
		consoleStreamStatInterval = oldStat
		consoleStreamIdleSleep = oldSleep
	})

	ws := t.TempDir()
	traceDir := filepath.Join(ws, ".x-claw", "audit", "runs", "feishu_oc_test")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		t.Fatalf("mkdir trace: %v", err)
	}
	path := filepath.Join(traceDir, "events.jsonl")
	if err := os.WriteFile(path, []byte("line-one\nline-two\n"), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}

	h := NewConsoleHandler(ConsoleHandlerOptions{Workspace: ws})
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/console/stream?path=.x-claw/audit/runs/feishu_oc_test/events.jsonl&tail=1", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	lines := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				lines <- line
			}
		}
	}()

	if got := readStreamLine(t, lines, time.Second); got != "line-two" {
		t.Fatalf("first line = %q, want %q", got, "line-two")
	}
	if err := os.WriteFile(path, []byte("line-three\n"), 0o644); err != nil {
		t.Fatalf("truncate+rewrite file: %v", err)
	}

	if got := readStreamLine(t, lines, 2*time.Second); got != "line-three" {
		t.Fatalf("truncated line = %q, want %q", got, "line-three")
	}
	cancel()
}

func TestConsoleHandler_StreamInitialTailReadFailureIsObservable(t *testing.T) {
	oldTailLines := consoleStreamTailLines
	consoleStreamTailLines = func(string, int, int64) ([]string, bool, error) {
		return nil, false, errors.New("simulated tail failure")
	}
	t.Cleanup(func() {
		consoleStreamTailLines = oldTailLines
	})

	ws := t.TempDir()
	traceDir := filepath.Join(ws, ".x-claw", "audit", "runs", "feishu_oc_test")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		t.Fatalf("mkdir trace: %v", err)
	}
	path := filepath.Join(traceDir, "events.jsonl")
	if err := os.WriteFile(path, []byte("line-one\n"), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}

	h := NewConsoleHandler(ConsoleHandlerOptions{Workspace: ws})
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/console/stream?path=.x-claw/audit/runs/feishu_oc_test/events.jsonl&tail=1", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if strings.Contains(string(body), "simulated tail failure") {
		t.Fatalf("expected stable error surface, got %q", string(body))
	}
	if !strings.Contains(string(body), "stream unavailable") {
		t.Fatalf("expected generic stream error, got %q", string(body))
	}
	if !strings.Contains(string(body), `"ok":false`) {
		t.Fatalf("expected error payload, got %q", string(body))
	}
}

func TestConsoleHandler_StreamStopsOnClientCancel(t *testing.T) {
	oldKeepAlive := consoleStreamKeepAliveInterval
	oldStat := consoleStreamStatInterval
	oldSleep := consoleStreamIdleSleep
	consoleStreamKeepAliveInterval = 100 * time.Millisecond
	consoleStreamStatInterval = 50 * time.Millisecond
	consoleStreamIdleSleep = 10 * time.Millisecond
	t.Cleanup(func() {
		consoleStreamKeepAliveInterval = oldKeepAlive
		consoleStreamStatInterval = oldStat
		consoleStreamIdleSleep = oldSleep
	})

	ws := t.TempDir()
	traceDir := filepath.Join(ws, ".x-claw", "audit", "runs", "feishu_oc_test")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		t.Fatalf("mkdir trace: %v", err)
	}
	path := filepath.Join(traceDir, "events.jsonl")
	if err := os.WriteFile(path, []byte("line-one\n"), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}

	h := NewConsoleHandler(ConsoleHandlerOptions{Workspace: ws})
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/console/stream?path=.x-claw/audit/runs/feishu_oc_test/events.jsonl&tail=1", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	lines := make(chan string, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				lines <- line
			}
		}
	}()

	if got := readStreamLine(t, lines, time.Second); got != "line-one" {
		t.Fatalf("first line = %q, want %q", got, "line-one")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stream to stop after client cancel")
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

func TestResumeLastTaskHandler_LoopbackOnlyWhenNoAPIKey(t *testing.T) {
	h := NewResumeLastTaskHandler(ResumeLastTaskHandlerOptions{
		Resume: func(_ctx context.Context) (any, string, error) {
			return map[string]any{"id": "r1"}, "ok", nil
		},
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/api/resume_last_task", strings.NewReader(`{}`))
	req.RemoteAddr = "203.0.113.10:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestResumeLastTaskHandler_APIKeyRequiredWhenConfigured(t *testing.T) {
	h := NewResumeLastTaskHandler(ResumeLastTaskHandlerOptions{
		APIKey: "secret",
		Resume: func(_ctx context.Context) (any, string, error) {
			return map[string]any{"id": "r1"}, "ok", nil
		},
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/api/resume_last_task", strings.NewReader(`{}`))
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
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
