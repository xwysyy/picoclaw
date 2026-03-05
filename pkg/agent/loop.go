// X-Claw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 X-Claw contributors

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xwysyy/X-Claw/pkg/auditlog"
	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/constants"
	"github.com/xwysyy/X-Claw/pkg/logger"
	"github.com/xwysyy/X-Claw/pkg/mcp"
	"github.com/xwysyy/X-Claw/pkg/providers"
	"github.com/xwysyy/X-Claw/pkg/routing"
	"github.com/xwysyy/X-Claw/pkg/session"
	"github.com/xwysyy/X-Claw/pkg/skills"
	"github.com/xwysyy/X-Claw/pkg/state"
	"github.com/xwysyy/X-Claw/pkg/tools"
	"github.com/xwysyy/X-Claw/pkg/utils"
	"github.com/xwysyy/X-Claw/pkg/voice"
)

type AgentLoop struct {
	bus              *bus.MessageBus
	cfgMu            sync.RWMutex
	cfg              *config.Config
	registry         *AgentRegistry
	sessions         session.Store
	state            *state.Manager
	taskLedger       *tools.TaskLedger
	running          atomic.Bool
	summarizing      sync.Map
	fallback         *providers.FallbackChain
	channelDirectory ChannelDirectory
	mediaResolver    MediaResolver
	transcriber      voice.Transcriber
	mcpMgr           *mcp.Manager

	tokenUsageMu     sync.Mutex
	tokenUsageStores map[string]*tokenUsageStore // workspace → store

	modelAutoMu           sync.Mutex
	modelAutoDowngradeMap map[string]sessionModelAutoDowngradeState // session_key -> state
}

// processOptions configures how a message is processed
type processOptions struct {
	SessionKey  string // Session identifier for history/context
	Channel     string // Target channel for tool execution
	ChatID      string // Target chat ID for tool execution
	SenderID    string // Message sender identifier for tool execution
	UserMessage string // User message content (may include trigger prefix)
	Media       []string

	DefaultResponse string // Response when LLM returns empty
	EnableSummary   bool   // Whether to trigger summarization
	SendResponse    bool   // Whether to send response via bus
	NoHistory       bool   // If true, don't load session history (for heartbeat)

	// Steering provides out-of-band user messages delivered while this run is still
	// executing. It is used by the Gateway inbound queue to support "/steer ..."
	// without waiting for the current run to finish.
	Steering <-chan bus.InboundMessage

	// PlanMode enables "plan" permission mode (ROADMAP.md:1225).
	// When true, side-effect tools are denied by the tool executor.
	PlanMode bool

	// WorkingState carries per-run structured progress state. It must be per-run
	// (not stored globally on the agent) so multiple sessions can be processed
	// concurrently without cross-talk.
	WorkingState *WorkingState

	// Phase E2: resume support
	RunID  string // optional: resume into an existing run_id
	Resume bool   // true when invoked by resume_last_task
}

const defaultResponse = "I've completed processing but have no response to give. Increase `max_tool_iterations` in config.json."

// isLLMTimeoutError checks if an error is a network/HTTP timeout (not a context window error).
func isLLMTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "client.timeout") ||
		strings.Contains(msg, "timed out") ||
		strings.Contains(msg, "timeout exceeded")
}

// isContextWindowError detects real context window / token limit errors.
func isContextWindowError(err error) bool {
	if isLLMTimeoutError(err) {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context_length_exceeded") ||
		strings.Contains(msg, "context window") ||
		strings.Contains(msg, "maximum context length") ||
		strings.Contains(msg, "token limit") ||
		strings.Contains(msg, "too many tokens") ||
		strings.Contains(msg, "max_tokens") ||
		strings.Contains(msg, "invalidparameter") ||
		strings.Contains(msg, "prompt is too long") ||
		strings.Contains(msg, "request too large")
}

type sessionModelAutoDowngradeState struct {
	TargetModel string
	Streak      int
	LastAt      time.Time
}

func NewAgentLoop(cfg *config.Config, msgBus *bus.MessageBus, provider providers.LLMProvider) *AgentLoop {
	registry := NewAgentRegistry(cfg, provider)

	// Set up shared fallback chain
	cooldown := providers.NewCooldownTracker()
	fallbackChain := providers.NewFallbackChain(cooldown)

	// MCP Bridge manager (Phase D1/D2).
	mcpMgr := mcp.NewManager()

	// Create state manager using default agent's workspace for channel recording
	defaultAgent := registry.GetDefaultAgent()
	var stateManager *state.Manager
	ledgerPath := filepath.Join(cfg.WorkspacePath(), "tasks", "ledger.json")
	sessionsPath := filepath.Join(cfg.WorkspacePath(), "sessions")
	if defaultAgent != nil {
		stateManager = state.NewManager(defaultAgent.Workspace)
		ledgerPath = filepath.Join(defaultAgent.Workspace, "tasks", "ledger.json")
		sessionsPath = filepath.Join(defaultAgent.Workspace, "sessions")
	}
	taskLedger := tools.NewTaskLedger(ledgerPath)

	// Phase F: shared sessions for Swarm-style multi-agent handoff.
	// Conversation history is shared across agents; the session itself stores active_agent_id.
	sharedSessions := session.NewSessionManager(sessionsPath)
	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok || agent == nil {
			continue
		}
		agent.Sessions = sharedSessions
		// Re-register session tools against the shared session manager.
		agent.Tools.Register(tools.NewSessionsListTool(sharedSessions))
		agent.Tools.Register(tools.NewSessionsHistoryTool(sharedSessions))
	}

	al := &AgentLoop{
		bus:         msgBus,
		cfg:         cfg,
		registry:    registry,
		sessions:    sharedSessions,
		state:       stateManager,
		taskLedger:  taskLedger,
		summarizing: sync.Map{},
		fallback:    fallbackChain,
		mcpMgr:      mcpMgr,

		tokenUsageStores: make(map[string]*tokenUsageStore),

		modelAutoDowngradeMap: make(map[string]sessionModelAutoDowngradeState),
	}

	// Register shared tools to all agents.
	registerSharedTools(cfg, msgBus, registry, provider, al, taskLedger)

	// Phase H3: append-only operational audit log.
	al.configureAuditLog(cfg)

	return al
}

// Config returns the current configuration snapshot for the agent loop.
func (al *AgentLoop) Config() *config.Config {
	if al == nil {
		return nil
	}
	al.cfgMu.RLock()
	defer al.cfgMu.RUnlock()
	return al.cfg
}

// SessionStore returns the shared session store used by this agent loop.
// It may be nil if the loop is not fully initialized.
func (al *AgentLoop) SessionStore() session.Store {
	if al == nil {
		return nil
	}
	return al.sessions
}

// SetConfig swaps the configuration pointer used by the agent loop.
// This is used by the gateway config hot reload path.
func (al *AgentLoop) SetConfig(cfg *config.Config) {
	if al == nil {
		return
	}
	al.cfgMu.Lock()
	al.cfg = cfg
	al.cfgMu.Unlock()

	// Keep audit log writers in sync with hot-reloaded config.
	al.configureAuditLog(cfg)
}

func (al *AgentLoop) configureAuditLog(cfg *config.Config) {
	if al == nil || cfg == nil {
		return
	}

	// Configure for the "main" workspace path as well as each agent workspace
	// (multi-agent setups may use per-agent workspaces).
	auditlog.Configure(cfg.WorkspacePath(), cfg.AuditLog)
	if al.registry == nil {
		return
	}
	for _, agentID := range al.registry.ListAgentIDs() {
		agent, ok := al.registry.GetAgent(agentID)
		if !ok || agent == nil {
			continue
		}
		auditlog.Configure(agent.Workspace, cfg.AuditLog)
	}
}

// ReloadMCPTools refreshes MCP servers and re-registers tools into each agent registry.
// This is best-effort and safe to call multiple times.
func (al *AgentLoop) ReloadMCPTools(ctx context.Context) {
	if al == nil || al.registry == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	cfg := al.Config()

	// Always unregister old MCP tools first to avoid stale tool definitions.
	for _, agentID := range al.registry.ListAgentIDs() {
		agent, ok := al.registry.GetAgent(agentID)
		if !ok || agent == nil || agent.Tools == nil {
			continue
		}
		agent.Tools.UnregisterPrefix("mcp_")
		if agent.ContextBuilder != nil {
			agent.ContextBuilder.InvalidateCache()
		}
	}

	oldMgr := al.mcpMgr

	// Disabled or empty config → close connections and exit.
	if cfg == nil || !cfg.Tools.MCP.Enabled || len(cfg.Tools.MCP.Servers) == 0 {
		if oldMgr != nil {
			_ = oldMgr.Close()
		}
		al.mcpMgr = mcp.NewManager()
		return
	}

	newMgr := mcp.NewManager()
	if err := newMgr.LoadFromConfig(ctx, cfg); err != nil {
		logger.WarnCF("agent", "MCP manager load failed (best-effort)", map[string]any{
			"error": err.Error(),
		})
	}

	// Deterministic registration order for stable prompts / KV cache.
	all := newMgr.GetAllTools()
	serverNames := make([]string, 0, len(all))
	for name := range all {
		serverNames = append(serverNames, name)
	}
	sort.Strings(serverNames)

	for _, agentID := range al.registry.ListAgentIDs() {
		agent, ok := al.registry.GetAgent(agentID)
		if !ok || agent == nil || agent.Tools == nil {
			continue
		}

		for _, serverName := range serverNames {
			for _, toolDef := range all[serverName] {
				if toolDef == nil {
					continue
				}
				agent.Tools.Register(tools.NewMCPTool(newMgr, serverName, toolDef))
			}
		}

		if agent.ContextBuilder != nil {
			agent.ContextBuilder.InvalidateCache()
		}
	}

	al.mcpMgr = newMgr
	if oldMgr != nil {
		_ = oldMgr.Close()
	}
}

func (al *AgentLoop) tokenUsageStore(workspace string) *tokenUsageStore {
	if al == nil {
		return nil
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil
	}

	al.tokenUsageMu.Lock()
	defer al.tokenUsageMu.Unlock()

	if al.tokenUsageStores == nil {
		al.tokenUsageStores = make(map[string]*tokenUsageStore)
	}
	if s, ok := al.tokenUsageStores[workspace]; ok && s != nil {
		return s
	}
	s := newTokenUsageStore(workspace)
	al.tokenUsageStores[workspace] = s
	return s
}

func pickFirstDifferentModel(current string, candidates []providers.FallbackCandidate) string {
	current = strings.TrimSpace(current)
	for _, c := range candidates {
		m := strings.TrimSpace(c.Model)
		if m == "" {
			continue
		}
		if current == "" || !strings.EqualFold(m, current) {
			return m
		}
	}
	return ""
}

func (al *AgentLoop) clearModelAutoDowngradeState(sessionKey string) {
	if al == nil {
		return
	}
	sessionKey = utils.CanonicalSessionKey(sessionKey)
	if sessionKey == "" {
		return
	}
	al.modelAutoMu.Lock()
	delete(al.modelAutoDowngradeMap, sessionKey)
	al.modelAutoMu.Unlock()
}

