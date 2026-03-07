package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/constants"
	"github.com/xwysyy/X-Claw/pkg/logger"
	"github.com/xwysyy/X-Claw/pkg/providers"
	"github.com/xwysyy/X-Claw/pkg/routing"
	"github.com/xwysyy/X-Claw/pkg/utils"
)

type preparedRun struct {
	messages    []providers.Message
	modelForRun string
	runTrace    *runTraceWriter
}

type messageToolRoundResetter interface {
	ResetSentInRound()
}

func (al *AgentLoop) processMessageImpl(ctx context.Context, msg bus.InboundMessage) (string, error) {
	logInboundMessage(msg)
	if msg.Channel == "system" {
		return al.processSystemMessage(ctx, msg)
	}

	cfg := al.Config()
	msg = al.transcribeAudioInMessage(ctx, msg)

	route := al.resolveInboundRoute(msg)
	conversationSessionKey := al.buildConversationSessionKey(msg, cfg)
	sessionKey := al.resolveInboundSessionKey(msg, conversationSessionKey)

	agent, err := al.resolveAgentForSession(sessionKey, route)
	if err != nil {
		return "", err
	}
	resetMessageToolState(agent)

	if response, handled := al.handleCommand(ctx, msg, agent, sessionKey); handled {
		return response, nil
	}

	logger.InfoCF("agent", "Routed message",
		map[string]any{
			"agent_id":    agent.ID,
			"session_key": sessionKey,
			"conv_key":    conversationSessionKey,
			"matched_by":  route.MatchedBy,
		})

	userMessage := al.buildUserMessage(agent, msg)
	planMode := al.resolvePlanMode(cfg, agent, msg, sessionKey, userMessage)

	return al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:      sessionKey,
		Channel:         msg.Channel,
		ChatID:          msg.ChatID,
		SenderID:        msg.SenderID,
		UserMessage:     userMessage,
		Media:           msg.Media,
		DefaultResponse: defaultResponse,
		EnableSummary:   true,
		SendResponse:    false,
		Steering:        steeringInboxFromContext(ctx),
		PlanMode:        planMode,
	})
}

func (al *AgentLoop) processSystemMessageImpl(
	ctx context.Context,
	msg bus.InboundMessage,
) (string, error) {
	if msg.Channel != "system" {
		return "", fmt.Errorf(
			"processSystemMessage called with non-system message channel: %s",
			msg.Channel,
		)
	}

	logger.InfoCF("agent", "Processing system message",
		map[string]any{
			"sender_id": msg.SenderID,
			"chat_id":   msg.ChatID,
		})

	originChannel, originChatID := parseSystemMessageOrigin(msg.ChatID)
	content := extractSystemMessageResult(msg.Content)
	if constants.IsInternalChannel(originChannel) {
		logger.InfoCF("agent", "Subagent completed (internal channel)",
			map[string]any{
				"sender_id":   msg.SenderID,
				"content_len": len(content),
				"channel":     originChannel,
			})
		return "", nil
	}

	sessionKey := al.resolveSystemMessageSessionKey(msg, originChannel, originChatID)
	agent, err := al.resolveSystemMessageAgent(sessionKey)
	if err != nil {
		return "", err
	}
	return al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:      sessionKey,
		Channel:         originChannel,
		ChatID:          originChatID,
		SenderID:        msg.SenderID,
		UserMessage:     fmt.Sprintf("[System: %s] %s", msg.SenderID, msg.Content),
		DefaultResponse: "Background task completed.",
		EnableSummary:   false,
		SendResponse:    true,
	})
}

