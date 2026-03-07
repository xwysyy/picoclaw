package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/xwysyy/X-Claw/pkg/utils"
)

// Tool is the interface that all tools must implement.
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	Execute(ctx context.Context, args map[string]any) *ToolResult
}

// ToolResult represents the structured return value from tool execution.
// It provides clear semantics for different types of results and supports
// async operations, user-facing messages, and error handling.
type ToolResult struct {
	ForLLM  string   `json:"for_llm"`
	ForUser string   `json:"for_user,omitempty"`
	Silent  bool     `json:"silent"`
	IsError bool     `json:"is_error"`
	Async   bool     `json:"async"`
	Err     error    `json:"-"`
	Media   []string `json:"media,omitempty"`
}

func NewToolResult(forLLM string) *ToolResult {
	return &ToolResult{ForLLM: forLLM}
}

func SilentResult(forLLM string) *ToolResult {
	return &ToolResult{ForLLM: forLLM, Silent: true, IsError: false, Async: false}
}

func AsyncResult(forLLM string) *ToolResult {
	return &ToolResult{ForLLM: forLLM, Silent: false, IsError: false, Async: true}
}

func ErrorResult(message string) *ToolResult {
	return &ToolResult{ForLLM: message, Silent: false, IsError: true, Async: false}
}

func UserResult(content string) *ToolResult {
	return &ToolResult{ForLLM: content, ForUser: content, Silent: false, IsError: false, Async: false}
}

func MediaResult(forLLM string, mediaRefs []string) *ToolResult {
	return &ToolResult{ForLLM: forLLM, Media: mediaRefs}
}

func (tr *ToolResult) MarshalJSON() ([]byte, error) {
	type Alias ToolResult
	return json.Marshal(&struct{ *Alias }{Alias: (*Alias)(tr)})
}

func ErrorWithSuggestion(message, suggestion string) *ToolResult {
	return &ToolResult{ForLLM: message + "\nSuggestion: " + suggestion, Silent: false, IsError: true, Async: false}
}

func (tr *ToolResult) WithError(err error) *ToolResult {
	tr.Err = err
	return tr
}

// --- Request-scoped tool context (channel / chatID) ---

type toolCtxKey struct{ name string }

var (
	ctxKeyChannel = &toolCtxKey{"channel"}
	ctxKeyChatID  = &toolCtxKey{"chatID"}
)

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

func ToolChannel(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyChannel).(string)
	v = strings.TrimSpace(v)
	if v != "" {
		return v
	}
	return toolExecutionChannel(ctx)
}

func ToolChatID(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyChatID).(string)
	v = strings.TrimSpace(v)
	if v != "" {
		return v
	}
	return toolExecutionChatID(ctx)
}

// AsyncCallback is a function type that async tools use to notify completion.
type AsyncCallback func(ctx context.Context, result *ToolResult)

// AsyncExecutor is an optional interface that tools can implement to support asynchronous execution.
type AsyncExecutor interface {
	Tool
	ExecuteAsync(ctx context.Context, args map[string]any, cb AsyncCallback) *ToolResult
}

// ToolParallelPolicy declares whether a tool is safe to run concurrently.
type ToolParallelPolicy string

const (
	ToolParallelSerialOnly ToolParallelPolicy = "serial_only"
	ToolParallelReadOnly   ToolParallelPolicy = "parallel_read_only"
)

const (
	ParallelToolsModeReadOnlyOnly = "read_only_only"
	ParallelToolsModeAll          = "all"
)

type ParallelPolicyProvider interface {
	ParallelPolicy() ToolParallelPolicy
}

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

type executionContextKey string

