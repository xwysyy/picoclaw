package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xwysyy/X-Claw/pkg/providers"
	"github.com/xwysyy/X-Claw/pkg/utils"
)

// SubagentHandoffBlockHook blocks the handoff tool when executed from a subagent
// tool loop. This prevents implicit handoffs initiated by background workers.
//
// The hook returns a tool_policy_denied payload that includes a structured
// handoff_suggestion the subagent can pass back to the parent agent.
type SubagentHandoffBlockHook struct{}

func NewSubagentHandoffBlockHook() *SubagentHandoffBlockHook {
	return &SubagentHandoffBlockHook{}
}

func (h *SubagentHandoffBlockHook) Name() string {
	return "subagent_handoff"
}

func (h *SubagentHandoffBlockHook) BeforeToolCall(_ context.Context, call providers.ToolCall, meta ToolHookContext) (providers.ToolCall, *ToolResult, *ToolHookAction) {
	if !strings.EqualFold(strings.TrimSpace(call.Name), "handoff") {
		return call, nil, nil
	}

	senderID := strings.ToLower(strings.TrimSpace(meta.SenderID))
	if !strings.HasPrefix(senderID, "subagent:") {
		return call, nil, nil
	}

	args := call.Arguments
	targetAgentID := ""
	if v, ok := getStringArg(args, "agent_id"); ok && strings.TrimSpace(v) != "" {
		targetAgentID = strings.TrimSpace(v)
	} else if v, ok := getStringArg(args, "agent_name"); ok && strings.TrimSpace(v) != "" {
		targetAgentID = strings.TrimSpace(v)
	}

	reason, _ := getStringArg(args, "reason")
	reason = strings.TrimSpace(reason)

	takeover, err := parseBoolArg(args, "takeover", true)
	if err != nil {
		takeover = true
	}

	suggestion := SubagentHandoffSuggestion{
		AgentID:    targetAgentID,
		Reason:     reason,
		Takeover:   takeover,
		ToolCallID: strings.TrimSpace(call.ID),
	}

	payload := map[string]any{
		"kind":               "tool_policy_denied",
		"tool":               "handoff",
		"reason":             "handoff is not allowed from subagents; return a handoff_suggestion to the parent agent instead",
		"handoff_suggestion": suggestion,
		"session_key":        utils.CanonicalSessionKey(meta.SessionKey),
		"next_step": "Return handoff_suggestion in your final summary; " +
			"the parent agent may decide to call handoff explicitly.",
	}

	encoded, _ := json.Marshal(payload)
	result := ErrorResult(string(encoded)).WithError(fmt.Errorf("handoff denied for subagent context"))
	result.Silent = true
	return call, result, &ToolHookAction{
		Decision: "deny",
		Reason:   "handoff denied for subagent context (explicit handoff only)",
	}
}

func (h *SubagentHandoffBlockHook) AfterToolCall(_ context.Context, call providers.ToolCall, result *ToolResult, _ ToolHookContext) (*ToolResult, *ToolHookAction) {
	_ = h
	_ = call
	return result, nil
}
