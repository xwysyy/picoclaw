package agent

import (
	"context"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/internal/core/ports"

	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/logger"
	"github.com/xwysyy/X-Claw/pkg/providers"
	"github.com/xwysyy/X-Claw/pkg/routing"
	"github.com/xwysyy/X-Claw/pkg/session"
	"github.com/xwysyy/X-Claw/pkg/tools"
)

// AgentInstance represents a configured agent with its own workspace, context builder,
// and tool registry. The session manager may be injected by the composition root
// (AgentLoop) to enable shared conversation history across agents.
// Type aliases for core ports. This keeps agent APIs readable while
// using the canonical interface definitions from internal/core.
type (
	ChannelDirectory = ports.ChannelDirectory
	MediaResolver    = ports.MediaResolver
	MediaMeta        = ports.MediaMeta
)

type AgentInstance struct {
	ID            string
	Name          string
	Model         string
	Fallbacks     []string
	Workspace     string
	MaxIterations int
	MaxTokens     int
	Temperature   float64

	ThinkingLevel ThinkingLevel

	// ContextWindow is the target context window (tokens) used for compaction/summarization decisions.
	ContextWindow int

	// Legacy summarization controls (still supported for compatibility).
	SummarizeMessageThreshold int
	SummarizeTokenPercent     int

	Provider       providers.LLMProvider
	Sessions       session.Store
	ContextBuilder *ContextBuilder
	Tools          *tools.ToolRegistry
	SkillsFilter   []string
	Candidates     []providers.FallbackCandidate

	Compaction     CompactionSettings
	ContextPruning ContextPruningSettings
	MemoryVector   MemoryVectorSettings
}

// NewAgentInstance creates an agent instance from config.
func NewAgentInstance(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	cfg *config.Config,
	provider providers.LLMProvider,
) *AgentInstance {
	workspace := resolveAgentWorkspace(agentCfg, defaults)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		logger.WarnCF("agent", "Failed to create agent workspace", map[string]any{
			"workspace": workspace,
			"error":     err.Error(),
		})
	}

	model := resolveAgentModel(agentCfg, defaults)
	fallbacks := resolveAgentFallbacks(agentCfg, defaults)

	restrict := defaults.RestrictToWorkspace
	readRestrict := restrict && !defaults.AllowReadOutsideWorkspace

	allowReadPaths := compilePatterns(cfg.Tools.AllowReadPaths)
	allowWritePaths := compilePatterns(cfg.Tools.AllowWritePaths)

	toolsRegistry, _ := buildBaseAgentToolRegistry(workspace, cfg, restrict, readRestrict, allowReadPaths, allowWritePaths)

	contextBuilder := NewContextBuilder(workspace)

	agentID, agentName, skillsFilter := resolveAgentIdentity(agentCfg)

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

	thinkingLevel := resolveAgentThinkingLevel(cfg, model)
	summarizeMessageThreshold, summarizeTokenPercent := resolveAgentSummaryThresholds(defaults)
	configureAgentContextBuilder(contextBuilder, cfg, maxTokens, pruning, memVec)
	registerAgentMemoryTools(toolsRegistry, contextBuilder, memVec)

	candidates := resolveFallbackCandidates(model, fallbacks, defaults.Provider, cfg)

	return &AgentInstance{
		ID:                        agentID,
		Name:                      agentName,
		Model:                     model,
		Fallbacks:                 fallbacks,
		Workspace:                 workspace,
		MaxIterations:             maxIter,
		MaxTokens:                 maxTokens,
		Temperature:               temperature,
		ThinkingLevel:             thinkingLevel,
		ContextWindow:             maxTokens,
		SummarizeMessageThreshold: summarizeMessageThreshold,
		SummarizeTokenPercent:     summarizeTokenPercent,
		Provider:                  provider,
		// Sessions are injected by the composition root (AgentLoop).
		Sessions:       nil,
		ContextBuilder: contextBuilder,
		Tools:          toolsRegistry,
		SkillsFilter:   skillsFilter,
		Candidates:     candidates,
		Compaction:     compaction,
		ContextPruning: pruning,
		MemoryVector:   memVec,
	}
}

