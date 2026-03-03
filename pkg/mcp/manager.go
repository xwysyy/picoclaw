package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type Manager struct {
	cfg config.MCPToolsConfig

	mu      sync.Mutex
	servers map[string]*Server
}

func NewManager(cfg config.MCPToolsConfig) *Manager {
	return &Manager{
		cfg:     cfg,
		servers: map[string]*Server{},
	}
}

func (m *Manager) Enabled() bool {
	return m != nil && m.cfg.Enabled
}

// ToolPrefixes returns the effective tool name prefixes for all configured MCP servers.
// It is used for hot reload to unregister previously registered MCP tools.
func (m *Manager) ToolPrefixes() []string {
	if m == nil {
		return nil
	}

	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()

	prefixes := make([]string, 0, len(cfg.Servers))
	seen := map[string]struct{}{}
	for _, raw := range cfg.Servers {
		name := strings.TrimSpace(raw.Name)
		if name == "" {
			continue
		}
		p := strings.TrimSpace(raw.ToolPrefix)
		if p == "" {
			p = "mcp_" + name + "_"
		}
		p = sanitizeToolName(p)
		if p != "" && !strings.HasSuffix(p, "_") {
			p += "_"
		}
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)
	return prefixes
}

// ApplyConfig swaps the MCP config and resets cached servers/tools.
// Any existing server connections are best-effort closed.
func (m *Manager) ApplyConfig(cfg config.MCPToolsConfig) {
	if m == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, s := range m.servers {
		if s != nil {
			_ = s.Close()
		}
	}
	m.servers = map[string]*Server{}
	m.cfg = cfg
}

func (m *Manager) Init(ctx context.Context) {
	if m == nil || !m.cfg.Enabled {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.servers == nil {
		m.servers = map[string]*Server{}
	}

	for _, raw := range m.cfg.Servers {
		name := strings.TrimSpace(raw.Name)
		if name == "" {
			logger.WarnCF("mcp", "Skipping MCP server with empty name", nil)
			continue
		}
		if _, ok := m.servers[name]; ok {
			continue
		}

		server := NewServer(raw)
		m.servers[name] = server

		// Best-effort connect+discover now (Phase D1: startup discovery).
		connectCtx := ctx
		timeout := server.Timeout()
		if timeout > 0 {
			if _, hasDeadline := connectCtx.Deadline(); !hasDeadline {
				var cancel context.CancelFunc
				connectCtx, cancel = context.WithTimeout(connectCtx, timeout)
				defer cancel()
			}
		}

		if _, err := server.ListTools(connectCtx); err != nil {
			logger.WarnCF("mcp", "MCP server discovery failed (best-effort)", map[string]any{
				"server": name,
				"error":  err.Error(),
			})
		}
	}
}

// RegisterTools registers all discovered MCP tools into a ToolRegistry.
//
// Tool names are prefixed (default: mcp_<server>_) and sanitized so they remain
// compatible with LLM tool calling name constraints.
func (m *Manager) RegisterTools(ctx context.Context, registry *tools.ToolRegistry) error {
	if m == nil || !m.cfg.Enabled {
		return nil
	}
	if registry == nil {
		return fmt.Errorf("tool registry is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Ensure servers are created and (best-effort) discovered.
	m.Init(ctx)

	m.mu.Lock()
	servers := make([]*Server, 0, len(m.servers))
	for _, s := range m.servers {
		servers = append(servers, s)
	}
	m.mu.Unlock()

	sort.Slice(servers, func(i, j int) bool { return servers[i].Name() < servers[j].Name() })

	registered := 0
	for _, server := range servers {
		toolDefs, err := server.ListTools(ctx)
		if err != nil {
			logger.WarnCF("mcp", "Skipping MCP server tools (list_tools failed)", map[string]any{
				"server": server.Name(),
				"error":  err.Error(),
			})
			continue
		}

		prefix := server.ToolPrefix()
		for _, def := range toolDefs {
			wrapped := NewTool(server, prefix, def)
			if wrapped == nil {
				continue
			}
			if _, exists := registry.Get(wrapped.Name()); exists {
				logger.WarnCF("mcp", "MCP tool name collision; skipping", map[string]any{
					"tool":   wrapped.Name(),
					"server": server.Name(),
				})
				continue
			}
			registry.Register(wrapped)
			registered++
		}
	}

	if registered > 0 {
		logger.InfoCF("mcp", "Registered MCP tools", map[string]any{
			"count": registered,
		})
	}

	return nil
}
