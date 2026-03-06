package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/pkg/providers"
	"github.com/xwysyy/X-Claw/pkg/session"
	"github.com/xwysyy/X-Claw/pkg/utils"
)

const (
	defaultSessionsListLimit   = 50
	maxSessionsListLimit       = 200
	defaultSessionsHistorySize = 200
	maxSessionsHistorySize     = 1000
	maxSessionPreviewMessages  = 50
	maxMessagePreviewChars     = 500
)

type SessionsListTool struct {
	sessions session.Store
}

func NewSessionsListTool(sm session.Store) *SessionsListTool {
	return &SessionsListTool{sessions: sm}
}

func (t *SessionsListTool) Name() string {
	return "sessions_list"
}

func (t *SessionsListTool) ParallelPolicy() ToolParallelPolicy {
	return ToolParallelReadOnly
}

func (t *SessionsListTool) Description() string {
	return "List known conversation sessions with metadata. Useful for debugging, navigation, and context inspection."
}

func (t *SessionsListTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kinds": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
				"description": "Optional session kind filter (e.g. main, direct, group, cron, subagent)",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of sessions to return (default 50, max 200)",
				"minimum":     1.0,
				"maximum":     200.0,
			},
			"active_minutes": map[string]any{
				"type":        "integer",
				"description": "Only include sessions updated within N minutes",
				"minimum":     1.0,
			},
			"message_limit": map[string]any{
				"type":        "integer",
				"description": "Include up to N recent preview messages per session (default 0, max 50)",
				"minimum":     0.0,
				"maximum":     50.0,
			},
			"include_tools": map[string]any{
				"type":        "boolean",
				"description": "When message_limit > 0, include tool messages in preview (default false)",
			},
		},
	}
}

func (t *SessionsListTool) Execute(_ context.Context, args map[string]any) *ToolResult {
	if t.sessions == nil {
		return ErrorResult("session manager not configured")
	}

	limit, err := parseIntArg(args, "limit", defaultSessionsListLimit, 1, maxSessionsListLimit)
	if err != nil {
		return ErrorResult(err.Error())
	}

	activeMinutes, err := parseOptionalIntArg(args, "active_minutes", 0, 0, 365*24*60)
	if err != nil {
		return ErrorResult(err.Error())
	}

	messageLimit, err := parseIntArg(args, "message_limit", 0, 0, maxSessionPreviewMessages)
	if err != nil {
		return ErrorResult(err.Error())
	}

	includeTools, err := parseBoolArg(args, "include_tools", false)
	if err != nil {
		return ErrorResult(err.Error())
	}

	kinds, err := parseStringSliceArg(args, "kinds")
	if err != nil {
		return ErrorResult(err.Error())
	}

	allowedKinds := make(map[string]struct{}, len(kinds))
	for _, k := range kinds {
		allowedKinds[strings.ToLower(strings.TrimSpace(k))] = struct{}{}
	}

	now := time.Now()
	snapshots := t.sessions.ListSessionSnapshots()
	outSessions := make([]sessionListItem, 0, min(limit, len(snapshots)))

	for _, s := range snapshots {
		kind := classifySessionKind(s.Key)

		if len(allowedKinds) > 0 {
			if _, ok := allowedKinds[kind]; !ok {
				continue
			}
		}

		if activeMinutes > 0 && now.Sub(s.Updated) > time.Duration(activeMinutes)*time.Minute {
			continue
		}

		item := sessionListItem{
			Key:          s.Key,
			Kind:         kind,
			CreatedAt:    s.Created.Format(time.RFC3339),
			UpdatedAt:    s.Updated.Format(time.RFC3339),
			MessageCount: len(s.Messages),
		}
		if strings.TrimSpace(s.Summary) != "" {
			item.Summary = s.Summary
		}
		if messageLimit > 0 {
			item.Messages = tailMessages(s.Messages, messageLimit, includeTools)
		}

		outSessions = append(outSessions, item)
		if len(outSessions) >= limit {
			break
		}
	}

	payload := sessionsListOutput{
		Count:    len(outSessions),
		Sessions: outSessions,
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to encode sessions list: %v", err))
	}
	return SilentResult(string(data))
}

type SessionsHistoryTool struct {
	sessions session.Store
}

func NewSessionsHistoryTool(sm session.Store) *SessionsHistoryTool {
	return &SessionsHistoryTool{sessions: sm}
}

func (t *SessionsHistoryTool) Name() string {
	return "sessions_history"
}

func (t *SessionsHistoryTool) ParallelPolicy() ToolParallelPolicy {
	return ToolParallelReadOnly
}

