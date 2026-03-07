package agent

import (
	"github.com/xwysyy/X-Claw/internal/core/events"

	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/constants"
	"github.com/xwysyy/X-Claw/pkg/fileutil"
	"github.com/xwysyy/X-Claw/pkg/logger"
	"github.com/xwysyy/X-Claw/pkg/providers"
	"github.com/xwysyy/X-Claw/pkg/routing"
	"github.com/xwysyy/X-Claw/pkg/session"
	"github.com/xwysyy/X-Claw/pkg/tools"
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
		if err := al.bus.PublishOutbound(ctx, bus.OutboundMessage{
			Channel: targetCh,
			ChatID:  targetChat,
			Content: notifyText,
		}); err != nil {
			logger.DebugCF("agent", "failed to publish completion notification", map[string]any{"channel": targetCh, "chat_id": targetChat, "error": err.Error()})
		}
	}
}

// --- merged from run_pipeline.go ---

func (al *AgentLoop) processMessage(ctx context.Context, msg bus.InboundMessage) (string, error) {
	return al.processMessageImpl(ctx, msg)
}

func (al *AgentLoop) processSystemMessage(ctx context.Context, msg bus.InboundMessage) (string, error) {
	return al.processSystemMessageImpl(ctx, msg)
}

func (al *AgentLoop) runAgentLoop(
	ctx context.Context,
	agent *AgentInstance,
	opts processOptions,
) (string, error) {
	return al.runAgentLoopImpl(ctx, agent, opts)
}

// --- merged from session_transcript.go ---

func addSessionMessage(store session.Store, sessionKey, role, content string) {
	if store == nil || strings.TrimSpace(sessionKey) == "" {
		return
	}
	store.AddMessage(sessionKey, role, content)
}

func addSessionMessageAndSave(store session.Store, sessionKey, role, content, warnMessage string, fields map[string]any) {
	addSessionMessage(store, sessionKey, role, content)
	saveSessionBestEffort(store, sessionKey, warnMessage, fields)
}

func addSessionFullMessage(store session.Store, sessionKey string, msg providers.Message) {
	if store == nil || strings.TrimSpace(sessionKey) == "" {
		return
	}
	store.AddFullMessage(sessionKey, msg)
}

func saveSessionBestEffort(store session.Store, sessionKey, warnMessage string, fields map[string]any) {
	if store == nil || strings.TrimSpace(sessionKey) == "" {
		return
	}
	if err := store.Save(sessionKey); err != nil {
		payload := map[string]any{"session_key": sessionKey, "error": err.Error()}
		for key, value := range fields {
			payload[key] = value
		}
		logger.WarnCF("agent", warnMessage, payload)
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
type WorkingState struct {
	mu             sync.RWMutex
	OriginalTask   string            `json:"original_task"`
	CurrentPlan    []PlanStep        `json:"current_plan,omitempty"`
	CompletedSteps []CompletedStep   `json:"completed_steps,omitempty"`
	CollectedData  map[string]string `json:"collected_data,omitempty"`
	OpenQuestions  []string          `json:"open_questions,omitempty"`
	NextAction     string            `json:"next_action,omitempty"`
	ToolCallCount  int               `json:"tool_call_count"`
	ErrorCount     int               `json:"error_count"`
}

// PlanStep represents a single step in the agent's execution plan.
type PlanStep struct {
	Description string `json:"description"`
	Status      string `json:"status"` // pending, running, done, failed, skipped
	ToolNeeded  string `json:"tool_needed,omitempty"`
}

// CompletedStep records a finished step with its outcome.
type CompletedStep struct {
	Description string `json:"description"`
	Outcome     string `json:"outcome"`
	ToolUsed    string `json:"tool_used,omitempty"`
}

// NewWorkingState creates a new WorkingState for the given task.
func NewWorkingState(task string) *WorkingState {
	return &WorkingState{
		OriginalTask:  task,
		CollectedData: make(map[string]string),
	}
}

// RecordToolCall increments the tool call counter and tracks errors.
func (ws *WorkingState) RecordToolCall(toolName string, isError bool) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.ToolCallCount++
	if isError {
		ws.ErrorCount++
	}
}

// SetNextAction updates the planned next action.
func (ws *WorkingState) SetNextAction(action string) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.NextAction = action
}

