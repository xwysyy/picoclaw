package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/providers"
)

type executorMockTool struct {
	name   string
	policy ToolParallelPolicy

	delay time.Duration

	result *ToolResult
	errMsg string

	running    *atomic.Int32
	maxRunning *atomic.Int32
	onExecute  func()
	onComplete func()
}

func (t *executorMockTool) Name() string {
	return t.name
}

func (t *executorMockTool) Description() string {
	return "executor mock tool"
}

func (t *executorMockTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
	}
}

func (t *executorMockTool) ParallelPolicy() ToolParallelPolicy {
	return t.policy
}

func (t *executorMockTool) Execute(_ context.Context, _ map[string]any) *ToolResult {
	if t.onExecute != nil {
		t.onExecute()
	}

	if t.running != nil && t.maxRunning != nil {
		cur := t.running.Add(1)
		for {
			prev := t.maxRunning.Load()
			if cur <= prev || t.maxRunning.CompareAndSwap(prev, cur) {
				break
			}
		}
		defer t.running.Add(-1)
	}

	if t.delay > 0 {
		time.Sleep(t.delay)
	}
	if t.onComplete != nil {
		t.onComplete()
	}

	if t.errMsg != "" {
		return ErrorResult(t.errMsg).WithError(fmt.Errorf("%s", t.errMsg))
	}
	if t.result != nil {
		return t.result
	}
	return SilentResult("ok")
}

type executorContextualTool struct {
	executorMockTool
	channel string
	chatID  string
}

func (t *executorContextualTool) SetContext(channel, chatID string) {
	t.channel = channel
	t.chatID = chatID
}

func (t *executorContextualTool) SupportsConcurrentExecution() bool {
	return false
}

type executorSchemaErrorTool struct{}

func (t *executorSchemaErrorTool) Name() string        { return "schema_error" }
func (t *executorSchemaErrorTool) Description() string { return "schema error tool" }
func (t *executorSchemaErrorTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
			"mode": map[string]any{"type": "string"},
		},
		"required": []string{"path"},
	}
}
func (t *executorSchemaErrorTool) Execute(_ context.Context, _ map[string]any) *ToolResult {
	return ErrorResult("boom").WithError(fmt.Errorf("boom"))
}

func TestExecuteToolCalls_PreservesOrderWithParallelBatch(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(&executorMockTool{
		name:   "slow",
		policy: ToolParallelReadOnly,
		delay:  60 * time.Millisecond,
		result: SilentResult("slow-result"),
	})
	registry.Register(&executorMockTool{
		name:   "fast",
		policy: ToolParallelReadOnly,
		delay:  5 * time.Millisecond,
		result: SilentResult("fast-result"),
	})

	calls := []providers.ToolCall{
		{ID: "tc-1", Name: "slow", Arguments: map[string]any{}},
		{ID: "tc-2", Name: "fast", Arguments: map[string]any{}},
	}

	results := ExecuteToolCalls(context.Background(), registry, calls, ToolCallExecutionOptions{
		Iteration: 1,
		LogScope:  "test",
		Parallel: ToolCallParallelConfig{
			Enabled:        true,
			MaxConcurrency: 8,
			Mode:           ParallelToolsModeReadOnlyOnly,
		},
	})

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if results[0].ToolCall.ID != "tc-1" || results[0].Result.ForLLM != "slow-result" {
		t.Fatalf("results[0] = %+v, want tc-1/slow-result", results[0])
	}
	if results[1].ToolCall.ID != "tc-2" || results[1].Result.ForLLM != "fast-result" {
		t.Fatalf("results[1] = %+v, want tc-2/fast-result", results[1])
	}
}

func TestExecuteToolCalls_ErrorTemplate_ToolNotFound(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(&executorMockTool{name: "exists"})

	calls := []providers.ToolCall{
		{ID: "tc-1", Name: "missing", Arguments: map[string]any{"a": "b"}},
	}

	results := ExecuteToolCalls(context.Background(), registry, calls, ToolCallExecutionOptions{
		Iteration: 1,
		LogScope:  "test",
		ErrorTemplate: ToolErrorTemplateOptions{
			Enabled:               true,
			IncludeSchema:         true,
			IncludeAvailableTools: true,
		},
	})

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Result == nil {
		t.Fatalf("nil ToolResult")
	}
	if !results[0].Result.IsError {
		t.Fatalf("expected IsError=true")
	}

	var payload toolErrorTemplate
	if err := json.Unmarshal([]byte(results[0].Result.ForLLM), &payload); err != nil {
		t.Fatalf("expected JSON error template, got err=%v content=%q", err, results[0].Result.ForLLM)
	}
	if payload.Kind != "tool_error" {
		t.Fatalf("payload.Kind = %q, want tool_error", payload.Kind)
	}
	if payload.Tool != "missing" {
		t.Fatalf("payload.Tool = %q, want missing", payload.Tool)
	}
	if payload.ToolCallID != "tc-1" {
		t.Fatalf("payload.ToolCallID = %q, want tc-1", payload.ToolCallID)
	}
	if len(payload.AvailableTools) == 0 || payload.AvailableTools[0] != "exists" {
		t.Fatalf("payload.AvailableTools = %#v, want contains 'exists'", payload.AvailableTools)
	}
}