func (al *AgentLoop) maybeAutoDowngradeSessionModel(
	workspace string,
	trace *runTraceWriter,
	agentID string,
	sessionKey string,
	runID string,
	channel string,
	chatID string,
	senderID string,
	iteration int,
	fromModel string,
	targetModel string,
	trigger string,
	fallbackAttempts []providers.FallbackAttempt,
) bool {
	if al == nil {
		return false
	}
	cfg := al.Config()
	if cfg == nil {
		return false
	}

	sessionKey = utils.CanonicalSessionKey(sessionKey)
	if sessionKey == "" {
		return false
	}

	targetModel = strings.TrimSpace(targetModel)
	fromModel = strings.TrimSpace(fromModel)
	if targetModel == "" || strings.EqualFold(targetModel, fromModel) {
		return false
	}

	policy := cfg.Agents.Defaults.SessionModelAutoDowngrade
	if !policy.Enabled {
		return false
	}
	if al.sessions == nil {
		return false
	}
	// Respect explicit/manual overrides.
	if _, ok := al.sessions.EffectiveModelOverride(sessionKey); ok {
		return false
	}

	threshold := policy.Threshold
	if threshold <= 0 {
		threshold = 2
	}
	windowMinutes := policy.WindowMinutes
	if windowMinutes <= 0 {
		windowMinutes = 15
	}
	ttlMinutes := policy.TTLMinutes
	if ttlMinutes <= 0 {
		ttlMinutes = 60
	}

	window := time.Duration(windowMinutes) * time.Minute
	ttl := time.Duration(ttlMinutes) * time.Minute

	now := time.Now()

	al.modelAutoMu.Lock()
	state := al.modelAutoDowngradeMap[sessionKey]
	if !state.LastAt.IsZero() && now.Sub(state.LastAt) > window {
		state = sessionModelAutoDowngradeState{}
	}
	if state.TargetModel != "" && !strings.EqualFold(strings.TrimSpace(state.TargetModel), targetModel) {
		state = sessionModelAutoDowngradeState{}
	}
	state.TargetModel = targetModel
	state.LastAt = now
	state.Streak++
	shouldSwitch := state.Streak >= threshold
	if !shouldSwitch {
		al.modelAutoDowngradeMap[sessionKey] = state
		al.modelAutoMu.Unlock()
		return false
	}
	delete(al.modelAutoDowngradeMap, sessionKey)
	al.modelAutoMu.Unlock()

	expiresAt, err := al.sessions.SetModelOverride(sessionKey, targetModel, ttl)
	if err != nil {
		logger.WarnCF("agent", "Session model auto-downgrade failed (best-effort)", map[string]any{
			"session_key": sessionKey,
			"from_model":  fromModel,
			"to_model":    targetModel,
			"error":       err.Error(),
		})
		return false
	}

	// Audit log (Phase H3): must be traceable.
	reasons := make(map[string]int)
	for _, a := range fallbackAttempts {
		r := strings.TrimSpace(string(a.Reason))
		if r == "" {
			continue
		}
		reasons[r]++
	}
	reasonKeys := make([]string, 0, len(reasons))
	for k := range reasons {
		reasonKeys = append(reasonKeys, k)
	}
	sort.Strings(reasonKeys)
	reasonParts := make([]string, 0, len(reasonKeys))
	for _, k := range reasonKeys {
		reasonParts = append(reasonParts, fmt.Sprintf("%s=%d", k, reasons[k]))
	}
	reasonSummary := strings.Join(reasonParts, ",")

	expiresText := ""
	if expiresAt != nil {
		expiresText = expiresAt.UTC().Format(time.RFC3339Nano)
	}
	note := fmt.Sprintf(
		"trigger=%s from=%q to=%q threshold=%d window_minutes=%d ttl_minutes=%d attempts=%d reasons=%q expires_at=%s",
		strings.TrimSpace(trigger),
		fromModel,
		targetModel,
		threshold,
		windowMinutes,
		ttlMinutes,
		len(fallbackAttempts),
		reasonSummary,
		expiresText,
	)
	auditlog.Record(workspace, auditlog.Event{
		Type:       "session.model_auto_downgrade",
		Source:     "agent",
		RunID:      strings.TrimSpace(runID),
		SessionKey: sessionKey,
		Channel:    strings.TrimSpace(channel),
		ChatID:     strings.TrimSpace(chatID),
		SenderID:   strings.TrimSpace(senderID),
		Iteration:  iteration,
		Note:       note,
	})

	if trace != nil {
		trace.appendEvent(runTraceEvent{
			Type: "model.autodowngrade",

			TS:   now.UTC().Format(time.RFC3339Nano),
			TSMS: now.UnixMilli(),

			RunID:      strings.TrimSpace(runID),
			SessionKey: sessionKey,
			Channel:    strings.TrimSpace(channel),
			ChatID:     strings.TrimSpace(chatID),
			SenderID:   strings.TrimSpace(senderID),

			AgentID: strings.TrimSpace(agentID),
			Model:   targetModel,

			Iteration:       iteration,
			ResponsePreview: utils.Truncate(note, 400),
		})
	}

	logger.InfoCF("agent", "Session model auto-downgrade applied",
		map[string]any{
			"session_key": sessionKey,
			"from_model":  fromModel,
			"to_model":    targetModel,
			"trigger":     strings.TrimSpace(trigger),
			"expires_at":  expiresText,
		})

	return true
}

// registerSharedTools registers tools that are shared across all agents (web, message, spawn).
func registerSharedTools(
	cfg *config.Config,
	msgBus *bus.MessageBus,
	registry *AgentRegistry,
	provider providers.LLMProvider,
	sessionsExecutor tools.SessionsSendExecutor,
	taskLedger *tools.TaskLedger,
) {
	listAgents := func() []tools.AgentInfo {
		ids := registry.ListAgentIDs()
		sort.Strings(ids)
		out := make([]tools.AgentInfo, 0, len(ids))
		for _, id := range ids {
			agent, ok := registry.GetAgent(id)
			if !ok || agent == nil {
				continue
			}
			out = append(out, tools.AgentInfo{
				ID:        strings.TrimSpace(agent.ID),
				Name:      strings.TrimSpace(agent.Name),
				Model:     strings.TrimSpace(agent.Model),
				Workspace: strings.TrimSpace(agent.Workspace),
			})
		}
		return out
	}
	lookupAgent := func(agentID string) (tools.AgentInfo, bool) {
		agent, ok := registry.GetAgent(agentID)
		if !ok || agent == nil {
			return tools.AgentInfo{}, false
		}
		return tools.AgentInfo{
			ID:        strings.TrimSpace(agent.ID),
			Name:      strings.TrimSpace(agent.Name),
			Model:     strings.TrimSpace(agent.Model),
			Workspace: strings.TrimSpace(agent.Workspace),
		}, true
	}

	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok {
			continue
		}
		currentAgentID := agentID

		// Web tools
		resolveKey := func(label string, ref config.SecretRef) string {
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
		resolveKeyList := func(label string, refs []config.SecretRef) []string {
			if len(refs) == 0 {
				return nil
			}
			out := make([]string, 0, len(refs))
			for _, ref := range refs {
				v := resolveKey(label, ref)
				if v != "" {
					out = append(out, v)
				}
			}
			return out
		}
		webOpts := tools.WebSearchToolOptions{
			BraveAPIKey:          resolveKey("tools.web.brave.api_key", cfg.Tools.Web.Brave.APIKey),
			BraveAPIKeys:         resolveKeyList("tools.web.brave.api_keys", cfg.Tools.Web.Brave.APIKeys),
			BraveMaxResults:      cfg.Tools.Web.Brave.MaxResults,
			BraveEnabled:         cfg.Tools.Web.Brave.Enabled,
			TavilyAPIKey:         resolveKey("tools.web.tavily.api_key", cfg.Tools.Web.Tavily.APIKey),
			TavilyAPIKeys:        resolveKeyList("tools.web.tavily.api_keys", cfg.Tools.Web.Tavily.APIKeys),
			TavilyBaseURL:        cfg.Tools.Web.Tavily.BaseURL,
			TavilyMaxResults:     cfg.Tools.Web.Tavily.MaxResults,
			TavilyEnabled:        cfg.Tools.Web.Tavily.Enabled,
			DuckDuckGoMaxResults: cfg.Tools.Web.DuckDuckGo.MaxResults,
			DuckDuckGoEnabled:    cfg.Tools.Web.DuckDuckGo.Enabled,
			PerplexityAPIKey:     resolveKey("tools.web.perplexity.api_key", cfg.Tools.Web.Perplexity.APIKey),
			PerplexityMaxResults: cfg.Tools.Web.Perplexity.MaxResults,
			PerplexityEnabled:    cfg.Tools.Web.Perplexity.Enabled,
			GLMSearchAPIKey:      resolveKey("tools.web.glm_search.api_key", cfg.Tools.Web.GLMSearch.APIKey),
			GLMSearchBaseURL:     cfg.Tools.Web.GLMSearch.BaseURL,
			GLMSearchEngine:      cfg.Tools.Web.GLMSearch.SearchEngine,
			GLMSearchMaxResults:  cfg.Tools.Web.GLMSearch.MaxResults,
			GLMSearchEnabled:     cfg.Tools.Web.GLMSearch.Enabled,
			GrokAPIKey:           resolveKey("tools.web.grok.api_key", cfg.Tools.Web.Grok.APIKey),
			GrokAPIKeys:          resolveKeyList("tools.web.grok.api_keys", cfg.Tools.Web.Grok.APIKeys),
			GrokEndpoint:         cfg.Tools.Web.Grok.Endpoint,
			GrokModel:            cfg.Tools.Web.Grok.DefaultModel,
			GrokMaxResults:       cfg.Tools.Web.Grok.MaxResults,
			GrokEnabled:          cfg.Tools.Web.Grok.Enabled,
			Proxy:                cfg.Tools.Web.Proxy,
			EvidenceModeEnabled:  cfg.Tools.Web.Evidence.Enabled,
			EvidenceMinDomains:   cfg.Tools.Web.Evidence.MinDomains,
		}
		if cfg == nil || cfg.Tools.IsToolEnabled("web") {
			searchTool := tools.NewWebSearchTool(webOpts)
			if searchTool != nil {
				agent.Tools.Register(searchTool)
			}
			dualSearchTool := tools.NewWebSearchDualTool(webOpts)
			if dualSearchTool != nil {
				agent.Tools.Register(dualSearchTool)
			}
		}
		if cfg == nil || cfg.Tools.IsToolEnabled("web_fetch") {
			fetchTool, err := tools.NewWebFetchToolWithProxy(50000, cfg.Tools.Web.Proxy, cfg.Tools.Web.FetchLimitBytes)
			if err != nil {
				logger.ErrorCF("agent", "Failed to create web fetch tool", map[string]any{"error": err.Error()})
			} else {
				agent.Tools.Register(fetchTool)
			}
		}

		// Message tool
		if cfg == nil || cfg.Tools.IsToolEnabled("message") {
			messageTool := tools.NewMessageTool()
			messageTool.SetSendCallback(func(channel, chatID, content string) error {
				pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer pubCancel()
				return msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
					Channel: channel,
					ChatID:  chatID,
					Content: content,
				})
			})
			agent.Tools.Register(messageTool)
		}

		// Tool confirmation (Phase E2): two-phase commit gate for side-effect tools.
		confirmTTL := time.Duration(cfg.Tools.Policy.Confirm.ExpiresSeconds) * time.Second
		agent.Tools.Register(tools.NewToolConfirmTool(agent.Workspace, confirmTTL))

		// Feishu calendar tool
		if strings.TrimSpace(cfg.Channels.Feishu.AppID) != "" &&
			cfg.Channels.Feishu.AppSecret.Present() {
			agent.Tools.Register(tools.NewFeishuCalendarTool(cfg.Channels.Feishu))
		}

		// Skill discovery and installation tools
		if cfg.Tools.IsToolEnabled("skills") {
			findEnabled := cfg.Tools.IsToolEnabled("find_skills")
			installEnabled := cfg.Tools.IsToolEnabled("install_skill")
			if findEnabled || installEnabled {
				clawhubAuthToken := resolveKey("tools.skills.registries.clawhub.auth_token", cfg.Tools.Skills.Registries.ClawHub.AuthToken)
				registryMgr := skills.NewRegistryManagerFromConfig(skills.RegistryConfig{
					MaxConcurrentSearches: cfg.Tools.Skills.MaxConcurrentSearches,
					ClawHub: skills.ClawHubConfig{
						Enabled:         cfg.Tools.Skills.Registries.ClawHub.Enabled,
						BaseURL:         cfg.Tools.Skills.Registries.ClawHub.BaseURL,
						AuthToken:       clawhubAuthToken,
						SearchPath:      cfg.Tools.Skills.Registries.ClawHub.SearchPath,
						SkillsPath:      cfg.Tools.Skills.Registries.ClawHub.SkillsPath,
						DownloadPath:    cfg.Tools.Skills.Registries.ClawHub.DownloadPath,
						Timeout:         cfg.Tools.Skills.Registries.ClawHub.Timeout,
						MaxZipSize:      cfg.Tools.Skills.Registries.ClawHub.MaxZipSize,
						MaxResponseSize: cfg.Tools.Skills.Registries.ClawHub.MaxResponseSize,
					},
				})

				if findEnabled {
					searchCache := skills.NewSearchCache(
						cfg.Tools.Skills.SearchCache.MaxSize,
						time.Duration(cfg.Tools.Skills.SearchCache.TTLSeconds)*time.Second,
					)
					agent.Tools.Register(tools.NewFindSkillsTool(registryMgr, searchCache))
				}
				if installEnabled {
					agent.Tools.Register(tools.NewInstallSkillTool(registryMgr, agent.Workspace))
				}
			}
		}

		// Phase F: agent discovery + explicit handoff.
		agent.Tools.Register(tools.NewAgentsListTool(listAgents))
		handoffTool := tools.NewHandoffTool(agent.ID, agent.Sessions, lookupAgent)
		parentSubagents := agent.Subagents
		handoffTool.SetAllowlistChecker(func(_ string, targetAgentID string) bool {
			// Default allow: if allow_agents is omitted (nil), allow handoff to any existing agent.
			// Explicit empty allow_agents [] means "disallow all".
			if parentSubagents == nil || parentSubagents.AllowAgents == nil {
				return true
			}
			if len(parentSubagents.AllowAgents) == 0 {
				return false
			}
			return registry.CanSpawnSubagent(currentAgentID, targetAgentID)
		})
		agent.Tools.Register(handoffTool)

		// Spawn/session tools with allowlist checker.
		if cfg.Tools.IsToolEnabled("spawn") {
			if cfg.Tools.IsToolEnabled("subagent") {
				subagentManager := tools.NewSubagentManager(provider, agent.Model, agent.Workspace, msgBus)
				subagentManager.SetLLMOptions(agent.MaxTokens, agent.Temperature)
				subagentManager.SetLimits(
					cfg.Orchestration.MaxParallelWorkers,
					cfg.Orchestration.MaxTasksPerAgent,
					cfg.Orchestration.MaxSpawnDepth,
				)
				subagentManager.SetToolCallParallelism(
					cfg.Orchestration.ToolCallsParallelEnabled,
					cfg.Orchestration.MaxToolCallConcurrency,
					cfg.Orchestration.ParallelToolsMode,
					cfg.Orchestration.ToolParallelOverrides,
				)
				subagentManager.SetToolExecutionPolicy(cfg.Tools.Policy, cfg.Tools.Policy.Audit.Tags)
				subagentManager.SetToolExecutionTracing(
					tools.ToolTraceOptions{
						Enabled:               cfg.Tools.Trace.Enabled,
						Dir:                   cfg.Tools.Trace.Dir,
						WritePerCallFiles:     cfg.Tools.Trace.WritePerCallFiles,
						MaxArgPreviewChars:    cfg.Tools.Trace.MaxArgPreviewChars,
						MaxResultPreviewChars: cfg.Tools.Trace.MaxResultPreviewChars,
					},
					tools.ToolErrorTemplateOptions{
						Enabled:               cfg.Tools.ErrorTemplate.Enabled,
						IncludeSchema:         cfg.Tools.ErrorTemplate.IncludeSchema,
						IncludeAvailableTools: true,
					},
				)
				subagentManager.SetToolHooks(tools.BuildDefaultToolHooks(cfg))
				subagentManager.SetResourceBudgets(cfg.Limits)
				subagentManager.SetTools(agent.Tools)
				agent.SubagentManager = subagentManager
				subagentManager.SetExecutionResolver(func(targetAgentID string) (tools.SubagentExecutionConfig, error) {
					return resolveSubagentExecution(cfg, registry, provider, currentAgentID, targetAgentID)
				})
				if taskLedger != nil {
					subagentManager.SetEventHandler(func(event tools.SubagentTaskEvent) {
						handleSubagentTaskEvent(taskLedger, cfg, event)
					})
				}
				spawnTool := tools.NewSpawnTool(subagentManager)
				sessionsSpawnTool := tools.NewSessionsSpawnTool(subagentManager)
				allowlist := func(targetAgentID string) bool {
					return registry.CanSpawnSubagent(currentAgentID, targetAgentID)
				}
				spawnTool.SetAllowlistChecker(allowlist)
				sessionsSpawnTool.SetAllowlistChecker(allowlist)
				agent.Tools.Register(spawnTool)
				agent.Tools.Register(sessionsSpawnTool)

				if sessionsExecutor != nil {
					agent.Tools.Register(tools.NewSessionsSendTool(sessionsExecutor))
				} else {
					logger.WarnCF("agent", "sessions_send tool disabled: executor unavailable", map[string]any{
						"agent_id": currentAgentID,
					})
				}
			} else {
				logger.WarnCF("agent", "spawn tool requires subagent to be enabled", map[string]any{
					"agent_id": currentAgentID,
				})
			}
		} else {
			// Note: sessions_send/sessions_spawn are part of the spawn/subagent subsystem.
			// Disabling spawn keeps the tool surface smaller.
		}

		// Update context builder with the complete tools registry
		agent.ContextBuilder.SetToolsRegistry(agent.Tools)
	}
}