func (t *SessionsHistoryTool) Description() string {
	return "Get full or partial message history for one session key."
}

func (t *SessionsHistoryTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"session_key": map[string]any{
				"type":        "string",
				"description": "Session key returned by sessions_list",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum messages to return from the tail (default 200, max 1000)",
				"minimum":     1.0,
				"maximum":     1000.0,
			},
			"include_tools": map[string]any{
				"type":        "boolean",
				"description": "Include tool-role messages (default false)",
			},
		},
		"required": []string{"session_key"},
	}
}

func (t *SessionsHistoryTool) Execute(_ context.Context, args map[string]any) *ToolResult {
	if t.sessions == nil {
		return ErrorResult("session manager not configured")
	}

	key, ok := getStringArg(args, "session_key")
	if !ok {
		return ErrorResult("session_key is required")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return ErrorResult("session_key is required")
	}

	limit, err := parseIntArg(args, "limit", defaultSessionsHistorySize, 1, maxSessionsHistorySize)
	if err != nil {
		return ErrorResult(err.Error())
	}

	includeTools, err := parseBoolArg(args, "include_tools", false)
	if err != nil {
		return ErrorResult(err.Error())
	}

	snapshot, ok := t.sessions.GetSessionSnapshot(key)
	if !ok {
		return ErrorResult(fmt.Sprintf("session %q not found", key))
	}

	messages := tailMessages(snapshot.Messages, limit, includeTools)
	payload := sessionHistoryOutput{
		SessionKey:   snapshot.Key,
		Kind:         classifySessionKind(snapshot.Key),
		CreatedAt:    snapshot.Created.Format(time.RFC3339),
		UpdatedAt:    snapshot.Updated.Format(time.RFC3339),
		MessageCount: len(messages),
		Messages:     messages,
	}
	if strings.TrimSpace(snapshot.Summary) != "" {
		payload.Summary = snapshot.Summary
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to encode session history: %v", err))
	}
	return SilentResult(string(data))
}

type sessionListItem struct {
	Key           string              `json:"key"`
	Kind          string              `json:"kind"`
	CreatedAt     string              `json:"created_at"`
	UpdatedAt     string              `json:"updated_at"`
	MessageCount  int                 `json:"message_count"`
	Summary       string              `json:"summary,omitempty"`
	Messages      []providers.Message `json:"messages,omitempty"`
}

type sessionsListOutput struct {
	Count    int               `json:"count"`
	Sessions []sessionListItem `json:"sessions"`
}

type sessionHistoryOutput struct {
	SessionKey    string              `json:"session_key"`
	Kind          string              `json:"kind"`
	CreatedAt     string              `json:"created_at"`
	UpdatedAt     string              `json:"updated_at"`
	MessageCount  int                 `json:"message_count"`
	Summary       string              `json:"summary,omitempty"`
	Messages      []providers.Message `json:"messages"`
}

func classifySessionKind(key string) string {
	k := utils.CanonicalSessionKey(key)
	switch {
	case k == "":
		return "other"
	case (strings.HasPrefix(k, "agent:") && strings.HasSuffix(k, ":main")) || k == "conv:main":
		return "main"
	case strings.HasPrefix(k, "subagent:") || strings.Contains(k, ":subagent:"):
		return "subagent"
	case strings.Contains(k, ":group:"):
		return "group"
	case strings.Contains(k, ":channel:"):
		return "channel"
	case strings.Contains(k, ":direct:"):
		return "direct"
	case strings.HasPrefix(k, "cron:") || strings.HasPrefix(k, "cron-"):
		return "cron"
	case strings.HasPrefix(k, "hook:"):
		return "hook"
	case strings.HasPrefix(k, "node-"):
		return "node"
	case k == "heartbeat":
		return "heartbeat"
	default:
		return "other"
	}
}

func tailMessages(messages []providers.Message, limit int, includeTools bool) []providers.Message {
	if limit <= 0 || len(messages) == 0 {
		return nil
	}

	selected := make([]providers.Message, 0, min(limit, len(messages)))
	for i := len(messages) - 1; i >= 0 && len(selected) < limit; i-- {
		msg := messages[i]
		if !includeTools && msg.Role == "tool" {
			continue
		}
		if len(msg.Content) > maxMessagePreviewChars {
			msg.Content = msg.Content[:maxMessagePreviewChars] + "...(truncated)"
		}
		selected = append(selected, msg)
	}

	// Reverse back to chronological order.
	for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
		selected[i], selected[j] = selected[j], selected[i]
	}
	return selected
}
