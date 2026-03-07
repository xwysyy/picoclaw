package channels

import (
	"context"
	"fmt"
	"strings"
	"time"
)

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

// RecordPlaceholder registers a placeholder message for later editing.
// Implements PlaceholderRecorder.
func (m *Manager) RecordPlaceholder(channel, chatID, placeholderID string) {
	key := channel + ":" + chatID
	m.placeholders.Store(key, placeholderEntry{id: placeholderID, createdAt: time.Now()})
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
		sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
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
	scheduleCtx, cancel := context.WithCancel(ctx)
	m.scheduled.Store(key, scheduledPlaceholderEntry{
		token:     token,
		timer:     timer,
		cancel:    cancel,
		createdAt: time.Now(),
	})

	go func() {
		defer func() {
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
		case <-scheduleCtx.Done():
			return
		}

		if scheduleCtx.Err() != nil {
			return
		}

		sendCtx, sendCancel := context.WithTimeout(scheduleCtx, 30*time.Second)
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
