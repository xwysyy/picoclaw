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
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/cron"
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

	return &ConsoleHandler{
		workspace:         workspace,
		apiKey:            strings.TrimSpace(opts.APIKey),
		lastActive:        opts.LastActive,
		info:              opts.Info,
		workspaceResolved: resolved,
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

	if !authorizeAPIKeyOrLoopback(h.apiKey, r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": "unauthorized",
		})
		return
	}

	if strings.HasPrefix(r.URL.Path, "/api/console/") || r.URL.Path == "/api/console" {
		h.serveAPI(w, r)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/console") {
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
				"/api/console/runs",
				"/api/console/tools",
				"/api/console/file?path=<relative-path>",
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

	case "runs":
		h.handleTraceList(w, r, traceListOptions{
			kind:    "runs",
			baseDir: filepath.Join(h.workspace, ".picoclaw", "audit", "runs"),
			eventsRel: func(token string) string {
				return filepath.ToSlash(filepath.Join(".picoclaw", "audit", "runs", token, "events.jsonl"))
			},
		})
		return

	case "tools":
		h.handleTraceList(w, r, traceListOptions{
			kind:    "tools",
			baseDir: filepath.Join(h.workspace, ".picoclaw", "audit", "tools"),
			eventsRel: func(token string) string {
				return filepath.ToSlash(filepath.Join(".picoclaw", "audit", "tools", token, "events.jsonl"))
			},
		})
		return

	case "file":
		h.handleFile(w, r)
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

	runsCount := h.countTraceSessions(filepath.Join(h.workspace, ".picoclaw", "audit", "runs"))
	toolsCount := h.countTraceSessions(filepath.Join(h.workspace, ".picoclaw", "audit", "tools"))

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
		"runs":  map[string]any{"sessions": runsCount, "base_dir": filepath.ToSlash(filepath.Join(".picoclaw", "audit", "runs"))},
		"tools": map[string]any{"sessions": toolsCount, "base_dir": filepath.ToSlash(filepath.Join(".picoclaw", "audit", "tools"))},
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
				sessionKey = strings.TrimSpace(meta.SessionKey)
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
		filepath.Join(".picoclaw", "audit"),
		"cron",
		"state",
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

const consoleHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width,initial-scale=1" />
  <title>PicoClaw Console</title>
  <style>
    :root { color-scheme: light dark; }
    body { font-family: ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, Helvetica, Arial, "Apple Color Emoji", "Segoe UI Emoji"; margin: 20px; line-height: 1.4; }
    h1 { margin: 0 0 6px 0; font-size: 20px; }
    .muted { opacity: .75; }
    .row { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
    input { padding: 6px 8px; min-width: 280px; }
    button { padding: 6px 10px; cursor: pointer; }
    code { font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace; }
    pre { padding: 10px; border: 1px solid rgba(127,127,127,.35); border-radius: 8px; overflow: auto; max-height: 360px; }
    table { border-collapse: collapse; width: 100%; }
    th, td { border-bottom: 1px solid rgba(127,127,127,.35); padding: 8px; text-align: left; vertical-align: top; }
    th { font-weight: 600; }
    .err { color: #b00020; white-space: pre-wrap; }
    .ok { color: #0a7; }
    .pill { display:inline-block; padding:2px 6px; border:1px solid rgba(127,127,127,.35); border-radius:999px; font-size:12px; opacity:.85; }
  </style>
</head>
<body>
  <h1>PicoClaw Console</h1>
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
  <div class="row muted">Files under <code>.picoclaw/audit/runs/&lt;session&gt;/events.jsonl</code></div>
  <div id="runs"></div>

  <h2>Tool traces</h2>
  <div class="row muted">Files under <code>.picoclaw/audit/tools/&lt;session&gt;/events.jsonl</code></div>
  <div id="tools"></div>

  <h2>Links</h2>
  <ul>
    <li><a href="/health" target="_blank">/health</a></li>
    <li><a href="/ready" target="_blank">/ready</a></li>
    <li><a href="/api/notify" target="_blank">/api/notify</a> (POST)</li>
  </ul>

<script>
const LS_KEY = "picoclaw.console.api_key";

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
      "<td><button onclick=\"downloadPath('" + escapeAttr(fp) + "')\">Download</button></td>" +
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
