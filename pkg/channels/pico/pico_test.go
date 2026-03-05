package pico

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/xwysyy/picoclaw/pkg/bus"
	"github.com/xwysyy/picoclaw/pkg/channels"
	"github.com/xwysyy/picoclaw/pkg/config"
)

func TestNewPicoChannel_RequiresToken(t *testing.T) {
	t.Parallel()

	_, err := NewPicoChannel(config.PicoConfig{}, bus.NewMessageBus())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestPicoChannel_Authenticate(t *testing.T) {
	t.Parallel()

	ch, err := NewPicoChannel(config.PicoConfig{Token: config.SecretRef{Inline: "t"}}, bus.NewMessageBus())
	if err != nil {
		t.Fatalf("NewPicoChannel error: %v", err)
	}

	r := httptest.NewRequest("GET", "http://example.test/pico/ws", nil)
	r.Header.Set("Authorization", "Bearer t")
	if !ch.authenticate(r) {
		t.Fatalf("authenticate(Authorization) = false, want true")
	}

	r = httptest.NewRequest("GET", "http://example.test/pico/ws?token=t", nil)
	if ch.authenticate(r) {
		t.Fatalf("authenticate(query) = true with AllowTokenQuery=false, want false")
	}

	ch.config.AllowTokenQuery = true
	r = httptest.NewRequest("GET", "http://example.test/pico/ws?token=t", nil)
	if !ch.authenticate(r) {
		t.Fatalf("authenticate(query) = false with AllowTokenQuery=true, want true")
	}
}

func TestPicoChannel_CheckOrigin(t *testing.T) {
	t.Parallel()

	t.Run("allow_all_when_not_configured", func(t *testing.T) {
		ch, err := NewPicoChannel(config.PicoConfig{Token: config.SecretRef{Inline: "t"}}, bus.NewMessageBus())
		if err != nil {
			t.Fatalf("NewPicoChannel error: %v", err)
		}

		r := httptest.NewRequest("GET", "http://example.test/pico/ws", nil)
		r.Header.Set("Origin", "https://any.example")
		if !ch.upgrader.CheckOrigin(r) {
			t.Fatalf("CheckOrigin = false, want true")
		}
	})

	t.Run("allow_specific_origin", func(t *testing.T) {
		ch, err := NewPicoChannel(config.PicoConfig{
			Token:        config.SecretRef{Inline: "t"},
			AllowOrigins: []string{"https://ok.example"},
		}, bus.NewMessageBus())
		if err != nil {
			t.Fatalf("NewPicoChannel error: %v", err)
		}

		okReq := httptest.NewRequest("GET", "http://example.test/pico/ws", nil)
		okReq.Header.Set("Origin", "https://ok.example")
		if !ch.upgrader.CheckOrigin(okReq) {
			t.Fatalf("CheckOrigin(ok) = false, want true")
		}

		badReq := httptest.NewRequest("GET", "http://example.test/pico/ws", nil)
		badReq.Header.Set("Origin", "https://bad.example")
		if ch.upgrader.CheckOrigin(badReq) {
			t.Fatalf("CheckOrigin(bad) = true, want false")
		}
	})

	t.Run("wildcard", func(t *testing.T) {
		ch, err := NewPicoChannel(config.PicoConfig{
			Token:        config.SecretRef{Inline: "t"},
			AllowOrigins: []string{"*"},
		}, bus.NewMessageBus())
		if err != nil {
			t.Fatalf("NewPicoChannel error: %v", err)
		}

		r := httptest.NewRequest("GET", "http://example.test/pico/ws", nil)
		r.Header.Set("Origin", "https://any.example")
		if !ch.upgrader.CheckOrigin(r) {
			t.Fatalf("CheckOrigin(*) = false, want true")
		}
	})
}

func TestPicoChannel_BroadcastToSession_NoConnections(t *testing.T) {
	t.Parallel()

	ch, err := NewPicoChannel(config.PicoConfig{Token: config.SecretRef{Inline: "t"}}, bus.NewMessageBus())
	if err != nil {
		t.Fatalf("NewPicoChannel error: %v", err)
	}
	ch.SetRunning(true)

	err = ch.broadcastToSession("pico:sess-1", newMessage(TypeMessageCreate, map[string]any{"content": "hi"}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, channels.ErrSendFailed) {
		t.Fatalf("error = %v, want ErrSendFailed", err)
	}
}

func TestPicoChannel_Send_NotRunning(t *testing.T) {
	t.Parallel()

	ch, err := NewPicoChannel(config.PicoConfig{Token: config.SecretRef{Inline: "t"}}, bus.NewMessageBus())
	if err != nil {
		t.Fatalf("NewPicoChannel error: %v", err)
	}

	err = ch.Send(context.Background(), bus.OutboundMessage{ChatID: "pico:sess-1", Content: "hi"})
	if !errors.Is(err, channels.ErrNotRunning) {
		t.Fatalf("Send error = %v, want ErrNotRunning", err)
	}
}
