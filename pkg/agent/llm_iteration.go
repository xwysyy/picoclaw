package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/constants"
	"github.com/xwysyy/X-Claw/pkg/logger"
	"github.com/xwysyy/X-Claw/pkg/providers"
	"github.com/xwysyy/X-Claw/pkg/tools"
	"github.com/xwysyy/X-Claw/pkg/utils"
)

type toolCallSignature struct {
	Name string
	Args string
}

type llmCallResult struct {
	response         *providers.LLMResponse
	usedModel        string
	fallbackAttempts []providers.FallbackAttempt
}

type llmIterationRunner struct {
	loop *AgentLoop
	ctx  context.Context

	agent    *AgentInstance
	messages []providers.Message
	opts     processOptions
	trace    *runTraceWriter

	modelForRun string

	iteration             int
	finalContent          string
	recentToolCalls       []toolCallSignature
	totalPromptTokens     int
	totalCompletionTokens int
	runStart              time.Time
	toolCallsUsed         int

	cfg                *config.Config
	maxWallTimeSeconds int
	maxToolCallsPerRun int
	maxToolResultChars int
}

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
	runner := newLLMIterationRunner(al, ctx, agent, messages, opts, trace, modelForRun)
	return runner.run()
}

func newLLMIterationRunner(
	loop *AgentLoop,
	ctx context.Context,
	agent *AgentInstance,
	messages []providers.Message,
	opts processOptions,
	trace *runTraceWriter,
	modelForRun string,
) *llmIterationRunner {
	cfg := loop.Config()
	modelForRun = strings.TrimSpace(modelForRun)
	if modelForRun == "" {
		modelForRun = strings.TrimSpace(agent.Model)
	}
	runner := &llmIterationRunner{
		loop:            loop,
		ctx:             ctx,
		agent:           agent,
		messages:        messages,
		opts:            opts,
		trace:           trace,
		modelForRun:     modelForRun,
		recentToolCalls: make([]toolCallSignature, 0, 32),
		runStart:        time.Now(),
		cfg:             cfg,
	}
	if cfg != nil && cfg.Limits.Enabled {
		runner.maxWallTimeSeconds = cfg.Limits.MaxRunWallTimeSeconds
		runner.maxToolCallsPerRun = cfg.Limits.MaxToolCallsPerRun
		runner.maxToolResultChars = cfg.Limits.MaxToolResultChars
	}
	return runner
}

func (r *llmIterationRunner) run() (string, int, *AgentInstance, error) {
	for r.iteration < r.agent.MaxIterations {
		r.iteration++
		r.logIterationStart()
		if r.enforceWallTimeBudget() {
			break
		}

		providerToolDefs := r.agent.Tools.ToProviderDefs()
		r.recordLLMRequest(providerToolDefs)
		call, err := r.callLLMWithRetry(providerToolDefs)
		if err != nil {
			logger.ErrorCF("agent", "LLM call failed",
				map[string]any{
					"agent_id":  r.agent.ID,
					"iteration": r.iteration,
					"error":     err.Error(),
				})
			return "", r.iteration, r.agent, fmt.Errorf("LLM call failed after retries: %w", err)
		}

		if r.afterLLMCall(call) {
			continue
		}
		if r.handleLLMResponse(call.response) {
			break
		}
	}

	r.logTokenSummary()
	return r.finalContent, r.iteration, r.agent, nil
}

func (r *llmIterationRunner) logIterationStart() {
	logger.DebugCF("agent", "LLM iteration",
		map[string]any{
			"agent_id":  r.agent.ID,
			"iteration": r.iteration,
			"max":       r.agent.MaxIterations,
		})
}

func (r *llmIterationRunner) enforceWallTimeBudget() bool {
	if r.maxWallTimeSeconds <= 0 || time.Since(r.runStart) <= time.Duration(r.maxWallTimeSeconds)*time.Second {
		return false
	}
	r.finalContent = fmt.Sprintf(
		"RESOURCE_BUDGET_EXCEEDED: run wall time exceeded (%ds). Please narrow the task or split it into smaller steps.",
		r.maxWallTimeSeconds,
	)
	logger.WarnCF("agent", "Resource budget exceeded (wall time)", map[string]any{
		"agent_id":          r.agent.ID,
		"iteration":         r.iteration,
		"wall_time_seconds": int(time.Since(r.runStart).Seconds()),
		"tool_calls_used":   r.toolCallsUsed,
		"session_key":       r.opts.SessionKey,
	})
	return true
}

