package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/channels"
	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/media"
	"github.com/xwysyy/X-Claw/pkg/providers"
	"github.com/xwysyy/X-Claw/pkg/routing"
	"github.com/xwysyy/X-Claw/pkg/tools"
)

type fakeChannel struct{ id string }

func (f *fakeChannel) Name() string                                            { return "fake" }
func (f *fakeChannel) Start(ctx context.Context) error                         { return nil }
func (f *fakeChannel) Stop(ctx context.Context) error                          { return nil }
func (f *fakeChannel) Send(ctx context.Context, msg bus.OutboundMessage) error { return nil }
func (f *fakeChannel) IsRunning() bool                                         { return true }
func (f *fakeChannel) IsAllowed(string) bool                                   { return true }
func (f *fakeChannel) IsAllowedSender(sender bus.SenderInfo) bool              { return true }
func (f *fakeChannel) ReasoningChannelID() string                              { return f.id }

func newTestAgentLoop(
	t *testing.T,
) (al *AgentLoop, cfg *config.Config, msgBus *bus.MessageBus, provider *mockProvider, cleanup func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	cfg = &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}
	msgBus = bus.NewMessageBus()
	provider = &mockProvider{}
	al = NewAgentLoop(cfg, msgBus, provider)
	return al, cfg, msgBus, provider, func() { os.RemoveAll(tmpDir) }
}

func TestRecordLastChannel(t *testing.T) {
	al, cfg, msgBus, provider, cleanup := newTestAgentLoop(t)
	defer cleanup()

	testChannel := "test-channel"
	if err := al.RecordLastChannel(testChannel); err != nil {
		t.Fatalf("RecordLastChannel failed: %v", err)
	}
	if got := al.state.GetLastChannel(); got != testChannel {
		t.Errorf("Expected channel '%s', got '%s'", testChannel, got)
	}
	al2 := NewAgentLoop(cfg, msgBus, provider)
	if got := al2.state.GetLastChannel(); got != testChannel {
		t.Errorf("Expected persistent channel '%s', got '%s'", testChannel, got)
	}
}

func TestRecordLastChatID(t *testing.T) {
	al, cfg, msgBus, provider, cleanup := newTestAgentLoop(t)
	defer cleanup()

	testChatID := "test-chat-id-123"
	if err := al.RecordLastChatID(testChatID); err != nil {
		t.Fatalf("RecordLastChatID failed: %v", err)
	}
	if got := al.state.GetLastChatID(); got != testChatID {
		t.Errorf("Expected chat ID '%s', got '%s'", testChatID, got)
	}
	al2 := NewAgentLoop(cfg, msgBus, provider)
	if got := al2.state.GetLastChatID(); got != testChatID {
		t.Errorf("Expected persistent chat ID '%s', got '%s'", testChatID, got)
	}
}

func TestNewAgentLoop_StateInitialized(t *testing.T) {
	// Create temp workspace
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test config
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	// Create agent loop
	msgBus := bus.NewMessageBus()
	provider := &mockProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	// Verify state manager is initialized
	if al.state == nil {
		t.Error("Expected state manager to be initialized")
	}

	// Verify state directory was created
	stateDir := filepath.Join(tmpDir, "state")
	if _, err := os.Stat(stateDir); os.IsNotExist(err) {
		t.Error("Expected state directory to exist")
	}
}

// TestToolRegistry_ToolRegistration verifies tools can be registered and retrieved
func TestToolRegistry_ToolRegistration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &mockProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	// Register a custom tool
	customTool := &mockCustomTool{}
	al.RegisterTool(customTool)

	// Verify tool is registered by checking it doesn't panic on GetStartupInfo
	// (actual tool retrieval is tested in tools package tests)
	info := al.GetStartupInfo()
	toolsInfo := info["tools"].(map[string]any)
	toolsList := toolsInfo["names"].([]string)

	// Check that our custom tool name is in the list
	found := slices.Contains(toolsList, "mock_custom")
	if !found {
		t.Error("Expected custom tool to be registered")
	}
}

// TestToolContext_Updates verifies tool context helpers work correctly
func TestToolContext_Updates(t *testing.T) {
	ctx := tools.WithToolContext(context.Background(), "telegram", "chat-42")

	if got := tools.ToolChannel(ctx); got != "telegram" {
		t.Errorf("expected channel 'telegram', got %q", got)
	}
	if got := tools.ToolChatID(ctx); got != "chat-42" {
		t.Errorf("expected chatID 'chat-42', got %q", got)
	}

	// Empty context returns empty strings
	if got := tools.ToolChannel(context.Background()); got != "" {
		t.Errorf("expected empty channel from bare context, got %q", got)
	}
}

// TestToolRegistry_GetDefinitions verifies tool definitions can be retrieved
func TestToolRegistry_GetDefinitions(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &mockProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	// Register a test tool and verify it shows up in startup info
	testTool := &mockCustomTool{}
	al.RegisterTool(testTool)

	info := al.GetStartupInfo()
	toolsInfo := info["tools"].(map[string]any)
	toolsList := toolsInfo["names"].([]string)

	// Check that our custom tool name is in the list
	found := slices.Contains(toolsList, "mock_custom")
	if !found {
		t.Error("Expected custom tool to be registered")
	}
}

// TestAgentLoop_GetStartupInfo verifies startup info contains tools
func TestAgentLoop_GetStartupInfo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = tmpDir
	cfg.Agents.Defaults.Model = "test-model"
	cfg.Agents.Defaults.MaxTokens = 4096
	cfg.Agents.Defaults.MaxToolIterations = 10

	msgBus := bus.NewMessageBus()
	provider := &mockProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	info := al.GetStartupInfo()

	// Verify tools info exists
	toolsInfo, ok := info["tools"]
	if !ok {
		t.Fatal("Expected 'tools' key in startup info")
	}

	toolsMap, ok := toolsInfo.(map[string]any)
	if !ok {
		t.Fatal("Expected 'tools' to be a map")
	}

	count, ok := toolsMap["count"]
	if !ok {
		t.Fatal("Expected 'count' in tools info")
	}

	// Should have default tools registered
	if count.(int) == 0 {
		t.Error("Expected at least some tools to be registered")
	}
}

// TestAgentLoop_Stop verifies Stop() sets running to false
func TestAgentLoop_Stop(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &mockProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	// Note: running is only set to true when Run() is called
	// We can't test that without starting the event loop
	// Instead, verify the Stop method can be called safely
	al.Stop()

	// Verify running is false (initial state or after Stop)
	if al.running.Load() {
		t.Error("Expected agent to be stopped (or never started)")
	}
}

// Mock implementations for testing

type simpleMockProvider struct {
	response string
}

func (m *simpleMockProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{
		Content:   m.response,
		ToolCalls: []providers.ToolCall{},
	}, nil
}

func (m *simpleMockProvider) GetDefaultModel() string {
	return "mock-model"
}

