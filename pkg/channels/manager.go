// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package channels

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/sipeed/picoclaw/pkg/auditlog"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/health"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
)

const (
	defaultChannelQueueSize = 16
	defaultRateLimit        = 10 // default 10 msg/s
	maxRetries              = 3

	janitorInterval = 10 * time.Second
	typingStopTTL   = 5 * time.Minute
	placeholderTTL  = 10 * time.Minute
)

// Retry timing defaults. These are variables (not const) so tests can override
// them to run quickly without waiting multiple real seconds.
var (
	rateLimitDelay = 1 * time.Second
	baseBackoff    = 500 * time.Millisecond
	maxBackoff     = 8 * time.Second
)

// typingEntry wraps a typing stop function with a creation timestamp for TTL eviction.
type typingEntry struct {
	stop      func()
	createdAt time.Time
}

// reactionEntry wraps a reaction undo function with a creation timestamp for TTL eviction.
type reactionEntry struct {
	undo      func()
	createdAt time.Time
}

// placeholderEntry wraps a placeholder ID with a creation timestamp for TTL eviction.
type placeholderEntry struct {
	id        string
	createdAt time.Time
}

type scheduledPlaceholderEntry struct {
	token     int64
	timer     *time.Timer
	cancel    context.CancelFunc
	createdAt time.Time
}

// channelRateConfig maps channel name to per-second rate limit.
var channelRateConfig = map[string]float64{
	"telegram": 20,
	"discord":  1,
	"slack":    1,
	"line":     10,
}

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

// RecordPlaceholder registers a placeholder message for later editing.
// Implements PlaceholderRecorder.
func (m *Manager) RecordPlaceholder(channel, chatID, placeholderID string) {
	key := channel + ":" + chatID
	m.placeholders.Store(key, placeholderEntry{id: placeholderID, createdAt: time.Now()})
}

// RecordTypingStop registers a typing stop function for later invocation.
// Implements PlaceholderRecorder.
func (m *Manager) RecordTypingStop(channel, chatID string, stop func()) {
	key := channel + ":" + chatID
	m.typingStops.Store(key, typingEntry{stop: stop, createdAt: time.Now()})
}

