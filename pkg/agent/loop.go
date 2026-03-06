// X-Claw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 X-Claw contributors

package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/h2non/filetype"
	"github.com/xwysyy/X-Claw/internal/core/events"
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

// resolveMediaRefs replaces media:// refs in message Media fields with base64 data URLs.
// Uses streaming base64 encoding (file handle → encoder → buffer) to avoid holding
// both raw bytes and encoded string in memory simultaneously.
// Returns a new slice; original messages are not mutated.
func resolveMediaRefs(messages []providers.Message, resolver MediaResolver, maxSize int) []providers.Message {
	if resolver == nil {
		return messages
	}

	result := make([]providers.Message, len(messages))
	copy(result, messages)

	for i, m := range result {
		if len(m.Media) == 0 {
			continue
		}

		resolved := make([]string, 0, len(m.Media))
		for _, ref := range m.Media {
			if !strings.HasPrefix(ref, "media://") {
				resolved = append(resolved, ref)
				continue
			}

			localPath, meta, err := resolver.ResolveWithMeta(ref)
			if err != nil {
				logger.WarnCF("agent", "Failed to resolve media ref", map[string]any{
					"ref":   ref,
					"error": err.Error(),
				})
				continue
			}

			info, err := os.Stat(localPath)
			if err != nil {
				logger.WarnCF("agent", "Failed to stat media file", map[string]any{
					"path":  localPath,
					"error": err.Error(),
				})
				continue
			}
			if info.Size() > int64(maxSize) {
				logger.WarnCF("agent", "Media file too large, skipping", map[string]any{
					"path":     localPath,
					"size":     info.Size(),
					"max_size": maxSize,
				})
				continue
			}

			// Determine MIME type: prefer metadata, fallback to magic-bytes detection
			mime := meta.ContentType
			if mime == "" {
				kind, ftErr := filetype.MatchFile(localPath)
				if ftErr != nil || kind == filetype.Unknown {
					logger.WarnCF("agent", "Unknown media type, skipping", map[string]any{
						"path": localPath,
					})
					continue
				}
				mime = kind.MIME.Value
			}

			// Streaming base64: open file → base64 encoder → buffer
			// Peak memory: ~1.33x file size (buffer only, no raw bytes copy)
			f, err := os.Open(localPath)
			if err != nil {
				logger.WarnCF("agent", "Failed to open media file", map[string]any{
					"path":  localPath,
					"error": err.Error(),
				})
				continue
			}

			prefix := "data:" + mime + ";base64,"
			encodedLen := base64.StdEncoding.EncodedLen(int(info.Size()))
			var buf bytes.Buffer
			buf.Grow(len(prefix) + encodedLen)
			buf.WriteString(prefix)

			encoder := base64.NewEncoder(base64.StdEncoding, &buf)
			if _, err := io.Copy(encoder, f); err != nil {
				f.Close()
				logger.WarnCF("agent", "Failed to encode media file", map[string]any{
					"path":  localPath,
					"error": err.Error(),
				})
				continue
			}
			encoder.Close()
			f.Close()

			resolved = append(resolved, buf.String())
		}

		result[i].Media = resolved
	}

	return result
}

func (al *AgentLoop) transcribeAudioInMessage(ctx context.Context, msg bus.InboundMessage) bus.InboundMessage {
	if al == nil || al.transcriber == nil || al.mediaResolver == nil || len(msg.Media) == 0 {
		return msg
	}

	transcriptions := make([]string, 0, len(msg.Media))
	for _, ref := range msg.Media {
		path, meta, err := al.mediaResolver.ResolveWithMeta(ref)
		if err != nil {
			logger.WarnCF("voice", "Failed to resolve media ref", map[string]any{"ref": ref, "error": err})
			continue
		}
		if !utils.IsAudioFile(meta.Filename, meta.ContentType) {
			continue
		}
		result, err := al.transcriber.Transcribe(ctx, path)
		if err != nil {
			logger.WarnCF("voice", "Transcription failed", map[string]any{"ref": ref, "error": err})
			transcriptions = append(transcriptions, "(transcription failed)")
			continue
		}
		transcriptions = append(transcriptions, strings.TrimSpace(result.Text))
	}

	if len(transcriptions) == 0 {
		return msg
	}

	idx := 0
	newContent := audioAnnotationRe.ReplaceAllStringFunc(msg.Content, func(match string) string {
		if idx >= len(transcriptions) {
			return match
		}
		text := transcriptions[idx]
		idx++
		if text == "" {
			return match
		}
		return "[voice: " + text + "]"
	})

	for ; idx < len(transcriptions); idx++ {
		text := strings.TrimSpace(transcriptions[idx])
		if text == "" {
			continue
		}
		newContent += "\n[voice: " + text + "]"
	}

	msg.Content = newContent
	return msg
}

