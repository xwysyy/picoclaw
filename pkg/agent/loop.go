// X-Claw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 X-Claw contributors

package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xwysyy/X-Claw/pkg/auditlog"
	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/constants"
	"github.com/xwysyy/X-Claw/pkg/logger"
	"github.com/xwysyy/X-Claw/pkg/mcp"
	"github.com/xwysyy/X-Claw/pkg/providers"
	"github.com/xwysyy/X-Claw/pkg/routing"
	"github.com/xwysyy/X-Claw/pkg/session"
	"github.com/xwysyy/X-Claw/pkg/state"
	"github.com/xwysyy/X-Claw/pkg/tools"
	"github.com/xwysyy/X-Claw/pkg/utils"
	"github.com/xwysyy/X-Claw/pkg/voice"
)

type AgentLoop struct {
	bus              *bus.MessageBus
	cfgMu            sync.RWMutex
	cfg              *config.Config
	registry         *AgentRegistry
	sessions         session.Store
	state            *state.Manager
	taskLedger       *tools.TaskLedger
	running          atomic.Bool
	summarizing      sync.Map
	fallback         *providers.FallbackChain
	channelDirectory ChannelDirectory
	mediaResolver    MediaResolver
	transcriber      voice.Transcriber
	mcpMgr           *mcp.Manager

	tokenUsageMu     sync.Mutex
	tokenUsageStores map[string]*tokenUsageStore // workspace → store

	modelAutoMu           sync.Mutex
	modelAutoDowngradeMap map[string]sessionModelAutoDowngradeState // session_key -> state
}

// processOptions configures how a message is processed
type processOptions struct {
	SessionKey  string // Session identifier for history/context
	Channel     string // Target channel for tool execution
	ChatID      string // Target chat ID for tool execution
	SenderID    string // Message sender identifier for tool execution
	UserMessage string // User message content (may include trigger prefix)
	Media       []string

	DefaultResponse string // Response when LLM returns empty
	EnableSummary   bool   // Whether to trigger summarization
	SendResponse    bool   // Whether to send response via bus
	NoHistory       bool   // If true, don't load session history (for heartbeat)

	// Steering provides out-of-band user messages delivered while this run is still
	// executing. It is used by the Gateway inbound queue to support "/steer ..."
	// without waiting for the current run to finish.
	Steering <-chan bus.InboundMessage

	// PlanMode enables "plan" permission mode (ROADMAP.md:1225).
	// When true, side-effect tools are denied by the tool executor.
	PlanMode bool

	// WorkingState carries per-run structured progress state. It must be per-run
	// (not stored globally on the agent) so multiple sessions can be processed
	// concurrently without cross-talk.
	WorkingState *WorkingState

	// Phase E2: resume support
	RunID  string // optional: resume into an existing run_id
	Resume bool   // true when invoked by resume_last_task
}

const defaultResponse = "I've completed processing but have no response to give. Increase `max_tool_iterations` in config.json."

// isLLMTimeoutError checks if an error is a network/HTTP timeout (not a context window error).
func isLLMTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "client.timeout") ||
		strings.Contains(msg, "timed out") ||
		strings.Contains(msg, "timeout exceeded")
}