func TestExecuteToolCalls_ErrorTemplate_IncludesSchemaSummary(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(&executorSchemaErrorTool{})

	calls := []providers.ToolCall{
		{ID: "tc-1", Name: "schema_error", Arguments: map[string]any{}},
	}

	results := ExecuteToolCalls(context.Background(), registry, calls, ToolCallExecutionOptions{
		Iteration: 7,
		LogScope:  "test",
		ErrorTemplate: ToolErrorTemplateOptions{
			Enabled:               true,
			IncludeSchema:         true,
			IncludeAvailableTools: true,
		},
	})

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Result == nil {
		t.Fatalf("nil ToolResult")
	}

	var payload toolErrorTemplate
	if err := json.Unmarshal([]byte(results[0].Result.ForLLM), &payload); err != nil {
		t.Fatalf("expected JSON error template, got err=%v content=%q", err, results[0].Result.ForLLM)
	}
	if payload.Kind != "tool_error" {
		t.Fatalf("payload.Kind = %q, want tool_error", payload.Kind)
	}
	if payload.Iteration != 7 {
		t.Fatalf("payload.Iteration = %d, want 7", payload.Iteration)
	}
	if payload.ToolSchema == nil {
		t.Fatalf("expected payload.ToolSchema")
	}
	if len(payload.ToolSchema.Required) != 1 || payload.ToolSchema.Required[0] != "path" {
		t.Fatalf("payload.ToolSchema.Required = %#v, want [path]", payload.ToolSchema.Required)
	}
	if len(payload.ToolSchema.Keys) != 2 {
		t.Fatalf("payload.ToolSchema.Keys = %#v, want 2 keys", payload.ToolSchema.Keys)
	}
}

func TestExecuteToolCalls_EstopKillAllBlocksTools(t *testing.T) {
	workspace := t.TempDir()
	stateDir := filepath.Join(workspace, ".x-claw", "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir estop state dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "estop.json"), []byte(`{"kill_all":true}`), 0o600); err != nil {
		t.Fatalf("write estop state: %v", err)
	}

	registry := NewToolRegistry()
	executed := atomic.Int32{}
	registry.Register(&executorMockTool{
		name:   "safe_read",
		policy: ToolParallelReadOnly,
		result: SilentResult("ok"),
		onExecute: func() {
			executed.Add(1)
		},
	})

	calls := []providers.ToolCall{
		{ID: "tc-1", Name: "safe_read", Arguments: map[string]any{}},
	}

	results := ExecuteToolCalls(context.Background(), registry, calls, ToolCallExecutionOptions{
		Workspace:  workspace,
		SessionKey: "s",
		RunID:      "r",
		Estop: config.EstopConfig{
			Enabled:    true,
			FailClosed: true,
		},
		Iteration: 1,
		LogScope:  "test",
	})

	if executed.Load() != 0 {
		t.Fatalf("expected estop to block execution, executed=%d", executed.Load())
	}
	if len(results) != 1 || results[0].Result == nil {
		t.Fatalf("unexpected results: %+v", results)
	}
	if !results[0].Result.IsError {
		t.Fatalf("expected IsError=true, got %+v", results[0].Result)
	}
	if !strings.Contains(results[0].Result.ForLLM, "TOOL_EXECUTION_DENIED") {
		t.Fatalf("expected deny message, got: %q", results[0].Result.ForLLM)
	}
}

func TestExecuteToolCalls_RespectsConcurrencyLimit(t *testing.T) {
	registry := NewToolRegistry()
	var running atomic.Int32
	var maxRunning atomic.Int32
	registry.Register(&executorMockTool{
		name:       "io",
		policy:     ToolParallelReadOnly,
		delay:      25 * time.Millisecond,
		result:     SilentResult("ok"),
		running:    &running,
		maxRunning: &maxRunning,
	})

	calls := make([]providers.ToolCall, 0, 20)
	for i := 0; i < 20; i++ {
		calls = append(calls, providers.ToolCall{
			ID:        fmt.Sprintf("tc-%d", i),
			Name:      "io",
			Arguments: map[string]any{"index": i},
		})
	}

	results := ExecuteToolCalls(context.Background(), registry, calls, ToolCallExecutionOptions{
		Iteration: 1,
		LogScope:  "test",
		Parallel: ToolCallParallelConfig{
			Enabled:        true,
			MaxConcurrency: 3,
			Mode:           ParallelToolsModeReadOnlyOnly,
		},
	})

	if len(results) != len(calls) {
		t.Fatalf("len(results) = %d, want %d", len(results), len(calls))
	}
	if maxRunning.Load() > 3 {
		t.Fatalf("maxRunning = %d, want <= 3", maxRunning.Load())
	}
	if maxRunning.Load() < 2 {
		t.Fatalf("maxRunning = %d, want >= 2 to confirm parallel execution", maxRunning.Load())
	}
}

