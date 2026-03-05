package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xwysyy/picoclaw/pkg/config"
	"github.com/xwysyy/picoclaw/pkg/providers"
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

func TestExecuteToolCalls_EstopKillAllDeniesTools(t *testing.T) {
	workspace := t.TempDir()
	if _, err := SaveEstopState(workspace, EstopState{Mode: EstopModeKillAll}); err != nil {
		t.Fatalf("failed to save estop state: %v", err)
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
		t.Fatalf("expected tool not to execute under estop kill_all, executed=%d", executed.Load())
	}
	if len(results) != 1 || results[0].Result == nil {
		t.Fatalf("unexpected results: %+v", results)
	}
	if !results[0].Result.IsError {
		t.Fatalf("expected IsError=true, got %+v", results[0].Result)
	}
	if !strings.Contains(results[0].Result.ForLLM, "ESTOP_DENY") {
		t.Fatalf("expected ESTOP_DENY message, got: %q", results[0].Result.ForLLM)
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

func TestExecuteToolCalls_PlanModeDeniesRestrictedTools(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(&executorMockTool{
		name:   "exec",
		result: SilentResult("should-not-run"),
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
		t.Fatalf("expected IsError=true, got false (%q)", results[0].Result.ForLLM)
	}
	if !strings.Contains(results[0].Result.ForLLM, "PLAN_MODE_DENY") {
		t.Fatalf("expected PLAN_MODE_DENY, got %q", results[0].Result.ForLLM)
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
