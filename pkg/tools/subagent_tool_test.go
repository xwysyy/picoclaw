package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/providers"
)

// MockLLMProvider is a test implementation of LLMProvider
type MockLLMProvider struct {
	lastOptions map[string]any
}

type staticMockProvider struct {
	content string
}

func (m *staticMockProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: m.content}, nil
}

func (m *staticMockProvider) GetDefaultModel() string {
	return "static-model"
}

func (m *MockLLMProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	m.lastOptions = options
	// Find the last user message to generate a response
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return &providers.LLMResponse{
				Content: "Task completed: " + messages[i].Content,
			}, nil
		}
	}
	return &providers.LLMResponse{Content: "No task provided"}, nil
}

func (m *MockLLMProvider) GetDefaultModel() string {
	return "test-model"
}

func (m *MockLLMProvider) SupportsTools() bool {
	return false
}

func (m *MockLLMProvider) GetContextWindow() int {
	return 4096
}

func TestSubagentManager_SetLLMOptions_AppliesToRunToolLoop(t *testing.T) {
	provider := &MockLLMProvider{}
	manager := NewSubagentManager(provider, "test-model", "/tmp/test", nil)
	manager.SetLLMOptions(2048, 0.6)
	tool := NewSubagentTool(manager)

	ctx := withExecutionContext(context.Background(), "cli", "direct", "")
	args := map[string]any{"task": "Do something"}
	result := tool.Execute(ctx, args)

	if result == nil || result.IsError {
		t.Fatalf("Expected successful result, got: %+v", result)
	}

	if provider.lastOptions == nil {
		t.Fatal("Expected LLM options to be passed, got nil")
	}
	if provider.lastOptions["max_tokens"] != 2048 {
		t.Fatalf("max_tokens = %v, want %d", provider.lastOptions["max_tokens"], 2048)
	}
	if provider.lastOptions["temperature"] != 0.6 {
		t.Fatalf("temperature = %v, want %v", provider.lastOptions["temperature"], 0.6)
	}
}

// TestSubagentTool_Name verifies tool name
func TestSubagentTool_Name(t *testing.T) {
	provider := &MockLLMProvider{}
	manager := NewSubagentManager(provider, "test-model", "/tmp/test", nil)
	tool := NewSubagentTool(manager)

	if tool.Name() != "subagent" {
		t.Errorf("Expected name 'subagent', got '%s'", tool.Name())
	}
}

// TestSubagentTool_Description verifies tool description
func TestSubagentTool_Description(t *testing.T) {
	provider := &MockLLMProvider{}
	manager := NewSubagentManager(provider, "test-model", "/tmp/test", nil)
	tool := NewSubagentTool(manager)

	desc := tool.Description()
	if desc == "" {
		t.Error("Description should not be empty")
	}
	if !strings.Contains(desc, "subagent") {
		t.Errorf("Description should mention 'subagent', got: %s", desc)
	}
}

// TestSubagentTool_Parameters verifies tool parameters schema
func TestSubagentTool_Parameters(t *testing.T) {
	provider := &MockLLMProvider{}
	manager := NewSubagentManager(provider, "test-model", "/tmp/test", nil)
	tool := NewSubagentTool(manager)

	params := tool.Parameters()
	if params == nil {
		t.Error("Parameters should not be nil")
	}

	// Check type
	if params["type"] != "object" {
		t.Errorf("Expected type 'object', got: %v", params["type"])
	}

	// Check properties
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatal("Properties should be a map")
	}

	// Verify task parameter
	task, ok := props["task"].(map[string]any)
	if !ok {
		t.Fatal("Task parameter should exist")
	}
	if task["type"] != "string" {
		t.Errorf("Task type should be 'string', got: %v", task["type"])
	}

	// Verify label parameter
	label, ok := props["label"].(map[string]any)
	if !ok {
		t.Fatal("Label parameter should exist")
	}
	if label["type"] != "string" {
		t.Errorf("Label type should be 'string', got: %v", label["type"])
	}

	// Check required fields
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatal("Required should be a string array")
	}
	if len(required) != 1 || required[0] != "task" {
		t.Errorf("Required should be ['task'], got: %v", required)
	}
}

