package agent

import (
	"testing"

	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/routing"
	"github.com/xwysyy/X-Claw/pkg/session"
)

func TestResolveAgentForSession_IgnoresSessionActiveAgentInSlimRuntime(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true, Name: "Main"},
		{ID: "worker", Name: "Worker"},
	}

	registry := NewAgentRegistry(cfg, nil)
	sessions := session.NewSessionManager(t.TempDir())
	sessions.SetActiveAgentID("conv:direct:user-1", "worker")

	loop := &AgentLoop{registry: registry, sessions: sessions}
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
