package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/xwysyy/X-Claw/internal/core/events"
	"github.com/xwysyy/X-Claw/pkg/auditlog"
	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/logger"
	"github.com/xwysyy/X-Claw/pkg/providers"
	"github.com/xwysyy/X-Claw/pkg/utils"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ToolCallParallelConfig configures in-batch parallel execution for tool calls.
type ToolCallParallelConfig struct {
	Enabled        bool
	MaxConcurrency int
	Mode           string
	// ToolPolicyOverrides allows per-tool policy overrides.
	// Values: "serial_only" or "parallel_read_only".
	ToolPolicyOverrides map[string]string
}

// ToolCallExecutionOptions controls how tool calls are executed.
type ToolCallExecutionOptions struct {
	Channel  string
	ChatID   string
	SenderID string

	// PlanMode enables the "plan" permission mode (Plan Mode, ROADMAP.md:1225).
	// When true, restricted tools are denied (typically side-effect tools).
	PlanMode bool
	// PlanRestrictedTools are denied while PlanMode is true.
	PlanRestrictedTools []string
	// PlanRestrictedPrefixes are denied while PlanMode is true.
	PlanRestrictedPrefixes []string

	// Workspace is the agent workspace path used for optional on-disk tool tracing.
	// When empty, tracing falls back to Trace.Dir (if set) or is disabled.
	Workspace string
	// SessionKey is a stable identifier for grouping tool traces on disk.
	// In agent mode this is typically the session key; when empty we fallback
	// to channel/chatID.
	SessionKey string
	// RunID associates tool traces with one durable run trace (Phase E1/E2).
	// When empty, per-run policy ledger features (confirm/idempotency) are disabled.
	RunID string
	// IsResume indicates this tool batch belongs to a resume_last_task flow (Phase E2).
	// Used by tool policy confirmation gating mode "resume_only".
	IsResume bool

	// Policy applies centralized tool guardrails (Phase D2).
	Policy config.ToolPolicyConfig
	// PolicyTags are attached to tool trace events and snapshots.
	// When empty, no tags are recorded.
	PolicyTags map[string]string

	// Estop enables the global kill switch for tool execution (ROADMAP.md:1138).
	// It is evaluated before Plan Mode / tool policy and applies to all tools
	// (built-in + MCP) through this executor chokepoint.
	Estop config.EstopConfig

	Iteration int
	LogScope  string

	Parallel ToolCallParallelConfig

	Trace ToolTraceOptions

	// MaxResultChars truncates ToolResult.ForLLM/ForUser to cap memory usage.
	// 0 disables truncation.
	MaxResultChars int

	// ErrorTemplate optionally wraps tool errors into a structured, self-recoverable
	// template for the LLM (A3 in ROADMAP.md).
	//
	// This is executor-level (not tool-specific) so we can standardize error recovery
	// without changing each tool's implementation.
	ErrorTemplate ToolErrorTemplateOptions

	// AsyncCallbackForCall creates a callback for async-capable tools.
	// It may be nil when async callbacks are not needed.
	AsyncCallbackForCall func(call providers.ToolCall) AsyncCallback

	// Hooks enables lightweight tool call interception/scrubbing (Phase N2).
	// When nil/empty, no hooks are applied.
	Hooks []ToolHook
}

// ToolCallExecution captures one tool call execution result.
type ToolCallExecution struct {
	ToolCall   providers.ToolCall
	Result     *ToolResult
	DurationMS int64
}

