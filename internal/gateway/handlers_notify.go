package gateway

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"
)

type MessageSender interface {
	SendToChannel(ctx context.Context, channelName, chatID, content string) error
}

type NotifyHandlerOptions struct {
	Sender     MessageSender
	APIKey     string
	LastActive func() (channel string, chatID string)
	MaxBodyBytes int64
	SendTimeout  time.Duration
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
		maxBody = 64 << 10
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
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(notifyResponse{OK: false, Error: "method not allowed"})
		return
	}
	if h.sender == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(notifyResponse{OK: false, Error: "notify service not configured"})
		return
	}
	if !authorizeAPIKeyOrLoopback(h.apiKey, r) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(notifyResponse{OK: false, Error: "unauthorized"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.maxBody)
	var req notifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(notifyResponse{OK: false, Error: "invalid json body"})
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
	if channel == "" && to == "" {
		channel, to = strings.TrimSpace(lastCh), strings.TrimSpace(lastTo)
	}
	if channel == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(notifyResponse{OK: false, Error: "channel is required (or omit both channel/to to use last active)"})
		return
	}
	if to == "" && channel == strings.TrimSpace(lastCh) {
		to = strings.TrimSpace(lastTo)
	}
	if to == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(notifyResponse{OK: false, Error: "to/chat_id is required (or omit both channel/to to use last active)"})
		return
	}

	content := strings.TrimSpace(req.Content)
	if content == "" {
		content = strings.TrimSpace(req.Text)
	}
	if content == "" {
		content = strings.TrimSpace(req.Message)
	}
	if title := strings.TrimSpace(req.Title); title != "" {
		if content != "" {
			content = title + "\n\n" + content
		} else {
			content = title
		}
	}
	if content == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(notifyResponse{OK: false, Error: "content is required"})
		return
	}

	sendCtx, cancel := context.WithTimeout(r.Context(), h.sendTimeout)
	defer cancel()
	if err := h.sender.SendToChannel(sendCtx, channel, to, content); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(notifyResponse{OK: false, Channel: channel, To: to, Error: err.Error()})
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(notifyResponse{OK: true, Channel: channel, To: to})
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
