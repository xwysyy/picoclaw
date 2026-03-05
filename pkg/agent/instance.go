package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// AgentInstance represents a configured agent with its own workspace, context builder,
// and tool registry. The session manager may be injected by the composition root
// (AgentLoop) to enable shared conversation history across agents.
type AgentInstance struct {
	ID              string
	Name            string
	Model           string
	Fallbacks       []string
	Workspace       string
	MaxIterations   int
	MaxTokens       int
	Temperature     float64
	ContextWindow   int
	Provider        providers.LLMProvider
	Sessions        *session.SessionManager
	ContextBuilder  *ContextBuilder
	Tools           *tools.ToolRegistry
	SubagentManager *tools.SubagentManager
	Subagents       *config.SubagentsConfig
	SkillsFilter    []string
	Candidates      []providers.FallbackCandidate

	Compaction     CompactionSettings
	ContextPruning ContextPruningSettings
	MemoryVector   MemoryVectorSettings
}

// CompactionSettings groups all compaction-related parameters.
type CompactionSettings struct {
	Mode                     string
	ReserveTokens            int
	KeepRecentTokens         int
	MaxHistoryShare          float64
	MemoryFlushEnabled       bool
	MemoryFlushSoftThreshold int
	NotifyUser               bool
}

// ContextPruningSettings groups all context pruning parameters.
type ContextPruningSettings struct {
	Mode              string
	IncludeChitChat   bool
	SoftToolChars     int
	HardToolChars     int
	TriggerRatio      float64
	BootstrapSnapshot bool
}

// resolveCompaction resolves compaction settings from config with defaults.
func resolveCompaction(c config.AgentCompactionConfig) CompactionSettings {
	mode := strings.TrimSpace(c.Mode)
	if mode == "" {
		mode = "safeguard"
	}

	flushEnabled := c.MemoryFlush.Enabled
	if !c.MemoryFlush.Enabled && c.MemoryFlush.SoftThresholdTokens == 0 {
		flushEnabled = true
	}

	return CompactionSettings{
		Mode:                     mode,
		ReserveTokens:            intDefault(c.ReserveTokens, 2048),
		KeepRecentTokens:         intDefault(c.KeepRecentTokens, 2048),
		MaxHistoryShare:          floatRangeDefault(c.MaxHistoryShare, 0, 0.9, 0.5),
		MemoryFlushEnabled:       flushEnabled,
		MemoryFlushSoftThreshold: intDefault(c.MemoryFlush.SoftThresholdTokens, 1500),
		NotifyUser:               c.NotifyUser,
	}
}

// resolveContextPruning resolves context pruning settings from config with defaults.
func resolveContextPruning(c config.AgentContextPruningConfig) ContextPruningSettings {
	mode := strings.TrimSpace(c.Mode)
	if mode == "" {
		mode = "tools_only"
	}
	return ContextPruningSettings{
		Mode:            mode,
		IncludeChitChat: c.IncludeOldChitChat,
		SoftToolChars:   intDefault(c.SoftToolResultChars, 2000),
		HardToolChars:   intDefault(c.HardToolResultChars, 350),
		TriggerRatio:    floatRangeDefault(c.TriggerRatio, 0, 1, 0.8),
	}
}

// resolveMemoryVector resolves memory vector settings from config with defaults.
func resolveMemoryVector(c config.AgentMemoryVectorConfig) MemoryVectorSettings {
	return MemoryVectorSettings{
		Enabled:         c.Enabled,
		Dimensions:      intDefault(c.Dimensions, defaultMemoryVectorDimensions),
		TopK:            intDefault(c.TopK, defaultMemoryVectorTopK),
		MinScore:        floatRangeDefault(c.MinScore, 0, 1, defaultMemoryVectorMinScore),
		MaxContextChars: intDefault(c.MaxContextChars, defaultMemoryVectorMaxContextChars),
		RecentDailyDays: intDefault(c.RecentDailyDays, defaultMemoryVectorRecentDailyDays),
		Embedding: MemoryVectorEmbeddingSettings{
			Kind:                  c.Embedding.Kind,
			APIKey:                c.Embedding.APIKey,
			APIBase:               c.Embedding.APIBase,
			Model:                 c.Embedding.Model,
			Proxy:                 c.Embedding.Proxy,
			BatchSize:             c.Embedding.BatchSize,
			RequestTimeoutSeconds: c.Embedding.RequestTimeoutSeconds,
		},
		Hybrid: MemoryHybridSettings{
			FTSWeight:    c.Hybrid.FTSWeight,
			VectorWeight: c.Hybrid.VectorWeight,
		},
	}
}

