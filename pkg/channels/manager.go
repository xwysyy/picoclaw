// X-Claw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 X-Claw contributors

package channels

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/xwysyy/X-Claw/pkg/auditlog"
	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/health"
	"github.com/xwysyy/X-Claw/pkg/identity"
	"github.com/xwysyy/X-Claw/pkg/logger"
	"github.com/xwysyy/X-Claw/pkg/media"
)

const (
	defaultChannelQueueSize = 16
	defaultRateLimit        = 10 // default 10 msg/s
	maxRetries              = 3

	janitorInterval = 10 * time.Second
	typingStopTTL   = 5 * time.Minute
	placeholderTTL  = 10 * time.Minute

	defaultPlaceholderDelay = 2500 * time.Millisecond
)

// Retry timing defaults. These are variables (not const) so tests can override
// them to run quickly without waiting multiple real seconds.
var (
	rateLimitDelay = 1 * time.Second
	baseBackoff    = 500 * time.Millisecond
	maxBackoff     = 8 * time.Second
)

type channelWorker struct {
	ch         Channel
	queue      chan bus.OutboundMessage
	mediaQueue chan bus.OutboundMediaMessage
	done       chan struct{}
	mediaDone  chan struct{}
	limiter    *rate.Limiter
}

type Manager struct {
	channels      map[string]Channel
	workers       map[string]*channelWorker
	bus           *bus.MessageBus
	config        *config.Config
	mediaStore    media.MediaStore
	dispatchTask  *asyncTask
	healthServer  *health.Server
	mux           *http.ServeMux
	httpServer    *http.Server
	mu            sync.RWMutex
	placeholders  sync.Map // "channel:chatID" → placeholderID (string)
	scheduled     sync.Map // "channel:chatID" → scheduledPlaceholderEntry
	typingStops   sync.Map // "channel:chatID" → func()
	reactionUndos sync.Map // "channel:chatID" → reactionEntry

	scheduledSeq atomic.Int64
}

type asyncTask struct {
	cancel context.CancelFunc
}

func (m *Manager) recordChannelAudit(evType string, channel string, chatID string, note string) {
	if m == nil || m.config == nil {
		return
	}
	evType = strings.TrimSpace(evType)
	channel = strings.TrimSpace(channel)
	chatID = strings.TrimSpace(chatID)
	if evType == "" || channel == "" || chatID == "" {
		return
	}

	workspace := strings.TrimSpace(m.config.WorkspacePath())
	if workspace == "" {
		return
	}

	auditlog.Record(workspace, auditlog.Event{
		Type:       evType,
		Source:     "channels",
		SessionKey: strings.ToLower(channel + ":" + chatID),
		Channel:    channel,
		ChatID:     chatID,
		Note:       strings.TrimSpace(note),
	})
}

// preSend handles typing stop, reaction undo, and placeholder editing before sending a message.
// Returns true if the message was edited into a placeholder (skip Send).
func (m *Manager) preSend(ctx context.Context, name string, msg bus.OutboundMessage, ch Channel) bool {
	key := name + ":" + msg.ChatID

	// 0. Cancel any delayed placeholder that hasn't been sent yet (avoids flicker).
	m.CancelPlaceholder(name, msg.ChatID)

	// 1. Stop typing
	if v, loaded := m.typingStops.LoadAndDelete(key); loaded {
		if entry, ok := v.(typingEntry); ok {
			entry.stop() // idempotent, safe
		}
	}

	// 2. Undo reaction
	if v, loaded := m.reactionUndos.LoadAndDelete(key); loaded {
		if entry, ok := v.(reactionEntry); ok {
			entry.undo() // idempotent, safe
		}
	}

	// 3. Try editing placeholder
	if v, loaded := m.placeholders.LoadAndDelete(key); loaded {
		if entry, ok := v.(placeholderEntry); ok && entry.id != "" {
			if editor, ok := ch.(MessageEditor); ok {
				if err := editor.EditMessage(ctx, msg.ChatID, entry.id, msg.Content); err == nil {
					m.bindReplyContext(name, msg.ChatID, entry.id, msg.SessionKey)
					m.recordChannelAudit("channel.placeholder.edited", name, msg.ChatID, fmt.Sprintf("id=%q", entry.id))
					return true // edited successfully, skip Send
				} else {
					m.recordChannelAudit("channel.placeholder.edit_failed", name, msg.ChatID, fmt.Sprintf("id=%q error=%q", entry.id, err.Error()))
				}
				// edit failed → fall through to normal Send
			}
		}
	}

	return false
}