// isContextWindowError detects real context window / token limit errors.
func isContextWindowError(err error) bool {
	if isLLMTimeoutError(err) {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context_length_exceeded") ||
		strings.Contains(msg, "context window") ||
		strings.Contains(msg, "maximum context length") ||
		strings.Contains(msg, "token limit") ||
		strings.Contains(msg, "too many tokens") ||
		strings.Contains(msg, "max_tokens") ||
		strings.Contains(msg, "invalidparameter") ||
		strings.Contains(msg, "prompt is too long") ||
		strings.Contains(msg, "request too large")
}

type sessionModelAutoDowngradeState struct {
	TargetModel string
	Streak      int
	LastAt      time.Time
}

func NewAgentLoop(cfg *config.Config, msgBus *bus.MessageBus, provider providers.LLMProvider) *AgentLoop {
	registry := NewAgentRegistry(cfg, provider)

	// Set up shared fallback chain
	cooldown := providers.NewCooldownTracker()
	fallbackChain := providers.NewFallbackChain(cooldown)

	// MCP Bridge manager (Phase D1/D2).
	mcpMgr := mcp.NewManager()

	// Create state manager using default agent's workspace for channel recording
	defaultAgent := registry.GetDefaultAgent()
	var stateManager *state.Manager
	ledgerPath := filepath.Join(cfg.WorkspacePath(), "tasks", "ledger.json")
	sessionsPath := filepath.Join(cfg.WorkspacePath(), "sessions")
	if defaultAgent != nil {
		stateManager = state.NewManager(defaultAgent.Workspace)
		ledgerPath = filepath.Join(defaultAgent.Workspace, "tasks", "ledger.json")
		sessionsPath = filepath.Join(defaultAgent.Workspace, "sessions")
	}
	taskLedger := tools.NewTaskLedger(ledgerPath)

	// Shared sessions keep conversation history in one place for the slim runtime.
	sharedSessions := session.NewSessionManager(sessionsPath)
	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok || agent == nil {
			continue
		}
		agent.Sessions = sharedSessions
		// Re-register session tools against the shared session manager.
		agent.Tools.Register(tools.NewSessionsListTool(sharedSessions))
		agent.Tools.Register(tools.NewSessionsHistoryTool(sharedSessions))
	}

	al := &AgentLoop{
		bus:         msgBus,
		cfg:         cfg,
		registry:    registry,
		sessions:    sharedSessions,
		state:       stateManager,
		taskLedger:  taskLedger,
		summarizing: sync.Map{},
		fallback:    fallbackChain,
		mcpMgr:      mcpMgr,

		tokenUsageStores: make(map[string]*tokenUsageStore),

		modelAutoDowngradeMap: make(map[string]sessionModelAutoDowngradeState),
	}

	// Register shared tools to all agents.
	registerSharedTools(cfg, msgBus, registry)

	// Phase H3: append-only operational audit log.
	al.configureAuditLog(cfg)

	return al
}

// Config returns the current configuration snapshot for the agent loop.
func (al *AgentLoop) Config() *config.Config {
	if al == nil {
		return nil
	}
	al.cfgMu.RLock()
	defer al.cfgMu.RUnlock()
	return al.cfg
}

// SessionStore returns the shared session store used by this agent loop.
// It may be nil if the loop is not fully initialized.
func (al *AgentLoop) SessionStore() session.Store {
	if al == nil {
		return nil
	}
	return al.sessions
}

// SetConfig swaps the configuration pointer used by the agent loop.
// This is used by the gateway config hot reload path.
func (al *AgentLoop) SetConfig(cfg *config.Config) {
	if al == nil {
		return
	}
	al.cfgMu.Lock()
	al.cfg = cfg
	al.cfgMu.Unlock()

	// Keep audit log writers in sync with hot-reloaded config.
	al.configureAuditLog(cfg)
}

func (al *AgentLoop) configureAuditLog(cfg *config.Config) {
	if al == nil || cfg == nil {
		return
	}

	// Configure for the "main" workspace path as well as each agent workspace
	// (multi-agent setups may use per-agent workspaces).
	auditlog.Configure(cfg.WorkspacePath(), cfg.AuditLog)
	if al.registry == nil {
		return
	}
	for _, agentID := range al.registry.ListAgentIDs() {
		agent, ok := al.registry.GetAgent(agentID)
		if !ok || agent == nil {
			continue
		}
		auditlog.Configure(agent.Workspace, cfg.AuditLog)
	}
}

