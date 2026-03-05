package tools

import (
	"context"
	"sync/atomic"
)

// MessageRoundTracker tracks whether the message tool sent a message during the
// current inbound message processing round.
//
// It is stored on context so concurrent sessions do not share mutable state.
type MessageRoundTracker struct {
	sent atomic.Bool
}

func (t *MessageRoundTracker) MarkSent() {
	if t == nil {
		return
	}
	t.sent.Store(true)
}

func (t *MessageRoundTracker) Sent() bool {
	if t == nil {
		return false
	}
	return t.sent.Load()
}

type messageRoundTrackerKey struct{}

// WithMessageRoundTracker attaches a tracker to context for the message tool to
// mark when it sends an outbound message.
func WithMessageRoundTracker(ctx context.Context, tracker *MessageRoundTracker) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if tracker == nil {
		return ctx
	}
	return context.WithValue(ctx, messageRoundTrackerKey{}, tracker)
}

func messageRoundTrackerFromContext(ctx context.Context) *MessageRoundTracker {
	if ctx == nil {
		return nil
	}
	tracker, _ := ctx.Value(messageRoundTrackerKey{}).(*MessageRoundTracker)
	return tracker
}