func NewManager(cfg *config.Config, messageBus *bus.MessageBus, store media.MediaStore) (*Manager, error) {
	if cfg != nil {
		if err := cfg.ValidateActiveChannelConfig(); err != nil {
			return nil, err
		}
	}

	m := &Manager{
		channels:   make(map[string]Channel),
		workers:    make(map[string]*channelWorker),
		bus:        messageBus,
		config:     cfg,
		mediaStore: store,
	}

	if err := m.initChannels(); err != nil {
		return nil, err
	}

	return m, nil
}

// initChannel is a helper that looks up a factory by name and creates the channel.
func (m *Manager) initChannel(name, displayName string) {
	f, ok := getFactory(name)
	if !ok {
		logger.WarnCF("channels", "Factory not registered", map[string]any{
			"channel": displayName,
		})
		return
	}
	logger.DebugCF("channels", "Attempting to initialize channel", map[string]any{
		"channel": displayName,
	})
	ch, err := f(m.config, m.bus)
	if err != nil {
		logger.ErrorCF("channels", "Failed to initialize channel", map[string]any{
			"channel": displayName,
			"error":   err.Error(),
		})
	} else {
		// Inject MediaStore if channel supports it
		if m.mediaStore != nil {
			if setter, ok := ch.(interface{ SetMediaStore(s media.MediaStore) }); ok {
				setter.SetMediaStore(m.mediaStore)
			}
		}
		// Inject PlaceholderRecorder if channel supports it
		if setter, ok := ch.(interface{ SetPlaceholderRecorder(r PlaceholderRecorder) }); ok {
			setter.SetPlaceholderRecorder(m)
		}
		// Inject owner reference so BaseChannel.HandleMessage can auto-trigger typing/reaction
		if setter, ok := ch.(interface{ SetOwner(ch Channel) }); ok {
			setter.SetOwner(ch)
		}
		m.channels[name] = ch
		logger.InfoCF("channels", "Channel enabled successfully", map[string]any{
			"channel": displayName,
		})
	}
}

func (m *Manager) initChannels() error {
	logger.InfoC("channels", "Initializing channel manager")

	for _, spec := range selectedChannelInitializers(m.config) {
		if spec.enabled != nil && spec.enabled(m.config) {
			m.initChannel(spec.name, spec.displayName)
		}
	}

	logger.InfoCF("channels", "Channel initialization completed", map[string]any{
		"enabled_channels": len(m.channels),
	})

	return nil
}

// SetupHTTPServer creates a shared HTTP server with the given listen address.
// It registers health endpoints from the health server and discovers channels
// that implement WebhookHandler and/or HealthChecker to register their handlers.
func (m *Manager) SetupHTTPServer(addr string, healthServer *health.Server) {
	m.mux = http.NewServeMux()

	// Register health endpoints
	if healthServer != nil {
		m.healthServer = healthServer
		healthServer.RegisterOnMux(m.mux)
	}

	// Discover and register webhook handlers and health checkers
	for name, ch := range m.channels {
		if wh, ok := ch.(WebhookHandler); ok {
			m.mux.Handle(wh.WebhookPath(), wh)
			logger.InfoCF("channels", "Webhook handler registered", map[string]any{
				"channel": name,
				"path":    wh.WebhookPath(),
			})
		}
		if hc, ok := ch.(HealthChecker); ok {
			m.mux.HandleFunc(hc.HealthPath(), hc.HealthHandler)
			logger.InfoCF("channels", "Health endpoint registered", map[string]any{
				"channel": name,
				"path":    hc.HealthPath(),
			})
		}
	}

	m.httpServer = &http.Server{
		Addr:        addr,
		Handler:     withSecurityHeaders(m.mux),
		ReadTimeout: 30 * time.Second,
		// Some gateway endpoints (e.g. resume_last_task) may take longer than a typical
		// webhook response because they can trigger LLM + tool execution.
		// Keep ReadTimeout strict to avoid slowloris, but allow longer writes.
		WriteTimeout: 3 * time.Minute,
		IdleTimeout:  3 * time.Minute,
	}
}

// RegisterHTTPHandler registers an extra HTTP handler on the shared gateway server.
// SetupHTTPServer must be called before using this.
func (m *Manager) RegisterHTTPHandler(pattern string, handler http.Handler) (err error) {
	if m.mux == nil {
		return fmt.Errorf("http server not initialized: call SetupHTTPServer first")
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("register http handler %q: %v", pattern, r)
		}
	}()
	m.mux.Handle(pattern, handler)
	return nil
}

