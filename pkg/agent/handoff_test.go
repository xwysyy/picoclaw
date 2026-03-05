package agent

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/providers"
)

type handoffMockProvider struct {
	mu     sync.Mutex
	calls  int
	models []string
}

func (m *handoffMockProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	model string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.calls++
	m.models = append(m.models, model)

	switch m.calls {
	case 1:
		// First agent decides to hand off.
		return &providers.LLMResponse{
			Content: "Handing off to worker...",
			ToolCalls: []providers.ToolCall{
				{
					ID:   "call-1",
					Type: "function",
					Name: "handoff",
					Arguments: map[string]any{
						"agent_name": "worker",
						"reason":     "needs specialist agent",
						"takeover":   true,
					},
				},
			},
		}, nil
	case 2:
		// New agent should take over immediately.
		return &providers.LLMResponse{
			Content:   "worker-response",
			ToolCalls: nil,
		}, nil
	case 3:
		// Next user turn should still route to worker via active_agent_id.
		return &providers.LLMResponse{
			Content:   "worker-response-2",
			ToolCalls: nil,
		}, nil
	default:
		return &providers.LLMResponse{Content: "unexpected"}, nil
	}
}

func (m *handoffMockProvider) GetDefaultModel() string {
	return "mock-default"
}

func TestHandoff_TakeoverAndPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	workerDir := filepath.Join(tmpDir, "worker")
	if err := os.MkdirAll(workerDir, 0o755); err != nil {
		t.Fatalf("mkdir worker workspace: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = tmpDir
	cfg.Agents.Defaults.Model = "main-model"
	cfg.Agents.Defaults.MaxToolIterations = 4
	cfg.Session.DMScope = "per-peer"
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true, Name: "Main"},
		{ID: "worker", Name: "Worker", Workspace: workerDir, Model: &config.AgentModelConfig{Primary: "worker-model"}},
	}

	msgBus := bus.NewMessageBus()
	provider := &handoffMockProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	// DMScope=per-peer + direct peer "user-1" → conv:direct:user-1
	sessionKey := "conv:direct:user-1"

	msg := bus.InboundMessage{
		Channel:  "test",
		SenderID: "user-1",
		ChatID:   "chat-1",
		Peer:     bus.Peer{Kind: "direct", ID: "user-1"},
		Content:  "Hola",
	}

	resp, err := al.processMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("processMessage failed: %v", err)
	}
	if resp != "worker-response" {
		t.Fatalf("resp = %q, want %q", resp, "worker-response")
	}

	if al.sessions == nil {
		t.Fatal("expected shared session manager to be initialized")
	}
	if got := al.sessions.GetActiveAgentID(sessionKey); got != "worker" {
		t.Fatalf("active_agent_id = %q, want %q", got, "worker")
	}

	// Next user message should route to worker without needing another handoff call.
	resp2, err := al.processMessage(context.Background(), bus.InboundMessage{
		Channel:  "test",
		SenderID: "user-1",
		ChatID:   "chat-1",
		Peer:     bus.Peer{Kind: "direct", ID: "user-1"},
		Content:  "Next",
	})
	if err != nil {
		t.Fatalf("second processMessage failed: %v", err)
	}
	if resp2 != "worker-response-2" {
		t.Fatalf("resp2 = %q, want %q", resp2, "worker-response-2")
	}

	provider.mu.Lock()
	models := append([]string(nil), provider.models...)
	provider.mu.Unlock()
	if len(models) < 3 {
		t.Fatalf("expected at least 3 model calls, got %d: %v", len(models), models)
	}
	if models[0] != "main-model" {
		t.Fatalf("first model = %q, want %q", models[0], "main-model")
	}
	if models[1] != "worker-model" {
		t.Fatalf("second model = %q, want %q", models[1], "worker-model")
	}
	if models[2] != "worker-model" {
		t.Fatalf("third model = %q, want %q", models[2], "worker-model")
	}
}