func (r *llmIterationRunner) recordLLMRequest(providerToolDefs []providers.ToolDefinition) {
	if r.trace != nil {
		r.trace.recordLLMRequest(r.iteration, len(r.messages), len(providerToolDefs))
	}
	logger.DebugCF("agent", "LLM request",
		map[string]any{
			"agent_id":          r.agent.ID,
			"iteration":         r.iteration,
			"model":             r.modelForRun,
			"messages_count":    len(r.messages),
			"tools_count":       len(providerToolDefs),
			"max_tokens":        r.agent.MaxTokens,
			"temperature":       r.agent.Temperature,
			"system_prompt_len": len(r.messages[0].Content),
		})
	logger.DebugCF("agent", "Full LLM request",
		map[string]any{
			"iteration":     r.iteration,
			"messages_json": formatMessagesForLog(r.messages),
			"tools_json":    formatToolsForLog(providerToolDefs),
		})
}

func (r *llmIterationRunner) callLLMWithRetry(providerToolDefs []providers.ToolDefinition) (*llmCallResult, error) {
	maxRetries := 2
	for retry := 0; retry <= maxRetries; retry++ {
		call, err := r.performLLMCall(providerToolDefs)
		if err == nil {
			return call, nil
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
			if r.handleContextWindowRetry(retry, err) {
				continue
			}
		}
		return nil, err
	}
	return nil, fmt.Errorf("unreachable LLM retry state")
}

func (r *llmIterationRunner) performLLMCall(providerToolDefs []providers.ToolDefinition) (*llmCallResult, error) {
	llmOpts := r.buildLLMOptions()
	lastFallbackAttempts := []providers.FallbackAttempt(nil)
	callLLM := func() (*providers.LLMResponse, string, error) {
		lastFallbackAttempts = nil
		if strings.TrimSpace(r.agent.Model) != "" && r.modelForRun != strings.TrimSpace(r.agent.Model) {
			resp, err := r.agent.Provider.Chat(r.ctx, r.messages, providerToolDefs, r.modelForRun, llmOpts)
			return resp, r.modelForRun, err
		}
		if len(r.agent.Candidates) > 1 && r.loop.fallback != nil {
			fbResult, fbErr := r.loop.fallback.Execute(
				r.ctx,
				r.agent.Candidates,
				func(ctx context.Context, provider, model string) (*providers.LLMResponse, error) {
					return r.agent.Provider.Chat(ctx, r.messages, providerToolDefs, model, llmOpts)
				},
			)
			if fbErr != nil {
				return nil, "", fbErr
			}
			if fbResult.Provider != "" && len(fbResult.Attempts) > 0 {
				logger.InfoCF(
					"agent",
					fmt.Sprintf("Fallback: succeeded with %s/%s after %d attempts", fbResult.Provider, fbResult.Model, len(fbResult.Attempts)+1),
					map[string]any{"agent_id": r.agent.ID, "iteration": r.iteration},
				)
			}
			lastFallbackAttempts = fbResult.Attempts
			return fbResult.Response, strings.TrimSpace(fbResult.Model), nil
		}
		resp, err := r.agent.Provider.Chat(r.ctx, r.messages, providerToolDefs, r.modelForRun, llmOpts)
		return resp, r.modelForRun, err
	}

	response, usedModel, err := callLLM()
	if err != nil {
		return nil, err
	}
	return &llmCallResult{
		response:         response,
		usedModel:        usedModel,
		fallbackAttempts: lastFallbackAttempts,
	}, nil
}

func (r *llmIterationRunner) buildLLMOptions() map[string]any {
	llmOpts := map[string]any{
		"max_tokens":       r.agent.MaxTokens,
		"temperature":      r.agent.Temperature,
		"prompt_cache_key": r.agent.ID,
	}
	if r.agent.ThinkingLevel != "" && r.agent.ThinkingLevel != ThinkingOff {
		if tc, ok := r.agent.Provider.(providers.ThinkingCapable); ok && tc.SupportsThinking() {
			llmOpts["thinking_level"] = string(r.agent.ThinkingLevel)
		} else {
			logger.WarnCF(
				"agent",
				"thinking_level is set but current provider does not support it, ignoring",
				map[string]any{"agent_id": r.agent.ID, "thinking_level": string(r.agent.ThinkingLevel)},
			)
		}
	}
	return llmOpts
}

