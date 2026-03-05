package agent

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/providers"
)

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
