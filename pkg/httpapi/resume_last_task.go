package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

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
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(resumeLastTaskResponse{OK: false, Error: "method not allowed"})
		return
	}

	if h.resume == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(resumeLastTaskResponse{OK: false, Error: "resume service not configured"})
		return
	}

	// Reuse same auth policy as /api/notify.
	if strings.TrimSpace(h.apiKey) == "" {
		if !isLoopbackRemote(r.RemoteAddr) {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(resumeLastTaskResponse{OK: false, Error: "unauthorized"})
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
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(resumeLastTaskResponse{OK: false, Error: "unauthorized"})
			return
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.maxBody)
	// Reserved for future flags; ignore body for now.
	_ = json.NewDecoder(r.Body).Decode(&map[string]any{})

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
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(resumeLastTaskResponse{
				OK:        false,
				Error:     res.err.Error(),
				Candidate: res.candidate,
			})
			return
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resumeLastTaskResponse{
			OK:        true,
			Candidate: res.candidate,
			Response:  res.response,
		})
		return

	case <-resumeCtx.Done():
		w.WriteHeader(http.StatusGatewayTimeout)
		_ = json.NewEncoder(w).Encode(resumeLastTaskResponse{
			OK:    false,
			Error: "resume timeout",
		})
		return
	}
}
