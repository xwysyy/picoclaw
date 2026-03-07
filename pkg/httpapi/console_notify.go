package httpapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/pkg/auditlog"
	"github.com/xwysyy/X-Claw/pkg/session"
	"github.com/xwysyy/X-Claw/pkg/utils"
)

type MessageSender interface {
	SendToChannel(ctx context.Context, channelName, chatID, content string) error
}

type NotifyHandlerOptions struct {
	Sender     MessageSender
	APIKey     string
	LastActive func() (channel string, chatID string)

	// MaxBodyBytes limits JSON request body size. If 0, defaults to 64KiB.
	MaxBodyBytes int64

	// SendTimeout bounds the enqueue/send operation. If 0, defaults to 5s.
	SendTimeout time.Duration
}

type NotifyHandler struct {
	sender      MessageSender
	apiKey      string
	lastActive  func() (string, string)
	maxBody     int64
	sendTimeout time.Duration
}

func NewNotifyHandler(opts NotifyHandlerOptions) *NotifyHandler {
	maxBody := opts.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = 64 << 10 // 64KiB
	}
	timeout := opts.SendTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &NotifyHandler{
		sender:      opts.Sender,
		apiKey:      strings.TrimSpace(opts.APIKey),
		lastActive:  opts.LastActive,
		maxBody:     maxBody,
		sendTimeout: timeout,
	}
}

type notifyRequest struct {
	Channel string `json:"channel"`
	To      string `json:"to"`
	ChatID  string `json:"chat_id"`

	Content string `json:"content"`
	Text    string `json:"text"`
	Message string `json:"message"`
	Title   string `json:"title"`
}

