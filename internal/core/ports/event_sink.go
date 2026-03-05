package ports

import (
	"context"
	"time"

	"github.com/xwysyy/X-Claw/internal/core/events"
)

// Event is the minimal core event envelope emitted by long-running processes
// (agent runs, tool calls, lifecycle transitions).
//
// This is intentionally generic: different sinks may serialize to JSONL,
// push to websockets/SSE, or render as placeholders in channels.
type Event struct {
	Type events.Type `json:"type"`
	TS   time.Time   `json:"ts"`

	SessionKey string `json:"session_key,omitempty"`
	AgentID    string `json:"agent_id,omitempty"`
	RunID      string `json:"run_id,omitempty"`

	// Payload holds event-specific fields. Keep this small and prefer stable
	// keys because these may be persisted.
	Payload map[string]any `json:"payload,omitempty"`
}

// EventSink consumes core events.
type EventSink interface {
	Emit(ctx context.Context, ev Event)
}
