package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestExecuteToolCalls_WritesJSONLToolTraceWhenEnabled(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(&executorMockTool{
		name:   "ok",
		policy: ToolParallelReadOnly,
		result: SilentResult("done"),
	})

	workspace := t.TempDir()
	sessionKey := "sess-1"

	calls := []providers.ToolCall{
		{ID: "tc-1", Name: "ok", Arguments: map[string]any{"x": 1, "y": "z"}},
	}

	results := ExecuteToolCalls(context.Background(), registry, calls, ToolCallExecutionOptions{
		Workspace:  workspace,
		SessionKey: sessionKey,
		Iteration:  7,
		LogScope:   "test",
		Trace: ToolTraceOptions{
			Enabled:               true,
			WritePerCallFiles:     true,
			MaxArgPreviewChars:    12,
			MaxResultPreviewChars: 8,
		},
	})
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}

	eventsPath := filepath.Join(workspace, ".picoclaw", "audit", "tools", sessionKey, "events.jsonl")
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("failed to read events.jsonl: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 JSONL lines (start/end), got %d:\n%s", len(lines), string(data))
	}

	var start toolTraceEvent
	if err := json.Unmarshal([]byte(lines[0]), &start); err != nil {
		t.Fatalf("failed to unmarshal start event: %v", err)
	}
	if start.Type != "tool.start" {
		t.Fatalf("start.Type = %q, want %q", start.Type, "tool.start")
	}
	if start.Tool != "ok" || start.ToolCallID != "tc-1" {
		t.Fatalf("unexpected start tool metadata: %+v", start)
	}
	if start.Iteration != 7 {
		t.Fatalf("start.Iteration = %d, want 7", start.Iteration)
	}
	if start.ArgsPreview == "" {
		t.Fatalf("expected non-empty args_preview")
	}
	if len(start.ArgsPreview) > 12 {
		t.Fatalf("args_preview too long (%d > 12): %q", len(start.ArgsPreview), start.ArgsPreview)
	}

	var end toolTraceEvent
	if err := json.Unmarshal([]byte(lines[1]), &end); err != nil {
		t.Fatalf("failed to unmarshal end event: %v", err)
	}
	if end.Type != "tool.end" {
		t.Fatalf("end.Type = %q, want %q", end.Type, "tool.end")
	}
	if end.ForLLMPreview != "done" {
		t.Fatalf("unexpected for_llm_preview: %q", end.ForLLMPreview)
	}

	// Per-call snapshot files should exist.
	callDir := filepath.Join(workspace, ".picoclaw", "audit", "tools", sessionKey, "calls")
	entries, err := os.ReadDir(callDir)
	if err != nil {
		t.Fatalf("failed to read per-call dir: %v", err)
	}
	var hasJSON, hasMD bool
	for _, e := range entries {
		switch filepath.Ext(e.Name()) {
		case ".json":
			hasJSON = true
		case ".md":
			hasMD = true
		}
	}
	if !hasJSON || !hasMD {
		t.Fatalf("expected both .json and .md snapshots, got entries: %v", entries)
	}
}