// ExecuteToolCalls executes tool calls with optional bounded parallelism while
// preserving output order exactly as provided in the input slice.
func ExecuteToolCalls(
	ctx context.Context,
	registry *ToolRegistry,
	toolCalls []providers.ToolCall,
	opts ToolCallExecutionOptions,
) []ToolCallExecution {
	if len(toolCalls) == 0 {
		return nil
	}
	batchStart := time.Now()

	scope := opts.LogScope
	if scope == "" {
		scope = "tool"
	}

	traceWriter := newToolTraceWriter(opts, scope)
	hooks := opts.Hooks

	results := make([]ToolCallExecution, len(toolCalls))
	parallelCount := 0
	serialCount := 0
	mode := normalizeParallelMode(opts.Parallel.Mode)

	shouldParallelize := func(tc providers.ToolCall) bool {
		if registry == nil {
			return false
		}
		if !opts.Parallel.Enabled {
			return false
		}
		if opts.Parallel.MaxConcurrency == 1 {
			return false
		}
		if !registry.IsParallelInstanceSafe(tc.Name) {
			return false
		}
		if override, ok := getOverridePolicy(tc.Name, opts.Parallel.ToolPolicyOverrides); ok {
			return override == ToolParallelReadOnly
		}
		switch mode {
		case ParallelToolsModeAll:
			return true
		case ParallelToolsModeReadOnlyOnly:
			return registry.CanRunToolCallInParallel(tc.Name, ParallelToolsModeReadOnlyOnly)
		default:
			return false
		}
	}

	runOne := func(idx int) {
		tc := toolCalls[idx]
		originalTool := strings.TrimSpace(tc.Name)
		originalToolCallID := strings.TrimSpace(tc.ID)

		execCtx := withExecutionRunID(withExecutionSessionKey(withExecutionIsResume(ctx, opts.IsResume), opts.SessionKey), opts.RunID)

		meta := ToolHookContext{
			Workspace:  strings.TrimSpace(opts.Workspace),
			SessionKey: utils.CanonicalSessionKey(opts.SessionKey),
			RunID:      strings.TrimSpace(opts.RunID),

			Channel:  strings.TrimSpace(opts.Channel),
			ChatID:   strings.TrimSpace(opts.ChatID),
			SenderID: strings.TrimSpace(opts.SenderID),

			Iteration: opts.Iteration,
			IsResume:  opts.IsResume,
			PlanMode:  opts.PlanMode,

			PolicyTags: copyStringMap(opts.PolicyTags),
		}

		hookActions := make([]ToolHookAction, 0, 2)
		call := tc
		var hookShortCircuit *ToolResult
		if len(hooks) > 0 {
			for _, h := range hooks {
				if h == nil {
					continue
				}
				updated, shortCircuit, action := h.BeforeToolCall(execCtx, call, meta)
				if action != nil {
					a := *action
					a.Hook = strings.TrimSpace(h.Name())
					if strings.TrimSpace(a.Stage) == "" {
						a.Stage = "before"
					}
					hookActions = append(hookActions, a)
				}
				if strings.TrimSpace(updated.ID) != originalToolCallID || strings.TrimSpace(updated.Name) != originalTool {
					hookShortCircuit = ErrorResult("tool hook attempted to change tool_call_id or tool name (unsupported)")
					break
				}
				call = updated
				if shortCircuit != nil {
					hookShortCircuit = shortCircuit
					break
				}
			}
		}

		argsJSON, _ := json.Marshal(call.Arguments)
		redactedArgsJSON := argsJSON
		argsPreview := utils.Truncate(string(redactedArgsJSON), 200)
		logger.InfoCF(scope, fmt.Sprintf("Tool call: %s(%s)", call.Name, argsPreview),
			map[string]any{
				"tool":      call.Name,
				"iteration": opts.Iteration,
			})

		var asyncCallback AsyncCallback
		if opts.AsyncCallbackForCall != nil {
			asyncCallback = opts.AsyncCallbackForCall(call)
		}

		start := time.Now()
		if traceWriter != nil {
			traceWriter.RecordStart(start, opts.Iteration, call, redactedArgsJSON)
		}

		policyDecision := ""
		policyReason := ""
		policyTimeoutMS := 0
		idempotencyKey := ""

		var toolResult *ToolResult

		// Estop (ROADMAP.md:1138): global kill switch / freeze layer.
		if toolResult == nil && hookShortCircuit != nil {
			policyDecision = "hook"
			policyReason = "hook short-circuit"
			toolResult = hookShortCircuit
		}

		if toolResult == nil {
			if denied, reason := evaluateEstop(opts.Estop, opts.Workspace, call.Name); denied {
				policyDecision = "deny"
				policyReason = reason
				toolResult = ErrorResult(formatToolExecutionDenied(call.Name, reason)).WithError(errors.New(reason))
			}
		}

		if toolResult == nil && opts.PlanMode {
			if matchesToolRule(call.Name, opts.PlanRestrictedTools, opts.PlanRestrictedPrefixes) {
				policyDecision = "deny"
				policyReason = "plan mode restricts this tool"
				toolResult = ErrorResult(formatToolExecutionDenied(call.Name, policyReason)).WithError(errors.New(policyReason))
			}
		}

		if toolResult == nil && opts.Policy.Enabled {
			if denied, reason := evaluateToolPolicy(call.Name, opts.Policy, opts.IsResume); denied {
				policyDecision = "deny"
				policyReason = reason
				toolResult = ErrorResult(formatToolExecutionDenied(call.Name, reason)).WithError(errors.New(reason))
			}
		}

		if toolResult == nil && opts.Policy.Enabled && opts.Policy.TimeoutMS > 0 {
			policyTimeoutMS = opts.Policy.TimeoutMS
			timeoutCtx, cancel := context.WithTimeout(execCtx, time.Duration(opts.Policy.TimeoutMS)*time.Millisecond)
			defer cancel()
			execCtx = timeoutCtx
		}

		if toolResult == nil {
			if registry != nil {
				toolResult = registry.ExecuteWithContext(
					execCtx,
					call.Name,
					call.Arguments,
					opts.Channel,
					opts.ChatID,
					opts.SenderID,
					asyncCallback,
				)
			} else {
				toolResult = ErrorResult("No tools available")
			}
			if policyDecision == "" {
				policyDecision = "allow"
			}
		}

		if toolResult == nil {
			toolResult = ErrorResult(fmt.Sprintf("tool %q returned nil result", tc.Name)).
				WithError(fmt.Errorf("tool %q returned nil result", tc.Name))
		}

		if toolResult.IsError && opts.ErrorTemplate.Enabled && !shouldSkipErrorTemplate(toolResult.ForLLM) {
			applyToolErrorTemplate(registry, call, redactedArgsJSON, toolResult, opts)
		}

		// Hooks: allow post-processing / scrubbing of tool outputs (Phase N2).
		if len(hooks) > 0 {
			for _, h := range hooks {
				if h == nil {
					continue
				}
				updated, action := h.AfterToolCall(execCtx, call, toolResult, meta)
				if action != nil {
					a := *action
					a.Hook = strings.TrimSpace(h.Name())
					if strings.TrimSpace(a.Stage) == "" {
						a.Stage = "after"
					}
					hookActions = append(hookActions, a)
				}
				if updated != nil {
					toolResult = updated
				}
			}
		}

		auditResult := toolResult
		returnedResult := toolResult

		// Resource budget: cap result sizes to keep history/trace memory stable.
		if opts.MaxResultChars > 0 {
			truncateToolResult(auditResult, opts.MaxResultChars)
			truncateToolResult(returnedResult, opts.MaxResultChars)
		}

		duration := time.Since(start)
		if traceWriter != nil {
			traceWriter.RecordEnd(start.Add(duration), opts.Iteration, call, redactedArgsJSON, auditResult, duration, policyDecision, policyReason, policyTimeoutMS, idempotencyKey, hookActions)
		}

		// Append-only operational audit log (best-effort).
		if strings.TrimSpace(opts.Workspace) != "" {
			errText := ""
			if auditResult != nil && auditResult.Err != nil {
				errText = auditResult.Err.Error()
			}
			resultPreview := ""
			if auditResult != nil {
				resultPreview = utils.Truncate(strings.TrimSpace(auditResult.ForLLM), 400)
			}
			if errText == "" && auditResult != nil && auditResult.IsError {
				errText = resultPreview
			}
			auditlog.Record(opts.Workspace, auditlog.Event{
				Type: string(events.ToolExecuted),

				Source: strings.TrimSpace(scope),

				RunID:      strings.TrimSpace(opts.RunID),
				SessionKey: utils.CanonicalSessionKey(opts.SessionKey),
				Channel:    strings.TrimSpace(opts.Channel),
				ChatID:     strings.TrimSpace(opts.ChatID),
				SenderID:   strings.TrimSpace(opts.SenderID),
				Iteration:  opts.Iteration,

				Tool:       strings.TrimSpace(call.Name),
				ToolCallID: strings.TrimSpace(call.ID),

				PolicyDecision:  strings.TrimSpace(policyDecision),
				PolicyReason:    utils.Truncate(strings.TrimSpace(policyReason), 400),
				PolicyTimeoutMS: policyTimeoutMS,
				IdempotencyKey:  strings.TrimSpace(idempotencyKey),

				DurationMS:    duration.Milliseconds(),
				IsError:       auditResult != nil && auditResult.IsError,
				Error:         utils.Truncate(strings.TrimSpace(errText), 1200),
				ArgsPreview:   utils.Truncate(strings.TrimSpace(argsPreview), 500),
				ResultPreview: resultPreview,
			})
		}

		results[idx] = ToolCallExecution{
			ToolCall:   call,
			Result:     returnedResult,
			DurationMS: duration.Milliseconds(),
		}
	}

	runParallelBatch := func(batch []int) {
		if len(batch) == 0 {
			return
		}

		maxConc := opts.Parallel.MaxConcurrency
		if maxConc <= 0 || maxConc > len(batch) {
			maxConc = len(batch)
		}
		if maxConc <= 1 {
			for _, idx := range batch {
				runOne(idx)
			}
			return
		}

		logger.DebugCF(scope, "Executing parallel tool batch", map[string]any{
			"iteration":     opts.Iteration,
			"batch_size":    len(batch),
			"max_parallel":  maxConc,
			"parallel_mode": mode,
		})

		jobs := make(chan int)
		var wg sync.WaitGroup
		for i := 0; i < maxConc; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for idx := range jobs {
					runOne(idx)
				}
			}()
		}

		for _, idx := range batch {
			jobs <- idx
		}
		close(jobs)
		wg.Wait()
	}

	parallelBatch := make([]int, 0, len(toolCalls))
	flushParallelBatch := func() {
		if len(parallelBatch) == 0 {
			return
		}
		runParallelBatch(parallelBatch)
		parallelBatch = parallelBatch[:0]
	}

	for i, tc := range toolCalls {
		if shouldParallelize(tc) {
			parallelCount++
			parallelBatch = append(parallelBatch, i)
			continue
		}
		serialCount++
		flushParallelBatch()
		runOne(i)
	}
	flushParallelBatch()

	errorCount := 0
	durations := make([]int64, 0, len(results))
	for _, executed := range results {
		if executed.Result != nil && executed.Result.IsError {
			errorCount++
		}
		durations = append(durations, executed.DurationMS)
	}
	p50, p95, avg, max := summarizeDurations(durations)

	logger.InfoCF(scope, "Tool call batch summary", map[string]any{
		"iteration":                 opts.Iteration,
		"tool_parallel_enabled":     opts.Parallel.Enabled,
		"max_tool_concurrency":      opts.Parallel.MaxConcurrency,
		"parallel_tools_mode":       mode,
		"parallel_candidate_count":  parallelCount,
		"serial_count":              serialCount,
		"total":                     len(toolCalls),
		"error_count":               errorCount,
		"batch_duration_ms":         time.Since(batchStart).Milliseconds(),
		"tool_call_duration_p50_ms": p50,
		"tool_call_duration_p95_ms": p95,
		"tool_call_duration_avg_ms": avg,
		"tool_call_duration_max_ms": max,
	})

	return results
}

