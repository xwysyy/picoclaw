package httpapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

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