// ReloadMCPTools refreshes MCP servers and re-registers tools into each agent registry.
// This is best-effort and safe to call multiple times.
func (al *AgentLoop) ReloadMCPTools(ctx context.Context) {
	if al == nil || al.registry == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	cfg := al.Config()

	// Always unregister old MCP tools first to avoid stale tool definitions.
	for _, agentID := range al.registry.ListAgentIDs() {
		agent, ok := al.registry.GetAgent(agentID)
		if !ok || agent == nil || agent.Tools == nil {
			continue
		}
		agent.Tools.UnregisterPrefix("mcp_")
		if agent.ContextBuilder != nil {
			agent.ContextBuilder.InvalidateCache()
		}
	}

	oldMgr := al.mcpMgr

	// Disabled or empty config → close connections and exit.
	if cfg == nil || !cfg.Tools.MCP.Enabled || len(cfg.Tools.MCP.Servers) == 0 {
		if oldMgr != nil {
			_ = oldMgr.Close()
		}
		al.mcpMgr = mcp.NewManager()
		return
	}

	newMgr := mcp.NewManager()
	if err := newMgr.LoadFromConfig(ctx, cfg); err != nil {
		logger.WarnCF("agent", "MCP manager load failed (best-effort)", map[string]any{
			"error": err.Error(),
		})
	}

	// Deterministic registration order for stable prompts / KV cache.
	all := newMgr.GetAllTools()
	serverNames := make([]string, 0, len(all))
	for name := range all {
		serverNames = append(serverNames, name)
	}
	sort.Strings(serverNames)

	for _, agentID := range al.registry.ListAgentIDs() {
		agent, ok := al.registry.GetAgent(agentID)
		if !ok || agent == nil || agent.Tools == nil {
			continue
		}

		for _, serverName := range serverNames {
			for _, toolDef := range all[serverName] {
				if toolDef == nil {
					continue
				}
				agent.Tools.Register(tools.NewMCPTool(newMgr, serverName, toolDef))
			}
		}

		if agent.ContextBuilder != nil {
			agent.ContextBuilder.InvalidateCache()
		}
	}

	al.mcpMgr = newMgr
	if oldMgr != nil {
		_ = oldMgr.Close()
	}
}

func (al *AgentLoop) tokenUsageStore(workspace string) *tokenUsageStore {
	if al == nil {
		return nil
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil
	}

	al.tokenUsageMu.Lock()
	defer al.tokenUsageMu.Unlock()

	if al.tokenUsageStores == nil {
		al.tokenUsageStores = make(map[string]*tokenUsageStore)
	}
	if s, ok := al.tokenUsageStores[workspace]; ok && s != nil {
		return s
	}
	s := newTokenUsageStore(workspace)
	al.tokenUsageStores[workspace] = s
	return s
}

func pickFirstDifferentModel(current string, candidates []providers.FallbackCandidate) string {
	current = strings.TrimSpace(current)
	for _, c := range candidates {
		m := strings.TrimSpace(c.Model)
		if m == "" {
			continue
		}
		if current == "" || !strings.EqualFold(m, current) {
			return m
		}
	}
	return ""
}

func (al *AgentLoop) clearModelAutoDowngradeState(sessionKey string) {
	if al == nil {
		return
	}
	sessionKey = utils.CanonicalSessionKey(sessionKey)
	if sessionKey == "" {
		return
	}
	al.modelAutoMu.Lock()
	delete(al.modelAutoDowngradeMap, sessionKey)
	al.modelAutoMu.Unlock()
}