func (al *AgentLoop) runAgentLoopImpl(
	ctx context.Context,
	agent *AgentInstance,
	opts processOptions,
) (string, error) {
	cfg := al.Config()
	al.recordLastExternalChannel(opts)

	prepared := al.prepareRun(agent, &opts, cfg)
	persistUserMessage(agent, opts)

	finalContent, iteration, activeAgent, err := al.runLLMIteration(
		ctx,
		agent,
		prepared.messages,
		opts,
		prepared.runTrace,
		prepared.modelForRun,
	)
	if err != nil {
		if prepared.runTrace != nil {
			prepared.runTrace.recordError(iteration, err)
		}
		return "", err
	}
	if activeAgent != nil {
		agent = activeAgent
	}

	return al.finalizeRun(ctx, agent, opts, cfg, prepared.runTrace, finalContent, iteration), nil
}

func logInboundMessage(msg bus.InboundMessage) {
	logContent := utils.Truncate(msg.Content, 80)
	if strings.Contains(msg.Content, "Error:") || strings.Contains(msg.Content, "error") {
		logContent = msg.Content
	}
	logger.InfoCF(
		"agent",
		fmt.Sprintf("Processing message from %s:%s: %s", msg.Channel, msg.SenderID, logContent),
		map[string]any{
			"channel":     msg.Channel,
			"chat_id":     msg.ChatID,
			"sender_id":   msg.SenderID,
			"session_key": msg.SessionKey,
		},
	)
}

func (al *AgentLoop) resolveInboundRoute(msg bus.InboundMessage) routing.ResolvedRoute {
	return al.registry.ResolveRoute(routing.RouteInput{
		Channel:    msg.Channel,
		AccountID:  msg.Metadata["account_id"],
		Peer:       extractPeer(msg),
		ParentPeer: extractParentPeer(msg),
		ThreadID:   msg.Metadata["thread_id"],
		GuildID:    msg.Metadata["guild_id"],
		TeamID:     msg.Metadata["team_id"],
	})
}

func (al *AgentLoop) buildConversationSessionKey(msg bus.InboundMessage, cfg *config.Config) string {
	dmScope := routing.DMScopeMain
	identityLinks := map[string][]string(nil)
	if cfg != nil {
		if v := routing.DMScope(strings.TrimSpace(cfg.Session.DMScope)); v != "" {
			dmScope = v
		}
		identityLinks = cfg.Session.IdentityLinks
	}
	conversationSessionKey := utils.CanonicalSessionKey(routing.BuildConversationPeerSessionKey(routing.SessionKeyParams{
		Channel:       msg.Channel,
		AccountID:     msg.Metadata["account_id"],
		Peer:          extractPeer(msg),
		ThreadID:      msg.Metadata["thread_id"],
		DMScope:       dmScope,
		IdentityLinks: identityLinks,
	}))
	if conversationSessionKey != "" {
		return conversationSessionKey
	}
	return utils.CanonicalSessionKey(strings.TrimSpace(msg.Channel) + ":" + strings.TrimSpace(msg.ChatID))
}

func (al *AgentLoop) resolveInboundSessionKey(msg bus.InboundMessage, conversationSessionKey string) string {
	if explicit := utils.CanonicalSessionKey(msg.SessionKey); explicit != "" {
		if strings.HasPrefix(explicit, "agent:") ||
			strings.HasPrefix(explicit, "conv:") ||
			constants.IsInternalChannel(msg.Channel) {
			return explicit
		}
	}
	if al != nil && al.bus != nil {
		for _, candidate := range []string{
			strings.TrimSpace(msg.Metadata["reply_to_message_id"]),
			strings.TrimSpace(msg.Metadata["parent_message_id"]),
			strings.TrimSpace(msg.Metadata["root_message_id"]),
			strings.TrimSpace(msg.Metadata["parent_id"]),
			strings.TrimSpace(msg.Metadata["root_id"]),
		} {
			if candidate == "" {
				continue
			}
			if replyCtx, ok := al.bus.LookupReplyContext(msg.Channel, msg.ChatID, candidate); ok {
				if sessionKey := utils.CanonicalSessionKey(replyCtx.SessionKey); sessionKey != "" {
					return sessionKey
				}
			}
		}
	}
	return conversationSessionKey
}

