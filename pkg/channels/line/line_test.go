package line

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLINEChannel_VerifySignature(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewLINEChannel(config.LINEConfig{
		ChannelSecret:      "secret",
		ChannelAccessToken: "token",
	}, mb)
	if err != nil {
		t.Fatalf("NewLINEChannel error: %v", err)
	}

	body := []byte(`{"hello":"world"}`)

	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write(body)
	wantSig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if got := ch.verifySignature(body, wantSig); !got {
		t.Fatalf("verifySignature(valid) = false, want true")
	}
	if got := ch.verifySignature(body, "bad"); got {
		t.Fatalf("verifySignature(invalid) = true, want false")
	}
	if got := ch.verifySignature(body, ""); got {
		t.Fatalf("verifySignature(empty) = true, want false")
	}
}

func TestLINEChannel_IsBotMentioned(t *testing.T) {
	t.Parallel()

	ch := &LINEChannel{
		botUserID:      "bot-user",
		botDisplayName: "MyBot",
	}

	t.Run("mention_all", func(t *testing.T) {
		msg := lineMessage{
			Text: "@MyBot hi",
			Mention: &struct {
				Mentionees []lineMentionee `json:"mentionees"`
			}{Mentionees: []lineMentionee{{Type: "all"}}},
		}
		if !ch.isBotMentioned(msg) {
			t.Fatalf("isBotMentioned = false, want true")
		}
	})

	t.Run("mention_by_user_id", func(t *testing.T) {
		msg := lineMessage{
			Text: "@MyBot hi",
			Mention: &struct {
				Mentionees []lineMentionee `json:"mentionees"`
			}{Mentionees: []lineMentionee{{Type: "user", UserID: "bot-user"}}},
		}
		if !ch.isBotMentioned(msg) {
			t.Fatalf("isBotMentioned = false, want true")
		}
	})

	t.Run("mention_metadata_overlaps_display_name", func(t *testing.T) {
		msg := lineMessage{
			Text: "@MyBot hi",
			Mention: &struct {
				Mentionees []lineMentionee `json:"mentionees"`
			}{Mentionees: []lineMentionee{{Type: "user", UserID: "", Index: 0, Length: 6}}},
		}
		if !ch.isBotMentioned(msg) {
			t.Fatalf("isBotMentioned = false, want true")
		}
	})

	t.Run("fallback_text_based", func(t *testing.T) {
		msg := lineMessage{Text: "hello @MyBot"}
		if !ch.isBotMentioned(msg) {
			t.Fatalf("isBotMentioned = false, want true")
		}
	})

	t.Run("no_mention", func(t *testing.T) {
		msg := lineMessage{Text: "hello"}
		if ch.isBotMentioned(msg) {
			t.Fatalf("isBotMentioned = true, want false")
		}
	})
}

func TestLINEChannel_StripBotMention(t *testing.T) {
	t.Parallel()

	ch := &LINEChannel{
		botUserID:      "bot-user",
		botDisplayName: "MyBot",
	}

	t.Run("strip_by_user_id", func(t *testing.T) {
		msg := lineMessage{
			Text: "@MyBot hi",
			Mention: &struct {
				Mentionees []lineMentionee `json:"mentionees"`
			}{Mentionees: []lineMentionee{{Type: "user", UserID: "bot-user", Index: 0, Length: 6}}},
		}
		if got := ch.stripBotMention(msg.Text, msg); got != "hi" {
			t.Fatalf("stripBotMention = %q, want %q", got, "hi")
		}
	})

	t.Run("strip_by_display_name_overlap", func(t *testing.T) {
		msg := lineMessage{
			Text: "@MyBot hi",
			Mention: &struct {
				Mentionees []lineMentionee `json:"mentionees"`
			}{Mentionees: []lineMentionee{{Type: "user", UserID: "", Index: 0, Length: 6}}},
		}
		if got := ch.stripBotMention(msg.Text, msg); got != "hi" {
			t.Fatalf("stripBotMention = %q, want %q", got, "hi")
		}
	})

	t.Run("invalid_indices_falls_back_to_text_replace", func(t *testing.T) {
		msg := lineMessage{
			Text: "@MyBot hi",
			Mention: &struct {
				Mentionees []lineMentionee `json:"mentionees"`
			}{Mentionees: []lineMentionee{{Type: "user", UserID: "bot-user", Index: 999, Length: 10}}},
		}
		if got := ch.stripBotMention(msg.Text, msg); got != "hi" {
			t.Fatalf("stripBotMention = %q, want %q", got, "hi")
		}
	})
}