func TestExecuteToolCalls_SerialBoundaryBeforeParallelBatch(t *testing.T) {
	registry := NewToolRegistry()
	writeDone := make(chan struct{})
	var readStartedBeforeWriteDone atomic.Bool

	registry.Register(&executorMockTool{
		name:       "write_file",
		policy:     ToolParallelSerialOnly,
		delay:      60 * time.Millisecond,
		result:     SilentResult("write-ok"),
		onComplete: func() { close(writeDone) },
	})

	readTool := &executorMockTool{
		name:   "read_file",
		policy: ToolParallelReadOnly,
		delay:  20 * time.Millisecond,
		result: SilentResult("read-ok"),
		onExecute: func() {
			select {
			case <-writeDone:
			default:
				readStartedBeforeWriteDone.Store(true)
			}
		},
	}
	registry.Register(readTool)

	calls := []providers.ToolCall{
		{ID: "tc-1", Name: "write_file", Arguments: map[string]any{"path": "x"}},
		{ID: "tc-2", Name: "read_file", Arguments: map[string]any{"path": "x"}},
		{ID: "tc-3", Name: "read_file", Arguments: map[string]any{"path": "y"}},
	}

	results := ExecuteToolCalls(context.Background(), registry, calls, ToolCallExecutionOptions{
		Iteration: 1,
		LogScope:  "test",
		Parallel: ToolCallParallelConfig{
			Enabled:        true,
			MaxConcurrency: 8,
			Mode:           ParallelToolsModeReadOnlyOnly,
		},
	})

	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(results))
	}
	if readStartedBeforeWriteDone.Load() {
		t.Fatal("read tool started before preceding serial write tool finished")
	}
}

func TestExecuteToolCalls_CollectsFailuresWithoutShortCircuit(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(&executorMockTool{
		name:   "ok",
		policy: ToolParallelReadOnly,
		delay:  20 * time.Millisecond,
		result: SilentResult("ok-result"),
	})
	registry.Register(&executorMockTool{
		name:   "fail",
		policy: ToolParallelReadOnly,
		delay:  5 * time.Millisecond,
		errMsg: "boom",
	})

	calls := []providers.ToolCall{
		{ID: "tc-1", Name: "ok", Arguments: map[string]any{}},
		{ID: "tc-2", Name: "fail", Arguments: map[string]any{}},
	}

	results := ExecuteToolCalls(context.Background(), registry, calls, ToolCallExecutionOptions{
		Iteration: 1,
		LogScope:  "test",
		Parallel: ToolCallParallelConfig{
			Enabled:        true,
			MaxConcurrency: 8,
			Mode:           ParallelToolsModeReadOnlyOnly,
		},
	})

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if results[0].Result.IsError {
		t.Fatalf("results[0].Result.IsError = true, want false")
	}
	if !results[1].Result.IsError {
		t.Fatalf("results[1].Result.IsError = false, want true")
	}
}

func TestExecuteToolCalls_OverrideForcesParallelInReadOnlyMode(t *testing.T) {
	registry := NewToolRegistry()
	var running atomic.Int32
	var maxRunning atomic.Int32

	// Default policy is serial_only for tools without ParallelPolicyProvider.
	registry.Register(&executorMockTool{
		name:       "custom_tool",
		delay:      20 * time.Millisecond,
		result:     SilentResult("ok"),
		running:    &running,
		maxRunning: &maxRunning,
	})

	calls := []providers.ToolCall{
		{ID: "tc-1", Name: "custom_tool", Arguments: map[string]any{}},
		{ID: "tc-2", Name: "custom_tool", Arguments: map[string]any{}},
	}

	results := ExecuteToolCalls(context.Background(), registry, calls, ToolCallExecutionOptions{
		Iteration: 1,
		LogScope:  "test",
		Parallel: ToolCallParallelConfig{
			Enabled:        true,
			MaxConcurrency: 8,
			Mode:           ParallelToolsModeReadOnlyOnly,
			ToolPolicyOverrides: map[string]string{
				"custom_tool": string(ToolParallelReadOnly),
			},
		},
	})

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if maxRunning.Load() < 2 {
		t.Fatalf("maxRunning = %d, want >= 2 when override forces parallel", maxRunning.Load())
	}
}

func TestExecuteToolCalls_OverrideForcesSerialInAllMode(t *testing.T) {
	registry := NewToolRegistry()
	var running atomic.Int32
	var maxRunning atomic.Int32
	registry.Register(&executorMockTool{
		name:       "any_tool",
		delay:      20 * time.Millisecond,
		result:     SilentResult("ok"),
		running:    &running,
		maxRunning: &maxRunning,
	})

	calls := []providers.ToolCall{
		{ID: "tc-1", Name: "any_tool", Arguments: map[string]any{}},
		{ID: "tc-2", Name: "any_tool", Arguments: map[string]any{}},
	}

	results := ExecuteToolCalls(context.Background(), registry, calls, ToolCallExecutionOptions{
		Iteration: 1,
		LogScope:  "test",
		Parallel: ToolCallParallelConfig{
			Enabled:        true,
			MaxConcurrency: 8,
			Mode:           ParallelToolsModeAll,
			ToolPolicyOverrides: map[string]string{
				"any_tool": string(ToolParallelSerialOnly),
			},
		},
	})

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if maxRunning.Load() > 1 {
		t.Fatalf("maxRunning = %d, want <= 1 when override forces serial", maxRunning.Load())
	}
}

func TestExecuteToolCalls_OverrideCannotBypassInstanceSafety(t *testing.T) {
	registry := NewToolRegistry()
	var running atomic.Int32
	var maxRunning atomic.Int32
	registry.Register(&executorContextualTool{
		executorMockTool: executorMockTool{
			name:       "ctx_tool",
			delay:      20 * time.Millisecond,
			result:     SilentResult("ok"),
			running:    &running,
			maxRunning: &maxRunning,
		},
	})

	calls := []providers.ToolCall{
		{ID: "tc-1", Name: "ctx_tool", Arguments: map[string]any{}},
		{ID: "tc-2", Name: "ctx_tool", Arguments: map[string]any{}},
	}

	results := ExecuteToolCalls(context.Background(), registry, calls, ToolCallExecutionOptions{
		Channel:   "telegram",
		ChatID:    "chat-1",
		Iteration: 1,
		LogScope:  "test",
		Parallel: ToolCallParallelConfig{
			Enabled:        true,
			MaxConcurrency: 8,
			Mode:           ParallelToolsModeAll,
			ToolPolicyOverrides: map[string]string{
				"ctx_tool": string(ToolParallelReadOnly),
			},
		},
	})

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if maxRunning.Load() > 1 {
		t.Fatalf("maxRunning = %d, want <= 1 because instance safety should block parallel", maxRunning.Load())
	}
}