func resolveSubagentExecution(
	cfg *config.Config,
	registry *AgentRegistry,
	fallbackProvider providers.LLMProvider,
	parentAgentID, targetAgentID string,
) (tools.SubagentExecutionConfig, error) {
	selectedAgentID := parentAgentID
	if strings.TrimSpace(targetAgentID) != "" {
		selectedAgentID = targetAgentID
	}

	targetAgent, ok := registry.GetAgent(selectedAgentID)
	if !ok || targetAgent == nil {
		return tools.SubagentExecutionConfig{}, fmt.Errorf("target agent %q not found", selectedAgentID)
	}

	execution := tools.SubagentExecutionConfig{
		Provider: fallbackProvider,
		Model:    targetAgent.Model,
		Tools:    targetAgent.Tools,
	}

	modelCfg, err := cfg.GetModelConfig(targetAgent.Model)
	if err != nil {
		if execution.Provider != nil {
			return execution, nil
		}
		return tools.SubagentExecutionConfig{}, err
	}

	cfgCopy := *modelCfg
	if cfgCopy.Workspace == "" {
		cfgCopy.Workspace = targetAgent.Workspace
	}

	resolvedProvider, resolvedModel, err := providers.CreateProviderFromConfig(&cfgCopy)
	if err != nil {
		if execution.Provider != nil {
			return execution, nil
		}
		return tools.SubagentExecutionConfig{}, err
	}
	if resolvedProvider != nil {
		execution.Provider = resolvedProvider
	}
	if resolvedModel != "" {
		execution.Model = resolvedModel
	}
	return execution, nil
}

func handleSubagentTaskEvent(ledger *tools.TaskLedger, cfg *config.Config, event tools.SubagentTaskEvent) {
	if ledger == nil {
		return
	}
	task := event.Task
	status := tools.TaskStatus(task.Status)
	if status == "" {
		status = tools.TaskStatusPlanned
	}

	var deadline *int64
	if cfg != nil && cfg.Orchestration.DefaultTaskTimeoutSeconds > 0 {
		d := task.Created + int64(cfg.Orchestration.DefaultTaskTimeoutSeconds)*1000
		deadline = &d
	}

	_ = ledger.UpsertTask(tools.TaskLedgerEntry{
		ID:            task.ID,
		ParentTaskID:  task.ParentTaskID,
		AgentID:       task.AgentID,
		Source:        "spawn",
		Intent:        task.Task,
		OriginChannel: task.OriginChannel,
		OriginChatID:  task.OriginChatID,
		Status:        status,
		CreatedAtMS:   task.Created,
		DeadlineAtMS:  deadline,
		Result:        task.Result,
		Error:         event.Err,
	})

	for _, tr := range event.Trace {
		_ = ledger.AddEvidence(task.ID, tools.TaskEvidence{
			TimestampMS:   event.Timestamp,
			Iteration:     tr.Iteration,
			ToolName:      tr.ToolName,
			Arguments:     tr.Arguments,
			ResultPreview: utils.Truncate(tr.Result, 400),
			IsError:       tr.IsError,
			DurationMS:    tr.DurationMS,
		})
	}
}