// AddCollectedData records a key piece of data gathered during execution.
func (ws *WorkingState) AddCollectedData(key, value string) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.CollectedData[key] = value
}

// AddCompletedStep records a finished step.
func (ws *WorkingState) AddCompletedStep(description, outcome, toolUsed string) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.CompletedSteps = append(ws.CompletedSteps, CompletedStep{
		Description: description,
		Outcome:     outcome,
		ToolUsed:    toolUsed,
	})
}

// FormatForContext returns a concise summary suitable for injection into the
// LLM context. Only includes non-empty sections to save tokens.
func (ws *WorkingState) FormatForContext() string {
	ws.mu.RLock()
	defer ws.mu.RUnlock()

	if ws.OriginalTask == "" {
		return ""
	}

	var sb strings.Builder
	sb.Grow(256)
	sb.WriteString("## Working State\n\n")
	fmt.Fprintf(&sb, "**Task**: %s\n", ws.OriginalTask)
	fmt.Fprintf(&sb, "**Progress**: %d tool calls, %d errors\n", ws.ToolCallCount, ws.ErrorCount)

	if len(ws.CompletedSteps) > 0 {
		sb.WriteString("\n**Completed**:\n")
		// Show only last 5 steps to save tokens
		start := 0
		if len(ws.CompletedSteps) > 5 {
			start = len(ws.CompletedSteps) - 5
			fmt.Fprintf(&sb, "- (%d earlier steps omitted)\n", start)
		}
		for _, step := range ws.CompletedSteps[start:] {
			fmt.Fprintf(&sb, "- %s → %s\n", step.Description, step.Outcome)
		}
	}

	if len(ws.CollectedData) > 0 {
		sb.WriteString("\n**Collected Data**:\n")
		for k, v := range ws.CollectedData {
			// Truncate long values
			if len(v) > 200 {
				v = v[:200] + "..."
			}
			fmt.Fprintf(&sb, "- %s: %s\n", k, v)
		}
	}

	if len(ws.OpenQuestions) > 0 {
		sb.WriteString("\n**Open Questions**:\n")
		for _, q := range ws.OpenQuestions {
			fmt.Fprintf(&sb, "- %s\n", q)
		}
	}

	if ws.NextAction != "" {
		fmt.Fprintf(&sb, "\n**Next Action**: %s\n", ws.NextAction)
	}

	return sb.String()
}

// --- merged from plan_mode.go ---

type sessionPermissionMode string

const (
	sessionPermissionModeRun  sessionPermissionMode = "run"
	sessionPermissionModePlan sessionPermissionMode = "plan"
)

type sessionPermissionState struct {
	Mode sessionPermissionMode `json:"mode,omitempty"`

	// PendingTask stores the last user request captured while in plan mode, so
	// /approve can execute it without retyping.
	PendingTask string `json:"pending_task,omitempty"`

	UpdatedAt   string `json:"updated_at,omitempty"`
	UpdatedAtMS int64  `json:"updated_at_ms,omitempty"`
}

func defaultSessionPermissionState() sessionPermissionState {
	return sessionPermissionState{Mode: sessionPermissionModeRun}
}

func normalizeSessionPermissionMode(mode sessionPermissionMode) sessionPermissionMode {
	switch strings.ToLower(strings.TrimSpace(string(mode))) {
	case string(sessionPermissionModePlan):
		return sessionPermissionModePlan
	default:
		return sessionPermissionModeRun
	}
}

func (s sessionPermissionState) normalized() sessionPermissionState {
	s.Mode = normalizeSessionPermissionMode(s.Mode)
	s.PendingTask = strings.TrimSpace(s.PendingTask)
	return s
}

