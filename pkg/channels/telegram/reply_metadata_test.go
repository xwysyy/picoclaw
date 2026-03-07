package telegram

import (
	"testing"

	"github.com/mymmrac/telego"
)

func TestBuildTelegramInboundMetadata_IncludesReplyAndThreadIDs(t *testing.T) {
	msg := telego.Message{
		Chat:            telego.Chat{Type: "supergroup"},
		MessageThreadID: 42,
		ReplyToMessage:  &telego.Message{MessageID: 7},
	}
	user := telego.User{ID: 123, Username: "alice", FirstName: "Alice"}

	metadata := buildTelegramInboundMetadata(&msg, &user)
	if metadata["thread_id"] != "42" {
		t.Fatalf("expected thread_id=42, got %q", metadata["thread_id"])
	}
	if metadata["reply_to_message_id"] != "7" {
		t.Fatalf("expected reply_to_message_id=7, got %q", metadata["reply_to_message_id"])
	}
	if metadata["user_id"] != "123" {
		t.Fatalf("expected user_id=123, got %q", metadata["user_id"])
	}
}
