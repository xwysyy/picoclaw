package gateway

import (
	"slices"
	"testing"

	"github.com/xwysyy/X-Claw/pkg/config"
)

func TestBuildGatewayHTTPRegistrations_SlimSurface(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()

	regs, err := buildGatewayHTTPRegistrations(&gatewayServices{cfg: cfg})
	if err != nil {
		t.Fatalf("buildGatewayHTTPRegistrations error: %v", err)
	}

	patterns := make([]string, 0, len(regs))
	for _, reg := range regs {
		patterns = append(patterns, reg.pattern)
	}

	for _, keep := range []string{"/api/notify", "/api/resume_last_task", "/api/session_model", "/console/", "/api/console/"} {
		if !slices.Contains(patterns, keep) {
			t.Fatalf("expected pattern %q to be registered; got=%v", keep, patterns)
		}
	}
	for _, removed := range []string{"/api/estop", "/api/security"} {
		if slices.Contains(patterns, removed) {
			t.Fatalf("expected pattern %q to be absent in slim runtime; got=%v", removed, patterns)
		}
	}
}
