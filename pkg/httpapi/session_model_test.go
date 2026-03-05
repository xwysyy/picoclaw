package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xwysyy/X-Claw/pkg/session"
)

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
