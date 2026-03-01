package dingtalk

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/identity"
)

func TestNewDingTalkChannel_RequiresCredentials(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()

	_, err := NewDingTalkChannel(config.DingTalkConfig{}, mb)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDingTalkChannel_Send_NotRunning(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewDingTalkChannel(config.DingTalkConfig{
		ClientID:     "id",
		ClientSecret: "secret",
	}, mb)
	if err != nil {
		t.Fatalf("NewDingTalkChannel error: %v", err)
	}

	err = ch.Send(context.Background(), bus.OutboundMessage{ChatID: "chat1", Content: "hi"})
	if !errors.Is(err, channels.ErrNotRunning) {
		t.Fatalf("Send error = %v, want ErrNotRunning", err)
	}
}

func TestDingTalkChannel_Send_MissingWebhook(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewDingTalkChannel(config.DingTalkConfig{
		ClientID:     "id",
		ClientSecret: "secret",
	}, mb)
	if err != nil {
		t.Fatalf("NewDingTalkChannel error: %v", err)
	}
	ch.SetRunning(true)

	err = ch.Send(context.Background(), bus.OutboundMessage{ChatID: "chat1", Content: "hi"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDingTalkChannel_Send_InvalidWebhookType(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewDingTalkChannel(config.DingTalkConfig{
		ClientID:     "id",
		ClientSecret: "secret",
	}, mb)
	if err != nil {
		t.Fatalf("NewDingTalkChannel error: %v", err)
	}
	ch.SetRunning(true)

	ch.sessionWebhooks.Store("chat1", 123)
	err = ch.Send(context.Background(), bus.OutboundMessage{ChatID: "chat1", Content: "hi"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDingTalkChannel_OnChatBotMessageReceived_EmptyContentIgnored(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewDingTalkChannel(config.DingTalkConfig{
		ClientID:     "id",
		ClientSecret: "secret",
	}, mb)
	if err != nil {
		t.Fatalf("NewDingTalkChannel error: %v", err)
	}

	_, err = ch.onChatBotMessageReceived(context.Background(), &chatbot.BotCallbackDataModel{})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if msg, ok := mb.ConsumeInbound(ctx); ok {
		t.Fatalf("unexpected inbound message: %+v", msg)
	}
}

func TestDingTalkChannel_OnChatBotMessageReceived_DirectPublishesAndStoresWebhook(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewDingTalkChannel(config.DingTalkConfig{
		ClientID:     "id",
		ClientSecret: "secret",
	}, mb)
	if err != nil {
		t.Fatalf("NewDingTalkChannel error: %v", err)
	}

	data := &chatbot.BotCallbackDataModel{
		Text:             chatbot.BotCallbackDataTextModel{Content: "hello"},
		SenderStaffId:    "staff-1",
		SenderNick:       "Alice",
		ConversationType: "1",
		ConversationId:   "conv-ignored",
		SessionWebhook:   "https://example.invalid/webhook",
	}

	_, err = ch.onChatBotMessageReceived(context.Background(), data)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	// Webhook should be stored under chatID = senderID for 1:1 chats.
	if v, ok := ch.sessionWebhooks.Load("staff-1"); !ok {
		t.Fatalf("session webhook not stored")
	} else if got, ok := v.(string); !ok || got != data.SessionWebhook {
		t.Fatalf("stored webhook = %#v, want %q", v, data.SessionWebhook)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	msg, ok := mb.ConsumeInbound(ctx)
	if !ok {
		t.Fatal("expected inbound message, got none")
	}

	if msg.Channel != "dingtalk" {
		t.Fatalf("Channel = %q, want %q", msg.Channel, "dingtalk")
	}
	if msg.ChatID != "staff-1" {
		t.Fatalf("ChatID = %q, want %q", msg.ChatID, "staff-1")
	}
	if msg.Peer.Kind != "direct" {
		t.Fatalf("Peer.Kind = %q, want %q", msg.Peer.Kind, "direct")
	}
	if msg.Content != "hello" {
		t.Fatalf("Content = %q, want %q", msg.Content, "hello")
	}

	wantCanonical := identity.BuildCanonicalID("dingtalk", "staff-1")
	if msg.SenderID != wantCanonical {
		t.Fatalf("SenderID = %q, want %q", msg.SenderID, wantCanonical)
	}
	if msg.Sender.CanonicalID != wantCanonical {
		t.Fatalf("Sender.CanonicalID = %q, want %q", msg.Sender.CanonicalID, wantCanonical)
	}
	if got := msg.Metadata["session_webhook"]; got != data.SessionWebhook {
		t.Fatalf("Metadata[session_webhook] = %q, want %q", got, data.SessionWebhook)
	}
}

func TestDingTalkChannel_OnChatBotMessageReceived_GroupTriggerMentionOnlyIgnores(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewDingTalkChannel(config.DingTalkConfig{
		ClientID:     "id",
		ClientSecret: "secret",
		GroupTrigger: config.GroupTriggerConfig{MentionOnly: true},
	}, mb)
	if err != nil {
		t.Fatalf("NewDingTalkChannel error: %v", err)
	}

	data := &chatbot.BotCallbackDataModel{
		Text:             chatbot.BotCallbackDataTextModel{Content: "hello"},
		SenderStaffId:    "staff-1",
		SenderNick:       "Alice",
		ConversationType: "2",
		ConversationId:   "group-1",
		SessionWebhook:   "https://example.invalid/webhook",
	}

	_, err = ch.onChatBotMessageReceived(context.Background(), data)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if msg, ok := mb.ConsumeInbound(ctx); ok {
		t.Fatalf("unexpected inbound message: %+v", msg)
	}
}

func TestDingTalkChannel_OnChatBotMessageReceived_GroupTriggerPrefixStripsPrefix(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewDingTalkChannel(config.DingTalkConfig{
		ClientID:     "id",
		ClientSecret: "secret",
		GroupTrigger: config.GroupTriggerConfig{Prefixes: []string{"!"}},
	}, mb)
	if err != nil {
		t.Fatalf("NewDingTalkChannel error: %v", err)
	}

	data := &chatbot.BotCallbackDataModel{
		Text:             chatbot.BotCallbackDataTextModel{Content: "!  ping"},
		SenderStaffId:    "staff-1",
		SenderNick:       "Alice",
		ConversationType: "2",
		ConversationId:   "group-1",
		SessionWebhook:   "https://example.invalid/webhook",
	}

	_, err = ch.onChatBotMessageReceived(context.Background(), data)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	msg, ok := mb.ConsumeInbound(ctx)
	if !ok {
		t.Fatal("expected inbound message, got none")
	}
	if msg.ChatID != "group-1" {
		t.Fatalf("ChatID = %q, want %q", msg.ChatID, "group-1")
	}
	if msg.Peer.Kind != "group" {
		t.Fatalf("Peer.Kind = %q, want %q", msg.Peer.Kind, "group")
	}
	if msg.Content != "ping" {
		t.Fatalf("Content = %q, want %q", msg.Content, "ping")
	}
}

func TestDingTalkChannel_OnChatBotMessageReceived_AllowFromBlocks(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewDingTalkChannel(config.DingTalkConfig{
		ClientID:     "id",
		ClientSecret: "secret",
		AllowFrom:    config.FlexibleStringSlice{"dingtalk:someone-else"},
	}, mb)
	if err != nil {
		t.Fatalf("NewDingTalkChannel error: %v", err)
	}

	data := &chatbot.BotCallbackDataModel{
		Text:             chatbot.BotCallbackDataTextModel{Content: "hello"},
		SenderStaffId:    "staff-1",
		SenderNick:       "Alice",
		ConversationType: "1",
		SessionWebhook:   "https://example.invalid/webhook",
	}

	_, err = ch.onChatBotMessageReceived(context.Background(), data)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if msg, ok := mb.ConsumeInbound(ctx); ok {
		t.Fatalf("unexpected inbound message: %+v", msg)
	}
}