func inferMediaType(filename, contentType string) string {
	ct := strings.ToLower(contentType)
	fn := strings.ToLower(filename)

	if strings.HasPrefix(ct, "image/") {
		return "image"
	}
	if strings.HasPrefix(ct, "audio/") || ct == "application/ogg" {
		return "audio"
	}
	if strings.HasPrefix(ct, "video/") {
		return "video"
	}

	ext := filepath.Ext(fn)
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg":
		return "image"
	case ".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac", ".wma", ".opus":
		return "audio"
	case ".mp4", ".avi", ".mov", ".webm", ".mkv":
		return "video"
	}

	return "file"
}

func (al *AgentLoop) targetReasoningChannelID(channelName string) (chatID string) {
	if al.channelDirectory == nil {
		return ""
	}
	return al.channelDirectory.ReasoningChannelID(channelName)
}

func (al *AgentLoop) handleReasoning(
	ctx context.Context,
	reasoningContent, channelName, channelID string,
) {
	if reasoningContent == "" || channelName == "" || channelID == "" {
		return
	}

	// Check context cancellation before attempting to publish,
	// since PublishOutbound's select may race between send and ctx.Done().
	if ctx.Err() != nil {
		return
	}

	// Use a short timeout so the goroutine does not block indefinitely when
	// the outbound bus is full.  Reasoning output is best-effort; dropping it
	// is acceptable to avoid goroutine accumulation.
	pubCtx, pubCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pubCancel()

	if err := al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Channel: channelName,
		ChatID:  channelID,
		Content: reasoningContent,
	}); err != nil {
		// Treat context.DeadlineExceeded / context.Canceled as expected
		// (bus full under load, or parent canceled).  Check the error
		// itself rather than ctx.Err(), because pubCtx may time out
		// (5 s) while the parent ctx is still active.
		// Also treat ErrBusClosed as expected — it occurs during normal
		// shutdown when the bus is closed before all goroutines finish.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
			errors.Is(err, bus.ErrBusClosed) {
			logger.DebugCF("agent", "Reasoning publish skipped (timeout/cancel)", map[string]any{
				"channel": channelName,
				"error":   err.Error(),
			})
		} else {
			logger.WarnCF("agent", "Failed to publish reasoning (best-effort)", map[string]any{
				"channel": channelName,
				"error":   err.Error(),
			})
		}
	}
}

// maybeSummarize triggers summarization if the session history exceeds thresholds.
func (al *AgentLoop) maybeSummarize(agent *AgentInstance, sessionKey, channel, chatID string) {
	newHistory := agent.Sessions.GetHistory(sessionKey)
	tokenEstimate := al.estimateTokens(newHistory)
	threshold := agent.ContextWindow * 75 / 100

	if len(newHistory) > 100 || tokenEstimate > threshold {
		summarizeKey := agent.ID + ":" + sessionKey
		if _, loading := al.summarizing.LoadOrStore(summarizeKey, true); !loading {
			go func() {
				defer al.summarizing.Delete(summarizeKey)
				logger.Debug("Memory threshold reached. Optimizing conversation history...")
				ctx, cancel := al.safeCompactionContext()
				defer cancel()

				if agent != nil && agent.Compaction.NotifyUser && al.bus != nil && channel != "" && chatID != "" && !constants.IsInternalChannel(channel) {
					if err := al.bus.PublishOutbound(ctx, bus.OutboundMessage{
						Channel: channel,
						ChatID:  chatID,
						Content: "Memory threshold reached. Optimizing conversation history...",
					}); err != nil {
						logger.WarnCF("agent", "Failed to publish compaction notice (best-effort)", map[string]any{
							"channel": channel,
							"chat_id": chatID,
							"error":   err.Error(),
						})
					}
				}

				if flushed, err := al.maybeFlushMemoryBeforeCompaction(
					ctx,
					agent,
					sessionKey,
					tokenEstimate,
				); err != nil {
					logger.WarnCF("agent", "Background memory flush failed", map[string]any{
						"session_key": sessionKey,
						"error":       err.Error(),
					})
				} else if flushed {
					logger.InfoCF("agent", "Background memory flush completed", map[string]any{
						"session_key": sessionKey,
					})
				}

				if compacted, err := al.compactWithSafeguard(ctx, agent, sessionKey); err != nil {
					logger.WarnCF("agent", "Background compaction cancelled", map[string]any{
						"session_key": sessionKey,
						"error":       err.Error(),
					})
				} else if compacted {
					logger.InfoCF("agent", "Background compaction completed", map[string]any{
						"session_key": sessionKey,
					})
				}
			}()
		}
	}
}

