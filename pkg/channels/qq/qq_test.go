package qq

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/channels"
	"github.com/xwysyy/X-Claw/pkg/config"
)

func TestQQChannel_Send_NotRunning(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewQQChannel(config.QQConfig{}, mb)
	if err != nil {
		t.Fatalf("NewQQChannel error: %v", err)
	}

	err = ch.Send(context.Background(), bus.OutboundMessage{ChatID: "u1", Content: "hi"})
	if !errors.Is(err, channels.ErrNotRunning) {
		t.Fatalf("Send error = %v, want ErrNotRunning", err)
	}
}

func TestQQChannel_IsDuplicate_Basic(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewQQChannel(config.QQConfig{}, mb)
	if err != nil {
		t.Fatalf("NewQQChannel error: %v", err)
	}

	if ch.isDuplicate("1") {
		t.Fatalf("first message should not be duplicate")
	}
	if !ch.isDuplicate("1") {
		t.Fatalf("second message should be duplicate")
	}
}

func TestQQChannel_IsDuplicate_Cleanup(t *testing.T) {
	t.Parallel()

	mb := bus.NewMessageBus()
	ch, err := NewQQChannel(config.QQConfig{}, mb)
	if err != nil {
		t.Fatalf("NewQQChannel error: %v", err)
	}

	// Pre-fill with 10k IDs to trigger cleanup on next insert.
	for i := 0; i < 10000; i++ {
		ch.processedIDs[strconv.Itoa(i)] = true
	}

	if ch.isDuplicate("new-id") {
		t.Fatalf("new-id should not be duplicate")
	}
	if got := len(ch.processedIDs); got != 5001 {
		t.Fatalf("len(processedIDs) = %d, want %d", got, 5001)
	}
}