// RecordReactionUndo registers a reaction undo function for later invocation.
// Implements PlaceholderRecorder.
func (m *Manager) RecordReactionUndo(channel, chatID string, undo func()) {
	key := channel + ":" + chatID
	m.reactionUndos.Store(key, reactionEntry{undo: undo, createdAt: time.Now()})
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

func (m *Manager) SchedulePlaceholder(
	ctx context.Context,
	channel string,
	chatID string,
	send func(context.Context) (string, error),
	delay time.Duration,
) {
	if m == nil {
		return
	}
	channel = strings.TrimSpace(channel)
	chatID = strings.TrimSpace(chatID)
	if channel == "" || chatID == "" || send == nil {
		return
	}

	if delay < 0 {
		delay = 0
	}

	key := channel + ":" + chatID
	m.CancelPlaceholder(channel, chatID)

	if delay == 0 {
		sendCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if id, err := send(sendCtx); err == nil && strings.TrimSpace(id) != "" {
			id = strings.TrimSpace(id)
			m.RecordPlaceholder(channel, chatID, id)
			m.recordChannelAudit("channel.placeholder.sent", channel, chatID, fmt.Sprintf("id=%q delay_ms=0", id))
		}
		return
	}

	token := m.scheduledSeq.Add(1)
	timer := time.NewTimer(delay)
	scheduleCtx, cancel := context.WithCancel(context.Background())
	m.scheduled.Store(key, scheduledPlaceholderEntry{
		token:     token,
		timer:     timer,
		cancel:    cancel,
		createdAt: time.Now(),
	})

	go func() {
		defer func() {
			// Ensure timer resources are released even if CancelPlaceholder was never called.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			cancel()
			if v, ok := m.scheduled.Load(key); ok {
				if entry, ok := v.(scheduledPlaceholderEntry); ok && entry.token == token {
					m.scheduled.Delete(key)
				}
			}
		}()

		select {
		case <-timer.C:
			// ok
		case <-scheduleCtx.Done():
			return
		case <-ctx.Done():
			return
		}

		if scheduleCtx.Err() != nil {
			return
		}

		sendCtx, sendCancel := context.WithTimeout(scheduleCtx, 10*time.Second)
		defer sendCancel()
		id, err := send(sendCtx)
		if err != nil || sendCtx.Err() != nil {
			return
		}
		if strings.TrimSpace(id) != "" {
			id = strings.TrimSpace(id)
			m.RecordPlaceholder(channel, chatID, id)
			m.recordChannelAudit("channel.placeholder.sent", channel, chatID, fmt.Sprintf("id=%q delay_ms=%d", id, delay.Milliseconds()))
		}
	}()
}

func (m *Manager) CancelPlaceholder(channel, chatID string) {
	if m == nil {
		return
	}
	channel = strings.TrimSpace(channel)
	chatID = strings.TrimSpace(chatID)
	if channel == "" || chatID == "" {
		return
	}

	key := channel + ":" + chatID
	if v, loaded := m.scheduled.LoadAndDelete(key); loaded {
		if entry, ok := v.(scheduledPlaceholderEntry); ok {
			if entry.cancel != nil {
				entry.cancel()
			}
			if entry.timer != nil {
				entry.timer.Stop()
			}
			age := time.Since(entry.createdAt).Milliseconds()
			m.recordChannelAudit("channel.placeholder.cancelled", channel, chatID, fmt.Sprintf("age_ms=%d", age))
		}
	}
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

	if m.config.Channels.Telegram.Enabled && m.config.Channels.Telegram.Token.Present() {
		m.initChannel("telegram", "Telegram")
	}

	if m.config.Channels.WhatsApp.Enabled {
		waCfg := m.config.Channels.WhatsApp
		if waCfg.UseNative {
			m.initChannel("whatsapp_native", "WhatsApp Native")
		} else if waCfg.BridgeURL != "" {
			m.initChannel("whatsapp", "WhatsApp")
		}
	}

	if m.config.Channels.Feishu.Enabled {
		m.initChannel("feishu", "Feishu")
	}

	if m.config.Channels.Discord.Enabled && m.config.Channels.Discord.Token.Present() {
		m.initChannel("discord", "Discord")
	}

	if m.config.Channels.QQ.Enabled {
		m.initChannel("qq", "QQ")
	}

	if m.config.Channels.DingTalk.Enabled && m.config.Channels.DingTalk.ClientID != "" {
		m.initChannel("dingtalk", "DingTalk")
	}

	if m.config.Channels.Slack.Enabled && m.config.Channels.Slack.BotToken.Present() {
		m.initChannel("slack", "Slack")
	}

	if m.config.Channels.LINE.Enabled && m.config.Channels.LINE.ChannelAccessToken.Present() {
		m.initChannel("line", "LINE")
	}

	if m.config.Channels.OneBot.Enabled && m.config.Channels.OneBot.WSUrl != "" {
		m.initChannel("onebot", "OneBot")
	}

	if m.config.Channels.WeCom.Enabled && m.config.Channels.WeCom.Token.Present() {
		m.initChannel("wecom", "WeCom")
	}

	if m.config.Channels.WeComAIBot.Enabled && m.config.Channels.WeComAIBot.Token.Present() {
		m.initChannel("wecom_aibot", "WeCom AI Bot")
	}

	if m.config.Channels.WeComApp.Enabled && m.config.Channels.WeComApp.CorpID != "" {
		m.initChannel("wecom_app", "WeCom App")
	}

	if m.config.Channels.Pico.Enabled && m.config.Channels.Pico.Token.Present() {
		m.initChannel("pico", "Pico")
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
		// Lazily create worker only after channel starts successfully
		w := newChannelWorker(name, channel)
		m.workers[name] = w
		go m.runWorker(dispatchCtx, name, w)
		go m.runMediaWorker(dispatchCtx, name, w)
	}

	// Start the dispatcher that reads from the bus and routes to workers
	go m.dispatchOutbound(dispatchCtx)
	go m.dispatchOutboundMedia(dispatchCtx)

	// Start the TTL janitor that cleans up stale typing/placeholder entries
	go m.runTTLJanitor(dispatchCtx)

	// Start shared HTTP server if configured
	if m.httpServer != nil {
		go func() {
			logger.InfoCF("channels", "Shared HTTP server listening", map[string]any{
				"addr": m.httpServer.Addr,
			})
			if err := m.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.ErrorCF("channels", "Shared HTTP server error", map[string]any{
					"error": err.Error(),
				})
			}
		}()
	}

	if m.healthServer != nil {
		m.healthServer.SetReady(true)
	}

	logger.InfoC("channels", "All channels started")
	return nil
}

func (m *Manager) StopAll(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

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

	// Close all worker queues and wait for them to drain
	for _, w := range m.workers {
		if w != nil {
			close(w.queue)
		}
	}
	for _, w := range m.workers {
		if w != nil {
			<-w.done
		}
	}
	// Close all media worker queues and wait for them to drain
	for _, w := range m.workers {
		if w != nil {
			close(w.mediaQueue)
		}
	}
	for _, w := range m.workers {
		if w != nil {
			<-w.mediaDone
		}
	}

	// Stop all channels
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
	return nil
}