func TestExecuteToolCalls_PlanModeBlocksRestrictedTools(t *testing.T) {
	registry := NewToolRegistry()
	executed := atomic.Int32{}
	registry.Register(&executorMockTool{
		name:   "exec",
		result: SilentResult("should-not-run"),
		onExecute: func() {
			executed.Add(1)
		},
	})

	calls := []providers.ToolCall{
		{ID: "tc-1", Name: "exec", Arguments: map[string]any{}},
	}

	results := ExecuteToolCalls(context.Background(), registry, calls, ToolCallExecutionOptions{
		Iteration: 1,
		LogScope:  "test",
		PlanMode:  true,
		PlanRestrictedTools: []string{
			"exec",
		},
	})

	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Result == nil {
		t.Fatalf("nil ToolResult")
	}
	if !results[0].Result.IsError {
		t.Fatalf("expected IsError=true, got %q", results[0].Result.ForLLM)
	}
	if executed.Load() != 0 {
		t.Fatalf("expected plan mode to block execution, executed=%d", executed.Load())
	}
	if !strings.Contains(results[0].Result.ForLLM, "TOOL_EXECUTION_DENIED") {
		t.Fatalf("expected deny message, got %q", results[0].Result.ForLLM)
	}
}

func TestExecuteToolCalls_ToolPolicyDenyBlocksTool(t *testing.T) {
	registry := NewToolRegistry()
	executed := atomic.Int32{}
	registry.Register(&executorMockTool{
		name:   "exec",
		result: SilentResult("should-not-run"),
		onExecute: func() {
			executed.Add(1)
		},
	})

	results := ExecuteToolCalls(context.Background(), registry, []providers.ToolCall{{ID: "tc-1", Name: "exec", Arguments: map[string]any{}}}, ToolCallExecutionOptions{
		Iteration: 1,
		LogScope:  "test",
		Policy: config.ToolPolicyConfig{
			Enabled: true,
			Deny:    []string{"exec"},
		},
	})

	if len(results) != 1 || results[0].Result == nil {
		t.Fatalf("unexpected results: %+v", results)
	}
	if !results[0].Result.IsError {
		t.Fatalf("expected IsError=true, got %+v", results[0].Result)
	}
	if executed.Load() != 0 {
		t.Fatalf("expected tool policy to block execution, executed=%d", executed.Load())
	}
	if !strings.Contains(results[0].Result.ForLLM, "TOOL_EXECUTION_DENIED") {
		t.Fatalf("expected deny message, got %q", results[0].Result.ForLLM)
	}
}

func TestExecuteToolCalls_ToolPolicyAllowlistRejectsUnlistedTool(t *testing.T) {
	registry := NewToolRegistry()
	executed := atomic.Int32{}
	registry.Register(&executorMockTool{
		name:   "exec",
		result: SilentResult("should-not-run"),
		onExecute: func() {
			executed.Add(1)
		},
	})

	results := ExecuteToolCalls(context.Background(), registry, []providers.ToolCall{{ID: "tc-1", Name: "exec", Arguments: map[string]any{}}}, ToolCallExecutionOptions{
		Iteration: 1,
		LogScope:  "test",
		Policy: config.ToolPolicyConfig{
			Enabled: true,
			Allow:   []string{"read_file"},
		},
	})

	if len(results) != 1 || results[0].Result == nil {
		t.Fatalf("unexpected results: %+v", results)
	}
	if !results[0].Result.IsError {
		t.Fatalf("expected IsError=true, got %+v", results[0].Result)
	}
	if executed.Load() != 0 {
		t.Fatalf("expected allowlist rejection to block execution, executed=%d", executed.Load())
	}
	if !strings.Contains(results[0].Result.ForLLM, "allowlist") {
		t.Fatalf("expected allowlist reason, got %q", results[0].Result.ForLLM)
	}
}

func TestExecuteToolCalls_ToolPolicyConfirmResumeOnlyBlocksResume(t *testing.T) {
	registry := NewToolRegistry()
	executed := atomic.Int32{}
	registry.Register(&executorMockTool{
		name:   "write_file",
		result: SilentResult("should-not-run"),
		onExecute: func() {
			executed.Add(1)
		},
	})

	results := ExecuteToolCalls(context.Background(), registry, []providers.ToolCall{{ID: "tc-1", Name: "write_file", Arguments: map[string]any{}}}, ToolCallExecutionOptions{
		Iteration: 1,
		LogScope:  "test",
		IsResume:  true,
		Policy: config.ToolPolicyConfig{
			Enabled: true,
			Confirm: config.ToolPolicyConfirmConfig{
				Enabled: true,
				Mode:    "resume_only",
				Tools:   []string{"write_file"},
			},
		},
	})

	if len(results) != 1 || results[0].Result == nil {
		t.Fatalf("unexpected results: %+v", results)
	}
	if !results[0].Result.IsError {
		t.Fatalf("expected IsError=true, got %+v", results[0].Result)
	}
	if executed.Load() != 0 {
		t.Fatalf("expected confirmation gate to block execution, executed=%d", executed.Load())
	}
	if !strings.Contains(results[0].Result.ForLLM, "confirmation required") {
		t.Fatalf("expected confirmation reason, got %q", results[0].Result.ForLLM)
	}
}