func (al *AgentLoop) maybeAutoDowngradeSessionModel(
	workspace string,
	trace *runTraceWriter,
	agentID string,
	sessionKey string,
	runID string,
	channel string,
	chatID string,
	senderID string,
	iteration int,
	fromModel string,
	targetModel string,
	trigger string,
	fallbackAttempts []providers.FallbackAttempt,
) bool {
	if al == nil {
		return false
	}
	cfg := al.Config()
	if cfg == nil {
		return false
	}

	sessionKey = utils.CanonicalSessionKey(sessionKey)
	if sessionKey == "" {
		return false
	}

	targetModel = strings.TrimSpace(targetModel)
	fromModel = strings.TrimSpace(fromModel)
	if targetModel == "" || strings.EqualFold(targetModel, fromModel) {
		return false
	}

	policy := cfg.Agents.Defaults.SessionModelAutoDowngrade
	if !policy.Enabled {
		return false
	}
	if al.sessions == nil {
		return false
	}
	// Respect explicit/manual overrides.
	if _, ok := al.sessions.EffectiveModelOverride(sessionKey); ok {
		return false
	}

	threshold := policy.Threshold
	if threshold <= 0 {
		threshold = 2
	}
	windowMinutes := policy.WindowMinutes
	if windowMinutes <= 0 {
		windowMinutes = 15
	}
	ttlMinutes := policy.TTLMinutes
	if ttlMinutes <= 0 {
		ttlMinutes = 60
	}

	window := time.Duration(windowMinutes) * time.Minute
	ttl := time.Duration(ttlMinutes) * time.Minute

	now := time.Now()

	al.modelAutoMu.Lock()
	state := al.modelAutoDowngradeMap[sessionKey]
	if !state.LastAt.IsZero() && now.Sub(state.LastAt) > window {
		state = sessionModelAutoDowngradeState{}
	}
	if state.TargetModel != "" && !strings.EqualFold(strings.TrimSpace(state.TargetModel), targetModel) {
		state = sessionModelAutoDowngradeState{}
	}
	state.TargetModel = targetModel
	state.LastAt = now
	state.Streak++
	shouldSwitch := state.Streak >= threshold
	if !shouldSwitch {
		al.modelAutoDowngradeMap[sessionKey] = state
		al.modelAutoMu.Unlock()
		return false
	}
	delete(al.modelAutoDowngradeMap, sessionKey)
	al.modelAutoMu.Unlock()

	expiresAt, err := al.sessions.SetModelOverride(sessionKey, targetModel, ttl)
	if err != nil {
		logger.WarnCF("agent", "Session model auto-downgrade failed (best-effort)", map[string]any{
			"session_key": sessionKey,
			"from_model":  fromModel,
			"to_model":    targetModel,
			"error":       err.Error(),
		})
		return false
	}

	// Audit log (Phase H3): must be traceable.
	reasons := make(map[string]int)
	for _, a := range fallbackAttempts {
		r := strings.TrimSpace(string(a.Reason))
		if r == "" {
			continue
		}
		reasons[r]++
	}
	reasonKeys := make([]string, 0, len(reasons))
	for k := range reasons {
		reasonKeys = append(reasonKeys, k)
	}
	sort.Strings(reasonKeys)
	reasonParts := make([]string, 0, len(reasonKeys))
	for _, k := range reasonKeys {
		reasonParts = append(reasonParts, fmt.Sprintf("%s=%d", k, reasons[k]))
	}
	reasonSummary := strings.Join(reasonParts, ",")

	expiresText := ""
	if expiresAt != nil {
		expiresText = expiresAt.UTC().Format(time.RFC3339Nano)
	}
	note := fmt.Sprintf(
		"trigger=%s from=%q to=%q threshold=%d window_minutes=%d ttl_minutes=%d attempts=%d reasons=%q expires_at=%s",
		strings.TrimSpace(trigger),
		fromModel,
		targetModel,
		threshold,
		windowMinutes,
		ttlMinutes,
		len(fallbackAttempts),
		reasonSummary,
		expiresText,
	)
	auditlog.Record(workspace, auditlog.Event{
		Type:       "session.model_auto_downgrade",
		Source:     "agent",
		RunID:      strings.TrimSpace(runID),
		SessionKey: sessionKey,
		Channel:    strings.TrimSpace(channel),
		ChatID:     strings.TrimSpace(chatID),
		SenderID:   strings.TrimSpace(senderID),
		Iteration:  iteration,
		Note:       note,
	})

	if trace != nil {
		trace.appendEvent(runTraceEvent{
			Type: "model.autodowngrade",

			TS:   now.UTC().Format(time.RFC3339Nano),
			TSMS: now.UnixMilli(),

			RunID:      strings.TrimSpace(runID),
			SessionKey: sessionKey,
			Channel:    strings.TrimSpace(channel),
			ChatID:     strings.TrimSpace(chatID),
			SenderID:   strings.TrimSpace(senderID),

			AgentID: strings.TrimSpace(agentID),
			Model:   targetModel,

			Iteration:       iteration,
			ResponsePreview: utils.Truncate(note, 400),
		})
	}

	logger.InfoCF("agent", "Session model auto-downgrade applied",
		map[string]any{
			"session_key": sessionKey,
			"from_model":  fromModel,
			"to_model":    targetModel,
			"trigger":     strings.TrimSpace(trigger),
			"expires_at":  expiresText,
		})

	return true
}