// TestSubagentTool_Execute_Success tests successful execution
func TestSubagentTool_Execute_Success(t *testing.T) {
	provider := &MockLLMProvider{}
	msgBus := bus.NewMessageBus()
	manager := NewSubagentManager(provider, "test-model", "/tmp/test", msgBus)
	tool := NewSubagentTool(manager)

	ctx := withExecutionContext(context.Background(), "telegram", "chat-123", "")
	args := map[string]any{
		"task":  "Write a haiku about coding",
		"label": "haiku-task",
	}

	result := tool.Execute(ctx, args)

	// Verify basic ToolResult structure
	if result == nil {
		t.Fatal("Result should not be nil")
	}

	// Verify no error
	if result.IsError {
		t.Errorf("Expected success, got error: %s", result.ForLLM)
	}

	// Verify not async
	if result.Async {
		t.Error("SubagentTool should be synchronous, not async")
	}

	// Verify not silent
	if result.Silent {
		t.Error("SubagentTool should not be silent")
	}

	// Verify ForUser contains brief summary (not empty)
	if result.ForUser == "" {
		t.Error("ForUser should contain result summary")
	}
	if !strings.Contains(result.ForUser, "Task completed") {
		t.Errorf("ForUser should contain task completion, got: %s", result.ForUser)
	}

	// Verify ForLLM is structured JSON payload
	if result.ForLLM == "" {
		t.Error("ForLLM should contain full details")
	}
	var payload SubagentResultPayload
	if err := json.Unmarshal([]byte(result.ForLLM), &payload); err != nil {
		t.Fatalf("ForLLM should be JSON payload, decode failed: %v\npayload=%s", err, result.ForLLM)
	}
	if payload.Kind != "subagent_result" {
		t.Fatalf("payload.kind = %q, want %q", payload.Kind, "subagent_result")
	}
	if payload.Status != "completed" {
		t.Fatalf("payload.status = %q, want %q", payload.Status, "completed")
	}
	if payload.Mode != "sync" {
		t.Fatalf("payload.mode = %q, want %q", payload.Mode, "sync")
	}
	if payload.Label != "haiku-task" {
		t.Fatalf("payload.label = %q, want %q", payload.Label, "haiku-task")
	}
	if !strings.Contains(payload.Summary, "Task completed:") {
		t.Fatalf("payload.summary should contain task result, got: %q", payload.Summary)
	}
}

// TestSubagentTool_Execute_NoLabel tests execution without label
func TestSubagentTool_Execute_NoLabel(t *testing.T) {
	provider := &MockLLMProvider{}
	msgBus := bus.NewMessageBus()
	manager := NewSubagentManager(provider, "test-model", "/tmp/test", msgBus)
	tool := NewSubagentTool(manager)

	ctx := context.Background()
	args := map[string]any{
		"task": "Test task without label",
	}

	result := tool.Execute(ctx, args)

	if result.IsError {
		t.Errorf("Expected success without label, got error: %s", result.ForLLM)
	}

	var payload SubagentResultPayload
	if err := json.Unmarshal([]byte(result.ForLLM), &payload); err != nil {
		t.Fatalf("decode subagent payload: %v", err)
	}
	if payload.Label != "(unnamed)" {
		t.Fatalf("payload.label = %q, want %q", payload.Label, "(unnamed)")
	}
}

// TestSubagentTool_Execute_MissingTask tests error handling for missing task
func TestSubagentTool_Execute_MissingTask(t *testing.T) {
	provider := &MockLLMProvider{}
	manager := NewSubagentManager(provider, "test-model", "/tmp/test", nil)
	tool := NewSubagentTool(manager)

	ctx := context.Background()
	args := map[string]any{
		"label": "test",
	}

	result := tool.Execute(ctx, args)

	// Should return error
	if !result.IsError {
		t.Error("Expected error for missing task parameter")
	}

	// ForLLM should contain error message
	if !strings.Contains(result.ForLLM, "task is required") {
		t.Errorf("Error message should mention 'task is required', got: %s", result.ForLLM)
	}

	// Err should be set
	if result.Err == nil {
		t.Error("Err should be set for validation failure")
	}
}

// TestSubagentTool_Execute_NilManager tests error handling for nil manager
func TestSubagentTool_Execute_NilManager(t *testing.T) {
	tool := NewSubagentTool(nil)

	ctx := context.Background()
	args := map[string]any{
		"task": "test task",
	}

	result := tool.Execute(ctx, args)

	// Should return error
	if !result.IsError {
		t.Error("Expected error for nil manager")
	}

	if !strings.Contains(result.ForLLM, "Subagent manager not configured") {
		t.Errorf("Error message should mention manager not configured, got: %s", result.ForLLM)
	}
}

