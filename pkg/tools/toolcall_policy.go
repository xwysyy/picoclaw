package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xwysyy/X-Claw/pkg/config"
)

type estopState struct {
	KillAll            bool     `json:"kill_all,omitempty"`
	NetworkKill        bool     `json:"network_kill,omitempty"`
	FrozenTools        []string `json:"frozen_tools,omitempty"`
	FrozenToolPrefixes []string `json:"frozen_tool_prefixes,omitempty"`
}

func formatToolExecutionDenied(toolName, reason string) string {
	toolName = strings.TrimSpace(toolName)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "tool execution denied by runtime policy"
	}
	if toolName == "" {
		return fmt.Sprintf("TOOL_EXECUTION_DENIED: %s. Choose a different tool or ask for approval to change mode/policy.", reason)
	}
	return fmt.Sprintf("TOOL_EXECUTION_DENIED: tool %q was blocked: %s. Choose a different tool or ask for approval to change mode/policy.", toolName, reason)
}

func matchesToolRule(toolName string, exact []string, prefixes []string) bool {
	toolName = strings.ToLower(strings.TrimSpace(toolName))
	if toolName == "" {
		return false
	}
	for _, name := range exact {
		if toolName == strings.ToLower(strings.TrimSpace(name)) {
			return true
		}
	}
	for _, prefix := range prefixes {
		prefix = strings.ToLower(strings.TrimSpace(prefix))
		if prefix != "" && strings.HasPrefix(toolName, prefix) {
			return true
		}
	}
	return false
}

func evaluateToolPolicy(toolName string, policy config.ToolPolicyConfig, isResume bool) (bool, string) {
	if !policy.Enabled {
		return false, ""
	}
	if matchesToolRule(toolName, policy.Deny, policy.DenyPrefixes) {
		return true, "tool policy deny matched"
	}
	if len(policy.Allow) > 0 || len(policy.AllowPrefixes) > 0 {
		if !matchesToolRule(toolName, policy.Allow, policy.AllowPrefixes) {
			return true, "tool policy allowlist rejected this tool"
		}
	}
	if policy.Confirm.Enabled && confirmModeApplies(policy.Confirm.Mode, isResume) && matchesToolRule(toolName, policy.Confirm.Tools, policy.Confirm.ToolPrefixes) {
		return true, "tool policy confirmation required"
	}
	return false, ""
}

func confirmModeApplies(mode string, isResume bool) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "always":
		return true
	case "resume_only":
		return isResume
	case "never":
		return false
	default:
		return false
	}
}

func evaluateEstop(cfg config.EstopConfig, workspace, toolName string) (bool, string) {
	if !cfg.Enabled {
		return false, ""
	}

	state, err := loadEstopState(workspace)
	if err != nil {
		if cfg.FailClosed {
			return true, "estop state unreadable (fail_closed)"
		}
		return false, ""
	}
	if state == nil {
		return false, ""
	}
	if state.KillAll {
		return true, "estop kill_all is active"
	}
	if matchesToolRule(toolName, state.FrozenTools, state.FrozenToolPrefixes) {
		return true, "estop froze this tool"
	}
	if state.NetworkKill && isNetworkSensitiveTool(toolName) {
		return true, "estop network_kill is active"
	}
	return false, ""
}

func loadEstopState(workspace string) (*estopState, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil, nil
	}
	path := filepath.Join(workspace, ".x-claw", "state", "estop.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state estopState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func isNetworkSensitiveTool(toolName string) bool {
	toolName = strings.ToLower(strings.TrimSpace(toolName))
	return matchesToolRule(toolName, []string{"web", "web_fetch", "message"}, []string{"web_", "message"})
}
