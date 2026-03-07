package agent

import (
	"os"
	"slices"
	"testing"

	"github.com/xwysyy/X-Claw/pkg/config"
)

func TestNewAgentInstance_UsesDefaultsTemperatureAndMaxTokens(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-instance-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         1234,
				MaxToolIterations: 5,
			},
		},
	}

	configuredTemp := 1.0
	cfg.Agents.Defaults.Temperature = &configuredTemp

	provider := &mockProvider{}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider)

	if agent.MaxTokens != 1234 {
		t.Fatalf("MaxTokens = %d, want %d", agent.MaxTokens, 1234)
	}
	if agent.Temperature != 1.0 {
		t.Fatalf("Temperature = %f, want %f", agent.Temperature, 1.0)
	}
}

func TestNewAgentInstance_DefaultsTemperatureWhenZero(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-instance-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         1234,
				MaxToolIterations: 5,
			},
		},
	}

	configuredTemp := 0.0
	cfg.Agents.Defaults.Temperature = &configuredTemp

	provider := &mockProvider{}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider)

	if agent.Temperature != 0.0 {
		t.Fatalf("Temperature = %f, want %f", agent.Temperature, 0.0)
	}
}

func TestNewAgentInstance_DefaultsTemperatureWhenUnset(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-instance-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Model:             "test-model",
				MaxTokens:         1234,
				MaxToolIterations: 5,
			},
		},
	}

	provider := &mockProvider{}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider)

	if agent.Temperature != 0.7 {
		t.Fatalf("Temperature = %f, want %f", agent.Temperature, 0.7)
	}
}

func TestNewAgentInstance_ResolveCandidatesFromModelListAlias(t *testing.T) {
	tests := []struct {
		name         string
		aliasName    string
		modelName    string
		apiBase      string
		wantProvider string
		wantModel    string
	}{
		{
			name:         "alias with provider prefix",
			aliasName:    "step-3.5-flash",
			modelName:    "openrouter/stepfun/step-3.5-flash:free",
			apiBase:      "https://openrouter.ai/api/v1",
			wantProvider: "openrouter",
			wantModel:    "stepfun/step-3.5-flash:free",
		},
		{
			name:         "alias without provider prefix",
			aliasName:    "glm-5",
			modelName:    "glm-5",
			apiBase:      "https://api.z.ai/api/coding/paas/v4",
			wantProvider: "openai",
			wantModel:    "glm-5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "agent-instance-test-*")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tmpDir)

			cfg := &config.Config{
				Agents: config.AgentsConfig{
					Defaults: config.AgentDefaults{
						Workspace: tmpDir,
						Model:     tt.aliasName,
					},
				},
				ModelList: []config.ModelConfig{
					{
						ModelName: tt.aliasName,
						Model:     tt.modelName,
						APIBase:   tt.apiBase,
					},
				},
			}

			provider := &mockProvider{}
			agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider)

			if len(agent.Candidates) != 1 {
				t.Fatalf("len(Candidates) = %d, want 1", len(agent.Candidates))
			}
			if agent.Candidates[0].Provider != tt.wantProvider {
				t.Fatalf("candidate provider = %q, want %q", agent.Candidates[0].Provider, tt.wantProvider)
			}
			if agent.Candidates[0].Model != tt.wantModel {
				t.Fatalf("candidate model = %q, want %q", agent.Candidates[0].Model, tt.wantModel)
			}
		})
	}
}

func TestResolveFallbackCandidates_DefaultsBareModelsToOpenAI(t *testing.T) {
	candidates := resolveFallbackCandidates("gpt-4o-mini", []string{"anthropic/claude-3-5-haiku"}, "", &config.Config{})

	if len(candidates) != 2 {
		t.Fatalf("len(candidates) = %d, want 2", len(candidates))
	}
	if candidates[0].Provider != "openai" || candidates[0].Model != "gpt-4o-mini" {
		t.Fatalf("primary candidate = %+v, want provider=openai model=gpt-4o-mini", candidates[0])
	}
	if candidates[1].Provider != "anthropic" || candidates[1].Model != "claude-3-5-haiku" {
		t.Fatalf("fallback candidate = %+v, want provider=anthropic model=claude-3-5-haiku", candidates[1])
	}
}

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

