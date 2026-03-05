package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xwysyy/X-Claw/pkg/logger"
	"github.com/xwysyy/X-Claw/pkg/providers"
)

type ToolRegistry struct {
	tools map[string]Tool
	mu    sync.RWMutex
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]Tool),
	}
}

func (r *ToolRegistry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		logger.WarnCF("tools", "Tool registration overwrites existing tool",
			map[string]any{"name": name})
	}
	r.tools[name] = tool
}

// Unregister removes a tool by name. It returns true if a tool existed.
func (r *ToolRegistry) Unregister(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[name]; !ok {
		return false
	}
	delete(r.tools, name)
	return true
}

// UnregisterPrefix removes all tools whose names start with prefix.
// Returns how many tools were removed.
func (r *ToolRegistry) UnregisterPrefix(prefix string) int {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	removed := 0
	for name := range r.tools {
		if strings.HasPrefix(name, prefix) {
			delete(r.tools, name)
			removed++
		}
	}
	return removed
}

func (r *ToolRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

// ParallelPolicy returns the configured parallel policy for a tool.
// Tools that do not implement ParallelPolicyProvider default to serial-only.
func (r *ToolRegistry) ParallelPolicy(name string) ToolParallelPolicy {
	tool, ok := r.Get(name)
	if !ok || tool == nil {
		return ToolParallelSerialOnly
	}
	provider, ok := tool.(ParallelPolicyProvider)
	if !ok {
		return ToolParallelSerialOnly
	}
	policy := provider.ParallelPolicy()
	if policy == "" {
		return ToolParallelSerialOnly
	}
	return policy
}

// IsParallelInstanceSafe reports whether one shared tool instance can be used
// concurrently across multiple tool calls.
func (r *ToolRegistry) IsParallelInstanceSafe(name string) bool {
	tool, ok := r.Get(name)
	if !ok || tool == nil {
		return false
	}
	if safeTool, ok := tool.(ConcurrentSafeTool); ok {
		return safeTool.SupportsConcurrentExecution()
	}
	// Default: assume tools are safe to reuse concurrently unless they explicitly
	// opt out via ConcurrentSafeTool. The executor still gates parallelization
	// by tool-level policy (read_only_only).
	return true
}

// CanRunToolCallInParallel reports whether a tool call may run in parallel
// under the given mode.
//
// Supported modes:
// - "read_only_only" (default): only tools marked parallel_read_only are parallelized.
// - "all": every tool is eligible.
func (r *ToolRegistry) CanRunToolCallInParallel(name, mode string) bool {
	if !r.IsParallelInstanceSafe(name) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ParallelToolsModeAll:
		return true
	case "", ParallelToolsModeReadOnlyOnly:
		return r.ParallelPolicy(name) == ToolParallelReadOnly
	default:
		return false
	}
}

func (r *ToolRegistry) Execute(ctx context.Context, name string, args map[string]any) *ToolResult {
	return r.ExecuteWithContext(ctx, name, args, "", "", "", nil)
}

// ExecuteWithContext executes a tool with channel/chatID context and an optional
// async callback.
//
// The async callback is attached to the execution context so async-capable tools
// can retrieve it without relying on shared mutable tool instance state.
func (r *ToolRegistry) ExecuteWithContext(
	ctx context.Context,
	name string,
	args map[string]any,
	channel, chatID string,
	senderID string,
	asyncCallback AsyncCallback,
) *ToolResult {
	ctx = withExecutionContext(ctx, channel, chatID, senderID)
	ctx = withExecutionAsyncCallback(ctx, asyncCallback)
	// Support newer ToolChannel/ToolChatID accessors as well.
	ctx = WithToolContext(ctx, channel, chatID)

	// Avoid logging raw args (may contain secrets). ToolTrace/Policy is the
	// canonical place for detailed auditing.
	argKeys := make([]string, 0, len(args))
	for k := range args {
		argKeys = append(argKeys, k)
	}
	sort.Strings(argKeys)
	logger.InfoCF("tool", "Tool execution started",
		map[string]any{
			"tool":      name,
			"args_keys": argKeys,
			"args_len":  len(args),
		})

	tool, ok := r.Get(name)
	if !ok {
		logger.ErrorCF("tool", "Tool not found",
			map[string]any{
				"tool": name,
			})
		return ErrorResult(fmt.Sprintf("tool %q not found", name)).WithError(fmt.Errorf("tool not found"))
	}

	// If tool implements AsyncExecutor and callback is provided, use ExecuteAsync.
	// The callback is a call parameter, not mutable state on the tool instance.
	var result *ToolResult
	start := time.Now()
	if asyncExec, ok := tool.(AsyncExecutor); ok && asyncCallback != nil {
		logger.DebugCF("tool", "Executing async tool via ExecuteAsync",
			map[string]any{
				"tool": name,
			})
		result = asyncExec.ExecuteAsync(ctx, args, asyncCallback)
	} else {
		result = tool.Execute(ctx, args)
	}
	duration := time.Since(start)

	// Log based on result type
	if result.IsError {
		logger.ErrorCF("tool", "Tool execution failed",
			map[string]any{
				"tool":        name,
				"duration_ms": duration.Milliseconds(),
			})
	} else if result.Async {
		logger.InfoCF("tool", "Tool started (async)",
			map[string]any{
				"tool":        name,
				"duration_ms": duration.Milliseconds(),
			})
	} else {
		logger.InfoCF("tool", "Tool execution completed",
			map[string]any{
				"tool":          name,
				"duration_ms":   duration.Milliseconds(),
				"result_length": len(result.ForLLM),
			})
	}

	return result
}

// sortedToolNames returns tool names in sorted order for deterministic iteration.
// This is critical for KV cache stability: non-deterministic map iteration would
// produce different system prompts and tool definitions on each call, invalidating
// the LLM's prefix cache even when no tools have changed.
func (r *ToolRegistry) sortedToolNames() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *ToolRegistry) GetDefinitions() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sorted := r.sortedToolNames()
	definitions := make([]map[string]any, 0, len(sorted))
	for _, name := range sorted {
		definitions = append(definitions, ToolToSchema(r.tools[name]))
	}
	return definitions
}

// ToProviderDefs converts tool definitions to provider-compatible format.
// This is the format expected by LLM provider APIs.
func (r *ToolRegistry) ToProviderDefs() []providers.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sorted := r.sortedToolNames()
	definitions := make([]providers.ToolDefinition, 0, len(sorted))
	for _, name := range sorted {
		tool := r.tools[name]
		schema := ToolToSchema(tool)

		// Safely extract nested values with type checks
		fn, ok := schema["function"].(map[string]any)
		if !ok {
			continue
		}

		name, _ := fn["name"].(string)
		desc, _ := fn["description"].(string)
		params, _ := fn["parameters"].(map[string]any)

		definitions = append(definitions, providers.ToolDefinition{
			Type: "function",
			Function: providers.ToolFunctionDefinition{
				Name:        name,
				Description: desc,
				Parameters:  params,
			},
		})
	}
	return definitions
}

// List returns a list of all registered tool names.
func (r *ToolRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.sortedToolNames()
}

// Count returns the number of registered tools.
func (r *ToolRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// GetSummaries returns human-readable summaries of all registered tools.
// Returns a slice of "name - description" strings.
func (r *ToolRegistry) GetSummaries() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sorted := r.sortedToolNames()
	summaries := make([]string, 0, len(sorted))
	for _, name := range sorted {
		tool := r.tools[name]
		summaries = append(summaries, fmt.Sprintf("- `%s` - %s", tool.Name(), tool.Description()))
	}
	return summaries
}
