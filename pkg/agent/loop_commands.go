package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/logger"
	"github.com/xwysyy/X-Claw/pkg/routing"
	"github.com/xwysyy/X-Claw/pkg/utils"
)

func (al *AgentLoop) handleCommand(ctx context.Context, msg bus.InboundMessage, agent *AgentInstance, sessionKey string) (string, bool) {
	cfg := al.Config()
	sessionKey = utils.CanonicalSessionKey(sessionKey)

	content := strings.TrimSpace(msg.Content)
	if !strings.HasPrefix(content, "/") {
		return "", false
	}

	parts := strings.Fields(content)
	if len(parts) == 0 {
		return "", false
	}

	cmd := parts[0]
	args := parts[1:]

	switch cmd {
	case "/plan":
		if cfg == nil || !cfg.Tools.PlanMode.Enabled {
			return "Plan mode is disabled (set tools.plan_mode.enabled=true in config.json)", true
		}
		if agent == nil {
			agent = al.registry.GetDefaultAgent()
		}
		if agent == nil {
			return "No agent available", true
		}
		if sessionKey == "" {
			return "No session available for plan mode (missing session_key)", true
		}

		defaultMode := sessionPermissionModeRun
		if strings.EqualFold(strings.TrimSpace(cfg.Tools.PlanMode.DefaultMode), "plan") {
			defaultMode = sessionPermissionModePlan
		}
		permWorkspace := agent.Workspace
		if da := al.registry.GetDefaultAgent(); da != nil && strings.TrimSpace(da.Workspace) != "" {
			permWorkspace = da.Workspace
		}
		perm := loadSessionPermissionStateWithDefault(permWorkspace, sessionKey, defaultMode)
		perm.Mode = sessionPermissionModePlan

		// If the user provided a task inline ("/plan <task>"), immediately run
		// the plan-stage loop in the same session.
		task := strings.TrimSpace(strings.Join(args, " "))
		if task != "" {
			perm.PendingTask = task
			if err := saveSessionPermissionState(permWorkspace, sessionKey, perm); err != nil {
				logger.WarnCF("agent", "Failed to persist plan mode state (best-effort)", map[string]any{
					"session_key": sessionKey,
					"error":       err.Error(),
				})
			}
			response, runErr := al.runAgentLoop(ctx, agent, processOptions{
				SessionKey:      sessionKey,
				Channel:         msg.Channel,
				ChatID:          msg.ChatID,
				SenderID:        msg.SenderID,
				UserMessage:     task,
				DefaultResponse: defaultResponse,
				EnableSummary:   true,
				SendResponse:    false,
				PlanMode:        true,
			})
			if runErr != nil {
				return fmt.Sprintf("Error: %v", runErr), true
			}
			return response, true
		}

		if err := saveSessionPermissionState(permWorkspace, sessionKey, perm); err != nil {
			logger.WarnCF("agent", "Failed to persist plan mode state (best-effort)", map[string]any{
				"session_key": sessionKey,
				"error":       err.Error(),
			})
		}
		return "Plan mode enabled for this session. Send your task, then send /approve (or /run) to execute.", true

	case "/approve", "/run":
		if cfg == nil || !cfg.Tools.PlanMode.Enabled {
			return "Plan mode is disabled (set tools.plan_mode.enabled=true in config.json)", true
		}
		if agent == nil {
			agent = al.registry.GetDefaultAgent()
		}
		if agent == nil {
			return "No agent available", true
		}
		if sessionKey == "" {
			return "No session available for plan mode (missing session_key)", true
		}

		defaultMode := sessionPermissionModeRun
		if strings.EqualFold(strings.TrimSpace(cfg.Tools.PlanMode.DefaultMode), "plan") {
			defaultMode = sessionPermissionModePlan
		}
		permWorkspace := agent.Workspace
		if da := al.registry.GetDefaultAgent(); da != nil && strings.TrimSpace(da.Workspace) != "" {
			permWorkspace = da.Workspace
		}
		perm := loadSessionPermissionStateWithDefault(permWorkspace, sessionKey, defaultMode)
		if !perm.isPlan() {
			return "Plan mode is not active for this session. Use /plan first.", true
		}
		task := strings.TrimSpace(perm.PendingTask)
		if task == "" {
			perm.Mode = sessionPermissionModeRun
			perm.PendingTask = ""
			_ = saveSessionPermissionState(permWorkspace, sessionKey, perm)
			return "No pending task captured. Send a task while in plan mode, then /approve.", true
		}

		perm.Mode = sessionPermissionModeRun
		perm.PendingTask = ""
		if err := saveSessionPermissionState(permWorkspace, sessionKey, perm); err != nil {
			logger.WarnCF("agent", "Failed to persist plan mode state (best-effort)", map[string]any{
				"session_key": sessionKey,
				"error":       err.Error(),
			})
		}

		approvedPrompt := fmt.Sprintf("[Plan approved] Execute the plan for the following task.\n\nTASK:\n%s", task)
		response, runErr := al.runAgentLoop(ctx, agent, processOptions{
			SessionKey:      sessionKey,
			Channel:         msg.Channel,
			ChatID:          msg.ChatID,
			SenderID:        msg.SenderID,
			UserMessage:     approvedPrompt,
			DefaultResponse: defaultResponse,
			EnableSummary:   true,
			SendResponse:    false,
			PlanMode:        false,
		})
		if runErr != nil {
			return fmt.Sprintf("Error: %v", runErr), true
		}
		return response, true

	case "/cancel":
		if cfg == nil || !cfg.Tools.PlanMode.Enabled {
			return "Plan mode is disabled (set tools.plan_mode.enabled=true in config.json)", true
		}
		if agent == nil {
			agent = al.registry.GetDefaultAgent()
		}
		if agent == nil {
			return "No agent available", true
		}
		if sessionKey == "" {
			return "No session available for plan mode (missing session_key)", true
		}

		defaultMode := sessionPermissionModeRun
		if strings.EqualFold(strings.TrimSpace(cfg.Tools.PlanMode.DefaultMode), "plan") {
			defaultMode = sessionPermissionModePlan
		}
		permWorkspace := agent.Workspace
		if da := al.registry.GetDefaultAgent(); da != nil && strings.TrimSpace(da.Workspace) != "" {
			permWorkspace = da.Workspace
		}
		perm := loadSessionPermissionStateWithDefault(permWorkspace, sessionKey, defaultMode)
		perm.Mode = sessionPermissionModeRun
		perm.PendingTask = ""
		if err := saveSessionPermissionState(permWorkspace, sessionKey, perm); err != nil {
			logger.WarnCF("agent", "Failed to persist plan mode state (best-effort)", map[string]any{
				"session_key": sessionKey,
				"error":       err.Error(),
			})
		}
		return "Plan mode cancelled; pending task cleared.", true

	case "/mode":
		if cfg == nil || !cfg.Tools.PlanMode.Enabled {
			return "Plan mode is disabled (tools.plan_mode.enabled=false)", true
		}
		if agent == nil {
			agent = al.registry.GetDefaultAgent()
		}
		if agent == nil {
			return "No agent available", true
		}
		if sessionKey == "" {
			return "No session available (missing session_key)", true
		}

		defaultMode := sessionPermissionModeRun
		if strings.EqualFold(strings.TrimSpace(cfg.Tools.PlanMode.DefaultMode), "plan") {
			defaultMode = sessionPermissionModePlan
		}
		permWorkspace := agent.Workspace
		if da := al.registry.GetDefaultAgent(); da != nil && strings.TrimSpace(da.Workspace) != "" {
			permWorkspace = da.Workspace
		}
		perm := loadSessionPermissionStateWithDefault(permWorkspace, sessionKey, defaultMode)

		pendingPreview := utils.Truncate(strings.TrimSpace(perm.PendingTask), 120)
		if pendingPreview == "" {
			pendingPreview = "(none)"
		}
		mode := "run"
		if perm.isPlan() {
			mode = "plan"
		}
		return fmt.Sprintf("mode=%s (session=%s)\npending_task=%s", mode, sessionKey, pendingPreview), true

	case "/show":
		if len(args) < 1 {
			return "Usage: /show [model|channel|agents]", true
		}
		switch args[0] {
		case "model":
			defaultAgent := al.registry.GetDefaultAgent()
			if defaultAgent == nil {
				return "No default agent configured", true
			}
			return fmt.Sprintf("Current model: %s", defaultAgent.Model), true
		case "channel":
			return fmt.Sprintf("Current channel: %s", msg.Channel), true
		case "agents":
			agentIDs := al.registry.ListAgentIDs()
			return fmt.Sprintf("Registered agents: %s", strings.Join(agentIDs, ", ")), true
		default:
			return fmt.Sprintf("Unknown show target: %s", args[0]), true
		}

	case "/list":
		if len(args) < 1 {
			return "Usage: /list [models|channels|agents]", true
		}
		switch args[0] {
		case "models":
			return "Available models: configured in config.json per agent", true
		case "channels":
			if al.channelDirectory == nil {
				return "Channel manager not initialized", true
			}
			channels := al.channelDirectory.EnabledChannels()
			if len(channels) == 0 {
				return "No channels enabled", true
			}
			return fmt.Sprintf("Enabled channels: %s", strings.Join(channels, ", ")), true
		case "agents":
			agentIDs := al.registry.ListAgentIDs()
			return fmt.Sprintf("Registered agents: %s", strings.Join(agentIDs, ", ")), true
		default:
			return fmt.Sprintf("Unknown list target: %s", args[0]), true
		}

	case "/tree":
		if agent == nil {
			agent = al.registry.GetDefaultAgent()
		}
		if agent == nil || agent.Sessions == nil {
			return "No session manager configured", true
		}
		if sessionKey == "" {
			return "No session available (missing session_key)", true
		}

		usage := "Usage:\n" +
			"/tree leaf\n" +
			"/tree list [N]\n" +
			"/tree switch <event_id>"

		if len(args) == 0 {
			leaf := agent.Sessions.LeafEventID(sessionKey)
			if leaf == "" {
				return "leaf: (none)\n\n" + usage, true
			}
			return fmt.Sprintf("leaf: %s\n\n%s", leaf, usage), true
		}

		sub := strings.ToLower(strings.TrimSpace(args[0]))
		switch sub {
		case "help":
			return usage, true

		case "leaf":
			leaf := agent.Sessions.LeafEventID(sessionKey)
			if leaf == "" {
				return "leaf: (none)", true
			}
			return fmt.Sprintf("leaf: %s", leaf), true

		case "list":
			limit := 30
			if len(args) >= 2 {
				if n, err := strconv.Atoi(strings.TrimSpace(args[1])); err == nil && n > 0 {
					limit = n
				}
			}
			tree, err := agent.Sessions.GetTree(sessionKey, limit)
			if err != nil {
				return fmt.Sprintf("Error: %v", err), true
			}
			if tree == nil {
				return "No tree available", true
			}

			var b strings.Builder
			b.WriteString(fmt.Sprintf("session=%s\nleaf=%s\n", sessionKey, tree.LeafID))
			b.WriteString(fmt.Sprintf("events=%d (showing last %d)\n", tree.Total, len(tree.Nodes)))
			b.WriteString("Legend: * leaf; + on-branch; - off-branch\n\n")
			for _, n := range tree.Nodes {
				mark := "-"
				if n.IsLeaf {
					mark = "*"
				} else if n.OnBranch {
					mark = "+"
				}

				parent := strings.TrimSpace(n.ParentID)
				if parent == "" {
					parent = "(root)"
				}

				role := strings.TrimSpace(n.Role)
				if role != "" {
					role = " role=" + role
				}

				preview := strings.TrimSpace(n.Preview)
				if preview != "" {
					preview = " " + preview
				}

				b.WriteString(fmt.Sprintf("%s %s parent=%s type=%s%s%s\n", mark, n.ID, parent, n.Type, role, preview))
			}
			return b.String(), true

		case "switch":
			if len(args) < 2 {
				return "Usage: /tree switch <event_id>", true
			}
			target := strings.TrimSpace(args[1])
			from, to, err := agent.Sessions.SwitchLeaf(sessionKey, target)
			if err != nil {
				return fmt.Sprintf("Error: %v", err), true
			}
			if from == "" {
				from = "(none)"
			}
			return fmt.Sprintf("Switched leaf: %s -> %s\nNext messages will branch from %s.", from, to, to), true

		default:
			return usage, true
		}

	case "/switch":
		if len(args) < 3 || args[1] != "to" {
			return "Usage: /switch [model|channel|session_model] to <name> [ttl_minutes]", true
		}
		target := args[0]
		value := args[2]

		switch target {
		case "model":
			defaultAgent := al.registry.GetDefaultAgent()
			if defaultAgent == nil {
				return "No default agent configured", true
			}
			oldModel := defaultAgent.Model
			defaultAgent.Model = value
			return fmt.Sprintf("Switched model from %s to %s", oldModel, value), true
		case "session_model":
			if agent == nil {
				agent = al.registry.GetDefaultAgent()
			}
			if agent == nil || agent.Sessions == nil {
				return "No session manager configured", true
			}
			if sessionKey == "" {
				return "No session available (missing session_key)", true
			}

			ttlMinutes := 0
			if len(args) >= 4 {
				if n, err := strconv.Atoi(strings.TrimSpace(args[3])); err == nil && n > 0 {
					ttlMinutes = n
				}
			}

			normalized := strings.ToLower(strings.TrimSpace(value))
			if normalized == "" || normalized == "default" || normalized == "clear" || normalized == "off" {
				_, _ = agent.Sessions.ClearModelOverride(sessionKey)
				return "Cleared session model override (using default model).", true
			}

			ttl := time.Duration(ttlMinutes) * time.Minute
			expiresAt, err := agent.Sessions.SetModelOverride(sessionKey, value, ttl)
			if err != nil {
				return fmt.Sprintf("Error: %v", err), true
			}
			if expiresAt != nil {
				return fmt.Sprintf(
					"Session model override set: %s (ttl=%dm; expires=%s)",
					value,
					ttlMinutes,
					expiresAt.UTC().Format(time.RFC3339Nano),
				), true
			}
			return fmt.Sprintf("Session model override set: %s (ttl=none)", value), true
		case "channel":
			if al.channelDirectory == nil {
				return "Channel manager not initialized", true
			}
			if !al.channelDirectory.HasChannel(value) && value != "cli" {
				return fmt.Sprintf("Channel '%s' not found or not enabled", value), true
			}
			return fmt.Sprintf("Switched target channel to %s", value), true
		default:
			return fmt.Sprintf("Unknown switch target: %s", target), true
		}
	}

	return "", false
}

// extractPeer extracts the routing peer from the inbound message's structured Peer field.
func extractPeer(msg bus.InboundMessage) *routing.RoutePeer {
	if msg.Peer.Kind == "" {
		return nil
	}
	peerID := msg.Peer.ID
	if peerID == "" {
		if msg.Peer.Kind == "direct" {
			peerID = msg.SenderID
		} else {
			peerID = msg.ChatID
		}
	}
	return &routing.RoutePeer{Kind: msg.Peer.Kind, ID: peerID}
}

// extractParentPeer extracts the parent peer (reply-to) from inbound message metadata.
func extractParentPeer(msg bus.InboundMessage) *routing.RoutePeer {
	parentKind := msg.Metadata["parent_peer_kind"]
	parentID := msg.Metadata["parent_peer_id"]
	if parentKind == "" || parentID == "" {
		return nil
	}
	return &routing.RoutePeer{Kind: parentKind, ID: parentID}
}