func (m *Manager) StartAll(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.channels) == 0 {
		logger.WarnC("channels", "No channels enabled")
		return errors.New("no channels enabled")
	}

	logger.InfoC("channels", "Starting all channels")

	dispatchCtx, cancel := context.WithCancel(ctx)
	m.dispatchTask = &asyncTask{cancel: cancel}
	started := 0

	for name, channel := range m.channels {
		logger.InfoCF("channels", "Starting channel", map[string]any{
			"channel": name,
		})
		if err := channel.Start(ctx); err != nil {
			logger.ErrorCF("channels", "Failed to start channel", map[string]any{
				"channel": name,
				"error":   err.Error(),
			})
			continue
		}
		started++
		// Lazily create worker only after channel starts successfully
		w := newChannelWorker(name, channel)
		m.workers[name] = w
		go m.runWorker(dispatchCtx, name, w)
		go m.runMediaWorker(dispatchCtx, name, w)
	}

	if started == 0 {
		cancel()
		m.dispatchTask = nil
		if m.healthServer != nil {
			m.healthServer.SetReady(false)
		}
		return errors.New("no channels started successfully")
	}

	// Start the dispatcher that reads from the bus and routes to workers
	go m.dispatchOutbound(dispatchCtx)
	go m.dispatchOutboundMedia(dispatchCtx)

	// Start the TTL janitor that cleans up stale typing/placeholder entries
	go m.runTTLJanitor(dispatchCtx)

	// Start shared HTTP server if configured.
	// Capture the server pointer so concurrent StopAll()/reload mutations do not race
	// the goroutine into dereferencing a nil field on the manager.
	if server := m.httpServer; server != nil {
		go func(server *http.Server) {
			logger.InfoCF("channels", "Shared HTTP server listening", map[string]any{
				"addr": server.Addr,
			})
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.ErrorCF("channels", "Shared HTTP server error", map[string]any{
					"error": err.Error(),
				})
			}
		}(server)
	}

	if m.healthServer != nil {
		m.healthServer.SetReady(true)
	}

	logger.InfoC("channels", "All channels started")
	return nil
}

func (m *Manager) StopAll(ctx context.Context) error {
	m.mu.Lock()

	logger.InfoC("channels", "Stopping all channels")

	if m.healthServer != nil {
		m.healthServer.SetReady(false)
	}

	// Shutdown shared HTTP server first
	if m.httpServer != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := m.httpServer.Shutdown(shutdownCtx); err != nil {
			logger.ErrorCF("channels", "Shared HTTP server shutdown error", map[string]any{
				"error": err.Error(),
			})
		}
		m.httpServer = nil
	}

	// Cancel dispatcher
	if m.dispatchTask != nil {
		m.dispatchTask.cancel()
		m.dispatchTask = nil
	}

	workers := make([]*channelWorker, 0, len(m.workers))
	for _, w := range m.workers {
		if w != nil {
			workers = append(workers, w)
		}
	}
	m.workers = make(map[string]*channelWorker)
	m.mu.Unlock()

	for _, w := range workers {
		<-w.done
	}
	for _, w := range workers {
		<-w.mediaDone
	}

	// Stop all channels
	m.mu.Lock()
	for name, channel := range m.channels {
		logger.InfoCF("channels", "Stopping channel", map[string]any{
			"channel": name,
		})
		if err := channel.Stop(ctx); err != nil {
			logger.ErrorCF("channels", "Error stopping channel", map[string]any{
				"channel": name,
				"error":   err.Error(),
			})
		}
	}

	logger.InfoC("channels", "All channels stopped")
	m.mu.Unlock()
	return nil
}

func (m *Manager) GetChannel(name string) (Channel, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	channel, ok := m.channels[name]
	return channel, ok
}

func (m *Manager) GetStatus() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()

	status := make(map[string]any)
	for name, channel := range m.channels {
		status[name] = map[string]any{
			"enabled": true,
			"running": channel.IsRunning(),
		}
	}
	return status
}

func (m *Manager) GetEnabledChannels() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.channels))
	for name := range m.channels {
		names = append(names, name)
	}
	return names
}

// EnabledChannels implements the agent.ChannelDirectory port.
// It is a small adapter shim to avoid agent core depending on the full Manager API.
func (m *Manager) EnabledChannels() []string {
	return m.GetEnabledChannels()
}

// HasChannel implements the agent.ChannelDirectory port.
func (m *Manager) HasChannel(channelName string) bool {
	_, ok := m.GetChannel(channelName)
	return ok
}