const (
	executionContextChannelKey  executionContextKey = "tool_execution_channel"
	executionContextChatIDKey   executionContextKey = "tool_execution_chat_id"
	executionContextSenderIDKey executionContextKey = "tool_execution_sender_id"
	executionContextSessionKey  executionContextKey = "tool_execution_session_key"
	executionContextRunIDKey    executionContextKey = "tool_execution_run_id"
	executionContextIsResumeKey executionContextKey = "tool_execution_is_resume"
	executionContextAsyncCBKey  executionContextKey = "tool_execution_async_callback"
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
	sessionKey = utils.CanonicalSessionKey(sessionKey)
	if sessionKey != "" {
		ctx = context.WithValue(ctx, executionContextSessionKey, sessionKey)
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

func withExecutionAsyncCallback(ctx context.Context, cb AsyncCallback) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if cb != nil {
		ctx = context.WithValue(ctx, executionContextAsyncCBKey, cb)
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
	return utils.CanonicalSessionKey(v)
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

func toolExecutionAsyncCallback(ctx context.Context) AsyncCallback {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(executionContextAsyncCBKey).(AsyncCallback)
	return v
}

func ExecutionSessionKey(ctx context.Context) string { return toolExecutionSessionKey(ctx) }
func ExecutionRunID(ctx context.Context) string      { return toolExecutionRunID(ctx) }
func ExecutionIsResume(ctx context.Context) bool     { return toolExecutionIsResume(ctx) }

func parseIntArg(args map[string]any, key string, defaultVal, minVal, maxVal int) (int, error) {
	val, exists := args[key]
	if !exists {
		return defaultVal, nil
	}
	n, err := toInt(val)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	if n < minVal || n > maxVal {
		return 0, fmt.Errorf("%s must be between %d and %d", key, minVal, maxVal)
	}
	return n, nil
}

func parseOptionalIntArg(args map[string]any, key string, defaultVal, minVal, maxVal int) (int, error) {
	val, exists := args[key]
	if !exists {
		return defaultVal, nil
	}
	n, err := toInt(val)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	if n < minVal || n > maxVal {
		return 0, fmt.Errorf("%s must be between %d and %d", key, minVal, maxVal)
	}
	return n, nil
}

func parseBoolArg(args map[string]any, key string, defaultVal bool) (bool, error) {
	val, exists := args[key]
	if !exists {
		return defaultVal, nil
	}
	b, ok := val.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return b, nil
}

func parseStringSliceArg(args map[string]any, key string) ([]string, error) {
	val, exists := args[key]
	if !exists {
		return nil, nil
	}
	raw, ok := val.([]any)
	if !ok {
		if s, ok := val.([]string); ok {
			out := make([]string, 0, len(s))
			for _, item := range s {
				item = strings.TrimSpace(item)
				if item != "" {
					out = append(out, item)
				}
			}
			return out, nil
		}
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be an array of strings", key)
		}
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

func getStringArg(args map[string]any, key string) (string, bool) {
	val, exists := args[key]
	if !exists {
		return "", false
	}
	s, ok := val.(string)
	if !ok {
		return "", false
	}
	return s, true
}

func toInt(v any) (int, error) {
	switch t := v.(type) {
	case int:
		return t, nil
	case int32:
		return int(t), nil
	case int64:
		if t > math.MaxInt || t < math.MinInt {
			return 0, fmt.Errorf("out of range")
		}
		return int(t), nil
	case float64:
		if t > float64(math.MaxInt) || t < float64(math.MinInt) {
			return 0, fmt.Errorf("out of range")
		}
		return int(t), nil
	default:
		return 0, fmt.Errorf("invalid type")
	}
}

type SendCallback func(ctx context.Context, channel, chatID, content string) error

type MessageTool struct {
	sendCallback SendCallback
}

func NewMessageTool() *MessageTool {
	return &MessageTool{}
}

func (t *MessageTool) Name() string {
	return "message"
}

func (t *MessageTool) Description() string {
	return "Send a message to user on a chat channel. " +
		"Input: content (string, required), channel (string, optional), chat_id (string, optional). " +
		"Output: confirmation that the message was sent (silent — user receives the message directly). " +
		"If channel/chat_id are omitted, uses the current conversation context. " +
		"Use this to proactively communicate results, progress updates, or ask follow-up questions."
}

func (t *MessageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content": map[string]any{
				"type":        "string",
				"description": "The message content to send",
			},
			"channel": map[string]any{
				"type":        "string",
				"description": "Optional: target channel (telegram, whatsapp, etc.)",
			},
			"chat_id": map[string]any{
				"type":        "string",
				"description": "Optional: target chat/user ID",
			},
		},
		"required": []string{"content"},
	}
}

func (t *MessageTool) SetSendCallback(callback SendCallback) {
	t.sendCallback = callback
}

func (t *MessageTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	content, ok := args["content"].(string)
	if !ok {
		return &ToolResult{ForLLM: "content is required", IsError: true}
	}

	channel, _ := args["channel"].(string)
	chatID, _ := args["chat_id"].(string)

	if channel == "" {
		channel = toolExecutionChannel(ctx)
	}
	if chatID == "" {
		chatID = toolExecutionChatID(ctx)
	}

	if channel == "" || chatID == "" {
		return &ToolResult{ForLLM: "No target channel/chat specified", IsError: true}
	}

	if t.sendCallback == nil {
		return &ToolResult{ForLLM: "Message sending not configured", IsError: true}
	}

	if err := t.sendCallback(ctx, channel, chatID, content); err != nil {
		return &ToolResult{
			ForLLM:  fmt.Sprintf("sending message: %v", err),
			IsError: true,
			Err:     err,
		}
	}

	if tracker := messageRoundTrackerFromContext(ctx); tracker != nil {
		tracker.MarkSent()
	}
	// Silent: user already received the message directly
	return &ToolResult{
		ForLLM: fmt.Sprintf("Message sent to %s:%s", channel, chatID),
		Silent: true,
	}
}