func (al *AgentLoop) resolveAgentForSession(
	sessionKey string,
	route routing.ResolvedRoute,
) (*AgentInstance, error) {
	var agent *AgentInstance
	if parsed := routing.ParseAgentSessionKey(sessionKey); parsed != nil {
		if a, ok := al.registry.GetAgent(parsed.AgentID); ok {
			agent = a
		}
	}
	if agent == nil {
		if a, ok := al.registry.GetAgent(route.AgentID); ok {
			agent = a
		} else {
			agent = al.registry.GetDefaultAgent()
		}
	}
	if agent == nil {
		return nil, fmt.Errorf("no agent available for route (agent_id=%s)", route.AgentID)
	}
	return agent, nil
}

func resetMessageToolState(agent *AgentInstance) {
	if tool, ok := agent.Tools.Get("message"); ok {
		if resetter, ok := tool.(messageToolRoundResetter); ok {
			resetter.ResetSentInRound()
		}
	}
}

func (al *AgentLoop) buildUserMessage(agent *AgentInstance, msg bus.InboundMessage) string {
	userMessage := msg.Content
	if body, ok := extractSteeringContent(userMessage); ok {
		userMessage = body
	}
	if note := al.importInboundMediaAndBuildNote(agent, msg); note != "" {
		if strings.TrimSpace(userMessage) != "" {
			var msgBuilder strings.Builder
			msgBuilder.Grow(len(userMessage) + len(note) + 2)
			msgBuilder.WriteString(userMessage)
			msgBuilder.WriteString("\n\n")
			msgBuilder.WriteString(note)
			userMessage = msgBuilder.String()
		} else {
			userMessage = note
		}
	}
	return userMessage
}

func (al *AgentLoop) resolvePlanMode(
	cfg *config.Config,
	agent *AgentInstance,
	msg bus.InboundMessage,
	sessionKey, userMessage string,
) bool {
	if cfg == nil || !cfg.Tools.PlanMode.Enabled {
		return false
	}

	defaultMode := sessionPermissionModeRun
	modeText := strings.TrimSpace(cfg.Tools.PlanMode.DefaultMode)
	if strings.EqualFold(strings.TrimSpace(msg.Peer.Kind), "group") && strings.TrimSpace(cfg.Tools.PlanMode.DefaultModeGroup) != "" {
		modeText = strings.TrimSpace(cfg.Tools.PlanMode.DefaultModeGroup)
	}
	if strings.EqualFold(strings.TrimSpace(modeText), "plan") {
		defaultMode = sessionPermissionModePlan
	}

	permWorkspace := agent.Workspace
	if da := al.registry.GetDefaultAgent(); da != nil && strings.TrimSpace(da.Workspace) != "" {
		permWorkspace = da.Workspace
	}
	perm := loadSessionPermissionStateWithDefault(permWorkspace, sessionKey, defaultMode)
	if !perm.isPlan() {
		return false
	}

	if strings.TrimSpace(userMessage) != "" {
		perm.PendingTask = userMessage
		if err := saveSessionPermissionState(permWorkspace, sessionKey, perm); err != nil {
			logger.WarnCF("agent", "Failed to persist plan-mode pending task (best-effort)", map[string]any{
				"session_key": sessionKey,
				"error":       err.Error(),
			})
		}
	}
	return true
}

func parseSystemMessageOrigin(chatID string) (string, string) {
	if idx := strings.Index(chatID, ":"); idx > 0 {
		return chatID[:idx], chatID[idx+1:]
	}
	return "cli", chatID
}

func extractSystemMessageResult(content string) string {
	if idx := strings.Index(content, "Result:\n"); idx >= 0 {
		return content[idx+8:]
	}
	return content
}