func TestExecuteToolCalls_ToolPolicyConfirmResumeOnlyAllowsNormalRun(t *testing.T) {
	registry := NewToolRegistry()
	executed := atomic.Int32{}
	registry.Register(&executorMockTool{
		name:   "write_file",
		result: SilentResult("ok"),
		onExecute: func() {
			executed.Add(1)
		},
	})

	results := ExecuteToolCalls(context.Background(), registry, []providers.ToolCall{{ID: "tc-1", Name: "write_file", Arguments: map[string]any{}}}, ToolCallExecutionOptions{
		Iteration: 1,
		LogScope:  "test",
		IsResume:  false,
		Policy: config.ToolPolicyConfig{
			Enabled: true,
			Confirm: config.ToolPolicyConfirmConfig{
				Enabled: true,
				Mode:    "resume_only",
				Tools:   []string{"write_file"},
			},
		},
	})

	if len(results) != 1 || results[0].Result == nil {
		t.Fatalf("unexpected results: %+v", results)
	}
	if results[0].Result.IsError {
		t.Fatalf("expected normal run to pass, got %+v", results[0].Result)
	}
	if executed.Load() != 1 {
		t.Fatalf("expected tool to execute on non-resume run, executed=%d", executed.Load())
	}
}

func TestExecuteToolCalls_MaxResultChars_UsesHeadTailTruncation(t *testing.T) {
	registry := NewToolRegistry()

	head := "HEAD_BEGIN:"
	tail := ":TAIL_END_1234567890"
	long := head + strings.Repeat("x", 500) + tail

	registry.Register(&executorMockTool{
		name: "big_ok",
		result: &ToolResult{
			ForLLM:  long,
			ForUser: long,
			IsError: false,
		},
	})
	registry.Register(&executorMockTool{
		name: "big_err",
		result: &ToolResult{
			ForLLM:  long,
			ForUser: long,
			IsError: true,
		},
	})

	calls := []providers.ToolCall{
		{ID: "tc-1", Name: "big_ok", Arguments: map[string]any{}},
		{ID: "tc-2", Name: "big_err", Arguments: map[string]any{}},
	}

	results := ExecuteToolCalls(context.Background(), registry, calls, ToolCallExecutionOptions{
		Iteration:      1,
		LogScope:       "test",
		MaxResultChars: 60,
	})

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	for i, res := range results {
		if res.Result == nil {
			t.Fatalf("results[%d].Result is nil", i)
		}
		if len([]rune(res.Result.ForLLM)) > 60 {
			t.Fatalf("results[%d].Result.ForLLM exceeds MaxResultChars: %d", i, len([]rune(res.Result.ForLLM)))
		}
		if !strings.Contains(res.Result.ForLLM, "\n... (truncated) ...\n") {
			t.Fatalf("results[%d].Result.ForLLM missing truncation marker: %q", i, res.Result.ForLLM)
		}
		if !strings.HasSuffix(res.Result.ForLLM, tail) {
			t.Fatalf("results[%d].Result.ForLLM should preserve tail suffix %q, got %q", i, tail, res.Result.ForLLM)
		}
	}

	// Non-error results should keep a meaningful head prefix; error results may
	// intentionally bias toward keeping more tail diagnostics.
	if !strings.HasPrefix(results[0].Result.ForLLM, head) {
		t.Fatalf("ok result should preserve head prefix %q, got %q", head, results[0].Result.ForLLM)
	}
}

func TestPercentileInt64_NearestRank(t *testing.T) {
	values := []int64{10, 100}
	if got := percentileInt64(values, 0.95); got != 100 {
		t.Fatalf("p95 with n=2 = %d, want 100 (nearest-rank)", got)
	}
	if got := percentileInt64(values, 0.50); got != 10 {
		t.Fatalf("p50 with n=2 = %d, want 10 (nearest-rank)", got)
	}

	values3 := []int64{10, 20, 30}
	if got := percentileInt64(values3, 0.95); got != 30 {
		t.Fatalf("p95 with n=3 = %d, want 30 (nearest-rank)", got)
	}
}

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

	eventsPath := filepath.Join(workspace, ".x-claw", "audit", "tools", sessionKey, "events.jsonl")
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
	callDir := filepath.Join(workspace, ".x-claw", "audit", "tools", sessionKey, "calls")
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

type scriptedToolLoopProvider struct {
	callCount int
}

