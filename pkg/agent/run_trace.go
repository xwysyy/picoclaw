package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/utils"
)

type runTraceWriter struct {
	enabled bool
	scope   string

	runID      string
	sessionKey string
	channel    string
	chatID     string
	senderID   string
	agentID    string
	model      string

	dir        string
	eventsPath string

	maxPreviewChars int

	mu sync.Mutex
}

type runTraceEvent struct {
	Type string `json:"type"`

	TS   string `json:"ts"`
	TSMS int64  `json:"ts_ms"`

	RunID      string `json:"run_id"`
	SessionKey string `json:"session_key,omitempty"`
	Channel    string `json:"channel,omitempty"`
	ChatID     string `json:"chat_id,omitempty"`
	SenderID   string `json:"sender_id,omitempty"`

	AgentID string `json:"agent_id,omitempty"`
	Model   string `json:"model,omitempty"`

	Iteration int `json:"iteration,omitempty"`

	UserMessagePreview string `json:"user_message_preview,omitempty"`
	UserMessageChars   int    `json:"user_message_chars,omitempty"`

	MessagesCount int `json:"messages_count,omitempty"`
	ToolsCount    int `json:"tools_count,omitempty"`

	ResponsePreview string   `json:"response_preview,omitempty"`
	ToolCalls       []string `json:"tool_calls,omitempty"`

	ToolBatch []runTraceToolExec `json:"tool_batch,omitempty"`

	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`

	Error string `json:"error,omitempty"`
}

type runTraceToolExec struct {
	Tool       string `json:"tool"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	Preview    string `json:"preview,omitempty"`
}

func newRunTraceWriter(workspace string, enabled bool, opts processOptions, agentID, model string) *runTraceWriter {
	if !enabled {
		return nil
	}

	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil
	}

	sessionKey := strings.TrimSpace(opts.SessionKey)
	if sessionKey == "" {
		// Should not happen in normal agent loop, but keep best-effort.
		sessionKey = strings.TrimSpace(opts.Channel) + ":" + strings.TrimSpace(opts.ChatID)
	}
	dirKey := tools.SafePathToken(sessionKey)
	if dirKey == "" {
		dirKey = "unknown"
	}

	dir := filepath.Join(workspace, ".picoclaw", "audit", "runs", dirKey)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.WarnCF("agent", "Run trace disabled: failed to create directory", map[string]any{
			"dir": dir,
			"err": err.Error(),
		})
		return nil
	}

	runID := strings.TrimSpace(opts.RunID)
	if runID == "" {
		runID = uuid.NewString()
	}

	return &runTraceWriter{
		enabled: true,
		scope:   "agent",

		runID:      runID,
		sessionKey: sessionKey,
		channel:    strings.TrimSpace(opts.Channel),
		chatID:     strings.TrimSpace(opts.ChatID),
		senderID:   strings.TrimSpace(opts.SenderID),
		agentID:    strings.TrimSpace(agentID),
		model:      strings.TrimSpace(model),

		dir:        dir,
		eventsPath: filepath.Join(dir, "events.jsonl"),

		maxPreviewChars: 400,
	}
}

func (w *runTraceWriter) RunID() string {
	if w == nil {
		return ""
	}
	return w.runID
}

func (w *runTraceWriter) recordStart(userMessage string, messagesCount, toolsCount int) {
	if w == nil || !w.enabled {
		return
	}
	ts := time.Now()
	w.appendEvent(runTraceEvent{
		Type: "run.start",

		TS:   ts.UTC().Format(time.RFC3339Nano),
		TSMS: ts.UnixMilli(),

		RunID:      w.runID,
		SessionKey: w.sessionKey,
		Channel:    w.channel,
		ChatID:     w.chatID,
		SenderID:   w.senderID,

		AgentID: w.agentID,
		Model:   w.model,

		UserMessagePreview: utils.Truncate(strings.TrimSpace(userMessage), w.maxPreviewChars),
		UserMessageChars:   len(userMessage),
		MessagesCount:      messagesCount,
		ToolsCount:         toolsCount,
	})
}

func (w *runTraceWriter) recordResume(userMessage string, messagesCount, toolsCount int) {
	if w == nil || !w.enabled {
		return
	}
	ts := time.Now()
	w.appendEvent(runTraceEvent{
		Type: "run.resume",

		TS:   ts.UTC().Format(time.RFC3339Nano),
		TSMS: ts.UnixMilli(),

		RunID:      w.runID,
		SessionKey: w.sessionKey,
		Channel:    w.channel,
		ChatID:     w.chatID,
		SenderID:   w.senderID,

		AgentID: w.agentID,
		Model:   w.model,

		UserMessagePreview: utils.Truncate(strings.TrimSpace(userMessage), w.maxPreviewChars),
		UserMessageChars:   len(userMessage),
		MessagesCount:      messagesCount,
		ToolsCount:         toolsCount,
	})
}

