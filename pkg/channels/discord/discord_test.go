package discord

import (
	"context"
	"errors"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestDiscordChannel_Send_NotRunning(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewDiscordChannel(config.DiscordConfig{Token: "test-token"}, mb)
	if err != nil {
		t.Fatalf("NewDiscordChannel error: %v", err)
	}

	err = ch.Send(context.Background(), bus.OutboundMessage{ChatID: "123", Content: "hi"})
	if !errors.Is(err, channels.ErrNotRunning) {
		t.Fatalf("Send error = %v, want ErrNotRunning", err)
	}
}

func TestDiscordChannel_Send_EmptyChannelID(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewDiscordChannel(config.DiscordConfig{Token: "test-token"}, mb)
	if err != nil {
		t.Fatalf("NewDiscordChannel error: %v", err)
	}
	ch.SetRunning(true)

	err = ch.Send(context.Background(), bus.OutboundMessage{ChatID: "", Content: "hi"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDiscordChannel_Send_EmptyContentIsNoop(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewDiscordChannel(config.DiscordConfig{Token: "test-token"}, mb)
	if err != nil {
		t.Fatalf("NewDiscordChannel error: %v", err)
	}
	ch.SetRunning(true)

	if err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "123", Content: ""}); err != nil {
		t.Fatalf("Send error = %v, want nil", err)
	}
}

func TestDiscordChannel_SendPlaceholder_DisabledIsNoop(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewDiscordChannel(config.DiscordConfig{
		Token:       "test-token",
		Placeholder: config.PlaceholderConfig{Enabled: false},
	}, mb)
	if err != nil {
		t.Fatalf("NewDiscordChannel error: %v", err)
	}

	id, err := ch.SendPlaceholder(context.Background(), "123")
	if err != nil {
		t.Fatalf("SendPlaceholder error = %v, want nil", err)
	}
	if id != "" {
		t.Fatalf("SendPlaceholder id = %q, want empty", id)
	}
}

func TestDiscordChannel_StripBotMention(t *testing.T) {
	t.Parallel()

	ch := &DiscordChannel{botUserID: "42"}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "no_mention", in: "hello", want: "hello"},
		{name: "regular_mention", in: "<@42> hello", want: "hello"},
		{name: "nickname_mention", in: "<@!42> hello", want: "hello"},
		{name: "multiple_mentions", in: "<@42> <@!42>  hello  ", want: "hello"},
		{name: "other_user_untouched", in: "<@7> hi <@42>", want: "<@7> hi"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ch.stripBotMention(tt.in); got != tt.want {
				t.Fatalf("stripBotMention(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestAppendContent(t *testing.T) {
	t.Parallel()

	if got := appendContent("", "x"); got != "x" {
		t.Fatalf("appendContent(empty, x) = %q, want %q", got, "x")
	}
	if got := appendContent("a", "b"); got != "a\nb" {
		t.Fatalf("appendContent(a, b) = %q, want %q", got, "a\nb")
	}
}
