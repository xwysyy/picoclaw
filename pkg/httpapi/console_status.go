package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
	Key        string `json:"key"`
	Summary    string `json:"summary,omitempty"`
	Created    string `json:"created,omitempty"`
	Updated    string `json:"updated,omitempty"`
	File       string `json:"file,omitempty"`
	EventsFile string `json:"events_file,omitempty"`
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
	jobStates := []map[string]any{}
	if data, err := os.ReadFile(storePath); err == nil {
		var store cron.CronStore
		if json.Unmarshal(data, &store) == nil {
			jobsCount = len(store.Jobs)
			jobStates = summarizeCronJobStates(store.Jobs)
		}
	}

	summary := map[string]any{
		"path":    filepath.ToSlash(filepath.Join("cron", "jobs.json")),
		"exists":  true,
		"jobs":    jobsCount,
		"modTime": st.ModTime().UTC().Format(time.RFC3339Nano),
		"size":    st.Size(),
	}
	if len(jobStates) > 0 {
		summary["jobStates"] = jobStates
	}
	return summary
}

func summarizeCronJobStates(jobs []cron.CronJob) []map[string]any {
	states := make([]map[string]any, 0, len(jobs))
	for _, job := range jobs {
		state := map[string]any{
			"id":      job.ID,
			"name":    job.Name,
			"enabled": job.Enabled,
			"running": job.State.Running,
		}
		if job.State.NextRunAtMS != nil {
			state["nextRunAtMS"] = *job.State.NextRunAtMS
		}
		if strings.TrimSpace(job.State.LastStatus) != "" {
			state["lastStatus"] = strings.TrimSpace(job.State.LastStatus)
		}
		if job.State.LastDurationMS != nil {
			state["lastDurationMS"] = *job.State.LastDurationMS
		}
		if preview := strings.TrimSpace(job.State.LastOutputPreview); preview != "" {
			state["lastOutputPreview"] = preview
		}
		if lastSessionKey := strings.TrimSpace(job.State.LastSessionKey); lastSessionKey != "" {
			state["lastSessionKey"] = lastSessionKey
		}
		if lastError := strings.TrimSpace(job.State.LastError); lastError != "" {
			state["lastError"] = lastError
		}
		if history := summarizeCronRunHistory(job.State.RunHistory); len(history) > 0 {
			state["runHistory"] = history
		}
		states = append(states, state)
	}
	return states
}

func summarizeCronRunHistory(records []cron.CronRunRecord) []map[string]any {
	if len(records) == 0 {
		return nil
	}
	start := len(records) - 3
	if start < 0 {
		start = 0
	}
	summary := make([]map[string]any, 0, len(records)-start)
	for _, record := range records[start:] {
		item := map[string]any{
			"runId":        record.RunID,
			"startedAtMS":  record.StartedAtMS,
			"finishedAtMS": record.FinishedAtMS,
			"durationMS":   record.DurationMS,
			"status":       record.Status,
		}
		if errText := strings.TrimSpace(record.Error); errText != "" {
			item["error"] = errText
		}
		if sessionKey := strings.TrimSpace(record.SessionKey); sessionKey != "" {
			item["sessionKey"] = sessionKey
		}
		if output := strings.TrimSpace(record.Output); output != "" {
			item["output"] = output
		}
		summary = append(summary, item)
	}
	return summary
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