func (w *runTraceWriter) recordLLMRequest(iteration int, messagesCount, toolsCount int) {
	if w == nil || !w.enabled {
		return
	}
	ts := time.Now()
	w.appendEvent(runTraceEvent{
		Type: "llm.request",

		TS:   ts.UTC().Format(time.RFC3339Nano),
		TSMS: ts.UnixMilli(),

		RunID:      w.runID,
		SessionKey: w.sessionKey,
		Channel:    w.channel,
		ChatID:     w.chatID,
		SenderID:   w.senderID,

		AgentID: w.agentID,
		Model:   w.model,

		Iteration:     iteration,
		MessagesCount: messagesCount,
		ToolsCount:    toolsCount,
	})
}

func (w *runTraceWriter) recordLLMResponse(iteration int, content string, toolCalls []string, usage *providers.UsageInfo) {
	if w == nil || !w.enabled {
		return
	}
	ts := time.Now()

	event := runTraceEvent{
		Type: "llm.response",

		TS:   ts.UTC().Format(time.RFC3339Nano),
		TSMS: ts.UnixMilli(),

		RunID:      w.runID,
		SessionKey: w.sessionKey,
		Channel:    w.channel,
		ChatID:     w.chatID,
		SenderID:   w.senderID,

		AgentID: w.agentID,
		Model:   w.model,

		Iteration:       iteration,
		ResponsePreview: utils.Truncate(strings.TrimSpace(content), w.maxPreviewChars),
		ToolCalls:       toolCalls,
	}
	if usage != nil {
		event.PromptTokens = usage.PromptTokens
		event.CompletionTokens = usage.CompletionTokens
		event.TotalTokens = usage.TotalTokens
	}

	w.appendEvent(event)
}

func (w *runTraceWriter) recordToolBatch(iteration int, execs []tools.ToolCallExecution) {
	if w == nil || !w.enabled || len(execs) == 0 {
		return
	}
	ts := time.Now()

	batch := make([]runTraceToolExec, 0, len(execs))
	for _, ex := range execs {
		preview := ""
		if ex.Result != nil {
			preview = ex.Result.ForLLM
			if preview == "" {
				preview = ex.Result.ForUser
			}
			if preview == "" && ex.Result.Err != nil {
				preview = ex.Result.Err.Error()
			}
		}
		batch = append(batch, runTraceToolExec{
			Tool:       ex.ToolCall.Name,
			ToolCallID: ex.ToolCall.ID,
			DurationMS: ex.DurationMS,
			IsError:    ex.Result != nil && ex.Result.IsError,
			Preview:    utils.Truncate(strings.TrimSpace(preview), w.maxPreviewChars),
		})
	}

	w.appendEvent(runTraceEvent{
		Type: "tool.batch",

		TS:   ts.UTC().Format(time.RFC3339Nano),
		TSMS: ts.UnixMilli(),

		RunID:      w.runID,
		SessionKey: w.sessionKey,
		Channel:    w.channel,
		ChatID:     w.chatID,
		SenderID:   w.senderID,

		AgentID: w.agentID,
		Model:   w.model,

		Iteration: iteration,
		ToolBatch: batch,
	})
}

func (w *runTraceWriter) recordEnd(iterations int, finalContent string) {
	if w == nil || !w.enabled {
		return
	}
	ts := time.Now()
	w.appendEvent(runTraceEvent{
		Type: "run.end",

		TS:   ts.UTC().Format(time.RFC3339Nano),
		TSMS: ts.UnixMilli(),

		RunID:      w.runID,
		SessionKey: w.sessionKey,
		Channel:    w.channel,
		ChatID:     w.chatID,
		SenderID:   w.senderID,

		AgentID: w.agentID,
		Model:   w.model,

		Iteration:       iterations,
		ResponsePreview: utils.Truncate(strings.TrimSpace(finalContent), w.maxPreviewChars),
	})
}

func (w *runTraceWriter) recordError(iteration int, err error) {
	if w == nil || !w.enabled || err == nil {
		return
	}
	ts := time.Now()
	w.appendEvent(runTraceEvent{
		Type: "run.error",

		TS:   ts.UTC().Format(time.RFC3339Nano),
		TSMS: ts.UnixMilli(),

		RunID:      w.runID,
		SessionKey: w.sessionKey,
		Channel:    w.channel,
		ChatID:     w.chatID,
		SenderID:   w.senderID,

		AgentID: w.agentID,
		Model:   w.model,

		Iteration: iteration,
		Error:     utils.Truncate(err.Error(), 1200),
	})
}

func (w *runTraceWriter) appendEvent(event runTraceEvent) {
	if w == nil || !w.enabled {
		return
	}

	payload, err := json.Marshal(event)
	if err != nil {
		logger.WarnCF(w.scope, "Run trace: failed to marshal event", map[string]any{
			"err": err.Error(),
		})
		return
	}
	payload = append(payload, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()

	f, err := os.OpenFile(w.eventsPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		logger.WarnCF(w.scope, "Run trace: failed to open events file", map[string]any{
			"path": w.eventsPath,
			"err":  err.Error(),
		})
		return
	}
	defer f.Close()

	if _, err := f.Write(payload); err != nil {
		logger.WarnCF(w.scope, "Run trace: failed to append event", map[string]any{
			"path": w.eventsPath,
			"err":  err.Error(),
		})
		return
	}
	_ = f.Sync()
}

func (w *runTraceWriter) String() string {
	if w == nil {
		return ""
	}
	return fmt.Sprintf("runTrace(run_id=%s, path=%s)", w.runID, w.eventsPath)
}