func (p *scriptedToolLoopProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.callCount++
	if p.callCount == 1 {
		return &providers.LLMResponse{
			ToolCalls: []providers.ToolCall{
				{ID: "tc-1", Name: "slow", Arguments: map[string]any{}},
				{ID: "tc-2", Name: "fast", Arguments: map[string]any{}},
			},
		}, nil
	}

	if p.callCount == 2 {
		toolMessages := make([]providers.Message, 0, 2)
		for _, m := range messages {
			if m.Role == "tool" {
				toolMessages = append(toolMessages, m)
			}
		}
		if len(toolMessages) != 2 {
			return nil, fmt.Errorf("tool message count = %d, want 2", len(toolMessages))
		}
		if toolMessages[0].ToolCallID != "tc-1" || toolMessages[0].Content != "slow-result" {
			return nil, fmt.Errorf("first tool message = %+v, want tc-1/slow-result", toolMessages[0])
		}
		if toolMessages[1].ToolCallID != "tc-2" || toolMessages[1].Content != "fast-result" {
			return nil, fmt.Errorf("second tool message = %+v, want tc-2/fast-result", toolMessages[1])
		}
		return &providers.LLMResponse{Content: "final-answer"}, nil
	}

	return &providers.LLMResponse{Content: "unexpected-extra-call"}, nil
}

func (p *scriptedToolLoopProvider) GetDefaultModel() string {
	return "toolloop-scripted"
}

func TestRunToolLoop_ParallelToolCallsPreserveOrder(t *testing.T) {
	provider := &scriptedToolLoopProvider{}
	registry := NewToolRegistry()
	registry.Register(&executorMockTool{
		name:   "slow",
		policy: ToolParallelReadOnly,
		delay:  50 * time.Millisecond,
		result: SilentResult("slow-result"),
	})
	registry.Register(&executorMockTool{
		name:   "fast",
		policy: ToolParallelReadOnly,
		delay:  5 * time.Millisecond,
		result: SilentResult("fast-result"),
	})

	result, err := RunToolLoop(
		context.Background(),
		ToolLoopConfig{
			Provider:                 provider,
			Model:                    "mock-model",
			Tools:                    registry,
			MaxIterations:            4,
			ToolCallsParallelEnabled: true,
			MaxToolCallConcurrency:   8,
			ParallelToolsMode:        ParallelToolsModeReadOnlyOnly,
		},
		[]providers.Message{
			{Role: "system", Content: "you are a test assistant"},
			{Role: "user", Content: "run tools"},
		},
		"cli",
		"direct",
	)
	if err != nil {
		t.Fatalf("RunToolLoop() error = %v", err)
	}
	if result == nil {
		t.Fatal("RunToolLoop() returned nil result")
	}
	if result.Content != "final-answer" {
		t.Fatalf("result.Content = %q, want %q", result.Content, "final-answer")
	}
	if provider.callCount != 2 {
		t.Fatalf("provider.callCount = %d, want 2", provider.callCount)
	}
	if len(result.Trace) != 2 {
		t.Fatalf("len(result.Trace) = %d, want 2", len(result.Trace))
	}
	if result.Trace[0].ToolCallID != "tc-1" || result.Trace[0].Result != "slow-result" {
		t.Fatalf("trace[0] = %+v, want tc-1/slow-result", result.Trace[0])
	}
	if result.Trace[1].ToolCallID != "tc-2" || result.Trace[1].Result != "fast-result" {
		t.Fatalf("trace[1] = %+v, want tc-2/fast-result", result.Trace[1])
	}
}

func TestNewToolResult(t *testing.T) {
	result := NewToolResult("test content")

	if result.ForLLM != "test content" {
		t.Errorf("Expected ForLLM 'test content', got '%s'", result.ForLLM)
	}
	if result.Silent {
		t.Error("Expected Silent to be false")
	}
	if result.IsError {
		t.Error("Expected IsError to be false")
	}
	if result.Async {
		t.Error("Expected Async to be false")
	}
}

func TestSilentResult(t *testing.T) {
	result := SilentResult("silent operation")

	if result.ForLLM != "silent operation" {
		t.Errorf("Expected ForLLM 'silent operation', got '%s'", result.ForLLM)
	}
	if !result.Silent {
		t.Error("Expected Silent to be true")
	}
	if result.IsError {
		t.Error("Expected IsError to be false")
	}
	if result.Async {
		t.Error("Expected Async to be false")
	}
}

func TestAsyncResult(t *testing.T) {
	result := AsyncResult("async task started")

	if result.ForLLM != "async task started" {
		t.Errorf("Expected ForLLM 'async task started', got '%s'", result.ForLLM)
	}
	if result.Silent {
		t.Error("Expected Silent to be false")
	}
	if result.IsError {
		t.Error("Expected IsError to be false")
	}
	if !result.Async {
		t.Error("Expected Async to be true")
	}
}

func TestErrorResult(t *testing.T) {
	result := ErrorResult("operation failed")

	if result.ForLLM != "operation failed" {
		t.Errorf("Expected ForLLM 'operation failed', got '%s'", result.ForLLM)
	}
	if result.Silent {
		t.Error("Expected Silent to be false")
	}
	if !result.IsError {
		t.Error("Expected IsError to be true")
	}
	if result.Async {
		t.Error("Expected Async to be false")
	}
}

func TestUserResult(t *testing.T) {
	content := "user visible message"
	result := UserResult(content)

	if result.ForLLM != content {
		t.Errorf("Expected ForLLM '%s', got '%s'", content, result.ForLLM)
	}
	if result.ForUser != content {
		t.Errorf("Expected ForUser '%s', got '%s'", content, result.ForUser)
	}
	if result.Silent {
		t.Error("Expected Silent to be false")
	}
	if result.IsError {
		t.Error("Expected IsError to be false")
	}
	if result.Async {
		t.Error("Expected Async to be false")
	}
}

