package whatsapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/channels"
	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/identity"
)

func TestWhatsAppChannel_Send_NotRunning(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewWhatsAppChannel(config.WhatsAppConfig{}, mb)
	if err != nil {
		t.Fatalf("NewWhatsAppChannel error: %v", err)
	}

	err = ch.Send(context.Background(), bus.OutboundMessage{ChatID: "u1", Content: "hi"})
	if !errors.Is(err, channels.ErrNotRunning) {
		t.Fatalf("Send error = %v, want ErrNotRunning", err)
	}
}

func TestWhatsAppChannel_Send_ContextCanceled(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewWhatsAppChannel(config.WhatsAppConfig{}, mb)
	if err != nil {
		t.Fatalf("NewWhatsAppChannel error: %v", err)
	}
	ch.SetRunning(true)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = ch.Send(ctx, bus.OutboundMessage{ChatID: "u1", Content: "hi"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Send error = %v, want context.Canceled", err)
	}
}

func TestWhatsAppChannel_Send_NoConnectionIsTemporary(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewWhatsAppChannel(config.WhatsAppConfig{}, mb)
	if err != nil {
		t.Fatalf("NewWhatsAppChannel error: %v", err)
	}
	ch.SetRunning(true)

	err = ch.Send(context.Background(), bus.OutboundMessage{ChatID: "u1", Content: "hi"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, channels.ErrTemporary) {
		t.Fatalf("Send error = %v, want ErrTemporary", err)
	}
}

func TestWhatsAppChannel_HandleIncomingMessage_PublishesInbound(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewWhatsAppChannel(config.WhatsAppConfig{}, mb)
	if err != nil {
		t.Fatalf("NewWhatsAppChannel error: %v", err)
	}
	ch.ctx = context.Background()

	ch.handleIncomingMessage(map[string]any{
		"type":      "message",
		"from":      "u1",
		"content":   "hello",
		"id":        "m1",
		"from_name": "Bob",
		"media":     []any{"a.jpg", 123, "b.png"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	msg, ok := mb.ConsumeInbound(ctx)
	if !ok {
		t.Fatal("expected inbound message, got none")
	}

	if msg.Channel != "whatsapp" {
		t.Fatalf("Channel = %q, want %q", msg.Channel, "whatsapp")
	}
	if msg.ChatID != "u1" {
		t.Fatalf("ChatID = %q, want %q", msg.ChatID, "u1")
	}
	if msg.Peer.Kind != "direct" {
		t.Fatalf("Peer.Kind = %q, want %q", msg.Peer.Kind, "direct")
	}
	if msg.MessageID != "m1" {
		t.Fatalf("MessageID = %q, want %q", msg.MessageID, "m1")
	}
	if msg.Metadata["user_name"] != "Bob" {
		t.Fatalf("Metadata[user_name] = %q, want %q", msg.Metadata["user_name"], "Bob")
	}
	if msg.Content != "hello" {
		t.Fatalf("Content = %q, want %q", msg.Content, "hello")
	}
	if len(msg.Media) != 2 || msg.Media[0] != "a.jpg" || msg.Media[1] != "b.png" {
		t.Fatalf("Media = %#v, want [a.jpg b.png]", msg.Media)
	}

	wantCanonical := identity.BuildCanonicalID("whatsapp", "u1")
	if msg.SenderID != wantCanonical {
		t.Fatalf("SenderID = %q, want %q", msg.SenderID, wantCanonical)
	}
	if msg.Sender.DisplayName != "Bob" {
		t.Fatalf("Sender.DisplayName = %q, want %q", msg.Sender.DisplayName, "Bob")
	}
}

func TestWhatsAppChannel_HandleIncomingMessage_GroupPeer(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewWhatsAppChannel(config.WhatsAppConfig{}, mb)
	if err != nil {
		t.Fatalf("NewWhatsAppChannel error: %v", err)
	}
	ch.ctx = context.Background()

	ch.handleIncomingMessage(map[string]any{
		"type":    "message",
		"from":    "u1",
		"chat":    "g1",
		"content": "hello",
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	msg, ok := mb.ConsumeInbound(ctx)
	if !ok {
		t.Fatal("expected inbound message, got none")
	}
	if msg.ChatID != "g1" {
		t.Fatalf("ChatID = %q, want %q", msg.ChatID, "g1")
	}
	if msg.Peer.Kind != "group" {
		t.Fatalf("Peer.Kind = %q, want %q", msg.Peer.Kind, "group")
	}
}

func TestWhatsAppChannel_HandleIncomingMessage_AllowFromBlocks(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewWhatsAppChannel(config.WhatsAppConfig{
		AllowFrom: config.FlexibleStringSlice{"whatsapp:someone-else"},
	}, mb)
	if err != nil {
		t.Fatalf("NewWhatsAppChannel error: %v", err)
	}
	ch.ctx = context.Background()

	ch.handleIncomingMessage(map[string]any{
		"type":    "message",
		"from":    "u1",
		"content": "hello",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if msg, ok := mb.ConsumeInbound(ctx); ok {
		t.Fatalf("unexpected inbound message: %+v", msg)
	}
}
