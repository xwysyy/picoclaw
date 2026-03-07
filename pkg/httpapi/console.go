package httpapi

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/pkg/cron"
	"github.com/xwysyy/X-Claw/pkg/utils"
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": "console is not configured (workspace missing)",
		})
		return
	}

	if strings.HasPrefix(r.URL.Path, "/api/console/") || r.URL.Path == "/api/console" {
		if !authorizeAPIKeyOrLoopback(h.apiKey, r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
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
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":    false,
				"error": "unauthorized",
			})
			return
		}
		h.servePage(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]any{
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "method not allowed"})
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *ConsoleHandler) loadState() (any, error) {
	statePath := filepath.Join(h.workspace, "state", "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil, err
	}
	var obj any
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("invalid state json")
	}
	return obj, nil
}

func (h *ConsoleHandler) summarizeCron() map[string]any {
	storePath := filepath.Join(h.workspace, "cron", "jobs.json")
	st, err := os.Stat(storePath)
	if err != nil {
		return map[string]any{
			"path":    filepath.ToSlash(filepath.Join("cron", "jobs.json")),
			"exists":  false,
			"jobs":    0,
			"modTime": "",
		}
	}

	jobsCount := 0
	if data, err := os.ReadFile(storePath); err == nil {
		var store cron.CronStore
		if json.Unmarshal(data, &store) == nil {
			jobsCount = len(store.Jobs)
		}
	}

	return map[string]any{
		"path":    filepath.ToSlash(filepath.Join("cron", "jobs.json")),
		"exists":  true,
		"jobs":    jobsCount,
		"modTime": st.ModTime().UTC().Format(time.RFC3339Nano),
		"size":    st.Size(),
	}
}

func (h *ConsoleHandler) countTraceSessions(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, ent.Name(), "events.jsonl")); err == nil {
			n++
		}
	}
	return n
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

func (h *ConsoleHandler) handleStatus(w http.ResponseWriter, _ *http.Request) {
	now := time.Now().UTC()

	lastCh, lastTo := "", ""
	if h.lastActive != nil {
		lastCh, lastTo = h.lastActive()
	}

	state, _ := h.loadState()
	cronSummary := h.summarizeCron()

	runsCount := h.countTraceSessions(filepath.Join(h.workspace, ".x-claw", "audit", "runs"))
	toolsCount := h.countTraceSessions(filepath.Join(h.workspace, ".x-claw", "audit", "tools"))

	h.writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"now":       now.Format(time.RFC3339Nano),
		"workspace": h.workspace,
		"info":      h.info,
		"last_active": map[string]any{
			"channel": strings.TrimSpace(lastCh),
			"chat_id": strings.TrimSpace(lastTo),
			"state":   state,
		},
		"cron":  cronSummary,
		"runs":  map[string]any{"sessions": runsCount, "base_dir": filepath.ToSlash(filepath.Join(".x-claw", "audit", "runs"))},
		"tools": map[string]any{"sessions": toolsCount, "base_dir": filepath.ToSlash(filepath.Join(".x-claw", "audit", "tools"))},
		"links": map[string]any{
			"health": "/health",
			"ready":  "/ready",
			"notify": "/api/notify",
		},
	})
}

func (h *ConsoleHandler) handleState(w http.ResponseWriter, _ *http.Request) {
	state, err := h.loadState()
	if err != nil {
		h.writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": state})
}

func (h *ConsoleHandler) handleCron(w http.ResponseWriter, _ *http.Request) {
	storePath := filepath.Join(h.workspace, "cron", "jobs.json")

	data, err := os.ReadFile(storePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			h.writeJSON(w, http.StatusOK, map[string]any{
				"ok":   true,
				"path": filepath.ToSlash(filepath.Join("cron", "jobs.json")),
				"jobs": []any{},
			})
			return
		}
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	var store cron.CronStore
	if err := json.Unmarshal(data, &store); err != nil {
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "invalid cron store json"})
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"path":    filepath.ToSlash(filepath.Join("cron", "jobs.json")),
		"version": store.Version,
		"jobs":    store.Jobs,
	})
}