// newChannelWorker creates a channelWorker with a rate limiter configured
// for the given channel name.
func newChannelWorker(name string, ch Channel) *channelWorker {
	rateVal := float64(defaultRateLimit)
	if r, ok := channelRateConfig[name]; ok {
		rateVal = r
	}
	burst := int(math.Max(1, math.Ceil(rateVal/2)))

	return &channelWorker{
		ch:         ch,
		queue:      make(chan bus.OutboundMessage, defaultChannelQueueSize),
		mediaQueue: make(chan bus.OutboundMediaMessage, defaultChannelQueueSize),
		done:       make(chan struct{}),
		mediaDone:  make(chan struct{}),
		limiter:    rate.NewLimiter(rate.Limit(rateVal), burst),
	}
}

// runWorker processes outbound messages for a single channel, splitting
// messages that exceed the channel's maximum message length.
func (m *Manager) runWorker(ctx context.Context, name string, w *channelWorker) {
	defer close(w.done)
	for {
		select {
		case msg, ok := <-w.queue:
			if !ok {
				return
			}
			maxLen := 0
			if mlp, ok := w.ch.(MessageLengthProvider); ok {
				maxLen = mlp.MaxMessageLength()
			}
			if maxLen > 0 && len([]rune(msg.Content)) > maxLen {
				chunks := SplitMessage(msg.Content, maxLen)
				for _, chunk := range chunks {
					chunkMsg := msg
					chunkMsg.Content = chunk
					m.sendWithRetry(ctx, name, w, chunkMsg)
				}
			} else {
				m.sendWithRetry(ctx, name, w, msg)
			}
		case <-ctx.Done():
			return
		}
	}
}

