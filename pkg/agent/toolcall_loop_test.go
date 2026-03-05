package agent

import (
	"encoding/json"
	"testing"

	"github.com/xwysyy/picoclaw/pkg/providers"
)

func TestDetectToolCallLoop_NoLoopWhenBelowThreshold(t *testing.T) {
	recent := []toolCallSignature{
		mustSig(t, "read_file", map[string]any{"path": "a.txt"}),
		mustSig(t, "read_file", map[string]any{"path": "a.txt"}),
	}
	current := []providers.ToolCall{
		{Name: "read_file", Arguments: map[string]any{"path": "a.txt"}},
	}

	if got := detectToolCallLoop(recent, current, 3); got != "" {
		t.Fatalf("detectToolCallLoop = %q, want empty (no loop)", got)
	}
}

func TestDetectToolCallLoop_TriggersAtThreshold(t *testing.T) {
	recent := []toolCallSignature{
		mustSig(t, "read_file", map[string]any{"path": "a.txt"}),
		mustSig(t, "read_file", map[string]any{"path": "a.txt"}),
		mustSig(t, "read_file", map[string]any{"path": "a.txt"}),
	}
	current := []providers.ToolCall{
		{Name: "read_file", Arguments: map[string]any{"path": "a.txt"}},
	}

	if got := detectToolCallLoop(recent, current, 3); got != "read_file" {
		t.Fatalf("detectToolCallLoop = %q, want %q", got, "read_file")
	}
}

func TestDetectToolCallLoop_DifferentArgsDoNotCount(t *testing.T) {
	recent := []toolCallSignature{
		mustSig(t, "read_file", map[string]any{"path": "a.txt"}),
		mustSig(t, "read_file", map[string]any{"path": "b.txt"}),
		mustSig(t, "read_file", map[string]any{"path": "a.txt"}),
	}
	current := []providers.ToolCall{
		{Name: "read_file", Arguments: map[string]any{"path": "a.txt"}},
	}

	// Only 2 exact matches for a.txt, so threshold 3 should not trigger.
	if got := detectToolCallLoop(recent, current, 3); got != "" {
		t.Fatalf("detectToolCallLoop = %q, want empty (no loop)", got)
	}
}

func TestDetectToolCallLoop_MultipleCurrent_ReturnsFirstMatch(t *testing.T) {
	recent := []toolCallSignature{
		mustSig(t, "tool_a", map[string]any{"x": 1}),
		mustSig(t, "tool_a", map[string]any{"x": 1}),
		mustSig(t, "tool_b", map[string]any{"y": "ok"}),
		mustSig(t, "tool_b", map[string]any{"y": "ok"}),
	}

	current := []providers.ToolCall{
		{Name: "tool_b", Arguments: map[string]any{"y": "ok"}},
		{Name: "tool_a", Arguments: map[string]any{"x": 1}},
	}

	// Threshold 2: both tool_a and tool_b are looping, but tool_b comes first in current.
	if got := detectToolCallLoop(recent, current, 2); got != "tool_b" {
		t.Fatalf("detectToolCallLoop = %q, want %q", got, "tool_b")
	}
}

func mustSig(t *testing.T, name string, args map[string]any) toolCallSignature {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}
	return toolCallSignature{
		Name: name,
		Args: string(b),
	}
}