func (s sessionPermissionState) isPlan() bool {
	return normalizeSessionPermissionMode(s.Mode) == sessionPermissionModePlan
}

func sessionPermissionStatePath(workspace, sessionKey string) (string, error) {
	workspace = strings.TrimSpace(workspace)
	sessionKey = utils.CanonicalSessionKey(sessionKey)
	if workspace == "" {
		return "", fmt.Errorf("workspace is required")
	}
	if sessionKey == "" {
		return "", fmt.Errorf("sessionKey is required")
	}

	token := tools.SafePathToken(sessionKey)
	if token == "" {
		token = "unknown"
	}

	return filepath.Join(workspace, ".x-claw", "state", "sessions", token, "permission.json"), nil
}

func loadSessionPermissionStateWithDefault(workspace, sessionKey string, defaultMode sessionPermissionMode) sessionPermissionState {
	path, err := sessionPermissionStatePath(workspace, sessionKey)
	if err != nil {
		return sessionPermissionState{Mode: normalizeSessionPermissionMode(defaultMode)}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return sessionPermissionState{Mode: normalizeSessionPermissionMode(defaultMode)}
	}

	var st sessionPermissionState
	if err := json.Unmarshal(data, &st); err != nil {
		return sessionPermissionState{Mode: normalizeSessionPermissionMode(defaultMode)}
	}
	return st.normalized()
}

func loadSessionPermissionState(workspace, sessionKey string) sessionPermissionState {
	return loadSessionPermissionStateWithDefault(workspace, sessionKey, sessionPermissionModeRun)
}

func saveSessionPermissionState(workspace, sessionKey string, st sessionPermissionState) error {
	path, err := sessionPermissionStatePath(workspace, sessionKey)
	if err != nil {
		return err
	}

	now := time.Now()
	st = st.normalized()
	st.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
	st.UpdatedAtMS = now.UnixMilli()

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(path, data, 0o600)
}

type ResumeCandidate struct {
	RunID      string `json:"run_id"`
	SessionKey string `json:"session_key"`
	Channel    string `json:"channel"`
	ChatID     string `json:"chat_id"`
	SenderID   string `json:"sender_id"`
	AgentID    string `json:"agent_id,omitempty"`
	Model      string `json:"model,omitempty"`

	LastEventType      string `json:"last_event_type,omitempty"`
	LastEventTSMS      int64  `json:"last_event_ts_ms,omitempty"`
	UserMessagePreview string `json:"user_message_preview,omitempty"`
}

func isInternalSessionKey(sessionKey string) bool {
	switch utils.CanonicalSessionKey(sessionKey) {
	case "heartbeat":
		return true
	default:
		return false
	}
}