// ReasoningChannelID implements the agent.ChannelDirectory port.
func (m *Manager) ReasoningChannelID(channelName string) string {
	ch, ok := m.GetChannel(channelName)
	if !ok || ch == nil {
		return ""
	}
	return ch.ReasoningChannelID()
}

func (m *Manager) RegisterChannel(name string, channel Channel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.channels[name] = channel
}

func (m *Manager) UnregisterChannel(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w, ok := m.workers[name]; ok && w != nil {
		close(w.queue)
		<-w.done
		close(w.mediaQueue)
		<-w.mediaDone
	}
	delete(m.workers, name)
	delete(m.channels, name)
}

func (m *Manager) SendToChannel(ctx context.Context, channelName, chatID, content string) error {
	m.mu.RLock()
	_, exists := m.channels[channelName]
	w, wExists := m.workers[channelName]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("channel %s not found", channelName)
	}

	msg := bus.OutboundMessage{
		Channel: channelName,
		ChatID:  chatID,
		Content: content,
	}

	if !wExists || w == nil {
		return fmt.Errorf("%w: channel %s has no active worker", ErrNotRunning, channelName)
	}

	sent, stopped := enqueueOutboundMessage(ctx, channelName, w, msg)
	if stopped {
		return ctx.Err()
	}
	if !sent {
		return fmt.Errorf("%w: channel %s has no active worker", ErrNotRunning, channelName)
	}
	return nil
}

var (
	uniqueIDCounter uint64
	uniqueIDPrefix  string
)

func init() {
	// One-time read from crypto/rand for a unique prefix (single syscall).
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// fallback to time-based prefix
		binary.BigEndian.PutUint64(b[:], uint64(time.Now().UnixNano()))
	}
	uniqueIDPrefix = hex.EncodeToString(b[:])
}

// uniqueID generates a process-unique ID using a random prefix and an atomic counter.
// This ID is intended for internal correlation (e.g. media scope keys) and is NOT
// cryptographically secure — it must not be used in contexts where unpredictability matters.
func uniqueID() string {
	n := atomic.AddUint64(&uniqueIDCounter, 1)
	return uniqueIDPrefix + strconv.FormatUint(n, 16)
}

// BaseChannelOption is a functional option for configuring a BaseChannel.
type BaseChannelOption func(*BaseChannel)

// WithMaxMessageLength sets the maximum message length (in runes) for a channel.
// Messages exceeding this limit will be automatically split by the Manager.
// A value of 0 means no limit.
func WithMaxMessageLength(n int) BaseChannelOption {
	return func(c *BaseChannel) { c.maxMessageLength = n }
}

// WithGroupTrigger sets the group trigger configuration for a channel.
func WithGroupTrigger(gt config.GroupTriggerConfig) BaseChannelOption {
	return func(c *BaseChannel) { c.groupTrigger = gt }
}

// WithReasoningChannelID sets the reasoning channel ID where thoughts should be sent.
func WithReasoningChannelID(id string) BaseChannelOption {
	return func(c *BaseChannel) { c.reasoningChannelID = id }
}

// WithPlaceholder configures placeholder scheduling behavior for this channel.
// DelayMS=0 means send immediately; typical default is ~2500ms to avoid flicker.
func WithPlaceholder(ph config.PlaceholderConfig) BaseChannelOption {
	return func(c *BaseChannel) {
		delayMS := ph.DelayMS
		if delayMS < 0 {
			delayMS = 0
		}
		c.placeholderDelay = time.Duration(delayMS) * time.Millisecond
	}
}

type BaseChannel struct {
	config              any
	bus                 *bus.MessageBus
	running             atomic.Bool
	name                string
	allowList           []string
	maxMessageLength    int
	groupTrigger        config.GroupTriggerConfig
	mediaStore          media.MediaStore
	placeholderRecorder PlaceholderRecorder
	placeholderDelay    time.Duration
	owner               Channel // the concrete channel that embeds this BaseChannel
	reasoningChannelID  string
}

func NewBaseChannel(
	name string,
	config any,
	bus *bus.MessageBus,
	allowList []string,
	opts ...BaseChannelOption,
) *BaseChannel {
	bc := &BaseChannel{
		config:    config,
		bus:       bus,
		name:      name,
		allowList: allowList,
		// Default placeholder delay (threshold): avoids flicker for fast replies.
		placeholderDelay: defaultPlaceholderDelay,
	}
	for _, opt := range opts {
		opt(bc)
	}
	return bc
}

// MaxMessageLength returns the maximum message length (in runes) for this channel.
// A value of 0 means no limit.
func (c *BaseChannel) MaxMessageLength() int {
	return c.maxMessageLength
}

