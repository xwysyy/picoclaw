package channels

import (
	"context"
	"testing"

	"golang.org/x/time/rate"

	"github.com/xwysyy/X-Claw/pkg/bus"
)

type mockMessageIDSender struct {
	mockChannel
	sendWithIDFn func(ctx context.Context, msg bus.OutboundMessage) (string, error)
}

func (m *mockMessageIDSender) SendWithMessageID(ctx context.Context, msg bus.OutboundMessage) (string, error) {
	return m.sendWithIDFn(ctx, msg)
}

func TestSendWithRetry_BindsReplyContextOnSend(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	m := &Manager{bus: mb}
	w := &channelWorker{
		ch: &mockMessageIDSender{
			mockChannel: mockChannel{sendFn: func(context.Context, bus.OutboundMessage) error { return nil }},
			sendWithIDFn: func(_ context.Context, msg bus.OutboundMessage) (string, error) {
				return "msg-42", nil
			},
		},
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	m.sendWithRetry(context.Background(), "test", w, bus.OutboundMessage{
		Channel:    "test",
		ChatID:     "chat-1",
		Content:    "hello",
		SessionKey: "conv:test:chat-1",
	})

	got, ok := mb.LookupReplyContext("test", "chat-1", "msg-42")
	if !ok {
		t.Fatal("expected reply binding after send")
	}
	if got.SessionKey != "conv:test:chat-1" {
		t.Fatalf("expected session key to be bound, got %q", got.SessionKey)
	}
}

func TestPreSend_PlaceholderEditBindsReplyContext(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	m := &Manager{bus: mb}
	ch := &mockMessageEditor{
		mockChannel: mockChannel{sendFn: func(context.Context, bus.OutboundMessage) error { return nil }},
		editFn:      func(context.Context, string, string, string) error { return nil },
	}

	m.RecordPlaceholder("test", "123", "ph-1")
	edited := m.preSend(context.Background(), "test", bus.OutboundMessage{
		Channel:    "test",
		ChatID:     "123",
		Content:    "hello",
		SessionKey: "conv:test:123",
	}, ch)
	if !edited {
		t.Fatal("expected placeholder edit path")
	}

	got, ok := mb.LookupReplyContext("test", "123", "ph-1")
	if !ok {
		t.Fatal("expected reply binding for edited placeholder")
	}
	if got.SessionKey != "conv:test:123" {
		t.Fatalf("expected session key to be bound, got %q", got.SessionKey)
	}
}