// registerSharedTools registers tools shared across all runtime agents.
func (al *AgentLoop) Run(ctx context.Context) error {
	al.running.Store(true)
	cfg := al.Config()
	if cfg != nil && cfg.Audit.Enabled {
		go al.runAuditLoop(ctx)
	}

	// Ensure MCP connections are cleaned up on exit, regardless of initialization success.
	if al != nil && al.mcpMgr != nil {
		defer func() {
			if err := al.mcpMgr.Close(); err != nil {
				logger.ErrorCF("agent", "Failed to close MCP manager", map[string]any{"error": err.Error()})
			}
		}()
	}

	// Best-effort: ensure MCP tools are registered on startup (and connections
	// are established if enabled). This is safe to call multiple times.
	if cfg != nil && cfg.Tools.MCP.Enabled {
		al.ReloadMCPTools(ctx)
	}

	queueEnabled := true
	maxConc := 1
	perSessionBuf := 32
	if cfg != nil {
		queueEnabled = cfg.Gateway.InboundQueue.Enabled
		maxConc = cfg.Gateway.InboundQueue.MaxConcurrency
		perSessionBuf = cfg.Gateway.InboundQueue.PerSessionBuffer
	}
	if maxConc <= 0 {
		maxConc = 1
	}
	if perSessionBuf <= 0 {
		perSessionBuf = 32
	}

	processOne := func(msg bus.InboundMessage, steering <-chan bus.InboundMessage) {
		roundTracker := &tools.MessageRoundTracker{}
		msgCtx := tools.WithMessageRoundTracker(ctx, roundTracker)
		msgCtx = withSteeringInbox(msgCtx, steering)

		response, err := al.processMessage(msgCtx, msg)
		if err != nil {
			response = fmt.Sprintf("Error processing message: %v", err)
		}

		if response == "" {
			return
		}

		if roundTracker.Sent() {
			logger.DebugCF("agent", "Skipped outbound (message tool already sent)", map[string]any{
				"channel": msg.Channel,
			})
			return
		}

		_ = al.bus.PublishOutbound(msgCtx, bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: response,
		})
		logger.InfoCF("agent", "Published outbound response",
			map[string]any{
				"channel":     msg.Channel,
				"chat_id":     msg.ChatID,
				"content_len": len(response),
			})
	}

	if !queueEnabled {
		for al.running.Load() {
			msg, ok := al.bus.ConsumeInbound(ctx)
			if !ok {
				return nil
			}
			processOne(msg, nil)
		}
		return nil
	}

	type bucket struct {
		ch     chan bus.InboundMessage
		steer  chan bus.InboundMessage
		active atomic.Bool
	}

	globalSem := make(chan struct{}, maxConc)
	buckets := make(map[string]*bucket)
	var bucketsMu sync.Mutex

	getBucketKey := func(msg bus.InboundMessage) string {
		// Prefer an explicit session key when it is known to be safe/stable.
		explicit := utils.CanonicalSessionKey(msg.SessionKey)
		if explicit != "" {
			if strings.HasPrefix(explicit, "agent:") || strings.HasPrefix(explicit, "conv:") || constants.IsInternalChannel(msg.Channel) {
				return explicit
			}
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

		// System messages route back to the originating conversation.
		// ChatID format: "origin_channel:origin_chat_id".
		if msg.Channel == "system" {
			originChannel, originChatID := "cli", strings.TrimSpace(msg.ChatID)
			if idx := strings.Index(msg.ChatID, ":"); idx > 0 {
				originChannel = strings.TrimSpace(msg.ChatID[:idx])
				originChatID = strings.TrimSpace(msg.ChatID[idx+1:])
			}
			key := utils.CanonicalSessionKey(routing.BuildConversationPeerSessionKey(routing.SessionKeyParams{
				Channel:       originChannel,
				AccountID:     msg.Metadata["account_id"],
				Peer:          &routing.RoutePeer{Kind: "direct", ID: originChatID},
				ThreadID:      msg.Metadata["thread_id"],
				DMScope:       dmScope,
				IdentityLinks: identityLinks,
			}))
			if key != "" {
				return key
			}
		}

		key := utils.CanonicalSessionKey(routing.BuildConversationPeerSessionKey(routing.SessionKeyParams{
			Channel:       msg.Channel,
			AccountID:     msg.Metadata["account_id"],
			Peer:          extractPeer(msg),
			ThreadID:      msg.Metadata["thread_id"],
			DMScope:       dmScope,
			IdentityLinks: identityLinks,
		}))
		if key == "" {
			key = utils.CanonicalSessionKey(strings.TrimSpace(msg.Channel) + ":" + strings.TrimSpace(msg.ChatID))
		}
		return key
	}

	enqueue := func(msg bus.InboundMessage) {
		key := getBucketKey(msg)

		bucketsMu.Lock()
		b := buckets[key]
		if b == nil {
			b = &bucket{
				ch:    make(chan bus.InboundMessage, perSessionBuf),
				steer: make(chan bus.InboundMessage, 16),
			}
			buckets[key] = b

			// One worker per session key: strict in-order processing within the session.
			go func(key string, b *bucket) {
				for {
					select {
					case <-ctx.Done():
						return
					case msg := <-b.ch:
						// Drop any steering messages that arrived after the last run completed.
						for {
							select {
							case <-b.steer:
								// discard
							default:
								goto drained
							}
						}
					drained:

						b.active.Store(true)
						globalSem <- struct{}{}
						func() {
							defer func() {
								<-globalSem
								b.active.Store(false)
							}()
							processOne(msg, b.steer)
						}()
					}
				}
			}(key, b)
		}
		bucketsMu.Unlock()

		// Steering: while a session is actively running, allow out-of-band "/steer ..."
		// messages to be injected into the current run rather than queued behind it.
		if b != nil && b.active.Load() {
			if body, ok := extractSteeringContent(msg.Content); ok {
				steerMsg := msg
				steerMsg.Content = body
				select {
				case b.steer <- steerMsg:
					return
				default:
					// If the steering inbox is full, fall back to normal enqueue to avoid losing input.
				}
			}
		}

		// Backpressure: if this session queue is full, we block here. This keeps
		// strict ordering and prevents unbounded memory growth.
		b.ch <- msg
	}

	for al.running.Load() {
		msg, ok := al.bus.ConsumeInbound(ctx)
		if !ok {
			return nil
		}
		enqueue(msg)
	}

	return nil
}