func (r *llmIterationRunner) handleContextWindowRetry(retry int, err error) bool {
	logger.WarnCF("agent", "Context window error detected, attempting compression", map[string]any{
		"error": err.Error(),
		"retry": retry,
	})

	if r.cfg != nil {
		target := pickFirstDifferentModel(r.modelForRun, r.agent.Candidates)
		if target != "" {
			if r.loop.maybeAutoDowngradeSessionModel(
				r.agent.Workspace,
				r.trace,
				r.agent.ID,
				r.opts.SessionKey,
				r.runID(),
				r.opts.Channel,
				r.opts.ChatID,
				r.opts.SenderID,
				r.iteration,
				r.modelForRun,
				target,
				"context_window",
				nil,
			) {
				r.modelForRun = target
			}
		}
	}

	if retry == 0 && !constants.IsInternalChannel(r.opts.Channel) {
		r.loop.bus.PublishOutbound(r.ctx, bus.OutboundMessage{
			Channel: r.opts.Channel,
			ChatID:  r.opts.ChatID,
			Content: "Context window exceeded. Compressing history and retrying...",
		})
	}

	compactionCtx, cancel := r.loop.safeCompactionContext()
	currentTokens := r.loop.estimateTokens(r.agent.Sessions.GetHistory(r.opts.SessionKey))
	if flushed, flushErr := r.loop.maybeFlushMemoryBeforeCompaction(
		compactionCtx,
		r.agent,
		r.opts.SessionKey,
		currentTokens,
	); flushErr != nil {
		logger.WarnCF("agent", "Pre-compaction memory flush failed", map[string]any{"error": flushErr.Error()})
	} else if flushed {
		logger.InfoCF("agent", "Pre-compaction memory flush completed", map[string]any{"session_key": r.opts.SessionKey})
	}

	compacted, compactErr := r.loop.compactWithSafeguard(compactionCtx, r.agent, r.opts.SessionKey)
	cancel()
	if compactErr != nil {
		logger.WarnCF("agent", "Compaction safeguard cancelled", map[string]any{"error": compactErr.Error()})
		return false
	}
	if !compacted {
		logger.WarnCF("agent", "Compaction safeguard skipped; preserving history", map[string]any{"session_key": r.opts.SessionKey})
		return true
	}

	newHistory := r.agent.Sessions.GetHistory(r.opts.SessionKey)
	newSummary := r.agent.Sessions.GetSummary(r.opts.SessionKey)
	r.messages = r.agent.ContextBuilder.BuildMessagesForSession(
		r.opts.SessionKey,
		newHistory,
		newSummary,
		"",
		nil,
		r.opts.Channel,
		r.opts.ChatID,
		r.opts.WorkingState,
	)
	return true
}

func (r *llmIterationRunner) afterLLMCall(call *llmCallResult) bool {
	usedModel := strings.TrimSpace(call.usedModel)
	if usedModel == "" {
		usedModel = r.modelForRun
	}
	if len(call.fallbackAttempts) == 0 && strings.EqualFold(usedModel, strings.TrimSpace(r.modelForRun)) {
		r.loop.clearModelAutoDowngradeState(r.opts.SessionKey)
	}
	if len(call.fallbackAttempts) > 0 && usedModel != "" && !strings.EqualFold(usedModel, strings.TrimSpace(r.modelForRun)) {
		if r.loop.maybeAutoDowngradeSessionModel(
			r.agent.Workspace,
			r.trace,
			r.agent.ID,
			r.opts.SessionKey,
			r.runID(),
			r.opts.Channel,
			r.opts.ChatID,
			r.opts.SenderID,
			r.iteration,
			r.modelForRun,
			usedModel,
			"fallback",
			call.fallbackAttempts,
		) {
			r.modelForRun = usedModel
		}
	}

	r.recordLLMResponse(call.response, usedModel)
	r.recordTokenUsage(call.response, usedModel)
	return r.absorbSteeringMessages(usedModel)
}

func (r *llmIterationRunner) runID() string {
	if r.trace != nil {
		return r.trace.RunID()
	}
	return strings.TrimSpace(r.opts.RunID)
}

func (r *llmIterationRunner) recordLLMResponse(response *providers.LLMResponse, usedModel string) {
	if r.trace == nil {
		return
	}
	if strings.TrimSpace(usedModel) != "" {
		r.trace.model = strings.TrimSpace(usedModel)
	}
	toolNames := make([]string, 0, len(response.ToolCalls))
	for _, tc := range response.ToolCalls {
		toolNames = append(toolNames, tc.Name)
	}
	sort.Strings(toolNames)
	r.trace.recordLLMResponse(r.iteration, response.Content, toolNames, response.Usage)
}