func (h *ConsoleHandler) handleTokens(w http.ResponseWriter, _ *http.Request) {
	storePath := filepath.Join(h.workspace, "state", "token_usage.json")

	data, err := os.ReadFile(storePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			h.writeJSON(w, http.StatusOK, map[string]any{
				"ok":   true,
				"path": filepath.ToSlash(filepath.Join("state", "token_usage.json")),
				"data": map[string]any{
					"version":  1,
					"totals":   map[string]any{"requests": 0, "prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
					"by_model": map[string]any{},
				},
			})
			return
		}
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "invalid token usage json"})
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"path": filepath.ToSlash(filepath.Join("state", "token_usage.json")),
		"data": payload,
	})
}

type sessionListItem struct {
	Key           string `json:"key"`
	Summary       string `json:"summary,omitempty"`
	ActiveAgentID string `json:"active_agent_id,omitempty"`
	Created       string `json:"created,omitempty"`
	Updated       string `json:"updated,omitempty"`
	File          string `json:"file,omitempty"`
	EventsFile    string `json:"events_file,omitempty"`
}

func (h *ConsoleHandler) handleSessions(w http.ResponseWriter, _ *http.Request) {
	sessionsDir := filepath.Join(h.workspace, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			h.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": []sessionListItem{}})
			return
		}
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	items := make([]sessionListItem, 0, len(entries))
	seenBase := make(map[string]struct{}, len(entries))
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := strings.TrimSpace(ent.Name())
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".meta.json") {
			continue
		}

		path := filepath.Join(sessionsDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var meta struct {
			Key           string    `json:"key"`
			Summary       string    `json:"summary,omitempty"`
			ActiveAgentID string    `json:"active_agent_id,omitempty"`
			Created       time.Time `json:"created"`
			Updated       time.Time `json:"updated"`
		}
		if json.Unmarshal(data, &meta) != nil {
			continue
		}

		base := strings.TrimSuffix(name, name[len(name)-len(".meta.json"):])
		if base != "" {
			seenBase[base] = struct{}{}
		}

		item := sessionListItem{
			Key:           strings.TrimSpace(meta.Key),
			Summary:       strings.TrimSpace(meta.Summary),
			ActiveAgentID: strings.TrimSpace(meta.ActiveAgentID),
			Created:       meta.Created.UTC().Format(time.RFC3339Nano),
			Updated:       meta.Updated.UTC().Format(time.RFC3339Nano),
			File:          filepath.ToSlash(filepath.Join("sessions", name)),
		}
		if base != "" {
			eventsName := base + ".jsonl"
			if _, err := os.Stat(filepath.Join(sessionsDir, eventsName)); err == nil {
				item.EventsFile = filepath.ToSlash(filepath.Join("sessions", eventsName))
			}
		}
		items = append(items, item)
	}

	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := strings.TrimSpace(ent.Name())
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".json") || strings.HasSuffix(lower, ".meta.json") {
			continue
		}
		base := strings.TrimSuffix(name, name[len(name)-len(".json"):])
		if _, ok := seenBase[base]; ok {
			continue
		}

		path := filepath.Join(sessionsDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var legacy struct {
			Key           string    `json:"key"`
			Summary       string    `json:"summary,omitempty"`
			ActiveAgentID string    `json:"active_agent_id,omitempty"`
			Created       time.Time `json:"created"`
			Updated       time.Time `json:"updated"`
		}
		if json.Unmarshal(data, &legacy) != nil {
			continue
		}

		item := sessionListItem{
			Key:           strings.TrimSpace(legacy.Key),
			Summary:       strings.TrimSpace(legacy.Summary),
			ActiveAgentID: strings.TrimSpace(legacy.ActiveAgentID),
			Created:       legacy.Created.UTC().Format(time.RFC3339Nano),
			Updated:       legacy.Updated.UTC().Format(time.RFC3339Nano),
			File:          filepath.ToSlash(filepath.Join("sessions", name)),
		}
		if _, err := os.Stat(filepath.Join(sessionsDir, base+".jsonl")); err == nil {
			item.EventsFile = filepath.ToSlash(filepath.Join("sessions", base+".jsonl"))
		}
		items = append(items, item)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Updated == items[j].Updated {
			return items[i].Key < items[j].Key
		}
		return items[i].Updated > items[j].Updated
	})

	h.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items})
}

