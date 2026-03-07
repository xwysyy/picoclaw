package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/xwysyy/X-Claw/pkg/logger"
)

type ConsoleHandlerOptions struct {
	Workspace string
	APIKey    string

	LastActive func() (channel string, chatID string)

	Info ConsoleInfo
}

type ConsoleInfo struct {
	Model                string `json:"model,omitempty"`
	NotifyOnTaskComplete bool   `json:"notify_on_task_complete,omitempty"`
	ToolTraceEnabled     bool   `json:"tool_trace_enabled,omitempty"`
	RunTraceEnabled      bool   `json:"run_trace_enabled,omitempty"`
	WebEvidenceMode      bool   `json:"web_evidence_mode,omitempty"`

	InboundQueueEnabled        bool `json:"inbound_queue_enabled,omitempty"`
	InboundQueueMaxConcurrency int  `json:"inbound_queue_max_concurrency,omitempty"`
}

// ConsoleHandler serves a minimal read-only HTML console and JSON endpoints.
//
// This is intentionally pico-scale: no external assets, no stateful backend.
// It reads from on-disk audit/state files in the workspace.
type ConsoleHandler struct {
	workspace string
	apiKey    string

	lastActive func() (string, string)
	info       ConsoleInfo

	workspaceResolved string

	staticDir string
	staticFS  http.Handler
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.WarnCF("httpapi", "json encode failed", map[string]any{"status": status, "error": err.Error()})
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, maxBody int64, dst any) error {
	if maxBody > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		return errors.New("invalid json body")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("invalid json body")
	}
	return nil
}

func NewConsoleHandler(opts ConsoleHandlerOptions) *ConsoleHandler {
	workspace := strings.TrimSpace(opts.Workspace)
	resolved := ""
	if workspace != "" {
		if abs, err := filepath.Abs(workspace); err == nil {
			workspace = abs
		}
		if rs, err := filepath.EvalSymlinks(workspace); err == nil {
			resolved = rs
		}
	}

	staticDir := pickConsoleStaticDir()
	var staticFS http.Handler
	if strings.TrimSpace(staticDir) != "" {
		staticFS = http.StripPrefix("/console", http.FileServer(http.Dir(staticDir)))
	}

	return &ConsoleHandler{
		workspace:         workspace,
		apiKey:            strings.TrimSpace(opts.APIKey),
		lastActive:        opts.LastActive,
		info:              opts.Info,
		workspaceResolved: resolved,
		staticDir:         staticDir,
		staticFS:          staticFS,
	}
}

func (h *ConsoleHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || strings.TrimSpace(h.workspace) == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok":    false,
			"error": "console is not configured (workspace missing)",
		})
		return
	}

	if strings.HasPrefix(r.URL.Path, "/api/console/") || r.URL.Path == "/api/console" {
		if !authorizeAPIKeyOrLoopback(h.apiKey, r) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"ok":    false,
				"error": "unauthorized",
			})
			return
		}
		h.serveAPI(w, r)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/console") {
		// UI should remain usable in browsers even when gateway.api_key is set.
		// When api_key is empty: loopback only.
		// When api_key is set: allow UI page loads, but keep /api/console/* protected.
		if strings.TrimSpace(h.apiKey) == "" && !isLoopbackRemote(r.RemoteAddr) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"ok":    false,
				"error": "unauthorized",
			})
			return
		}
		h.servePage(w, r)
		return
	}

	writeJSON(w, http.StatusNotFound, map[string]any{
		"ok":    false,
		"error": "not found",
	})
}

func (h *ConsoleHandler) servePage(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		// ok
	default:
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if h != nil && h.staticFS != nil && strings.TrimSpace(h.staticDir) != "" {
		h.staticFS.ServeHTTP(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.WriteString(w, consoleHTML)
}

func (h *ConsoleHandler) serveAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}

	action := strings.TrimPrefix(r.URL.Path, "/api/console/")
	action = strings.Trim(action, "/")

	switch action {
	case "", "help":
		h.writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"endpoints": []string{
				"/api/console/status",
				"/api/console/state",
				"/api/console/cron",
				"/api/console/tokens",
				"/api/console/sessions",
				"/api/console/runs",
				"/api/console/tools",
				"/api/console/file?path=<relative-path>",
				"/api/console/tail?path=<relative-path>&lines=200",
				"/api/console/stream?path=<relative-path>&tail=200",
			},
		})
		return

	case "status":
		h.handleStatus(w, r)
		return

	case "state":
		h.handleState(w, r)
		return

	case "cron":
		h.handleCron(w, r)
		return

	case "tokens":
		h.handleTokens(w, r)
		return

	case "sessions":
		h.handleSessions(w, r)
		return

	case "runs":
		h.handleTraceList(w, r, traceListOptions{
			kind:    "runs",
			baseDir: filepath.Join(h.workspace, ".x-claw", "audit", "runs"),
			eventsRel: func(token string) string {
				return filepath.ToSlash(filepath.Join(".x-claw", "audit", "runs", token, "events.jsonl"))
			},
		})
		return

	case "tools":
		h.handleTraceList(w, r, traceListOptions{
			kind:    "tools",
			baseDir: filepath.Join(h.workspace, ".x-claw", "audit", "tools"),
			eventsRel: func(token string) string {
				return filepath.ToSlash(filepath.Join(".x-claw", "audit", "tools", token, "events.jsonl"))
			},
		})
		return

	case "file":
		h.handleFile(w, r)
		return

	case "tail":
		h.handleTail(w, r)
		return

	case "stream":
		h.handleStream(w, r)
		return

	default:
		h.writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
		return
	}
}

func (h *ConsoleHandler) writeJSON(w http.ResponseWriter, status int, v any) {
	writeJSON(w, status, v)
}

func pickConsoleStaticDir() string {
	candidates := []string{}
	if home, _ := os.UserHomeDir(); strings.TrimSpace(home) != "" {
		candidates = append(candidates,
			filepath.Join(home, ".x-claw", "console"),
			filepath.Join(home, ".local", "share", "x-claw", "console"),
		)
	}
	candidates = append(candidates,
		"/usr/local/share/x-claw/console",
		filepath.Join("web", "x-claw-console", "out"),
	)
	for _, p := range candidates {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return ""
}