// forceCompression aggressively reduces context when the limit is hit.
// It drops the oldest 50% of messages (keeping system prompt and last user message).
func (al *AgentLoop) forceCompression(agent *AgentInstance, sessionKey string) {
	history := agent.Sessions.GetHistory(sessionKey)
	if len(history) <= 4 {
		return
	}

	// Keep system prompt (usually [0]) and the very last message (user's trigger)
	// We want to drop the oldest half of the *conversation*
	// Assuming [0] is system, [1:] is conversation
	conversation := history[1 : len(history)-1]
	if len(conversation) == 0 {
		return
	}

	// Helper to find the mid-point of the conversation
	mid := len(conversation) / 2

	// New history structure:
	// 1. System Prompt (with compression note appended)
	// 2. Second half of conversation
	// 3. Last message

	droppedCount := mid
	keptConversation := conversation[mid:]

	newHistory := make([]providers.Message, 0, 1+len(keptConversation)+1)

	// Append compression note to the original system prompt instead of adding a new system message
	// This avoids having two consecutive system messages which some APIs (like Zhipu) reject
	compressionNote := fmt.Sprintf(
		"\n\n[System Note: Emergency compression dropped %d oldest messages due to context limit]",
		droppedCount,
	)
	enhancedSystemPrompt := history[0]
	enhancedSystemPrompt.Content = enhancedSystemPrompt.Content + compressionNote
	newHistory = append(newHistory, enhancedSystemPrompt)

	newHistory = append(newHistory, keptConversation...)
	newHistory = append(newHistory, history[len(history)-1]) // Last message

	// Update session
	agent.Sessions.SetHistory(sessionKey, newHistory)
	agent.Sessions.Save(sessionKey)

	logger.WarnCF("agent", "Forced compression executed", map[string]any{
		"session_key":  sessionKey,
		"dropped_msgs": droppedCount,
		"new_count":    len(newHistory),
	})
}

// GetStartupInfo returns information about loaded tools and skills for logging.
func (al *AgentLoop) GetStartupInfo() map[string]any {
	info := make(map[string]any)

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		return info
	}

	// Tools info
	toolsList := agent.Tools.List()
	info["tools"] = map[string]any{
		"count": len(toolsList),
		"names": toolsList,
	}

	// Skills info
	info["skills"] = agent.ContextBuilder.GetSkillsInfo()

	// Agents info
	info["agents"] = map[string]any{
		"count": len(al.registry.ListAgentIDs()),
		"ids":   al.registry.ListAgentIDs(),
	}

	return info
}