func (al *AgentLoop) Stop() {
	al.running.Store(false)
}

func (al *AgentLoop) RegisterTool(tool tools.Tool) {
	for _, agentID := range al.registry.ListAgentIDs() {
		if agent, ok := al.registry.GetAgent(agentID); ok {
			agent.Tools.Register(tool)
		}
	}
}

func (al *AgentLoop) SetChannelManager(dir ChannelDirectory) {
	al.channelDirectory = dir
}

// SetMediaResolver injects a media resolver for media:// lifecycle lookups.
func (al *AgentLoop) SetMediaResolver(r MediaResolver) {
	al.mediaResolver = r
}

func (al *AgentLoop) GetTaskLedger() *tools.TaskLedger {
	return al.taskLedger
}

// SetTranscriber injects a voice transcriber for agent-level audio transcription.
func (al *AgentLoop) SetTranscriber(t voice.Transcriber) {
	al.transcriber = t
}

var audioAnnotationRe = regexp.MustCompile(`\[(voice|audio)(?::[^\]]*)?\]`)

// transcribeAudioInMessage resolves audio media refs, transcribes them, and
// replaces audio annotations in msg.Content with the transcribed text.
// RecordLastChannel records the last active channel for this workspace.
// This uses the atomic state save mechanism to prevent data loss on crash.
func (al *AgentLoop) RecordLastChannel(channel string) error {
	if al.state == nil {
		return nil
	}
	return al.state.SetLastChannel(channel)
}

