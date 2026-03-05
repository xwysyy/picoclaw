package discord

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/xwysyy/picoclaw/pkg/bus"
	"github.com/xwysyy/picoclaw/pkg/channels"
	"github.com/xwysyy/picoclaw/pkg/config"
)

func TestDiscordChannel_Send_NotRunning(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewDiscordChannel(config.DiscordConfig{Token: config.SecretRef{Inline: "test-token"}}, mb)
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
	ch, err := NewDiscordChannel(config.DiscordConfig{Token: config.SecretRef{Inline: "test-token"}}, mb)
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
	ch, err := NewDiscordChannel(config.DiscordConfig{Token: config.SecretRef{Inline: "test-token"}}, mb)
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
		Token:       config.SecretRef{Inline: "test-token"},
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

func TestApplyDiscordProxy_CustomProxy(t *testing.T) {
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("discordgo.New() error: %v", err)
	}

	if err = applyDiscordProxy(session, "http://127.0.0.1:7890"); err != nil {
		t.Fatalf("applyDiscordProxy() error: %v", err)
	}

	req, err := http.NewRequest("GET", "https://discord.com/api/v10/gateway", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error: %v", err)
	}

	restProxy := session.Client.Transport.(*http.Transport).Proxy
	restProxyURL, err := restProxy(req)
	if err != nil {
		t.Fatalf("rest proxy func error: %v", err)
	}
	if got, want := restProxyURL.String(), "http://127.0.0.1:7890"; got != want {
		t.Fatalf("REST proxy = %q, want %q", got, want)
	}

	wsProxyURL, err := session.Dialer.Proxy(req)
	if err != nil {
		t.Fatalf("ws proxy func error: %v", err)
	}
	if got, want := wsProxyURL.String(), "http://127.0.0.1:7890"; got != want {
		t.Fatalf("WS proxy = %q, want %q", got, want)
	}
}

func TestApplyDiscordProxy_FromEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:8888")
	t.Setenv("http_proxy", "http://127.0.0.1:8888")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:8888")
	t.Setenv("https_proxy", "http://127.0.0.1:8888")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("discordgo.New() error: %v", err)
	}

	if err = applyDiscordProxy(session, ""); err != nil {
		t.Fatalf("applyDiscordProxy() error: %v", err)
	}

	req, err := http.NewRequest("GET", "https://discord.com/api/v10/gateway", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error: %v", err)
	}

	gotURL, err := session.Dialer.Proxy(req)
	if err != nil {
		t.Fatalf("ws proxy func error: %v", err)
	}

	wantURL, err := url.Parse("http://127.0.0.1:8888")
	if err != nil {
		t.Fatalf("url.Parse() error: %v", err)
	}
	if gotURL.String() != wantURL.String() {
		t.Fatalf("WS proxy = %q, want %q", gotURL.String(), wantURL.String())
	}
}

func TestApplyDiscordProxy_InvalidProxyURL(t *testing.T) {
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("discordgo.New() error: %v", err)
	}

	if err = applyDiscordProxy(session, "://bad-proxy"); err == nil {
		t.Fatal("applyDiscordProxy() expected error for invalid proxy URL, got nil")
	}
}