func (r *llmIterationRunner) recordTokenUsage(response *providers.LLMResponse, usedModel string) {
	if response.Usage == nil {
		return
	}
	if strings.TrimSpace(usedModel) == "" {
		usedModel = r.modelForRun
	}
	if store := r.loop.tokenUsageStore(r.agent.Workspace); store != nil {
		store.Record(usedModel, response.Usage)
	}
	logger.InfoCF("agent", "Token usage",
		map[string]any{
			"agent_id":          r.agent.ID,
			"iteration":         r.iteration,
			"model":             usedModel,
			"prompt_tokens":     response.Usage.PromptTokens,
			"completion_tokens": response.Usage.CompletionTokens,
			"total_tokens":      response.Usage.TotalTokens,
			"session_key":       r.opts.SessionKey,
		})
	r.totalPromptTokens += response.Usage.PromptTokens
	r.totalCompletionTokens += response.Usage.CompletionTokens
}

func (r *llmIterationRunner) absorbSteeringMessages(usedModel string) bool {
	if r.opts.Steering == nil {
		return false
	}
	steeringMsgs := make([]bus.InboundMessage, 0, 4)
	for {
		select {
		case sm := <-r.opts.Steering:
			steeringMsgs = append(steeringMsgs, sm)
		default:
			goto steeringDrained
		}
	}
steeringDrained:
	if len(steeringMsgs) == 0 {
		return false
	}
	for _, sm := range steeringMsgs {
		content := strings.TrimSpace(sm.Content)
		if content == "" {
			continue
		}
		addSessionMessageAndSave(
			r.agent.Sessions,
			r.opts.SessionKey,
			"user",
			content,
			"Failed to persist steering message (best-effort)",
			map[string]any{"iteration": r.iteration},
		)
		r.messages = append(r.messages, providers.Message{Role: "user", Content: content})
		if r.trace != nil {
			now := time.Now()
			r.trace.appendEvent(runTraceEvent{
				Type:               "steering.message",
				TS:                 now.UTC().Format(time.RFC3339Nano),
				TSMS:               now.UnixMilli(),
				RunID:              r.trace.runID,
				SessionKey:         r.opts.SessionKey,
				Channel:            strings.TrimSpace(r.opts.Channel),
				ChatID:             strings.TrimSpace(r.opts.ChatID),
				SenderID:           strings.TrimSpace(r.opts.SenderID),
				AgentID:            strings.TrimSpace(r.agent.ID),
				Model:              strings.TrimSpace(usedModel),
				Iteration:          r.iteration,
				UserMessagePreview: utils.Truncate(content, 400),
				UserMessageChars:   len(content),
			})
		}
	}
	return true
}

func (r *llmIterationRunner) handleLLMResponse(response *providers.LLMResponse) bool {
	go r.loop.handleReasoning(
		r.ctx,
		response.Reasoning,
		r.opts.Channel,
		r.loop.targetReasoningChannelID(r.opts.Channel),
	)

	logger.DebugCF("agent", "LLM response",
		map[string]any{
			"agent_id":       r.agent.ID,
			"iteration":      r.iteration,
			"content_chars":  len(response.Content),
			"tool_calls":     len(response.ToolCalls),
			"reasoning":      response.Reasoning,
			"target_channel": r.loop.targetReasoningChannelID(r.opts.Channel),
			"channel":        r.opts.Channel,
		})

	if len(response.ToolCalls) == 0 {
		r.finalContent = response.Content
		logger.InfoCF("agent", "LLM response without tool calls (direct answer)",
			map[string]any{
				"agent_id":      r.agent.ID,
				"iteration":     r.iteration,
				"content_chars": len(r.finalContent),
			})
		return true
	}

	normalizedToolCalls := normalizeToolCalls(response.ToolCalls)
	if r.exceedsToolCallBudget(normalizedToolCalls) {
		return true
	}
	r.updateWorkingStateHint(response.Content)
	r.logRequestedToolCalls(normalizedToolCalls)
	if r.handleToolLoop(response, normalizedToolCalls) {
		return false
	}
	r.recordRecentToolCalls(normalizedToolCalls)
	r.appendAssistantToolCallMessage(response, normalizedToolCalls)
	toolExecutions := r.executeToolCalls(normalizedToolCalls)
	r.applyToolExecutionResults(toolExecutions)
	return false
}