// RecordLastChatID records the last active chat ID for this workspace.
// This uses the atomic state save mechanism to prevent data loss on crash.
func (al *AgentLoop) RecordLastChatID(chatID string) error {
	if al.state == nil {
		return nil
	}
	return al.state.SetLastChatID(chatID)
}

// LastActive returns the most recently used channel and chat ID for this workspace.
// It is backed by the persisted state file (state/state.json), but uses the in-memory
// state manager instance so it stays up-to-date during a running gateway process.
func (al *AgentLoop) LastActive() (string, string) {
	if al == nil || al.state == nil {
		return "", ""
	}
	key := strings.TrimSpace(al.state.GetLastChannel())
	if key == "" {
		return "", ""
	}

	parts := strings.SplitN(key, ":", 2)
	if len(parts) != 2 {
		return "", ""
	}
	channel := strings.TrimSpace(parts[0])
	chatID := strings.TrimSpace(parts[1])
	if channel == "" || chatID == "" {
		return "", ""
	}
	return channel, chatID
}

func (al *AgentLoop) ProcessDirect(
	ctx context.Context,
	content, sessionKey string,
) (string, error) {
	return al.ProcessDirectWithChannel(ctx, content, sessionKey, "cli", "direct")
}

func (al *AgentLoop) ProcessDirectWithChannel(
	ctx context.Context,
	content, sessionKey, channel, chatID string,
) (string, error) {
	msg := bus.InboundMessage{
		Channel:    channel,
		SenderID:   "cron",
		ChatID:     chatID,
		Content:    content,
		SessionKey: sessionKey,
	}

	return al.processMessage(ctx, msg)
}

// ProcessSessionMessage injects a message into a specific session key directly.
// Unlike ProcessDirectWithChannel, this bypasses route-derived session rewriting.
func (al *AgentLoop) ProcessSessionMessage(
	ctx context.Context,
	content, sessionKey, channel, chatID string,
) (string, error) {
	key := utils.CanonicalSessionKey(sessionKey)
	if key == "" {
		return "", fmt.Errorf("sessionKey is required")
	}

	targetAgent := al.registry.GetDefaultAgent()
	if parsed := routing.ParseAgentSessionKey(key); parsed != nil {
		if agent, ok := al.registry.GetAgent(parsed.AgentID); ok {
			targetAgent = agent
		}
	}
	if targetAgent == nil {
		return "", fmt.Errorf("no agent available for session %q", key)
	}

	if strings.TrimSpace(channel) == "" {
		channel = "system"
	}
	if strings.TrimSpace(chatID) == "" {
		chatID = "sessions-send"
	}

	return al.runAgentLoop(ctx, targetAgent, processOptions{
		SessionKey:      key,
		Channel:         channel,
		ChatID:          chatID,
		UserMessage:     content,
		DefaultResponse: "I've completed processing but have no response to give.",
		EnableSummary:   true,
		SendResponse:    false,
	})
}

// ProcessHeartbeat processes a heartbeat request without session history.
// Each heartbeat is independent and doesn't accumulate context.
func (al *AgentLoop) ProcessHeartbeat(
	ctx context.Context,
	content, channel, chatID string,
) (string, error) {
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		return "", fmt.Errorf("no default agent for heartbeat")
	}
	return al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:      "heartbeat",
		Channel:         channel,
		ChatID:          chatID,
		UserMessage:     content,
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
		NoHistory:       true, // Don't load session history for heartbeat
	})
}
