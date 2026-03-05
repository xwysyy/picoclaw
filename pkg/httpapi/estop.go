package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/pkg/auditlog"
	"github.com/xwysyy/X-Claw/pkg/tools"
)

type EstopHandlerOptions struct {
	APIKey string

	// Workspace is the X-Claw workspace root (where .x-claw/state/estop.json lives).
	Workspace string

	Enabled    bool
	FailClosed bool

	// MaxBodyBytes limits JSON request body size. If 0, defaults to 8KiB.
	MaxBodyBytes int64
}

type EstopHandler struct {
	apiKey    string
	workspace string
	enabled   bool
	failClose bool
	maxBody   int64
}

func NewEstopHandler(opts EstopHandlerOptions) *EstopHandler {
	maxBody := opts.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = 8 << 10
	}
	return &EstopHandler{
		apiKey:    strings.TrimSpace(opts.APIKey),
		workspace: strings.TrimSpace(opts.Workspace),
		enabled:   opts.Enabled,
		failClose: opts.FailClosed,
		maxBody:   maxBody,
	}
}

type estopRequest struct {
	Mode           string   `json:"mode"`
	BlockedDomains []string `json:"blocked_domains,omitempty"`
	FrozenTools    []string `json:"frozen_tools,omitempty"`
	FrozenPrefixes []string `json:"frozen_prefixes,omitempty"`
	Note           string   `json:"note,omitempty"`
}

type estopResponse struct {
	OK        bool             `json:"ok"`
	Error     string           `json:"error,omitempty"`
	State     tools.EstopState `json:"state,omitempty"`
	Workspace string           `json:"workspace,omitempty"`
	Timestamp string           `json:"timestamp,omitempty"`
}

func (h *EstopHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if h == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(estopResponse{OK: false, Error: "estop service not configured"})
		return
	}
	if !h.enabled {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(estopResponse{OK: false, Error: "estop is disabled by config"})
		return
	}
	if strings.TrimSpace(h.workspace) == "" {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(estopResponse{OK: false, Error: "workspace is not configured"})
		return
	}

	// Reuse same auth policy as /api/notify.
	if strings.TrimSpace(h.apiKey) == "" {
		if !isLoopbackRemote(r.RemoteAddr) {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(estopResponse{OK: false, Error: "unauthorized"})
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
			_ = json.NewEncoder(w).Encode(estopResponse{OK: false, Error: "unauthorized"})
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		st, err := tools.LoadEstopState(h.workspace)
		if err != nil && h.failClose {
			st = tools.EstopState{Mode: tools.EstopModeKillAll, Note: "fail-closed: " + err.Error()}.Normalized()
			err = nil
		}
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(estopResponse{OK: false, Error: err.Error(), Workspace: h.workspace})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(estopResponse{
			OK:        true,
			State:     st,
			Workspace: h.workspace,
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		})
		return

	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, h.maxBody)
		var req estopRequest
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(estopResponse{OK: false, Error: "invalid json body"})
			return
		}
		mode := strings.TrimSpace(req.Mode)
		if mode == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(estopResponse{OK: false, Error: "mode is required"})
			return
		}

		normalizedMode := strings.ToLower(strings.TrimSpace(mode))
		switch normalizedMode {
		case string(tools.EstopModeOff):
			// ok
		case string(tools.EstopModeKillAll), "on", "kill":
			normalizedMode = string(tools.EstopModeKillAll)
		case string(tools.EstopModeNetworkKill), "network":
			normalizedMode = string(tools.EstopModeNetworkKill)
		default:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(estopResponse{OK: false, Error: "invalid mode (expected off|kill_all|network_kill)"})
			return
		}

		st := tools.EstopState{
			Mode:           tools.EstopMode(normalizedMode),
			BlockedDomains: req.BlockedDomains,
			FrozenTools:    req.FrozenTools,
			FrozenPrefixes: req.FrozenPrefixes,
			Note:           req.Note,
		}

		saved, err := tools.SaveEstopState(h.workspace, st)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(estopResponse{OK: false, Error: err.Error(), Workspace: h.workspace})
			return
		}

		auditlog.Record(h.workspace, auditlog.Event{
			Type:   "estop.set",
			Source: "httpapi",
			Note: fmt.Sprintf(
				"mode=%s blocked_domains=%d frozen_tools=%d frozen_prefixes=%d note=%q",
				saved.Mode,
				len(saved.BlockedDomains),
				len(saved.FrozenTools),
				len(saved.FrozenPrefixes),
				strings.TrimSpace(saved.Note),
			),
		})

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(estopResponse{
			OK:        true,
			State:     saved,
			Workspace: h.workspace,
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		})
		return

	default:
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(estopResponse{OK: false, Error: "method not allowed"})
		return
	}
}