// ShouldRespondInGroup determines whether the bot should respond in a group chat.
// Each channel is responsible for:
//  1. Detecting isMentioned (platform-specific)
//  2. Stripping bot mention from content (platform-specific)
//  3. Calling this method to get the group response decision
//
// Logic:
//   - If isMentioned → always respond
//   - If command_bypass enabled and message looks like a command → respond
//   - If mention_only configured and not mentioned → ignore
//   - If prefixes configured → respond if content starts with any prefix (strip it)
//   - If mentionless enabled → respond to all
//   - Otherwise (no trigger configured) → ignore (safe-by-default)
func (c *BaseChannel) ShouldRespondInGroup(isMentioned bool, content string) (bool, string) {
	gt := c.groupTrigger

	// Mentioned → always respond
	if isMentioned {
		return true, strings.TrimSpace(content)
	}

	trimmed := strings.TrimSpace(content)

	// Slash-command bypass (e.g. "/tree", "/switch ...") to keep ops usable in groups.
	if gt.CommandBypass && isCommandBypassHit(trimmed, gt.CommandPrefixes) {
		return true, trimmed
	}

	// mention_only → require mention
	if gt.MentionOnly {
		return false, content
	}

	// Prefix matching
	if len(gt.Prefixes) > 0 {
		for _, prefix := range gt.Prefixes {
			if prefix != "" && strings.HasPrefix(trimmed, prefix) {
				return true, strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
			}
		}
		// Prefixes configured but none matched and not mentioned → ignore
		return false, content
	}

	// mentionless → respond to all
	if gt.Mentionless {
		return true, trimmed
	}

	// Safe-by-default: ignore group messages unless explicitly triggered.
	return false, content
}

func isCommandBypassHit(content string, prefixes []string) bool {
	if content == "" {
		return false
	}
	if len(prefixes) == 0 {
		prefixes = []string{"/"}
	}
	for _, p := range prefixes {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.HasPrefix(content, p) && len(content) > len(p) {
			return true
		}
	}
	return false
}

func (c *BaseChannel) Name() string {
	return c.name
}

func (c *BaseChannel) ReasoningChannelID() string {
	return c.reasoningChannelID
}

func (c *BaseChannel) IsRunning() bool {
	return c.running.Load()
}

func (c *BaseChannel) IsAllowed(senderID string) bool {
	if len(c.allowList) == 0 {
		return true
	}

	// Extract parts from compound senderID like "123456|username"
	idPart := senderID
	userPart := ""
	if idx := strings.Index(senderID, "|"); idx > 0 {
		idPart = senderID[:idx]
		userPart = senderID[idx+1:]
	}

	for _, allowed := range c.allowList {
		// Strip leading "@" from allowed value for username matching
		trimmed := strings.TrimPrefix(allowed, "@")
		allowedID := trimmed
		allowedUser := ""
		if idx := strings.Index(trimmed, "|"); idx > 0 {
			allowedID = trimmed[:idx]
			allowedUser = trimmed[idx+1:]
		}

		// Support either side using "id|username" compound form.
		// This keeps backward compatibility with legacy Telegram allowlist entries.
		if senderID == allowed ||
			idPart == allowed ||
			senderID == trimmed ||
			idPart == trimmed ||
			idPart == allowedID ||
			(allowedUser != "" && senderID == allowedUser) ||
			(userPart != "" && (userPart == allowed || userPart == trimmed || userPart == allowedUser)) {
			return true
		}
	}

	return false
}

// IsAllowedSender checks whether a structured SenderInfo is permitted by the allow-list.
// It delegates to identity.MatchAllowed for each entry, providing unified matching
// across all legacy formats and the new canonical "platform:id" format.
func (c *BaseChannel) IsAllowedSender(sender bus.SenderInfo) bool {
	if len(c.allowList) == 0 {
		return true
	}

	for _, allowed := range c.allowList {
		if identity.MatchAllowed(sender, allowed) {
			return true
		}
	}

	return false
}