func TestToolResultJSONSerialization(t *testing.T) {
	tests := []struct {
		name   string
		result *ToolResult
	}{
		{
			name:   "basic result",
			result: NewToolResult("basic content"),
		},
		{
			name:   "silent result",
			result: SilentResult("silent content"),
		},
		{
			name:   "async result",
			result: AsyncResult("async content"),
		},
		{
			name:   "error result",
			result: ErrorResult("error content"),
		},
		{
			name:   "user result",
			result: UserResult("user content"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal to JSON
			data, err := json.Marshal(tt.result)
			if err != nil {
				t.Fatalf("Failed to marshal: %v", err)
			}

			// Unmarshal back
			var decoded ToolResult
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}

			// Verify fields match (Err should be excluded)
			if decoded.ForLLM != tt.result.ForLLM {
				t.Errorf("ForLLM mismatch: got '%s', want '%s'", decoded.ForLLM, tt.result.ForLLM)
			}
			if decoded.ForUser != tt.result.ForUser {
				t.Errorf("ForUser mismatch: got '%s', want '%s'", decoded.ForUser, tt.result.ForUser)
			}
			if decoded.Silent != tt.result.Silent {
				t.Errorf("Silent mismatch: got %v, want %v", decoded.Silent, tt.result.Silent)
			}
			if decoded.IsError != tt.result.IsError {
				t.Errorf("IsError mismatch: got %v, want %v", decoded.IsError, tt.result.IsError)
			}
			if decoded.Async != tt.result.Async {
				t.Errorf("Async mismatch: got %v, want %v", decoded.Async, tt.result.Async)
			}
		})
	}
}

func TestToolResultWithErrors(t *testing.T) {
	err := errors.New("underlying error")
	result := ErrorResult("error message").WithError(err)

	if result.Err == nil {
		t.Error("Expected Err to be set")
	}
	if result.Err.Error() != "underlying error" {
		t.Errorf("Expected Err message 'underlying error', got '%s'", result.Err.Error())
	}

	// Verify Err is not serialized
	data, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatalf("Failed to marshal: %v", marshalErr)
	}

	var decoded ToolResult
	if unmarshalErr := json.Unmarshal(data, &decoded); unmarshalErr != nil {
		t.Fatalf("Failed to unmarshal: %v", unmarshalErr)
	}

	if decoded.Err != nil {
		t.Error("Expected Err to be nil after JSON round-trip (should not be serialized)")
	}
}

func TestToolResultJSONStructure(t *testing.T) {
	result := UserResult("test content")

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	// Verify JSON structure
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	// Check expected keys exist
	if _, ok := parsed["for_llm"]; !ok {
		t.Error("Expected 'for_llm' key in JSON")
	}
	if _, ok := parsed["for_user"]; !ok {
		t.Error("Expected 'for_user' key in JSON")
	}
	if _, ok := parsed["silent"]; !ok {
		t.Error("Expected 'silent' key in JSON")
	}
	if _, ok := parsed["is_error"]; !ok {
		t.Error("Expected 'is_error' key in JSON")
	}
	if _, ok := parsed["async"]; !ok {
		t.Error("Expected 'async' key in JSON")
	}

	// Check that 'err' is NOT present (it should have json:"-" tag)
	if _, ok := parsed["err"]; ok {
		t.Error("Expected 'err' key to be excluded from JSON")
	}

	// Verify values
	if parsed["for_llm"] != "test content" {
		t.Errorf("Expected for_llm 'test content', got %v", parsed["for_llm"])
	}
	if parsed["silent"] != false {
		t.Errorf("Expected silent false, got %v", parsed["silent"])
	}
}

func TestMessageTool_Execute_Success(t *testing.T) {
	tool := NewMessageTool()

	var sentChannel, sentChatID, sentContent string
	tool.SetSendCallback(func(channel, chatID, content string) error {
		sentChannel = channel
		sentChatID = chatID
		sentContent = content
		return nil
	})

	ctx := withExecutionContext(context.Background(), "test-channel", "test-chat-id", "")
	args := map[string]any{
		"content": "Hello, world!",
	}

	result := tool.Execute(ctx, args)

	// Verify message was sent with correct parameters
	if sentChannel != "test-channel" {
		t.Errorf("Expected channel 'test-channel', got '%s'", sentChannel)
	}
	if sentChatID != "test-chat-id" {
		t.Errorf("Expected chatID 'test-chat-id', got '%s'", sentChatID)
	}
	if sentContent != "Hello, world!" {
		t.Errorf("Expected content 'Hello, world!', got '%s'", sentContent)
	}

	// Verify ToolResult meets US-011 criteria:
	// - Send success returns SilentResult (Silent=true)
	if !result.Silent {
		t.Error("Expected Silent=true for successful send")
	}

	// - ForLLM contains send status description
	if result.ForLLM != "Message sent to test-channel:test-chat-id" {
		t.Errorf("Expected ForLLM 'Message sent to test-channel:test-chat-id', got '%s'", result.ForLLM)
	}

	// - ForUser is empty (user already received message directly)
	if result.ForUser != "" {
		t.Errorf("Expected ForUser to be empty, got '%s'", result.ForUser)
	}

	// - IsError should be false
	if result.IsError {
		t.Error("Expected IsError=false for successful send")
	}
}