func buildBaseAgentToolRegistry(
	workspace string,
	cfg *config.Config,
	restrict bool,
	readRestrict bool,
	allowReadPaths []*regexp.Regexp,
	allowWritePaths []*regexp.Regexp,
) (*tools.ToolRegistry, *tools.ExecTool) {
	toolsRegistry := tools.NewToolRegistry()

	if cfg.Tools.IsToolEnabled("read_file") {
		readFileTool := tools.NewReadFileTool(workspace, readRestrict, allowReadPaths)
		if cfg != nil && cfg.Limits.Enabled && cfg.Limits.MaxReadFileBytes > 0 {
			readFileTool.SetMaxReadBytes(cfg.Limits.MaxReadFileBytes)
		}
		toolsRegistry.Register(readFileTool)
	}
	if cfg.Tools.IsToolEnabled("document_text") {
		toolsRegistry.Register(tools.NewDocumentTextTool(workspace, readRestrict))
	}
	if cfg.Tools.IsToolEnabled("write_file") {
		toolsRegistry.Register(tools.NewWriteFileTool(workspace, restrict, allowWritePaths))
	}
	if cfg.Tools.IsToolEnabled("list_dir") {
		toolsRegistry.Register(tools.NewListDirTool(workspace, readRestrict, allowReadPaths))
	}
	if cfg.Tools.IsToolEnabled("edit_file") {
		toolsRegistry.Register(tools.NewEditFileTool(workspace, restrict, allowWritePaths))
	}
	if cfg.Tools.IsToolEnabled("append_file") {
		toolsRegistry.Register(tools.NewAppendFileTool(workspace, restrict, allowWritePaths))
	}

	var execTool *tools.ExecTool
	if cfg.Tools.IsToolEnabled("exec") {
		var err error
		execTool, err = tools.NewExecToolWithConfig(workspace, restrict, cfg)
		if err != nil {
			log.Fatalf("Critical error: unable to initialize exec tool: %v", err)
		}
		toolsRegistry.Register(execTool)
	}
	if execTool != nil && cfg.Tools.IsToolEnabled("process") {
		toolsRegistry.Register(tools.NewProcessTool(execTool.ProcessManager()))
	}

	return toolsRegistry, execTool
}

func resolveAgentThinkingLevel(cfg *config.Config, model string) ThinkingLevel {
	var thinkingLevelStr string
	if cfg != nil {
		if mc, err := cfg.GetModelConfig(model); err == nil && mc != nil {
			thinkingLevelStr = mc.ThinkingLevel
		}
	}
	return parseThinkingLevel(thinkingLevelStr)
}

func resolveAgentSummaryThresholds(defaults *config.AgentDefaults) (int, int) {
	summarizeMessageThreshold := defaults.SummarizeMessageThreshold
	if summarizeMessageThreshold == 0 {
		summarizeMessageThreshold = 20
	}

	summarizeTokenPercent := defaults.SummarizeTokenPercent
	if summarizeTokenPercent == 0 {
		summarizeTokenPercent = 75
	}

	return summarizeMessageThreshold, summarizeTokenPercent
}

func configureAgentContextBuilder(
	contextBuilder *ContextBuilder,
	cfg *config.Config,
	maxTokens int,
	pruning ContextPruningSettings,
	memVec MemoryVectorSettings,
) {
	if contextBuilder == nil {
		return
	}

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
}

func registerAgentMemoryTools(
	toolsRegistry *tools.ToolRegistry,
	contextBuilder *ContextBuilder,
	memVec MemoryVectorSettings,
) {
	if toolsRegistry == nil || contextBuilder == nil {
		return
	}
	memoryProvider := func(ctx context.Context) MemoryReader {
		return contextBuilder.MemoryReadForSession(tools.ExecutionSessionKey(ctx), "", "")
	}
	toolsRegistry.Register(NewMemorySearchToolWithProvider(memoryProvider, memVec.TopK, memVec.MinScore))
	toolsRegistry.Register(NewMemoryGetToolWithProvider(memoryProvider))
}

