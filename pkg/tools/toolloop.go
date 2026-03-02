// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// ToolLoopConfig configures the tool execution loop.
type ToolLoopConfig struct {
	Provider      providers.LLMProvider
	Model         string
	Tools         *ToolRegistry
	MaxIterations int
	LLMOptions    map[string]any
	SenderID      string

	// Workspace/session/run metadata are used for tool trace + tool policy features.
	Workspace  string
	SessionKey string
	RunID      string
	IsResume   bool

	// Policy controls centralized tool guardrails (Phase D2).
	Policy     config.ToolPolicyConfig
	PolicyTags map[string]string
	// Trace controls optional on-disk tool tracing (Phase A1).
	Trace ToolTraceOptions
	// ErrorTemplate controls tool error wrapping (Phase A3).
	ErrorTemplate ToolErrorTemplateOptions

	ToolCallsParallelEnabled bool
	MaxToolCallConcurrency   int
	ParallelToolsMode        string
	ToolPolicyOverrides      map[string]string
}

// ToolLoopResult contains the result of running the tool loop.
type ToolLoopResult struct {
	Content    string
	Iterations int
	Trace      []ToolExecutionTrace
}

// ToolExecutionTrace captures a single tool execution inside the loop.
type ToolExecutionTrace struct {
	Iteration      int
	ToolName       string
	Arguments      map[string]any
	Result         string
	IsError        bool
	DurationMS     int64
	ToolCallID     string
	LLMReasoning   string   // The LLM's text reasoning that accompanied this tool call
	PrecedingTools []string // Names of tools called in previous iterations
}