func TestMessageTool_Execute_WithCustomChannel(t *testing.T) {
	tool := NewMessageTool()

	var sentChannel, sentChatID string
	tool.SetSendCallback(func(channel, chatID, content string) error {
		sentChannel = channel
		sentChatID = chatID
		return nil
	})

	ctx := withExecutionContext(context.Background(), "default-channel", "default-chat-id", "")
	args := map[string]any{
		"content": "Test message",
		"channel": "custom-channel",
		"chat_id": "custom-chat-id",
	}

	result := tool.Execute(ctx, args)

	// Verify custom channel/chatID were used instead of defaults
	if sentChannel != "custom-channel" {
		t.Errorf("Expected channel 'custom-channel', got '%s'", sentChannel)
	}
	if sentChatID != "custom-chat-id" {
		t.Errorf("Expected chatID 'custom-chat-id', got '%s'", sentChatID)
	}

	if !result.Silent {
		t.Error("Expected Silent=true")
	}
	if result.ForLLM != "Message sent to custom-channel:custom-chat-id" {
		t.Errorf("Expected ForLLM 'Message sent to custom-channel:custom-chat-id', got '%s'", result.ForLLM)
	}
}

func TestMessageTool_Execute_SendFailure(t *testing.T) {
	tool := NewMessageTool()

	sendErr := errors.New("network error")
	tool.SetSendCallback(func(channel, chatID, content string) error {
		return sendErr
	})

	ctx := withExecutionContext(context.Background(), "test-channel", "test-chat-id", "")
	args := map[string]any{
		"content": "Test message",
	}

	result := tool.Execute(ctx, args)

	// Verify ToolResult for send failure:
	// - Send failure returns ErrorResult (IsError=true)
	if !result.IsError {
		t.Error("Expected IsError=true for failed send")
	}

	// - ForLLM contains error description
	expectedErrMsg := "sending message: network error"
	if result.ForLLM != expectedErrMsg {
		t.Errorf("Expected ForLLM '%s', got '%s'", expectedErrMsg, result.ForLLM)
	}

	// - Err field should contain original error
	if result.Err == nil {
		t.Error("Expected Err to be set")
	}
	if result.Err != sendErr {
		t.Errorf("Expected Err to be sendErr, got %v", result.Err)
	}
}

func TestMessageTool_Execute_MissingContent(t *testing.T) {
	tool := NewMessageTool()

	ctx := withExecutionContext(context.Background(), "test-channel", "test-chat-id", "")
	args := map[string]any{} // content missing

	result := tool.Execute(ctx, args)

	// Verify error result for missing content
	if !result.IsError {
		t.Error("Expected IsError=true for missing content")
	}
	if result.ForLLM != "content is required" {
		t.Errorf("Expected ForLLM 'content is required', got '%s'", result.ForLLM)
	}
}

func TestMessageTool_Execute_NoTargetChannel(t *testing.T) {
	tool := NewMessageTool()

	tool.SetSendCallback(func(channel, chatID, content string) error {
		return nil
	})

	ctx := context.Background()
	args := map[string]any{
		"content": "Test message",
	}

	result := tool.Execute(ctx, args)

	// Verify error when no target channel specified
	if !result.IsError {
		t.Error("Expected IsError=true when no target channel")
	}
	if result.ForLLM != "No target channel/chat specified" {
		t.Errorf("Expected ForLLM 'No target channel/chat specified', got '%s'", result.ForLLM)
	}
}

func TestMessageTool_Execute_NotConfigured(t *testing.T) {
	tool := NewMessageTool()
	// No SetSendCallback called

	ctx := withExecutionContext(context.Background(), "test-channel", "test-chat-id", "")
	args := map[string]any{
		"content": "Test message",
	}

	result := tool.Execute(ctx, args)

	// Verify error when send callback not configured
	if !result.IsError {
		t.Error("Expected IsError=true when send callback not configured")
	}
	if result.ForLLM != "Message sending not configured" {
		t.Errorf("Expected ForLLM 'Message sending not configured', got '%s'", result.ForLLM)
	}
}

func TestMessageTool_Name(t *testing.T) {
	tool := NewMessageTool()
	if tool.Name() != "message" {
		t.Errorf("Expected name 'message', got '%s'", tool.Name())
	}
}

func TestMessageTool_Description(t *testing.T) {
	tool := NewMessageTool()
	desc := tool.Description()
	if desc == "" {
		t.Error("Description should not be empty")
	}
}

func TestMessageTool_Parameters(t *testing.T) {
	tool := NewMessageTool()
	params := tool.Parameters()

	// Verify parameters structure
	typ, ok := params["type"].(string)
	if !ok || typ != "object" {
		t.Error("Expected type 'object'")
	}

	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("Expected properties to be a map")
	}

	// Check required properties
	required, ok := params["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "content" {
		t.Error("Expected 'content' to be required")
	}

	// Check content property
	contentProp, ok := props["content"].(map[string]any)
	if !ok {
		t.Error("Expected 'content' property")
	}
	if contentProp["type"] != "string" {
		t.Error("Expected content type to be 'string'")
	}

	// Check channel property (optional)
	channelProp, ok := props["channel"].(map[string]any)
	if !ok {
		t.Error("Expected 'channel' property")
	}
	if channelProp["type"] != "string" {
		t.Error("Expected channel type to be 'string'")
	}

	// Check chat_id property (optional)
	chatIDProp, ok := props["chat_id"].(map[string]any)
	if !ok {
		t.Error("Expected 'chat_id' property")
	}
	if chatIDProp["type"] != "string" {
		t.Error("Expected chat_id type to be 'string'")
	}
}