// retryWithBackoff executes sendFn with retry logic.
// Callers are responsible for rate limiting before calling this function.
// Error classification determines the retry strategy:
//   - ErrNotRunning / ErrSendFailed: permanent, no retry
//   - ErrRateLimit: fixed delay retry
//   - ErrTemporary / unknown: exponential backoff retry
func (m *Manager) retryWithBackoff(ctx context.Context, w *channelWorker, sendFn func() error) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		lastErr = sendFn()
		if lastErr == nil {
			return nil
		}

		// Permanent failures — don't retry
		if errors.Is(lastErr, ErrNotRunning) || errors.Is(lastErr, ErrSendFailed) {
			break
		}

		// Last attempt exhausted — don't sleep
		if attempt == maxRetries {
			break
		}

		// Rate limit error — fixed delay
		if errors.Is(lastErr, ErrRateLimit) {
			select {
			case <-time.After(rateLimitDelay):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		// ErrTemporary or unknown error — exponential backoff
		backoff := min(time.Duration(float64(baseBackoff)*math.Pow(2, float64(attempt))), maxBackoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return lastErr
}

// sendWithRetry sends a message through the channel with rate limiting and retry logic.
func (m *Manager) sendWithRetry(ctx context.Context, name string, w *channelWorker, msg bus.OutboundMessage) {
	// Rate limit: wait for token before preSend (preserves original ordering)
	if err := w.limiter.Wait(ctx); err != nil {
		return
	}

	// Pre-send: stop typing and try to edit placeholder
	if m.preSend(ctx, name, msg, w.ch) {
		return // placeholder was edited successfully, skip Send
	}

	err := m.retryWithBackoff(ctx, w, func() error {
		return w.ch.Send(ctx, msg)
	})
	if err != nil && ctx.Err() == nil {
		logger.ErrorCF("channels", "Send failed", map[string]any{
			"channel": name,
			"chat_id": msg.ChatID,
			"error":   err.Error(),
			"retries": maxRetries,
		})
	}
}

// sendMediaWithRetry sends a media message through the channel with rate limiting and retry logic.
func (m *Manager) sendMediaWithRetry(ctx context.Context, name string, w *channelWorker, msg bus.OutboundMediaMessage) {
	ms, ok := w.ch.(MediaSender)
	if !ok {
		logger.DebugCF("channels", "Channel does not support MediaSender, skipping media", map[string]any{
			"channel": name,
		})
		return
	}

	// Rate limit: wait for token
	if err := w.limiter.Wait(ctx); err != nil {
		return
	}

	err := m.retryWithBackoff(ctx, w, func() error {
		return ms.SendMedia(ctx, msg)
	})
	if err != nil && ctx.Err() == nil {
		logger.ErrorCF("channels", "SendMedia failed", map[string]any{
			"channel": name,
			"chat_id": msg.ChatID,
			"error":   err.Error(),
			"retries": maxRetries,
		})
	}
}

func dispatchLoop[M any](
	ctx context.Context,
	m *Manager,
	subscribe func(context.Context) (M, bool),
	getChannel func(M) string,
	enqueue func(context.Context, *channelWorker, M) bool,
	startMsg, stopMsg, unknownMsg, noWorkerMsg string,
) {
	logger.InfoC("channels", startMsg)

	for {
		msg, ok := subscribe(ctx)
		if !ok {
			logger.InfoC("channels", stopMsg)
			return
		}

		channel := getChannel(msg)

		// Silently skip internal channels
		if constants.IsInternalChannel(channel) {
			continue
		}

		m.mu.RLock()
		_, exists := m.channels[channel]
		w, wExists := m.workers[channel]
		m.mu.RUnlock()

		if !exists {
			logger.WarnCF("channels", unknownMsg, map[string]any{"channel": channel})
			continue
		}

		if wExists && w != nil {
			if !enqueue(ctx, w, msg) {
				return
			}
		} else if exists {
			logger.WarnCF("channels", noWorkerMsg, map[string]any{"channel": channel})
		}
	}
}

func (m *Manager) dispatchOutbound(ctx context.Context) {
	dispatchLoop(
		ctx, m,
		m.bus.SubscribeOutbound,
		func(msg bus.OutboundMessage) string { return msg.Channel },
		func(ctx context.Context, w *channelWorker, msg bus.OutboundMessage) bool {
			select {
			case w.queue <- msg:
				return true
			case <-ctx.Done():
				return false
			}
		},
		"Outbound dispatcher started",
		"Outbound dispatcher stopped",
		"Unknown channel for outbound message",
		"Channel has no active worker, skipping message",
	)
}

func (m *Manager) dispatchOutboundMedia(ctx context.Context) {
	dispatchLoop(
		ctx, m,
		m.bus.SubscribeOutboundMedia,
		func(msg bus.OutboundMediaMessage) string { return msg.Channel },
		func(ctx context.Context, w *channelWorker, msg bus.OutboundMediaMessage) bool {
			select {
			case w.mediaQueue <- msg:
				return true
			case <-ctx.Done():
				return false
			}
		},
		"Outbound media dispatcher started",
		"Outbound media dispatcher stopped",
		"Unknown channel for outbound media message",
		"Channel has no active worker, skipping media message",
	)
}

// lookupWorker finds the active worker for a channel, logging warnings for unknown or inactive channels.
func (m *Manager) lookupWorker(channel, label string) *channelWorker {
	m.mu.RLock()
	_, exists := m.channels[channel]
	w, wExists := m.workers[channel]
	m.mu.RUnlock()

	if !exists {
		logger.WarnCF("channels", "Unknown channel for "+label+" message", map[string]any{
			"channel": channel,
		})
		return nil
	}
	if !wExists || w == nil {
		logger.WarnCF("channels", "Channel has no active worker, skipping "+label+" message", map[string]any{
			"channel": channel,
		})
		return nil
	}
	return w
}

// runMediaWorker processes outbound media messages for a single channel.
func (m *Manager) runMediaWorker(ctx context.Context, name string, w *channelWorker) {
	defer close(w.mediaDone)
	for {
		select {
		case msg, ok := <-w.mediaQueue:
			if !ok {
				return
			}
			m.sendMediaWithRetry(ctx, name, w, msg)
		case <-ctx.Done():
			return
		}
	}
}

// runTTLJanitor periodically scans the typingStops, reactionUndos, and placeholders maps
// and evicts entries that have exceeded their TTL.
func (m *Manager) runTTLJanitor(ctx context.Context) {
	ticker := time.NewTicker(janitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			m.evictStaleEntries(now)
		}
	}
}

// evictStaleEntries removes expired typing stops, reactions, and placeholders.
func (m *Manager) evictStaleEntries(now time.Time) {
	m.typingStops.Range(func(key, value any) bool {
		if entry, ok := value.(typingEntry); ok {
			if now.Sub(entry.createdAt) > typingStopTTL {
				if _, loaded := m.typingStops.LoadAndDelete(key); loaded {
					entry.stop()
				}
			}
		}
		return true
	})
	m.reactionUndos.Range(func(key, value any) bool {
		if entry, ok := value.(reactionEntry); ok {
			if now.Sub(entry.createdAt) > typingStopTTL {
				if _, loaded := m.reactionUndos.LoadAndDelete(key); loaded {
					entry.undo()
				}
			}
		}
		return true
	})
	m.placeholders.Range(func(key, value any) bool {
		if entry, ok := value.(placeholderEntry); ok {
			if now.Sub(entry.createdAt) > placeholderTTL {
				m.placeholders.Delete(key)
			}
		}
		return true
	})
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

	if wExists && w != nil {
		select {
		case w.queue <- msg:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Fallback: direct send (should not happen)
	channel, _ := m.channels[channelName]
	return channel.Send(ctx, msg)
}