// mockCustomTool is a simple mock tool for registration testing
type mockCustomTool struct{}

func (m *mockCustomTool) Name() string {
	return "mock_custom"
}

func (m *mockCustomTool) Description() string {
	return "Mock custom tool for testing"
}

func (m *mockCustomTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (m *mockCustomTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	return tools.SilentResult("Custom tool executed")
}

// testHelper executes a message and returns the response
type testHelper struct {
	al *AgentLoop
}

func (h testHelper) executeAndGetResponse(tb testing.TB, ctx context.Context, msg bus.InboundMessage) string {
	// Use a short timeout to avoid hanging
	timeoutCtx, cancel := context.WithTimeout(ctx, responseTimeout)
	defer cancel()

	response, err := h.al.processMessage(timeoutCtx, msg)
	if err != nil {
		tb.Fatalf("processMessage failed: %v", err)
	}
	return response
}

const responseTimeout = 3 * time.Second

// TestToolResult_SilentToolDoesNotSendUserMessage verifies silent tools don't trigger outbound
func TestToolResult_SilentToolDoesNotSendUserMessage(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &simpleMockProvider{response: "File operation complete"}
	al := NewAgentLoop(cfg, msgBus, provider)
	helper := testHelper{al: al}

	// ReadFileTool returns SilentResult, which should not send user message
	ctx := context.Background()
	msg := bus.InboundMessage{
		Channel:    "test",
		SenderID:   "user1",
		ChatID:     "chat1",
		Content:    "read test.txt",
		SessionKey: "test-session",
	}

	response := helper.executeAndGetResponse(t, ctx, msg)

	// Silent tool should return the LLM's response directly
	if response != "File operation complete" {
		t.Errorf("Expected 'File operation complete', got: %s", response)
	}
}

// TestToolResult_UserFacingToolDoesSendMessage verifies user-facing tools trigger outbound
func TestToolResult_UserFacingToolDoesSendMessage(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &simpleMockProvider{response: "Command output: hello world"}
	al := NewAgentLoop(cfg, msgBus, provider)
	helper := testHelper{al: al}

	// ExecTool returns UserResult, which should send user message
	ctx := context.Background()
	msg := bus.InboundMessage{
		Channel:    "test",
		SenderID:   "user1",
		ChatID:     "chat1",
		Content:    "run hello",
		SessionKey: "test-session",
	}

	response := helper.executeAndGetResponse(t, ctx, msg)

	// User-facing tool should include the output in final response
	if response != "Command output: hello world" {
		t.Errorf("Expected 'Command output: hello world', got: %s", response)
	}
}

// failFirstMockProvider fails on the first N calls with a specific error
type failFirstMockProvider struct {
	failures    int
	currentCall int
	failError   error
	successResp string
}

func (m *failFirstMockProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	m.currentCall++
	if m.currentCall <= m.failures {
		return nil, m.failError
	}
	return &providers.LLMResponse{
		Content:   m.successResp,
		ToolCalls: []providers.ToolCall{},
	}, nil
}

func (m *failFirstMockProvider) GetDefaultModel() string {
	return "mock-fail-model"
}

// TestAgentLoop_ContextExhaustionRetry verify that the agent retries on context errors
func TestAgentLoop_ContextExhaustionRetry(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	msgBus := bus.NewMessageBus()

	// Create a provider that fails once with a context error
	contextErr := fmt.Errorf("InvalidParameter: Total tokens of image and text exceed max message tokens")
	provider := &failFirstMockProvider{
		failures:    1,
		failError:   contextErr,
		successResp: "Recovered from context error",
	}

	al := NewAgentLoop(cfg, msgBus, provider)

	// Inject some history to simulate a full context
	sessionKey := "test-session-context"
	// Create dummy history
	history := []providers.Message{
		{Role: "system", Content: "System prompt"},
		{Role: "user", Content: "Old message 1"},
		{Role: "assistant", Content: "Old response 1"},
		{Role: "user", Content: "Old message 2"},
		{Role: "assistant", Content: "Old response 2"},
		{Role: "user", Content: "Trigger message"},
	}
	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("No default agent found")
	}
	defaultAgent.Sessions.SetHistory(sessionKey, history)

	// Call ProcessDirectWithChannel
	// Note: ProcessDirectWithChannel calls processMessage which will execute runLLMIteration
	response, err := al.ProcessDirectWithChannel(
		context.Background(),
		"Trigger message",
		sessionKey,
		"test",
		"test-chat",
	)
	if err != nil {
		t.Fatalf("Expected success after retry, got error: %v", err)
	}

	if response != "Recovered from context error" {
		t.Errorf("Expected 'Recovered from context error', got '%s'", response)
	}

	// We expect at least 2 calls:
	// 1) initial failed request
	// 2) retry request succeeds (with or without compaction summary call)
	if provider.currentCall < 2 {
		t.Errorf("Expected at least 2 calls after retry, got %d", provider.currentCall)
	}

	// Check final history length
	finalHistory := defaultAgent.Sessions.GetHistory(sessionKey)
	// We verify that the history has been modified (compressed)
	// Original length: 6
	// Expected behavior: compression drops ~50% of history (mid slice)
	// We can assert that the length is NOT what it would be without compression.
	// Without compression: 6 + 1 (new user msg) + 1 (assistant msg) = 8
	if len(finalHistory) >= 8 {
		t.Errorf("Expected history to be compressed (len < 8), got %d", len(finalHistory))
	}
}

func TestTargetReasoningChannelID_AllChannels(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	al := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{})
	chManager, err := channels.NewManager(&config.Config{}, bus.NewMessageBus(), nil)
	if err != nil {
		t.Fatalf("Failed to create channel manager: %v", err)
	}
	for name, id := range map[string]string{
		"whatsapp":  "rid-whatsapp",
		"telegram":  "rid-telegram",
		"feishu":    "rid-feishu",
		"discord":   "rid-discord",
		"qq":        "rid-qq",
		"dingtalk":  "rid-dingtalk",
		"slack":     "rid-slack",
		"line":      "rid-line",
		"onebot":    "rid-onebot",
		"wecom":     "rid-wecom",
		"wecom_app": "rid-wecom-app",
	} {
		chManager.RegisterChannel(name, &fakeChannel{id: id})
	}
	al.SetChannelManager(chManager)
	tests := []struct {
		channel string
		wantID  string
	}{
		{channel: "whatsapp", wantID: "rid-whatsapp"},
		{channel: "telegram", wantID: "rid-telegram"},
		{channel: "feishu", wantID: "rid-feishu"},
		{channel: "discord", wantID: "rid-discord"},
		{channel: "qq", wantID: "rid-qq"},
		{channel: "dingtalk", wantID: "rid-dingtalk"},
		{channel: "slack", wantID: "rid-slack"},
		{channel: "line", wantID: "rid-line"},
		{channel: "onebot", wantID: "rid-onebot"},
		{channel: "wecom", wantID: "rid-wecom"},
		{channel: "wecom_app", wantID: "rid-wecom-app"},
		{channel: "unknown", wantID: ""},
	}

	for _, tt := range tests {
		t.Run(tt.channel, func(t *testing.T) {
			got := al.targetReasoningChannelID(tt.channel)
			if got != tt.wantID {
				t.Fatalf("targetReasoningChannelID(%q) = %q, want %q", tt.channel, got, tt.wantID)
			}
		})
	}
}