func (c *BaseChannel) HandleMessage(
	ctx context.Context,
	peer bus.Peer,
	messageID, senderID, chatID, content string,
	media []string,
	metadata map[string]string,
	senderOpts ...bus.SenderInfo,
) {
	// Use SenderInfo-based allow check when available, else fall back to string
	var sender bus.SenderInfo
	if len(senderOpts) > 0 {
		sender = senderOpts[0]
	}
	if sender.CanonicalID != "" || sender.PlatformID != "" {
		if !c.IsAllowedSender(sender) {
			logger.WarnCF("channels", "Inbound message blocked by allow_from", map[string]any{
				"channel":          c.name,
				"sender_id":        senderID,
				"sender_canonical": sender.CanonicalID,
				"chat_id":          chatID,
				"message_id":       messageID,
				"allow_from_count": len(c.allowList),
			})
			return
		}
	} else {
		if !c.IsAllowed(senderID) {
			logger.WarnCF("channels", "Inbound message blocked by allow_from", map[string]any{
				"channel":          c.name,
				"sender_id":        senderID,
				"chat_id":          chatID,
				"message_id":       messageID,
				"allow_from_count": len(c.allowList),
			})
			return
		}
	}

	// Set SenderID to canonical if available, otherwise keep the raw senderID
	resolvedSenderID := senderID
	if sender.CanonicalID != "" {
		resolvedSenderID = sender.CanonicalID
	}

	scope := BuildMediaScope(c.name, chatID, messageID)

	msg := bus.InboundMessage{
		Channel:    c.name,
		SenderID:   resolvedSenderID,
		Sender:     sender,
		ChatID:     chatID,
		Content:    content,
		Media:      media,
		Peer:       peer,
		MessageID:  messageID,
		MediaScope: scope,
		Metadata:   metadata,
	}

	// Auto-trigger typing indicator, message reaction, and placeholder before publishing.
	// Each capability is independent — all three may fire for the same message.
	if c.owner != nil && c.placeholderRecorder != nil {
		// Typing — independent pipeline
		if tc, ok := c.owner.(TypingCapable); ok {
			if stop, err := tc.StartTyping(ctx, chatID); err == nil {
				c.placeholderRecorder.RecordTypingStop(c.name, chatID, stop)
			}
		}
		// Reaction — independent pipeline
		if rc, ok := c.owner.(ReactionCapable); ok && messageID != "" {
			if undo, err := rc.ReactToMessage(ctx, chatID, messageID); err == nil {
				c.placeholderRecorder.RecordReactionUndo(c.name, chatID, undo)
			}
		}
		// Placeholder — independent pipeline
		if pc, ok := c.owner.(PlaceholderCapable); ok {
			if sched, ok := c.placeholderRecorder.(PlaceholderScheduler); ok {
				delay := c.placeholderDelay
				if delay < 0 {
					delay = 0
				}
				sched.SchedulePlaceholder(ctx, c.name, chatID, func(sendCtx context.Context) (string, error) {
					return pc.SendPlaceholder(sendCtx, chatID)
				}, delay)
			} else {
				// Fallback: send placeholder immediately (legacy behavior).
				if phID, err := pc.SendPlaceholder(ctx, chatID); err == nil && phID != "" {
					c.placeholderRecorder.RecordPlaceholder(c.name, chatID, phID)
				}
			}
		}
	}

	if err := c.bus.PublishInbound(ctx, msg); err != nil {
		logger.ErrorCF("channels", "Failed to publish inbound message", map[string]any{
			"channel": c.name,
			"chat_id": chatID,
			"error":   err.Error(),
		})
	}
}

func (c *BaseChannel) SetRunning(running bool) {
	c.running.Store(running)
}

// SetMediaStore injects a MediaStore into the channel.
func (c *BaseChannel) SetMediaStore(s media.MediaStore) { c.mediaStore = s }

// GetMediaStore returns the injected MediaStore (may be nil).
func (c *BaseChannel) GetMediaStore() media.MediaStore { return c.mediaStore }

// SetPlaceholderRecorder injects a PlaceholderRecorder into the channel.
func (c *BaseChannel) SetPlaceholderRecorder(r PlaceholderRecorder) {
	c.placeholderRecorder = r
}

// GetPlaceholderRecorder returns the injected PlaceholderRecorder (may be nil).
func (c *BaseChannel) GetPlaceholderRecorder() PlaceholderRecorder {
	return c.placeholderRecorder
}

// SetOwner injects the concrete channel that embeds this BaseChannel.
// This allows HandleMessage to auto-trigger TypingCapable / ReactionCapable / PlaceholderCapable.
func (c *BaseChannel) SetOwner(ch Channel) {
	c.owner = ch
}

// BuildMediaScope constructs a scope key for media lifecycle tracking.
func BuildMediaScope(channel, chatID, messageID string) string {
	id := messageID
	if id == "" {
		id = uniqueID()
	}
	return channel + ":" + chatID + ":" + id
}

type channelInitSpec struct {
	name        string
	displayName string
	enabled     func(cfg *config.Config) bool
}

