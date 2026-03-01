package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthHandler_AlwaysOK(t *testing.T) {
	s := NewServer("127.0.0.1", 0)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	s.healthHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code: got %d want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type: got %q want application/json", ct)
	}

	var resp StatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v; body=%q", err, rr.Body.String())
	}
	if resp.Status != "ok" {
		t.Fatalf("status: got %q want %q", resp.Status, "ok")
	}
	if strings.TrimSpace(resp.Uptime) == "" {
		t.Fatalf("uptime should be non-empty; got %q", resp.Uptime)
	}
}

func TestReadyHandler_NotReadyWhenFlagFalse(t *testing.T) {
	s := NewServer("127.0.0.1", 0)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	s.readyHandler(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code: got %d want %d", rr.Code, http.StatusServiceUnavailable)
	}

	var resp StatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v; body=%q", err, rr.Body.String())
	}
	if resp.Status != "not ready" {
		t.Fatalf("status: got %q want %q", resp.Status, "not ready")
	}
	if len(resp.Checks) != 0 {
		t.Fatalf("expected no checks, got %d", len(resp.Checks))
	}
}

func TestReadyHandler_ReadyWhenFlagTrueAndNoFailingChecks(t *testing.T) {
	s := NewServer("127.0.0.1", 0)
	s.SetReady(true)

	s.RegisterCheck("ok-check", func() (bool, string) { return true, "ok" })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	s.readyHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code: got %d want %d", rr.Code, http.StatusOK)
	}

	var resp StatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v; body=%q", err, rr.Body.String())
	}
	if resp.Status != "ready" {
		t.Fatalf("status: got %q want %q", resp.Status, "ready")
	}
	if strings.TrimSpace(resp.Uptime) == "" {
		t.Fatalf("uptime should be non-empty; got %q", resp.Uptime)
	}
	if resp.Checks == nil || len(resp.Checks) != 1 {
		t.Fatalf("expected exactly 1 check, got %#v", resp.Checks)
	}
	check := resp.Checks["ok-check"]
	if check.Name != "ok-check" || check.Status != "ok" || check.Message != "ok" {
		t.Fatalf("unexpected check payload: %#v", check)
	}
}

func TestReadyHandler_NotReadyWhenAnyCheckFails(t *testing.T) {
	s := NewServer("127.0.0.1", 0)
	s.SetReady(true)

	s.RegisterCheck("db", func() (bool, string) { return false, "down" })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	s.readyHandler(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status code: got %d want %d", rr.Code, http.StatusServiceUnavailable)
	}

	var resp StatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v; body=%q", err, rr.Body.String())
	}
	if resp.Status != "not ready" {
		t.Fatalf("status: got %q want %q", resp.Status, "not ready")
	}
	if resp.Checks == nil || len(resp.Checks) != 1 {
		t.Fatalf("expected exactly 1 check in response, got %#v", resp.Checks)
	}
	check := resp.Checks["db"]
	if check.Status != "fail" || check.Message != "down" {
		t.Fatalf("unexpected check payload: %#v", check)
	}
}