// formatMessagesForLog formats messages for logging
func formatMessagesForLog(messages []providers.Message) string {
	if len(messages) == 0 {
		return "[]"
	}

	var sb strings.Builder
	sb.WriteString("[\n")
	for i, msg := range messages {
		fmt.Fprintf(&sb, "  [%d] Role: %s\n", i, msg.Role)
		if len(msg.ToolCalls) > 0 {
			sb.WriteString("  ToolCalls:\n")
			for _, tc := range msg.ToolCalls {
				fmt.Fprintf(&sb, "    - ID: %s, Type: %s, Name: %s\n", tc.ID, tc.Type, tc.Name)
				if tc.Function != nil {
					fmt.Fprintf(
						&sb,
						"      Arguments: %s\n",
						utils.Truncate(tc.Function.Arguments, 200),
					)
				}
			}
		}
		if msg.Content != "" {
			content := utils.Truncate(msg.Content, 200)
			fmt.Fprintf(&sb, "  Content: %s\n", content)
		}
		if msg.ToolCallID != "" {
			fmt.Fprintf(&sb, "  ToolCallID: %s\n", msg.ToolCallID)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("]")
	return sb.String()
}

// formatToolsForLog formats tool definitions for logging
func formatToolsForLog(toolDefs []providers.ToolDefinition) string {
	if len(toolDefs) == 0 {
		return "[]"
	}

	var sb strings.Builder
	sb.WriteString("[\n")
	for i, tool := range toolDefs {
		fmt.Fprintf(&sb, "  [%d] Type: %s, Name: %s\n", i, tool.Type, tool.Function.Name)
		fmt.Fprintf(&sb, "      Description: %s\n", tool.Function.Description)
		if len(tool.Function.Parameters) > 0 {
			fmt.Fprintf(
				&sb,
				"      Parameters: %s\n",
				utils.Truncate(fmt.Sprintf("%v", tool.Function.Parameters), 200),
			)
		}
	}
	sb.WriteString("]")
	return sb.String()
}

// summarizeSession summarizes the conversation history for a session.
func (al *AgentLoop) summarizeSession(agent *AgentInstance, sessionKey string) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	history := agent.Sessions.GetHistory(sessionKey)
	summary := agent.Sessions.GetSummary(sessionKey)

	// Keep last 4 messages for continuity
	if len(history) <= 4 {
		return
	}

	toSummarize := history[:len(history)-4]

	// Oversized Message Guard
	maxMessageTokens := agent.ContextWindow / 2
	validMessages := make([]providers.Message, 0)
	omitted := false

	for _, m := range toSummarize {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		msgTokens := len(m.Content) / 2
		if msgTokens > maxMessageTokens {
			omitted = true
			continue
		}
		validMessages = append(validMessages, m)
	}

	if len(validMessages) == 0 {
		return
	}

	// Multi-Part Summarization
	var finalSummary string
	if len(validMessages) > 10 {
		mid := len(validMessages) / 2
		part1 := validMessages[:mid]
		part2 := validMessages[mid:]

		s1, _ := al.summarizeBatch(ctx, agent, part1, "")
		s2, _ := al.summarizeBatch(ctx, agent, part2, "")

		mergePrompt := fmt.Sprintf(
			"Merge these two conversation summaries into one cohesive summary:\n\n1: %s\n\n2: %s",
			s1,
			s2,
		)
		resp, err := agent.Provider.Chat(
			ctx,
			[]providers.Message{{Role: "user", Content: mergePrompt}},
			nil,
			agent.Model,
			map[string]any{
				"max_tokens":       1024,
				"temperature":      0.3,
				"prompt_cache_key": agent.ID,
			},
		)
		if err == nil {
			finalSummary = resp.Content
		} else {
			finalSummary = s1 + " " + s2
		}
	} else {
		finalSummary, _ = al.summarizeBatch(ctx, agent, validMessages, summary)
	}

	if omitted && finalSummary != "" {
		finalSummary += "\n[Note: Some oversized messages were omitted from this summary for efficiency.]"
	}

	if finalSummary != "" {
		agent.Sessions.SetSummary(sessionKey, finalSummary)
		agent.Sessions.TruncateHistory(sessionKey, 4)
		agent.Sessions.Save(sessionKey)
	}
}

// summarizeBatch summarizes a batch of messages.
func (al *AgentLoop) summarizeBatch(
	ctx context.Context,
	agent *AgentInstance,
	batch []providers.Message,
	existingSummary string,
) (string, error) {
	var sb strings.Builder
	sb.WriteString(
		"Provide a concise summary of this conversation segment, preserving core context and key points.\n",
	)
	if existingSummary != "" {
		sb.WriteString("Existing context: ")
		sb.WriteString(existingSummary)
		sb.WriteString("\n")
	}
	sb.WriteString("\nCONVERSATION:\n")
	for _, m := range batch {
		fmt.Fprintf(&sb, "%s: %s\n", m.Role, m.Content)
	}
	prompt := sb.String()

	response, err := agent.Provider.Chat(
		ctx,
		[]providers.Message{{Role: "user", Content: prompt}},
		nil,
		agent.Model,
		map[string]any{
			"max_tokens":       1024,
			"temperature":      0.3,
			"prompt_cache_key": agent.ID,
		},
	)
	if err != nil {
		return "", err
	}
	return response.Content, nil
}

// estimateTokens estimates the number of tokens in a message list.
// Delegates to the shared estimateTotalTokens helper in context.go.
func (al *AgentLoop) estimateTokens(messages []providers.Message) int {
	return estimateTotalTokens("", messages)
}

// estimateMessageTokens estimates the number of tokens in a single message.
// Kept as a thin wrapper for compaction helpers that operate message-by-message.
func (al *AgentLoop) estimateMessageTokens(message providers.Message) int {
	return estimateTotalTokens("", []providers.Message{message})
}

// ThinkingLevel controls how the provider sends thinking parameters.
//
//   - "adaptive": sends {thinking: {type: "adaptive"}} + output_config.effort (Claude 4.6+)
//   - "low"/"medium"/"high"/"xhigh": sends {thinking: {type: "enabled", budget_tokens: N}} (all models)
//   - "off": disables thinking
type ThinkingLevel string

const (
	ThinkingOff      ThinkingLevel = "off"
	ThinkingLow      ThinkingLevel = "low"
	ThinkingMedium   ThinkingLevel = "medium"
	ThinkingHigh     ThinkingLevel = "high"
	ThinkingXHigh    ThinkingLevel = "xhigh"
	ThinkingAdaptive ThinkingLevel = "adaptive"
)

// parseThinkingLevel normalizes a config string to a ThinkingLevel.
// Case-insensitive and whitespace-tolerant for user-facing config values.
// Returns ThinkingOff for unknown or empty values.
func parseThinkingLevel(level string) ThinkingLevel {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "adaptive":
		return ThinkingAdaptive
	case "low":
		return ThinkingLow
	case "medium":
		return ThinkingMedium
	case "high":
		return ThinkingHigh
	case "xhigh":
		return ThinkingXHigh
	default:
		return ThinkingOff
	}
}

// Consolidated from compaction.go

func (al *AgentLoop) maybeFlushMemoryBeforeCompaction(
	ctx context.Context,
	agent *AgentInstance,
	sessionKey string,
	tokenEstimate int,
) (bool, error) {
	if !isCompactionModeEnabled(agent.Compaction.Mode) {
		return false, nil
	}
	if !agent.Compaction.MemoryFlushEnabled || sessionKey == "" {
		return false, nil
	}

	triggerPoint := agent.ContextWindow - agent.Compaction.ReserveTokens - agent.Compaction.MemoryFlushSoftThreshold
	if triggerPoint < agent.ContextWindow/3 {
		triggerPoint = agent.ContextWindow / 3
	}
	if tokenEstimate < triggerPoint {
		return false, nil
	}

	compactionCount, flushedAtCount, _ := agent.Sessions.GetCompactionState(sessionKey)
	if flushedAtCount == compactionCount {
		return false, nil
	}

	if err := al.flushMemorySnapshot(ctx, agent, sessionKey); err != nil {
		return false, err
	}

	agent.Sessions.MarkMemoryFlush(sessionKey, compactionCount)
	_ = agent.Sessions.Save(sessionKey)
	return true, nil
}

