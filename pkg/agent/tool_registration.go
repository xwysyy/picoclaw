package agent

import (
	"context"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/logger"
	"github.com/xwysyy/X-Claw/pkg/tools"
)

type toolRegistrar struct {
	cfg              *config.Config
	msgBus           *bus.MessageBus
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
	messageTool.SetSendCallback(func(channel, chatID, content string) error {
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer pubCancel()
		return r.msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
			Channel: channel,
			ChatID:  chatID,
			Content: content,
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
