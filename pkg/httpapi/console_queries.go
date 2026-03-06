package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/pkg/cron"
)

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
			Key     string    `json:"key"`
			Summary string    `json:"summary,omitempty"`
			Created time.Time `json:"created"`
			Updated time.Time `json:"updated"`
		}
		if json.Unmarshal(data, &meta) != nil {
			continue
		}

		base := strings.TrimSuffix(name, name[len(name)-len(".meta.json"):])
		if base != "" {
			seenBase[base] = struct{}{}
		}

		item := sessionListItem{
			Key:     strings.TrimSpace(meta.Key),
			Summary: strings.TrimSpace(meta.Summary),
			Created: meta.Created.UTC().Format(time.RFC3339Nano),
			Updated: meta.Updated.UTC().Format(time.RFC3339Nano),
			File:    filepath.ToSlash(filepath.Join("sessions", name)),
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
			Key     string    `json:"key"`
			Summary string    `json:"summary,omitempty"`
			Created time.Time `json:"created"`
			Updated time.Time `json:"updated"`
		}
		if json.Unmarshal(data, &legacy) != nil {
			continue
		}

		item := sessionListItem{
			Key:     strings.TrimSpace(legacy.Key),
			Summary: strings.TrimSpace(legacy.Summary),
			Created: legacy.Created.UTC().Format(time.RFC3339Nano),
			Updated: legacy.Updated.UTC().Format(time.RFC3339Nano),
			File:    filepath.ToSlash(filepath.Join("sessions", name)),
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