// TestSubagentTool_Execute_ContextPassing verifies context is properly used
func TestSubagentTool_Execute_ContextPassing(t *testing.T) {
	provider := &MockLLMProvider{}
	msgBus := bus.NewMessageBus()
	manager := NewSubagentManager(provider, "test-model", "/tmp/test", msgBus)
	tool := NewSubagentTool(manager)

	channel := "test-channel"
	chatID := "test-chat"
	ctx := withExecutionContext(context.Background(), channel, chatID, "")
	args := map[string]any{
		"task": "Test context passing",
	}

	result := tool.Execute(ctx, args)

	// Should succeed
	if result.IsError {
		t.Errorf("Expected success with context, got error: %s", result.ForLLM)
	}

	// The context is used internally; we can't directly test it
	// but execution success indicates context was handled properly
}

// TestSubagentTool_ForUserTruncation verifies long content is truncated for user
func TestSubagentTool_ForUserTruncation(t *testing.T) {
	// Create a mock provider that returns very long content
	provider := &MockLLMProvider{}
	msgBus := bus.NewMessageBus()
	manager := NewSubagentManager(provider, "test-model", "/tmp/test", msgBus)
	tool := NewSubagentTool(manager)

	ctx := context.Background()

	// Create a task that will generate long response
	longTask := strings.Repeat("This is a very long task description. ", 100)
	args := map[string]any{
		"task":  longTask,
		"label": "long-test",
	}

	result := tool.Execute(ctx, args)

	// ForUser should be truncated to 500 chars + "..."
	maxUserLen := 500
	if len(result.ForUser) > maxUserLen+3 { // +3 for "..."
		t.Errorf("ForUser should be truncated to ~%d chars, got: %d", maxUserLen, len(result.ForUser))
	}

	// ForLLM should have full content
	if !strings.Contains(result.ForLLM, longTask[:50]) {
		t.Error("ForLLM should contain reference to original task")
	}
}

func TestSubagentManager_ExecutionResolverUsedBySpawn(t *testing.T) {
	defaultProvider := &staticMockProvider{content: "default-provider-result"}
	manager := NewSubagentManager(defaultProvider, "default-model", t.TempDir(), nil)

	var resolvedAgentID string
	manager.SetExecutionResolver(func(targetAgentID string) (SubagentExecutionConfig, error) {
		resolvedAgentID = targetAgentID
		return SubagentExecutionConfig{
			Provider: &staticMockProvider{content: "resolved-provider-result"},
			Model:    "resolved-model",
			Tools:    NewToolRegistry(),
		}, nil
	})

	done := make(chan *ToolResult, 1)
	_, err := manager.Spawn(
		context.Background(),
		"test task",
		"resolver-test",
		"agent-worker",
		"cli",
		"direct",
		func(ctx context.Context, result *ToolResult) {
			done <- result
		},
	)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}

	select {
	case result := <-done:
		if result == nil {
			t.Fatal("expected callback result")
		}
		if result.IsError {
			t.Fatalf("expected success result, got error: %s", result.ForLLM)
		}
		if !strings.Contains(result.ForUser, "resolved-provider-result") {
			t.Fatalf("expected resolved provider result, got: %s", result.ForUser)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for spawn callback")
	}

	if resolvedAgentID != "agent-worker" {
		t.Fatalf("resolver target agent id = %q, want %q", resolvedAgentID, "agent-worker")
	}
}

func TestSubagentManager_EventHandlerLifecycle(t *testing.T) {
	manager := NewSubagentManager(&staticMockProvider{content: "ok"}, "default-model", t.TempDir(), nil)
	manager.SetExecutionResolver(func(targetAgentID string) (SubagentExecutionConfig, error) {
		return SubagentExecutionConfig{
			Provider: &staticMockProvider{content: "ok"},
			Model:    "model",
			Tools:    NewToolRegistry(),
		}, nil
	})

	var mu sync.Mutex
	seen := map[SubagentTaskEventType]bool{}
	manager.SetEventHandler(func(event SubagentTaskEvent) {
		mu.Lock()
		defer mu.Unlock()
		seen[event.Type] = true
	})

	done := make(chan struct{}, 1)
	_, err := manager.Spawn(
		context.Background(),
		"event task",
		"event-test",
		"",
		"cli",
		"direct",
		func(ctx context.Context, result *ToolResult) {
			done <- struct{}{}
		},
	)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for spawn callback")
	}

	mu.Lock()
	defer mu.Unlock()
	if !seen[SubagentTaskCreated] {
		t.Fatalf("expected created event, got %+v", seen)
	}
	if !seen[SubagentTaskRunning] {
		t.Fatalf("expected running event, got %+v", seen)
	}
	if !seen[SubagentTaskCompleted] {
		t.Fatalf("expected completed event, got %+v", seen)
	}
}