func findLastUnfinishedRun(workspace string) (*ResumeCandidate, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil, fmt.Errorf("workspace is empty")
	}

	root := filepath.Join(workspace, ".x-claw", "audit", "runs")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read runs dir: %w", err)
	}

	type runState struct {
		runID      string
		sessionKey string
		channel    string
		chatID     string
		senderID   string
		agentID    string
		model      string
		userMsg    string

		lastType string
		lastTSMS int64

		endedNormally bool
	}

	byRunID := make(map[string]*runState)

	// Scan each session's events.jsonl.
	for _, e := range entries {
		if e == nil || !e.IsDir() {
			continue
		}
		// Never try to resume internal system sessions (e.g. heartbeat).
		// The resume API is designed for user-initiated runs only.
		if strings.EqualFold(strings.TrimSpace(e.Name()), "heartbeat") {
			continue
		}
		eventsPath := filepath.Join(root, e.Name(), "events.jsonl")
		f, err := os.Open(eventsPath)
		if err != nil {
			continue
		}

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var ev runTraceEvent
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				continue
			}
			rid := strings.TrimSpace(ev.RunID)
			if rid == "" {
				continue
			}

			st := byRunID[rid]
			if st == nil {
				st = &runState{runID: rid}
				byRunID[rid] = st
			}

			// Keep best-effort metadata.
			if st.sessionKey == "" && strings.TrimSpace(ev.SessionKey) != "" {
				st.sessionKey = utils.CanonicalSessionKey(ev.SessionKey)
			}
			if st.channel == "" && strings.TrimSpace(ev.Channel) != "" {
				st.channel = strings.TrimSpace(ev.Channel)
			}
			if st.chatID == "" && strings.TrimSpace(ev.ChatID) != "" {
				st.chatID = strings.TrimSpace(ev.ChatID)
			}
			if st.senderID == "" && strings.TrimSpace(ev.SenderID) != "" {
				st.senderID = strings.TrimSpace(ev.SenderID)
			}
			if st.agentID == "" && strings.TrimSpace(ev.AgentID) != "" {
				st.agentID = strings.TrimSpace(ev.AgentID)
			}
			if st.model == "" && strings.TrimSpace(ev.Model) != "" {
				st.model = strings.TrimSpace(ev.Model)
			}

			if ev.Type == events.RunStart && strings.TrimSpace(ev.UserMessagePreview) != "" {
				st.userMsg = strings.TrimSpace(ev.UserMessagePreview)
			}

			if ev.TSMS > 0 && ev.TSMS >= st.lastTSMS {
				st.lastTSMS = ev.TSMS
				st.lastType = strings.TrimSpace(string(ev.Type))
			}

			if ev.Type == events.RunEnd {
				st.endedNormally = true
			}
		}
		f.Close()
	}

	var best *runState
	for _, st := range byRunID {
		if st == nil {
			continue
		}
		if st.endedNormally {
			continue
		}
		if st.lastType == string(events.RunError) {
			continue
		}
		if isInternalSessionKey(st.sessionKey) {
			continue
		}
		// Must have enough routing info to resume safely.
		if strings.TrimSpace(st.sessionKey) == "" || strings.TrimSpace(st.channel) == "" || strings.TrimSpace(st.chatID) == "" {
			continue
		}
		if best == nil || st.lastTSMS > best.lastTSMS {
			best = st
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no unfinished runs found")
	}

	return &ResumeCandidate{
		RunID:              best.runID,
		SessionKey:         best.sessionKey,
		Channel:            best.channel,
		ChatID:             best.chatID,
		SenderID:           best.senderID,
		AgentID:            best.agentID,
		Model:              best.model,
		LastEventType:      best.lastType,
		LastEventTSMS:      best.lastTSMS,
		UserMessagePreview: best.userMsg,
	}, nil
}

func findLastUnfinishedRunAcrossWorkspaces(workspaces []string) (*ResumeCandidate, error) {
	seen := make(map[string]struct{}, len(workspaces))
	var best *ResumeCandidate

	for _, workspace := range workspaces {
		workspace = strings.TrimSpace(workspace)
		if workspace == "" {
			continue
		}
		if _, ok := seen[workspace]; ok {
			continue
		}
		seen[workspace] = struct{}{}

		candidate, err := findLastUnfinishedRun(workspace)
		if err != nil {
			continue
		}
		if best == nil || candidate.LastEventTSMS > best.LastEventTSMS {
			best = candidate
		}
	}

	if best == nil {
		return nil, fmt.Errorf("no unfinished runs found")
	}
	return best, nil
}

func resumeLastTaskPrompt() string {
	return "[resume_last_task] Continue the unfinished task from its last known state."
}