type traceListOptions struct {
	kind      string
	baseDir   string
	eventsRel func(token string) string
}

type traceSessionItem struct {
	Token      string `json:"token"`
	Kind       string `json:"kind"`
	SessionKey string `json:"session_key,omitempty"`
	Channel    string `json:"channel,omitempty"`
	ChatID     string `json:"chat_id,omitempty"`
	RunID      string `json:"run_id,omitempty"`

	EventsPath string `json:"events_path"`
	EventsSize int64  `json:"events_size_bytes,omitempty"`
	ModTime    string `json:"mod_time,omitempty"`

	LastEventType string `json:"last_event_type,omitempty"`
	LastEventTS   string `json:"last_event_ts,omitempty"`
	LastEventTSMS int64  `json:"last_event_ts_ms,omitempty"`
}

func (h *ConsoleHandler) handleTraceList(w http.ResponseWriter, _ *http.Request, opts traceListOptions) {
	items, err := listTraceSessions(opts)
	if err != nil {
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"kind":  opts.kind,
		"items": items,
	})
}

func listTraceSessions(opts traceListOptions) ([]traceSessionItem, error) {
	entries, err := os.ReadDir(opts.baseDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []traceSessionItem{}, nil
		}
		return nil, err
	}

	items := make([]traceSessionItem, 0, len(entries))
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		token := ent.Name()
		if token == "" {
			continue
		}
		eventsPath := filepath.Join(opts.baseDir, token, "events.jsonl")
		st, err := os.Stat(eventsPath)
		if err != nil {
			continue
		}

		sessionKey, channel, chatID, runID := "", "", "", ""
		if line, err := readFirstNonEmptyLine(eventsPath, 64<<10); err == nil {
			var meta struct {
				SessionKey string `json:"session_key"`
				Channel    string `json:"channel"`
				ChatID     string `json:"chat_id"`
				RunID      string `json:"run_id"`
			}
			if json.Unmarshal([]byte(line), &meta) == nil {
				sessionKey = utils.CanonicalSessionKey(meta.SessionKey)
				channel = strings.TrimSpace(meta.Channel)
				chatID = strings.TrimSpace(meta.ChatID)
				runID = strings.TrimSpace(meta.RunID)
			}
		}

		lastType, lastTS, lastTSMS := "", "", int64(0)
		if line, err := readLastNonEmptyLine(eventsPath, 64<<10); err == nil {
			var meta struct {
				Type string `json:"type"`
				TS   string `json:"ts"`
				TSMS int64  `json:"ts_ms"`
			}
			if json.Unmarshal([]byte(line), &meta) == nil {
				lastType = strings.TrimSpace(meta.Type)
				lastTS = strings.TrimSpace(meta.TS)
				lastTSMS = meta.TSMS
			}
		}

		items = append(items, traceSessionItem{
			Token:      token,
			Kind:       opts.kind,
			SessionKey: sessionKey,
			Channel:    channel,
			ChatID:     chatID,
			RunID:      runID,

			EventsPath: opts.eventsRel(token),
			EventsSize: st.Size(),
			ModTime:    st.ModTime().UTC().Format(time.RFC3339Nano),

			LastEventType: lastType,
			LastEventTS:   lastTS,
			LastEventTSMS: lastTSMS,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].ModTime == items[j].ModTime {
			return items[i].Token < items[j].Token
		}
		return items[i].ModTime > items[j].ModTime
	})

	return items, nil
}