// intDefault returns v if positive, otherwise fallback.
func intDefault(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

// floatRangeDefault returns v if within (lo, hi) exclusive, otherwise fallback.
func floatRangeDefault(v, lo, hi, fallback float64) float64 {
	if v <= lo || v >= hi {
		return fallback
	}
	return v
}

// NewAgentInstance creates an agent instance from config.
func NewAgentInstance(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	cfg *config.Config,
	provider providers.LLMProvider,
) *AgentInstance {
	workspace := resolveAgentWorkspace(agentCfg, defaults)
	os.MkdirAll(workspace, 0o755)

	model := resolveAgentModel(agentCfg, defaults)
	fallbacks := resolveAgentFallbacks(agentCfg, defaults)

	restrict := defaults.RestrictToWorkspace
	readRestrict := restrict && !defaults.AllowReadOutsideWorkspace

	// Compile path whitelist patterns from config.
	allowReadPaths := compilePatterns(cfg.Tools.AllowReadPaths)
	allowWritePaths := compilePatterns(cfg.Tools.AllowWritePaths)

	toolsRegistry := tools.NewToolRegistry()
	readFileTool := tools.NewReadFileTool(workspace, readRestrict, allowReadPaths)
	if cfg != nil && cfg.Limits.Enabled && cfg.Limits.MaxReadFileBytes > 0 {
		readFileTool.SetMaxReadBytes(cfg.Limits.MaxReadFileBytes)
	}
	toolsRegistry.Register(readFileTool)
	toolsRegistry.Register(tools.NewDocumentTextTool(workspace, readRestrict))
	toolsRegistry.Register(tools.NewWriteFileTool(workspace, restrict, allowWritePaths))
	toolsRegistry.Register(tools.NewListDirTool(workspace, readRestrict, allowReadPaths))
	execTool, err := tools.NewExecToolWithConfig(workspace, restrict, cfg)
	if err != nil {
		log.Fatalf("Critical error: unable to initialize exec tool: %v", err)
	}
	toolsRegistry.Register(execTool)
	toolsRegistry.Register(tools.NewProcessTool(execTool.ProcessManager()))

	toolsRegistry.Register(tools.NewEditFileTool(workspace, restrict, allowWritePaths))
	toolsRegistry.Register(tools.NewAppendFileTool(workspace, restrict, allowWritePaths))

	contextBuilder := NewContextBuilder(workspace)

	agentID, agentName, subagents, skillsFilter := resolveAgentIdentity(agentCfg)

	maxIter := intDefault(defaults.MaxToolIterations, 20)
	maxTokens := intDefault(defaults.MaxTokens, 8192)

	temperature := 0.7
	if defaults.Temperature != nil {
		temperature = *defaults.Temperature
	}

	compaction := resolveCompaction(defaults.Compaction)
	pruning := resolveContextPruning(defaults.ContextPruning)
	pruning.BootstrapSnapshot = defaults.BootstrapSnapshot.Enabled
	memVec := resolveMemoryVector(defaults.MemoryVector)

	contextBuilder.SetRuntimeSettings(ContextRuntimeSettings{
		ContextWindowTokens:      maxTokens,
		PruningMode:              pruning.Mode,
		IncludeOldChitChat:       pruning.IncludeChitChat,
		SoftToolResultChars:      pruning.SoftToolChars,
		HardToolResultChars:      pruning.HardToolChars,
		TriggerRatio:             pruning.TriggerRatio,
		BootstrapSnapshotEnabled: pruning.BootstrapSnapshot,
		MemoryVectorEnabled:      memVec.Enabled,
		MemoryVectorDimensions:   memVec.Dimensions,
		MemoryVectorTopK:         memVec.TopK,
		MemoryVectorMinScore:     memVec.MinScore,
		MemoryVectorMaxChars:     memVec.MaxContextChars,
		MemoryVectorRecentDays:   memVec.RecentDailyDays,
		MemoryVectorEmbedding:    memVec.Embedding,
		MemoryHybrid:             memVec.Hybrid,
	})
	if cfg != nil {
		contextBuilder.SetWebEvidenceMode(cfg.Tools.Web.Evidence.Enabled, cfg.Tools.Web.Evidence.MinDomains)
	}

	memoryProvider := func(ctx context.Context) MemoryReader {
		if contextBuilder == nil {
			return nil
		}
		return contextBuilder.MemoryReadForSession(tools.ExecutionSessionKey(ctx), "", "")
	}
	toolsRegistry.Register(NewMemorySearchToolWithProvider(memoryProvider, memVec.TopK, memVec.MinScore))
	toolsRegistry.Register(NewMemoryGetToolWithProvider(memoryProvider))

	candidates := resolveFallbackCandidates(model, fallbacks, defaults.Provider, cfg)

	return &AgentInstance{
		ID:            agentID,
		Name:          agentName,
		Model:         model,
		Fallbacks:     fallbacks,
		Workspace:     workspace,
		MaxIterations: maxIter,
		MaxTokens:     maxTokens,
		Temperature:   temperature,
		ContextWindow: maxTokens,
		Provider:      provider,
		// Sessions are injected by the composition root (AgentLoop) so multi-agent
		// handoff can share one conversation history across agents.
		Sessions:       nil,
		ContextBuilder: contextBuilder,
		Tools:          toolsRegistry,
		Subagents:      subagents,
		SkillsFilter:   skillsFilter,
		Candidates:     candidates,
		Compaction:     compaction,
		ContextPruning: pruning,
		MemoryVector:   memVec,
	}
}

// resolveAgentIdentity extracts identity fields from agent config.
func resolveAgentIdentity(agentCfg *config.AgentConfig) (id, name string, subagents *config.SubagentsConfig, skills []string) {
	if agentCfg == nil {
		return routing.DefaultAgentID, "", nil, nil
	}
	return routing.NormalizeAgentID(agentCfg.ID), agentCfg.Name, agentCfg.Subagents, agentCfg.Skills
}

// resolveFallbackCandidates builds the fallback candidate list from model config.
func resolveFallbackCandidates(model string, fallbacks []string, defaultProvider string, cfg *config.Config) []providers.FallbackCandidate {
	modelCfg := providers.ModelConfig{
		Primary:   model,
		Fallbacks: fallbacks,
	}
	lookup := func(raw string) (string, bool) {
		raw = strings.TrimSpace(raw)
		if raw == "" || cfg == nil {
			return "", false
		}
		if mc, err := cfg.GetModelConfig(raw); err == nil && mc != nil && strings.TrimSpace(mc.Model) != "" {
			return ensureProtocol(mc.Model), true
		}
		for i := range cfg.ModelList {
			fullModel := strings.TrimSpace(cfg.ModelList[i].Model)
			if fullModel == "" {
				continue
			}
			if fullModel == raw {
				return ensureProtocol(fullModel), true
			}
			if _, modelID := providers.ExtractProtocol(fullModel); modelID == raw {
				return ensureProtocol(fullModel), true
			}
		}
		return "", false
	}
	return providers.ResolveCandidatesWithLookup(modelCfg, defaultProvider, lookup)
}

// ensureProtocol adds "openai/" prefix to bare model names.
func ensureProtocol(model string) string {
	model = strings.TrimSpace(model)
	if model == "" || strings.Contains(model, "/") {
		return model
	}
	return "openai/" + model
}

// resolveAgentWorkspace determines the workspace directory for an agent.
func resolveAgentWorkspace(agentCfg *config.AgentConfig, defaults *config.AgentDefaults) string {
	if agentCfg != nil && strings.TrimSpace(agentCfg.Workspace) != "" {
		return expandHome(strings.TrimSpace(agentCfg.Workspace))
	}
	if agentCfg == nil || agentCfg.Default || agentCfg.ID == "" || routing.NormalizeAgentID(agentCfg.ID) == "main" {
		return expandHome(defaults.Workspace)
	}
	home, _ := os.UserHomeDir()
	id := routing.NormalizeAgentID(agentCfg.ID)
	return filepath.Join(home, ".picoclaw", "workspace-"+id)
}

// resolveAgentModel resolves the primary model for an agent.
func resolveAgentModel(agentCfg *config.AgentConfig, defaults *config.AgentDefaults) string {
	if agentCfg != nil && agentCfg.Model != nil && strings.TrimSpace(agentCfg.Model.Primary) != "" {
		return strings.TrimSpace(agentCfg.Model.Primary)
	}
	return defaults.GetModelName()
}

// resolveAgentFallbacks resolves the fallback models for an agent.
func resolveAgentFallbacks(agentCfg *config.AgentConfig, defaults *config.AgentDefaults) []string {
	if agentCfg != nil && agentCfg.Model != nil && agentCfg.Model.Fallbacks != nil {
		return agentCfg.Model.Fallbacks
	}
	return defaults.ModelFallbacks
}

func compilePatterns(patterns []string) []*regexp.Regexp {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			fmt.Printf("Warning: invalid path pattern %q: %v\n", p, err)
			continue
		}
		compiled = append(compiled, re)
	}
	return compiled
}

func expandHome(path string) string {
	if path == "" {
		return path
	}
	if path[0] == '~' {
		home, _ := os.UserHomeDir()
		if len(path) > 1 && path[1] == '/' {
			return home + path[1:]
		}
		return home
	}
	return path
}