// RunToolLoop executes the LLM + tool call iteration loop.
// This is the core agent logic that can be reused by both main agent and subagents.
func RunToolLoop(
	ctx context.Context,
	config ToolLoopConfig,
	messages []providers.Message,
	channel, chatID string,
) (*ToolLoopResult, error) {
	iteration := 0
	var finalContent string
	trace := make([]ToolExecutionTrace, 0)
	precedingTools := make([]string, 0, 16) // accumulates tool names across iterations

	// Loop detection state
	type toolloopCallSig struct{ name, args string }
	recentCalls := make([]toolloopCallSig, 0, 32)
	const loopThreshold = 3

	for iteration < config.MaxIterations {
		iteration++

		logger.DebugCF("toolloop", "LLM iteration",
			map[string]any{
				"iteration": iteration,
				"max":       config.MaxIterations,
			})

		// 1. Build tool definitions
		var providerToolDefs []providers.ToolDefinition
		if config.Tools != nil {
			providerToolDefs = config.Tools.ToProviderDefs()
		}

		// 2. Set default LLM options
		llmOpts := config.LLMOptions
		if llmOpts == nil {
			llmOpts = map[string]any{}
		}
		// 3. Call LLM
		response, err := config.Provider.Chat(ctx, messages, providerToolDefs, config.Model, llmOpts)
		if err != nil {
			logger.ErrorCF("toolloop", "LLM call failed",
				map[string]any{
					"iteration": iteration,
					"error":     err.Error(),
				})
			return nil, fmt.Errorf("LLM call failed: %w", err)
		}

		// 4. If no tool calls, we're done
		if len(response.ToolCalls) == 0 {
			finalContent = response.Content
			logger.InfoCF("toolloop", "LLM response without tool calls (direct answer)",
				map[string]any{
					"iteration":     iteration,
					"content_chars": len(finalContent),
				})
			break
		}

		normalizedToolCalls := make([]providers.ToolCall, 0, len(response.ToolCalls))
		for _, tc := range response.ToolCalls {
			normalizedToolCalls = append(normalizedToolCalls, providers.NormalizeToolCall(tc))
		}

		// 5. Log tool calls
		toolNames := make([]string, 0, len(normalizedToolCalls))
		for _, tc := range normalizedToolCalls {
			toolNames = append(toolNames, tc.Name)
		}
		logger.InfoCF("toolloop", "LLM requested tool calls",
			map[string]any{
				"tools":     toolNames,
				"count":     len(normalizedToolCalls),
				"iteration": iteration,
			})

		// Loop detection: check for repeated identical tool calls
		loopDetected := ""
		for _, tc := range normalizedToolCalls {
			argsJSON, _ := json.Marshal(tc.Arguments)
			sig := string(argsJSON)
			count := 0
			for _, prev := range recentCalls {
				if prev.name == tc.Name && prev.args == sig {
					count++
				}
			}
			if count >= loopThreshold {
				loopDetected = tc.Name
				break
			}
		}

		// Track current calls
		for _, tc := range normalizedToolCalls {
			argsJSON, _ := json.Marshal(tc.Arguments)
			recentCalls = append(recentCalls, toolloopCallSig{name: tc.Name, args: string(argsJSON)})
		}

		// 6. Build assistant message with tool calls
		assistantMsg := providers.Message{
			Role:    "assistant",
			Content: response.Content,
		}
		for _, tc := range normalizedToolCalls {
			argumentsJSON, _ := json.Marshal(tc.Arguments)
			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, providers.ToolCall{
				ID:        tc.ID,
				Type:      "function",
				Name:      tc.Name,
				Arguments: tc.Arguments,
				Function: &providers.FunctionCall{
					Name:      tc.Name,
					Arguments: string(argumentsJSON),
				},
			})
		}
		messages = append(messages, assistantMsg)

		// Loop break: if a repeated tool call pattern was detected, skip execution
		// and inject error tool results so the LLM can course-correct.
		if loopDetected != "" {
			logger.WarnCF("toolloop", "Loop detected, injecting error results",
				map[string]any{
					"tool":      loopDetected,
					"iteration": iteration,
					"threshold": loopThreshold,
				})
			for _, tc := range normalizedToolCalls {
				errMsg := fmt.Sprintf(
					"Loop detected: tool '%s' has been called with identical arguments %d+ times. "+
						"This is not making progress. Try a different approach, use different arguments, "+
						"or consider whether the task can be completed with the information already gathered.",
					loopDetected, loopThreshold,
				)
				trace = append(trace, ToolExecutionTrace{
					Iteration:    iteration,
					ToolName:     tc.Name,
					Arguments:    tc.Arguments,
					Result:       errMsg,
					IsError:      true,
					ToolCallID:   tc.ID,
					LLMReasoning: response.Content,
				})
				messages = append(messages, providers.Message{
					Role:       "tool",
					Content:    errMsg,
					ToolCallID: tc.ID,
				})
			}
			continue
		}

		toolExecutions := ExecuteToolCalls(ctx, config.Tools, normalizedToolCalls, ToolCallExecutionOptions{
			Channel:       channel,
			ChatID:        chatID,
			SenderID:      config.SenderID,
			Workspace:     config.Workspace,
			SessionKey:    config.SessionKey,
			RunID:         config.RunID,
			IsResume:      config.IsResume,
			Policy:        config.Policy,
			PolicyTags:    config.PolicyTags,
			Iteration:     iteration,
			LogScope:      "toolloop",
			Trace:         config.Trace,
			ErrorTemplate: config.ErrorTemplate,
			Parallel: ToolCallParallelConfig{
				Enabled:             config.ToolCallsParallelEnabled,
				MaxConcurrency:      config.MaxToolCallConcurrency,
				Mode:                config.ParallelToolsMode,
				ToolPolicyOverrides: config.ToolPolicyOverrides,
			},
		})

		for _, executed := range toolExecutions {
			toolResult := executed.Result
			tc := executed.ToolCall

			// Determine content for LLM
			contentForLLM := toolResult.ForLLM
			if contentForLLM == "" && toolResult.Err != nil {
				contentForLLM = toolResult.Err.Error()
			}

			// Copy preceding tools slice for this trace entry
			ptCopy := make([]string, len(precedingTools))
			copy(ptCopy, precedingTools)

			trace = append(trace, ToolExecutionTrace{
				Iteration:      iteration,
				ToolName:       tc.Name,
				Arguments:      tc.Arguments,
				Result:         contentForLLM,
				IsError:        toolResult.IsError,
				DurationMS:     executed.DurationMS,
				ToolCallID:     tc.ID,
				LLMReasoning:   response.Content, // LLM text that accompanied tool calls
				PrecedingTools: ptCopy,
			})
			precedingTools = append(precedingTools, tc.Name)

			// Add tool result message
			toolResultMsg := providers.Message{
				Role:       "tool",
				Content:    contentForLLM,
				ToolCallID: tc.ID,
			}
			messages = append(messages, toolResultMsg)
		}
	}

	return &ToolLoopResult{
		Content:    finalContent,
		Iterations: iteration,
		Trace:      trace,
	}, nil
}