func (al *AgentLoop) Run(ctx context.Context) error {
	al.running.Store(true)
	cfg := al.Config()
	if cfg != nil && cfg.Audit.Enabled {
		go al.runAuditLoop(ctx)
	}

	// Ensure MCP connections are cleaned up on exit, regardless of initialization success.
	if al != nil && al.mcpMgr != nil {
		defer func() {
			if err := al.mcpMgr.Close(); err != nil {
				logger.ErrorCF("agent", "Failed to close MCP manager", map[string]any{"error": err.Error()})
			}
		}()
	}

	// Best-effort: ensure MCP tools are registered on startup (and connections
	// are established if enabled). This is safe to call multiple times.
	if cfg != nil && cfg.Tools.MCP.Enabled {
		al.ReloadMCPTools(ctx)
	}

	queueEnabled := true
	maxConc := 1
	perSessionBuf := 32
	if cfg != nil {
		queueEnabled = cfg.Gateway.InboundQueue.Enabled
		maxConc = cfg.Gateway.InboundQueue.MaxConcurrency
		perSessionBuf = cfg.Gateway.InboundQueue.PerSessionBuffer
	}
	if maxConc <= 0 {
		maxConc = 1
	}
	if perSessionBuf <= 0 {
		perSessionBuf = 32
	}

	processOne := func(msg bus.InboundMessage, steering <-chan bus.InboundMessage) {
		roundTracker := &tools.MessageRoundTracker{}
		msgCtx := tools.WithMessageRoundTracker(ctx, roundTracker)
		msgCtx = withSteeringInbox(msgCtx, steering)

		response, err := al.processMessage(msgCtx, msg)
		if err != nil {
			response = fmt.Sprintf("Error processing message: %v", err)
		}

		if response == "" {
			return
		}

		if roundTracker.Sent() {
			logger.DebugCF("agent", "Skipped outbound (message tool already sent)", map[string]any{
				"channel": msg.Channel,
			})
			return
		}

		_ = al.bus.PublishOutbound(msgCtx, bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: response,
		})
		logger.InfoCF("agent", "Published outbound response",
			map[string]any{
				"channel":     msg.Channel,
				"chat_id":     msg.ChatID,
				"content_len": len(response),
			})
	}

	if !queueEnabled {
		for al.running.Load() {
			msg, ok := al.bus.ConsumeInbound(ctx)
			if !ok {
				return nil
			}
			processOne(msg, nil)
		}
		return nil
	}

	type bucket struct {
		ch     chan bus.InboundMessage
		steer  chan bus.InboundMessage
		active atomic.Bool
	}

	globalSem := make(chan struct{}, maxConc)
	buckets := make(map[string]*bucket)
	var bucketsMu sync.Mutex

	getBucketKey := func(msg bus.InboundMessage) string {
		// Prefer an explicit session key when it is known to be safe/stable.
		explicit := utils.CanonicalSessionKey(msg.SessionKey)
		if explicit != "" {
			if strings.HasPrefix(explicit, "agent:") || strings.HasPrefix(explicit, "conv:") || constants.IsInternalChannel(msg.Channel) {
				return explicit
			}
		}

		cfg := al.Config()
		dmScope := routing.DMScopeMain
		identityLinks := map[string][]string(nil)
		if cfg != nil {
			if v := routing.DMScope(strings.TrimSpace(cfg.Session.DMScope)); v != "" {
				dmScope = v
			}
			identityLinks = cfg.Session.IdentityLinks
		}

		// System messages (subagent completion) route back to the originating conversation.
		// ChatID format: "origin_channel:origin_chat_id".
		if msg.Channel == "system" {
			originChannel, originChatID := "cli", strings.TrimSpace(msg.ChatID)
			if idx := strings.Index(msg.ChatID, ":"); idx > 0 {
				originChannel = strings.TrimSpace(msg.ChatID[:idx])
				originChatID = strings.TrimSpace(msg.ChatID[idx+1:])
			}
			key := utils.CanonicalSessionKey(routing.BuildConversationPeerSessionKey(routing.SessionKeyParams{
				Channel:       originChannel,
				AccountID:     msg.Metadata["account_id"],
				Peer:          &routing.RoutePeer{Kind: "direct", ID: originChatID},
				ThreadID:      msg.Metadata["thread_id"],
				DMScope:       dmScope,
				IdentityLinks: identityLinks,
			}))
			if key != "" {
				return key
			}
		}

		key := utils.CanonicalSessionKey(routing.BuildConversationPeerSessionKey(routing.SessionKeyParams{
			Channel:       msg.Channel,
			AccountID:     msg.Metadata["account_id"],
			Peer:          extractPeer(msg),
			ThreadID:      msg.Metadata["thread_id"],
			DMScope:       dmScope,
			IdentityLinks: identityLinks,
		}))
		if key == "" {
			key = utils.CanonicalSessionKey(strings.TrimSpace(msg.Channel) + ":" + strings.TrimSpace(msg.ChatID))
		}
		return key
	}

	enqueue := func(msg bus.InboundMessage) {
		key := getBucketKey(msg)

		bucketsMu.Lock()
		b := buckets[key]
		if b == nil {
			b = &bucket{
				ch:    make(chan bus.InboundMessage, perSessionBuf),
				steer: make(chan bus.InboundMessage, 16),
			}
			buckets[key] = b

			// One worker per session key: strict in-order processing within the session.
			go func(key string, b *bucket) {
				for {
					select {
					case <-ctx.Done():
						return
					case msg := <-b.ch:
						// Drop any steering messages that arrived after the last run completed.
						for {
							select {
							case <-b.steer:
								// discard
							default:
								goto drained
							}
						}
					drained:

						b.active.Store(true)
						globalSem <- struct{}{}
						func() {
							defer func() {
								<-globalSem
								b.active.Store(false)
							}()
							processOne(msg, b.steer)
						}()
					}
				}
			}(key, b)
		}
		bucketsMu.Unlock()

		// Steering: while a session is actively running, allow out-of-band "/steer ..."
		// messages to be injected into the current run rather than queued behind it.
		if b != nil && b.active.Load() {
			if body, ok := extractSteeringContent(msg.Content); ok {
				steerMsg := msg
				steerMsg.Content = body
				select {
				case b.steer <- steerMsg:
					return
				default:
					// If the steering inbox is full, fall back to normal enqueue to avoid losing input.
				}
			}
		}

		// Backpressure: if this session queue is full, we block here. This keeps
		// strict ordering and prevents unbounded memory growth.
		b.ch <- msg
	}

	for al.running.Load() {
		msg, ok := al.bus.ConsumeInbound(ctx)
		if !ok {
			return nil
		}
		enqueue(msg)
	}

	return nil
}

func (al *AgentLoop) Stop() {
	al.running.Store(false)
}

func (al *AgentLoop) RegisterTool(tool tools.Tool) {
	for _, agentID := range al.registry.ListAgentIDs() {
		if agent, ok := al.registry.GetAgent(agentID); ok {
			agent.Tools.Register(tool)
		}
	}
}

func (al *AgentLoop) SetChannelManager(dir ChannelDirectory) {
	al.channelDirectory = dir
}

// SetMediaResolver injects a media resolver for media:// lifecycle lookups.
func (al *AgentLoop) SetMediaResolver(r MediaResolver) {
	al.mediaResolver = r
}

func (al *AgentLoop) GetTaskLedger() *tools.TaskLedger {
	return al.taskLedger
}

// SetTranscriber injects a voice transcriber for agent-level audio transcription.
func (al *AgentLoop) SetTranscriber(t voice.Transcriber) {
	al.transcriber = t
}

var audioAnnotationRe = regexp.MustCompile(`\[(voice|audio)(?::[^\]]*)?\]`)

// transcribeAudioInMessage resolves audio media refs, transcribes them, and
// replaces audio annotations in msg.Content with the transcribed text.
func (al *AgentLoop) transcribeAudioInMessage(ctx context.Context, msg bus.InboundMessage) bus.InboundMessage {
	if al == nil || al.transcriber == nil || al.mediaResolver == nil || len(msg.Media) == 0 {
		return msg
	}

	// Transcribe each audio media ref in order.
	transcriptions := make([]string, 0, len(msg.Media))
	for _, ref := range msg.Media {
		path, meta, err := al.mediaResolver.ResolveWithMeta(ref)
		if err != nil {
			logger.WarnCF("voice", "Failed to resolve media ref", map[string]any{"ref": ref, "error": err})
			continue
		}
		if !utils.IsAudioFile(meta.Filename, meta.ContentType) {
			continue
		}
		result, err := al.transcriber.Transcribe(ctx, path)
		if err != nil {
			logger.WarnCF("voice", "Transcription failed", map[string]any{"ref": ref, "error": err})
			transcriptions = append(transcriptions, "(transcription failed)")
			continue
		}
		transcriptions = append(transcriptions, strings.TrimSpace(result.Text))
	}

	if len(transcriptions) == 0 {
		return msg
	}

	// Replace audio annotations sequentially with transcriptions.
	idx := 0
	newContent := audioAnnotationRe.ReplaceAllStringFunc(msg.Content, func(match string) string {
		if idx >= len(transcriptions) {
			return match
		}
		text := transcriptions[idx]
		idx++
		if text == "" {
			return match
		}
		return "[voice: " + text + "]"
	})

	// Append any remaining transcriptions not matched by an annotation.
	for ; idx < len(transcriptions); idx++ {
		text := strings.TrimSpace(transcriptions[idx])
		if text == "" {
			continue
		}
		newContent += "\n[voice: " + text + "]"
	}

	msg.Content = newContent
	return msg
}

// inferMediaType determines the media type ("image", "audio", "video", "file")
// from a filename and MIME content type.
func inferMediaType(filename, contentType string) string {
	ct := strings.ToLower(contentType)
	fn := strings.ToLower(filename)

	if strings.HasPrefix(ct, "image/") {
		return "image"
	}
	if strings.HasPrefix(ct, "audio/") || ct == "application/ogg" {
		return "audio"
	}
	if strings.HasPrefix(ct, "video/") {
		return "video"
	}

	// Fallback: infer from extension
	ext := filepath.Ext(fn)
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg":
		return "image"
	case ".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac", ".wma", ".opus":
		return "audio"
	case ".mp4", ".avi", ".mov", ".webm", ".mkv":
		return "video"
	}

	return "file"
}

// RecordLastChannel records the last active channel for this workspace.
// This uses the atomic state save mechanism to prevent data loss on crash.
func (al *AgentLoop) RecordLastChannel(channel string) error {
	if al.state == nil {
		return nil
	}
	return al.state.SetLastChannel(channel)
}

// RecordLastChatID records the last active chat ID for this workspace.
// This uses the atomic state save mechanism to prevent data loss on crash.
func (al *AgentLoop) RecordLastChatID(chatID string) error {
	if al.state == nil {
		return nil
	}
	return al.state.SetLastChatID(chatID)
}

// LastActive returns the most recently used channel and chat ID for this workspace.
// It is backed by the persisted state file (state/state.json), but uses the in-memory
// state manager instance so it stays up-to-date during a running gateway process.
func (al *AgentLoop) LastActive() (string, string) {
	if al == nil || al.state == nil {
		return "", ""
	}
	key := strings.TrimSpace(al.state.GetLastChannel())
	if key == "" {
		return "", ""
	}

	parts := strings.SplitN(key, ":", 2)
	if len(parts) != 2 {
		return "", ""
	}
	channel := strings.TrimSpace(parts[0])
	chatID := strings.TrimSpace(parts[1])
	if channel == "" || chatID == "" {
		return "", ""
	}
	return channel, chatID
}

func (al *AgentLoop) ProcessDirect(
	ctx context.Context,
	content, sessionKey string,
) (string, error) {
	return al.ProcessDirectWithChannel(ctx, content, sessionKey, "cli", "direct")
}

func (al *AgentLoop) ProcessDirectWithChannel(
	ctx context.Context,
	content, sessionKey, channel, chatID string,
) (string, error) {
	msg := bus.InboundMessage{
		Channel:    channel,
		SenderID:   "cron",
		ChatID:     chatID,
		Content:    content,
		SessionKey: sessionKey,
	}

	return al.processMessage(ctx, msg)
}

// ProcessSessionMessage injects a message into a specific session key directly.
// Unlike ProcessDirectWithChannel, this bypasses route-derived session rewriting.
func (al *AgentLoop) ProcessSessionMessage(
	ctx context.Context,
	content, sessionKey, channel, chatID string,
) (string, error) {
	key := utils.CanonicalSessionKey(sessionKey)
	if key == "" {
		return "", fmt.Errorf("sessionKey is required")
	}

	targetAgent := al.registry.GetDefaultAgent()
	if parsed := routing.ParseAgentSessionKey(key); parsed != nil {
		if agent, ok := al.registry.GetAgent(parsed.AgentID); ok {
			targetAgent = agent
		}
	} else if al.sessions != nil {
		if active := al.sessions.GetActiveAgentID(key); active != "" {
			if agent, ok := al.registry.GetAgent(active); ok {
				targetAgent = agent
			}
		}
	}
	if targetAgent == nil {
		return "", fmt.Errorf("no agent available for session %q", key)
	}

	if strings.TrimSpace(channel) == "" {
		channel = "system"
	}
	if strings.TrimSpace(chatID) == "" {
		chatID = "sessions-send"
	}

	return al.runAgentLoop(ctx, targetAgent, processOptions{
		SessionKey:      key,
		Channel:         channel,
		ChatID:          chatID,
		UserMessage:     content,
		DefaultResponse: "I've completed processing but have no response to give.",
		EnableSummary:   true,
		SendResponse:    false,
	})
}

// ProcessHeartbeat processes a heartbeat request without session history.
// Each heartbeat is independent and doesn't accumulate context.
func (al *AgentLoop) ProcessHeartbeat(
	ctx context.Context,
	content, channel, chatID string,
) (string, error) {
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		return "", fmt.Errorf("no default agent for heartbeat")
	}
	return al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:      "heartbeat",
		Channel:         channel,
		ChatID:          chatID,
		UserMessage:     content,
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
		NoHistory:       true, // Don't load session history for heartbeat
	})
}

