package agent

import (
	"slices"
	"testing"

	"github.com/xwysyy/X-Claw/pkg/config"
)

func TestDefaultSharedToolInstallers_SlimToolSurface(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Channels.Feishu.AppID = "app-id"
	cfg.Channels.Feishu.AppSecret = config.SecretRef{Inline: "secret"}

	registry := NewAgentRegistry(cfg, nil)
	agent := registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	registerSharedTools(cfg, nil, registry)

	toolsList := agent.Tools.List()
	for _, keep := range []string{"web_search", "web_fetch", "message", "feishu_calendar"} {
		if !slices.Contains(toolsList, keep) {
			t.Fatalf("expected tool %q to be registered; got=%v", keep, toolsList)
		}
	}
	for _, removed := range []string{"tool_confirm", "find_skills", "install_skill", "handoff", "agents_list", "spawn", "sessions_spawn"} {
		if slices.Contains(toolsList, removed) {
			t.Fatalf("expected tool %q to be absent in slim default surface; got=%v", removed, toolsList)
		}
	}
}