func (al *AgentLoop) ResumeLastTask(ctx context.Context) (*ResumeCandidate, string, error) {
	if al == nil || al.registry == nil {
		return nil, "", fmt.Errorf("agent loop not initialized")
	}

	workspaces := al.resumeCandidateWorkspaces()
	if len(workspaces) == 0 {
		return nil, "", fmt.Errorf("no agent available")
	}

	candidate, err := findLastUnfinishedRunAcrossWorkspaces(workspaces)
	if err != nil {
		return nil, "", err
	}

	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		return nil, "", fmt.Errorf("no agent available")
	}

	agent := defaultAgent
	if strings.TrimSpace(candidate.AgentID) != "" {
		if a, ok := al.registry.GetAgent(candidate.AgentID); ok && a != nil {
			agent = a
		}
	} else if parsed := routing.ParseAgentSessionKey(candidate.SessionKey); parsed != nil {
		if a, ok := al.registry.GetAgent(parsed.AgentID); ok && a != nil {
			agent = a
		}
	}

	// A synthetic user message acts as a deterministic "resume trigger".
	// It is stored in session WAL and the run trace records "run.resume".
	resumePrompt := resumeLastTaskPrompt()

	logger.InfoCF("agent", "Resuming last unfinished run", map[string]any{
		"run_id":      candidate.RunID,
		"session_key": candidate.SessionKey,
		"channel":     candidate.Channel,
		"chat_id":     candidate.ChatID,
		"agent_id":    agent.ID,
	})

	resp, err := al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:      candidate.SessionKey,
		Channel:         candidate.Channel,
		ChatID:          candidate.ChatID,
		SenderID:        candidate.SenderID,
		UserMessage:     resumePrompt,
		DefaultResponse: "Resume completed.",
		EnableSummary:   true,
		SendResponse:    true,
		RunID:           candidate.RunID,
		Resume:          true,
	})
	if err != nil {
		return candidate, "", err
	}

	return candidate, resp, nil
}

func (al *AgentLoop) resumeCandidateWorkspaces() []string {
	if al == nil || al.registry == nil {
		return nil
	}

	seen := make(map[string]struct{})
	workspaces := make([]string, 0, len(al.registry.ListAgentIDs())+1)
	for _, agentID := range al.registry.ListAgentIDs() {
		agent, ok := al.registry.GetAgent(agentID)
		if !ok || agent == nil {
			continue
		}
		workspace := strings.TrimSpace(agent.Workspace)
		if workspace == "" {
			continue
		}
		if _, ok := seen[workspace]; ok {
			continue
		}
		seen[workspace] = struct{}{}
		workspaces = append(workspaces, workspace)
	}
	return workspaces
}

type importedInboundMedia struct {
	RelativePath string
	Filename     string
	ContentType  string
	SizeBytes    int64
	SourceRef    string
}

func (al *AgentLoop) importInboundMediaAndBuildNote(agent *AgentInstance, msg bus.InboundMessage) string {
	if agent == nil || strings.TrimSpace(agent.Workspace) == "" {
		return ""
	}
	if len(msg.Media) == 0 {
		return ""
	}

	imported, skipped := al.importInboundMediaToWorkspace(agent.Workspace, msg)
	if len(imported) == 0 && skipped == 0 {
		return ""
	}
	return formatInboundMediaNote(imported, skipped)
}

