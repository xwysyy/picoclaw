package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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

func TestConsoleHandler_WorkspaceMissing(t *testing.T) {
	h := NewConsoleHandler(ConsoleHandlerOptions{})
	req := httptest.NewRequest(http.MethodGet, "http://example.com/console/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
}