func (al *AgentLoop) resolveSystemMessageSessionKey(
	msg bus.InboundMessage,
	originChannel, originChatID string,
) string {
	sessionKey := utils.CanonicalSessionKey(msg.SessionKey)
	if sessionKey != "" {
		return sessionKey
	}

	cfg := al.Config()
	dmScope := routing.DMScopeMain
	identityLinks := map[string][]string(nil)
	if cfg != nil {
		if v := routing.DMScope(strings.TrimSpace(cfg.Session.DMScope)); v != "" {
			dmScope = v
		}
		identityLinks = cfg.Session.IdentityLinks
	}
	return utils.CanonicalSessionKey(routing.BuildConversationPeerSessionKey(routing.SessionKeyParams{
		Channel:       originChannel,
		AccountID:     msg.Metadata["account_id"],
		Peer:          &routing.RoutePeer{Kind: "direct", ID: originChatID},
		DMScope:       dmScope,
		IdentityLinks: identityLinks,
	}))
}

func (al *AgentLoop) resolveSystemMessageAgent(sessionKey string) (*AgentInstance, error) {
	agent := al.registry.GetDefaultAgent()
	if parsed := routing.ParseAgentSessionKey(sessionKey); parsed != nil {
		if a, ok := al.registry.GetAgent(parsed.AgentID); ok && a != nil {
			agent = a
		}
	}
	if agent == nil {
		return nil, fmt.Errorf("no agent available for system message (session_key=%s)", sessionKey)
	}
	return agent, nil
}

func (al *AgentLoop) prepareRun(agent *AgentInstance, opts *processOptions, cfg *config.Config) preparedRun {
	history, summary := loadRunHistory(agent, *opts)
	ws := NewWorkingState(opts.UserMessage)
	opts.WorkingState = ws

	llmUserMessage := buildRunUserMessage(cfg, *opts)
	messages := agent.ContextBuilder.BuildMessagesForSession(
		opts.SessionKey,
		history,
		summary,
		llmUserMessage,
		opts.Media,
		opts.Channel,
		opts.ChatID,
		ws,
	)
	messages = resolveMediaRefs(messages, al.mediaResolver, resolveMaxMediaSize(cfg))

	modelForRun := resolveRunModel(agent, opts.SessionKey)
	runTrace := newRunTraceForMessages(agent, *opts, cfg, messages, modelForRun)
	return preparedRun{
		messages:    messages,
		modelForRun: modelForRun,
		runTrace:    runTrace,
	}
}

// --- merged from steering.go ---

type steeringInboxKey struct{}

func withSteeringInbox(ctx context.Context, inbox <-chan bus.InboundMessage) context.Context {
	if ctx == nil || inbox == nil {
		return ctx
	}
	return context.WithValue(ctx, steeringInboxKey{}, inbox)
}

func steeringInboxFromContext(ctx context.Context) <-chan bus.InboundMessage {
	if ctx == nil {
		return nil
	}
	v := ctx.Value(steeringInboxKey{})
	if v == nil {
		return nil
	}
	if ch, ok := v.(<-chan bus.InboundMessage); ok {
		return ch
	}
	if ch, ok := v.(chan bus.InboundMessage); ok {
		return ch
	}
	return nil
}

// extractSteeringContent recognizes "/steer <text>" style messages.
// It returns the message body and true only when a non-empty body exists.
func extractSteeringContent(content string) (string, bool) {
	raw := strings.TrimSpace(content)
	if raw == "" {
		return "", false
	}
	fields := strings.Fields(raw)
	if len(fields) < 2 {
		return "", false
	}
	cmd := strings.ToLower(strings.TrimSpace(fields[0]))
	if cmd != "/steer" && cmd != "/steering" {
		return "", false
	}
	body := strings.TrimSpace(raw[len(fields[0]):])
	if body == "" {
		return "", false
	}
	return body, true
}

// --- merged from working_state.go ---

// WorkingState tracks the agent's current task progress as structured data.
// Instead of relying on the LLM to extract state from long conversation history,
// this provides an explicit, maintained state object that is injected into the
// context on each LLM call.
//
// This is the "working memory" layer — more structured than conversation history
// (short-term), less persistent than MEMORY.md (long-term).