func (al *AgentLoop) flushMemorySnapshot(ctx context.Context, agent *AgentInstance, sessionKey string) error {
	history := agent.Sessions.GetHistory(sessionKey)
	if len(history) == 0 {
		return nil
	}

	recent := make([]providers.Message, 0, 12)
	for i := len(history) - 1; i >= 0 && len(recent) < 12; i-- {
		msg := history[i]
		if (msg.Role != "user" && msg.Role != "assistant") || strings.TrimSpace(msg.Content) == "" {
			continue
		}
		recent = append([]providers.Message{msg}, recent...)
	}
	if len(recent) == 0 {
		return nil
	}

	var prompt strings.Builder
	prompt.WriteString("Extract durable memory from this chat — facts worth remembering long-term.\n")
	prompt.WriteString("Return concise markdown bullets under these headings only:\n")
	prompt.WriteString("## Profile\n## Long-term Facts\n## Active Goals\n## Constraints\n## Open Threads\n## Deprecated/Resolved\n\n")
	prompt.WriteString("Rules:\n")
	prompt.WriteString("- Only extract information that would be useful in FUTURE conversations.\n")
	prompt.WriteString("- Skip transient details (greetings, acknowledgements, single-use commands).\n")
	prompt.WriteString("- Each bullet should be self-contained and understandable without context.\n")
	prompt.WriteString("- Omit sections with nothing to report.\n")
	prompt.WriteString("\nCHAT:\n")
	for _, m := range recent {
		prompt.WriteString(m.Role)
		prompt.WriteString(": ")
		prompt.WriteString(m.Content)
		prompt.WriteString("\n")
	}

	resp, err := agent.Provider.Chat(
		ctx,
		[]providers.Message{{Role: "user", Content: prompt.String()}},
		nil,
		agent.Model,
		map[string]any{
			"max_tokens":  700,
			"temperature": 0.2,
		},
	)
	if err != nil {
		return err
	}
	if strings.TrimSpace(resp.Content) == "" {
		return fmt.Errorf("empty memory flush response")
	}

	memory := (*MemoryStore)(nil)
	if agent.ContextBuilder != nil {
		memory = agent.ContextBuilder.MemoryForSession(sessionKey, "", "")
	}
	if memory == nil {
		memory = NewMemoryStore(agent.Workspace)
	}
	return memory.OrganizeWriteback(resp.Content)
}

func (al *AgentLoop) compactWithSafeguard(
	ctx context.Context,
	agent *AgentInstance,
	sessionKey string,
) (bool, error) {
	switch normalizeCompactionMode(agent.Compaction.Mode) {
	case "off":
		return false, nil
	case "legacy":
		beforeHistory := len(agent.Sessions.GetHistory(sessionKey))
		beforeSummary := strings.TrimSpace(agent.Sessions.GetSummary(sessionKey))
		al.summarizeSession(agent, sessionKey)
		afterHistory := len(agent.Sessions.GetHistory(sessionKey))
		afterSummary := strings.TrimSpace(agent.Sessions.GetSummary(sessionKey))
		if afterHistory < beforeHistory || afterSummary != beforeSummary {
			agent.Sessions.IncrementCompactionCount(sessionKey)
			_ = agent.Sessions.Save(sessionKey)
			return true, nil
		}
		return false, nil
	}

	history := sanitizeHistoryForProvider(agent.Sessions.GetHistory(sessionKey))
	if len(history) <= 6 {
		return false, nil
	}

	historyTokens := al.estimateTokens(history)
	historyBudget := int(float64(agent.ContextWindow) * agent.Compaction.MaxHistoryShare)
	if historyBudget <= 0 {
		historyBudget = agent.ContextWindow / 2
	}
	if historyTokens <= historyBudget && len(history) < 24 {
		return false, nil
	}

	keepRecentTokens := agent.Compaction.KeepRecentTokens
	if keepRecentTokens <= 0 {
		keepRecentTokens = maxInt(1024, agent.ContextWindow/4)
	}

	keepStart := len(history) - 4
	if keepStart < 1 {
		keepStart = 1
	}
	acc := 0
	for i := len(history) - 1; i >= 1; i-- {
		acc += al.estimateMessageTokens(history[i])
		if acc >= keepRecentTokens {
			keepStart = i
			break
		}
	}
	if keepStart <= 0 || keepStart >= len(history) {
		return false, nil
	}

	toSummarize := history[:keepStart]
	kept := history[keepStart:]
	if len(toSummarize) == 0 || len(kept) == 0 {
		return false, nil
	}

	existingSummary := agent.Sessions.GetSummary(sessionKey)
	summary, err := al.generateCompactionSummary(ctx, agent, toSummarize, existingSummary)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(summary) == "" {
		return false, fmt.Errorf("compaction summary unavailable")
	}

	summary = strings.TrimSpace(summary) + "\n\n[Post-compaction refresh: Re-check AGENTS.md and MEMORY.md before continuing.]"
	agent.Sessions.SetSummary(sessionKey, summary)
	agent.Sessions.SetHistory(sessionKey, kept)
	agent.Sessions.IncrementCompactionCount(sessionKey)
	_ = agent.Sessions.Save(sessionKey)

	logger.InfoCF("agent", "Compaction safeguard completed", map[string]any{
		"session_key":           sessionKey,
		"history_tokens_before": historyTokens,
		"kept_messages":         len(kept),
		"summarized_messages":   len(toSummarize),
	})
	return true, nil
}