func (al *AgentLoop) importInboundMediaToWorkspace(workspace string, msg bus.InboundMessage) ([]importedInboundMedia, int) {
	const maxFiles = 12
	const maxImportBytes = int64(30 * 1024 * 1024) // 30MB safety limit per file

	channel := sanitizePathSegment(msg.Channel)
	chatID := sanitizePathSegment(msg.ChatID)
	messageID := sanitizePathSegment(msg.MessageID)
	if messageID == "" || messageID == "unknown" {
		messageID = fmt.Sprintf("msg-%d", time.Now().UnixNano())
	}

	destDir := filepath.Join(workspace, "uploads", channel, chatID, messageID)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		logger.WarnCF("agent", "Failed to create uploads directory", map[string]any{
			"dir":   destDir,
			"error": err.Error(),
		})
		return nil, 0
	}

	imported := make([]importedInboundMedia, 0, len(msg.Media))
	skipped := 0

	for _, item := range msg.Media {
		if len(imported) >= maxFiles {
			skipped += len(msg.Media) - len(imported)
			break
		}

		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}

		srcPath, meta, ok := al.resolveInboundMedia(item)
		if !ok || strings.TrimSpace(srcPath) == "" {
			skipped++
			continue
		}

		info, err := os.Stat(srcPath)
		if err != nil || info.IsDir() {
			skipped++
			continue
		}
		if info.Size() > maxImportBytes {
			skipped++
			continue
		}

		filename := strings.TrimSpace(meta.Filename)
		if filename == "" {
			filename = filepath.Base(srcPath)
		}
		filename = utils.SanitizeFilename(filename)
		if filename == "" || filename == "." || filename == string(os.PathSeparator) {
			filename = "file"
		}

		dstName := uuid.New().String()[:8] + "_" + filename
		dstPath := filepath.Join(destDir, dstName)

		size, err := copyFile(dstPath, srcPath, 0o600)
		if err != nil {
			logger.WarnCF("agent", "Failed to import inbound media into workspace", map[string]any{
				"src":   srcPath,
				"dst":   dstPath,
				"error": err.Error(),
			})
			skipped++
			continue
		}

		rel, err := filepath.Rel(workspace, dstPath)
		if err != nil || strings.HasPrefix(rel, "..") {
			// Should never happen, but keep a safe fallback.
			skipped++
			_ = os.Remove(dstPath)
			continue
		}

		imported = append(imported, importedInboundMedia{
			RelativePath: filepath.ToSlash(rel),
			Filename:     filename,
			ContentType:  strings.TrimSpace(meta.ContentType),
			SizeBytes:    size,
			SourceRef:    item,
		})
	}

	return imported, skipped
}

func (al *AgentLoop) resolveInboundMedia(item string) (string, MediaMeta, bool) {
	if strings.HasPrefix(item, "media://") && al.mediaResolver != nil {
		localPath, meta, err := al.mediaResolver.ResolveWithMeta(item)
		if err != nil {
			return "", MediaMeta{}, false
		}
		return localPath, meta, true
	}

	// Fallback: some channels may emit raw local paths when MediaStore is not set.
	if filepath.IsAbs(item) {
		return item, MediaMeta{Filename: filepath.Base(item)}, true
	}

	return "", MediaMeta{}, false
}

func formatInboundMediaNote(imported []importedInboundMedia, skipped int) string {
	if len(imported) == 0 && skipped == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("[Uploaded files]\n")
	sb.WriteString("The user uploaded file(s). They have been saved into your workspace so tools can access them.\n")

	for _, f := range imported {
		line := "- " + f.RelativePath
		if f.ContentType != "" {
			line += " (content_type=" + f.ContentType + ")"
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	if skipped > 0 {
		sb.WriteString(fmt.Sprintf("- (skipped %d attachment(s): missing/too large/unavailable)\n", skipped))
	}

	sb.WriteString("Tip: use document_text for PDF/DOCX, read_file for plain text, or exec to inspect/convert (file/head/python etc.).")
	return strings.TrimSpace(sb.String())
}

func sanitizePathSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}

	// Only keep common safe path characters; replace others with '_'.
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}

	out := strings.Trim(b.String(), "_")
	if out == "" {
		out = "unknown"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

type copyFileDestination interface {
	io.Writer
	Close() error
}

var copyFileOpenDestination = func(dstPath string, flag int, perm os.FileMode) (copyFileDestination, error) {
	return os.OpenFile(dstPath, flag, perm)
}

func copyFile(dstPath, srcPath string, perm os.FileMode) (int64, error) {
	in, err := os.Open(srcPath)
	if err != nil {
		return 0, err
	}
	defer in.Close()

	out, err := copyFileOpenDestination(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return 0, err
	}

	n, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dstPath)
		return 0, fmt.Errorf("copy: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(dstPath)
		return 0, fmt.Errorf("close destination: %w", closeErr)
	}
	return n, nil
}
