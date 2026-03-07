package agent

import (
	"testing"

	"github.com/xwysyy/X-Claw/pkg/bus"
)

func TestResolveInboundSessionKey_UsesReplyBinding(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	msgBus.BindReplyContext("feishu", "oc_test", "om_parent", bus.ReplyContext{SessionKey: "conv:feishu:direct:oc_test"})

	got := al.resolveInboundSessionKey(bus.InboundMessage{
		Channel: "feishu",
		ChatID:  "oc_test",
		Metadata: map[string]string{
			"reply_to_message_id": "om_parent",
		},
	}, "conv:feishu:direct:fallback")

	if got != "conv:feishu:direct:oc_test" {
		t.Fatalf("expected bound session key, got %q", got)
	}
}

func TestResolveInboundSessionKey_ExplicitSessionWinsOverReplyBinding(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	msgBus.BindReplyContext("feishu", "oc_test", "om_parent", bus.ReplyContext{SessionKey: "conv:feishu:direct:oc_test"})

	got := al.resolveInboundSessionKey(bus.InboundMessage{
		Channel:    "feishu",
		ChatID:     "oc_test",
		SessionKey: "conv:feishu:direct:explicit",
		Metadata: map[string]string{
			"reply_to_message_id": "om_parent",
		},
	}, "conv:feishu:direct:fallback")

	if got != "conv:feishu:direct:explicit" {
		t.Fatalf("expected explicit session key, got %q", got)
	}
}

func TestResolveInboundSessionKey_UsesRootReplyBindingFallback(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	msgBus.BindReplyContext("feishu", "oc_test", "om_root", bus.ReplyContext{SessionKey: "cron-job-root"})

	got := al.resolveInboundSessionKey(bus.InboundMessage{
		Channel: "feishu",
		ChatID:  "oc_test",
		Metadata: map[string]string{
			"root_message_id": "om_root",
		},
	}, "conv:feishu:direct:fallback")

	if got != "cron-job-root" {
		t.Fatalf("expected root-bound session key, got %q", got)
	}
}