func readFirstNonEmptyLine(path string, maxBytes int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	r := bufio.NewReader(f)
	limit := maxBytes
	if limit <= 0 {
		limit = 64 << 10
	}

	var total int
	for {
		line, err := r.ReadString('\n')
		total += len(line)
		if total > limit {
			return "", fmt.Errorf("line too long")
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed, nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", io.EOF
			}
			return "", err
		}
	}
}

func readLastNonEmptyLine(path string, maxBytes int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if st.Size() <= 0 {
		return "", io.EOF
	}

	n := int64(maxBytes)
	if n <= 0 {
		n = 64 << 10
	}
	if n > st.Size() {
		n = st.Size()
	}

	if _, err := f.Seek(-n, io.SeekEnd); err != nil {
		return "", err
	}

	buf := make([]byte, n)
	if _, err := io.ReadFull(f, buf); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}

	s := strings.TrimSpace(string(buf))
	if s == "" {
		return "", io.EOF
	}

	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed != "" {
			return trimmed, nil
		}
	}
	return "", io.EOF
}

func (h *ConsoleHandler) handleFile(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	if rel == "" {
		h.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "path is required"})
		return
	}

	abs, relClean, err := h.resolveConsolePath(rel)
	if err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	st, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			h.writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "file not found"})
			return
		}
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if st.IsDir() {
		h.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "path must be a file"})
		return
	}

	name := filepath.Base(relClean)
	w.Header().Set("Content-Type", contentTypeForExt(filepath.Ext(name)))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	http.ServeFile(w, r, abs)
}

func (h *ConsoleHandler) handleTail(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	if rel == "" {
		h.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "path is required"})
		return
	}

	lines := 200
	if v := strings.TrimSpace(r.URL.Query().Get("lines")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			lines = n
		}
	}
	if lines <= 0 {
		lines = 200
	}
	if lines > 500 {
		lines = 500
	}

	abs, relClean, err := h.resolveConsolePath(rel)
	if err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	content, truncated, err := tailLines(abs, lines, 1<<20)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			h.writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "file not found"})
			return
		}
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"path":            relClean,
		"lines_requested": lines,
		"truncated":       truncated,
		"lines":           content,
	})
}

func (h *ConsoleHandler) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "method not allowed"})
		return
	}

	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	if rel == "" {
		h.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "path is required"})
		return
	}

	tail := 200
	if v := strings.TrimSpace(r.URL.Query().Get("tail")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			tail = n
		}
	}
	if tail < 0 {
		tail = 0
	}
	if tail > 500 {
		tail = 500
	}

	abs, relClean, err := h.resolveConsolePath(rel)
	if err != nil {
		h.writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	f, err := os.Open(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			h.writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "file not found"})
			return
		}
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer f.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		h.writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "streaming not supported"})
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if tail > 0 {
		const maxBytes = int64(1 << 20)
		if st, err := f.Stat(); err == nil && st != nil && st.Size() > 0 {
			start := st.Size() - maxBytes
			if start < 0 {
				start = 0
			}
			if _, err := f.Seek(start, io.SeekStart); err == nil {
				buf, _ := io.ReadAll(f)
				lines := strings.Split(string(buf), "\n")
				if start > 0 && len(lines) > 0 {
					lines = lines[1:]
				}
				trimmed := make([]string, 0, len(lines))
				for _, line := range lines {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}
					trimmed = append(trimmed, line)
				}
				if len(trimmed) > tail {
					trimmed = trimmed[len(trimmed)-tail:]
				}
				for _, line := range trimmed {
					_, _ = io.WriteString(w, line+"\n")
				}
				flusher.Flush()
			}
		}
	}

	reader := bufio.NewReader(f)
	lastKeepAlive := time.Now()
	lastStat := time.Now()
	var lastSize int64
	if st, err := f.Stat(); err == nil && st != nil {
		lastSize = st.Size()
	}

	for {
		select {
		case <-r.Context().Done():
			return
		default:
			line, err := reader.ReadString('\n')
			if err == nil {
				line = strings.TrimSpace(line)
				if line != "" {
					_, _ = io.WriteString(w, line+"\n")
					flusher.Flush()
				}
				continue
			}

			if !errors.Is(err, io.EOF) {
				_, _ = io.WriteString(w, fmt.Sprintf("{\"ok\":false,\"error\":%q,\"path\":%q}\n", err.Error(), relClean))
				flusher.Flush()
				return
			}

			if time.Since(lastKeepAlive) > 10*time.Second {
				_, _ = io.WriteString(w, "\n")
				flusher.Flush()
				lastKeepAlive = time.Now()
			}

			if time.Since(lastStat) > 2*time.Second {
				lastStat = time.Now()
				if st, err := os.Stat(abs); err == nil && st != nil {
					if st.Size() < lastSize {
						if _, err := f.Seek(0, io.SeekStart); err == nil {
							reader.Reset(f)
						}
					}
					lastSize = st.Size()
				}
			}

			time.Sleep(150 * time.Millisecond)
		}
	}
}

