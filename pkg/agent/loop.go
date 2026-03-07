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
	"github.com/h2non/filetype"
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
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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

var fallbackProviderFactory = func(al *AgentLoop, candidate providers.FallbackCandidate) (providers.LLMProvider, string, error) {
	if al == nil {
		return nil, "", fmt.Errorf("agent loop is nil")
	}
	return al.createProviderForFallbackCandidate(candidate)
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
	sharedSessions := session.NewSessionManagerWithConfig(sessionsPath, cfg.Session)
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
		sessionKey := al.resolveInboundSessionKey(msg, al.buildConversationSessionKey(msg, al.Config()))

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

		if err := al.bus.PublishOutbound(msgCtx, bus.OutboundMessage{
			Channel:    msg.Channel,
			ChatID:     msg.ChatID,
			Content:    response,
			SessionKey: sessionKey,
		}); err != nil {
			logger.DebugCF("agent", "failed to publish outbound response", map[string]any{"channel": msg.Channel, "chat_id": msg.ChatID, "error": err.Error()})
			return
		}
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

// RecordLastSessionKey records the last active external conversation session key.
func (al *AgentLoop) RecordLastSessionKey(sessionKey string) error {
	if al.state == nil {
		return nil
	}
	return al.state.SetLastSessionKey(utils.CanonicalSessionKey(sessionKey))
}

// LastActive returns the most recently used channel and chat ID for this workspace.
// It is backed by the persisted state file (state/state.json), but uses the in-memory
// state manager instance so it stays up-to-date during a running gateway process.
func (al *AgentLoop) LastActive() (string, string) {
	_, channel, chatID := al.LastActiveContext()
	return channel, chatID
}

// LastActiveContext returns the most recently used external session key, channel, and chat ID.
func (al *AgentLoop) LastActiveContext() (string, string, string) {
	if al == nil || al.state == nil {
		return "", "", ""
	}
	key := strings.TrimSpace(al.state.GetLastChannel())
	if key == "" {
		return "", "", ""
	}

	parts := strings.SplitN(key, ":", 2)
	if len(parts) != 2 {
		return "", "", ""
	}
	channel := strings.TrimSpace(parts[0])
	chatID := strings.TrimSpace(parts[1])
	if channel == "" || chatID == "" {
		return "", "", ""
	}
	return utils.CanonicalSessionKey(al.state.GetLastSessionKey()), channel, chatID
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

type toolCallSignature struct {
	Name              string
	Args              string
	ResultFingerprint string
}

type llmCallResult struct {
	response         *providers.LLMResponse
	usedModel        string
	fallbackAttempts []providers.FallbackAttempt
}

type llmIterationRunner struct {
	loop *AgentLoop
	ctx  context.Context

	agent    *AgentInstance
	messages []providers.Message
	opts     processOptions
	trace    *runTraceWriter

	modelForRun string

	iteration             int
	finalContent          string
	recentToolCalls       []toolCallSignature
	totalPromptTokens     int
	totalCompletionTokens int
	runStart              time.Time
	toolCallsUsed         int

	cfg                *config.Config
	maxWallTimeSeconds int
	maxToolCallsPerRun int
	maxToolResultChars int
}

func detectToolCallLoop(recent []toolCallSignature, current []providers.ToolCall, threshold int) string {
	for _, tc := range current {
		argsJSON, _ := json.Marshal(tc.Arguments)
		sig := string(argsJSON)
		count := 0
		lastResult := ""
		for i := len(recent) - 1; i >= 0; i-- {
			prev := recent[i]
			if prev.Name != tc.Name || prev.Args != sig {
				break
			}
			if lastResult == "" {
				lastResult = prev.ResultFingerprint
			}
			if prev.ResultFingerprint != lastResult {
				break
			}
			count++
		}
		if count >= threshold {
			return tc.Name
		}
	}
	return ""
}

func (al *AgentLoop) runLLMIteration(
	ctx context.Context,
	agent *AgentInstance,
	messages []providers.Message,
	opts processOptions,
	trace *runTraceWriter,
	modelForRun string,
) (string, int, *AgentInstance, error) {
	runner := newLLMIterationRunner(al, ctx, agent, messages, opts, trace, modelForRun)
	return runner.run()
}

func newLLMIterationRunner(
	loop *AgentLoop,
	ctx context.Context,
	agent *AgentInstance,
	messages []providers.Message,
	opts processOptions,
	trace *runTraceWriter,
	modelForRun string,
) *llmIterationRunner {
	cfg := loop.Config()
	modelForRun = strings.TrimSpace(modelForRun)
	if modelForRun == "" {
		modelForRun = strings.TrimSpace(agent.Model)
	}
	runner := &llmIterationRunner{
		loop:            loop,
		ctx:             ctx,
		agent:           agent,
		messages:        messages,
		opts:            opts,
		trace:           trace,
		modelForRun:     modelForRun,
		recentToolCalls: make([]toolCallSignature, 0, 32),
		runStart:        time.Now(),
		cfg:             cfg,
	}
	if cfg != nil && cfg.Limits.Enabled {
		runner.maxWallTimeSeconds = cfg.Limits.MaxRunWallTimeSeconds
		runner.maxToolCallsPerRun = cfg.Limits.MaxToolCallsPerRun
		runner.maxToolResultChars = cfg.Limits.MaxToolResultChars
	}
	return runner
}

func (r *llmIterationRunner) run() (string, int, *AgentInstance, error) {
	for r.iteration < r.agent.MaxIterations {
		r.iteration++
		r.logIterationStart()
		if r.enforceWallTimeBudget() {
			break
		}

		providerToolDefs := r.agent.Tools.ToProviderDefs()
		r.recordLLMRequest(providerToolDefs)
		call, err := r.callLLMWithRetry(providerToolDefs)
		if err != nil {
			logger.ErrorCF("agent", "LLM call failed",
				map[string]any{
					"agent_id":  r.agent.ID,
					"iteration": r.iteration,
					"error":     err.Error(),
				})
			return "", r.iteration, r.agent, fmt.Errorf("LLM call failed after retries: %w", err)
		}

		if r.afterLLMCall(call) {
			continue
		}
		if r.handleLLMResponse(call.response) {
			break
		}
	}

	r.logTokenSummary()
	return r.finalContent, r.iteration, r.agent, nil
}

func (r *llmIterationRunner) logIterationStart() {
	logger.DebugCF("agent", "LLM iteration",
		map[string]any{
			"agent_id":  r.agent.ID,
			"iteration": r.iteration,
			"max":       r.agent.MaxIterations,
		})
}

func (r *llmIterationRunner) enforceWallTimeBudget() bool {
	if r.maxWallTimeSeconds <= 0 || time.Since(r.runStart) <= time.Duration(r.maxWallTimeSeconds)*time.Second {
		return false
	}
	r.finalContent = fmt.Sprintf(
		"RESOURCE_BUDGET_EXCEEDED: run wall time exceeded (%ds). Please narrow the task or split it into smaller steps.",
		r.maxWallTimeSeconds,
	)
	logger.WarnCF("agent", "Resource budget exceeded (wall time)", map[string]any{
		"agent_id":          r.agent.ID,
		"iteration":         r.iteration,
		"wall_time_seconds": int(time.Since(r.runStart).Seconds()),
		"tool_calls_used":   r.toolCallsUsed,
		"session_key":       r.opts.SessionKey,
	})
	return true
}

func (r *llmIterationRunner) recordLLMRequest(providerToolDefs []providers.ToolDefinition) {
	if r.trace != nil {
		r.trace.recordLLMRequest(r.iteration, len(r.messages), len(providerToolDefs))
	}
	logger.DebugCF("agent", "LLM request",
		map[string]any{
			"agent_id":          r.agent.ID,
			"iteration":         r.iteration,
			"model":             r.modelForRun,
			"messages_count":    len(r.messages),
			"tools_count":       len(providerToolDefs),
			"max_tokens":        r.agent.MaxTokens,
			"temperature":       r.agent.Temperature,
			"system_prompt_len": len(r.messages[0].Content),
		})
	logger.DebugCF("agent", "Full LLM request",
		map[string]any{
			"iteration":     r.iteration,
			"messages_json": formatMessagesForLog(r.messages),
			"tools_json":    formatToolsForLog(providerToolDefs),
		})
}

func (r *llmIterationRunner) callLLMWithRetry(providerToolDefs []providers.ToolDefinition) (*llmCallResult, error) {
	maxRetries := 2
	for retry := 0; retry <= maxRetries; retry++ {
		call, err := r.performLLMCall(providerToolDefs)
		if err == nil {
			return call, nil
		}
		if isLLMTimeoutError(err) && retry < maxRetries {
			backoff := time.Duration(retry+1) * 5 * time.Second
			logger.WarnCF("agent", "Timeout error, retrying after backoff", map[string]any{
				"error":   err.Error(),
				"retry":   retry,
				"backoff": backoff.String(),
			})
			time.Sleep(backoff)
			continue
		}
		if isContextWindowError(err) && retry < maxRetries {
			if r.handleContextWindowRetry(retry, err) {
				continue
			}
		}
		return nil, err
	}
	return nil, fmt.Errorf("unreachable LLM retry state")
}

func (r *llmIterationRunner) performLLMCall(providerToolDefs []providers.ToolDefinition) (*llmCallResult, error) {
	llmOpts := r.buildLLMOptions()
	lastFallbackAttempts := []providers.FallbackAttempt(nil)
	callLLM := func() (*providers.LLMResponse, string, error) {
		lastFallbackAttempts = nil
		if strings.TrimSpace(r.agent.Model) != "" && r.modelForRun != strings.TrimSpace(r.agent.Model) {
			resp, err := r.agent.Provider.Chat(r.ctx, r.messages, providerToolDefs, r.modelForRun, llmOpts)
			return resp, r.modelForRun, err
		}
		if len(r.agent.Candidates) > 1 && r.loop.fallback != nil {
			primaryCandidate := providers.FallbackCandidate{}
			if len(r.agent.Candidates) > 0 {
				primaryCandidate = r.agent.Candidates[0]
			}
			fbResult, fbErr := r.loop.fallback.Execute(
				r.ctx,
				r.agent.Candidates,
				func(ctx context.Context, provider, model string) (*providers.LLMResponse, error) {
					if providers.ModelKey(provider, model) == providers.ModelKey(primaryCandidate.Provider, primaryCandidate.Model) {
						return r.agent.Provider.Chat(ctx, r.messages, providerToolDefs, model, llmOpts)
					}
					providerInstance, resolvedModel, err := fallbackProviderFactory(r.loop, providers.FallbackCandidate{
						Provider: provider,
						Model:    model,
					})
					if err != nil {
						return nil, err
					}
					if providerInstance == nil {
						return nil, fmt.Errorf("fallback provider is nil for %s/%s", provider, model)
					}
					if closable, ok := providerInstance.(providers.StatefulProvider); ok && providerInstance != r.agent.Provider {
						defer closable.Close()
					}
					return providerInstance.Chat(ctx, r.messages, providerToolDefs, resolvedModel, llmOpts)
				},
			)
			if fbErr != nil {
				return nil, "", fbErr
			}
			if fbResult.Provider != "" && len(fbResult.Attempts) > 0 {
				logger.InfoCF(
					"agent",
					fmt.Sprintf("Fallback: succeeded with %s/%s after %d attempts", fbResult.Provider, fbResult.Model, len(fbResult.Attempts)+1),
					map[string]any{"agent_id": r.agent.ID, "iteration": r.iteration},
				)
			}
			lastFallbackAttempts = fbResult.Attempts
			return fbResult.Response, strings.TrimSpace(fbResult.Model), nil
		}
		resp, err := r.agent.Provider.Chat(r.ctx, r.messages, providerToolDefs, r.modelForRun, llmOpts)
		return resp, r.modelForRun, err
	}

	response, usedModel, err := callLLM()
	if err != nil {
		return nil, err
	}
	return &llmCallResult{
		response:         response,
		usedModel:        usedModel,
		fallbackAttempts: lastFallbackAttempts,
	}, nil
}

func (r *llmIterationRunner) buildLLMOptions() map[string]any {
	llmOpts := map[string]any{
		"max_tokens":       r.agent.MaxTokens,
		"temperature":      r.agent.Temperature,
		"prompt_cache_key": r.agent.ID,
	}
	if r.agent.ThinkingLevel != "" && r.agent.ThinkingLevel != ThinkingOff {
		if tc, ok := r.agent.Provider.(providers.ThinkingCapable); ok && tc.SupportsThinking() {
			llmOpts["thinking_level"] = string(r.agent.ThinkingLevel)
		} else {
			logger.WarnCF(
				"agent",
				"thinking_level is set but current provider does not support it, ignoring",
				map[string]any{"agent_id": r.agent.ID, "thinking_level": string(r.agent.ThinkingLevel)},
			)
		}
	}
	return llmOpts
}

func (r *llmIterationRunner) handleContextWindowRetry(retry int, err error) bool {
	logger.WarnCF("agent", "Context window error detected, attempting compression", map[string]any{
		"error": err.Error(),
		"retry": retry,
	})

	if r.cfg != nil {
		target := pickFirstDifferentModel(r.modelForRun, r.agent.Candidates)
		if target != "" {
			if r.loop.maybeAutoDowngradeSessionModel(
				r.agent.Workspace,
				r.trace,
				r.agent.ID,
				r.opts.SessionKey,
				r.runID(),
				r.opts.Channel,
				r.opts.ChatID,
				r.opts.SenderID,
				r.iteration,
				r.modelForRun,
				target,
				"context_window",
				nil,
			) {
				r.modelForRun = target
			}
		}
	}

	if retry == 0 && !constants.IsInternalChannel(r.opts.Channel) {
		r.loop.bus.PublishOutbound(r.ctx, bus.OutboundMessage{
			Channel: r.opts.Channel,
			ChatID:  r.opts.ChatID,
			Content: "Context window exceeded. Compressing history and retrying...",
		})
	}

	compactionCtx, cancel := r.loop.safeCompactionContext()
	currentTokens := r.loop.estimateTokens(r.agent.Sessions.GetHistory(r.opts.SessionKey))
	if flushed, flushErr := r.loop.maybeFlushMemoryBeforeCompaction(
		compactionCtx,
		r.agent,
		r.opts.SessionKey,
		currentTokens,
	); flushErr != nil {
		logger.WarnCF("agent", "Pre-compaction memory flush failed", map[string]any{"error": flushErr.Error()})
	} else if flushed {
		logger.InfoCF("agent", "Pre-compaction memory flush completed", map[string]any{"session_key": r.opts.SessionKey})
	}

	compacted, compactErr := r.loop.compactWithSafeguard(compactionCtx, r.agent, r.opts.SessionKey)
	cancel()
	if compactErr != nil {
		logger.WarnCF("agent", "Compaction safeguard cancelled", map[string]any{"error": compactErr.Error()})
		return false
	}
	if !compacted {
		logger.WarnCF("agent", "Compaction safeguard skipped; preserving history", map[string]any{"session_key": r.opts.SessionKey})
		return true
	}

	newHistory := r.agent.Sessions.GetHistory(r.opts.SessionKey)
	newSummary := r.agent.Sessions.GetSummary(r.opts.SessionKey)
	r.messages = r.agent.ContextBuilder.BuildMessagesForSession(
		r.opts.SessionKey,
		newHistory,
		newSummary,
		"",
		nil,
		r.opts.Channel,
		r.opts.ChatID,
		r.opts.WorkingState,
	)
	return true
}

func (r *llmIterationRunner) afterLLMCall(call *llmCallResult) bool {
	usedModel := strings.TrimSpace(call.usedModel)
	if usedModel == "" {
		usedModel = r.modelForRun
	}
	if len(call.fallbackAttempts) == 0 && strings.EqualFold(usedModel, strings.TrimSpace(r.modelForRun)) {
		r.loop.clearModelAutoDowngradeState(r.opts.SessionKey)
	}
	if len(call.fallbackAttempts) > 0 && usedModel != "" && !strings.EqualFold(usedModel, strings.TrimSpace(r.modelForRun)) {
		if r.loop.maybeAutoDowngradeSessionModel(
			r.agent.Workspace,
			r.trace,
			r.agent.ID,
			r.opts.SessionKey,
			r.runID(),
			r.opts.Channel,
			r.opts.ChatID,
			r.opts.SenderID,
			r.iteration,
			r.modelForRun,
			usedModel,
			"fallback",
			call.fallbackAttempts,
		) {
			r.modelForRun = usedModel
		}
	}

	r.recordLLMResponse(call.response, usedModel)
	r.recordTokenUsage(call.response, usedModel)
	return r.absorbSteeringMessages(usedModel)
}

func (r *llmIterationRunner) runID() string {
	if r.trace != nil {
		return r.trace.RunID()
	}
	return strings.TrimSpace(r.opts.RunID)
}

func (r *llmIterationRunner) recordLLMResponse(response *providers.LLMResponse, usedModel string) {
	if r.trace == nil {
		return
	}
	if strings.TrimSpace(usedModel) != "" {
		r.trace.model = strings.TrimSpace(usedModel)
	}
	toolNames := make([]string, 0, len(response.ToolCalls))
	for _, tc := range response.ToolCalls {
		toolNames = append(toolNames, tc.Name)
	}
	sort.Strings(toolNames)
	r.trace.recordLLMResponse(r.iteration, response.Content, toolNames, response.Usage)
}

func (r *llmIterationRunner) recordTokenUsage(response *providers.LLMResponse, usedModel string) {
	if response.Usage == nil {
		return
	}
	if strings.TrimSpace(usedModel) == "" {
		usedModel = r.modelForRun
	}
	if store := r.loop.tokenUsageStore(r.agent.Workspace); store != nil {
		store.Record(usedModel, response.Usage)
	}
	logger.InfoCF("agent", "Token usage",
		map[string]any{
			"agent_id":          r.agent.ID,
			"iteration":         r.iteration,
			"model":             usedModel,
			"prompt_tokens":     response.Usage.PromptTokens,
			"completion_tokens": response.Usage.CompletionTokens,
			"total_tokens":      response.Usage.TotalTokens,
			"session_key":       r.opts.SessionKey,
		})
	r.totalPromptTokens += response.Usage.PromptTokens
	r.totalCompletionTokens += response.Usage.CompletionTokens
}

func (r *llmIterationRunner) absorbSteeringMessages(usedModel string) bool {
	if r.opts.Steering == nil {
		return false
	}
	steeringMsgs := make([]bus.InboundMessage, 0, 4)
	for {
		select {
		case sm := <-r.opts.Steering:
			steeringMsgs = append(steeringMsgs, sm)
		default:
			goto steeringDrained
		}
	}
steeringDrained:
	if len(steeringMsgs) == 0 {
		return false
	}
	for _, sm := range steeringMsgs {
		content := strings.TrimSpace(sm.Content)
		if content == "" {
			continue
		}
		addSessionMessageAndSave(
			r.agent.Sessions,
			r.opts.SessionKey,
			"user",
			content,
			"Failed to persist steering message (best-effort)",
			map[string]any{"iteration": r.iteration},
		)
		r.messages = append(r.messages, providers.Message{Role: "user", Content: content})
		if r.trace != nil {
			now := time.Now()
			r.trace.appendEvent(runTraceEvent{
				Type:               "steering.message",
				TS:                 now.UTC().Format(time.RFC3339Nano),
				TSMS:               now.UnixMilli(),
				RunID:              r.trace.runID,
				SessionKey:         r.opts.SessionKey,
				Channel:            strings.TrimSpace(r.opts.Channel),
				ChatID:             strings.TrimSpace(r.opts.ChatID),
				SenderID:           strings.TrimSpace(r.opts.SenderID),
				AgentID:            strings.TrimSpace(r.agent.ID),
				Model:              strings.TrimSpace(usedModel),
				Iteration:          r.iteration,
				UserMessagePreview: utils.Truncate(content, 400),
				UserMessageChars:   len(content),
			})
		}
	}
	return true
}

func (r *llmIterationRunner) handleLLMResponse(response *providers.LLMResponse) bool {
	go r.loop.handleReasoning(
		r.ctx,
		response.Reasoning,
		r.opts.Channel,
		r.loop.targetReasoningChannelID(r.opts.Channel),
	)

	logger.DebugCF("agent", "LLM response",
		map[string]any{
			"agent_id":       r.agent.ID,
			"iteration":      r.iteration,
			"content_chars":  len(response.Content),
			"tool_calls":     len(response.ToolCalls),
			"reasoning":      response.Reasoning,
			"target_channel": r.loop.targetReasoningChannelID(r.opts.Channel),
			"channel":        r.opts.Channel,
		})

	if len(response.ToolCalls) == 0 {
		r.finalContent = response.Content
		logger.InfoCF("agent", "LLM response without tool calls (direct answer)",
			map[string]any{
				"agent_id":      r.agent.ID,
				"iteration":     r.iteration,
				"content_chars": len(r.finalContent),
			})
		return true
	}

	normalizedToolCalls := normalizeToolCalls(response.ToolCalls)
	if r.exceedsToolCallBudget(normalizedToolCalls) {
		return true
	}
	r.updateWorkingStateHint(response.Content)
	r.logRequestedToolCalls(normalizedToolCalls)
	if r.handleToolLoop(response, normalizedToolCalls) {
		return false
	}
	r.appendAssistantToolCallMessage(response, normalizedToolCalls)
	toolExecutions := r.executeToolCalls(normalizedToolCalls)
	r.recordRecentToolCalls(toolExecutions)
	r.applyToolExecutionResults(toolExecutions)
	return false
}

func normalizeToolCalls(toolCalls []providers.ToolCall) []providers.ToolCall {
	normalized := make([]providers.ToolCall, 0, len(toolCalls))
	for _, tc := range toolCalls {
		normalized = append(normalized, providers.NormalizeToolCall(tc))
	}
	return normalized
}

func (r *llmIterationRunner) exceedsToolCallBudget(toolCalls []providers.ToolCall) bool {
	if r.maxToolCallsPerRun <= 0 || r.toolCallsUsed+len(toolCalls) <= r.maxToolCallsPerRun {
		return false
	}
	r.finalContent = fmt.Sprintf(
		"RESOURCE_BUDGET_EXCEEDED: tool call budget exceeded (%d). Please narrow the request or reduce the number of tools used.",
		r.maxToolCallsPerRun,
	)
	logger.WarnCF("agent", "Resource budget exceeded (tool calls)", map[string]any{
		"agent_id":           r.agent.ID,
		"iteration":          r.iteration,
		"tool_calls_used":    r.toolCallsUsed,
		"tool_calls_pending": len(toolCalls),
		"tool_calls_budget":  r.maxToolCallsPerRun,
		"session_key":        r.opts.SessionKey,
	})
	return true
}

func (r *llmIterationRunner) updateWorkingStateHint(content string) {
	reasoning := strings.TrimSpace(content)
	if reasoning == "" || r.opts.WorkingState == nil {
		return
	}
	hint := reasoning
	if len(hint) > 200 {
		hint = hint[:200] + "..."
	}
	r.opts.WorkingState.SetNextAction(hint)
}

func (r *llmIterationRunner) logRequestedToolCalls(toolCalls []providers.ToolCall) {
	toolNames := make([]string, 0, len(toolCalls))
	for _, tc := range toolCalls {
		toolNames = append(toolNames, tc.Name)
	}
	logger.InfoCF("agent", "LLM requested tool calls",
		map[string]any{
			"agent_id":  r.agent.ID,
			"tools":     toolNames,
			"count":     len(toolCalls),
			"iteration": r.iteration,
		})
}

func (r *llmIterationRunner) handleToolLoop(response *providers.LLMResponse, toolCalls []providers.ToolCall) bool {
	loopingTool := detectToolCallLoop(r.recentToolCalls, toolCalls, 3)
	if loopingTool == "" {
		return false
	}
	logger.WarnCF("agent", "Tool call loop detected",
		map[string]any{
			"agent_id":  r.agent.ID,
			"tool":      loopingTool,
			"iteration": r.iteration,
		})

	loopAssistantMsg := providers.Message{Role: "assistant", Content: response.Content}
	for _, tc := range toolCalls {
		argumentsJSON, _ := json.Marshal(tc.Arguments)
		loopAssistantMsg.ToolCalls = append(loopAssistantMsg.ToolCalls, providers.ToolCall{
			ID:   tc.ID,
			Type: "function",
			Name: tc.Name,
			Function: &providers.FunctionCall{
				Name:      tc.Name,
				Arguments: string(argumentsJSON),
			},
		})
	}
	r.messages = append(r.messages, loopAssistantMsg)

	loopNotice := fmt.Sprintf("Loop detected: '%s' called with same arguments 3+ times. Try a different approach, use a different tool, or explain why you are stuck.", loopingTool)
	for _, tc := range toolCalls {
		r.messages = append(r.messages, providers.Message{Role: "tool", Content: loopNotice, ToolCallID: tc.ID})
	}
	return true
}

func toolResultFingerprint(result *tools.ToolResult) string {
	if result == nil {
		return "<nil>"
	}
	errText := ""
	if result.Err != nil {
		errText = utils.TruncateHeadTail(strings.TrimSpace(result.Err.Error()), 120, 40)
	}
	return fmt.Sprintf(
		"is_error=%t|async=%t|llm=%s|user=%s|err=%s|media=%s",
		result.IsError,
		result.Async,
		utils.TruncateHeadTail(strings.TrimSpace(result.ForLLM), 160, 50),
		utils.TruncateHeadTail(strings.TrimSpace(result.ForUser), 120, 40),
		errText,
		strings.Join(result.Media, ","),
	)
}

func (r *llmIterationRunner) recordRecentToolCalls(toolExecutions []tools.ToolCallExecution) {
	for _, execution := range toolExecutions {
		argsJSON, _ := json.Marshal(execution.ToolCall.Arguments)
		r.recentToolCalls = append(r.recentToolCalls, toolCallSignature{
			Name:              execution.ToolCall.Name,
			Args:              string(argsJSON),
			ResultFingerprint: toolResultFingerprint(execution.Result),
		})
	}
	const maxRecentToolCalls = 24
	if len(r.recentToolCalls) > maxRecentToolCalls {
		r.recentToolCalls = append([]toolCallSignature(nil), r.recentToolCalls[len(r.recentToolCalls)-maxRecentToolCalls:]...)
	}
}

func (r *llmIterationRunner) appendAssistantToolCallMessage(response *providers.LLMResponse, toolCalls []providers.ToolCall) {
	assistantMsg := providers.Message{Role: "assistant", Content: response.Content, ReasoningContent: response.ReasoningContent}
	for _, tc := range toolCalls {
		argumentsJSON, _ := json.Marshal(tc.Arguments)
		extraContent := tc.ExtraContent
		thoughtSignature := ""
		if tc.Function != nil {
			thoughtSignature = tc.Function.ThoughtSignature
		}
		assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, providers.ToolCall{
			ID:   tc.ID,
			Type: "function",
			Name: tc.Name,
			Function: &providers.FunctionCall{
				Name:             tc.Name,
				Arguments:        string(argumentsJSON),
				ThoughtSignature: thoughtSignature,
			},
			ExtraContent:     extraContent,
			ThoughtSignature: thoughtSignature,
		})
	}
	r.messages = append(r.messages, assistantMsg)
	addSessionFullMessage(r.agent.Sessions, r.opts.SessionKey, assistantMsg)
}

func (r *llmIterationRunner) executeToolCalls(toolCalls []providers.ToolCall) []tools.ToolCallExecution {
	cfg := r.loop.Config()
	parallelCfg := tools.ToolCallParallelConfig{Enabled: cfg != nil && cfg.Orchestration.ToolCallsParallelEnabled}
	if cfg != nil {
		parallelCfg.MaxConcurrency = cfg.Orchestration.MaxToolCallConcurrency
		parallelCfg.Mode = cfg.Orchestration.ParallelToolsMode
		parallelCfg.ToolPolicyOverrides = cfg.Orchestration.ToolParallelOverrides
	}

	traceOpts := tools.ToolTraceOptions{}
	if cfg != nil {
		traceOpts.Enabled = cfg.Tools.Trace.Enabled
		traceOpts.Dir = cfg.Tools.Trace.Dir
		traceOpts.WritePerCallFiles = cfg.Tools.Trace.WritePerCallFiles
		traceOpts.MaxArgPreviewChars = cfg.Tools.Trace.MaxArgPreviewChars
		traceOpts.MaxResultPreviewChars = cfg.Tools.Trace.MaxResultPreviewChars
	}

	errorTemplateOpts := tools.ToolErrorTemplateOptions{}
	if cfg != nil {
		errorTemplateOpts.Enabled = cfg.Tools.ErrorTemplate.Enabled
		errorTemplateOpts.IncludeSchema = cfg.Tools.ErrorTemplate.IncludeSchema
		errorTemplateOpts.IncludeAvailableTools = true
	}

	toolExecutions := tools.ExecuteToolCalls(r.ctx, r.agent.Tools, toolCalls, tools.ToolCallExecutionOptions{
		Channel:        r.opts.Channel,
		ChatID:         r.opts.ChatID,
		SenderID:       r.opts.SenderID,
		Workspace:      r.agent.Workspace,
		SessionKey:     r.opts.SessionKey,
		RunID:          r.runID(),
		IsResume:       r.opts.Resume,
		Iteration:      r.iteration,
		LogScope:       "agent",
		Parallel:       parallelCfg,
		Trace:          traceOpts,
		MaxResultChars: r.maxToolResultChars,
		ErrorTemplate:  errorTemplateOpts,
		Hooks:          tools.BuildDefaultToolHooks(cfg),
		AsyncCallbackForCall: func(call providers.ToolCall) tools.AsyncCallback {
			return func(callbackCtx context.Context, result *tools.ToolResult) {
				if result == nil {
					return
				}
				if !result.Silent && result.ForUser != "" {
					logger.InfoCF("agent", "Async tool completed, agent will handle notification",
						map[string]any{"tool": call.Name, "content_len": len(result.ForUser)})
				}
			}
		},
	})
	r.toolCallsUsed += len(toolExecutions)
	if r.trace != nil {
		r.trace.recordToolBatch(r.iteration, toolExecutions)
	}
	return toolExecutions
}

func (r *llmIterationRunner) applyToolExecutionResults(toolExecutions []tools.ToolCallExecution) {
	for _, executed := range toolExecutions {
		toolResult := executed.Result
		tc := executed.ToolCall

		if ws := r.opts.WorkingState; ws != nil {
			ws.RecordToolCall(tc.Name, toolResult.IsError)
			outcome := toolResult.ForLLM
			if len(outcome) > 120 {
				outcome = outcome[:120] + "..."
			}
			if toolResult.IsError {
				outcome = "[error] " + outcome
			}
			ws.AddCompletedStep(tc.Name, outcome, tc.Name)
		}

		if !toolResult.Silent && toolResult.ForUser != "" && r.opts.SendResponse {
			r.loop.bus.PublishOutbound(r.ctx, bus.OutboundMessage{Channel: r.opts.Channel, ChatID: r.opts.ChatID, Content: toolResult.ForUser})
			logger.DebugCF("agent", "Sent tool result to user",
				map[string]any{"tool": tc.Name, "content_len": len(toolResult.ForUser)})
		}

		if len(toolResult.Media) > 0 && r.opts.SendResponse {
			parts := make([]bus.MediaPart, 0, len(toolResult.Media))
			for _, ref := range toolResult.Media {
				part := bus.MediaPart{Ref: ref}
				if r.loop.mediaResolver != nil {
					if _, meta, err := r.loop.mediaResolver.ResolveWithMeta(ref); err == nil {
						part.Filename = strings.TrimSpace(meta.Filename)
						part.ContentType = strings.TrimSpace(meta.ContentType)
						part.Type = inferMediaType(part.Filename, part.ContentType)
					}
				}
				parts = append(parts, part)
			}
			r.loop.bus.PublishOutboundMedia(r.ctx, bus.OutboundMediaMessage{Channel: r.opts.Channel, ChatID: r.opts.ChatID, Parts: parts})
		}

		contentForLLM := toolResult.ForLLM
		if contentForLLM == "" && toolResult.Err != nil {
			contentForLLM = toolResult.Err.Error()
		}
		toolResultMsg := providers.Message{Role: "tool", Content: contentForLLM, ToolCallID: tc.ID}
		r.messages = append(r.messages, toolResultMsg)
		addSessionFullMessage(r.agent.Sessions, r.opts.SessionKey, toolResultMsg)
	}
}

func (r *llmIterationRunner) logTokenSummary() {
	if r.totalPromptTokens == 0 && r.totalCompletionTokens == 0 {
		return
	}
	logger.InfoCF("agent", "Request token usage summary",
		map[string]any{
			"agent_id":                r.agent.ID,
			"iterations":              r.iteration,
			"total_prompt_tokens":     r.totalPromptTokens,
			"total_completion_tokens": r.totalCompletionTokens,
			"total_tokens":            r.totalPromptTokens + r.totalCompletionTokens,
			"session_key":             r.opts.SessionKey,
		})
}