func TestResolveCompaction_Defaults(t *testing.T) {
	got := resolveCompaction(config.AgentCompactionConfig{})

	if got.Mode != "safeguard" {
		t.Fatalf("Mode = %q, want %q", got.Mode, "safeguard")
	}
	if got.ReserveTokens != 2048 {
		t.Fatalf("ReserveTokens = %d, want %d", got.ReserveTokens, 2048)
	}
	if got.KeepRecentTokens != 2048 {
		t.Fatalf("KeepRecentTokens = %d, want %d", got.KeepRecentTokens, 2048)
	}
	if got.MaxHistoryShare != 0.5 {
		t.Fatalf("MaxHistoryShare = %v, want %v", got.MaxHistoryShare, 0.5)
	}

	// Default behavior: memory flush is enabled unless explicitly configured.
	if got.MemoryFlushEnabled != true {
		t.Fatalf("MemoryFlushEnabled = %v, want true", got.MemoryFlushEnabled)
	}
	if got.MemoryFlushSoftThreshold != 1500 {
		t.Fatalf("MemoryFlushSoftThreshold = %d, want %d", got.MemoryFlushSoftThreshold, 1500)
	}
}

func TestResolveCompaction_MemoryFlushExplicitDisable(t *testing.T) {
	got := resolveCompaction(config.AgentCompactionConfig{
		MemoryFlush: config.AgentCompactionMemoryFlushConfig{
			Enabled:             false,
			SoftThresholdTokens: 100,
		},
	})

	if got.MemoryFlushEnabled != false {
		t.Fatalf("MemoryFlushEnabled = %v, want false", got.MemoryFlushEnabled)
	}
	if got.MemoryFlushSoftThreshold != 100 {
		t.Fatalf("MemoryFlushSoftThreshold = %d, want %d", got.MemoryFlushSoftThreshold, 100)
	}
}

func TestResolveContextPruning_DefaultsAndBounds(t *testing.T) {
	got := resolveContextPruning(config.AgentContextPruningConfig{
		// Intentionally set out-of-range values to exercise fallback.
		Mode:         "",
		TriggerRatio: 1, // invalid (hi exclusive)
	})

	if got.Mode != "tools_only" {
		t.Fatalf("Mode = %q, want %q", got.Mode, "tools_only")
	}
	if got.SoftToolChars != 2000 {
		t.Fatalf("SoftToolChars = %d, want %d", got.SoftToolChars, 2000)
	}
	if got.HardToolChars != 350 {
		t.Fatalf("HardToolChars = %d, want %d", got.HardToolChars, 350)
	}
	if got.TriggerRatio != 0.8 {
		t.Fatalf("TriggerRatio = %v, want %v", got.TriggerRatio, 0.8)
	}
}

func TestResolveMemoryVector_Defaults(t *testing.T) {
	got := resolveMemoryVector(config.AgentMemoryVectorConfig{})

	if got.Enabled {
		t.Fatalf("Enabled = true, want false")
	}
	if got.Dimensions != defaultMemoryVectorDimensions {
		t.Fatalf("Dimensions = %d, want %d", got.Dimensions, defaultMemoryVectorDimensions)
	}
	if got.TopK != defaultMemoryVectorTopK {
		t.Fatalf("TopK = %d, want %d", got.TopK, defaultMemoryVectorTopK)
	}
	if got.MinScore != defaultMemoryVectorMinScore {
		t.Fatalf("MinScore = %v, want %v", got.MinScore, defaultMemoryVectorMinScore)
	}
	if got.MaxContextChars != defaultMemoryVectorMaxContextChars {
		t.Fatalf("MaxContextChars = %d, want %d", got.MaxContextChars, defaultMemoryVectorMaxContextChars)
	}
	if got.RecentDailyDays != defaultMemoryVectorRecentDailyDays {
		t.Fatalf("RecentDailyDays = %d, want %d", got.RecentDailyDays, defaultMemoryVectorRecentDailyDays)
	}
}