func NewAgentRegistry(
	cfg *config.Config,
	provider providers.LLMProvider,
) *AgentRegistry {
	registry := &AgentRegistry{
		agents:   make(map[string]*AgentInstance),
		resolver: routing.NewRouteResolver(cfg),
	}

	agentConfigs := cfg.Agents.List
	if len(agentConfigs) == 0 {
		implicitAgent := &config.AgentConfig{
			ID:      "main",
			Default: true,
		}
		instance := NewAgentInstance(implicitAgent, &cfg.Agents.Defaults, cfg, provider)
		registry.agents["main"] = instance
		logger.InfoCF("agent", "Created implicit main agent (no agents.list configured)", nil)
	} else {
		for i := range agentConfigs {
			ac := &agentConfigs[i]
			id := routing.NormalizeAgentID(ac.ID)
			instance := NewAgentInstance(ac, &cfg.Agents.Defaults, cfg, provider)
			registry.agents[id] = instance
			logger.InfoCF("agent", "Registered agent",
				map[string]any{
					"agent_id":  id,
					"name":      ac.Name,
					"workspace": instance.Workspace,
					"model":     instance.Model,
				})
		}
	}

	return registry
}

// GetAgent returns the agent instance for a given ID.
func (r *AgentRegistry) GetAgent(agentID string) (*AgentInstance, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id := routing.NormalizeAgentID(agentID)
	agent, ok := r.agents[id]
	return agent, ok
}

// ResolveRoute determines which agent handles the message.
func (r *AgentRegistry) ResolveRoute(input routing.RouteInput) routing.ResolvedRoute {
	return r.resolver.ResolveRoute(input)
}

// ListAgentIDs returns all registered agent IDs.
func (r *AgentRegistry) ListAgentIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.agents))
	for id := range r.agents {
		ids = append(ids, id)
	}
	return ids
}

// GetDefaultAgent returns the default agent instance.
func (r *AgentRegistry) GetDefaultAgent() *AgentInstance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if agent, ok := r.agents["main"]; ok {
		return agent
	}
	for _, agent := range r.agents {
		return agent
	}
	return nil
}

type toolRegistrar struct {
	cfg    *config.Config
	msgBus *bus.MessageBus
}

type sharedToolInstaller func(toolRegistrar, *AgentInstance, string)

func defaultSharedToolInstallers() []sharedToolInstaller {
	return []sharedToolInstaller{
		func(r toolRegistrar, agent *AgentInstance, _ string) { r.registerWebTools(agent) },
		func(r toolRegistrar, agent *AgentInstance, _ string) { r.registerMessageTool(agent) },
		func(r toolRegistrar, agent *AgentInstance, _ string) { r.registerCalendarTool(agent) },
	}
}

func registerSharedTools(
	cfg *config.Config,
	msgBus *bus.MessageBus,
	registry *AgentRegistry,
) {
	registrar := toolRegistrar{
		cfg:    cfg,
		msgBus: msgBus,
	}

	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok {
			continue
		}

		for _, install := range defaultSharedToolInstallers() {
			install(registrar, agent, agentID)
		}
		agent.ContextBuilder.SetToolsRegistry(agent.Tools)
	}
}

func resolveSecretValue(label string, ref config.SecretRef) string {
	if !ref.Present() {
		return ""
	}
	v, err := ref.Resolve("")
	if err != nil {
		logger.WarnCF("agent", "Secret resolve failed (best-effort)", map[string]any{
			"secret": label,
			"error":  err.Error(),
		})
		return ""
	}
	return strings.TrimSpace(v)
}

