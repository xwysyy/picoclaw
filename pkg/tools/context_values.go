package tools

import (
	"context"
	"strings"
)

type executionContextKey string

const (
	executionContextChannelKey  executionContextKey = "tool_execution_channel"
	executionContextChatIDKey   executionContextKey = "tool_execution_chat_id"
	executionContextSenderIDKey executionContextKey = "tool_execution_sender_id"
	executionContextSessionKey  executionContextKey = "tool_execution_session_key"
	executionContextRunIDKey    executionContextKey = "tool_execution_run_id"
	executionContextIsResumeKey executionContextKey = "tool_execution_is_resume"
)

func withExecutionContext(ctx context.Context, channel, chatID, senderID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(channel) != "" {
		ctx = context.WithValue(ctx, executionContextChannelKey, strings.TrimSpace(channel))
	}
	if strings.TrimSpace(chatID) != "" {
		ctx = context.WithValue(ctx, executionContextChatIDKey, strings.TrimSpace(chatID))
	}
	if strings.TrimSpace(senderID) != "" {
		ctx = context.WithValue(ctx, executionContextSenderIDKey, strings.TrimSpace(senderID))
	}
	return ctx
}

func withExecutionSessionKey(ctx context.Context, sessionKey string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(sessionKey) != "" {
		ctx = context.WithValue(ctx, executionContextSessionKey, strings.TrimSpace(sessionKey))
	}
	return ctx
}

func withExecutionRunID(ctx context.Context, runID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(runID) != "" {
		ctx = context.WithValue(ctx, executionContextRunIDKey, strings.TrimSpace(runID))
	}
	return ctx
}

func withExecutionIsResume(ctx context.Context, isResume bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if isResume {
		ctx = context.WithValue(ctx, executionContextIsResumeKey, true)
	}
	return ctx
}

func toolExecutionChannel(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(executionContextChannelKey).(string)
	return strings.TrimSpace(v)
}

func toolExecutionChatID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(executionContextChatIDKey).(string)
	return strings.TrimSpace(v)
}

func toolExecutionSenderID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(executionContextSenderIDKey).(string)
	return strings.TrimSpace(v)
}

func toolExecutionSessionKey(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(executionContextSessionKey).(string)
	return strings.TrimSpace(v)
}

func toolExecutionRunID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(executionContextRunIDKey).(string)
	return strings.TrimSpace(v)
}

func toolExecutionIsResume(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(executionContextIsResumeKey).(bool)
	return v
}

// ExecutionSessionKey returns the session key associated with the current tool
// execution, if provided by the agent loop / tool executor.
func ExecutionSessionKey(ctx context.Context) string {
	return toolExecutionSessionKey(ctx)
}

// ExecutionRunID returns the run_id associated with the current tool execution,
// if provided by the agent loop / tool executor (Phase E2 resume support).
func ExecutionRunID(ctx context.Context) string {
	return toolExecutionRunID(ctx)
}

// ExecutionIsResume reports whether the current tool execution belongs to a
// resume_last_task flow (Phase E2), if provided by the agent loop / tool executor.
func ExecutionIsResume(ctx context.Context) bool {
	return toolExecutionIsResume(ctx)
}