func selectedChannelInitializers(cfg *config.Config) []channelInitSpec {
	if cfg == nil {
		return nil
	}

	return []channelInitSpec{
		{
			name:        "telegram",
			displayName: "Telegram",
			enabled: func(cfg *config.Config) bool {
				return cfg.Channels.Telegram.Enabled && cfg.Channels.Telegram.Token.Present()
			},
		},
		{
			name:        "feishu",
			displayName: "Feishu",
			enabled:     func(cfg *config.Config) bool { return cfg.Channels.Feishu.Enabled },
		},
	}
}

func withSecurityHeaders(next http.Handler) http.Handler {
	if next == nil {
		next = http.DefaultServeMux
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()

		// Prevent MIME sniffing.
		if h.Get("X-Content-Type-Options") == "" {
			h.Set("X-Content-Type-Options", "nosniff")
		}
		// Disallow embedding in iframes.
		if h.Get("X-Frame-Options") == "" {
			h.Set("X-Frame-Options", "DENY")
		}
		// Avoid leaking internal URLs via Referer.
		if h.Get("Referrer-Policy") == "" {
			h.Set("Referrer-Policy", "no-referrer")
		}
		// Deny powerful features by default.
		if h.Get("Permissions-Policy") == "" {
			h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		}

		next.ServeHTTP(w, r)
	})
}

func SplitMessage(content string, maxLen int) []string {
	if maxLen <= 0 {
		if content == "" {
			return nil
		}
		return []string{content}
	}

	runes := []rune(content)
	totalLen := len(runes)
	var messages []string

	// Dynamic buffer: 10% of maxLen, but at least 50 chars if possible
	codeBlockBuffer := max(maxLen/10, 50)
	if codeBlockBuffer > maxLen/2 {
		codeBlockBuffer = maxLen / 2
	}

	start := 0
	for start < totalLen {
		remaining := totalLen - start
		if remaining <= maxLen {
			messages = append(messages, string(runes[start:totalLen]))
			break
		}

		// Effective split point: maxLen minus buffer, to leave room for code blocks
		effectiveLimit := max(maxLen-codeBlockBuffer, maxLen/2)

		end := start + effectiveLimit

		// Find natural split point within the effective limit
		msgEnd := findLastNewlineInRange(runes, start, end, 200)
		if msgEnd <= start {
			msgEnd = findLastSpaceInRange(runes, start, end, 100)
		}
		if msgEnd <= start {
			msgEnd = end
		}

		// Check if this would end with an incomplete code block
		unclosedIdx := findLastUnclosedCodeBlockInRange(runes, start, msgEnd)

		if unclosedIdx >= 0 {
			// Message would end with incomplete code block
			// Try to extend up to maxLen to include the closing ```
			if totalLen > msgEnd {
				closingIdx := findNextClosingCodeBlockInRange(runes, msgEnd, totalLen)
				if closingIdx > 0 && closingIdx-start <= maxLen {
					// Extend to include the closing ```
					msgEnd = closingIdx
				} else {
					// Code block is too long to fit in one chunk or missing closing fence.
					// Try to split inside by injecting closing and reopening fences.
					headerEnd := findNewlineFrom(runes, unclosedIdx)
					var header string
					if headerEnd == -1 {
						header = strings.TrimSpace(string(runes[unclosedIdx : unclosedIdx+3]))
					} else {
						header = strings.TrimSpace(string(runes[unclosedIdx:headerEnd]))
					}
					headerEndIdx := unclosedIdx + len([]rune(header))
					if headerEnd != -1 {
						headerEndIdx = headerEnd
					}

					// If we have a reasonable amount of content after the header, split inside
					if msgEnd > headerEndIdx+20 {
						// Find a better split point closer to maxLen
						innerLimit := min(
							// Leave room for "\n```"
							start+maxLen-5, totalLen)
						betterEnd := findLastNewlineInRange(runes, start, innerLimit, 200)
						if betterEnd > headerEndIdx {
							msgEnd = betterEnd
						} else {
							msgEnd = innerLimit
						}
						chunk := strings.TrimRight(string(runes[start:msgEnd]), " \t\n\r") + "\n```"
						messages = append(messages, chunk)
						remaining := strings.TrimSpace(header + "\n" + string(runes[msgEnd:totalLen]))
						// Replace the tail of runes with the reconstructed remaining
						runes = []rune(remaining)
						totalLen = len(runes)
						start = 0
						continue
					}

					// Otherwise, try to split before the code block starts
					newEnd := findLastNewlineInRange(runes, start, unclosedIdx, 200)
					if newEnd <= start {
						newEnd = findLastSpaceInRange(runes, start, unclosedIdx, 100)
					}
					if newEnd > start {
						msgEnd = newEnd
					} else {
						// If we can't split before, we MUST split inside (last resort)
						if unclosedIdx-start > 20 {
							msgEnd = unclosedIdx
						} else {
							splitAt := min(start+maxLen-5, totalLen)
							chunk := strings.TrimRight(string(runes[start:splitAt]), " \t\n\r") + "\n```"
							messages = append(messages, chunk)
							remaining := strings.TrimSpace(header + "\n" + string(runes[splitAt:totalLen]))
							runes = []rune(remaining)
							totalLen = len(runes)
							start = 0
							continue
						}
					}
				}
			}
		}

		if msgEnd <= start {
			msgEnd = start + effectiveLimit
		}

		messages = append(messages, string(runes[start:msgEnd]))
		// Advance start, skipping leading whitespace of next chunk
		start = msgEnd
		for start < totalLen && (runes[start] == ' ' || runes[start] == '\t' || runes[start] == '\n' || runes[start] == '\r') {
			start++
		}
	}

	return messages
}

