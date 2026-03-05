package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/pkg/utils"
)

type ToolConfirmTool struct {
	workspace string
	expires   time.Duration
}

func NewToolConfirmTool(workspace string, expires time.Duration) *ToolConfirmTool {
	if expires <= 0 {
		expires = 15 * time.Minute
	}
	return &ToolConfirmTool{
		workspace: strings.TrimSpace(workspace),
		expires:   expires,
	}
}

func (t *ToolConfirmTool) Name() string { return "tool_confirm" }

func (t *ToolConfirmTool) Description() string {
	return "Confirm a pending side-effect tool call so it can be executed safely (two-phase commit)."
}

func (t *ToolConfirmTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"confirm_key": map[string]any{
				"type":        "string",
				"description": "Confirmation key provided by tool_policy_confirmation_required.",
			},
			"note": map[string]any{
				"type":        "string",
				"description": "Optional note describing why the action is approved.",
			},
		},
		"required": []string{"confirm_key"},
	}
}

func (t *ToolConfirmTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if t == nil || strings.TrimSpace(t.workspace) == "" {
		return ErrorResult("tool_confirm: workspace is not configured").WithError(fmt.Errorf("workspace is empty"))
	}

	confirmKey := ""
	if v, ok := args["confirm_key"].(string); ok {
		confirmKey = strings.TrimSpace(v)
	}
	if confirmKey == "" {
		return ErrorResult("tool_confirm: confirm_key is required").WithError(fmt.Errorf("confirm_key is empty"))
	}

	note := ""
	if v, ok := args["note"].(string); ok {
		note = strings.TrimSpace(v)
	}

	sessionKey := ExecutionSessionKey(ctx)
	runID := ExecutionRunID(ctx)
	if sessionKey == "" || runID == "" {
		return ErrorResult("tool_confirm: missing session/run context (cannot confirm without run_id/session_key)").WithError(
			fmt.Errorf("missing run context"),
		)
	}

	store := getToolPolicyStore(t.workspace, sessionKey, runID)
	if store == nil || !store.enabled {
		return ErrorResult("tool_confirm: policy store is unavailable").WithError(fmt.Errorf("policy store unavailable"))
	}

	expiresAt := time.Now().Add(t.expires)
	if err := store.RecordConfirmation(runID, sessionKey, confirmKey, "", "", note, expiresAt); err != nil {
		return ErrorResult("tool_confirm: failed to record confirmation: " + err.Error()).WithError(err)
	}

	payload := map[string]any{
		"kind":         "tool_policy_confirmed",
		"confirm_key":  confirmKey,
		"expires_at":   expiresAt.UTC().Format(time.RFC3339),
		"expires_in_s": int(t.expires.Seconds()),
		"note":         utils.Truncate(note, 200),
	}
	encoded, _ := json.Marshal(payload)
	return SilentResult(string(encoded))
}
