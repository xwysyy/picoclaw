package tools

import (
	"context"
	"strings"
)

// Tool is the interface that all tools must implement.
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	Execute(ctx context.Context, args map[string]any) *ToolResult
}

// --- Request-scoped tool context (channel / chatID) ---
//
// Carried via context.Value so that concurrent tool calls each receive
// their own immutable copy — no mutable state on singleton tool instances.
//
// Keys are unexported pointer-typed vars — guaranteed collision-free,
// and only accessible through the helper functions below.

type toolCtxKey struct{ name string }

var (
	ctxKeyChannel = &toolCtxKey{"channel"}
	ctxKeyChatID  = &toolCtxKey{"chatID"}
)

// WithToolContext returns a child context carrying channel and chatID.
func WithToolContext(ctx context.Context, channel, chatID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	channel = strings.TrimSpace(channel)
	chatID = strings.TrimSpace(chatID)
	ctx = context.WithValue(ctx, ctxKeyChannel, channel)
	ctx = context.WithValue(ctx, ctxKeyChatID, chatID)
	return ctx
}

// ToolChannel extracts the channel from ctx, or "" if unset.
func ToolChannel(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyChannel).(string)
	v = strings.TrimSpace(v)
	if v != "" {
		return v
	}
	// Backward-compat: allow tools/tests that still use the legacy execution context keys.
	return toolExecutionChannel(ctx)
}

// ToolChatID extracts the chatID from ctx, or "" if unset.
func ToolChatID(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyChatID).(string)
	v = strings.TrimSpace(v)
	if v != "" {
		return v
	}
	// Backward-compat: allow tools/tests that still use the legacy execution context keys.
	return toolExecutionChatID(ctx)
}

// AsyncCallback is a function type that async tools use to notify completion.
// When an async tool finishes its work, it calls this callback with the result.
//
// The ctx parameter allows the callback to be canceled if the agent is shutting down.
// The result parameter contains the tool's execution result.
type AsyncCallback func(ctx context.Context, result *ToolResult)

// AsyncExecutor is an optional interface that tools can implement to support
// asynchronous execution with completion callbacks.
//
// Unlike the old AsyncTool pattern (SetCallback + Execute), AsyncExecutor
// receives the callback as a parameter of ExecuteAsync. This eliminates the
// data race where concurrent calls could overwrite each other's callbacks
// on a shared tool instance.
//
// This is useful for:
//   - Long-running operations that shouldn't block the agent loop
//   - Long-running background tasks that complete independently
//   - Background tasks that need to report results later
//
// Example:
//
//	func (t *BackgroundTool) ExecuteAsync(ctx context.Context, args map[string]any, cb AsyncCallback) *ToolResult {
//	    go func() {
//	        result := t.runSubagent(ctx, args)
//	        if cb != nil { cb(ctx, result) }
//	    }()
//	    return AsyncResult("Subagent spawned, will report back")
//	}
type AsyncExecutor interface {
	Tool
	// ExecuteAsync runs the tool asynchronously. The callback cb will be
	// invoked (possibly from another goroutine) when the async operation
	// completes. cb is guaranteed to be non-nil by the caller (registry).
	ExecuteAsync(ctx context.Context, args map[string]any, cb AsyncCallback) *ToolResult
}

// ToolParallelPolicy declares whether a tool is safe to run concurrently
// within a single LLM tool-call batch.
type ToolParallelPolicy string

const (
	// ToolParallelSerialOnly is the safe default for tools with side effects.
	ToolParallelSerialOnly ToolParallelPolicy = "serial_only"
	// ToolParallelReadOnly marks tools that are safe to run in parallel.
	ToolParallelReadOnly ToolParallelPolicy = "parallel_read_only"
)

const (
	// ParallelToolsModeReadOnlyOnly allows parallel execution only for tools
	// explicitly marked as ToolParallelReadOnly.
	ParallelToolsModeReadOnlyOnly = "read_only_only"
	// ParallelToolsModeAll allows all tools to run in parallel.
	ParallelToolsModeAll = "all"
)

// ParallelPolicyProvider is an optional interface that tools can implement
// to opt into parallel batch execution.
type ParallelPolicyProvider interface {
	ParallelPolicy() ToolParallelPolicy
}

// ConcurrentSafeTool is an optional interface for tool instances that can
// safely handle concurrent ExecuteWithContext calls on the same singleton object.
//
// Tools that rely on mutable per-call instance state (for example through
// SetContext or SetCallback) should return false.
type ConcurrentSafeTool interface {
	SupportsConcurrentExecution() bool
}

func ToolToSchema(tool Tool) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        tool.Name(),
			"description": tool.Description(),
			"parameters":  tool.Parameters(),
		},
	}
}
