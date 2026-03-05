package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/pkg/auditlog"
	"github.com/xwysyy/X-Claw/pkg/session"
	"github.com/xwysyy/X-Claw/pkg/utils"
)

type SessionModelHandlerOptions struct {
	APIKey string

	// Workspace is the X-Claw workspace root (used for audit log).
	// When empty, audit logging is skipped.
	Workspace string

	// Sessions is required to read/write per-session model overrides.
	Sessions session.Store

	// Enabled allows turning the handler on/off via config.
	Enabled bool

	// MaxBodyBytes limits JSON request body size. If 0, defaults to 8KiB.
	MaxBodyBytes int64
}

type SessionModelHandler struct {
	apiKey    string
	workspace string
	sessions  session.Store
	enabled   bool
	maxBody   int64
}

func NewSessionModelHandler(opts SessionModelHandlerOptions) *SessionModelHandler {
	maxBody := opts.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = 8 << 10
	}
	return &SessionModelHandler{
		apiKey:    strings.TrimSpace(opts.APIKey),
		workspace: strings.TrimSpace(opts.Workspace),
		sessions:  opts.Sessions,
		enabled:   opts.Enabled,
		maxBody:   maxBody,
	}
}

type sessionModelRequest struct {
	SessionKey string `json:"session_key"`
	Model      string `json:"model"`
	TTLMinutes int    `json:"ttl_minutes,omitempty"`
}

type sessionModelResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`

	SessionKey string `json:"session_key,omitempty"`

	HasOverride bool   `json:"has_override,omitempty"`
	Model       string `json:"model,omitempty"`

	ExpiresAt   string `json:"expires_at,omitempty"`
	ExpiresAtMS *int64 `json:"expires_at_ms,omitempty"`

	Timestamp string `json:"timestamp,omitempty"`
}

func (h *SessionModelHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if h == nil || h.sessions == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(sessionModelResponse{OK: false, Error: "session_model service not configured"})
		return
	}
	if !h.enabled {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(sessionModelResponse{OK: false, Error: "session_model api is disabled by config"})
		return
	}
	if !authorizeAPIKeyOrLoopback(h.apiKey, r) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(sessionModelResponse{OK: false, Error: "unauthorized"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		key := utils.CanonicalSessionKey(r.URL.Query().Get("session_key"))
		if key == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(sessionModelResponse{OK: false, Error: "session_key is required"})
			return
		}

		model, ok := h.sessions.EffectiveModelOverride(key)

		expiresAt := ""
		var expiresAtMS *int64
		if snap, exists := h.sessions.GetSessionSnapshot(key); exists && snap != nil && snap.ModelOverrideExpiresAtMS != nil && *snap.ModelOverrideExpiresAtMS > 0 {
			ms := *snap.ModelOverrideExpiresAtMS
			expiresAtMS = &ms
			expiresAt = time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(sessionModelResponse{
			OK:          true,
			SessionKey:  key,
			HasOverride: ok,
			Model:       model,
			ExpiresAt:   expiresAt,
			ExpiresAtMS: expiresAtMS,
			Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		})
		return

	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, h.maxBody)
		var req sessionModelRequest
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(sessionModelResponse{OK: false, Error: "invalid json body"})
			return
		}

		key := utils.CanonicalSessionKey(req.SessionKey)
		if key == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(sessionModelResponse{OK: false, Error: "session_key is required"})
			return
		}

		ttlMinutes := req.TTLMinutes
		if ttlMinutes < 0 {
			ttlMinutes = 0
		}

		model := strings.TrimSpace(req.Model)
		normalized := strings.ToLower(strings.TrimSpace(model))

		var expiresAt *time.Time
		var err error
		action := "set"
		switch normalized {
		case "", "default", "clear", "off":
			action = "clear"
			_, err = h.sessions.ClearModelOverride(key)
		default:
			ttl := time.Duration(ttlMinutes) * time.Minute
			expiresAt, err = h.sessions.SetModelOverride(key, model, ttl)
		}
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(sessionModelResponse{OK: false, Error: err.Error(), SessionKey: key})
			return
		}

		// Read back effective state (also clears expired overrides).
		effective, has := h.sessions.EffectiveModelOverride(key)
		expiresAtText := ""
		var expiresAtMS *int64
		if expiresAt != nil {
			expiresAtText = expiresAt.UTC().Format(time.RFC3339Nano)
			ms := expiresAt.UnixMilli()
			expiresAtMS = &ms
		} else if snap, exists := h.sessions.GetSessionSnapshot(key); exists && snap != nil && snap.ModelOverrideExpiresAtMS != nil && *snap.ModelOverrideExpiresAtMS > 0 {
			ms := *snap.ModelOverrideExpiresAtMS
			expiresAtMS = &ms
			expiresAtText = time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
		}

		if strings.TrimSpace(h.workspace) != "" {
			note := fmt.Sprintf("action=%s model=%q ttl_minutes=%d", action, effective, ttlMinutes)
			if expiresAtText != "" {
				note += " expires_at=" + expiresAtText
			}
			auditlog.Record(h.workspace, auditlog.Event{
				Type:       "session.model_override." + action,
				Source:     "httpapi",
				SessionKey: key,
				Note:       note,
			})
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(sessionModelResponse{
			OK:          true,
			SessionKey:  key,
			HasOverride: has,
			Model:       effective,
			ExpiresAt:   expiresAtText,
			ExpiresAtMS: expiresAtMS,
			Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		})
		return

	default:
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(sessionModelResponse{OK: false, Error: "method not allowed"})
		return
	}
}