func TestHandleReasoning(t *testing.T) {
	newLoop := func(t *testing.T) (*AgentLoop, *bus.MessageBus) {
		t.Helper()
		tmpDir, err := os.MkdirTemp("", "agent-test-*")
		if err != nil {
			t.Fatalf("Failed to create temp dir: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
		cfg := &config.Config{
			Agents: config.AgentsConfig{
				Defaults: config.AgentDefaults{
					Workspace:         tmpDir,
					Model:             "test-model",
					MaxTokens:         4096,
					MaxToolIterations: 10,
				},
			},
		}
		msgBus := bus.NewMessageBus()
		return NewAgentLoop(cfg, msgBus, &mockProvider{}), msgBus
	}

	t.Run("skips when any required field is empty", func(t *testing.T) {
		al, msgBus := newLoop(t)
		al.handleReasoning(context.Background(), "reasoning", "telegram", "")

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if msg, ok := msgBus.SubscribeOutbound(ctx); ok {
			t.Fatalf("expected no outbound message, got %+v", msg)
		}
	})

	t.Run("publishes one message for non telegram", func(t *testing.T) {
		al, msgBus := newLoop(t)
		al.handleReasoning(context.Background(), "hello reasoning", "slack", "channel-1")

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		msg, ok := msgBus.SubscribeOutbound(ctx)
		if !ok {
			t.Fatal("expected an outbound message")
		}
		if msg.Channel != "slack" || msg.ChatID != "channel-1" || msg.Content != "hello reasoning" {
			t.Fatalf("unexpected outbound message: %+v", msg)
		}
	})

	t.Run("publishes one message for telegram", func(t *testing.T) {
		al, msgBus := newLoop(t)
		reasoning := "hello telegram reasoning"
		al.handleReasoning(context.Background(), reasoning, "telegram", "tg-chat")

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		msg, ok := msgBus.SubscribeOutbound(ctx)
		if !ok {
			t.Fatal("expected outbound message")
		}

		if msg.Channel != "telegram" {
			t.Fatalf("expected telegram channel message, got %+v", msg)
		}
		if msg.ChatID != "tg-chat" {
			t.Fatalf("expected chatID tg-chat, got %+v", msg)
		}
		if msg.Content != reasoning {
			t.Fatalf("content mismatch: got %q want %q", msg.Content, reasoning)
		}
	})
	t.Run("expired ctx", func(t *testing.T) {
		al, msgBus := newLoop(t)
		reasoning := "hello telegram reasoning"
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		al.handleReasoning(ctx, reasoning, "telegram", "tg-chat")

		ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		msg, ok := msgBus.SubscribeOutbound(ctx)
		if ok {
			t.Fatalf("expected no outbound message, got %+v", msg)
		}
	})

	t.Run("returns promptly when bus is full", func(t *testing.T) {
		al, msgBus := newLoop(t)

		// Fill the outbound bus buffer until a publish would block.
		// Use a short timeout to detect when the buffer is full,
		// rather than hardcoding the buffer size.
		for i := 0; ; i++ {
			fillCtx, fillCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			err := msgBus.PublishOutbound(fillCtx, bus.OutboundMessage{
				Channel: "filler",
				ChatID:  "filler",
				Content: fmt.Sprintf("filler-%d", i),
			})
			fillCancel()
			if err != nil {
				// Buffer is full (timed out trying to send).
				break
			}
		}

		// Use a short-deadline parent context to bound the test.
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		start := time.Now()
		al.handleReasoning(ctx, "should timeout", "slack", "channel-full")
		elapsed := time.Since(start)

		// handleReasoning uses a 5s internal timeout, but the parent ctx
		// expires in 500ms. It should return within ~500ms, not 5s.
		if elapsed > 2*time.Second {
			t.Fatalf("handleReasoning blocked too long (%v); expected prompt return", elapsed)
		}

		// Drain the bus and verify the reasoning message was NOT published
		// (it should have been dropped due to timeout).
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer drainCancel()
		foundReasoning := false
		for {
			msg, ok := msgBus.SubscribeOutbound(drainCtx)
			if !ok {
				break
			}
			if msg.Content == "should timeout" {
				foundReasoning = true
			}
		}
		if foundReasoning {
			t.Fatal("expected reasoning message to be dropped when bus is full, but it was published")
		}
	})
}

func TestResolveMediaRefs_ResolvesToBase64(t *testing.T) {
	store := media.NewFileMediaStore()
	dir := t.TempDir()

	// Create a minimal valid PNG (8-byte header is enough for filetype detection)
	pngPath := filepath.Join(dir, "test.png")
	// PNG magic: 0x89 P N G \r \n 0x1A \n + minimal IHDR
	pngHeader := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
		0x00, 0x00, 0x00, 0x0D, // IHDR length
		0x49, 0x48, 0x44, 0x52, // "IHDR"
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02, // 1x1 RGB
		0x00, 0x00, 0x00, // no interlace
		0x90, 0x77, 0x53, 0xDE, // CRC
	}
	if err := os.WriteFile(pngPath, pngHeader, 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(pngPath, media.MediaMeta{}, "test")
	if err != nil {
		t.Fatal(err)
	}

	messages := []providers.Message{
		{Role: "user", Content: "describe this", Media: []string{ref}},
	}
	result := resolveMediaRefs(messages, media.AsMediaResolver(store), config.DefaultMaxMediaSize)

	if len(result[0].Media) != 1 {
		t.Fatalf("expected 1 resolved media, got %d", len(result[0].Media))
	}
	if !strings.HasPrefix(result[0].Media[0], "data:image/png;base64,") {
		t.Fatalf("expected data:image/png;base64, prefix, got %q", result[0].Media[0][:40])
	}
}

func TestResolveMediaRefs_SkipsOversizedFile(t *testing.T) {
	store := media.NewFileMediaStore()
	dir := t.TempDir()

	bigPath := filepath.Join(dir, "big.png")
	// Write PNG header + padding to exceed limit
	data := make([]byte, 1024+1) // 1KB + 1 byte
	copy(data, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A})
	if err := os.WriteFile(bigPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	ref, _ := store.Store(bigPath, media.MediaMeta{}, "test")

	messages := []providers.Message{
		{Role: "user", Content: "hi", Media: []string{ref}},
	}
	// Use a tiny limit (1KB) so the file is oversized
	result := resolveMediaRefs(messages, media.AsMediaResolver(store), 1024)

	if len(result[0].Media) != 0 {
		t.Fatalf("expected 0 media (oversized), got %d", len(result[0].Media))
	}
}

func TestResolveMediaRefs_SkipsUnknownType(t *testing.T) {
	store := media.NewFileMediaStore()
	dir := t.TempDir()

	txtPath := filepath.Join(dir, "readme.txt")
	if err := os.WriteFile(txtPath, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, _ := store.Store(txtPath, media.MediaMeta{}, "test")

	messages := []providers.Message{
		{Role: "user", Content: "hi", Media: []string{ref}},
	}
	result := resolveMediaRefs(messages, media.AsMediaResolver(store), config.DefaultMaxMediaSize)

	if len(result[0].Media) != 0 {
		t.Fatalf("expected 0 media (unknown type), got %d", len(result[0].Media))
	}
}

func TestResolveMediaRefs_PassesThroughNonMediaRefs(t *testing.T) {
	messages := []providers.Message{
		{Role: "user", Content: "hi", Media: []string{"https://example.com/img.png"}},
	}
	result := resolveMediaRefs(messages, nil, config.DefaultMaxMediaSize)

	if len(result[0].Media) != 1 || result[0].Media[0] != "https://example.com/img.png" {
		t.Fatalf("expected passthrough of non-media:// URL, got %v", result[0].Media)
	}
}

func TestResolveMediaRefs_DoesNotMutateOriginal(t *testing.T) {
	store := media.NewFileMediaStore()
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "test.png")
	pngHeader := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02,
		0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE,
	}
	os.WriteFile(pngPath, pngHeader, 0o644)
	ref, _ := store.Store(pngPath, media.MediaMeta{}, "test")

	original := []providers.Message{
		{Role: "user", Content: "hi", Media: []string{ref}},
	}
	originalRef := original[0].Media[0]

	resolveMediaRefs(original, media.AsMediaResolver(store), config.DefaultMaxMediaSize)

	if original[0].Media[0] != originalRef {
		t.Fatal("resolveMediaRefs mutated original message slice")
	}
}

func TestResolveMediaRefs_UsesMetaContentType(t *testing.T) {
	store := media.NewFileMediaStore()
	dir := t.TempDir()

	// File with JPEG content but stored with explicit content type
	jpegPath := filepath.Join(dir, "photo")
	jpegHeader := []byte{0xFF, 0xD8, 0xFF, 0xE0} // JPEG magic bytes
	os.WriteFile(jpegPath, jpegHeader, 0o644)
	ref, _ := store.Store(jpegPath, media.MediaMeta{ContentType: "image/jpeg"}, "test")

	messages := []providers.Message{
		{Role: "user", Content: "hi", Media: []string{ref}},
	}
	result := resolveMediaRefs(messages, media.AsMediaResolver(store), config.DefaultMaxMediaSize)

	if len(result[0].Media) != 1 {
		t.Fatalf("expected 1 media, got %d", len(result[0].Media))
	}
	if !strings.HasPrefix(result[0].Media[0], "data:image/jpeg;base64,") {
		t.Fatalf("expected jpeg prefix, got %q", result[0].Media[0][:30])
	}
}

func TestResolveAgentForSession_IgnoresSessionActiveAgentInSlimRuntime(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true, Name: "Main"},
		{ID: "worker", Name: "Worker"},
	}

	registry := NewAgentRegistry(cfg, nil)
	loop := &AgentLoop{registry: registry}
	agent, err := loop.resolveAgentForSession("conv:direct:user-1", routing.ResolvedRoute{AgentID: "main"})
	if err != nil {
		t.Fatalf("resolveAgentForSession error: %v", err)
	}
	if agent == nil {
		t.Fatal("expected agent")
	}
	if agent.ID != "main" {
		t.Fatalf("agent.ID = %q, want %q", agent.ID, "main")
	}
}

