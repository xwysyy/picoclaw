package onebot

import (
	"encoding/json"
	"testing"

	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/config"
)

func TestIsAPIResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  json.RawMessage
		want bool
	}{
		{name: "empty", raw: nil, want: false},
		{name: "status_ok", raw: json.RawMessage(`"ok"`), want: true},
		{name: "status_failed", raw: json.RawMessage(`"failed"`), want: true},
		{name: "status_other", raw: json.RawMessage(`"noop"`), want: false},
		{name: "bot_status_online", raw: json.RawMessage(`{"online":true}`), want: true},
		{name: "bot_status_good", raw: json.RawMessage(`{"good":true}`), want: true},
		{name: "bot_status_false", raw: json.RawMessage(`{"online":false,"good":false}`), want: false},
		{name: "invalid_json", raw: json.RawMessage(`{`), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAPIResponse(tt.raw); got != tt.want {
				t.Fatalf("isAPIResponse(%s) = %v, want %v", string(tt.raw), got, tt.want)
			}
		})
	}
}

func TestParseJSONInt64(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     json.RawMessage
		want    int64
		wantErr bool
	}{
		{name: "empty", raw: nil, want: 0, wantErr: false},
		{name: "number", raw: json.RawMessage(`123`), want: 123, wantErr: false},
		{name: "string_number", raw: json.RawMessage(`"456"`), want: 456, wantErr: false},
		{name: "string_invalid", raw: json.RawMessage(`"x"`), want: 0, wantErr: true},
		{name: "object", raw: json.RawMessage(`{"n":1}`), want: 0, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseJSONInt64(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseJSONInt64(%s) err=%v, wantErr=%v", string(tt.raw), err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("parseJSONInt64(%s) = %d, want %d", string(tt.raw), got, tt.want)
			}
		})
	}
}

func TestParseJSONString(t *testing.T) {
	t.Parallel()

	if got := parseJSONString(nil); got != "" {
		t.Fatalf("parseJSONString(nil) = %q, want empty", got)
	}
	if got := parseJSONString(json.RawMessage(`"abc"`)); got != "abc" {
		t.Fatalf("parseJSONString(\"abc\") = %q, want %q", got, "abc")
	}
	// Non-string JSON falls back to the raw bytes.
	if got := parseJSONString(json.RawMessage(`123`)); got != "123" {
		t.Fatalf("parseJSONString(123) = %q, want %q", got, "123")
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()

	if got := truncate("hello", 10); got != "hello" {
		t.Fatalf("truncate(short) = %q, want %q", got, "hello")
	}
	// Rune-counting behavior (not byte-counting).
	if got := truncate("你好世界", 2); got != "你好..." {
		t.Fatalf("truncate(unicode) = %q, want %q", got, "你好...")
	}
}

func TestParseMessageSegments_StringWithCQAt(t *testing.T) {
	t.Parallel()

	ch, err := NewOneBotChannel(config.OneBotConfig{}, bus.NewMessageBus())
	if err != nil {
		t.Fatalf("NewOneBotChannel error: %v", err)
	}

	raw := json.RawMessage(`"[CQ:at,qq=123] hi"`)
	got := ch.parseMessageSegments(raw, 123, nil, "")

	if got.IsBotMentioned != true {
		t.Fatalf("IsBotMentioned = %v, want true", got.IsBotMentioned)
	}
	if got.Text != "hi" {
		t.Fatalf("Text = %q, want %q", got.Text, "hi")
	}
}

func TestParseMessageSegments_Segments(t *testing.T) {
	t.Parallel()

	ch, err := NewOneBotChannel(config.OneBotConfig{}, bus.NewMessageBus())
	if err != nil {
		t.Fatalf("NewOneBotChannel error: %v", err)
	}

	raw := json.RawMessage(`[
		{"type":"at","data":{"qq":"123"}},
		{"type":"text","data":{"text":" hello"}},
		{"type":"reply","data":{"id":"999"}},
		{"type":"face","data":{"id":12}}
	]`)

	got := ch.parseMessageSegments(raw, 123, nil, "")

	if got.IsBotMentioned != true {
		t.Fatalf("IsBotMentioned = %v, want true", got.IsBotMentioned)
	}
	if got.Text != "hello[face:12]" {
		t.Fatalf("Text = %q, want %q", got.Text, "hello[face:12]")
	}
	if got.ReplyTo != "999" {
		t.Fatalf("ReplyTo = %q, want %q", got.ReplyTo, "999")
	}
}

func TestOneBotChannel_IsDuplicate_WithEviction(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewOneBotChannel(config.OneBotConfig{}, mb)
	if err != nil {
		t.Fatalf("NewOneBotChannel error: %v", err)
	}

	// Force a small ring to make eviction testable without 1024 inserts.
	ch.dedup = make(map[string]struct{}, 2)
	ch.dedupRing = make([]string, 2)
	ch.dedupIdx = 0

	if ch.isDuplicate("") {
		t.Fatalf("empty messageID should not be duplicate")
	}
	if ch.isDuplicate("0") {
		t.Fatalf("messageID '0' should not be duplicate")
	}

	if ch.isDuplicate("a") {
		t.Fatalf("first a should not be duplicate")
	}
	if !ch.isDuplicate("a") {
		t.Fatalf("second a should be duplicate")
	}

	// Fill ring and evict "a".
	_ = ch.isDuplicate("b")
	_ = ch.isDuplicate("c")

	// "a" should have been evicted (ring size is 2).
	if ch.isDuplicate("a") {
		t.Fatalf("a should have been evicted and treated as non-duplicate")
	}
}