type estopState struct {
	KillAll            bool     `json:"kill_all,omitempty"`
	NetworkKill        bool     `json:"network_kill,omitempty"`
	FrozenTools        []string `json:"frozen_tools,omitempty"`
	FrozenToolPrefixes []string `json:"frozen_tool_prefixes,omitempty"`
}

func formatToolExecutionDenied(toolName, reason string) string {
	toolName = strings.TrimSpace(toolName)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "tool execution denied by runtime policy"
	}
	if toolName == "" {
		return fmt.Sprintf("TOOL_EXECUTION_DENIED: %s. Choose a different tool or ask for approval to change mode/policy.", reason)
	}
	return fmt.Sprintf("TOOL_EXECUTION_DENIED: tool %q was blocked: %s. Choose a different tool or ask for approval to change mode/policy.", toolName, reason)
}

func matchesToolRule(toolName string, exact []string, prefixes []string) bool {
	toolName = strings.ToLower(strings.TrimSpace(toolName))
	if toolName == "" {
		return false
	}
	for _, name := range exact {
		if toolName == strings.ToLower(strings.TrimSpace(name)) {
			return true
		}
	}
	for _, prefix := range prefixes {
		prefix = strings.ToLower(strings.TrimSpace(prefix))
		if prefix != "" && strings.HasPrefix(toolName, prefix) {
			return true
		}
	}
	return false
}