func (al *AgentLoop) generateCompactionSummary(
	ctx context.Context,
	agent *AgentInstance,
	history []providers.Message,
	existingSummary string,
) (string, error) {
	safeMessages := make([]providers.Message, 0, len(history))
	for _, msg := range history {
		if msg.Role != "user" && msg.Role != "assistant" && msg.Role != "tool" {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		if msg.Role == "tool" && len(content) > 1200 {
			const maxLen = 1100
			const tailMin = 300
			content = utils.TruncateHeadTailWithMarker(content, maxLen, "\n...\n[tool result condensed]\n...\n", tailMin)
		}
		msg.Content = content
		safeMessages = append(safeMessages, msg)
	}
	if len(safeMessages) == 0 {
		return strings.TrimSpace(existingSummary), nil
	}

	maxChunkTokens := int(float64(agent.ContextWindow)*0.35) - 1024
	if maxChunkTokens < 512 {
		maxChunkTokens = 512
	}

	chunks := al.splitMessagesByTokenBudget(safeMessages, maxChunkTokens)
	summary := strings.TrimSpace(existingSummary)
	for _, chunk := range chunks {
		next, err := al.summarizeBatchStructured(ctx, agent, chunk, summary)
		if err != nil {
			return "", err
		}
		summary = strings.TrimSpace(next)
	}
	return summary, nil
}

func (al *AgentLoop) summarizeBatchStructured(
	ctx context.Context,
	agent *AgentInstance,
	batch []providers.Message,
	existingSummary string,
) (string, error) {
	var sb strings.Builder
	sb.WriteString("Summarize this conversation segment for future continuity.\n")
	sb.WriteString("Output EXACTLY in this format (keep each section to 1-3 bullet points):\n\n")
	sb.WriteString("## Intent\n- <what the user wants to achieve>\n\n")
	sb.WriteString("## Decisions\n- <key decisions made during the conversation>\n\n")
	sb.WriteString("## Tool Results\n- <important tool outputs with key data points>\n\n")
	sb.WriteString("## Pending Actions\n- <what still needs to be done>\n\n")
	sb.WriteString("## Constraints\n- <any limitations or requirements discovered>\n\n")
	sb.WriteString("Keep total output under 300 words. Prioritize actionable information over conversational details.\n")
	sb.WriteString("Omit empty sections. Do NOT include greetings, pleasantries, or meta-commentary.\n")
	if existingSummary != "" {
		sb.WriteString("\nExisting summary:\n")
		sb.WriteString(existingSummary)
		sb.WriteString("\n")
	}
	sb.WriteString("\nConversation:\n")
	for _, m := range batch {
		sb.WriteString(m.Role)
		sb.WriteString(": ")
		sb.WriteString(m.Content)
		sb.WriteString("\n")
	}

	resp, err := agent.Provider.Chat(
		ctx,
		[]providers.Message{{Role: "user", Content: sb.String()}},
		nil,
		agent.Model,
		map[string]any{
			"max_tokens":  800,
			"temperature": 0.2,
		},
	)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

func (al *AgentLoop) splitMessagesByTokenBudget(
	messages []providers.Message,
	maxTokens int,
) [][]providers.Message {
	if len(messages) == 0 || maxTokens <= 0 {
		return nil
	}
	chunks := make([][]providers.Message, 0, 4)
	current := make([]providers.Message, 0, 8)
	currentTokens := 0
	for _, msg := range messages {
		msgTokens := al.estimateMessageTokens(msg)
		if len(current) > 0 && currentTokens+msgTokens > maxTokens {
			chunks = append(chunks, current)
			current = make([]providers.Message, 0, 8)
			currentTokens = 0
		}
		current = append(current, msg)
		currentTokens += msgTokens
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (al *AgentLoop) safeCompactionContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 90*time.Second)
}

func normalizeCompactionMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "safeguard":
		return "safeguard"
	case "off", "none", "disabled":
		return "off"
	case "legacy":
		return "legacy"
	default:
		return "safeguard"
	}
}

func isCompactionModeEnabled(mode string) bool {
	return normalizeCompactionMode(mode) != "off"
}

// Consolidated from run_trace.go

type runTraceWriter struct {
	enabled bool
	scope   string

	runID      string
	sessionKey string
	channel    string
	chatID     string
	senderID   string
	agentID    string
	model      string

	dir        string
	eventsPath string

	maxPreviewChars int

	mu sync.Mutex
}

