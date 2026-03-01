package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