func evaluateToolPolicy(toolName string, policy config.ToolPolicyConfig, isResume bool) (bool, string) {
	if !policy.Enabled {
		return false, ""
	}
	if matchesToolRule(toolName, policy.Deny, policy.DenyPrefixes) {
		return true, "tool policy deny matched"
	}
	if len(policy.Allow) > 0 || len(policy.AllowPrefixes) > 0 {
		if !matchesToolRule(toolName, policy.Allow, policy.AllowPrefixes) {
			return true, "tool policy allowlist rejected this tool"
		}
	}
	if policy.Confirm.Enabled && confirmModeApplies(policy.Confirm.Mode, isResume) && matchesToolRule(toolName, policy.Confirm.Tools, policy.Confirm.ToolPrefixes) {
		return true, "tool policy confirmation required"
	}
	return false, ""
}

func confirmModeApplies(mode string, isResume bool) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "always":
		return true
	case "resume_only":
		return isResume
	case "never":
		return false
	default:
		return false
	}
}

func evaluateEstop(cfg config.EstopConfig, workspace, toolName string) (bool, string) {
	if !cfg.Enabled {
		return false, ""
	}

	state, err := loadEstopState(workspace)
	if err != nil {
		if cfg.FailClosed {
			return true, "estop state unreadable (fail_closed)"
		}
		return false, ""
	}
	if state == nil {
		return false, ""
	}
	if state.KillAll {
		return true, "estop kill_all is active"
	}
	if matchesToolRule(toolName, state.FrozenTools, state.FrozenToolPrefixes) {
		return true, "estop froze this tool"
	}
	if state.NetworkKill && isNetworkSensitiveTool(toolName) {
		return true, "estop network_kill is active"
	}
	return false, ""
}