func (al *AgentLoop) processMessage(ctx context.Context, msg bus.InboundMessage) (string, error) {
	// Add message preview to log (show full content for error messages)
	var logContent string
	if strings.Contains(msg.Content, "Error:") || strings.Contains(msg.Content, "error") {
		logContent = msg.Content // Full content for errors
	} else {
		logContent = utils.Truncate(msg.Content, 80)
	}
	logger.InfoCF(
		"agent",
		fmt.Sprintf("Processing message from %s:%s: %s", msg.Channel, msg.SenderID, logContent),
		map[string]any{
			"channel":     msg.Channel,
			"chat_id":     msg.ChatID,
			"sender_id":   msg.SenderID,
			"session_key": msg.SessionKey,
		},
	)

	// Route system messages to processSystemMessage
	if msg.Channel == "system" {
		return al.processSystemMessage(ctx, msg)
	}

	cfg := al.Config()

	// Best-effort: if a voice transcriber is configured, turn audio attachments into
	// text before routing/command handling.
	msg = al.transcribeAudioInMessage(ctx, msg)

	// Route to determine default agent for this peer/channel.
	route := al.registry.ResolveRoute(routing.RouteInput{
		Channel:    msg.Channel,
		AccountID:  msg.Metadata["account_id"],
		Peer:       extractPeer(msg),
		ParentPeer: extractParentPeer(msg),
		ThreadID:   msg.Metadata["thread_id"],
		GuildID:    msg.Metadata["guild_id"],
		TeamID:     msg.Metadata["team_id"],
	})

	// Build the conversation session key (agent-independent) so that handoffs can keep
	// one shared conversation history across agents (Phase F).
	dmScope := routing.DMScopeMain
	identityLinks := map[string][]string(nil)
	if cfg != nil {
		if v := routing.DMScope(strings.TrimSpace(cfg.Session.DMScope)); v != "" {
			dmScope = v
		}
		identityLinks = cfg.Session.IdentityLinks
	}
	conversationSessionKey := utils.CanonicalSessionKey(routing.BuildConversationPeerSessionKey(routing.SessionKeyParams{
		Channel:       msg.Channel,
		AccountID:     msg.Metadata["account_id"],
		Peer:          extractPeer(msg),
		ThreadID:      msg.Metadata["thread_id"],
		DMScope:       dmScope,
		IdentityLinks: identityLinks,
	}))
	if conversationSessionKey == "" {
		conversationSessionKey = utils.CanonicalSessionKey(strings.TrimSpace(msg.Channel) + ":" + strings.TrimSpace(msg.ChatID))
	}

	// Use explicit session keys only when they are known to be safe/stable.
	// - agent:* forces an agent (internal/control plane)
	// - conv:* forces a specific conversation session (internal/control plane)
	// - internal channels may inject arbitrary session keys for testing/ops
	sessionKey := conversationSessionKey
	if explicit := utils.CanonicalSessionKey(msg.SessionKey); explicit != "" {
		if strings.HasPrefix(explicit, "agent:") || strings.HasPrefix(explicit, "conv:") || constants.IsInternalChannel(msg.Channel) {
			sessionKey = explicit
		}
	}

	// Determine the active agent:
	// 1) Agent-scoped keys force the agent. Otherwise, prefer the session's active agent.
	// 2) Fall back to routed agent (bindings) or default agent.
	var agent *AgentInstance
	if parsed := routing.ParseAgentSessionKey(sessionKey); parsed != nil {
		if a, ok := al.registry.GetAgent(parsed.AgentID); ok {
			agent = a
		}
	} else if al.sessions != nil {
		if active := al.sessions.GetActiveAgentID(sessionKey); active != "" {
			if a, ok := al.registry.GetAgent(active); ok {
				agent = a
			}
		}
	}
	if agent == nil {
		if a, ok := al.registry.GetAgent(route.AgentID); ok {
			agent = a
		} else {
			agent = al.registry.GetDefaultAgent()
		}
	}
	if agent == nil {
		return "", fmt.Errorf("no agent available for route (agent_id=%s)", route.AgentID)
	}

	// Ensure the conversation session has an active agent recorded.
	if al.sessions != nil && routing.ParseAgentSessionKey(sessionKey) == nil {
		if al.sessions.GetActiveAgentID(sessionKey) == "" {
			al.sessions.SetActiveAgentID(sessionKey, agent.ID)
		}
	}

	// Reset message-tool state for this round so we don't skip publishing due to a previous round.
	if tool, ok := agent.Tools.Get("message"); ok {
		if resetter, ok := tool.(interface{ ResetSentInRound() }); ok {
			resetter.ResetSentInRound()
		}
	}

	// Check for commands (after routing so commands can be scoped to session/agent).
	if response, handled := al.handleCommand(ctx, msg, agent, sessionKey); handled {
		return response, nil
	}

	logger.InfoCF("agent", "Routed message",
		map[string]any{
			"agent_id":    agent.ID,
			"session_key": sessionKey,
			"conv_key":    conversationSessionKey,
			"matched_by":  route.MatchedBy,
		})

	userMessage := msg.Content
	// "/steer <msg>" behaves as a normal message when no run is active (it will be
	// treated as steering only when injected via the inbound session bucket).
	if body, ok := extractSteeringContent(userMessage); ok {
		userMessage = body
	}
	if note := al.importInboundMediaAndBuildNote(agent, msg); note != "" {
		if strings.TrimSpace(userMessage) != "" {
			userMessage += "\n\n" + note
		} else {
			userMessage = note
		}
	}

	planMode := false
	if cfg != nil && cfg.Tools.PlanMode.Enabled {
		defaultMode := sessionPermissionModeRun
		modeText := strings.TrimSpace(cfg.Tools.PlanMode.DefaultMode)
		if strings.EqualFold(strings.TrimSpace(msg.Peer.Kind), "group") && strings.TrimSpace(cfg.Tools.PlanMode.DefaultModeGroup) != "" {
			modeText = strings.TrimSpace(cfg.Tools.PlanMode.DefaultModeGroup)
		}
		if strings.EqualFold(strings.TrimSpace(modeText), "plan") {
			defaultMode = sessionPermissionModePlan
		}
		permWorkspace := agent.Workspace
		if da := al.registry.GetDefaultAgent(); da != nil && strings.TrimSpace(da.Workspace) != "" {
			permWorkspace = da.Workspace
		}
		perm := loadSessionPermissionStateWithDefault(permWorkspace, sessionKey, defaultMode)
		if perm.isPlan() {
			planMode = true
			if strings.TrimSpace(userMessage) != "" {
				perm.PendingTask = userMessage
				if err := saveSessionPermissionState(permWorkspace, sessionKey, perm); err != nil {
					logger.WarnCF("agent", "Failed to persist plan-mode pending task (best-effort)", map[string]any{
						"session_key": sessionKey,
						"error":       err.Error(),
					})
				}
			}
		}
	}

	return al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:      sessionKey,
		Channel:         msg.Channel,
		ChatID:          msg.ChatID,
		SenderID:        msg.SenderID,
		UserMessage:     userMessage,
		Media:           msg.Media,
		DefaultResponse: defaultResponse,
		EnableSummary:   true,
		SendResponse:    false,
		Steering:        steeringInboxFromContext(ctx),
		PlanMode:        planMode,
	})
}

func (al *AgentLoop) processSystemMessage(
	ctx context.Context,
	msg bus.InboundMessage,
) (string, error) {
	if msg.Channel != "system" {
		return "", fmt.Errorf(
			"processSystemMessage called with non-system message channel: %s",
			msg.Channel,
		)
	}

	logger.InfoCF("agent", "Processing system message",
		map[string]any{
			"sender_id": msg.SenderID,
			"chat_id":   msg.ChatID,
		})

	// Parse origin channel from chat_id (format: "channel:chat_id")
	var originChannel, originChatID string
	if idx := strings.Index(msg.ChatID, ":"); idx > 0 {
		originChannel = msg.ChatID[:idx]
		originChatID = msg.ChatID[idx+1:]
	} else {
		originChannel = "cli"
		originChatID = msg.ChatID
	}

	// Extract subagent result from message content
	// Format: "Task 'label' completed.\n\nResult:\n<actual content>"
	content := msg.Content
	if idx := strings.Index(content, "Result:\n"); idx >= 0 {
		content = content[idx+8:] // Extract just the result part
	}

	// Skip internal channels - only log, don't send to user
	if constants.IsInternalChannel(originChannel) {
		logger.InfoCF("agent", "Subagent completed (internal channel)",
			map[string]any{
				"sender_id":   msg.SenderID,
				"content_len": len(content),
				"channel":     originChannel,
			})
		return "", nil
	}

	// Prefer an explicit session key (propagated from tools such as spawn/subagent).
	sessionKey := utils.CanonicalSessionKey(msg.SessionKey)
	if sessionKey == "" {
		cfg := al.Config()
		dmScope := routing.DMScopeMain
		identityLinks := map[string][]string(nil)
		if cfg != nil {
			if v := routing.DMScope(strings.TrimSpace(cfg.Session.DMScope)); v != "" {
				dmScope = v
			}
			identityLinks = cfg.Session.IdentityLinks
		}
		sessionKey = utils.CanonicalSessionKey(routing.BuildConversationPeerSessionKey(routing.SessionKeyParams{
			Channel:       originChannel,
			AccountID:     msg.Metadata["account_id"],
			Peer:          &routing.RoutePeer{Kind: "direct", ID: originChatID},
			DMScope:       dmScope,
			IdentityLinks: identityLinks,
		}))
	}

	// Use the session's active agent if present; otherwise default agent.
	agent := al.registry.GetDefaultAgent()
	if parsed := routing.ParseAgentSessionKey(sessionKey); parsed != nil {
		if a, ok := al.registry.GetAgent(parsed.AgentID); ok && a != nil {
			agent = a
		}
	} else if al.sessions != nil {
		if active := al.sessions.GetActiveAgentID(sessionKey); active != "" {
			if a, ok := al.registry.GetAgent(active); ok && a != nil {
				agent = a
			}
		}
	}
	if agent == nil {
		return "", fmt.Errorf("no agent available for system message (session_key=%s)", sessionKey)
	}
	if al.sessions != nil && routing.ParseAgentSessionKey(sessionKey) == nil {
		if al.sessions.GetActiveAgentID(sessionKey) == "" {
			al.sessions.SetActiveAgentID(sessionKey, agent.ID)
		}
	}

	return al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:      sessionKey,
		Channel:         originChannel,
		ChatID:          originChatID,
		SenderID:        msg.SenderID,
		UserMessage:     fmt.Sprintf("[System: %s] %s", msg.SenderID, msg.Content),
		DefaultResponse: "Background task completed.",
		EnableSummary:   false,
		SendResponse:    true,
	})
}