type notifyResponse struct {
	OK      bool   `json:"ok"`
	Channel string `json:"channel,omitempty"`
	To      string `json:"to,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (h *NotifyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, notifyResponse{
			OK:    false,
			Error: "method not allowed",
		})
		return
	}

	if h.sender == nil {
		writeJSON(w, http.StatusServiceUnavailable, notifyResponse{
			OK:    false,
			Error: "notify service not configured",
		})
		return
	}

	if !h.authorize(r) {
		writeJSON(w, http.StatusUnauthorized, notifyResponse{
			OK:    false,
			Error: "unauthorized",
		})
		return
	}

	var req notifyRequest
	if err := readJSON(w, r, h.maxBody, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, notifyResponse{
			OK:    false,
			Error: err.Error(),
		})
		return
	}

	channel := strings.TrimSpace(req.Channel)
	to := strings.TrimSpace(req.To)
	if to == "" {
		to = strings.TrimSpace(req.ChatID)
	}

	lastCh, lastTo := "", ""
	if h.lastActive != nil {
		lastCh, lastTo = h.lastActive()
	}

	// If neither provided, default to last active conversation.
	if channel == "" && to == "" {
		channel, to = strings.TrimSpace(lastCh), strings.TrimSpace(lastTo)
	}

	if channel == "" {
		writeJSON(w, http.StatusBadRequest, notifyResponse{
			OK:    false,
			Error: "channel is required (or omit both channel/to to use last active)",
		})
		return
	}

	if to == "" && channel == strings.TrimSpace(lastCh) {
		to = strings.TrimSpace(lastTo)
	}
	if to == "" {
		writeJSON(w, http.StatusBadRequest, notifyResponse{
			OK:    false,
			Error: "to/chat_id is required (or omit both channel/to to use last active)",
		})
		return
	}

	content := strings.TrimSpace(req.Content)
	if content == "" {
		content = strings.TrimSpace(req.Text)
	}
	if content == "" {
		content = strings.TrimSpace(req.Message)
	}
	title := strings.TrimSpace(req.Title)
	if title != "" {
		if content != "" {
			content = title + "\n\n" + content
		} else {
			content = title
		}
	}

	if strings.TrimSpace(content) == "" {
		writeJSON(w, http.StatusBadRequest, notifyResponse{
			OK:    false,
			Error: "content is required",
		})
		return
	}

	sendCtx, cancel := context.WithTimeout(r.Context(), h.sendTimeout)
	defer cancel()
	if err := h.sender.SendToChannel(sendCtx, channel, to, content); err != nil {
		writeJSON(w, http.StatusInternalServerError, notifyResponse{
			OK:      false,
			Channel: channel,
			To:      to,
			Error:   err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, notifyResponse{
		OK:      true,
		Channel: channel,
		To:      to,
	})
}

func (h *NotifyHandler) authorize(r *http.Request) bool {
	if strings.TrimSpace(h.apiKey) == "" {
		return isLoopbackRemote(r.RemoteAddr)
	}

	if strings.TrimSpace(r.Header.Get("X-API-Key")) == h.apiKey {
		return true
	}

	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		token := strings.TrimSpace(auth[7:])
		return token != "" && token == h.apiKey
	}

	return false
}

func isLoopbackRemote(remoteAddr string) bool {
	host := strings.TrimSpace(remoteAddr)
	if host == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

func authorizeAPIKeyOrLoopback(apiKey string, r *http.Request) bool {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return isLoopbackRemote(r.RemoteAddr)
	}

	if strings.TrimSpace(r.Header.Get("X-API-Key")) == apiKey {
		return true
	}

	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		token := strings.TrimSpace(auth[7:])
		return token != "" && token == apiKey
	}

	return false
}

type ResumeLastTaskHandlerOptions struct {
	APIKey string

	// Resume performs the actual resume operation.
	// It should be safe to call multiple times (idempotency/confirmation lives in tools policy).
	Resume func(ctx context.Context) (candidate any, response string, err error)

	// MaxBodyBytes limits request body size (reserved for future flags). Defaults to 8KiB.
	MaxBodyBytes int64

	// Timeout bounds the resume operation. Defaults to 30s.
	Timeout time.Duration
}

type ResumeLastTaskHandler struct {
	apiKey  string
	resume  func(ctx context.Context) (any, string, error)
	maxBody int64
	timeout time.Duration
}

func NewResumeLastTaskHandler(opts ResumeLastTaskHandlerOptions) *ResumeLastTaskHandler {
	maxBody := opts.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = 8 << 10
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &ResumeLastTaskHandler{
		apiKey:  strings.TrimSpace(opts.APIKey),
		resume:  opts.Resume,
		maxBody: maxBody,
		timeout: timeout,
	}
}

type resumeLastTaskResponse struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	Candidate any    `json:"candidate,omitempty"`
	Response  string `json:"response,omitempty"`
}

func (h *ResumeLastTaskHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, resumeLastTaskResponse{OK: false, Error: "method not allowed"})
		return
	}

	if h.resume == nil {
		writeJSON(w, http.StatusServiceUnavailable, resumeLastTaskResponse{OK: false, Error: "resume service not configured"})
		return
	}

	// Reuse same auth policy as /api/notify.
	if strings.TrimSpace(h.apiKey) == "" {
		if !isLoopbackRemote(r.RemoteAddr) {
			writeJSON(w, http.StatusUnauthorized, resumeLastTaskResponse{OK: false, Error: "unauthorized"})
			return
		}
	} else {
		authorized := strings.TrimSpace(r.Header.Get("X-API-Key")) == h.apiKey
		if !authorized {
			auth := strings.TrimSpace(r.Header.Get("Authorization"))
			if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
				token := strings.TrimSpace(auth[7:])
				authorized = token != "" && token == h.apiKey
			}
		}
		if !authorized {
			writeJSON(w, http.StatusUnauthorized, resumeLastTaskResponse{OK: false, Error: "unauthorized"})
			return
		}
	}

	var body map[string]any
	if err := readJSON(w, r, h.maxBody, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, resumeLastTaskResponse{OK: false, Error: err.Error()})
		return
	}

	resumeCtx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	type resumeResult struct {
		candidate any
		response  string
		err       error
	}
	done := make(chan resumeResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- resumeResult{err: fmt.Errorf("resume panic: %v", r)}
			}
		}()
		candidate, response, err := h.resume(resumeCtx)
		done <- resumeResult{candidate: candidate, response: response, err: err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			writeJSON(w, http.StatusInternalServerError, resumeLastTaskResponse{
				OK:        false,
				Error:     res.err.Error(),
				Candidate: res.candidate,
			})
			return
		}

		writeJSON(w, http.StatusOK, resumeLastTaskResponse{
			OK:        true,
			Candidate: res.candidate,
			Response:  res.response,
		})
		return

	case <-resumeCtx.Done():
		writeJSON(w, http.StatusGatewayTimeout, resumeLastTaskResponse{
			OK:    false,
			Error: "resume timeout",
		})
		return
	}
}

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
	if h == nil || h.sessions == nil {
		writeJSON(w, http.StatusServiceUnavailable, sessionModelResponse{OK: false, Error: "session_model service not configured"})
		return
	}
	if !h.enabled {
		writeJSON(w, http.StatusServiceUnavailable, sessionModelResponse{OK: false, Error: "session_model api is disabled by config"})
		return
	}
	if !authorizeAPIKeyOrLoopback(h.apiKey, r) {
		writeJSON(w, http.StatusUnauthorized, sessionModelResponse{OK: false, Error: "unauthorized"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		key := utils.CanonicalSessionKey(r.URL.Query().Get("session_key"))
		if key == "" {
			writeJSON(w, http.StatusBadRequest, sessionModelResponse{OK: false, Error: "session_key is required"})
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

		writeJSON(w, http.StatusOK, sessionModelResponse{
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
		var req sessionModelRequest
		if err := readJSON(w, r, h.maxBody, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, sessionModelResponse{OK: false, Error: err.Error()})
			return
		}

		key := utils.CanonicalSessionKey(req.SessionKey)
		if key == "" {
			writeJSON(w, http.StatusBadRequest, sessionModelResponse{OK: false, Error: "session_key is required"})
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
			writeJSON(w, http.StatusInternalServerError, sessionModelResponse{OK: false, Error: err.Error(), SessionKey: key})
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

		writeJSON(w, http.StatusOK, sessionModelResponse{
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
		writeJSON(w, http.StatusMethodNotAllowed, sessionModelResponse{OK: false, Error: "method not allowed"})
		return
	}
}
