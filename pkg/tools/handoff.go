package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xwysyy/X-Claw/pkg/session"
	"github.com/xwysyy/X-Claw/pkg/utils"
)

type AgentLookupFunc func(agentID string) (AgentInfo, bool)

type HandoffTool struct {
	currentAgentID string
	sessions       session.Store
	lookup         AgentLookupFunc
	allow          func(fromAgentID, toAgentID string) bool
}

func NewHandoffTool(currentAgentID string, sessions session.Store, lookup AgentLookupFunc) *HandoffTool {
	return &HandoffTool{
		currentAgentID: strings.TrimSpace(currentAgentID),
		sessions:       sessions,
		lookup:         lookup,
	}
}

func (t *HandoffTool) Name() string {
	return "handoff"
}

func (t *HandoffTool) Description() string {
	return "Switch the active agent for the current conversation. " +
		"Use this when another agent is better suited to handle the task. " +
		"By default, the new agent should take over immediately in the same turn."
}

func (t *HandoffTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agent_name": map[string]any{
				"type":        "string",
				"description": "Target agent to hand off to (agent ID preferred; use agents_list to discover).",
			},
			"agent_id": map[string]any{
				"type":        "string",
				"description": "Target agent ID (preferred). Use agents_list to discover available agents.",
			},
			"reason": map[string]any{
				"type":        "string",
				"description": "Why you are handing off (brief but specific).",
			},
			"takeover": map[string]any{
				"type":        "boolean",
				"description": "If true (default), the new agent should continue immediately in the same turn.",
			},
		},
		"required": []string{"agent_id", "reason"},
	}
}

func (t *HandoffTool) SetAllowlistChecker(check func(fromAgentID, toAgentID string) bool) {
	t.allow = check
}

func (t *HandoffTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	agentID := ""
	if v, ok := getStringArg(args, "agent_id"); ok && strings.TrimSpace(v) != "" {
		agentID = strings.TrimSpace(v)
	} else if v, ok := getStringArg(args, "agent_name"); ok && strings.TrimSpace(v) != "" {
		agentID = strings.TrimSpace(v)
	}
	if agentID == "" {
		return ErrorResult("agent_id is required (use agents_list)")
	}

	reason, ok := getStringArg(args, "reason")
	if !ok || strings.TrimSpace(reason) == "" {
		return ErrorResult("reason is required")
	}
	reason = strings.TrimSpace(reason)

	takeover, err := parseBoolArg(args, "takeover", true)
	if err != nil {
		return ErrorResult(err.Error())
	}

	if t != nil && t.allow != nil && !t.allow(t.currentAgentID, agentID) {
		return ErrorResult(fmt.Sprintf("not allowed to hand off from %q to %q", t.currentAgentID, agentID))
	}

	if t != nil && t.lookup != nil {
		if _, exists := t.lookup(agentID); !exists {
			return ErrorResult(fmt.Sprintf("agent %q not found (use agents_list)", agentID))
		}
	}

	sessionKey := toolExecutionSessionKey(ctx)
	if sessionKey == "" {
		// Fallback for non-agent tool contexts.
		channel := toolExecutionChannel(ctx)
		chatID := toolExecutionChatID(ctx)
		if channel != "" && chatID != "" {
			sessionKey = utils.CanonicalSessionKey(fmt.Sprintf("%s:%s", channel, chatID))
		}
	}
	if sessionKey == "" {
		return ErrorResult("handoff failed: missing session key context")
	}

	if t.sessions == nil {
		return ErrorResult("handoff failed: session manager not configured")
	}

	warnings := make([]string, 0, 2)
	if strings.EqualFold(strings.TrimSpace(agentID), strings.TrimSpace(t.currentAgentID)) {
		warnings = append(warnings, "handoff target is the same as current agent (no-op)")
	}

	t.sessions.SetActiveAgentID(sessionKey, agentID)
	if err := t.sessions.Save(sessionKey); err != nil {
		return ErrorResult(fmt.Sprintf("handoff failed: cannot persist session state: %v", err)).WithError(err)
	}

	effectiveAt := "next_turn"
	if takeover {
		effectiveAt = "this_run"
	}

	payload := map[string]any{
		"status":       "ok",
		"from_agent":   t.currentAgentID,
		"to_agent":     agentID,
		"reason":       reason,
		"takeover":     takeover,
		"effective_at": effectiveAt,
		"session_key":  sessionKey,
		"warnings":     warnings,
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to encode handoff payload: %v", err))
	}
	return &ToolResult{
		ForLLM:  string(data),
		ForUser: fmt.Sprintf("Handoff: %s → %s (%s).", strings.TrimSpace(t.currentAgentID), agentID, effectiveAt),
		Silent:  true,
		IsError: false,
		Async:   false,
	}
}