func normalizeToolCalls(toolCalls []providers.ToolCall) []providers.ToolCall {
	normalized := make([]providers.ToolCall, 0, len(toolCalls))
	for _, tc := range toolCalls {
		normalized = append(normalized, providers.NormalizeToolCall(tc))
	}
	return normalized
}

func (r *llmIterationRunner) exceedsToolCallBudget(toolCalls []providers.ToolCall) bool {
	if r.maxToolCallsPerRun <= 0 || r.toolCallsUsed+len(toolCalls) <= r.maxToolCallsPerRun {
		return false
	}
	r.finalContent = fmt.Sprintf(
		"RESOURCE_BUDGET_EXCEEDED: tool call budget exceeded (%d). Please narrow the request or reduce the number of tools used.",
		r.maxToolCallsPerRun,
	)
	logger.WarnCF("agent", "Resource budget exceeded (tool calls)", map[string]any{
		"agent_id":           r.agent.ID,
		"iteration":          r.iteration,
		"tool_calls_used":    r.toolCallsUsed,
		"tool_calls_pending": len(toolCalls),
		"tool_calls_budget":  r.maxToolCallsPerRun,
		"session_key":        r.opts.SessionKey,
	})
	return true
}

func (r *llmIterationRunner) updateWorkingStateHint(content string) {
	reasoning := strings.TrimSpace(content)
	if reasoning == "" || r.opts.WorkingState == nil {
		return
	}
	hint := reasoning
	if len(hint) > 200 {
		hint = hint[:200] + "..."
	}
	r.opts.WorkingState.SetNextAction(hint)
}

func (r *llmIterationRunner) logRequestedToolCalls(toolCalls []providers.ToolCall) {
	toolNames := make([]string, 0, len(toolCalls))
	for _, tc := range toolCalls {
		toolNames = append(toolNames, tc.Name)
	}
	logger.InfoCF("agent", "LLM requested tool calls",
		map[string]any{
			"agent_id":  r.agent.ID,
			"tools":     toolNames,
			"count":     len(toolCalls),
			"iteration": r.iteration,
		})
}

