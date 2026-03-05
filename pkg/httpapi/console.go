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

	// File points to a small JSON snapshot suitable for download in the console UI.
	// New format: sessions/*.meta.json. Legacy: sessions/*.json.
	File string `json:"file,omitempty"`

	// EventsFile points to the append-only JSONL event log (sessions/*.jsonl).
	EventsFile string `json:"events_file,omitempty"`
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

	// Prefer new lightweight meta snapshots (*.meta.json) and attach JSONL event log when present.
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
			LastEventID   string    `json:"last_event_id,omitempty"`
			MessagesCount int       `json:"messages_count,omitempty"`
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

		eventsName := base + ".jsonl"
		if base != "" {
			if _, err := os.Stat(filepath.Join(sessionsDir, eventsName)); err == nil {
				item.EventsFile = filepath.ToSlash(filepath.Join("sessions", eventsName))
			}
		}

		items = append(items, item)
	}

	// Backward compatibility: legacy full session snapshots (*.json) when meta is absent.
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

	// Find last non-empty line.
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

	// Initial tail from the end of the file (bounded).
	if tail > 0 {
		const maxBytes = int64(1 << 20) // 1MiB
		if st, err := f.Stat(); err == nil && st != nil && st.Size() > 0 {
			start := st.Size() - maxBytes
			if start < 0 {
				start = 0
			}
			if _, err := f.Seek(start, io.SeekStart); err == nil {
				buf, _ := io.ReadAll(f)
				lines := strings.Split(string(buf), "\n")
				// Drop an incomplete first line when we seek into the middle.
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

	// Follow appended lines.
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

			// Keepalive (helps reverse proxies and browsers keep the connection).
			if time.Since(lastKeepAlive) > 10*time.Second {
				_, _ = io.WriteString(w, "\n")
				flusher.Flush()
				lastKeepAlive = time.Now()
			}

			// Detect truncation/rotation and re-seek to end if needed.
			if time.Since(lastStat) > 2*time.Second {
				lastStat = time.Now()
				if st, err := os.Stat(abs); err == nil && st != nil {
					if st.Size() < lastSize {
						// File truncated: start reading from the beginning.
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
		// ok
	default:
		return "", "", fmt.Errorf("file type not allowed")
	}

	absPath := filepath.Join(h.workspace, raw)

	// Best-effort symlink escape prevention.
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

// tailLines returns up to maxLines last non-empty lines from the file.
// It reads at most maxBytes from the end of the file.
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
	if _, err := io.ReadFull(f, buf); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, false, err
	}

	raw := strings.TrimSpace(string(buf))
	if raw == "" {
		return []string{}, false, nil
	}

	lines := strings.Split(raw, "\n")
	out := make([]string, 0, maxLines)
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		out = append(out, line)
		if len(out) >= maxLines {
			break
		}
	}

	// Reverse back to chronological order.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}

	truncated := len(out) >= maxLines && len(lines) > maxLines
	return out, truncated, nil
}

const consoleHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width,initial-scale=1" />
  <title>X-Claw Console</title>
  <style>
    :root { color-scheme: light dark; }
    body { font-family: ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, Helvetica, Arial, "Apple Color Emoji", "Segoe UI Emoji"; margin: 20px; line-height: 1.4; }
    h1 { margin: 0 0 6px 0; font-size: 20px; }
    .muted { opacity: .75; }
    .row { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
    input { padding: 6px 8px; min-width: 280px; }
    input[type="number"] { min-width: 80px; width: 90px; }
    button { padding: 6px 10px; cursor: pointer; }
    code { font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace; }
    pre { padding: 10px; border: 1px solid rgba(127,127,127,.35); border-radius: 8px; overflow: auto; max-height: 360px; }
    table { border-collapse: collapse; width: 100%; }
    th, td { border-bottom: 1px solid rgba(127,127,127,.35); padding: 8px; text-align: left; vertical-align: top; }
    th { font-weight: 600; }
    .err { color: #b00020; white-space: pre-wrap; }
    .ok { color: #0a7; }
    .pill { display:inline-block; padding:2px 6px; border:1px solid rgba(127,127,127,.35); border-radius:999px; font-size:12px; opacity:.85; }
    .nowrap { white-space: nowrap; }
    .clickable { cursor: pointer; }
    .selected { background: rgba(127,127,127,.12); }
    .mono { font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace; }
    .small { font-size: 12px; }
  </style>
</head>
<body>
  <h1>X-Claw Console</h1>
  <div class="muted">Read-only. Uses the same auth policy as <code>/api/notify</code> (loopback when no API key; otherwise bearer token).</div>

  <div style="margin-top:12px" class="row">
    <label>API key:</label>
    <input id="apiKey" type="password" placeholder="(optional on loopback)"/>
    <button onclick="saveKey()">Save</button>
    <button onclick="clearKey()">Clear</button>
    <button onclick="refreshAll()">Refresh</button>
    <span id="statusPill" class="pill muted">idle</span>
  </div>

  <div id="err" class="err" style="margin-top:10px"></div>

  <h2>Status</h2>
  <pre id="status"></pre>

  <h2>Cron</h2>
  <div class="row">
    <button onclick="downloadPath('cron/jobs.json')">Download <code>cron/jobs.json</code></button>
  </div>
  <pre id="cron"></pre>

  <h2>Run traces</h2>
  <div class="row muted">Files under <code>.x-claw/audit/runs/&lt;session&gt;/events.jsonl</code></div>
  <div id="runs"></div>

  <h2>Tool traces</h2>
  <div class="row muted">Files under <code>.x-claw/audit/tools/&lt;session&gt;/events.jsonl</code></div>
  <div id="tools"></div>

  <h2>Audit log</h2>
  <div class="row muted">Append-only events under <code>.x-claw/audit/audit.jsonl</code></div>
  <div class="row">
    <button onclick="viewTrace('audit','.x-claw/audit/audit.jsonl','audit')">View</button>
    <button onclick="downloadPath('.x-claw/audit/audit.jsonl')">Download</button>
  </div>

  <h2>Trace viewer</h2>
  <div class="row muted">Select a run/tool trace above to view recent events (tail, bounded).</div>
  <div class="row">
    <span id="viewerTitle" class="pill muted">none selected</span>
    <button onclick="reloadViewer()">Reload</button>
    <button onclick="downloadViewer()">Download</button>
    <label class="small">Lines:</label>
    <input id="viewerLines" type="number" value="200" min="50" max="500" />
    <label class="small">Filter:</label>
    <input id="viewerFilter" type="text" placeholder="type/tool/text..." oninput="renderViewer()"/>
    <label class="small"><input id="viewerRawToggle" type="checkbox" onchange="renderViewer()"/> raw</label>
  </div>
  <div id="viewerMeta" class="muted" style="margin-top:8px"></div>
  <div id="viewer"></div>
  <pre id="viewerRaw" style="display:none"></pre>

  <h2>Links</h2>
  <ul>
    <li><a href="/health" target="_blank">/health</a></li>
    <li><a href="/ready" target="_blank">/ready</a></li>
    <li><a href="/api/notify" target="_blank">/api/notify</a> (POST)</li>
  </ul>

<script>
const LS_KEY = "x-claw.console.api_key";
const VIEWER_DEFAULT_LINES = 200;

const viewerState = {
  kind: "",
  path: "",
  session: "",
  events: [],
  truncated: false,
  linesRequested: 0,
  loadedAt: "",
  selectedIndex: -1,
};

function getKey() {
  return (document.getElementById("apiKey").value || "").trim();
}

function setPill(text, ok) {
  const el = document.getElementById("statusPill");
  el.textContent = text;
  el.className = "pill " + (ok ? "ok" : "muted");
}

function setErr(msg) {
  document.getElementById("err").textContent = msg || "";
}

function saveKey() {
  const k = getKey();
  localStorage.setItem(LS_KEY, k);
  setPill(k ? "key saved" : "no key", true);
}

function clearKey() {
  localStorage.removeItem(LS_KEY);
  document.getElementById("apiKey").value = "";
  setPill("cleared", true);
}

async function apiFetch(url) {
  const k = getKey();
  const headers = {};
  if (k) headers["Authorization"] = "Bearer " + k;
  const res = await fetch(url, { headers });
  if (!res.ok) {
    const txt = await res.text();
    throw new Error(res.status + " " + res.statusText + "\\n" + txt);
  }
  return res;
}

function fmt(obj) {
  return JSON.stringify(obj, null, 2);
}

async function refreshAll() {
  setErr("");
  setPill("loading...", false);
  try {
    const [status, cron, runs, tools] = await Promise.all([
      (await apiFetch("/api/console/status")).json(),
      (await apiFetch("/api/console/cron")).json(),
      (await apiFetch("/api/console/runs")).json(),
      (await apiFetch("/api/console/tools")).json(),
    ]);
    document.getElementById("status").textContent = fmt(status);
    document.getElementById("cron").textContent = fmt(cron);
    renderTraceList("runs", runs);
    renderTraceList("tools", tools);
    if (viewerState.path) {
      await loadViewer();
    } else {
      renderViewer();
    }
    setPill("ok", true);
  } catch (e) {
    setErr(String(e && e.message ? e.message : e));
    setPill("error", false);
  }
}

function renderTraceList(containerId, payload) {
  const el = document.getElementById(containerId);
  const items = (payload && payload.items) ? payload.items : [];
  if (!items.length) {
    el.innerHTML = "<div class='muted'>No items.</div>";
    return;
  }
  let html = "<table><thead><tr>" +
    "<th>Session</th><th>Last</th><th>File</th><th>Actions</th>" +
    "</tr></thead><tbody>";
  for (const it of items) {
    const sess = it.session_key || it.token;
    const last = (it.last_event_type || "") + (it.last_event_ts ? ("\\n" + it.last_event_ts) : "");
    const fp = it.events_path;
    html += "<tr>" +
      "<td><code>" + escapeHtml(sess) + "</code><div class='muted'><code>" + escapeHtml(it.token) + "</code></div></td>" +
      "<td><code>" + escapeHtml(last.trim() || "-") + "</code></td>" +
      "<td><code>" + escapeHtml(fp) + "</code><div class='muted'>" + escapeHtml(it.mod_time || "") + "</div></td>" +
      "<td class='nowrap'>" +
        "<button onclick=\"viewTrace('" + escapeAttr(containerId) + "','" + escapeAttr(fp) + "','" + escapeAttr(sess) + "')\">View</button> " +
        "<button onclick=\"downloadPath('" + escapeAttr(fp) + "')\">Download</button>" +
      "</td>" +
      "</tr>";
  }
  html += "</tbody></table>";
  el.innerHTML = html;
}

function escapeHtml(s) {
  return String(s || "").replace(/[&<>\"']/g, (c) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    "\"": "&quot;",
    "'": "&#39;",
  }[c]));
}
function escapeAttr(s) {
  return String(s || "").replace(/'/g, "&#39;");
}

function viewerLines() {
  const el = document.getElementById("viewerLines");
  let n = parseInt(el && el.value ? el.value : "", 10);
  if (!n || n < 50) n = VIEWER_DEFAULT_LINES;
  if (n > 500) n = 500;
  return n;
}

function setViewerTitle() {
  const el = document.getElementById("viewerTitle");
  if (!el) return;
  if (!viewerState.path) {
    el.textContent = "none selected";
    el.className = "pill muted";
    return;
  }
  el.textContent = viewerState.kind + " • " + (viewerState.session || viewerState.path);
  el.className = "pill ok";
}

async function viewTrace(kind, relPath, sessionKey) {
  viewerState.kind = String(kind || "").trim() || "trace";
  viewerState.path = String(relPath || "").trim();
  viewerState.session = String(sessionKey || "").trim();
  viewerState.events = [];
  viewerState.truncated = false;
  viewerState.linesRequested = 0;
  viewerState.loadedAt = "";
  viewerState.selectedIndex = -1;
  setViewerTitle();
  await loadViewer();
}

async function reloadViewer() {
  if (!viewerState.path) {
    renderViewer();
    return;
  }
  await loadViewer();
}

function downloadViewer() {
  if (!viewerState.path) return;
  downloadPath(viewerState.path);
}

function safeJsonParse(s) {
  try { return JSON.parse(s); } catch { return null; }
}

function eventTS(ev) {
  if (!ev) return "";
  if (ev.ts) return String(ev.ts);
  if (ev.ts_ms) {
    try { return new Date(Number(ev.ts_ms)).toISOString(); } catch {}
  }
  return "";
}

function eventType(ev) {
  if (!ev) return "";
  return String(ev.type || "").trim();
}

function eventTool(ev) {
  if (!ev) return "";
  return String(ev.tool || "").trim();
}

function eventIter(ev) {
  if (!ev) return "";
  const n = ev.iteration;
  if (typeof n === "number") return String(n);
  return "";
}

function summarizeEvent(ev) {
  if (!ev) return "";
  const bits = [];
  if (ev.user_message_preview) bits.push(ev.user_message_preview);
  if (ev.response_preview) bits.push(ev.response_preview);
  if (ev.args_preview) bits.push("args: " + ev.args_preview);
  if (ev.for_user_preview) bits.push("user: " + ev.for_user_preview);
  if (!ev.for_user_preview && ev.for_llm_preview) bits.push("llm: " + ev.for_llm_preview);
  if (Array.isArray(ev.tool_calls) && ev.tool_calls.length) bits.push("tools: " + ev.tool_calls.join(", "));
  if (Array.isArray(ev.tool_batch) && ev.tool_batch.length) bits.push("tool_batch: " + ev.tool_batch.length);
  if (ev.policy_decision) bits.push("policy=" + ev.policy_decision);
  if (ev.is_error && !ev.error) bits.push("error=true");
  if (ev.error) bits.push("error: " + ev.error);
  return bits.join(" | ");
}

function matchesFilter(ev, needle) {
  if (!needle) return true;
  const raw = JSON.stringify(ev || {}).toLowerCase();
  return raw.includes(needle);
}

function setViewerMeta(msg) {
  const el = document.getElementById("viewerMeta");
  if (!el) return;
  el.textContent = msg || "";
}

function setViewerRaw(text, visible) {
  const el = document.getElementById("viewerRaw");
  if (!el) return;
  el.textContent = text || "";
  el.style.display = visible ? "block" : "none";
}

function selectViewerEvent(idx) {
  viewerState.selectedIndex = idx;
  renderViewer();
}

function renderViewer() {
  setViewerTitle();
  const el = document.getElementById("viewer");
  if (!el) return;

  if (!viewerState.path) {
    el.innerHTML = "<div class='muted'>No trace selected.</div>";
    setViewerMeta("");
    setViewerRaw("", false);
    return;
  }

  const needle = (document.getElementById("viewerFilter").value || "").trim().toLowerCase();
  const showRaw = !!(document.getElementById("viewerRawToggle") && document.getElementById("viewerRawToggle").checked);

  const events = viewerState.events || [];
  const filtered = [];
  for (let i = 0; i < events.length; i++) {
    const ev = events[i];
    if (!matchesFilter(ev, needle)) continue;
    filtered.push({ idx: i, ev });
  }

  const meta = [
    "path=" + viewerState.path,
    "events=" + events.length,
    (needle ? ("filtered=" + filtered.length) : ""),
    (viewerState.truncated ? "truncated=true" : ""),
    (viewerState.loadedAt ? ("loaded_at=" + viewerState.loadedAt) : ""),
  ].filter(Boolean).join(" • ");
  setViewerMeta(meta);

  if (!filtered.length) {
    el.innerHTML = "<div class='muted'>No events (or filter matched none).</div>";
    setViewerRaw("", false);
    return;
  }

  let html = "<table><thead><tr>" +
    "<th>TS</th><th>Type</th><th>Tool</th><th>Iter</th><th>Summary</th>" +
    "</tr></thead><tbody>";
  for (const row of filtered) {
    const ev = row.ev;
    const isErr = !!(ev && (ev.is_error || ev.error || String(ev.type || "").endsWith(".error")));
    const cls = (row.idx === viewerState.selectedIndex) ? "selected clickable" : "clickable";
    html += "<tr class='" + cls + "' onclick='selectViewerEvent(" + row.idx + ")'>" +
      "<td class='mono small'>" + escapeHtml(eventTS(ev)) + "</td>" +
      "<td class='mono small'>" + escapeHtml(eventType(ev)) + "</td>" +
      "<td class='mono small'>" + escapeHtml(eventTool(ev)) + "</td>" +
      "<td class='mono small'>" + escapeHtml(eventIter(ev)) + "</td>" +
      "<td class='" + (isErr ? "err" : "") + "'>" + escapeHtml(summarizeEvent(ev)) + "</td>" +
      "</tr>";
  }
  html += "</tbody></table>";
  el.innerHTML = html;

  if (showRaw && viewerState.selectedIndex >= 0 && viewerState.selectedIndex < events.length) {
    setViewerRaw(fmt(events[viewerState.selectedIndex]), true);
  } else {
    setViewerRaw("", false);
  }
}

async function loadViewer() {
  if (!viewerState.path) {
    renderViewer();
    return;
  }
  setErr("");
  setPill("loading trace...", false);
  try {
    const url = "/api/console/tail?path=" + encodeURIComponent(viewerState.path) + "&lines=" + encodeURIComponent(viewerLines());
    const payload = await (await apiFetch(url)).json();
    const lines = (payload && payload.lines) ? payload.lines : [];
    const events = [];
    for (const line of lines) {
      const ev = safeJsonParse(line);
      if (!ev || typeof ev !== "object") continue;
      events.push(ev);
    }
    viewerState.events = events;
    viewerState.truncated = !!(payload && payload.truncated);
    viewerState.linesRequested = payload && payload.lines_requested ? payload.lines_requested : 0;
    viewerState.loadedAt = new Date().toISOString();
    if (viewerState.selectedIndex >= events.length) viewerState.selectedIndex = -1;
    renderViewer();
    setPill("ok", true);
  } catch (e) {
    setErr(String(e && e.message ? e.message : e));
    setPill("error", false);
  }
}

async function downloadPath(relPath) {
  setErr("");
  try {
    const url = "/api/console/file?path=" + encodeURIComponent(relPath);
    const res = await apiFetch(url);
    const blob = await res.blob();
    const a = document.createElement("a");
    const name = (relPath.split("/").pop() || "download");
    a.href = URL.createObjectURL(blob);
    a.download = name;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  } catch (e) {
    setErr(String(e && e.message ? e.message : e));
  }
}

// Init key from localStorage
document.getElementById("apiKey").value = (localStorage.getItem(LS_KEY) || "");
refreshAll();
</script>
</body>
</html>`
