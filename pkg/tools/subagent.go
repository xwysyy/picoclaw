package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type SubagentTask struct {
	ID            string
	ParentTaskID  string
	Task          string
	Label         string
	AgentID       string
	SessionKey    string
	RunID         string
	IsResume      bool
	OriginChannel string
	OriginChatID  string
	Status        string
	Result        string
	Created       int64
	Depth         int
}

type SubagentToolCallArtifact struct {
	Iteration  int    `json:"iteration,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

type SubagentArtifacts struct {
	ToolsUsed       []string                   `json:"tools_used,omitempty"`
	Files           []string                   `json:"files,omitempty"`
	ToolCalls       []SubagentToolCallArtifact `json:"tool_calls,omitempty"`
	Errors          int                        `json:"errors,omitempty"`
	DurationMSTotal int64                      `json:"duration_ms_total,omitempty"`
	EvidenceDropped int                        `json:"evidence_dropped,omitempty"`
}

type SubagentHandoffSuggestion struct {
	AgentID    string `json:"agent_id,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Takeover   bool   `json:"takeover,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// SubagentResultPayload is the stable JSON contract returned by subagent/spawn results
// and used for system announcements.
type SubagentResultPayload struct {
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Mode   string `json:"mode,omitempty"`

	TaskID       string `json:"task_id,omitempty"`
	ParentTaskID string `json:"parent_task_id,omitempty"`
	Label        string `json:"label,omitempty"`
	Task         string `json:"task,omitempty"`
	AgentID      string `json:"agent_id,omitempty"`

	SessionKey string `json:"session_key,omitempty"`
	RunID      string `json:"run_id,omitempty"`

	OriginChannel string `json:"origin_channel,omitempty"`
	OriginChatID  string `json:"origin_chat_id,omitempty"`

	Depth      int `json:"depth,omitempty"`
	Iterations int `json:"iterations,omitempty"`

	Summary string `json:"summary,omitempty"`
	Error   string `json:"error,omitempty"`

	HandoffSuggestions []SubagentHandoffSuggestion `json:"handoff_suggestions,omitempty"`
	Artifacts          SubagentArtifacts           `json:"artifacts,omitempty"`
	Warnings           []string                    `json:"warnings,omitempty"`
}

type SubagentExecutionConfig struct {
	Provider providers.LLMProvider
	Model    string
	Tools    *ToolRegistry
}

type SubagentExecutionResolver func(targetAgentID string) (SubagentExecutionConfig, error)

type SubagentTaskEventType string

const (
	SubagentTaskCreated   SubagentTaskEventType = "created"
	SubagentTaskRunning   SubagentTaskEventType = "running"
	SubagentTaskCompleted SubagentTaskEventType = "completed"
	SubagentTaskFailed    SubagentTaskEventType = "failed"
	SubagentTaskCancelled SubagentTaskEventType = "cancelled"
)

type SubagentTaskEvent struct {
	Type      SubagentTaskEventType
	Task      SubagentTask
	Trace     []ToolExecutionTrace
	Err       string
	Timestamp int64
}

type SubagentTaskEventHandler func(event SubagentTaskEvent)

type SubagentManager struct {
	tasks          map[string]*SubagentTask
	mu             sync.RWMutex
	provider       providers.LLMProvider
	defaultModel   string
	bus            *bus.MessageBus
	workspace      string
	tools          *ToolRegistry
	maxIterations  int
	maxTokens      int
	temperature    float64
	hasMaxTokens   bool
	hasTemperature bool
	nextID         int
	resolver       SubagentExecutionResolver
	eventHandler   SubagentTaskEventHandler
	maxConcurrent  int
	maxTasks       int
	maxDepth       int
	taskCancels    map[string]context.CancelFunc

	toolCallsParallelEnabled bool
	maxToolCallConcurrency   int
	parallelToolsMode        string
	toolPolicyOverrides      map[string]string

	toolPolicy        config.ToolPolicyConfig
	toolPolicyTags    map[string]string
	toolTrace         ToolTraceOptions
	toolErrorTemplate ToolErrorTemplateOptions
	toolHooks         []ToolHook

	// Resource budgets (soft limits).
	maxToolCallsPerRun int
	maxWallTimeSeconds int
	maxToolResultChars int
}

func NewSubagentManager(
	provider providers.LLMProvider,
	defaultModel, workspace string,
	bus *bus.MessageBus,
) *SubagentManager {
	return &SubagentManager{
		tasks:         make(map[string]*SubagentTask),
		provider:      provider,
		defaultModel:  defaultModel,
		bus:           bus,
		workspace:     workspace,
		tools:         NewToolRegistry(),
		maxIterations: 10,
		nextID:        1,
		taskCancels:   make(map[string]context.CancelFunc),
	}
}

// SetLLMOptions sets max tokens and temperature for subagent LLM calls.
func (sm *SubagentManager) SetLLMOptions(maxTokens int, temperature float64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.maxTokens = maxTokens
	sm.hasMaxTokens = true
	sm.temperature = temperature
	sm.hasTemperature = true
}

// SetTools sets the tool registry for subagent execution.
// If not set, subagent will have access to the provided tools.
func (sm *SubagentManager) SetTools(tools *ToolRegistry) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.tools = tools
}