func loadEstopState(workspace string) (*estopState, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil, nil
	}
	path := filepath.Join(workspace, ".x-claw", "state", "estop.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state estopState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func isNetworkSensitiveTool(toolName string) bool {
	toolName = strings.ToLower(strings.TrimSpace(toolName))
	return matchesToolRule(toolName, []string{"web", "web_fetch", "message"}, []string{"web_", "message"})
}

func truncateToolResult(result *ToolResult, maxChars int) {
	if result == nil || maxChars <= 0 {
		return
	}

	// Preserve tail diagnostics (stack traces, error summaries, etc). This mirrors
	// OpenClaw's production hardening: head-only truncation often drops the most
	// useful information at the end of outputs.
	tailMin := min(120, max(20, maxChars/4))
	if result.IsError {
		tailMin = min(200, max(20, maxChars/2))
	}
	if strings.TrimSpace(result.ForLLM) != "" {
		result.ForLLM = utils.TruncateHeadTail(result.ForLLM, maxChars, tailMin)
	}
	if strings.TrimSpace(result.ForUser) != "" {
		result.ForUser = utils.TruncateHeadTail(result.ForUser, maxChars, tailMin)
	}
}

func normalizeParallelMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", ParallelToolsModeReadOnlyOnly:
		return ParallelToolsModeReadOnlyOnly
	case ParallelToolsModeAll:
		return ParallelToolsModeAll
	default:
		return ""
	}
}

func getOverridePolicy(toolName string, overrides map[string]string) (ToolParallelPolicy, bool) {
	if len(overrides) == 0 {
		return "", false
	}
	raw, ok := overrides[toolName]
	if !ok {
		raw, ok = overrides[strings.ToLower(strings.TrimSpace(toolName))]
	}
	if !ok {
		return "", false
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(ToolParallelSerialOnly):
		return ToolParallelSerialOnly, true
	case string(ToolParallelReadOnly):
		return ToolParallelReadOnly, true
	default:
		return "", false
	}
}