// runAgentLoop is the core message processing logic.
func (al *AgentLoop) runAgentLoop(
	ctx context.Context,
	agent *AgentInstance,
	opts processOptions,
) (string, error) {
	cfg := al.Config()
	// 0. Record last channel for heartbeat notifications (skip internal channels)
	if opts.Channel != "" && opts.ChatID != "" {
		// Don't record internal channels (cli, system, subagent)
		if !constants.IsInternalChannel(opts.Channel) {
			channelKey := fmt.Sprintf("%s:%s", opts.Channel, opts.ChatID)
			if err := al.RecordLastChannel(channelKey); err != nil {
				logger.WarnCF(
					"agent",
					"Failed to record last channel",
					map[string]any{"error": err.Error()},
				)
			}
		}
	}

	// 1. Build messages (skip history for heartbeat)
	var history []providers.Message
	var summary string
	if !opts.NoHistory {
		history = agent.Sessions.GetHistory(opts.SessionKey)
		summary = agent.Sessions.GetSummary(opts.SessionKey)
	}

	// Per-run working state (must NOT be stored globally on ContextBuilder).
	ws := NewWorkingState(opts.UserMessage)
	opts.WorkingState = ws

	llmUserMessage := opts.UserMessage
	if opts.PlanMode {
		restricted := "exec/edit_file/write_file/append_file"
		if cfg != nil && len(cfg.Tools.PlanMode.RestrictedTools) > 0 {
			restricted = strings.Join(cfg.Tools.PlanMode.RestrictedTools, ", ")
		}
		llmUserMessage = fmt.Sprintf(
			"[PLAN MODE]\nYou are currently in PLAN mode for this session.\n"+
				"- You MUST NOT call restricted tools (%s).\n"+
				"- Draft a plan, ask the user to approve execution (/approve or /run), then stop.\n\n"+
				"USER REQUEST:\n%s",
			restricted,
			opts.UserMessage,
		)
	}
	messages := agent.ContextBuilder.BuildMessagesForSession(
		opts.SessionKey,
		history,
		summary,
		llmUserMessage,
		opts.Media,
		opts.Channel,
		opts.ChatID,
		ws,
	)

	// Resolve media:// refs to base64 data URLs (streaming)
	maxMediaSize := config.DefaultMaxMediaSize
	if cfg != nil {
		maxMediaSize = cfg.Agents.Defaults.GetMaxMediaSize()
	}
	messages = resolveMediaRefs(messages, al.mediaResolver, maxMediaSize)

	// Phase E1: durable run checkpoint (append-only JSONL event stream).
	runTraceEnabled := cfg != nil && cfg.Tools.Trace.Enabled
	modelForRun := strings.TrimSpace(agent.Model)
	if agent.Sessions != nil {
		if override, ok := agent.Sessions.EffectiveModelOverride(opts.SessionKey); ok {
			modelForRun = override
		}
	}
	runTrace := newRunTraceWriter(agent.Workspace, runTraceEnabled, opts, agent.ID, modelForRun)
	if runTrace != nil {
		if opts.Resume {
			runTrace.recordResume(opts.UserMessage, len(messages), len(agent.Tools.List()))
		} else {
			runTrace.recordStart(opts.UserMessage, len(messages), len(agent.Tools.List()))
		}
	}

	// Save user message to session (WAL before entering the LLM/tool loop).
	agent.Sessions.AddMessage(opts.SessionKey, "user", opts.UserMessage)
	// WAL: persist user message before entering the LLM/tool loop to avoid losing
	// inbound user input on crash/restart.
	if err := agent.Sessions.Save(opts.SessionKey); err != nil {
		// WAL failures should not block the conversation; warn and continue.
		logger.WarnCF("agent", "Failed to WAL user message (best-effort)", map[string]any{
			"session_key": opts.SessionKey,
			"error":       err.Error(),
		})
	}

	// 5. Run LLM iteration loop (may switch active agent via handoff).
	finalContent, iteration, activeAgent, err := al.runLLMIteration(ctx, agent, messages, opts, runTrace, modelForRun)
	if err != nil {
		if runTrace != nil {
			runTrace.recordError(iteration, err)
		}
		return "", err
	}
	if activeAgent != nil {
		agent = activeAgent
	}

	// If last tool had ForUser content and we already sent it, we might not need to send final response
	// This is controlled by the tool's Silent flag and ForUser content

	// 5. Handle empty response
	if finalContent == "" {
		finalContent = opts.DefaultResponse
	}

	// 6. Save final assistant message to session
	agent.Sessions.AddMessage(opts.SessionKey, "assistant", finalContent)
	agent.Sessions.Save(opts.SessionKey)

	// 7. Optional: summarization
	if opts.EnableSummary {
		al.maybeSummarize(agent, opts.SessionKey, opts.Channel, opts.ChatID)
	}

	// 8. Optional: send response via bus
	if opts.SendResponse {
		al.bus.PublishOutbound(ctx, bus.OutboundMessage{
			Channel: opts.Channel,
			ChatID:  opts.ChatID,
			Content: finalContent,
		})
	}

	// 9. Log response
	responsePreview := utils.Truncate(finalContent, 120)
	logger.InfoCF("agent", fmt.Sprintf("Response: %s", responsePreview),
		map[string]any{
			"agent_id":     agent.ID,
			"session_key":  opts.SessionKey,
			"iterations":   iteration,
			"final_length": len(finalContent),
		})

	if runTrace != nil {
		runTrace.recordEnd(iteration, finalContent)
	}

	// Optional notification hook (ROADMAP.md:1226): when a run completes in an
	// internal channel (system/cli/subagent), notify the last active external chat.
	if cfg != nil && cfg.Notify.OnTaskComplete && constants.IsInternalChannel(opts.Channel) {
		trimmedResult := strings.TrimSpace(finalContent)
		// Quiet-by-default: allow background tasks (cron/heartbeat/etc.) to opt out
		// by returning a deterministic sentinel.
		if trimmedResult == "" ||
			strings.EqualFold(trimmedResult, "NO_UPDATE") ||
			strings.EqualFold(trimmedResult, "HEARTBEAT_OK") {
			return finalContent, nil
		}

		targetCh, targetChat := al.LastActive()
		if strings.TrimSpace(targetCh) != "" && strings.TrimSpace(targetChat) != "" && !constants.IsInternalChannel(targetCh) {
			notifyText := fmt.Sprintf(
				"✅ Task complete\n\nTask:\n%s\n\nResult:\n%s",
				utils.Truncate(strings.TrimSpace(opts.UserMessage), 240),
				utils.Truncate(strings.TrimSpace(finalContent), 1200),
			)
			if tool, ok := agent.Tools.Get("message"); ok && tool != nil {
				_ = tool.Execute(ctx, map[string]any{
					"content": notifyText,
					"channel": targetCh,
					"chat_id": targetChat,
				})
			} else if al.bus != nil {
				_ = al.bus.PublishOutbound(ctx, bus.OutboundMessage{
					Channel: targetCh,
					ChatID:  targetChat,
					Content: notifyText,
				})
			}
		}
	}

	return finalContent, nil
}

// runLLMIteration executes the LLM call loop with tool handling.
// toolCallSignature captures a tool call for loop detection.
type toolCallSignature struct {
	Name string
	Args string
}

// detectToolCallLoop checks if any of the current tool calls have been
// repeated with identical arguments more than the threshold within the
// recent history. Returns the name of the looping tool, or "" if none.
func detectToolCallLoop(recent []toolCallSignature, current []providers.ToolCall, threshold int) string {
	for _, tc := range current {
		argsJSON, _ := json.Marshal(tc.Arguments)
		sig := string(argsJSON)
		count := 0
		for _, prev := range recent {
			if prev.Name == tc.Name && prev.Args == sig {
				count++
			}
		}
		if count >= threshold {
			return tc.Name
		}
	}
	return ""
}

