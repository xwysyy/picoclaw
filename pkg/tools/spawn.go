package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xwysyy/X-Claw/pkg/utils"
)

type SpawnTool struct {
	manager        *SubagentManager
	allowlistCheck func(targetAgentID string) bool
}

// Compile-time check: SpawnTool implements AsyncExecutor.
var _ AsyncExecutor = (*SpawnTool)(nil)

func NewSpawnTool(manager *SubagentManager) *SpawnTool {
	return &SpawnTool{
		manager: manager,
	}
}

func (t *SpawnTool) Name() string {
	return "spawn"
}

func (t *SpawnTool) Description() string {
	return "Spawn a subagent to handle a task asynchronously in the background. " +
		"Input: task (string, required) — clear description of what the subagent should do. " +
		"Output: confirmation with subagent task ID. The result will be reported back via system message when done. " +
		"Use this for tasks that can run independently (e.g., research, long computations). " +
		"The subagent has access to the same tools but runs in its own context. " +
		"For tasks where you need the result immediately, use the 'subagent' tool instead."
}

func (t *SpawnTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{
				"type":        "string",
				"description": "The task for subagent to complete",
			},
			"label": map[string]any{
				"type":        "string",
				"description": "Optional short label for the task (for display)",
			},
			"agent_id": map[string]any{
				"type":        "string",
				"description": "Optional target agent ID to delegate the task to",
			},
		},
		"required": []string{"task"},
	}
}

func (t *SpawnTool) SetAllowlistChecker(check func(targetAgentID string) bool) {
	t.allowlistCheck = check
}

func (t *SpawnTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	return t.execute(ctx, args, nil)
}

// ExecuteAsync implements AsyncExecutor. The callback is passed through to the
// subagent manager as a call parameter — never stored on the SpawnTool instance.
func (t *SpawnTool) ExecuteAsync(ctx context.Context, args map[string]any, cb AsyncCallback) *ToolResult {
	return t.execute(ctx, args, cb)
}

func (t *SpawnTool) execute(ctx context.Context, args map[string]any, cb AsyncCallback) *ToolResult {
	task, ok := args["task"].(string)
	if !ok || strings.TrimSpace(task) == "" {
		return ErrorResult("task is required and must be a non-empty string")
	}

	label, _ := args["label"].(string)
	agentID, _ := args["agent_id"].(string)

	// Check allowlist if targeting a specific agent
	if agentID != "" && t.allowlistCheck != nil {
		if !t.allowlistCheck(agentID) {
			return ErrorResult(fmt.Sprintf("not allowed to spawn agent '%s'", agentID))
		}
	}

	if t.manager == nil {
		return ErrorResult("Subagent manager not configured")
	}

	originChannel := toolExecutionChannel(ctx)
	originChatID := toolExecutionChatID(ctx)
	if strings.TrimSpace(originChannel) == "" {
		originChannel = "cli"
	}
	if strings.TrimSpace(originChatID) == "" {
		originChatID = "direct"
	}

	// Backward-compatible: older executor path injected callback via context.
	if cb == nil {
		cb = toolExecutionAsyncCallback(ctx)
	}

	taskInfo, err := t.manager.SpawnTask(ctx, task, label, agentID, originChannel, originChatID, cb)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to spawn subagent: %v", err)).WithError(err)
	}

	payload := map[string]any{
		"kind":        "subagent_spawn",
		"status":      "accepted",
		"task_id":     taskInfo.ID,
		"task":        taskInfo.Task,
		"label":       taskInfo.Label,
		"agent_id":    taskInfo.AgentID,
		"run_id":      strings.TrimSpace(taskInfo.RunID),
		"session_key": utils.CanonicalSessionKey(taskInfo.SessionKey),
		"result_delivery": map[string]any{
			"channel": "system",
			"chat_id": fmt.Sprintf("%s:%s", originChannel, originChatID),
		},
	}

	data, marshalErr := json.MarshalIndent(payload, "", "  ")
	if marshalErr != nil {
		return ErrorResult(fmt.Sprintf("failed to encode spawn payload: %v", marshalErr)).WithError(marshalErr)
	}

	// Return AsyncResult since the task runs in background
	return &ToolResult{
		ForLLM:  string(data),
		Silent:  true,
		IsError: false,
		Async:   true,
	}
}