type runTraceEvent struct {
	Type events.Type `json:"type"`

	TS   string `json:"ts"`
	TSMS int64  `json:"ts_ms"`

	RunID      string `json:"run_id"`
	SessionKey string `json:"session_key,omitempty"`
	Channel    string `json:"channel,omitempty"`
	ChatID     string `json:"chat_id,omitempty"`
	SenderID   string `json:"sender_id,omitempty"`

	AgentID string `json:"agent_id,omitempty"`
	Model   string `json:"model,omitempty"`

	Iteration int `json:"iteration,omitempty"`

	UserMessagePreview string `json:"user_message_preview,omitempty"`
	UserMessageChars   int    `json:"user_message_chars,omitempty"`

	MessagesCount int `json:"messages_count,omitempty"`
	ToolsCount    int `json:"tools_count,omitempty"`

	ResponsePreview string   `json:"response_preview,omitempty"`
	ToolCalls       []string `json:"tool_calls,omitempty"`

	ToolBatch []runTraceToolExec `json:"tool_batch,omitempty"`

	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`

	Error string `json:"error,omitempty"`
}

type runTraceToolExec struct {
	Tool       string `json:"tool"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	Preview    string `json:"preview,omitempty"`
}

func newRunTraceWriter(workspace string, enabled bool, opts processOptions, agentID, model string) *runTraceWriter {
	if !enabled {
		return nil
	}

	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil
	}

	sessionKey := utils.CanonicalSessionKey(opts.SessionKey)
	if sessionKey == "" {
		// Should not happen in normal agent loop, but keep best-effort.
		sessionKey = utils.CanonicalSessionKey(strings.TrimSpace(opts.Channel) + ":" + strings.TrimSpace(opts.ChatID))
	}
	dirKey := tools.SafePathToken(sessionKey)
	if dirKey == "" {
		dirKey = "unknown"
	}

	dir := filepath.Join(workspace, ".x-claw", "audit", "runs", dirKey)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.WarnCF("agent", "Run trace disabled: failed to create directory", map[string]any{
			"dir": dir,
			"err": err.Error(),
		})
		return nil
	}

	runID := strings.TrimSpace(opts.RunID)
	if runID == "" {
		runID = uuid.NewString()
	}

	return &runTraceWriter{
		enabled: true,
		scope:   "agent",

		runID:      runID,
		sessionKey: sessionKey,
		channel:    strings.TrimSpace(opts.Channel),
		chatID:     strings.TrimSpace(opts.ChatID),
		senderID:   strings.TrimSpace(opts.SenderID),
		agentID:    strings.TrimSpace(agentID),
		model:      strings.TrimSpace(model),

		dir:        dir,
		eventsPath: filepath.Join(dir, "events.jsonl"),

		maxPreviewChars: 400,
	}
}

func (w *runTraceWriter) RunID() string {
	if w == nil {
		return ""
	}
	return w.runID
}

func (w *runTraceWriter) recordStart(userMessage string, messagesCount, toolsCount int) {
	if w == nil || !w.enabled {
		return
	}
	ts := time.Now()
	w.appendEvent(runTraceEvent{
		Type: events.RunStart,

		TS:   ts.UTC().Format(time.RFC3339Nano),
		TSMS: ts.UnixMilli(),

		RunID:      w.runID,
		SessionKey: w.sessionKey,
		Channel:    w.channel,
		ChatID:     w.chatID,
		SenderID:   w.senderID,

		AgentID: w.agentID,
		Model:   w.model,

		UserMessagePreview: utils.Truncate(strings.TrimSpace(userMessage), w.maxPreviewChars),
		UserMessageChars:   len(userMessage),
		MessagesCount:      messagesCount,
		ToolsCount:         toolsCount,
	})
}

func (w *runTraceWriter) recordResume(userMessage string, messagesCount, toolsCount int) {
	if w == nil || !w.enabled {
		return
	}
	ts := time.Now()
	w.appendEvent(runTraceEvent{
		Type: events.RunResume,

		TS:   ts.UTC().Format(time.RFC3339Nano),
		TSMS: ts.UnixMilli(),

		RunID:      w.runID,
		SessionKey: w.sessionKey,
		Channel:    w.channel,
		ChatID:     w.chatID,
		SenderID:   w.senderID,

		AgentID: w.agentID,
		Model:   w.model,

		UserMessagePreview: utils.Truncate(strings.TrimSpace(userMessage), w.maxPreviewChars),
		UserMessageChars:   len(userMessage),
		MessagesCount:      messagesCount,
		ToolsCount:         toolsCount,
	})
}

func (w *runTraceWriter) recordLLMRequest(iteration int, messagesCount, toolsCount int) {
	if w == nil || !w.enabled {
		return
	}
	ts := time.Now()
	w.appendEvent(runTraceEvent{
		Type: events.LLMRequest,

		TS:   ts.UTC().Format(time.RFC3339Nano),
		TSMS: ts.UnixMilli(),

		RunID:      w.runID,
		SessionKey: w.sessionKey,
		Channel:    w.channel,
		ChatID:     w.chatID,
		SenderID:   w.senderID,

		AgentID: w.agentID,
		Model:   w.model,

		Iteration:     iteration,
		MessagesCount: messagesCount,
		ToolsCount:    toolsCount,
	})
}

