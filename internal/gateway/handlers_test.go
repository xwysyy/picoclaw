package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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

	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, 1, sender.calls)
	require.Equal(t, "feishu", sender.lastChannel)
	require.Equal(t, "oc_test", sender.lastTo)
	require.Equal(t, "done", sender.lastContent)
}

func TestResumeLastTaskHandler_Timeout(t *testing.T) {
	h := NewResumeLastTaskHandler(ResumeLastTaskHandlerOptions{
		Timeout: 20 * time.Millisecond,
		Resume: func(_ context.Context) (any, string, error) {
			time.Sleep(50 * time.Millisecond)
			return nil, "", nil
		},
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/api/resume_last_task", strings.NewReader(`{}`))
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusGatewayTimeout, rr.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Equal(t, false, resp["ok"])
	require.Equal(t, "resume timeout", resp["error"])
}

func TestConsoleHandler_WorkspaceMissing(t *testing.T) {
	h := NewConsoleHandler(ConsoleHandlerOptions{})
	req := httptest.NewRequest(http.MethodGet, "http://example.com/console/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}
