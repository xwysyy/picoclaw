package channels

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/constants"
	"github.com/xwysyy/X-Claw/pkg/logger"
)

// channelRateConfig maps channel name to per-second rate limit.
var channelRateConfig = map[string]float64{
	"telegram": 20,
	"discord":  1,
	"slack":    1,
	"line":     10,
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

func enqueueOutboundMessage(ctx context.Context, channel string, w *channelWorker, msg bus.OutboundMessage) (sent bool, stopped bool) {
	defer func() {
		if r := recover(); r != nil {
			logger.WarnCF("channels", "Channel worker queue closed, skipping message", map[string]any{
				"channel": channel,
			})
			sent = false
			stopped = false
		}
	}()

	select {
	case w.queue <- msg:
		return true, false
	case <-ctx.Done():
		return false, true
	}
}

func (m *Manager) bindReplyContext(channel, chatID, messageID, sessionKey string) {
	if m == nil || m.bus == nil {
		return
	}
	messageID = strings.TrimSpace(messageID)
	sessionKey = strings.TrimSpace(sessionKey)
	if messageID == "" || sessionKey == "" {
		return
	}
	m.bus.BindReplyContext(channel, chatID, messageID, bus.ReplyContext{SessionKey: sessionKey})
}

func enqueueOutboundMediaMessage(ctx context.Context, channel string, w *channelWorker, msg bus.OutboundMediaMessage) (sent bool, stopped bool) {
	defer func() {
		if r := recover(); r != nil {
			logger.WarnCF("channels", "Channel media worker queue closed, skipping media message", map[string]any{
				"channel": channel,
			})
			sent = false
			stopped = false
		}
	}()

	select {
	case w.mediaQueue <- msg:
		return true, false
	case <-ctx.Done():
		return false, true
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

		if errors.Is(lastErr, ErrNotRunning) || errors.Is(lastErr, ErrSendFailed) {
			break
		}
		if attempt == maxRetries {
			break
		}
		if errors.Is(lastErr, ErrRateLimit) {
			select {
			case <-time.After(rateLimitDelay):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

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
	if err := w.limiter.Wait(ctx); err != nil {
		return
	}
	if m.preSend(ctx, name, msg, w.ch) {
		return
	}

	sentMessageID := ""
	err := m.retryWithBackoff(ctx, w, func() error {
		if sender, ok := w.ch.(MessageIDSender); ok {
			messageID, err := sender.SendWithMessageID(ctx, msg)
			if err != nil {
				return err
			}
			sentMessageID = strings.TrimSpace(messageID)
			return nil
		}
		return w.ch.Send(ctx, msg)
	})
	if err == nil {
		m.bindReplyContext(name, msg.ChatID, sentMessageID, msg.SessionKey)
		return
	}
	if ctx.Err() == nil {
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
			_, stopped := enqueueOutboundMessage(ctx, msg.Channel, w, msg)
			return !stopped
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
			_, stopped := enqueueOutboundMediaMessage(ctx, msg.Channel, w, msg)
			return !stopped
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
