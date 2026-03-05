package session

import (
	"time"

	"github.com/xwysyy/X-Claw/internal/core/provider/protocoltypes"
)

// Session is the durable in-memory representation of one conversation session.
// The JSON shape is also used for legacy persistence and meta snapshots; keep it
// stable for backward compatibility.
type Session struct {
	Key      string                  `json:"key"`
	Messages []protocoltypes.Message `json:"messages"`

	Summary       string `json:"summary,omitempty"`
	ActiveAgentID string `json:"active_agent_id,omitempty"`

	CompactionCount            int       `json:"compaction_count,omitempty"`
	MemoryFlushAt              time.Time `json:"memory_flush_at,omitempty"`
	MemoryFlushCompactionCount int       `json:"memory_flush_compaction_count,omitempty"`

	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`

	ModelOverride            string `json:"model_override,omitempty"`
	ModelOverrideExpiresAtMS *int64 `json:"model_override_expires_at_ms,omitempty"`

	// LastEventID tracks the last appended JSONL session event for parent linking.
	// It is not required for correctness, but improves tree reconstruction and reload.
	LastEventID string `json:"last_event_id,omitempty"`
}

// EventType is the durable session event taxonomy used in JSONL session event logs.
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

// SessionEvent is one append-only JSONL event for a session, used to build session trees
// and enable leaf switching / replay. Keep this shape stable for backward compatibility.
type SessionEvent struct {
	Type EventType `json:"type"`

	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`

	TS   string `json:"ts"`
	TSMS int64  `json:"ts_ms"`

	SessionKey string `json:"session_key,omitempty"`

	// Message payload.
	Message *protocoltypes.Message `json:"message,omitempty"`

	// Summary payload.
	Summary string `json:"summary,omitempty"`

	// Active agent payload (Phase F: Swarm-style handoff persistence).
	ActiveAgentID string `json:"active_agent_id,omitempty"`

	// State payload.
	CompactionCount            int       `json:"compaction_count,omitempty"`
	MemoryFlushAt              time.Time `json:"memory_flush_at,omitempty"`
	MemoryFlushCompactionCount int       `json:"memory_flush_compaction_count,omitempty"`

	// History ops.
	KeepLast int                     `json:"keep_last,omitempty"`
	History  []protocoltypes.Message `json:"history,omitempty"`
}

// SessionMeta is a lightweight snapshot used by ops/console surfaces.
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

// TreeNode is one node in the session tree view.
type TreeNode struct {
	ID       string    `json:"id"`
	ParentID string    `json:"parent_id,omitempty"`
	Type     EventType `json:"type"`

	TS   string `json:"ts,omitempty"`
	TSMS int64  `json:"ts_ms,omitempty"`

	Role    string `json:"role,omitempty"`
	Preview string `json:"preview,omitempty"`

	OnBranch bool `json:"on_branch,omitempty"`
	IsLeaf   bool `json:"is_leaf,omitempty"`
}

// SessionTree is the lightweight tree view returned by the session store.
type SessionTree struct {
	SessionKey string     `json:"session_key"`
	LeafID     string     `json:"leaf_id,omitempty"`
	Total      int        `json:"total"`
	Nodes      []TreeNode `json:"nodes,omitempty"`
}
