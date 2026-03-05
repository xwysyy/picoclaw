package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/providers"
)

func TestToolPolicy_ConfirmThenExecuteAndReplayIdempotently(t *testing.T) {
	workspace := t.TempDir()
	sessionKey := "agent:default:feishu:group:chat123"
	runID := "run-abc"

	var executed atomic.Int32
	registry := NewToolRegistry()
	registry.Register(&executorMockTool{
		name:   "exec",
		policy: ToolParallelSerialOnly,
		result: SilentResult("OK"),
		onExecute: func() {
			executed.Add(1)
		},
	})
	registry.Register(NewToolConfirmTool(workspace, 30*time.Minute))

	policyCfg := config.ToolPolicyConfig{
		Enabled: true,
		Confirm: config.ToolPolicyConfirmConfig{
			Enabled: true,
			Mode:    "always",
			Tools:   []string{"exec"},
		},
		Idempotency: config.ToolPolicyIdempotencyConfig{
			Enabled:     true,
			CacheResult: true,
			Tools:       []string{"exec"},
		},
	}

	opts := ToolCallExecutionOptions{
		Workspace:  workspace,
		SessionKey: sessionKey,
		RunID:      runID,
		IsResume:   true,
		Policy:     policyCfg,
		Iteration:  1,
		LogScope:   "test",
	}

	// 1) First attempt should be blocked by confirmation gate (no execution).
	results := ExecuteToolCalls(context.Background(), registry, []providers.ToolCall{
		{ID: "tc-1", Name: "exec", Arguments: map[string]any{"cmd": "echo hi"}},
	}, opts)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if executed.Load() != 0 {
		t.Fatalf("exec executed unexpectedly: %d", executed.Load())
	}
	if results[0].Result == nil || !results[0].Result.IsError {
		t.Fatalf("expected policy confirmation result with IsError=true, got: %+v", results[0].Result)
	}

	var confirmPayload struct {
		Kind       string `json:"kind"`
		ConfirmKey string `json:"confirm_key"`
	}
	if err := json.Unmarshal([]byte(results[0].Result.ForLLM), &confirmPayload); err != nil {
		t.Fatalf("expected JSON policy payload, got: %q (err=%v)", results[0].Result.ForLLM, err)
	}
	if confirmPayload.Kind != "tool_policy_confirmation_required" {
		t.Fatalf("kind = %q, want %q", confirmPayload.Kind, "tool_policy_confirmation_required")
	}
	if strings.TrimSpace(confirmPayload.ConfirmKey) == "" {
		t.Fatalf("expected non-empty confirm_key")
	}

	// 2) Confirm via tool_confirm.
	results = ExecuteToolCalls(context.Background(), registry, []providers.ToolCall{
		{ID: "tc-2", Name: "tool_confirm", Arguments: map[string]any{"confirm_key": confirmPayload.ConfirmKey}},
	}, opts)
	if len(results) != 1 || results[0].Result == nil || results[0].Result.IsError {
		t.Fatalf("tool_confirm failed: %+v", results)
	}

	// 3) Re-run the original exec; it should execute now and record idempotency output.
	results = ExecuteToolCalls(context.Background(), registry, []providers.ToolCall{
		{ID: "tc-3", Name: "exec", Arguments: map[string]any{"cmd": "echo hi"}},
	}, opts)
	if len(results) != 1 || results[0].Result == nil || results[0].Result.IsError {
		t.Fatalf("exec failed after confirmation: %+v", results)
	}
	if executed.Load() != 1 {
		t.Fatalf("exec executed count = %d, want 1", executed.Load())
	}

	// 4) Repeat the same exec again; it should replay from cache (no second execution).
	results = ExecuteToolCalls(context.Background(), registry, []providers.ToolCall{
		{ID: "tc-4", Name: "exec", Arguments: map[string]any{"cmd": "echo hi"}},
	}, opts)
	if len(results) != 1 || results[0].Result == nil {
		t.Fatalf("missing replay result: %+v", results)
	}
	if executed.Load() != 1 {
		t.Fatalf("exec executed again unexpectedly: %d", executed.Load())
	}
	if !strings.Contains(results[0].Result.ForLLM, "tool_policy_idempotent_replay") {
		t.Fatalf("expected replay metadata in ForLLM, got: %q", results[0].Result.ForLLM)
	}

	// Ledger should exist for this run.
	dirKey := SafePathToken(sessionKey)
	runKey := SafePathToken(runID)
	ledgerPath := filepath.Join(workspace, ".x-claw", "audit", "runs", dirKey, "runs", runKey, "policy.jsonl")
	data, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("failed to read policy ledger: %v", err)
	}
	if !strings.Contains(string(data), "\"type\":\"tool.confirmed\"") {
		t.Fatalf("expected tool.confirmed event in ledger:\n%s", string(data))
	}
	if !strings.Contains(string(data), "\"type\":\"tool.executed\"") {
		t.Fatalf("expected tool.executed event in ledger:\n%s", string(data))
	}
}