func resolveSecretValueList(label string, refs []config.SecretRef) []string {
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if v := resolveSecretValue(label, ref); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func buildWebSearchToolOptions(cfg *config.Config) tools.WebSearchToolOptions {
	return tools.WebSearchToolOptions{
		BraveAPIKey:          resolveSecretValue("tools.web.brave.api_key", cfg.Tools.Web.Brave.APIKey),
		BraveAPIKeys:         resolveSecretValueList("tools.web.brave.api_keys", cfg.Tools.Web.Brave.APIKeys),
		BraveMaxResults:      cfg.Tools.Web.Brave.MaxResults,
		BraveEnabled:         cfg.Tools.Web.Brave.Enabled,
		TavilyAPIKey:         resolveSecretValue("tools.web.tavily.api_key", cfg.Tools.Web.Tavily.APIKey),
		TavilyAPIKeys:        resolveSecretValueList("tools.web.tavily.api_keys", cfg.Tools.Web.Tavily.APIKeys),
		TavilyBaseURL:        cfg.Tools.Web.Tavily.BaseURL,
		TavilyMaxResults:     cfg.Tools.Web.Tavily.MaxResults,
		TavilyEnabled:        cfg.Tools.Web.Tavily.Enabled,
		DuckDuckGoMaxResults: cfg.Tools.Web.DuckDuckGo.MaxResults,
		DuckDuckGoEnabled:    cfg.Tools.Web.DuckDuckGo.Enabled,
		PerplexityAPIKey:     resolveSecretValue("tools.web.perplexity.api_key", cfg.Tools.Web.Perplexity.APIKey),
		PerplexityMaxResults: cfg.Tools.Web.Perplexity.MaxResults,
		PerplexityEnabled:    cfg.Tools.Web.Perplexity.Enabled,
		GLMSearchAPIKey:      resolveSecretValue("tools.web.glm_search.api_key", cfg.Tools.Web.GLMSearch.APIKey),
		GLMSearchBaseURL:     cfg.Tools.Web.GLMSearch.BaseURL,
		GLMSearchEngine:      cfg.Tools.Web.GLMSearch.SearchEngine,
		GLMSearchMaxResults:  cfg.Tools.Web.GLMSearch.MaxResults,
		GLMSearchEnabled:     cfg.Tools.Web.GLMSearch.Enabled,
		GrokAPIKey:           resolveSecretValue("tools.web.grok.api_key", cfg.Tools.Web.Grok.APIKey),
		GrokAPIKeys:          resolveSecretValueList("tools.web.grok.api_keys", cfg.Tools.Web.Grok.APIKeys),
		GrokEndpoint:         cfg.Tools.Web.Grok.Endpoint,
		GrokModel:            cfg.Tools.Web.Grok.DefaultModel,
		GrokMaxResults:       cfg.Tools.Web.Grok.MaxResults,
		GrokEnabled:          cfg.Tools.Web.Grok.Enabled,
		Proxy:                cfg.Tools.Web.Proxy,
		EvidenceModeEnabled:  cfg.Tools.Web.Evidence.Enabled,
		EvidenceMinDomains:   cfg.Tools.Web.Evidence.MinDomains,
	}
}

func (r toolRegistrar) registerWebTools(agent *AgentInstance) {
	if r.cfg == nil {
		return
	}
	webOpts := buildWebSearchToolOptions(r.cfg)
	if r.cfg.Tools.IsToolEnabled("web") {
		if searchTool := tools.NewWebSearchTool(webOpts); searchTool != nil {
			agent.Tools.Register(searchTool)
		}
		if dualSearchTool := tools.NewWebSearchDualTool(webOpts); dualSearchTool != nil {
			agent.Tools.Register(dualSearchTool)
		}
	}
	if !r.cfg.Tools.IsToolEnabled("web_fetch") {
		return
	}
	fetchTool, err := tools.NewWebFetchToolWithProxy(50000, r.cfg.Tools.Web.Proxy, r.cfg.Tools.Web.FetchLimitBytes)
	if err != nil {
		logger.ErrorCF("agent", "Failed to create web fetch tool", map[string]any{"error": err.Error()})
		return
	}
	agent.Tools.Register(fetchTool)
}

func (r toolRegistrar) registerMessageTool(agent *AgentInstance) {
	if r.cfg == nil || !r.cfg.Tools.IsToolEnabled("message") {
		return
	}
	messageTool := tools.NewMessageTool()
	messageTool.SetSendCallback(func(ctx context.Context, channel, chatID, content string) error {
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer pubCancel()
		return r.msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
			Channel:    channel,
			ChatID:     chatID,
			Content:    content,
			SessionKey: tools.ExecutionSessionKey(ctx),
		})
	})
	agent.Tools.Register(messageTool)
}

func (r toolRegistrar) registerCalendarTool(agent *AgentInstance) {
	if r.cfg == nil {
		return
	}
	if strings.TrimSpace(r.cfg.Channels.Feishu.AppID) == "" || !r.cfg.Channels.Feishu.AppSecret.Present() {
		return
	}
	agent.Tools.Register(tools.NewFeishuCalendarTool(r.cfg.Channels.Feishu))
}