// RegisterTool registers a tool for subagent execution.
func (sm *SubagentManager) RegisterTool(tool Tool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.tools.Register(tool)
}

// SetExecutionResolver sets a resolver used to pick provider/model/tools for each task.
// If not set, the manager falls back to its default provider/model/tools.
func (sm *SubagentManager) SetExecutionResolver(resolver SubagentExecutionResolver) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.resolver = resolver
}

// SetEventHandler sets a callback that receives task lifecycle events.
func (sm *SubagentManager) SetEventHandler(handler SubagentTaskEventHandler) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.eventHandler = handler
}

// SetLimits sets orchestration limits for this manager.
// maxConcurrent <= 0 means unlimited.
// maxTasks <= 0 means unlimited.
// maxDepth <= 0 means unlimited.
func (sm *SubagentManager) SetLimits(maxConcurrent, maxTasks, maxDepth int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.maxConcurrent = maxConcurrent
	sm.maxTasks = maxTasks
	sm.maxDepth = maxDepth
}

// SetResourceBudgets configures soft resource limits for subagent runs.
// Passing a disabled limits config clears all budgets.
func (sm *SubagentManager) SetResourceBudgets(limits config.LimitsConfig) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !limits.Enabled {
		sm.maxToolCallsPerRun = 0
		sm.maxWallTimeSeconds = 0
		sm.maxToolResultChars = 0
		return
	}

	sm.maxToolCallsPerRun = limits.MaxToolCallsPerRun
	sm.maxWallTimeSeconds = limits.MaxRunWallTimeSeconds
	sm.maxToolResultChars = limits.MaxToolResultChars
}

// SetToolCallParallelism configures in-batch parallel tool execution for
// subagent tool loops.
func (sm *SubagentManager) SetToolCallParallelism(
	enabled bool,
	maxConcurrency int,
	mode string,
	toolPolicyOverrides map[string]string,
) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.toolCallsParallelEnabled = enabled
	sm.maxToolCallConcurrency = maxConcurrency
	sm.parallelToolsMode = mode
	sm.toolPolicyOverrides = clonePolicyOverrides(toolPolicyOverrides)
}

// SetToolExecutionPolicy configures the centralized tool policy for subagent tool loops.
// This should match the main agent tool executor policy so all tool sources behave consistently.
func (sm *SubagentManager) SetToolExecutionPolicy(policy config.ToolPolicyConfig, tags map[string]string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.toolPolicy = policy
	sm.toolPolicyTags = copyStringMap(tags)
}

// SetToolExecutionTracing configures tool trace + error template behavior for subagent tool loops.
func (sm *SubagentManager) SetToolExecutionTracing(trace ToolTraceOptions, errorTemplate ToolErrorTemplateOptions) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.toolTrace = trace
	sm.toolErrorTemplate = errorTemplate
}

// SetToolHooks configures the tool hook chain applied to all tool executions
// performed by subagents (Phase N2).
func (sm *SubagentManager) SetToolHooks(hooks []ToolHook) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.toolHooks = hooks
}

