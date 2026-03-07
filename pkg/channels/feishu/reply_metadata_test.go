package feishu

import (
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestBuildFeishuInboundMetadata_IncludesReplyIDs(t *testing.T) {
	msg := &larkim.EventMessage{
		MessageId:   stringPtr("om_child"),
		ParentId:    stringPtr("om_parent"),
		RootId:      stringPtr("om_root"),
		ThreadId:    stringPtr("thread_1"),
		MessageType: stringPtr("text"),
		ChatType:    stringPtr("group"),
	}
	sender := &larkim.EventSender{TenantKey: stringPtr("tenant-key")}

	metadata := buildFeishuInboundMetadata(msg, sender)
	if metadata["reply_to_message_id"] != "om_parent" {
		t.Fatalf("expected reply_to_message_id=om_parent, got %q", metadata["reply_to_message_id"])
	}
	if metadata["parent_message_id"] != "om_parent" {
		t.Fatalf("expected parent_message_id=om_parent, got %q", metadata["parent_message_id"])
	}
	if metadata["root_message_id"] != "om_root" {
		t.Fatalf("expected root_message_id=om_root, got %q", metadata["root_message_id"])
	}
	if metadata["thread_id"] != "thread_1" {
		t.Fatalf("expected thread_id=thread_1, got %q", metadata["thread_id"])
	}
}

func stringPtr(v string) *string { return &v }