// findLastUnclosedCodeBlockInRange finds the last opening ``` that doesn't have a closing ```
// within runes[start:end]. Returns the absolute rune index or -1.
func findLastUnclosedCodeBlockInRange(runes []rune, start, end int) int {
	inCodeBlock := false
	lastOpenIdx := -1

	for i := start; i < end; i++ {
		if i+2 < end && runes[i] == '`' && runes[i+1] == '`' && runes[i+2] == '`' {
			if !inCodeBlock {
				lastOpenIdx = i
			}
			inCodeBlock = !inCodeBlock
			i += 2
		}
	}

	if inCodeBlock {
		return lastOpenIdx
	}
	return -1
}

// findNextClosingCodeBlockInRange finds the next closing ``` starting from startIdx
// within runes[startIdx:end]. Returns the absolute index after the closing ``` or -1.
func findNextClosingCodeBlockInRange(runes []rune, startIdx, end int) int {
	for i := startIdx; i < end; i++ {
		if i+2 < end && runes[i] == '`' && runes[i+1] == '`' && runes[i+2] == '`' {
			return i + 3
		}
	}
	return -1
}

// findNewlineFrom finds the first newline character starting from the given index.
// Returns the absolute index or -1 if not found.
func findNewlineFrom(runes []rune, from int) int {
	for i := from; i < len(runes); i++ {
		if runes[i] == '\n' {
			return i
		}
	}
	return -1
}

// findLastNewlineInRange finds the last newline within the last searchWindow runes
// of the range runes[start:end]. Returns the absolute index or start-1 (indicating not found).
func findLastNewlineInRange(runes []rune, start, end, searchWindow int) int {
	searchStart := max(end-searchWindow, start)
	for i := end - 1; i >= searchStart; i-- {
		if runes[i] == '\n' {
			return i
		}
	}
	return start - 1
}

// findLastSpaceInRange finds the last space/tab within the last searchWindow runes
// of the range runes[start:end]. Returns the absolute index or start-1 (indicating not found).
func findLastSpaceInRange(runes []rune, start, end, searchWindow int) int {
	searchStart := max(end-searchWindow, start)
	for i := end - 1; i >= searchStart; i-- {
		if runes[i] == ' ' || runes[i] == '\t' {
			return i
		}
	}
	return start - 1
}

var (
	// ErrNotRunning indicates the channel is not running.
	// Manager will not retry.
	ErrNotRunning = errors.New("channel not running")

	// ErrRateLimit indicates the platform returned a rate-limit response (e.g. HTTP 429).
	// Manager will wait a fixed delay and retry.
	ErrRateLimit = errors.New("rate limited")

	// ErrTemporary indicates a transient failure (e.g. network timeout, 5xx).
	// Manager will use exponential backoff and retry.
	ErrTemporary = errors.New("temporary failure")

	// ErrSendFailed indicates a permanent failure (e.g. invalid chat ID, 4xx non-429).
	// Manager will not retry.
	ErrSendFailed = errors.New("send failed")
)

func ClassifySendError(statusCode int, rawErr error) error {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %v", ErrRateLimit, rawErr)
	case statusCode >= 500:
		return fmt.Errorf("%w: %w", ErrTemporary, rawErr)
	case statusCode >= 400:
		return fmt.Errorf("%w: %w", ErrSendFailed, rawErr)
	default:
		return rawErr
	}
}

// ClassifyNetError wraps a network/timeout error as ErrTemporary.
func ClassifyNetError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrTemporary, err)
}

type MediaSender interface {
	SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) error
}