func summarizeDurations(durations []int64) (p50, p95, avg, max int64) {
	if len(durations) == 0 {
		return 0, 0, 0, 0
	}

	sorted := append([]int64(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	total := int64(0)
	for _, d := range sorted {
		total += d
	}
	avg = total / int64(len(sorted))
	max = sorted[len(sorted)-1]
	p50 = percentileInt64(sorted, 0.50)
	p95 = percentileInt64(sorted, 0.95)
	return p50, p95, avg, max
}

func percentileInt64(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	// Nearest-rank percentile: rank = ceil(p*n), index = rank-1.
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// ToolErrorTemplateOptions controls optional tool error wrapping for the LLM.
//
// When enabled, tool execution failures (ToolResult.IsError=true) are transformed
// into a structured JSON payload with recovery hints, making the model more
// likely to self-correct by adjusting arguments or choosing a different tool.
type ToolErrorTemplateOptions struct {
	Enabled bool

	// IncludeSchema adds a small tool-schema summary (required + known keys) when available.
	IncludeSchema bool

	// IncludeAvailableTools attaches a truncated list of available tools when tool is not found.
	IncludeAvailableTools bool

	// MaxMessageChars limits the error message length inside the template.
	MaxMessageChars int
	// MaxArgsPreviewChars limits args_preview length inside the template.
	MaxArgsPreviewChars int
	// MaxHintCount limits number of hints emitted.
	MaxHintCount int
	// MaxAvailableTools limits length of available_tools list when included.
	MaxAvailableTools int
}

type toolErrorTemplate struct {
	Kind    string `json:"kind"`
	Version int    `json:"version"`

	Tool       string `json:"tool"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Iteration  int    `json:"iteration,omitempty"`

	Message     string `json:"message"`
	ArgsPreview string `json:"args_preview,omitempty"`

	Hints []string `json:"hints,omitempty"`

	ToolSchema     *toolSchemaSummary `json:"tool_schema,omitempty"`
	AvailableTools []string           `json:"available_tools,omitempty"`
	SuggestedTools []string           `json:"suggested_tools,omitempty"`
}

type toolSchemaSummary struct {
	Required []string `json:"required,omitempty"`
	Keys     []string `json:"keys,omitempty"`
}

func applyToolErrorTemplate(registry *ToolRegistry, tc providers.ToolCall, argsJSON []byte, result *ToolResult, opts ToolCallExecutionOptions) {
	if result == nil || !result.IsError {
		return
	}
	if !opts.ErrorTemplate.Enabled {
		return
	}

	// Avoid sending JSON blobs to humans. If a tool explicitly provided ForUser and
	// it's not suppressed, keep the original output so the user sees a friendly message.
	if strings.TrimSpace(result.ForUser) != "" && !result.Silent {
		return
	}

	cfg := opts.ErrorTemplate
	if cfg.MaxMessageChars <= 0 {
		cfg.MaxMessageChars = 900
	}
	if cfg.MaxArgsPreviewChars <= 0 {
		cfg.MaxArgsPreviewChars = 220
	}
	if cfg.MaxHintCount <= 0 {
		cfg.MaxHintCount = 6
	}
	if cfg.MaxAvailableTools <= 0 {
		cfg.MaxAvailableTools = 40
	}

	message := strings.TrimSpace(result.ForLLM)
	if message == "" && result.Err != nil {
		message = strings.TrimSpace(result.Err.Error())
	}
	if message == "" {
		message = "tool execution failed"
	}

	schemaSummary := (*toolSchemaSummary)(nil)
	availableTools := []string(nil)
	suggestedTools := []string(nil)

	toolExists := false
	var tool Tool
	if registry != nil {
		if t, ok := registry.Get(tc.Name); ok && t != nil {
			tool = t
			toolExists = true
		}
	}

	if !toolExists && registry != nil {
		all := registry.List()
		suggestedTools = suggestSimilarToolNames(tc.Name, all, 5)
		if cfg.IncludeAvailableTools {
			if len(all) > cfg.MaxAvailableTools {
				all = all[:cfg.MaxAvailableTools]
			}
			availableTools = all
		}
	}

	if cfg.IncludeSchema && toolExists {
		schemaSummary = summarizeToolSchema(tool)
	}

	hints := buildToolErrorHints(tc.Name, toolExists, schemaSummary, suggestedTools)
	if len(hints) > cfg.MaxHintCount {
		hints = hints[:cfg.MaxHintCount]
	}

	payload := toolErrorTemplate{
		Kind:    "tool_error",
		Version: 1,

		Tool:       tc.Name,
		ToolCallID: tc.ID,
		Iteration:  opts.Iteration,

		Message:     utils.Truncate(message, cfg.MaxMessageChars),
		ArgsPreview: utils.Truncate(string(argsJSON), cfg.MaxArgsPreviewChars),

		Hints: hints,

		ToolSchema:     schemaSummary,
		AvailableTools: availableTools,
		SuggestedTools: suggestedTools,
	}

	encoded, err := marshalNoEscape(payload)
	if err != nil {
		return
	}
	result.ForLLM = string(encoded)
}

func buildToolErrorHints(toolName string, toolExists bool, schema *toolSchemaSummary, suggested []string) []string {
	name := strings.ToLower(strings.TrimSpace(toolName))
	hints := make([]string, 0, 8)

	if !toolExists {
		hints = append(hints, "Tool not found. Check spelling and choose an available tool name.")
		if len(suggested) > 0 {
			hints = append(hints, "Try one of suggested_tools (closest match).")
		}
		return hints
	}

	if schema != nil && len(schema.Required) > 0 {
		hints = append(hints, "Ensure required arguments are present: "+strings.Join(schema.Required, ", "))
	}
	hints = append(hints, "Double-check argument keys/types match the tool schema exactly.")

	switch name {
	case "read_file", "list_dir":
		hints = append(hints, "If a path fails, verify with list_dir on the parent directory first.")
	case "write_file", "edit_file", "append_file":
		hints = append(hints, "Prefer read_file before write/edit, and keep edits minimal and precise.")
	case "run_command":
		hints = append(hints, "Keep commands non-interactive; if it needs files, prefer read_file/list_dir.")
	case "web_search":
		hints = append(hints, "Try a more specific query; if results are noisy, refine keywords.")
	case "web_fetch":
		hints = append(hints, "Fetch a single URL; avoid very large pages and consider narrower sources.")
	case "cron":
		hints = append(hints, "For one-time use at_seconds; for recurring use every_seconds or cron_expr (not both).")
	case "sessions_list":
		hints = append(hints, "Use sessions_list to discover valid session keys before querying history.")
	case "sessions_history":
		hints = append(hints, "If session not found, call sessions_list and retry with an existing key.")
	case "skills_search":
		hints = append(hints, "Search first; then install by exact skill name/version from the results.")
	case "skills_install":
		hints = append(hints, "If installation fails, verify registry config/network and retry with a smaller set.")
	}

	return hints
}

func summarizeToolSchema(tool Tool) *toolSchemaSummary {
	if tool == nil {
		return nil
	}
	params := tool.Parameters()
	if params == nil {
		return nil
	}

	required := extractStringSlice(params["required"])
	keys := make([]string, 0)
	if props, ok := params["properties"].(map[string]any); ok && len(props) > 0 {
		keys = make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
	}
	sort.Strings(required)

	if len(required) == 0 && len(keys) == 0 {
		return nil
	}
	return &toolSchemaSummary{
		Required: required,
		Keys:     keys,
	}
}

func extractStringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		out := make([]string, 0, len(t))
		for _, s := range t {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(t))
		for _, raw := range t {
			if s, ok := raw.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func suggestSimilarToolNames(name string, toolNames []string, limit int) []string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || limit <= 0 || len(toolNames) == 0 {
		return nil
	}

	type scored struct {
		name  string
		score int
	}
	scoredList := make([]scored, 0, len(toolNames))
	for _, cand := range toolNames {
		c := strings.ToLower(strings.TrimSpace(cand))
		if c == "" {
			continue
		}
		// Lower score is better.
		d := levenshteinDistance(name, c)
		// Prefer substring matches slightly.
		if strings.Contains(c, name) || strings.Contains(name, c) {
			d -= 2
		}
		scoredList = append(scoredList, scored{name: cand, score: d})
	}

	sort.SliceStable(scoredList, func(i, j int) bool {
		if scoredList[i].score == scoredList[j].score {
			return scoredList[i].name < scoredList[j].name
		}
		return scoredList[i].score < scoredList[j].score
	})

	if len(scoredList) > limit {
		scoredList = scoredList[:limit]
	}
	out := make([]string, 0, len(scoredList))
	for _, s := range scoredList {
		out = append(out, s.name)
	}
	return out
}

func levenshteinDistance(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return len([]rune(b))
	}
	if b == "" {
		return len([]rune(a))
	}

	ar := []rune(a)
	br := []rune(b)
	la := len(ar)
	lb := len(br)

	// Use two-row DP to reduce allocations.
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)

	for j := 0; j <= lb; j++ {
		prev[j] = j
	}

	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 0
			if ar[i-1] != br[j-1] {
				cost = 1
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			cur[j] = min3(del, ins, sub)
		}
		prev, cur = cur, prev
	}

	return prev[lb]
}

func min3(a, b, c int) int {
	if a <= b && a <= c {
		return a
	}
	if b <= a && b <= c {
		return b
	}
	return c
}

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func shouldSkipErrorTemplate(content string) bool {
	content = strings.TrimSpace(content)
	if content == "" {
		return false
	}
	return strings.Contains(content, `"kind":"tool_error"`)
}

// ToolHookContext carries metadata about the current tool call execution.
// It is intentionally small and stable; do not add large blobs or raw tool args.
type ToolHookContext struct {
	Workspace  string
	SessionKey string
	RunID      string

	Channel  string
	ChatID   string
	SenderID string

	Iteration int
	IsResume  bool
	PlanMode  bool

	PolicyTags map[string]string
}

type ToolHookAction struct {
	// Hook is the short name of the hook that produced this action.
	Hook string `json:"hook,omitempty"`
	// Stage indicates where the action occurred: "before" or "after".
	Stage string `json:"stage,omitempty"`
	// Decision is a short machine-friendly label (e.g. "deny", "rewrite", "scrub").
	Decision string `json:"decision,omitempty"`
	// Reason is an optional human-readable explanation (best-effort; may be truncated).
	Reason string `json:"reason,omitempty"`
}

// ToolHook provides an extension point around tool execution.
//
// Hooks run inside the executor chokepoint so they apply to built-in tools and MCP tools.
// They MUST be safe-by-default: no network, no filesystem writes beyond the workspace.
//
// Semantics:
// - BeforeToolCall may rewrite the tool call (name/args) or return a non-nil ToolResult to short-circuit execution.
// - AfterToolCall may scrub/transform the returned ToolResult.
type ToolHook interface {
	Name() string

	BeforeToolCall(ctx context.Context, call providers.ToolCall, meta ToolHookContext) (providers.ToolCall, *ToolResult, *ToolHookAction)
	AfterToolCall(ctx context.Context, call providers.ToolCall, result *ToolResult, meta ToolHookContext) (*ToolResult, *ToolHookAction)
}

// BuildDefaultToolHooks returns the built-in hook chain for this configuration.
// It is safe to call with a nil config.
func BuildDefaultToolHooks(cfg *config.Config) []ToolHook {
	if cfg == nil {
		return nil
	}
	if !cfg.Tools.Hooks.Enabled {
		return nil
	}

	hooks := make([]ToolHook, 0, 2)

	if cfg.Tools.Hooks.Redact.Enabled {
		if hook := NewToolResultRedactHook(cfg.Tools.Hooks.Redact); hook != nil {
			hooks = append(hooks, hook)
		}
	}

	return hooks
}

type toolResultRedactor struct {
	enabled     bool
	applyToLLM  bool
	applyToUser bool

	fields   map[string]struct{}
	patterns []*regexp.Regexp
}

func newToolResultRedactor(cfg config.ToolPolicyRedactConfig) *toolResultRedactor {
	r := &toolResultRedactor{
		enabled:     cfg.Enabled,
		applyToLLM:  cfg.ApplyToLLM,
		applyToUser: cfg.ApplyToUser,
		fields:      map[string]struct{}{},
		patterns:    compileRedactionRegexes(cfg.Patterns),
	}
	for _, f := range cfg.JSONFields {
		f = strings.ToLower(strings.TrimSpace(f))
		if f != "" {
			r.fields[f] = struct{}{}
		}
	}
	return r
}

func (r *toolResultRedactor) redactText(s string) string {
	if r == nil || !r.enabled {
		return s
	}
	out := s
	for _, re := range r.patterns {
		if re == nil {
			continue
		}
		repl := "[REDACTED]"
		switch re.NumSubexp() {
		case 0:
			repl = "[REDACTED]"
		case 1:
			repl = "${1}[REDACTED]"
		default:
			repl = "${1}${2}[REDACTED]"
		}
		out = re.ReplaceAllString(out, repl)
	}
	return out
}

func (r *toolResultRedactor) redactJSONValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			kn := strings.ToLower(strings.TrimSpace(k))
			if _, ok := r.fields[kn]; ok {
				out[k] = "[REDACTED]"
				continue
			}
			out[k] = r.redactJSONValue(vv)
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, vv := range t {
			out = append(out, r.redactJSONValue(vv))
		}
		return out
	case string:
		return r.redactText(t)
	default:
		return v
	}
}

func (r *toolResultRedactor) redactOutputString(s string) string {
	if r == nil || !r.enabled {
		return s
	}
	raw := strings.TrimSpace(s)
	if raw == "" {
		return s
	}

	// Best-effort structured redaction for JSON strings.
	if (strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[")) && len(r.fields) > 0 {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err == nil {
			v = r.redactJSONValue(v)
			if out, err := json.Marshal(v); err == nil && len(out) > 0 {
				raw = string(out)
			}
		}
	}

	return r.redactText(raw)
}

func (r *toolResultRedactor) redactToolResult(result *ToolResult) (out *ToolResult, changed bool) {
	if r == nil || !r.enabled || result == nil {
		return result, false
	}
	if !r.applyToLLM && !r.applyToUser {
		return result, false
	}

	clone := *result
	clone.Err = result.Err

	if r.applyToLLM && strings.TrimSpace(clone.ForLLM) != "" {
		clone.ForLLM = r.redactOutputString(clone.ForLLM)
	}
	if r.applyToUser && strings.TrimSpace(clone.ForUser) != "" {
		clone.ForUser = r.redactOutputString(clone.ForUser)
	}

	changed = clone.ForLLM != result.ForLLM || clone.ForUser != result.ForUser
	if !changed {
		return result, false
	}
	return &clone, true
}

// ToolResultRedactHook applies best-effort redaction to tool outputs.
// It is intentionally separate from tools.policy so users can enable redaction
// without turning on full policy gating.
type ToolResultRedactHook struct {
	redactor *toolResultRedactor
}

func NewToolResultRedactHook(cfg config.ToolPolicyRedactConfig) *ToolResultRedactHook {
	r := newToolResultRedactor(cfg)
	if r == nil || !r.enabled {
		return nil
	}
	if !r.applyToLLM && !r.applyToUser {
		return nil
	}
	return &ToolResultRedactHook{redactor: r}
}

func (h *ToolResultRedactHook) Name() string {
	return "redact"
}

func (h *ToolResultRedactHook) BeforeToolCall(_ context.Context, call providers.ToolCall, _ ToolHookContext) (providers.ToolCall, *ToolResult, *ToolHookAction) {
	return call, nil, nil
}

func (h *ToolResultRedactHook) AfterToolCall(_ context.Context, call providers.ToolCall, result *ToolResult, _ ToolHookContext) (*ToolResult, *ToolHookAction) {
	_ = call
	if h == nil || h.redactor == nil || result == nil {
		return result, nil
	}
	redacted, changed := h.redactor.redactToolResult(result)
	if !changed {
		return result, nil
	}
	return redacted, &ToolHookAction{Decision: "scrub", Reason: "tool output redacted"}
}

func compileRedactionRegexes(patterns []string) []*regexp.Regexp {
	if len(patterns) == 0 {
		return nil
	}
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, raw := range patterns {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		re, err := regexp.Compile(raw)
		if err != nil {
			continue
		}
		out = append(out, re)
	}
	return out
}