func clonePolicyOverrides(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

const (
	defaultSubagentMaxSummaryChars = 12000
	defaultSubagentMaxTaskChars    = 2000

	defaultSubagentMaxEvidenceItems = 40
	defaultSubagentMaxArtifactFiles = 60
)

func truncateText(value string, maxChars int) string {
	value = strings.TrimSpace(value)
	if maxChars <= 0 || len(value) <= maxChars {
		return value
	}
	if maxChars <= 3 {
		return value[:maxChars]
	}
	return value[:maxChars] + "..."
}

func buildSubagentArtifacts(trace []ToolExecutionTrace) SubagentArtifacts {
	toolSet := make(map[string]struct{})
	fileSet := make(map[string]struct{})

	artifacts := SubagentArtifacts{
		ToolCalls: make([]SubagentToolCallArtifact, 0, min(len(trace), defaultSubagentMaxEvidenceItems)),
	}

	for _, tr := range trace {
		name := strings.TrimSpace(tr.ToolName)
		if name != "" {
			toolSet[name] = struct{}{}
		}

		if tr.IsError {
			artifacts.Errors++
		}
		if tr.DurationMS > 0 {
			artifacts.DurationMSTotal += tr.DurationMS
		}

		// Best-effort artifact extraction: common "path" argument for file tools.
		if tr.Arguments != nil {
			if raw, ok := tr.Arguments["path"].(string); ok {
				path := strings.TrimSpace(raw)
				if path != "" {
					fileSet[path] = struct{}{}
				}
			}
		}

		if len(artifacts.ToolCalls) < defaultSubagentMaxEvidenceItems {
			artifacts.ToolCalls = append(artifacts.ToolCalls, SubagentToolCallArtifact{
				Iteration:  tr.Iteration,
				ToolName:   name,
				ToolCallID: strings.TrimSpace(tr.ToolCallID),
				IsError:    tr.IsError,
				DurationMS: tr.DurationMS,
			})
		} else {
			artifacts.EvidenceDropped++
		}
	}

	if len(toolSet) > 0 {
		toolsUsed := make([]string, 0, len(toolSet))
		for name := range toolSet {
			toolsUsed = append(toolsUsed, name)
		}
		sort.Strings(toolsUsed)
		artifacts.ToolsUsed = toolsUsed
	}

	if len(fileSet) > 0 {
		files := make([]string, 0, len(fileSet))
		for path := range fileSet {
			files = append(files, path)
		}
		sort.Strings(files)
		if len(files) > defaultSubagentMaxArtifactFiles {
			files = files[:defaultSubagentMaxArtifactFiles]
		}
		artifacts.Files = files
	}

	return artifacts
}

func extractSubagentHandoffSuggestions(trace []ToolExecutionTrace) ([]SubagentHandoffSuggestion, []string) {
	if len(trace) == 0 {
		return nil, nil
	}

	out := make([]SubagentHandoffSuggestion, 0, 1)
	warnings := []string(nil)

	for _, tr := range trace {
		if !strings.EqualFold(strings.TrimSpace(tr.ToolName), "handoff") {
			continue
		}
		raw := strings.TrimSpace(tr.Result)
		if raw == "" || !strings.HasPrefix(raw, "{") {
			continue
		}

		var payload struct {
			Kind              string                     `json:"kind"`
			HandoffSuggestion *SubagentHandoffSuggestion `json:"handoff_suggestion,omitempty"`
			Suggestion        *SubagentHandoffSuggestion `json:"suggestion,omitempty"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			continue
		}
		if payload.HandoffSuggestion == nil {
			payload.HandoffSuggestion = payload.Suggestion
		}
		if payload.HandoffSuggestion == nil {
			continue
		}

		s := *payload.HandoffSuggestion
		if strings.TrimSpace(s.ToolCallID) == "" {
			s.ToolCallID = strings.TrimSpace(tr.ToolCallID)
		}
		s.AgentID = strings.TrimSpace(s.AgentID)
		s.Reason = strings.TrimSpace(s.Reason)
		if s.AgentID == "" && s.Reason == "" && !s.Takeover {
			warnings = append(warnings, "subagent handoff suggestion is empty (ignored)")
			continue
		}
		out = append(out, s)
	}

	if len(out) == 0 {
		return nil, warnings
	}
	return out, warnings
}

func marshalSubagentPayload(payload SubagentResultPayload) (string, error) {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (sm *SubagentManager) Spawn(
	ctx context.Context,
	task, label, agentID, originChannel, originChatID string,
	callback AsyncCallback,
) (string, error) {
	subagentTask, err := sm.SpawnTask(ctx, task, label, agentID, originChannel, originChatID, callback)
	if err != nil {
		return "", err
	}

	if label != "" {
		return fmt.Sprintf("Spawned subagent '%s' for task: %s (id: %s)", label, task, subagentTask.ID), nil
	}
	return fmt.Sprintf("Spawned subagent for task: %s (id: %s)", task, subagentTask.ID), nil
}

// SpawnTask starts a background subagent task and returns an immutable snapshot of the created task.
func (sm *SubagentManager) SpawnTask(
	ctx context.Context,
	task, label, agentID, originChannel, originChatID string,
	callback AsyncCallback,
) (*SubagentTask, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.maxTasks > 0 {
		active := 0
		for _, item := range sm.tasks {
			if !isTerminalTaskStatus(item.Status) {
				active++
			}
		}
		if active >= sm.maxTasks {
			return nil, fmt.Errorf("max task limit reached (%d)", sm.maxTasks)
		}
	}
	if sm.maxConcurrent > 0 {
		running := 0
		for _, item := range sm.tasks {
			if item.Status == "running" {
				running++
			}
		}
		if running >= sm.maxConcurrent {
			return nil, fmt.Errorf("max concurrent task limit reached (%d)", sm.maxConcurrent)
		}
	}

	depth := 1
	parentTaskID := ""
	senderID := toolExecutionSenderID(ctx)
	if strings.HasPrefix(strings.ToLower(senderID), "subagent:") {
		parentID := strings.TrimSpace(senderID[len("subagent:"):])
		parentTaskID = parentID
		if parent, ok := sm.tasks[parentID]; ok && parent.Depth > 0 {
			depth = parent.Depth + 1
		} else {
			depth = 2
		}
	}
	if sm.maxDepth > 0 && depth > sm.maxDepth {
		return nil, fmt.Errorf("max spawn depth reached (%d)", sm.maxDepth)
	}

	taskID := fmt.Sprintf("subagent-%d", sm.nextID)
	sm.nextID++

	sessionKey := toolExecutionSessionKey(ctx)
	runID := toolExecutionRunID(ctx)
	isResume := toolExecutionIsResume(ctx)

	subagentTask := &SubagentTask{
		ID:            taskID,
		ParentTaskID:  parentTaskID,
		Task:          task,
		Label:         label,
		AgentID:       agentID,
		SessionKey:    sessionKey,
		RunID:         runID,
		IsResume:      isResume,
		OriginChannel: originChannel,
		OriginChatID:  originChatID,
		Status:        "running",
		Created:       time.Now().UnixMilli(),
		Depth:         depth,
	}
	sm.tasks[taskID] = subagentTask
	taskCtx, cancel := context.WithCancel(ctx)
	sm.taskCancels[taskID] = cancel
	snapshot := *subagentTask

	// Start task in background with context cancellation support.
	go sm.runTask(taskCtx, subagentTask, callback)
	go sm.emitEvent(SubagentTaskEvent{
		Type:      SubagentTaskCreated,
		Task:      snapshot,
		Timestamp: time.Now().UnixMilli(),
	})
	return &snapshot, nil
}

func (sm *SubagentManager) runTask(ctx context.Context, task *SubagentTask, callback AsyncCallback) {
	defer sm.clearTaskCancel(task.ID)

	task.Created = time.Now().UnixMilli()
	sm.updateTaskAndEmit(task, SubagentTaskRunning, "running", task.Result, "")

	// Build system prompt for subagent
	systemPrompt := fmt.Sprintf(`You are a subagent working under the picoclaw system.

Complete the given task independently and report the result.

Guidelines:
1. Use available tools as needed. Check tool results before proceeding.
2. If a tool fails, try an alternative approach. Do NOT repeat the same failed call.
3. If you cannot complete the task, explain what went wrong clearly.
4. Do NOT fabricate results. Only report what tools actually returned.
5. Keep your final response concise and actionable.

Output format — provide a clear summary with:
- What was done (actions taken)
- What was found (key results or data)
- Any issues encountered

Working directory: %s`, sm.workspace)

	messages := []providers.Message{
		{
			Role:    "system",
			Content: systemPrompt,
		},
		{
			Role:    "user",
			Content: task.Task,
		},
	}

	// Check if context is already canceled before starting
	select {
	case <-ctx.Done():
		sm.updateTaskAndEmit(task, SubagentTaskCancelled, "cancelled", "Task cancelled before execution", "")
		sm.cancelTaskTree(task.ID)
		return
	default:
	}

	// Run tool loop with access to tools
	sm.mu.RLock()
	maxIter := sm.maxIterations
	maxTokens := sm.maxTokens
	temperature := sm.temperature
	hasMaxTokens := sm.hasMaxTokens
	hasTemperature := sm.hasTemperature
	resolver := sm.resolver
	defaultTools := sm.tools
	defaultProvider := sm.provider
	defaultModel := sm.defaultModel
	toolCallsParallelEnabled := sm.toolCallsParallelEnabled
	maxToolCallConcurrency := sm.maxToolCallConcurrency
	parallelToolsMode := sm.parallelToolsMode
	toolPolicyOverrides := clonePolicyOverrides(sm.toolPolicyOverrides)
	policyCfg := sm.toolPolicy
	policyTags := copyStringMap(sm.toolPolicyTags)
	traceOpts := sm.toolTrace
	errorTemplateOpts := sm.toolErrorTemplate
	toolHooks := sm.toolHooks
	if len(toolHooks) > 0 {
		toolHooks = append([]ToolHook(nil), toolHooks...)
	}
	maxToolCallsPerRun := sm.maxToolCallsPerRun
	maxWallTimeSeconds := sm.maxWallTimeSeconds
	maxToolResultChars := sm.maxToolResultChars
	sm.mu.RUnlock()

	execution := SubagentExecutionConfig{
		Provider: defaultProvider,
		Model:    defaultModel,
		Tools:    defaultTools,
	}
	if resolver != nil {
		resolved, err := resolver(task.AgentID)
		if err != nil {
			summary := truncateText(fmt.Sprintf("Error: %v", err), defaultSubagentMaxSummaryChars)
			snapshot := sm.updateTaskAndEmit(task, SubagentTaskFailed, "failed", summary, err.Error())

			payload := SubagentResultPayload{
				Kind:   "subagent_result",
				Status: "failed",
				Mode:   "async",

				TaskID:       snapshot.ID,
				ParentTaskID: snapshot.ParentTaskID,
				Label:        snapshot.Label,
				Task:         truncateText(snapshot.Task, defaultSubagentMaxTaskChars),
				AgentID:      snapshot.AgentID,

				SessionKey: snapshot.SessionKey,
				RunID:      snapshot.RunID,

				OriginChannel: snapshot.OriginChannel,
				OriginChatID:  snapshot.OriginChatID,
				Depth:         snapshot.Depth,

				Summary:   summary,
				Error:     truncateText(err.Error(), 1200),
				Artifacts: buildSubagentArtifacts(nil),
			}

			payloadJSON, marshalErr := marshalSubagentPayload(payload)
			if marshalErr != nil {
				payloadJSON = summary
			}

			if callback != nil {
				callback(ctx, &ToolResult{
					ForLLM:  payloadJSON,
					ForUser: summary,
					Silent:  true,
					IsError: true,
					Async:   true,
					Err:     err,
				})
			}
			if sm.bus != nil {
				sm.publishTaskAnnouncement(snapshot, task.Status, payloadJSON)
			}
			sm.cancelTaskTree(task.ID)
			return
		}
		if resolved.Provider != nil {
			execution.Provider = resolved.Provider
		}
		if resolved.Model != "" {
			execution.Model = resolved.Model
		}
		if resolved.Tools != nil {
			execution.Tools = resolved.Tools
		}
	}
	if execution.Tools == nil {
		execution.Tools = NewToolRegistry()
	}

	var llmOptions map[string]any
	if hasMaxTokens || hasTemperature {
		llmOptions = map[string]any{}
		if hasMaxTokens {
			llmOptions["max_tokens"] = maxTokens
		}
		if hasTemperature {
			llmOptions["temperature"] = temperature
		}
	}

	loopResult, err := RunToolLoop(ctx, ToolLoopConfig{
		Provider:                 execution.Provider,
		Model:                    execution.Model,
		Tools:                    execution.Tools,
		MaxIterations:            maxIter,
		MaxToolCallsPerRun:       maxToolCallsPerRun,
		MaxWallTimeSeconds:       maxWallTimeSeconds,
		MaxToolResultChars:       maxToolResultChars,
		LLMOptions:               llmOptions,
		SenderID:                 fmt.Sprintf("subagent:%s", task.ID),
		Workspace:                sm.workspace,
		SessionKey:               task.SessionKey,
		RunID:                    task.RunID,
		IsResume:                 task.IsResume,
		Policy:                   policyCfg,
		PolicyTags:               policyTags,
		Trace:                    traceOpts,
		ErrorTemplate:            errorTemplateOpts,
		Hooks:                    toolHooks,
		ToolCallsParallelEnabled: toolCallsParallelEnabled,
		MaxToolCallConcurrency:   maxToolCallConcurrency,
		ParallelToolsMode:        parallelToolsMode,
		ToolPolicyOverrides:      toolPolicyOverrides,
	}, messages, task.OriginChannel, task.OriginChatID)

	var result *ToolResult
	var snapshot SubagentTask
	var trace []ToolExecutionTrace
	iterations := 0
	if loopResult != nil {
		trace = loopResult.Trace
		iterations = loopResult.Iterations
	}

	handoffSuggestions, handoffWarnings := extractSubagentHandoffSuggestions(trace)

	if err != nil {
		status, eventType := "failed", SubagentTaskFailed
		errResult := fmt.Sprintf("Error: %v", err)
		errText := err.Error()
		if ctx.Err() != nil {
			status, eventType = "cancelled", SubagentTaskCancelled
			errResult = "Task cancelled during execution"
			errText = strings.TrimSpace(errResult)
		}
		summary := truncateText(errResult, defaultSubagentMaxSummaryChars)
		snapshot = sm.updateTaskAndEmit(task, eventType, status, summary, errText, trace)

		payload := SubagentResultPayload{
			Kind:   "subagent_result",
			Status: status,
			Mode:   "async",

			TaskID:       snapshot.ID,
			ParentTaskID: snapshot.ParentTaskID,
			Label:        snapshot.Label,
			Task:         truncateText(snapshot.Task, defaultSubagentMaxTaskChars),
			AgentID:      snapshot.AgentID,

			SessionKey: snapshot.SessionKey,
			RunID:      snapshot.RunID,

			OriginChannel: snapshot.OriginChannel,
			OriginChatID:  snapshot.OriginChatID,
			Depth:         snapshot.Depth,
			Iterations:    iterations,

			Summary:            summary,
			Error:              truncateText(errText, 1200),
			HandoffSuggestions: handoffSuggestions,
			Artifacts:          buildSubagentArtifacts(trace),
			Warnings:           handoffWarnings,
		}
		payloadJSON, marshalErr := marshalSubagentPayload(payload)
		if marshalErr != nil {
			payloadJSON = summary
		}

		result = &ToolResult{
			ForLLM:  payloadJSON,
			ForUser: summary,
			Silent:  true,
			IsError: true,
			Async:   true,
			Err:     err,
		}
		sm.cancelTaskTree(task.ID)
	} else {
		summary := ""
		if loopResult != nil {
			summary = loopResult.Content
		}
		summary = truncateText(summary, defaultSubagentMaxSummaryChars)

		snapshot = sm.updateTaskAndEmit(task, SubagentTaskCompleted, "completed", summary, "", trace)

		payload := SubagentResultPayload{
			Kind:   "subagent_result",
			Status: "completed",
			Mode:   "async",

			TaskID:       snapshot.ID,
			ParentTaskID: snapshot.ParentTaskID,
			Label:        snapshot.Label,
			Task:         truncateText(snapshot.Task, defaultSubagentMaxTaskChars),
			AgentID:      snapshot.AgentID,

			SessionKey: snapshot.SessionKey,
			RunID:      snapshot.RunID,

			OriginChannel: snapshot.OriginChannel,
			OriginChatID:  snapshot.OriginChatID,
			Depth:         snapshot.Depth,
			Iterations:    iterations,

			Summary:            summary,
			HandoffSuggestions: handoffSuggestions,
			Artifacts:          buildSubagentArtifacts(trace),
			Warnings:           handoffWarnings,
		}
		payloadJSON, marshalErr := marshalSubagentPayload(payload)
		if marshalErr != nil {
			payloadJSON = summary
		}

		result = &ToolResult{
			ForLLM:  payloadJSON,
			ForUser: summary,
			Silent:  true,
			IsError: false,
			Async:   true,
		}
	}

	// Call callback if provided and result is set.
	if callback != nil && result != nil {
		callback(ctx, result)
	}

	// Send announce message back to main agent
	if sm.bus != nil {
		announceBody := snapshot.Result
		if result != nil && strings.TrimSpace(result.ForLLM) != "" {
			announceBody = result.ForLLM
		}
		sm.publishTaskAnnouncement(snapshot, task.Status, announceBody)
	}
}

func (sm *SubagentManager) GetTask(taskID string) (*SubagentTask, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	task, ok := sm.tasks[taskID]
	if !ok {
		return nil, false
	}
	snapshot := *task
	return &snapshot, true
}

func (sm *SubagentManager) ListTasks() []*SubagentTask {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	tasks := make([]*SubagentTask, 0, len(sm.tasks))
	for _, task := range sm.tasks {
		snapshot := *task
		tasks = append(tasks, &snapshot)
	}
	return tasks
}

func (sm *SubagentManager) emitEvent(event SubagentTaskEvent) {
	sm.mu.RLock()
	handler := sm.eventHandler
	sm.mu.RUnlock()
	if handler == nil {
		return
	}
	handler(event)
}

// updateTaskAndEmit atomically updates task status/result, takes a snapshot,
// and emits a lifecycle event. This centralizes the repeated lock-update-emit pattern.
// The optional trace slice is attached to the event for observability.
func (sm *SubagentManager) updateTaskAndEmit(task *SubagentTask, eventType SubagentTaskEventType, status, result, errMsg string, trace ...[]ToolExecutionTrace) SubagentTask {
	sm.mu.Lock()
	task.Status = status
	task.Result = result
	snapshot := *task
	sm.mu.Unlock()
	event := SubagentTaskEvent{
		Type:      eventType,
		Task:      snapshot,
		Err:       errMsg,
		Timestamp: time.Now().UnixMilli(),
	}
	if len(trace) > 0 {
		event.Trace = trace[0]
	}
	sm.emitEvent(event)
	return snapshot
}

func (sm *SubagentManager) publishTaskAnnouncement(task SubagentTask, status string, result string) {
	if sm.bus == nil {
		return
	}
	state := "completed"
	if status == "failed" {
		state = "failed"
	} else if status == "cancelled" {
		state = "cancelled"
	}
	label := strings.TrimSpace(task.Label)
	if label == "" {
		label = strings.TrimSpace(task.ID)
	}
	announceContent := fmt.Sprintf("Task '%s' %s.\n\nResult:\n%s", label, state, strings.TrimSpace(result))
	pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pubCancel()
	_ = sm.bus.PublishInbound(pubCtx, bus.InboundMessage{
		Channel:  "system",
		SenderID: fmt.Sprintf("subagent:%s", task.ID),
		// Format: "original_channel:original_chat_id" for routing back
		ChatID:     fmt.Sprintf("%s:%s", task.OriginChannel, task.OriginChatID),
		Content:    announceContent,
		SessionKey: task.SessionKey,
	})
}

func (sm *SubagentManager) clearTaskCancel(taskID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.taskCancels, taskID)
}

func (sm *SubagentManager) cancelTaskTree(parentTaskID string) {
	queue := []string{strings.TrimSpace(parentTaskID)}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		var children []string
		var cancels []context.CancelFunc

		sm.mu.RLock()
		for taskID, task := range sm.tasks {
			if task.ParentTaskID != current {
				continue
			}
			children = append(children, taskID)
			if cancel, ok := sm.taskCancels[taskID]; ok {
				cancels = append(cancels, cancel)
			}
		}
		sm.mu.RUnlock()

		for _, cancel := range cancels {
			cancel()
		}
		queue = append(queue, children...)
	}
}

func isTerminalTaskStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

// SubagentTool executes a subagent task synchronously and returns the result.
// Unlike SpawnTool which runs tasks asynchronously, SubagentTool waits for completion
// and returns the result directly in the ToolResult.
type SubagentTool struct {
	manager *SubagentManager
}

func NewSubagentTool(manager *SubagentManager) *SubagentTool {
	return &SubagentTool{
		manager: manager,
	}
}

func (t *SubagentTool) Name() string {
	return "subagent"
}

func (t *SubagentTool) Description() string {
	return "Execute a subagent task synchronously and wait for the result. " +
		"Input: task (string, required) — clear description of what the subagent should do. " +
		"Output: structured JSON (summary + artifacts + iteration count). " +
		"Use this when you need the subagent's result before continuing your own work. " +
		"The subagent runs with its own tools and context. " +
		"For background tasks where you don't need to wait, use the 'spawn' tool instead."
}

func (t *SubagentTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{
				"type":        "string",
				"description": "The task for subagent to complete",
			},
			"label": map[string]any{
				"type":        "string",
				"description": "Optional short label for the task (for display)",
			},
			"agent_id": map[string]any{
				"type":        "string",
				"description": "Optional target agent ID to delegate the task to",
			},
		},
		"required": []string{"task"},
	}
}

func (t *SubagentTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	task, ok := args["task"].(string)
	if !ok {
		return ErrorResult("task is required").WithError(fmt.Errorf("task parameter is required"))
	}

	label, _ := args["label"].(string)
	agentID, _ := args["agent_id"].(string)

	if t.manager == nil {
		return ErrorResult("Subagent manager not configured").WithError(fmt.Errorf("manager is nil"))
	}

	// Build messages for subagent
	messages := []providers.Message{
		{
			Role: "system",
			Content: "You are a subagent working under the picoclaw system. " +
				"Complete the given task independently and provide a clear, concise result. " +
				"Use tools as needed, check results before proceeding, and do NOT fabricate data. " +
				"If a tool fails, try an alternative. If you cannot complete the task, explain why.",
		},
		{
			Role:    "user",
			Content: task,
		},
	}

	// Use RunToolLoop to execute with tools (same as async SpawnTool)
	sm := t.manager
	sm.mu.RLock()
	maxIter := sm.maxIterations
	maxTokens := sm.maxTokens
	temperature := sm.temperature
	hasMaxTokens := sm.hasMaxTokens
	hasTemperature := sm.hasTemperature
	resolver := sm.resolver
	toolCallsParallelEnabled := sm.toolCallsParallelEnabled
	maxToolCallConcurrency := sm.maxToolCallConcurrency
	parallelToolsMode := sm.parallelToolsMode
	toolPolicyOverrides := clonePolicyOverrides(sm.toolPolicyOverrides)
	policyCfg := sm.toolPolicy
	policyTags := copyStringMap(sm.toolPolicyTags)
	traceOpts := sm.toolTrace
	errorTemplateOpts := sm.toolErrorTemplate
	toolHooks := sm.toolHooks
	if len(toolHooks) > 0 {
		toolHooks = append([]ToolHook(nil), toolHooks...)
	}
	maxToolCallsPerRun := sm.maxToolCallsPerRun
	maxWallTimeSeconds := sm.maxWallTimeSeconds
	maxToolResultChars := sm.maxToolResultChars
	execution := SubagentExecutionConfig{
		Provider: sm.provider,
		Model:    sm.defaultModel,
		Tools:    sm.tools,
	}
	sm.mu.RUnlock()

	if resolver != nil {
		resolved, resolveErr := resolver(agentID)
		if resolveErr != nil {
			return ErrorResult(fmt.Sprintf("Subagent execution failed: %v", resolveErr)).WithError(resolveErr)
		}
		if resolved.Provider != nil {
			execution.Provider = resolved.Provider
		}
		if resolved.Model != "" {
			execution.Model = resolved.Model
		}
		if resolved.Tools != nil {
			execution.Tools = resolved.Tools
		}
	}
	if execution.Tools == nil {
		execution.Tools = NewToolRegistry()
	}

	var llmOptions map[string]any
	if hasMaxTokens || hasTemperature {
		llmOptions = map[string]any{}
		if hasMaxTokens {
			llmOptions["max_tokens"] = maxTokens
		}
		if hasTemperature {
			llmOptions["temperature"] = temperature
		}
	}

	originChannel := toolExecutionChannel(ctx)
	originChatID := toolExecutionChatID(ctx)
	if strings.TrimSpace(originChannel) == "" {
		originChannel = "cli"
	}
	if strings.TrimSpace(originChatID) == "" {
		originChatID = "direct"
	}

	loopResult, err := RunToolLoop(ctx, ToolLoopConfig{
		Provider:                 execution.Provider,
		Model:                    execution.Model,
		Tools:                    execution.Tools,
		MaxIterations:            maxIter,
		MaxToolCallsPerRun:       maxToolCallsPerRun,
		MaxWallTimeSeconds:       maxWallTimeSeconds,
		MaxToolResultChars:       maxToolResultChars,
		LLMOptions:               llmOptions,
		SenderID:                 fmt.Sprintf("subagent:sync:%s", toolExecutionSenderID(ctx)),
		Workspace:                sm.workspace,
		SessionKey:               toolExecutionSessionKey(ctx),
		RunID:                    toolExecutionRunID(ctx),
		IsResume:                 toolExecutionIsResume(ctx),
		Policy:                   policyCfg,
		PolicyTags:               policyTags,
		Trace:                    traceOpts,
		ErrorTemplate:            errorTemplateOpts,
		Hooks:                    toolHooks,
		ToolCallsParallelEnabled: toolCallsParallelEnabled,
		MaxToolCallConcurrency:   maxToolCallConcurrency,
		ParallelToolsMode:        parallelToolsMode,
		ToolPolicyOverrides:      toolPolicyOverrides,
	}, messages, originChannel, originChatID)
	status := "completed"
	errText := ""
	content := ""
	iterations := 0
	trace := []ToolExecutionTrace(nil)
	if loopResult != nil {
		content = loopResult.Content
		iterations = loopResult.Iterations
		trace = loopResult.Trace
	}
	if err != nil {
		status = "failed"
		errText = err.Error()
		if strings.TrimSpace(content) == "" {
			content = fmt.Sprintf("Error: %v", err)
		}
	}

	// ForUser: brief summary for user (truncated if too long)
	userContent := content
	maxUserLen := 500
	if len(userContent) > maxUserLen {
		userContent = userContent[:maxUserLen] + "..."
	}

	labelStr := label
	if labelStr == "" {
		labelStr = "(unnamed)"
	}

	handoffSuggestions, handoffWarnings := extractSubagentHandoffSuggestions(trace)

	payload := SubagentResultPayload{
		Kind:   "subagent_result",
		Status: status,
		Mode:   "sync",

		Label:   labelStr,
		Task:    truncateText(task, defaultSubagentMaxTaskChars),
		AgentID: strings.TrimSpace(agentID),

		Iterations:         iterations,
		Summary:            truncateText(content, defaultSubagentMaxSummaryChars),
		Error:              truncateText(errText, 1200),
		HandoffSuggestions: handoffSuggestions,
		Artifacts:          buildSubagentArtifacts(trace),
		Warnings:           handoffWarnings,
	}

	payloadJSON, marshalErr := marshalSubagentPayload(payload)
	if marshalErr != nil {
		if status == "failed" {
			return ErrorResult(fmt.Sprintf("Subagent execution failed: %v", err)).WithError(err)
		}
		return ErrorResult(fmt.Sprintf("failed to encode subagent payload: %v", marshalErr)).WithError(marshalErr)
	}

	return &ToolResult{
		ForLLM:  payloadJSON,
		ForUser: userContent,
		Silent:  false,
		IsError: status != "completed",
		Async:   false,
		Err:     err,
	}
}
