package agent

import (
	"context"
	"strings"

	"github.com/xwysyy/X-Claw/pkg/bus"
)

type steeringInboxKey struct{}

func withSteeringInbox(ctx context.Context, inbox <-chan bus.InboundMessage) context.Context {
	if ctx == nil || inbox == nil {
		return ctx
	}
	return context.WithValue(ctx, steeringInboxKey{}, inbox)
}

func steeringInboxFromContext(ctx context.Context) <-chan bus.InboundMessage {
	if ctx == nil {
		return nil
	}
	v := ctx.Value(steeringInboxKey{})
	if v == nil {
		return nil
	}
	if ch, ok := v.(<-chan bus.InboundMessage); ok {
		return ch
	}
	if ch, ok := v.(chan bus.InboundMessage); ok {
		return ch
	}
	return nil
}

// extractSteeringContent recognizes "/steer <text>" style messages.
// It returns the message body and true only when a non-empty body exists.
func extractSteeringContent(content string) (string, bool) {
	raw := strings.TrimSpace(content)
	if raw == "" {
		return "", false
	}
	fields := strings.Fields(raw)
	if len(fields) < 2 {
		return "", false
	}
	cmd := strings.ToLower(strings.TrimSpace(fields[0]))
	if cmd != "/steer" && cmd != "/steering" {
		return "", false
	}
	body := strings.TrimSpace(raw[len(fields[0]):])
	if body == "" {
		return "", false
	}
	return body, true
}