func TestLINEChannel_ResolveChatID(t *testing.T) {
	t.Parallel()

	ch := &LINEChannel{}

	if got := ch.resolveChatID(lineSource{Type: "group", GroupID: "G"}); got != "G" {
		t.Fatalf("group chatID = %q, want %q", got, "G")
	}
	if got := ch.resolveChatID(lineSource{Type: "room", RoomID: "R"}); got != "R" {
		t.Fatalf("room chatID = %q, want %q", got, "R")
	}
	if got := ch.resolveChatID(lineSource{Type: "user", UserID: "U"}); got != "U" {
		t.Fatalf("user chatID = %q, want %q", got, "U")
	}
}

func TestBuildTextMessage_QuoteTokenOptional(t *testing.T) {
	t.Parallel()

	m := buildTextMessage("hello", "")
	if m["type"] != "text" || m["text"] != "hello" {
		t.Fatalf("unexpected message map: %#v", m)
	}
	if _, ok := m["quoteToken"]; ok {
		t.Fatalf("expected quoteToken to be absent, got %#v", m)
	}

	m = buildTextMessage("hello", "qt")
	if got := m["quoteToken"]; got != "qt" {
		t.Fatalf("quoteToken = %q, want %q", got, "qt")
	}
}

func TestLINEChannel_StartTyping_GroupChatIsNoop(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewLINEChannel(config.LINEConfig{
		ChannelSecret:      "secret",
		ChannelAccessToken: "token",
	}, mb)
	if err != nil {
		t.Fatalf("NewLINEChannel error: %v", err)
	}

	// If StartTyping tries to call the API for group chats, this transport will panic.
	ch.apiClient.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		panic("unexpected API call: " + r.URL.String())
	})

	stop, err := ch.StartTyping(context.Background(), "C123")
	if err != nil {
		t.Fatalf("StartTyping error = %v, want nil", err)
	}
	if stop == nil {
		t.Fatalf("StartTyping stop = nil, want non-nil")
	}
	stop()
}

func TestLINEChannel_StartTyping_EmptyChatIDIsNoop(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewLINEChannel(config.LINEConfig{
		ChannelSecret:      "secret",
		ChannelAccessToken: "token",
	}, mb)
	if err != nil {
		t.Fatalf("NewLINEChannel error: %v", err)
	}

	ch.apiClient.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		panic("unexpected API call: " + r.URL.String())
	})

	stop, err := ch.StartTyping(context.Background(), "")
	if err != nil {
		t.Fatalf("StartTyping error = %v, want nil", err)
	}
	if stop == nil {
		t.Fatalf("StartTyping stop = nil, want non-nil")
	}
	stop()
}

func TestLINEChannel_CallAPI_MarshalError(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewLINEChannel(config.LINEConfig{
		ChannelSecret:      "secret",
		ChannelAccessToken: "token",
	}, mb)
	if err != nil {
		t.Fatalf("NewLINEChannel error: %v", err)
	}

	// json.Marshal fails on functions.
	badPayload := map[string]any{"x": func() {}}
	err = ch.callAPI(context.Background(), linePushEndpoint, badPayload)
	if err == nil {
		t.Fatalf("expected marshal error, got nil")
	}
}

func TestLINEChannel_CallAPI_Non200ClassifiesError(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewLINEChannel(config.LINEConfig{
		ChannelSecret:      "secret",
		ChannelAccessToken: "token",
	}, mb)
	if err != nil {
		t.Fatalf("NewLINEChannel error: %v", err)
	}

	ch.apiClient.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       ioNopCloser{r: bytes.NewReader([]byte("bad request"))},
			Header:     make(http.Header),
		}, nil
	})

	err = ch.callAPI(context.Background(), linePushEndpoint, map[string]any{"ok": true})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

// ioNopCloser is a minimal io.ReadCloser for http.Response bodies.
type ioNopCloser struct{ r *bytes.Reader }

func (c ioNopCloser) Read(p []byte) (int, error) { return c.r.Read(p) }
func (c ioNopCloser) Close() error               { return nil }