func (w *runTraceWriter) recordLLMResponse(iteration int, content string, toolCalls []string, usage *providers.UsageInfo) {
	if w == nil || !w.enabled {
		return
	}
	ts := time.Now()

	event := runTraceEvent{
		Type: events.LLMResponse,

		TS:   ts.UTC().Format(time.RFC3339Nano),
		TSMS: ts.UnixMilli(),

		RunID:      w.runID,
		SessionKey: w.sessionKey,
		Channel:    w.channel,
		ChatID:     w.chatID,
		SenderID:   w.senderID,

		AgentID: w.agentID,
		Model:   w.model,

		Iteration:       iteration,
		ResponsePreview: utils.Truncate(strings.TrimSpace(content), w.maxPreviewChars),
		ToolCalls:       toolCalls,
	}
	if usage != nil {
		event.PromptTokens = usage.PromptTokens
		event.CompletionTokens = usage.CompletionTokens
		event.TotalTokens = usage.TotalTokens
	}

	w.appendEvent(event)
}

func (w *runTraceWriter) recordToolBatch(iteration int, execs []tools.ToolCallExecution) {
	if w == nil || !w.enabled || len(execs) == 0 {
		return
	}
	ts := time.Now()

	batch := make([]runTraceToolExec, 0, len(execs))
	for _, ex := range execs {
		preview := ""
		if ex.Result != nil {
			preview = ex.Result.ForLLM
			if preview == "" {
				preview = ex.Result.ForUser
			}
			if preview == "" && ex.Result.Err != nil {
				preview = ex.Result.Err.Error()
			}
		}
		batch = append(batch, runTraceToolExec{
			Tool:       ex.ToolCall.Name,
			ToolCallID: ex.ToolCall.ID,
			DurationMS: ex.DurationMS,
			IsError:    ex.Result != nil && ex.Result.IsError,
			Preview:    utils.Truncate(strings.TrimSpace(preview), w.maxPreviewChars),
		})
	}

	w.appendEvent(runTraceEvent{
		Type: events.ToolBatch,

		TS:   ts.UTC().Format(time.RFC3339Nano),
		TSMS: ts.UnixMilli(),

		RunID:      w.runID,
		SessionKey: w.sessionKey,
		Channel:    w.channel,
		ChatID:     w.chatID,
		SenderID:   w.senderID,

		AgentID: w.agentID,
		Model:   w.model,

		Iteration: iteration,
		ToolBatch: batch,
	})
}

func (w *runTraceWriter) recordEnd(iterations int, finalContent string) {
	if w == nil || !w.enabled {
		return
	}
	ts := time.Now()
	w.appendEvent(runTraceEvent{
		Type: events.RunEnd,

		TS:   ts.UTC().Format(time.RFC3339Nano),
		TSMS: ts.UnixMilli(),

		RunID:      w.runID,
		SessionKey: w.sessionKey,
		Channel:    w.channel,
		ChatID:     w.chatID,
		SenderID:   w.senderID,

		AgentID: w.agentID,
		Model:   w.model,

		Iteration:       iterations,
		ResponsePreview: utils.Truncate(strings.TrimSpace(finalContent), w.maxPreviewChars),
	})
}

func (w *runTraceWriter) recordError(iteration int, err error) {
	if w == nil || !w.enabled || err == nil {
		return
	}
	ts := time.Now()
	w.appendEvent(runTraceEvent{
		Type: events.RunError,

		TS:   ts.UTC().Format(time.RFC3339Nano),
		TSMS: ts.UnixMilli(),

		RunID:      w.runID,
		SessionKey: w.sessionKey,
		Channel:    w.channel,
		ChatID:     w.chatID,
		SenderID:   w.senderID,

		AgentID: w.agentID,
		Model:   w.model,

		Iteration: iteration,
		Error:     utils.Truncate(err.Error(), 1200),
	})
}

func (w *runTraceWriter) appendEvent(event runTraceEvent) {
	if w == nil || !w.enabled {
		return
	}

	payload, err := json.Marshal(event)
	if err != nil {
		logger.WarnCF(w.scope, "Run trace: failed to marshal event", map[string]any{
			"err": err.Error(),
		})
		return
	}
	payload = append(payload, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()

	f, err := os.OpenFile(w.eventsPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		logger.WarnCF(w.scope, "Run trace: failed to open events file", map[string]any{
			"path": w.eventsPath,
			"err":  err.Error(),
		})
		return
	}
	defer f.Close()

	if _, err := f.Write(payload); err != nil {
		logger.WarnCF(w.scope, "Run trace: failed to append event", map[string]any{
			"path": w.eventsPath,
			"err":  err.Error(),
		})
		return
	}
	_ = f.Sync()
}

func (w *runTraceWriter) String() string {
	if w == nil {
		return ""
	}
	return fmt.Sprintf("runTrace(run_id=%s, path=%s)", w.runID, w.eventsPath)
}
