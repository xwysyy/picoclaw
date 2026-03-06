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
	al.ensureActiveAgentForSession(sessionKey, agent)
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
	al.ensureActiveAgentForSession(sessionKey, agent)

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
	sessionKey := conversationSessionKey
	if explicit := utils.CanonicalSessionKey(msg.SessionKey); explicit != "" {
		if strings.HasPrefix(explicit, "agent:") ||
			strings.HasPrefix(explicit, "conv:") ||
			constants.IsInternalChannel(msg.Channel) {
			sessionKey = explicit
		}
	}
	return sessionKey
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

func (al *AgentLoop) ensureActiveAgentForSession(sessionKey string, agent *AgentInstance) {
	_ = sessionKey
	_ = agent
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
			userMessage += "\n\n" + note
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

func (al *AgentLoop) recordLastExternalChannel(opts processOptions) {
	if opts.Channel == "" || opts.ChatID == "" || constants.IsInternalChannel(opts.Channel) {
		return
	}
	channelKey := fmt.Sprintf("%s:%s", opts.Channel, opts.ChatID)
	if err := al.RecordLastChannel(channelKey); err != nil {
		logger.WarnCF(
			"agent",
			"Failed to record last channel",
			map[string]any{"error": err.Error()},
		)
	}
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

func loadRunHistory(agent *AgentInstance, opts processOptions) ([]providers.Message, string) {
	if opts.NoHistory {
		return nil, ""
	}
	return agent.Sessions.GetHistory(opts.SessionKey), agent.Sessions.GetSummary(opts.SessionKey)
}

func buildRunUserMessage(cfg *config.Config, opts processOptions) string {
	if !opts.PlanMode {
		return opts.UserMessage
	}
	restricted := "exec/edit_file/write_file/append_file"
	if cfg != nil && len(cfg.Tools.PlanMode.RestrictedTools) > 0 {
		restricted = strings.Join(cfg.Tools.PlanMode.RestrictedTools, ", ")
	}
	return fmt.Sprintf(
		"[PLAN MODE]\nYou are currently in PLAN mode for this session.\n"+
			"- You MUST NOT call restricted tools (%s).\n"+
			"- Draft a plan, ask the user to approve execution (/approve or /run), then stop.\n\n"+
			"USER REQUEST:\n%s",
		restricted,
		opts.UserMessage,
	)
}

func resolveMaxMediaSize(cfg *config.Config) int {
	if cfg == nil {
		return config.DefaultMaxMediaSize
	}
	return cfg.Agents.Defaults.GetMaxMediaSize()
}

func resolveRunModel(agent *AgentInstance, sessionKey string) string {
	modelForRun := strings.TrimSpace(agent.Model)
	if agent.Sessions != nil {
		if override, ok := agent.Sessions.EffectiveModelOverride(sessionKey); ok {
			modelForRun = override
		}
	}
	return modelForRun
}

func newRunTraceForMessages(
	agent *AgentInstance,
	opts processOptions,
	cfg *config.Config,
	messages []providers.Message,
	modelForRun string,
) *runTraceWriter {
	runTraceEnabled := cfg != nil && cfg.Tools.Trace.Enabled
	runTrace := newRunTraceWriter(agent.Workspace, runTraceEnabled, opts, agent.ID, modelForRun)
	if runTrace == nil {
		return nil
	}
	if opts.Resume {
		runTrace.recordResume(opts.UserMessage, len(messages), len(agent.Tools.List()))
	} else {
		runTrace.recordStart(opts.UserMessage, len(messages), len(agent.Tools.List()))
	}
	return runTrace
}

func persistUserMessage(agent *AgentInstance, opts processOptions) {
	addSessionMessageAndSave(
		agent.Sessions,
		opts.SessionKey,
		"user",
		opts.UserMessage,
		"Failed to WAL user message (best-effort)",
		nil,
	)
}

func (al *AgentLoop) finalizeRun(
	ctx context.Context,
	agent *AgentInstance,
	opts processOptions,
	cfg *config.Config,
	runTrace *runTraceWriter,
	finalContent string,
	iteration int,
) string {
	if finalContent == "" {
		finalContent = opts.DefaultResponse
	}

	addSessionMessage(agent.Sessions, opts.SessionKey, "assistant", finalContent)
	saveSessionBestEffort(agent.Sessions, opts.SessionKey, "Failed to persist assistant message (best-effort)", nil)

	if opts.EnableSummary {
		al.maybeSummarize(agent, opts.SessionKey, opts.Channel, opts.ChatID)
	}
	if opts.SendResponse {
		al.bus.PublishOutbound(ctx, bus.OutboundMessage{
			Channel: opts.Channel,
			ChatID:  opts.ChatID,
			Content: finalContent,
		})
	}

	responsePreview := utils.Truncate(finalContent, 120)
	logger.InfoCF("agent", fmt.Sprintf("Response: %s", responsePreview),
		map[string]any{
			"agent_id":     agent.ID,
			"session_key":  opts.SessionKey,
			"iterations":   iteration,
			"final_length": len(finalContent),
		})

	if runTrace != nil {
		runTrace.recordEnd(iteration, finalContent)
	}
	if cfg != nil && cfg.Notify.OnTaskComplete && constants.IsInternalChannel(opts.Channel) {
		al.notifyLastActiveOnInternalRun(ctx, agent, opts, finalContent)
	}
	return finalContent
}

func (al *AgentLoop) notifyLastActiveOnInternalRun(
	ctx context.Context,
	agent *AgentInstance,
	opts processOptions,
	finalContent string,
) {
	trimmedResult := strings.TrimSpace(finalContent)
	if trimmedResult == "" ||
		strings.EqualFold(trimmedResult, "NO_UPDATE") ||
		strings.EqualFold(trimmedResult, "HEARTBEAT_OK") {
		return
	}

	targetCh, targetChat := al.LastActive()
	if strings.TrimSpace(targetCh) == "" || strings.TrimSpace(targetChat) == "" || constants.IsInternalChannel(targetCh) {
		return
	}

	notifyText := fmt.Sprintf(
		"✅ Task complete\n\nTask:\n%s\n\nResult:\n%s",
		utils.Truncate(strings.TrimSpace(opts.UserMessage), 240),
		utils.Truncate(strings.TrimSpace(finalContent), 1200),
	)
	if tool, ok := agent.Tools.Get("message"); ok && tool != nil {
		_ = tool.Execute(ctx, map[string]any{
			"content": notifyText,
			"channel": targetCh,
			"chat_id": targetChat,
		})
		return
	}
	if al.bus != nil {
		_ = al.bus.PublishOutbound(ctx, bus.OutboundMessage{
			Channel: targetCh,
			ChatID:  targetChat,
			Content: notifyText,
		})
	}
}