func TestParseThinkingLevel(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  ThinkingLevel
	}{
		{"off", "off", ThinkingOff},
		{"empty", "", ThinkingOff},
		{"low", "low", ThinkingLow},
		{"medium", "medium", ThinkingMedium},
		{"high", "high", ThinkingHigh},
		{"xhigh", "xhigh", ThinkingXHigh},
		{"adaptive", "adaptive", ThinkingAdaptive},
		{"unknown", "unknown", ThinkingOff},
		// Case-insensitive and whitespace-tolerant
		{"upper_Medium", "Medium", ThinkingMedium},
		{"upper_HIGH", "HIGH", ThinkingHigh},
		{"mixed_Adaptive", "Adaptive", ThinkingAdaptive},
		{"leading_space", " high", ThinkingHigh},
		{"trailing_space", "low ", ThinkingLow},
		{"both_spaces", " medium ", ThinkingMedium},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseThinkingLevel(tt.input); got != tt.want {
				t.Errorf("parseThinkingLevel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFindLastUnfinishedRun_OnlyHeartbeatIsIgnored(t *testing.T) {
	workspace := t.TempDir()

	heartbeatDir := filepath.Join(workspace, ".x-claw", "audit", "runs", "heartbeat")
	if err := os.MkdirAll(heartbeatDir, 0o755); err != nil {
		t.Fatalf("mkdir heartbeat dir: %v", err)
	}
	// Heartbeat run: unfinished, but should be ignored by resume discovery.
	if err := os.WriteFile(filepath.Join(heartbeatDir, "events.jsonl"), []byte(
		`{"type":"run.start","ts_ms":3000,"run_id":"hb-1","session_key":"heartbeat","channel":"feishu","chat_id":"oc_hb"}`+"\n",
	), 0o600); err != nil {
		t.Fatalf("write heartbeat events: %v", err)
	}

	cand, err := findLastUnfinishedRun(workspace)
	if err == nil {
		t.Fatalf("expected error, got candidate: %+v", cand)
	}
}

func TestFindLastUnfinishedRun_PrefersNonHeartbeatEvenIfNewer(t *testing.T) {
	workspace := t.TempDir()

	// Heartbeat run (newer ts_ms): should still be ignored.
	heartbeatDir := filepath.Join(workspace, ".x-claw", "audit", "runs", "heartbeat")
	if err := os.MkdirAll(heartbeatDir, 0o755); err != nil {
		t.Fatalf("mkdir heartbeat dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(heartbeatDir, "events.jsonl"), []byte(
		`{"type":"run.start","ts_ms":9000,"run_id":"hb-2","session_key":"heartbeat","channel":"feishu","chat_id":"oc_hb"}`+"\n",
	), 0o600); err != nil {
		t.Fatalf("write heartbeat events: %v", err)
	}

	// Real user-ish session run.
	userDir := filepath.Join(workspace, ".x-claw", "audit", "runs", "agent_main_main")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatalf("mkdir user dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "events.jsonl"), []byte(
		`{"type":"run.start","ts_ms":2000,"run_id":"run-1","session_key":"agent:main:main","channel":"feishu","chat_id":"oc_user","sender_id":"u1"}`+"\n",
	), 0o600); err != nil {
		t.Fatalf("write user events: %v", err)
	}

	cand, err := findLastUnfinishedRun(workspace)
	if err != nil {
		t.Fatalf("expected candidate, got error: %v", err)
	}
	if cand.RunID != "run-1" {
		t.Fatalf("expected run_id=run-1, got %q", cand.RunID)
	}
	if cand.SessionKey != "agent:main:main" {
		t.Fatalf("expected session_key=agent:main:main, got %q", cand.SessionKey)
	}
	if cand.Channel != "feishu" || cand.ChatID != "oc_user" {
		t.Fatalf("unexpected route: channel=%q chat_id=%q", cand.Channel, cand.ChatID)
	}
}

func TestFindLastUnfinishedRun_SkipsErrorTerminatedRuns(t *testing.T) {
	workspace := t.TempDir()

	failedDir := filepath.Join(workspace, ".x-claw", "audit", "runs", "agent_main_failed")
	if err := os.MkdirAll(failedDir, 0o755); err != nil {
		t.Fatalf("mkdir failed dir: %v", err)
	}
	failedEvents := strings.Join([]string{
		`{"type":"run.start","ts_ms":3000,"run_id":"run-failed","session_key":"agent:main:failed","channel":"feishu","chat_id":"oc_failed","sender_id":"u2"}`,
		`{"type":"run.error","ts_ms":3100,"run_id":"run-failed","session_key":"agent:main:failed","channel":"feishu","chat_id":"oc_failed","sender_id":"u2","error":"provider timeout"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(failedDir, "events.jsonl"), []byte(failedEvents), 0o600); err != nil {
		t.Fatalf("write failed events: %v", err)
	}

	resumableDir := filepath.Join(workspace, ".x-claw", "audit", "runs", "agent_main_resumable")
	if err := os.MkdirAll(resumableDir, 0o755); err != nil {
		t.Fatalf("mkdir resumable dir: %v", err)
	}
	resumableEvents := `{"type":"run.start","ts_ms":2000,"run_id":"run-ok","session_key":"agent:main:ok","channel":"feishu","chat_id":"oc_ok","sender_id":"u1"}` + "\n"
	if err := os.WriteFile(filepath.Join(resumableDir, "events.jsonl"), []byte(resumableEvents), 0o600); err != nil {
		t.Fatalf("write resumable events: %v", err)
	}

	cand, err := findLastUnfinishedRun(workspace)
	if err != nil {
		t.Fatalf("expected candidate, got error: %v", err)
	}
	if cand.RunID != "run-ok" {
		t.Fatalf("run_id = %q, want %q", cand.RunID, "run-ok")
	}
	if cand.LastEventType != "run.start" {
		t.Fatalf("last_event_type = %q, want %q", cand.LastEventType, "run.start")
	}
}

func TestFindLastUnfinishedRunAcrossWorkspaces_PrefersLatestCandidate(t *testing.T) {
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()

	runsA := filepath.Join(workspaceA, ".x-claw", "audit", "runs", "agent_main_a")
	if err := os.MkdirAll(runsA, 0o755); err != nil {
		t.Fatalf("mkdir runsA: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runsA, "events.jsonl"), []byte(
		`{"type":"run.start","ts_ms":1000,"run_id":"run-a","session_key":"agent:main:a","channel":"feishu","chat_id":"oc_a","sender_id":"u1","agent_id":"main"}`+"\n",
	), 0o600); err != nil {
		t.Fatalf("write workspaceA events: %v", err)
	}

	runsB := filepath.Join(workspaceB, ".x-claw", "audit", "runs", "agent_worker_b")
	if err := os.MkdirAll(runsB, 0o755); err != nil {
		t.Fatalf("mkdir runsB: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runsB, "events.jsonl"), []byte(
		`{"type":"run.start","ts_ms":2000,"run_id":"run-b","session_key":"agent:worker:b","channel":"telegram","chat_id":"oc_b","sender_id":"u2","agent_id":"worker"}`+"\n",
	), 0o600); err != nil {
		t.Fatalf("write workspaceB events: %v", err)
	}

	cand, err := findLastUnfinishedRunAcrossWorkspaces([]string{workspaceA, workspaceB, workspaceA})
	if err != nil {
		t.Fatalf("expected candidate, got error: %v", err)
	}
	if cand.RunID != "run-b" {
		t.Fatalf("run_id = %q, want %q", cand.RunID, "run-b")
	}
	if cand.AgentID != "worker" {
		t.Fatalf("agent_id = %q, want %q", cand.AgentID, "worker")
	}
	if cand.Channel != "telegram" || cand.ChatID != "oc_b" {
		t.Fatalf("unexpected route: channel=%q chat_id=%q", cand.Channel, cand.ChatID)
	}
}

func TestResumeLastTask_ScansSecondaryAgentWorkspace(t *testing.T) {
	mainWS := t.TempDir()
	workerWS := t.TempDir()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         mainWS,
				Model:             "test-model",
				MaxTokens:         512,
				MaxToolIterations: 1,
			},
			List: []config.AgentConfig{
				{ID: "main", Default: true, Workspace: mainWS},
				{ID: "worker", Workspace: workerWS},
			},
		},
	}

	runsDir := filepath.Join(workerWS, ".x-claw", "audit", "runs", "agent_worker_resume")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatalf("mkdir runs dir: %v", err)
	}
	eventsJSONL := `{"type":"run.start","ts_ms":4000,"run_id":"run-worker","session_key":"agent:worker:resume","channel":"cli","chat_id":"direct","sender_id":"cron","agent_id":"worker"}` + "\n"
	if err := os.WriteFile(filepath.Join(runsDir, "events.jsonl"), []byte(eventsJSONL), 0o600); err != nil {
		t.Fatalf("write worker events: %v", err)
	}

	loop := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{})
	candidate, response, err := loop.ResumeLastTask(context.Background())
	if err != nil {
		t.Fatalf("ResumeLastTask error = %v", err)
	}
	if candidate == nil {
		t.Fatal("expected candidate")
	}
	if candidate.RunID != "run-worker" {
		t.Fatalf("run_id = %q, want %q", candidate.RunID, "run-worker")
	}
	if candidate.AgentID != "worker" {
		t.Fatalf("agent_id = %q, want %q", candidate.AgentID, "worker")
	}
	if response != "Mock response" {
		t.Fatalf("response = %q, want %q", response, "Mock response")
	}
}

func TestResumeLastTaskPrompt_SlimPrompt(t *testing.T) {
	prompt := resumeLastTaskPrompt()
	if strings.Contains(strings.ToLower(prompt), "tool_confirm") {
		t.Fatalf("expected resume prompt to drop tool_confirm guidance, got: %s", prompt)
	}
	if !strings.Contains(prompt, "Continue the unfinished task") {
		t.Fatalf("unexpected resume prompt: %s", prompt)
	}
}

func TestTokenUsageStoreRecord_IgnoresEmptyUsage(t *testing.T) {
	workspace := t.TempDir()
	store := newTokenUsageStore(workspace)
	if store == nil {
		t.Fatalf("expected store")
	}

	store.Record("gpt-x", &providers.UsageInfo{})

	if _, err := os.Stat(filepath.Join(workspace, "state", "token_usage.json")); err == nil {
		t.Fatalf("expected token_usage.json not to be created for empty usage")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat token_usage.json: %v", err)
	}
}

func TestTokenUsageStoreRecord_ComputesTotalWhenMissing(t *testing.T) {
	workspace := t.TempDir()
	store := newTokenUsageStore(workspace)
	if store == nil {
		t.Fatalf("expected store")
	}

	store.Record("m1", &providers.UsageInfo{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      0,
	})

	data, err := os.ReadFile(filepath.Join(workspace, "state", "token_usage.json"))
	if err != nil {
		t.Fatalf("read token_usage.json: %v", err)
	}

	var snap tokenUsageSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}

	if got, want := snap.Totals.TotalTokens, int64(15); got != want {
		t.Fatalf("totals.total_tokens = %d, want %d", got, want)
	}
	if got, want := snap.ByModel["m1"].TotalTokens, int64(15); got != want {
		t.Fatalf("by_model[m1].total_tokens = %d, want %d", got, want)
	}
}

func TestTokenUsageStoreRecord_AccumulatesByModelAndTotals(t *testing.T) {
	workspace := t.TempDir()
	store := newTokenUsageStore(workspace)
	if store == nil {
		t.Fatalf("expected store")
	}

	store.Record("m1", &providers.UsageInfo{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5})
	store.Record("m2", &providers.UsageInfo{PromptTokens: 7, CompletionTokens: 11, TotalTokens: 18})
	store.Record("m1", &providers.UsageInfo{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2})

	data, err := os.ReadFile(filepath.Join(workspace, "state", "token_usage.json"))
	if err != nil {
		t.Fatalf("read token_usage.json: %v", err)
	}

	var snap tokenUsageSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}

	if got, want := snap.Totals.Requests, int64(3); got != want {
		t.Fatalf("totals.requests = %d, want %d", got, want)
	}
	if got, want := snap.Totals.PromptTokens, int64(10); got != want {
		t.Fatalf("totals.prompt_tokens = %d, want %d", got, want)
	}
	if got, want := snap.Totals.CompletionTokens, int64(15); got != want {
		t.Fatalf("totals.completion_tokens = %d, want %d", got, want)
	}
	if got, want := snap.Totals.TotalTokens, int64(25); got != want {
		t.Fatalf("totals.total_tokens = %d, want %d", got, want)
	}

	if got, want := snap.ByModel["m1"].Requests, int64(2); got != want {
		t.Fatalf("by_model[m1].requests = %d, want %d", got, want)
	}
	if got, want := snap.ByModel["m1"].TotalTokens, int64(7); got != want {
		t.Fatalf("by_model[m1].total_tokens = %d, want %d", got, want)
	}
	if got, want := snap.ByModel["m2"].Requests, int64(1); got != want {
		t.Fatalf("by_model[m2].requests = %d, want %d", got, want)
	}
	if got, want := snap.ByModel["m2"].TotalTokens, int64(18); got != want {
		t.Fatalf("by_model[m2].total_tokens = %d, want %d", got, want)
	}
}

type mockProvider struct{}

func (m *mockProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{
		Content:   "Mock response",
		ToolCalls: []providers.ToolCall{},
	}, nil
}

func (m *mockProvider) GetDefaultModel() string {
	return "mock-model"
}

func TestRunTaskAudit_DetectsMissedAndQualityIssues(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Audit.Enabled = true
	cfg.Audit.LookbackMinutes = 360
	cfg.Audit.Supervisor.Enabled = false
	cfg.Orchestration.DefaultTaskTimeoutSeconds = 1
	cfg.Orchestration.RetryLimitPerTask = 2

	loop := NewAgentLoop(cfg, bus.NewMessageBus(), &mockProvider{})
	ledger := loop.GetTaskLedger()
	if ledger == nil {
		t.Fatal("expected task ledger")
	}

	oldTS := time.Now().Add(-10 * time.Minute).UnixMilli()
	_ = ledger.UpsertTask(tools.TaskLedgerEntry{
		ID:          "task-planned-old",
		Status:      tools.TaskStatusPlanned,
		Intent:      "do something",
		CreatedAtMS: oldTS,
		UpdatedAtMS: oldTS,
	})
	_ = ledger.UpsertTask(tools.TaskLedgerEntry{
		ID:          "task-completed-empty",
		Status:      tools.TaskStatusCompleted,
		CreatedAtMS: oldTS,
		UpdatedAtMS: oldTS,
		Result:      "",
	})

	report, err := loop.RunTaskAudit(context.Background())
	if err != nil {
		t.Fatalf("RunTaskAudit error: %v", err)
	}
	if report == nil {
		t.Fatal("expected non-nil report")
	}
	if len(report.Findings) < 2 {
		t.Fatalf("expected at least 2 findings, got %d", len(report.Findings))
	}

	hasMissed := false
	hasQuality := false
	for _, f := range report.Findings {
		if f.TaskID == "task-planned-old" && f.Category == "missed" {
			hasMissed = true
		}
		if f.TaskID == "task-completed-empty" && f.Category == "quality" {
			hasQuality = true
		}
	}
	if !hasMissed {
		t.Fatal("expected missed finding for overdue planned task")
	}
	if !hasQuality {
		t.Fatal("expected quality finding for empty completed task")
	}
}

func TestParseSupervisorReview_EmbeddedJSON(t *testing.T) {
	raw := "review result:\n{\"score\":0.42,\"issues\":[{\"category\":\"quality\",\"severity\":\"high\",\"message\":\"missing evidence\"}]}"
	review, err := parseSupervisorReview(raw)
	if err != nil {
		t.Fatalf("parseSupervisorReview error: %v", err)
	}
	if review.Score != 0.42 {
		t.Fatalf("score = %v, want 0.42", review.Score)
	}
	if len(review.Issues) != 1 {
		t.Fatalf("issues len = %d, want 1", len(review.Issues))
	}
	if !strings.EqualFold(review.Issues[0].Category, "quality") {
		t.Fatalf("issue category = %q", review.Issues[0].Category)
	}
}

// TestMain applies conservative GC/memory knobs to keep the agent test suite
// stable in memory-constrained environments. Some tests exercise memory and
// embedding paths that can temporarily increase heap usage.
//
// To disable, set X_CLAW_TEST_MEMLIMIT=0.
func TestMain(m *testing.M) {
	memLimit := int64(384 << 20) // 384 MiB default
	raw := strings.TrimSpace(os.Getenv("X_CLAW_TEST_MEMLIMIT"))
	if raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			memLimit = n
		}
	}

	if memLimit > 0 {
		debug.SetMemoryLimit(memLimit)
		debug.SetGCPercent(20)
	}

	os.Exit(m.Run())
}

func TestImportInboundMediaToWorkspace_CopiesFileAndBuildsNote(t *testing.T) {
	workspace := t.TempDir()
	srcDir := t.TempDir()

	srcPath := filepath.Join(srcDir, "hello.txt")
	if err := os.WriteFile(srcPath, []byte("hello world\n"), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	store := media.NewFileMediaStore()
	ref, err := store.Store(srcPath, media.MediaMeta{
		Filename:    "hello.txt",
		ContentType: "text/plain",
		Source:      "test",
	}, "scope")
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	al := &AgentLoop{mediaResolver: media.AsMediaResolver(store)}
	msg := bus.InboundMessage{
		Channel:   "feishu",
		ChatID:    "oc_test",
		MessageID: "om_test",
		Media:     []string{ref},
	}

	imported, skipped := al.importInboundMediaToWorkspace(workspace, msg)
	if skipped != 0 {
		t.Fatalf("unexpected skipped=%d", skipped)
	}
	if len(imported) != 1 {
		t.Fatalf("expected 1 imported file, got %d", len(imported))
	}

	dstPath := filepath.Join(workspace, filepath.FromSlash(imported[0].RelativePath))
	data, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("ReadFile(dst) failed: %v", err)
	}
	if string(data) != "hello world\n" {
		t.Fatalf("unexpected dst content: %q", string(data))
	}

	note := formatInboundMediaNote(imported, skipped)
	if !strings.Contains(note, imported[0].RelativePath) {
		t.Fatalf("note should contain relative path, got: %s", note)
	}
	if !strings.Contains(note, "content_type=text/plain") {
		t.Fatalf("note should contain content type, got: %s", note)
	}
}

type failPrimaryProvider struct {
	mu    sync.Mutex
	calls []string
}

func (p *failPrimaryProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	model string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.calls = append(p.calls, model)
	p.mu.Unlock()

	switch model {
	case "primary-model":
		return nil, fmt.Errorf("timeout: simulated primary failure")
	case "fallback-model":
		return &providers.LLMResponse{Content: "ok", ToolCalls: []providers.ToolCall{}}, nil
	default:
		return &providers.LLMResponse{Content: "ok", ToolCalls: []providers.ToolCall{}}, nil
	}
}

func (p *failPrimaryProvider) GetDefaultModel() string { return "primary-model" }

func (p *failPrimaryProvider) Models() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.calls))
	out = append(out, p.calls...)
	return out
}

type fallbackSwitchProvider struct {
	mu         sync.Mutex
	name       string
	failModels map[string]error
	calls      []string
	response   string
}

func (p *fallbackSwitchProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	model string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.calls = append(p.calls, model)
	err := p.failModels[model]
	resp := p.response
	p.mu.Unlock()

	if err != nil {
		return nil, err
	}
	if resp == "" {
		resp = "ok"
	}
	return &providers.LLMResponse{Content: resp, ToolCalls: []providers.ToolCall{}}, nil
}

func (p *fallbackSwitchProvider) GetDefaultModel() string { return "test-model" }

func (p *fallbackSwitchProvider) Models() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.calls))
	out = append(out, p.calls...)
	return out
}

func TestAgentLoop_FallbackAcrossProviders_UsesFallbackProviderInstance(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "primary-model",
				ModelFallbacks:    []string{"anthropic/fallback-model"},
				MaxTokens:         4096,
				MaxToolIterations: 3,
			},
		},
	}

	primary := &fallbackSwitchProvider{
		name: "primary",
		failModels: map[string]error{
			"primary-model":  &providers.FailoverError{Reason: providers.FailoverTimeout, Wrapped: fmt.Errorf("primary provider failed on primary model")},
			"fallback-model": fmt.Errorf("primary provider must not be reused for fallback model"),
		},
	}
	fallback := &fallbackSwitchProvider{name: "fallback", response: "fallback ok"}

	originalFactory := fallbackProviderFactory
	t.Cleanup(func() {
		fallbackProviderFactory = originalFactory
	})
	fallbackProviderFactory = func(_ *AgentLoop, candidate providers.FallbackCandidate) (providers.LLMProvider, string, error) {
		switch {
		case candidate.Provider == "openai" && candidate.Model == "primary-model":
			return primary, candidate.Model, nil
		case candidate.Provider == "anthropic" && candidate.Model == "fallback-model":
			return fallback, candidate.Model, nil
		default:
			return nil, "", fmt.Errorf("unexpected candidate %s/%s", candidate.Provider, candidate.Model)
		}
	}

	al := NewAgentLoop(cfg, bus.NewMessageBus(), primary)
	resp, err := al.ProcessDirectWithChannel(context.Background(), "hello", "fallback-provider-session", "cli", "direct")
	if err != nil {
		t.Fatalf("ProcessDirectWithChannel error = %v", err)
	}
	if resp != "fallback ok" {
		t.Fatalf("response = %q, want %q", resp, "fallback ok")
	}

	if got := primary.Models(); len(got) != 1 || got[0] != "primary-model" {
		t.Fatalf("primary calls = %v, want [primary-model]", got)
	}
	if got := fallback.Models(); len(got) != 1 || got[0] != "fallback-model" {
		t.Fatalf("fallback calls = %v, want [fallback-model]", got)
	}
}

func TestAgentLoop_SessionModelAutoDowngrade_AppliesAfterConsecutiveFallbacks(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-autodowngrade-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:                 tmpDir,
				Model:                     "primary-model",
				ModelFallbacks:            []string{"anthropic/fallback-model"},
				MaxTokens:                 4096,
				MaxToolIterations:         3,
				SessionModelAutoDowngrade: config.SessionModelAutoDowngradeConfig{Enabled: true, Threshold: 2, WindowMinutes: 60, TTLMinutes: 10},
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &failPrimaryProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)
	originalFactory := fallbackProviderFactory
	t.Cleanup(func() {
		fallbackProviderFactory = originalFactory
	})
	fallbackProviderFactory = func(_ *AgentLoop, candidate providers.FallbackCandidate) (providers.LLMProvider, string, error) {
		if candidate.Provider == "anthropic" && candidate.Model == "fallback-model" {
			return provider, candidate.Model, nil
		}
		return nil, "", fmt.Errorf("unexpected candidate %s/%s", candidate.Provider, candidate.Model)
	}

	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("No default agent found")
	}

	sessionKey := "test-session-autodowngrade"

	// First run: fallback happens, but threshold not reached.
	if _, err := al.ProcessDirectWithChannel(context.Background(), "msg-1", sessionKey, "cli", "direct"); err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	if got, ok := defaultAgent.Sessions.EffectiveModelOverride(sessionKey); ok || got != "" {
		t.Fatalf("expected no override after first fallback, got=%q ok=%v", got, ok)
	}
	firstCalls := provider.Models()
	seenPrimary := false
	for _, m := range firstCalls {
		if m == "primary-model" {
			seenPrimary = true
			break
		}
	}
	if !seenPrimary {
		t.Fatalf("expected at least one primary-model call in first run, got %v", firstCalls)
	}

	// Second run: fallback happens again; should trigger auto-downgrade.
	if _, err := al.ProcessDirectWithChannel(context.Background(), "msg-2", sessionKey, "cli", "direct"); err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if got, ok := defaultAgent.Sessions.EffectiveModelOverride(sessionKey); !ok || got != "fallback-model" {
		t.Fatalf("expected override to fallback-model after threshold, got=%q ok=%v", got, ok)
	}
	beforeThird := provider.Models()

	// Third run: should use the override directly (no primary failure call).
	if _, err := al.ProcessDirectWithChannel(context.Background(), "msg-3", sessionKey, "cli", "direct"); err != nil {
		t.Fatalf("third run failed: %v", err)
	}

	afterThird := provider.Models()
	if len(afterThird) <= len(beforeThird) {
		t.Fatalf("expected at least one additional provider.Chat call in third run, got before=%d after=%d", len(beforeThird), len(afterThird))
	}
	newCalls := afterThird[len(beforeThird):]
	if len(newCalls) != 1 || newCalls[0] != "fallback-model" {
		t.Fatalf("expected third run to call fallback-model exactly once, got %v (all=%v)", newCalls, afterThird)
	}
}

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

func TestDetectToolCallLoop_NonConsecutiveMatchesDoNotCount(t *testing.T) {
	recent := []toolCallSignature{
		mustSig(t, "read_file", map[string]any{"path": "a.txt"}),
		mustSig(t, "list_dir", map[string]any{"path": "."}),
		mustSig(t, "read_file", map[string]any{"path": "a.txt"}),
	}
	current := []providers.ToolCall{{Name: "read_file", Arguments: map[string]any{"path": "a.txt"}}}

	if got := detectToolCallLoop(recent, current, 2); got != "" {
		t.Fatalf("detectToolCallLoop = %q, want empty (non-consecutive repeats should not loop)", got)
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

func TestDetectToolCallLoop_ChangedResultsDoNotCountAsLoop(t *testing.T) {
	recent := []toolCallSignature{
		mustSigWithResult(t, "wait_job", map[string]any{"id": "job-1"}, "status=pending"),
		mustSigWithResult(t, "wait_job", map[string]any{"id": "job-1"}, "status=pending"),
		mustSigWithResult(t, "wait_job", map[string]any{"id": "job-1"}, "status=running"),
	}
	current := []providers.ToolCall{{Name: "wait_job", Arguments: map[string]any{"id": "job-1"}}}

	if got := detectToolCallLoop(recent, current, 2); got != "" {
		t.Fatalf("detectToolCallLoop = %q, want empty (result progress should reset loop detection)", got)
	}
}

func mustSig(t *testing.T, name string, args map[string]any) toolCallSignature {
	return mustSigWithResult(t, name, args, "steady")
}

func mustSigWithResult(t *testing.T, name string, args map[string]any, result string) toolCallSignature {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}
	return toolCallSignature{
		Name:              name,
		Args:              string(b),
		ResultFingerprint: result,
	}
}

type parallelLoopMockProvider struct {
	callCount int
}

func (m *parallelLoopMockProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	m.callCount++
	if m.callCount == 1 {
		return &providers.LLMResponse{
			ToolCalls: []providers.ToolCall{
				{ID: "tc-1", Name: "slow_parallel", Arguments: map[string]any{}},
				{ID: "tc-2", Name: "fast_parallel", Arguments: map[string]any{}},
			},
		}, nil
	}

	if m.callCount == 2 {
		toolMessages := make([]providers.Message, 0, 2)
		for _, msg := range messages {
			if msg.Role == "tool" {
				toolMessages = append(toolMessages, msg)
			}
		}
		if len(toolMessages) != 2 {
			return nil, fmt.Errorf("tool message count = %d, want 2", len(toolMessages))
		}
		if toolMessages[0].ToolCallID != "tc-1" || toolMessages[0].Content != "slow-ok" {
			return nil, fmt.Errorf("first tool message = %+v, want tc-1/slow-ok", toolMessages[0])
		}
		if toolMessages[1].ToolCallID != "tc-2" || toolMessages[1].Content != "fast-ok" {
			return nil, fmt.Errorf("second tool message = %+v, want tc-2/fast-ok", toolMessages[1])
		}
		return &providers.LLMResponse{Content: "final-from-provider"}, nil
	}

	return &providers.LLMResponse{Content: "unexpected-extra-call"}, nil
}

func (m *parallelLoopMockProvider) GetDefaultModel() string {
	return "parallel-loop-mock"
}

type parallelTestTool struct {
	name   string
	result string
	delay  time.Duration
}

func (t *parallelTestTool) Name() string {
	return t.name
}

func (t *parallelTestTool) Description() string {
	return "parallel test tool"
}

func (t *parallelTestTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
	}
}

func (t *parallelTestTool) ParallelPolicy() tools.ToolParallelPolicy {
	return tools.ToolParallelReadOnly
}

func (t *parallelTestTool) Execute(_ context.Context, _ map[string]any) *tools.ToolResult {
	if t.delay > 0 {
		time.Sleep(t.delay)
	}
	return tools.SilentResult(t.result)
}

func TestAgentLoop_RunLLMIteration_ParallelToolCallsPreserveOrder(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-loop-parallel-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = tmpDir
	cfg.Agents.Defaults.Model = "test-model"
	cfg.Agents.Defaults.MaxToolIterations = 4
	cfg.Orchestration.ToolCallsParallelEnabled = true
	cfg.Orchestration.MaxToolCallConcurrency = 8
	cfg.Orchestration.ParallelToolsMode = tools.ParallelToolsModeReadOnlyOnly

	msgBus := bus.NewMessageBus()
	provider := &parallelLoopMockProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)
	al.RegisterTool(&parallelTestTool{
		name:   "slow_parallel",
		result: "slow-ok",
		delay:  50 * time.Millisecond,
	})
	al.RegisterTool(&parallelTestTool{
		name:   "fast_parallel",
		result: "fast-ok",
		delay:  5 * time.Millisecond,
	})

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent not found")
	}

	finalContent, iterations, _, err := al.runLLMIteration(
		context.Background(),
		agent,
		[]providers.Message{
			{Role: "system", Content: "you are a test assistant"},
			{Role: "user", Content: "run parallel tools"},
		},
		processOptions{
			SessionKey:   "parallel-session",
			Channel:      "cli",
			ChatID:       "direct",
			SenderID:     "tester",
			SendResponse: false,
		},
		nil,
		agent.Model,
	)
	if err != nil {
		t.Fatalf("runLLMIteration() error = %v", err)
	}
	if iterations != 2 {
		t.Fatalf("iterations = %d, want 2", iterations)
	}
	if finalContent != "final-from-provider" {
		t.Fatalf("finalContent = %q, want %q", finalContent, "final-from-provider")
	}
}
