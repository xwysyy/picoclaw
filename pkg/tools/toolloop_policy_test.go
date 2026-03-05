package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/providers"
)

type policyToolLoopProvider struct {
	callCount int
}

func (p *policyToolLoopProvider) Chat(
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
				{ID: "tc-1", Name: "exec", Arguments: map[string]any{"command": "echo hi"}},
			},
		}, nil
	}

	if p.callCount == 2 {
		var toolMsg *providers.Message
		for i := range messages {
			if messages[i].Role == "tool" && messages[i].ToolCallID == "tc-1" {
				toolMsg = &messages[i]
				break
			}
		}
		if toolMsg == nil {
			return nil, fmt.Errorf("expected tool message tc-1, got none")
		}
		if !strings.Contains(toolMsg.Content, `"kind":"tool_policy_denied"`) {
			return nil, fmt.Errorf("expected tool_policy_denied, got: %s", toolMsg.Content)
		}
		return &providers.LLMResponse{Content: "done"}, nil
	}

	return &providers.LLMResponse{Content: "unexpected-extra-call"}, nil
}

func (p *policyToolLoopProvider) GetDefaultModel() string { return "toolloop-policy" }

func TestRunToolLoop_AppliesToolPolicy(t *testing.T) {
	provider := &policyToolLoopProvider{}

	result, err := RunToolLoop(
		context.Background(),
		ToolLoopConfig{
			Provider:      provider,
			Model:         "mock-model",
			Tools:         NewToolRegistry(),
			MaxIterations: 3,
			Policy: config.ToolPolicyConfig{
				Enabled: true,
				Deny:    []string{"exec"},
			},
		},
		[]providers.Message{
			{Role: "system", Content: "you are a test assistant"},
			{Role: "user", Content: "try to exec"},
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
	if result.Content != "done" {
		t.Fatalf("result.Content = %q, want %q", result.Content, "done")
	}
	if provider.callCount != 2 {
		t.Fatalf("provider.callCount = %d, want 2", provider.callCount)
	}
}
