package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