func (r *llmIterationRunner) handleToolLoop(response *providers.LLMResponse, toolCalls []providers.ToolCall) bool {
	loopingTool := detectToolCallLoop(r.recentToolCalls, toolCalls, 3)
	if loopingTool == "" {
		return false
	}
	logger.WarnCF("agent", "Tool call loop detected",
		map[string]any{
			"agent_id":  r.agent.ID,
			"tool":      loopingTool,
			"iteration": r.iteration,
		})

	loopAssistantMsg := providers.Message{Role: "assistant", Content: response.Content}
	for _, tc := range toolCalls {
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
	r.messages = append(r.messages, loopAssistantMsg)

	loopNotice := fmt.Sprintf("Loop detected: '%s' called with same arguments 3+ times. Try a different approach, use a different tool, or explain why you are stuck.", loopingTool)
	for _, tc := range toolCalls {
		r.messages = append(r.messages, providers.Message{Role: "tool", Content: loopNotice, ToolCallID: tc.ID})
	}
	return true
}

func (r *llmIterationRunner) recordRecentToolCalls(toolCalls []providers.ToolCall) {
	for _, tc := range toolCalls {
		argsJSON, _ := json.Marshal(tc.Arguments)
		r.recentToolCalls = append(r.recentToolCalls, toolCallSignature{Name: tc.Name, Args: string(argsJSON)})
	}
}

func (r *llmIterationRunner) appendAssistantToolCallMessage(response *providers.LLMResponse, toolCalls []providers.ToolCall) {
	assistantMsg := providers.Message{Role: "assistant", Content: response.Content, ReasoningContent: response.ReasoningContent}
	for _, tc := range toolCalls {
		argumentsJSON, _ := json.Marshal(tc.Arguments)
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
	r.messages = append(r.messages, assistantMsg)
	addSessionFullMessage(r.agent.Sessions, r.opts.SessionKey, assistantMsg)
}

func (r *llmIterationRunner) executeToolCalls(toolCalls []providers.ToolCall) []tools.ToolCallExecution {
	cfg := r.loop.Config()
	parallelCfg := tools.ToolCallParallelConfig{Enabled: cfg != nil && cfg.Orchestration.ToolCallsParallelEnabled}
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
		errorTemplateOpts.IncludeAvailableTools = true
	}

	toolExecutions := tools.ExecuteToolCalls(r.ctx, r.agent.Tools, toolCalls, tools.ToolCallExecutionOptions{
		Channel:                r.opts.Channel,
		ChatID:                 r.opts.ChatID,
		SenderID:               r.opts.SenderID,
		Workspace:              r.agent.Workspace,
		SessionKey:             r.opts.SessionKey,
		RunID:                  r.runID(),
		IsResume:               r.opts.Resume,
		Iteration:              r.iteration,
		LogScope:               "agent",
		Parallel:               parallelCfg,
		Trace:                  traceOpts,
		MaxResultChars:         r.maxToolResultChars,
		ErrorTemplate:          errorTemplateOpts,
		Hooks:                  tools.BuildDefaultToolHooks(cfg),
		AsyncCallbackForCall: func(call providers.ToolCall) tools.AsyncCallback {
			return func(callbackCtx context.Context, result *tools.ToolResult) {
				if result == nil {
					return
				}
				if !result.Silent && result.ForUser != "" {
					logger.InfoCF("agent", "Async tool completed, agent will handle notification",
						map[string]any{"tool": call.Name, "content_len": len(result.ForUser)})
				}
			}
		},
	})
	r.toolCallsUsed += len(toolExecutions)
	if r.trace != nil {
		r.trace.recordToolBatch(r.iteration, toolExecutions)
	}
	return toolExecutions
}

func (r *llmIterationRunner) applyToolExecutionResults(toolExecutions []tools.ToolCallExecution) {
	for _, executed := range toolExecutions {
		toolResult := executed.Result
		tc := executed.ToolCall

		if ws := r.opts.WorkingState; ws != nil {
			ws.RecordToolCall(tc.Name, toolResult.IsError)
			outcome := toolResult.ForLLM
			if len(outcome) > 120 {
				outcome = outcome[:120] + "..."
			}
			if toolResult.IsError {
				outcome = "[error] " + outcome
			}
			ws.AddCompletedStep(tc.Name, outcome, tc.Name)
		}

		if !toolResult.Silent && toolResult.ForUser != "" && r.opts.SendResponse {
			r.loop.bus.PublishOutbound(r.ctx, bus.OutboundMessage{Channel: r.opts.Channel, ChatID: r.opts.ChatID, Content: toolResult.ForUser})
			logger.DebugCF("agent", "Sent tool result to user",
				map[string]any{"tool": tc.Name, "content_len": len(toolResult.ForUser)})
		}

		if len(toolResult.Media) > 0 && r.opts.SendResponse {
			parts := make([]bus.MediaPart, 0, len(toolResult.Media))
			for _, ref := range toolResult.Media {
				part := bus.MediaPart{Ref: ref}
				if r.loop.mediaResolver != nil {
					if _, meta, err := r.loop.mediaResolver.ResolveWithMeta(ref); err == nil {
						part.Filename = strings.TrimSpace(meta.Filename)
						part.ContentType = strings.TrimSpace(meta.ContentType)
						part.Type = inferMediaType(part.Filename, part.ContentType)
					}
				}
				parts = append(parts, part)
			}
			r.loop.bus.PublishOutboundMedia(r.ctx, bus.OutboundMediaMessage{Channel: r.opts.Channel, ChatID: r.opts.ChatID, Parts: parts})
		}

		contentForLLM := toolResult.ForLLM
		if contentForLLM == "" && toolResult.Err != nil {
			contentForLLM = toolResult.Err.Error()
		}
		toolResultMsg := providers.Message{Role: "tool", Content: contentForLLM, ToolCallID: tc.ID}
		r.messages = append(r.messages, toolResultMsg)
		addSessionFullMessage(r.agent.Sessions, r.opts.SessionKey, toolResultMsg)
	}
}

func (r *llmIterationRunner) logTokenSummary() {
	if r.totalPromptTokens == 0 && r.totalCompletionTokens == 0 {
		return
	}
	logger.InfoCF("agent", "Request token usage summary",
		map[string]any{
			"agent_id":                r.agent.ID,
			"iterations":              r.iteration,
			"total_prompt_tokens":     r.totalPromptTokens,
			"total_completion_tokens": r.totalCompletionTokens,
			"total_tokens":            r.totalPromptTokens + r.totalCompletionTokens,
			"session_key":             r.opts.SessionKey,
		})
}
