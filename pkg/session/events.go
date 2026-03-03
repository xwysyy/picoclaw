package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type EventType string

const (
	EventSessionMessage       EventType = "session.message"
	EventSessionSummary       EventType = "session.summary"
	EventSessionActiveAgent   EventType = "session.active_agent"
	EventSessionCompactionInc EventType = "session.compaction_inc"
	EventSessionMemoryFlush   EventType = "session.memory_flush"
	EventSessionHistorySet    EventType = "session.history_set"
	EventSessionHistoryTrunc  EventType = "session.history_truncate"
)

type SessionEvent struct {
	Type EventType `json:"type"`

	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`

	TS   string `json:"ts"`
	TSMS int64  `json:"ts_ms"`

	SessionKey string `json:"session_key,omitempty"`

	// Message payload.
	Message *providers.Message `json:"message,omitempty"`

	// Summary payload.
	Summary string `json:"summary,omitempty"`

	// Active agent payload (Phase F: Swarm-style handoff persistence).
	ActiveAgentID string `json:"active_agent_id,omitempty"`

	// State payload.
	CompactionCount            int       `json:"compaction_count,omitempty"`
	MemoryFlushAt              time.Time `json:"memory_flush_at,omitempty"`
	MemoryFlushCompactionCount int       `json:"memory_flush_compaction_count,omitempty"`

	// History ops.
	KeepLast int                 `json:"keep_last,omitempty"`
	History  []providers.Message `json:"history,omitempty"`
}

type SessionMeta struct {
	Key     string `json:"key"`
	Summary string `json:"summary,omitempty"`
	// ActiveAgentID stores the current active agent id for this conversation session.
	// It is used by Swarm-style `handoff` to persist who should respond next.
	ActiveAgentID string    `json:"active_agent_id,omitempty"`
	Created       time.Time `json:"created"`
	Updated       time.Time `json:"updated"`

	LastEventID string `json:"last_event_id,omitempty"`

	MessagesCount int `json:"messages_count,omitempty"`

	ModelOverride            string `json:"model_override,omitempty"`
	ModelOverrideExpiresAtMS *int64 `json:"model_override_expires_at_ms,omitempty"`
}

func newEventID() string { return uuid.NewString() }

func appendJSONLEvent(path string, event SessionEvent) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	payload = append(payload, '\n')

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(payload); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	_ = f.Sync()
	return nil
}

func writeMetaFile(path string, meta SessionMeta) error {
	payload, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return fileutil.WriteFileAtomic(path, payload, 0o644)
}

func readJSONLEvents(path string) ([]SessionEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	events := make([]SessionEvent, 0, 128)
	scanner := bufio.NewScanner(f)
	// Allow larger session events (tool outputs can be large).
	// This is still bounded to avoid unbounded memory on corrupt inputs.
	buf := make([]byte, 0, 64<<10)
	scanner.Buffer(buf, 32<<20)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev SessionEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return events, err
	}
	return events, nil
}