func (al *AgentLoop) runLLMIteration(
	ctx context.Context,
	agent *AgentInstance,
	messages []providers.Message,
	opts processOptions,
	trace *runTraceWriter,
	modelForRun string,
) (string, int, *AgentInstance, error) {
	iteration := 0
	var finalContent string
	recentToolCalls := make([]toolCallSignature, 0, 32) // for loop detection
	totalPromptTokens, totalCompletionTokens := 0, 0    // cumulative token tracking
	runStart := time.Now()
	toolCallsUsed := 0

	cfg := al.Config()
	limitsEnabled := cfg != nil && cfg.Limits.Enabled
	maxWallTimeSeconds := 0
	maxToolCallsPerRun := 0

	modelForRun = strings.TrimSpace(modelForRun)
	if modelForRun == "" {
		modelForRun = strings.TrimSpace(agent.Model)
	}
	maxToolResultChars := 0
	if limitsEnabled {
		maxWallTimeSeconds = cfg.Limits.MaxRunWallTimeSeconds
		maxToolCallsPerRun = cfg.Limits.MaxToolCallsPerRun
		maxToolResultChars = cfg.Limits.MaxToolResultChars
	}

	for iteration < agent.MaxIterations {
		iteration++

		logger.DebugCF("agent", "LLM iteration",
			map[string]any{
				"agent_id":  agent.ID,
				"iteration": iteration,
				"max":       agent.MaxIterations,
			})

		// Resource budgets (soft limits): stop runaway runs before they trigger OOM kills.
		if maxWallTimeSeconds > 0 && time.Since(runStart) > time.Duration(maxWallTimeSeconds)*time.Second {
			finalContent = fmt.Sprintf(
				"RESOURCE_BUDGET_EXCEEDED: run wall time exceeded (%ds). "+
					"Please narrow the task or split it into smaller steps.",
				maxWallTimeSeconds,
			)
			logger.WarnCF("agent", "Resource budget exceeded (wall time)", map[string]any{
				"agent_id":          agent.ID,
				"iteration":         iteration,
				"wall_time_seconds": int(time.Since(runStart).Seconds()),
				"tool_calls_used":   toolCallsUsed,
				"session_key":       opts.SessionKey,
			})
			break
		}

		// Build tool definitions
		providerToolDefs := agent.Tools.ToProviderDefs()
		if trace != nil {
			trace.recordLLMRequest(iteration, len(messages), len(providerToolDefs))
		}

		// Log LLM request details
		logger.DebugCF("agent", "LLM request",
			map[string]any{
				"agent_id":          agent.ID,
				"iteration":         iteration,
				"model":             modelForRun,
				"messages_count":    len(messages),
				"tools_count":       len(providerToolDefs),
				"max_tokens":        agent.MaxTokens,
				"temperature":       agent.Temperature,
				"system_prompt_len": len(messages[0].Content),
			})

		// Log full messages (detailed)
		logger.DebugCF("agent", "Full LLM request",
			map[string]any{
				"iteration":     iteration,
				"messages_json": formatMessagesForLog(messages),
				"tools_json":    formatToolsForLog(providerToolDefs),
			})

		// Call LLM with fallback chain if candidates are configured.
		var response *providers.LLMResponse
		var usedModel string
		var err error
		var lastFallbackAttempts []providers.FallbackAttempt

		llmOpts := map[string]any{
			"max_tokens":       agent.MaxTokens,
			"temperature":      agent.Temperature,
			"prompt_cache_key": agent.ID,
		}
		// parseThinkingLevel guarantees ThinkingOff for empty/unknown values,
		// so checking != ThinkingOff is sufficient.
		if agent.ThinkingLevel != "" && agent.ThinkingLevel != ThinkingOff {
			if tc, ok := agent.Provider.(providers.ThinkingCapable); ok && tc.SupportsThinking() {
				llmOpts["thinking_level"] = string(agent.ThinkingLevel)
			} else {
				logger.WarnCF(
					"agent",
					"thinking_level is set but current provider does not support it, ignoring",
					map[string]any{"agent_id": agent.ID, "thinking_level": string(agent.ThinkingLevel)},
				)
			}
		}

		callLLM := func() (*providers.LLMResponse, string, error) {
			lastFallbackAttempts = nil
			if strings.TrimSpace(agent.Model) != "" && modelForRun != strings.TrimSpace(agent.Model) {
				resp, err := agent.Provider.Chat(ctx, messages, providerToolDefs, modelForRun, llmOpts)
				return resp, modelForRun, err
			}
			if len(agent.Candidates) > 1 && al.fallback != nil {
				fbResult, fbErr := al.fallback.Execute(
					ctx,
					agent.Candidates,
					func(ctx context.Context, provider, model string) (*providers.LLMResponse, error) {
						return agent.Provider.Chat(
							ctx,
							messages,
							providerToolDefs,
							model,
							llmOpts,
						)
					},
				)
				if fbErr != nil {
					return nil, "", fbErr
				}
				if fbResult.Provider != "" && len(fbResult.Attempts) > 0 {
					logger.InfoCF(
						"agent",
						fmt.Sprintf("Fallback: succeeded with %s/%s after %d attempts",
							fbResult.Provider, fbResult.Model, len(fbResult.Attempts)+1),
						map[string]any{"agent_id": agent.ID, "iteration": iteration},
					)
				}
				lastFallbackAttempts = fbResult.Attempts
				return fbResult.Response, strings.TrimSpace(fbResult.Model), nil
			}
			resp, err := agent.Provider.Chat(ctx, messages, providerToolDefs, modelForRun, llmOpts)
			return resp, modelForRun, err
		}

		// Retry loop for context/token errors
		maxRetries := 2
		for retry := 0; retry <= maxRetries; retry++ {
			response, usedModel, err = callLLM()
			if err == nil {
				break
			}

			if isLLMTimeoutError(err) && retry < maxRetries {
				backoff := time.Duration(retry+1) * 5 * time.Second
				logger.WarnCF("agent", "Timeout error, retrying after backoff", map[string]any{
					"error":   err.Error(),
					"retry":   retry,
					"backoff": backoff.String(),
				})
				time.Sleep(backoff)
				continue
			}

			if isContextWindowError(err) && retry < maxRetries {
				logger.WarnCF("agent", "Context window error detected, attempting compression", map[string]any{
					"error": err.Error(),
					"retry": retry,
				})

				// Phase J2: context window errors can be persistent for a given model.
				// After consecutive context errors, switch to the first fallback model and
				// persist as a session override (TTL).
				if cfg != nil {
					target := pickFirstDifferentModel(modelForRun, agent.Candidates)
					runID := strings.TrimSpace(opts.RunID)
					if trace != nil {
						runID = trace.RunID()
					}
					if target != "" {
						if al.maybeAutoDowngradeSessionModel(
							agent.Workspace,
							trace,
							agent.ID,
							opts.SessionKey,
							runID,
							opts.Channel,
							opts.ChatID,
							opts.SenderID,
							iteration,
							modelForRun,
							target,
							"context_window",
							nil,
						) {
							modelForRun = target
						}
					}
				}

				if retry == 0 && !constants.IsInternalChannel(opts.Channel) {
					al.bus.PublishOutbound(ctx, bus.OutboundMessage{
						Channel: opts.Channel,
						ChatID:  opts.ChatID,
						Content: "Context window exceeded. Compressing history and retrying...",
					})
				}

				compactionCtx, cancel := al.safeCompactionContext()
				currentTokens := al.estimateTokens(agent.Sessions.GetHistory(opts.SessionKey))
				if flushed, flushErr := al.maybeFlushMemoryBeforeCompaction(
					compactionCtx,
					agent,
					opts.SessionKey,
					currentTokens,
				); flushErr != nil {
					logger.WarnCF("agent", "Pre-compaction memory flush failed", map[string]any{
						"error": flushErr.Error(),
					})
				} else if flushed {
					logger.InfoCF("agent", "Pre-compaction memory flush completed", map[string]any{
						"session_key": opts.SessionKey,
					})
				}

				compacted, compactErr := al.compactWithSafeguard(compactionCtx, agent, opts.SessionKey)
				cancel()
				if compactErr != nil {
					logger.WarnCF("agent", "Compaction safeguard cancelled", map[string]any{
						"error": compactErr.Error(),
					})
					break
				}
				if !compacted {
					logger.WarnCF("agent", "Compaction safeguard skipped; preserving history", map[string]any{
						"session_key": opts.SessionKey,
					})
					continue
				}
				newHistory := agent.Sessions.GetHistory(opts.SessionKey)
				newSummary := agent.Sessions.GetSummary(opts.SessionKey)
				messages = agent.ContextBuilder.BuildMessagesForSession(
					opts.SessionKey,
					newHistory, newSummary, "",
					nil, opts.Channel, opts.ChatID,
					opts.WorkingState,
				)
				continue
			}
			break
		}

		if err != nil {
			logger.ErrorCF("agent", "LLM call failed",
				map[string]any{
					"agent_id":  agent.ID,
					"iteration": iteration,
					"error":     err.Error(),
				})
			return "", iteration, agent, fmt.Errorf("LLM call failed after retries: %w", err)
		}

		// Phase J2: automatic session model downgrade when fallback repeatedly triggers.
		if strings.TrimSpace(usedModel) == "" {
			usedModel = modelForRun
		}
		if len(lastFallbackAttempts) == 0 && strings.EqualFold(strings.TrimSpace(usedModel), strings.TrimSpace(modelForRun)) {
			al.clearModelAutoDowngradeState(opts.SessionKey)
		}
		if len(lastFallbackAttempts) > 0 && strings.TrimSpace(usedModel) != "" && !strings.EqualFold(strings.TrimSpace(usedModel), strings.TrimSpace(modelForRun)) {
			runID := strings.TrimSpace(opts.RunID)
			if trace != nil {
				runID = trace.RunID()
			}
			if al.maybeAutoDowngradeSessionModel(
				agent.Workspace,
				trace,
				agent.ID,
				opts.SessionKey,
				runID,
				opts.Channel,
				opts.ChatID,
				opts.SenderID,
				iteration,
				modelForRun,
				usedModel,
				"fallback",
				lastFallbackAttempts,
			) {
				modelForRun = usedModel
			}
		}

		if trace != nil {
			if strings.TrimSpace(usedModel) != "" {
				trace.model = strings.TrimSpace(usedModel)
			}
			toolNames := make([]string, 0, len(response.ToolCalls))
			for _, tc := range response.ToolCalls {
				toolNames = append(toolNames, tc.Name)
			}
			sort.Strings(toolNames)
			trace.recordLLMResponse(iteration, response.Content, toolNames, response.Usage)
		}

		// Log token usage if available
		if response.Usage != nil {
			if strings.TrimSpace(usedModel) == "" {
				usedModel = modelForRun
			}

			// Persist token usage counters (best-effort, durable).
			if store := al.tokenUsageStore(agent.Workspace); store != nil {
				store.Record(usedModel, response.Usage)
			}

			logger.InfoCF("agent", "Token usage",
				map[string]any{
					"agent_id":          agent.ID,
					"iteration":         iteration,
					"model":             usedModel,
					"prompt_tokens":     response.Usage.PromptTokens,
					"completion_tokens": response.Usage.CompletionTokens,
					"total_tokens":      response.Usage.TotalTokens,
					"session_key":       opts.SessionKey,
				})
			totalPromptTokens += response.Usage.PromptTokens
			totalCompletionTokens += response.Usage.CompletionTokens
		}

		// Steering messages: if a user injects "/steer ..." while a run is still
		// executing, incorporate that message and re-run this iteration instead
		// of executing possibly-stale tool calls.
		if opts.Steering != nil {
			steeringMsgs := make([]bus.InboundMessage, 0, 4)
			for {
				select {
				case sm := <-opts.Steering:
					steeringMsgs = append(steeringMsgs, sm)
				default:
					goto steeringDrained
				}
			}
		steeringDrained:
			if len(steeringMsgs) > 0 {
				for _, sm := range steeringMsgs {
					content := strings.TrimSpace(sm.Content)
					if content == "" {
						continue
					}
					agent.Sessions.AddMessage(opts.SessionKey, "user", content)
					// Best-effort WAL for steering messages as well.
					_ = agent.Sessions.Save(opts.SessionKey)
					messages = append(messages, providers.Message{
						Role:    "user",
						Content: content,
					})
					if trace != nil {
						trace.appendEvent(runTraceEvent{
							Type: "steering.message",

							TS:   time.Now().UTC().Format(time.RFC3339Nano),
							TSMS: time.Now().UnixMilli(),

							RunID:      trace.runID,
							SessionKey: opts.SessionKey,
							Channel:    strings.TrimSpace(opts.Channel),
							ChatID:     strings.TrimSpace(opts.ChatID),
							SenderID:   strings.TrimSpace(opts.SenderID),

							AgentID: strings.TrimSpace(agent.ID),
							Model:   strings.TrimSpace(usedModel),

							Iteration: iteration,

							UserMessagePreview: utils.Truncate(content, 400),
							UserMessageChars:   len(content),
						})
					}
				}
				continue
			}
		}

		go al.handleReasoning(
			ctx,
			response.Reasoning,
			opts.Channel,
			al.targetReasoningChannelID(opts.Channel),
		)

		logger.DebugCF("agent", "LLM response",
			map[string]any{
				"agent_id":       agent.ID,
				"iteration":      iteration,
				"content_chars":  len(response.Content),
				"tool_calls":     len(response.ToolCalls),
				"reasoning":      response.Reasoning,
				"target_channel": al.targetReasoningChannelID(opts.Channel),
				"channel":        opts.Channel,
			})

		// Check if no tool calls - we're done
		if len(response.ToolCalls) == 0 {
			finalContent = response.Content
			logger.InfoCF("agent", "LLM response without tool calls (direct answer)",
				map[string]any{
					"agent_id":      agent.ID,
					"iteration":     iteration,
					"content_chars": len(finalContent),
				})
			break
		}

		normalizedToolCalls := make([]providers.ToolCall, 0, len(response.ToolCalls))
		for _, tc := range response.ToolCalls {
			normalizedToolCalls = append(normalizedToolCalls, providers.NormalizeToolCall(tc))
		}

		// Resource budget: cap total executed tool calls (soft limit).
		if maxToolCallsPerRun > 0 && toolCallsUsed+len(normalizedToolCalls) > maxToolCallsPerRun {
			finalContent = fmt.Sprintf(
				"RESOURCE_BUDGET_EXCEEDED: tool call budget exceeded (%d). "+
					"Please narrow the request or reduce the number of tools used.",
				maxToolCallsPerRun,
			)
			logger.WarnCF("agent", "Resource budget exceeded (tool calls)", map[string]any{
				"agent_id":           agent.ID,
				"iteration":          iteration,
				"tool_calls_used":    toolCallsUsed,
				"tool_calls_pending": len(normalizedToolCalls),
				"tool_calls_budget":  maxToolCallsPerRun,
				"session_key":        opts.SessionKey,
			})
			break
		}

		// Update working state with LLM's reasoning as next-action hint
		if reasoning := strings.TrimSpace(response.Content); reasoning != "" {
			if ws := opts.WorkingState; ws != nil {
				hint := reasoning
				if len(hint) > 200 {
					hint = hint[:200] + "..."
				}
				ws.SetNextAction(hint)
			}
		}

		// Log tool calls
		toolNames := make([]string, 0, len(normalizedToolCalls))
		for _, tc := range normalizedToolCalls {
			toolNames = append(toolNames, tc.Name)
		}
		logger.InfoCF("agent", "LLM requested tool calls",
			map[string]any{
				"agent_id":  agent.ID,
				"tools":     toolNames,
				"count":     len(normalizedToolCalls),
				"iteration": iteration,
			})

		// Loop detection: check if the same tool+args have been called too many times
		if loopingTool := detectToolCallLoop(recentToolCalls, normalizedToolCalls, 3); loopingTool != "" {
			logger.WarnCF("agent", "Tool call loop detected",
				map[string]any{
					"agent_id":  agent.ID,
					"tool":      loopingTool,
					"iteration": iteration,
				})

			// Build assistant message so tool results can be attached (API requirement)
			loopAssistantMsg := providers.Message{
				Role:    "assistant",
				Content: response.Content,
			}
			for _, tc := range normalizedToolCalls {
				argumentsJSON, _ := json.Marshal(tc.Arguments)
				loopAssistantMsg.ToolCalls = append(loopAssistantMsg.ToolCalls, providers.ToolCall{
					ID:   tc.ID,
					Type: "function",
					Name: tc.Name,
					Function: &providers.FunctionCall{
						Name:      tc.Name,
						Arguments: string(argumentsJSON),
					},
				})
			}
			messages = append(messages, loopAssistantMsg)

			// Return error tool results for each call
			loopNotice := fmt.Sprintf("Loop detected: '%s' called with same arguments 3+ times. "+
				"Try a different approach, use a different tool, or explain why you are stuck.", loopingTool)
			for _, tc := range normalizedToolCalls {
				messages = append(messages, providers.Message{
					Role:       "tool",
					Content:    loopNotice,
					ToolCallID: tc.ID,
				})
			}
			continue
		}

		// Track current tool calls for future loop detection
		for _, tc := range normalizedToolCalls {
			argsJSON, _ := json.Marshal(tc.Arguments)
			recentToolCalls = append(recentToolCalls, toolCallSignature{
				Name: tc.Name,
				Args: string(argsJSON),
			})
		}

		// Build assistant message with tool calls
		assistantMsg := providers.Message{
			Role:             "assistant",
			Content:          response.Content,
			ReasoningContent: response.ReasoningContent,
		}
		for _, tc := range normalizedToolCalls {
			argumentsJSON, _ := json.Marshal(tc.Arguments)
			// Copy ExtraContent to ensure thought_signature is persisted for Gemini 3
			extraContent := tc.ExtraContent
			thoughtSignature := ""
			if tc.Function != nil {
				thoughtSignature = tc.Function.ThoughtSignature
			}

			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, providers.ToolCall{
				ID:   tc.ID,
				Type: "function",
				Name: tc.Name,
				Function: &providers.FunctionCall{
					Name:             tc.Name,
					Arguments:        string(argumentsJSON),
					ThoughtSignature: thoughtSignature,
				},
				ExtraContent:     extraContent,
				ThoughtSignature: thoughtSignature,
			})
		}
		messages = append(messages, assistantMsg)

		// Save assistant message with tool calls to session
		agent.Sessions.AddFullMessage(opts.SessionKey, assistantMsg)

		cfg := al.Config()
		parallelCfg := tools.ToolCallParallelConfig{
			Enabled:        cfg != nil && cfg.Orchestration.ToolCallsParallelEnabled,
			MaxConcurrency: 0,
			Mode:           "",
		}
		if cfg != nil {
			parallelCfg.MaxConcurrency = cfg.Orchestration.MaxToolCallConcurrency
			parallelCfg.Mode = cfg.Orchestration.ParallelToolsMode
			parallelCfg.ToolPolicyOverrides = cfg.Orchestration.ToolParallelOverrides
		}

		traceOpts := tools.ToolTraceOptions{}
		if cfg != nil {
			traceOpts.Enabled = cfg.Tools.Trace.Enabled
			traceOpts.Dir = cfg.Tools.Trace.Dir
			traceOpts.WritePerCallFiles = cfg.Tools.Trace.WritePerCallFiles
			traceOpts.MaxArgPreviewChars = cfg.Tools.Trace.MaxArgPreviewChars
			traceOpts.MaxResultPreviewChars = cfg.Tools.Trace.MaxResultPreviewChars
		}

		errorTemplateOpts := tools.ToolErrorTemplateOptions{}
		if cfg != nil {
			errorTemplateOpts.Enabled = cfg.Tools.ErrorTemplate.Enabled
			errorTemplateOpts.IncludeSchema = cfg.Tools.ErrorTemplate.IncludeSchema
			// Include tool list for "tool not found" to help the model self-correct.
			errorTemplateOpts.IncludeAvailableTools = true
		}

		runID := ""
		if trace != nil {
			runID = trace.RunID()
		}

		policyCfg := config.ToolPolicyConfig{}
		policyTags := map[string]string(nil)
		if cfg != nil {
			policyCfg = cfg.Tools.Policy
			policyTags = cfg.Tools.Policy.Audit.Tags
		}
		estopCfg := config.EstopConfig{}
		if cfg != nil {
			estopCfg = cfg.Tools.Estop
		}

		planRestrictedTools := []string(nil)
		planRestrictedPrefixes := []string(nil)
		if cfg != nil {
			planRestrictedTools = cfg.Tools.PlanMode.RestrictedTools
			planRestrictedPrefixes = cfg.Tools.PlanMode.RestrictedPrefixes
		}

		toolExecutions := tools.ExecuteToolCalls(ctx, agent.Tools, normalizedToolCalls, tools.ToolCallExecutionOptions{
			Channel:                opts.Channel,
			ChatID:                 opts.ChatID,
			SenderID:               opts.SenderID,
			PlanMode:               opts.PlanMode,
			PlanRestrictedTools:    planRestrictedTools,
			PlanRestrictedPrefixes: planRestrictedPrefixes,
			Workspace:              agent.Workspace,
			SessionKey:             opts.SessionKey,
			RunID:                  runID,
			IsResume:               opts.Resume,
			Policy:                 policyCfg,
			PolicyTags:             policyTags,
			Estop:                  estopCfg,
			Iteration:              iteration,
			LogScope:               "agent",
			Parallel:               parallelCfg,
			Trace:                  traceOpts,
			MaxResultChars:         maxToolResultChars,
			ErrorTemplate:          errorTemplateOpts,
			Hooks:                  tools.BuildDefaultToolHooks(cfg),
			// Create async callback for tools that implement AsyncTool.
			// Following openclaw's design, async tools do not send results directly
			// to users. The agent handles user notification via processSystemMessage.
			AsyncCallbackForCall: func(call providers.ToolCall) tools.AsyncCallback {
				return func(callbackCtx context.Context, result *tools.ToolResult) {
					if result == nil {
						return
					}
					if !result.Silent && result.ForUser != "" {
						logger.InfoCF("agent", "Async tool completed, agent will handle notification",
							map[string]any{
								"tool":        call.Name,
								"content_len": len(result.ForUser),
							})
					}
				}
			},
		})
		toolCallsUsed += len(toolExecutions)
		if trace != nil {
			trace.recordToolBatch(iteration, toolExecutions)
		}

		handoffTargetID := ""
		handoffTakeover := false
		for _, executed := range toolExecutions {
			toolResult := executed.Result
			tc := executed.ToolCall

			// Track tool execution in working state (per-run).
			if ws := opts.WorkingState; ws != nil {
				ws.RecordToolCall(tc.Name, toolResult.IsError)
				// Record as a completed step with truncated outcome
				outcome := toolResult.ForLLM
				if len(outcome) > 120 {
					outcome = outcome[:120] + "..."
				}
				if toolResult.IsError {
					outcome = "[error] " + outcome
				}
				ws.AddCompletedStep(tc.Name, outcome, tc.Name)
			}

			// Send ForUser content to user immediately if not Silent.
			if !toolResult.Silent && toolResult.ForUser != "" && opts.SendResponse {
				al.bus.PublishOutbound(ctx, bus.OutboundMessage{
					Channel: opts.Channel,
					ChatID:  opts.ChatID,
					Content: toolResult.ForUser,
				})
				logger.DebugCF("agent", "Sent tool result to user",
					map[string]any{
						"tool":        tc.Name,
						"content_len": len(toolResult.ForUser),
					})
			}

			// If tool returned media refs, publish them as outbound media
			if len(toolResult.Media) > 0 && opts.SendResponse {
				parts := make([]bus.MediaPart, 0, len(toolResult.Media))
				for _, ref := range toolResult.Media {
					part := bus.MediaPart{Ref: ref}
					// Populate metadata from MediaResolver when available
					if al.mediaResolver != nil {
						if _, meta, err := al.mediaResolver.ResolveWithMeta(ref); err == nil {
							part.Filename = strings.TrimSpace(meta.Filename)
							part.ContentType = strings.TrimSpace(meta.ContentType)
							part.Type = inferMediaType(part.Filename, part.ContentType)
						}
					}
					parts = append(parts, part)
				}
				al.bus.PublishOutboundMedia(ctx, bus.OutboundMediaMessage{
					Channel: opts.Channel,
					ChatID:  opts.ChatID,
					Parts:   parts,
				})
			}

			// Determine content for LLM based on tool result
			contentForLLM := toolResult.ForLLM
			if contentForLLM == "" && toolResult.Err != nil {
				contentForLLM = toolResult.Err.Error()
			}

			// Phase F: Swarm-style agent handoff. If the model calls `handoff`, switch the
			// active agent for subsequent iterations while keeping the shared conversation history.
			if strings.EqualFold(strings.TrimSpace(tc.Name), "handoff") && !toolResult.IsError {
				if raw, ok := tc.Arguments["agent_id"].(string); ok {
					handoffTargetID = strings.TrimSpace(raw)
				}
				if handoffTargetID == "" {
					if raw, ok := tc.Arguments["agent_name"].(string); ok {
						handoffTargetID = strings.TrimSpace(raw)
					}
				}
				// Default takeover=true (matches tool default).
				takeover := true
				if v, ok := tc.Arguments["takeover"].(bool); ok {
					takeover = v
				}
				handoffTakeover = takeover
			}

			toolResultMsg := providers.Message{
				Role:       "tool",
				Content:    contentForLLM,
				ToolCallID: tc.ID,
			}
			messages = append(messages, toolResultMsg)

			// Save tool result message to session
			agent.Sessions.AddFullMessage(opts.SessionKey, toolResultMsg)
		}

		// Apply handoff after all tool results are recorded, then rebuild the prompt with
		// the new agent's system prompt and tool set, while preserving session history.
		if strings.TrimSpace(handoffTargetID) != "" && handoffTakeover {
			target, ok := al.registry.GetAgent(handoffTargetID)
			if ok && target != nil {
				logger.InfoCF("agent", "Handoff: switching active agent", map[string]any{
					"from_agent_id": agent.ID,
					"to_agent_id":   target.ID,
					"session_key":   opts.SessionKey,
					"iteration":     iteration,
				})

				agent = target
				modelForRun = strings.TrimSpace(agent.Model)
				if agent.Sessions != nil {
					if override, ok := agent.Sessions.EffectiveModelOverride(opts.SessionKey); ok {
						modelForRun = override
					}
				}
				if trace != nil {
					trace.agentID = strings.TrimSpace(agent.ID)
					trace.model = strings.TrimSpace(modelForRun)
				}

				history := agent.Sessions.GetHistory(opts.SessionKey)
				summary := agent.Sessions.GetSummary(opts.SessionKey)
				messages = agent.ContextBuilder.BuildMessagesForSession(
					opts.SessionKey,
					history,
					summary,
					"",
					nil,
					opts.Channel,
					opts.ChatID,
					opts.WorkingState,
				)
			} else {
				logger.WarnCF("agent", "Handoff target agent not found", map[string]any{
					"target_agent_id": handoffTargetID,
					"session_key":     opts.SessionKey,
					"iteration":       iteration,
				})
			}
		}
	}

	// Log cumulative token usage for the entire request
	if totalPromptTokens > 0 || totalCompletionTokens > 0 {
		logger.InfoCF("agent", "Request token usage summary",
			map[string]any{
				"agent_id":                agent.ID,
				"iterations":              iteration,
				"total_prompt_tokens":     totalPromptTokens,
				"total_completion_tokens": totalCompletionTokens,
				"total_tokens":            totalPromptTokens + totalCompletionTokens,
				"session_key":             opts.SessionKey,
			})
	}

	return finalContent, iteration, agent, nil
}
