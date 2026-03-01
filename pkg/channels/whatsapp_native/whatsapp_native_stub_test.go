package whatsapp

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestNewWhatsAppNativeChannel_StubExplainsBuildTag(t *testing.T) {
	t.Parallel()

	ch, err := NewWhatsAppNativeChannel(config.WhatsAppConfig{}, bus.NewMessageBus(), "")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if ch != nil {
		t.Fatalf("expected nil channel, got %#v", ch)
	}
	if !strings.Contains(err.Error(), "whatsapp_native") {
		t.Fatalf("expected error to mention build tag; err=%v", err)
	}
}
