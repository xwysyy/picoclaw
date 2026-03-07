package channels

import (
	"context"
	"net/http"
	"time"

	"github.com/xwysyy/X-Claw/pkg/bus"
)

type Channel interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Send(ctx context.Context, msg bus.OutboundMessage) error
	IsRunning() bool
	IsAllowed(senderID string) bool
	IsAllowedSender(sender bus.SenderInfo) bool
	ReasoningChannelID() string
}

// MessageLengthProvider is an opt-in interface that channels implement
// to advertise their maximum message length. The Manager uses this via
// type assertion to decide whether to split outbound messages.
type MessageLengthProvider interface {
	MaxMessageLength() int
}

// TypingCapable — channels that can show a typing/thinking indicator.
// StartTyping begins the indicator and returns a stop function.
// The stop function MUST be idempotent and safe to call multiple times.
type TypingCapable interface {
	StartTyping(ctx context.Context, chatID string) (stop func(), err error)
}

// MessageEditor — channels that can edit an existing message.
// messageID is always string; channels convert platform-specific types internally.
type MessageEditor interface {
	EditMessage(ctx context.Context, chatID string, messageID string, content string) error
}

// ReactionCapable — channels that can add a reaction (e.g. 👀) to an inbound message.
// ReactToMessage adds a reaction and returns an undo function to remove it.
// The undo function MUST be idempotent and safe to call multiple times.
type ReactionCapable interface {
	ReactToMessage(ctx context.Context, chatID, messageID string) (undo func(), err error)
}

// PlaceholderCapable — channels that can send a placeholder message.
type PlaceholderCapable interface {
	SendPlaceholder(ctx context.Context, chatID string) (messageID string, err error)
}

// PlaceholderRecorder is injected into channels by Manager.
type PlaceholderRecorder interface {
	RecordPlaceholder(channel, chatID, placeholderID string)
	RecordTypingStop(channel, chatID string, stop func())
	RecordReactionUndo(channel, chatID string, undo func())
}

// PlaceholderScheduler is an optional extension interface implemented by Manager.
type PlaceholderScheduler interface {
	SchedulePlaceholder(ctx context.Context, channel, chatID string, send func(context.Context) (string, error), delay time.Duration)
	CancelPlaceholder(channel, chatID string)
}

// WebhookHandler is an optional interface for channels that receive messages via HTTP webhooks.
type WebhookHandler interface {
	WebhookPath() string
	http.Handler
}

// HealthChecker is an optional interface for channels that expose a health check endpoint.
type HealthChecker interface {
	HealthPath() string
	HealthHandler(w http.ResponseWriter, r *http.Request)
}