func contentTypeForExt(ext string) string {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".json":
		return "application/json"
	case ".jsonl":
		return "text/plain; charset=utf-8"
	case ".md", ".txt", ".log":
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

func (h *ConsoleHandler) resolveConsolePath(raw string) (abs string, relClean string, err error) {
	if h == nil || strings.TrimSpace(h.workspace) == "" {
		return "", "", fmt.Errorf("workspace not configured")
	}

	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "/")
	raw = filepath.Clean(filepath.FromSlash(raw))

	if raw == "." || raw == "" {
		return "", "", fmt.Errorf("invalid path")
	}
	if !filepath.IsLocal(raw) {
		return "", "", fmt.Errorf("invalid path")
	}

	allowedPrefixes := []string{
		filepath.Join(".x-claw", "audit"),
		"cron",
		"state",
		"sessions",
	}
	allowed := false
	for _, p := range allowedPrefixes {
		if raw == p || strings.HasPrefix(raw, p+string(os.PathSeparator)) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", "", fmt.Errorf("path not allowed")
	}

	ext := strings.ToLower(filepath.Ext(raw))
	switch ext {
	case ".json", ".jsonl", ".md", ".txt", ".log":
	default:
		return "", "", fmt.Errorf("file type not allowed")
	}

	absPath := filepath.Join(h.workspace, raw)
	baseResolved := strings.TrimSpace(h.workspaceResolved)
	if baseResolved == "" {
		if rs, err := filepath.EvalSymlinks(h.workspace); err == nil {
			baseResolved = rs
		} else {
			baseResolved = h.workspace
		}
	}
	if rs, err := filepath.EvalSymlinks(absPath); err == nil {
		basePrefix := baseResolved + string(os.PathSeparator)
		if rs != baseResolved && !strings.HasPrefix(rs, basePrefix) {
			return "", "", fmt.Errorf("path escapes workspace")
		}
	}

	return absPath, filepath.ToSlash(raw), nil
}

func tailLines(path string, maxLines int, maxBytes int64) ([]string, bool, error) {
	if maxLines <= 0 {
		return []string{}, false, nil
	}
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if st.Size() <= 0 {
		return []string{}, false, nil
	}

	n := maxBytes
	if n > st.Size() {
		n = st.Size()
	}

	if _, err := f.Seek(-n, io.SeekEnd); err != nil {
		return nil, false, err
	}

	buf := make([]byte, n)
	readN, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, false, err
	}
	buf = buf[:readN]

	lines := strings.Split(string(buf), "\n")
	truncated := st.Size() > n
	if truncated && len(lines) > 0 {
		lines = lines[1:]
	}

	out := make([]string, 0, maxLines)
	for i := len(lines) - 1; i >= 0 && len(out) < maxLines; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, truncated, nil
}
