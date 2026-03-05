package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/xwysyy/X-Claw/internal/core/events"
	"github.com/xwysyy/X-Claw/pkg/fileutil"
	"github.com/xwysyy/X-Claw/pkg/logger"
	"github.com/xwysyy/X-Claw/pkg/providers"
	"github.com/xwysyy/X-Claw/pkg/utils"
)

// ToolTraceOptions controls optional on-disk tracing for tool calls.
//
// This is intentionally "tools-layer" (not provider-layer): it traces actual
// tool executions (name + arguments + result) rather than model output.
type ToolTraceOptions struct {
	Enabled bool

	// Dir overrides the default per-session trace directory.
	// When empty, tracing falls back to: <workspace>/.x-claw/audit/tools/<session>/
	Dir string

	// WritePerCallFiles writes one JSON + one Markdown file per call.
	WritePerCallFiles bool

	// MaxArgPreviewChars limits args preview in JSONL events.
	MaxArgPreviewChars int
	// MaxResultPreviewChars limits output preview in JSONL events.
	MaxResultPreviewChars int
}

type toolTraceWriter struct {
	scope string

	enabled bool
	dir     string

	runID string

	sessionKey string
	channel    string
	chatID     string
	senderID   string

	eventsPath string

	writePerCallFiles  bool
	perCallDir         string
	maxArgPreviewChars int
	maxResPreviewChars int

	policyTags map[string]string

	mu sync.Mutex
}

type toolTraceEvent struct {
	Type events.Type `json:"type"`

	TS   string `json:"ts"`
	TSMS int64  `json:"ts_ms"`

	RunID string `json:"run_id,omitempty"`

	SessionKey string `json:"session_key,omitempty"`
	Channel    string `json:"channel,omitempty"`
	ChatID     string `json:"chat_id,omitempty"`
	SenderID   string `json:"sender_id,omitempty"`

	Iteration  int    `json:"iteration,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Tool       string `json:"tool"`

	PolicyDecision  string            `json:"policy_decision,omitempty"`
	PolicyReason    string            `json:"policy_reason,omitempty"`
	PolicyTimeoutMS int               `json:"policy_timeout_ms,omitempty"`
	IdempotencyKey  string            `json:"idempotency_key,omitempty"`
	PolicyTags      map[string]string `json:"policy_tags,omitempty"`
	HookActions     []ToolHookAction  `json:"hook_actions,omitempty"`

	Args        json.RawMessage `json:"args,omitempty"`
	ArgsPreview string          `json:"args_preview,omitempty"`

	DurationMS int64  `json:"duration_ms,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	Error      string `json:"error,omitempty"`

	ForLLMPreview  string `json:"for_llm_preview,omitempty"`
	ForUserPreview string `json:"for_user_preview,omitempty"`
	Silent         bool   `json:"silent,omitempty"`
	Async          bool   `json:"async,omitempty"`
	MediaCount     int    `json:"media_count,omitempty"`
}

type toolTraceSnapshot struct {
	Kind string `json:"kind"`

	TS   string `json:"ts"`
	TSMS int64  `json:"ts_ms"`

	RunID string `json:"run_id,omitempty"`

	SessionKey string `json:"session_key,omitempty"`
	Channel    string `json:"channel,omitempty"`
	ChatID     string `json:"chat_id,omitempty"`
	SenderID   string `json:"sender_id,omitempty"`

	Iteration  int    `json:"iteration,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Tool       string `json:"tool"`

	PolicyDecision  string            `json:"policy_decision,omitempty"`
	PolicyReason    string            `json:"policy_reason,omitempty"`
	PolicyTimeoutMS int               `json:"policy_timeout_ms,omitempty"`
	IdempotencyKey  string            `json:"idempotency_key,omitempty"`
	PolicyTags      map[string]string `json:"policy_tags,omitempty"`
	HookActions     []ToolHookAction  `json:"hook_actions,omitempty"`

	Args json.RawMessage `json:"args,omitempty"`

	DurationMS int64 `json:"duration_ms,omitempty"`

	Result *ToolResult `json:"result,omitempty"`
	// ErrString is a lossy error representation (ToolResult.Err is not serialized).
	ErrString string `json:"err,omitempty"`
}

func newToolTraceWriter(opts ToolCallExecutionOptions, scope string) *toolTraceWriter {
	if !opts.Trace.Enabled {
		return nil
	}

	dir := strings.TrimSpace(opts.Trace.Dir)
	effectiveSessionKey := utils.CanonicalSessionKey(opts.SessionKey)
	if effectiveSessionKey == "" {
		effectiveSessionKey = utils.CanonicalSessionKey(strings.TrimSpace(opts.Channel) + ":" + strings.TrimSpace(opts.ChatID))
	}

	if dir == "" {
		workspace := strings.TrimSpace(opts.Workspace)
		if workspace == "" {
			return nil
		}

		dirKey := SafePathToken(effectiveSessionKey)
		if dirKey == "" {
			dirKey = "unknown"
		}

		dir = filepath.Join(workspace, ".x-claw", "audit", "tools", dirKey)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.WarnCF(scope, "Tool trace disabled: failed to create trace directory", map[string]any{
			"dir": dir,
			"err": err.Error(),
		})
		return nil
	}

	maxArgPreview := opts.Trace.MaxArgPreviewChars
	if maxArgPreview <= 0 {
		maxArgPreview = 200
	}
	maxResPreview := opts.Trace.MaxResultPreviewChars
	if maxResPreview <= 0 {
		maxResPreview = 400
	}

	w := &toolTraceWriter{
		scope: scope,

		enabled: true,
		dir:     dir,

		runID: strings.TrimSpace(opts.RunID),

		sessionKey: effectiveSessionKey,
		channel:    strings.TrimSpace(opts.Channel),
		chatID:     strings.TrimSpace(opts.ChatID),
		senderID:   strings.TrimSpace(opts.SenderID),

		eventsPath: filepath.Join(dir, "events.jsonl"),

		writePerCallFiles:  opts.Trace.WritePerCallFiles,
		perCallDir:         filepath.Join(dir, "calls"),
		maxArgPreviewChars: maxArgPreview,
		maxResPreviewChars: maxResPreview,

		policyTags: copyStringMap(opts.PolicyTags),
	}

	if w.writePerCallFiles {
		if err := os.MkdirAll(w.perCallDir, 0o755); err != nil {
			logger.WarnCF(scope, "Tool trace per-call files disabled: failed to create directory", map[string]any{
				"dir": w.perCallDir,
				"err": err.Error(),
			})
			w.writePerCallFiles = false
		}
	}

	return w
}

func (w *toolTraceWriter) RecordStart(ts time.Time, iteration int, tc providers.ToolCall, argsJSON []byte) {
	if w == nil || !w.enabled {
		return
	}

	event := toolTraceEvent{
		Type: events.ToolStart,
		TS:   ts.UTC().Format(time.RFC3339Nano),
		TSMS: ts.UnixMilli(),

		RunID: w.runID,

		SessionKey: w.sessionKey,
		Channel:    w.channel,
		ChatID:     w.chatID,
		SenderID:   w.senderID,

		Iteration:  iteration,
		ToolCallID: tc.ID,
		Tool:       tc.Name,

		PolicyTags: w.policyTags,

		Args:        argsJSON,
		ArgsPreview: utils.Truncate(string(argsJSON), w.maxArgPreviewChars),
	}

	w.appendEvent(event)
}

func (w *toolTraceWriter) RecordEnd(
	ts time.Time,
	iteration int,
	tc providers.ToolCall,
	argsJSON []byte,
	result *ToolResult,
	duration time.Duration,
	policyDecision string,
	policyReason string,
	policyTimeoutMS int,
	idempotencyKey string,
	hookActions []ToolHookAction,
) {
	if w == nil || !w.enabled {
		return
	}

	var errText string
	if result != nil && result.Err != nil {
		errText = result.Err.Error()
	}

	forLLM := ""
	forUser := ""
	silent := false
	async := false
	isError := false
	mediaCount := 0
	if result != nil {
		forLLM = result.ForLLM
		forUser = result.ForUser
		silent = result.Silent
		async = result.Async
		isError = result.IsError
		mediaCount = len(result.Media)
	}
	if strings.TrimSpace(errText) == "" && isError {
		// Fall back to the tool-facing error content when no underlying error is attached.
		errText = forLLM
	}

	event := toolTraceEvent{
		Type: events.ToolEnd,
		TS:   ts.UTC().Format(time.RFC3339Nano),
		TSMS: ts.UnixMilli(),

		RunID: w.runID,

		SessionKey: w.sessionKey,
		Channel:    w.channel,
		ChatID:     w.chatID,
		SenderID:   w.senderID,

		Iteration:  iteration,
		ToolCallID: tc.ID,
		Tool:       tc.Name,

		PolicyDecision:  strings.TrimSpace(policyDecision),
		PolicyReason:    utils.Truncate(strings.TrimSpace(policyReason), 200),
		PolicyTimeoutMS: policyTimeoutMS,
		IdempotencyKey:  strings.TrimSpace(idempotencyKey),
		PolicyTags:      w.policyTags,
		HookActions:     normalizeHookActions(hookActions),

		Args:        argsJSON,
		ArgsPreview: utils.Truncate(string(argsJSON), w.maxArgPreviewChars),

		DurationMS: duration.Milliseconds(),
		IsError:    isError,
		Error:      utils.Truncate(strings.TrimSpace(errText), 800),

		ForLLMPreview:  utils.Truncate(strings.TrimSpace(forLLM), w.maxResPreviewChars),
		ForUserPreview: utils.Truncate(strings.TrimSpace(forUser), w.maxResPreviewChars),
		Silent:         silent,
		Async:          async,
		MediaCount:     mediaCount,
	}

	w.appendEvent(event)

	if w.writePerCallFiles {
		w.writeSnapshot(ts, iteration, tc, argsJSON, result, duration, policyDecision, policyReason, policyTimeoutMS, idempotencyKey, hookActions)
	}
}

func (w *toolTraceWriter) appendEvent(event toolTraceEvent) {
	if w == nil || !w.enabled {
		return
	}

	payload, err := json.Marshal(event)
	if err != nil {
		logger.WarnCF(w.scope, "Tool trace: failed to marshal event", map[string]any{
			"err": err.Error(),
		})
		return
	}
	payload = append(payload, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()

	f, err := os.OpenFile(w.eventsPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		logger.WarnCF(w.scope, "Tool trace: failed to open events file", map[string]any{
			"path": w.eventsPath,
			"err":  err.Error(),
		})
		return
	}
	defer f.Close()

	if _, err := f.Write(payload); err != nil {
		logger.WarnCF(w.scope, "Tool trace: failed to append event", map[string]any{
			"path": w.eventsPath,
			"err":  err.Error(),
		})
		return
	}
	_ = f.Sync()
}

func (w *toolTraceWriter) writeSnapshot(
	ts time.Time,
	iteration int,
	tc providers.ToolCall,
	argsJSON []byte,
	result *ToolResult,
	duration time.Duration,
	policyDecision string,
	policyReason string,
	policyTimeoutMS int,
	idempotencyKey string,
	hookActions []ToolHookAction,
) {
	if w == nil || !w.enabled || !w.writePerCallFiles {
		return
	}

	errText := ""
	if result != nil && result.Err != nil {
		errText = result.Err.Error()
	}
	if strings.TrimSpace(errText) == "" && result != nil && result.IsError {
		errText = result.ForLLM
	}

	snapshot := toolTraceSnapshot{
		Kind: "tool_call_snapshot",

		TS:   ts.UTC().Format(time.RFC3339Nano),
		TSMS: ts.UnixMilli(),

		RunID: w.runID,

		SessionKey: w.sessionKey,
		Channel:    w.channel,
		ChatID:     w.chatID,
		SenderID:   w.senderID,

		Iteration:  iteration,
		ToolCallID: tc.ID,
		Tool:       tc.Name,

		PolicyDecision:  strings.TrimSpace(policyDecision),
		PolicyReason:    utils.Truncate(strings.TrimSpace(policyReason), 200),
		PolicyTimeoutMS: policyTimeoutMS,
		IdempotencyKey:  strings.TrimSpace(idempotencyKey),
		PolicyTags:      w.policyTags,
		HookActions:     normalizeHookActions(hookActions),

		Args: argsJSON,

		DurationMS: duration.Milliseconds(),
		Result:     result,
		ErrString:  errText,
	}

	base := fmt.Sprintf(
		"%s_iter%03d_%s_%s",
		ts.UTC().Format("20060102T150405.000Z0700"),
		iteration,
		SafePathToken(tc.Name),
		SafePathToken(tc.ID),
	)
	if base == "" {
		base = fmt.Sprintf("%d_iter%03d", ts.UnixMilli(), iteration)
	}

	jsonPath := filepath.Join(w.perCallDir, base+".json")
	mdPath := filepath.Join(w.perCallDir, base+".md")

	if payload, err := json.MarshalIndent(snapshot, "", "  "); err == nil {
		if err := fileutil.WriteFileAtomic(jsonPath, payload, 0o600); err != nil {
			logger.WarnCF(w.scope, "Tool trace: failed to write snapshot json", map[string]any{
				"path": jsonPath,
				"err":  err.Error(),
			})
		}
	}

	md := renderToolSnapshotMarkdown(snapshot)
	if md != "" {
		if err := fileutil.WriteFileAtomic(mdPath, []byte(md), 0o600); err != nil {
			logger.WarnCF(w.scope, "Tool trace: failed to write snapshot markdown", map[string]any{
				"path": mdPath,
				"err":  err.Error(),
			})
		}
	}
}

func renderToolSnapshotMarkdown(s toolTraceSnapshot) string {
	var sb strings.Builder
	sb.WriteString("# Tool Call Trace\n\n")
	sb.WriteString(fmt.Sprintf("- ts: %s\n", s.TS))
	if s.RunID != "" {
		sb.WriteString(fmt.Sprintf("- run_id: %s\n", s.RunID))
	}
	if s.SessionKey != "" {
		sb.WriteString(fmt.Sprintf("- session_key: %s\n", s.SessionKey))
	}
	if s.Channel != "" || s.ChatID != "" {
		sb.WriteString(fmt.Sprintf("- channel: %s\n", s.Channel))
		sb.WriteString(fmt.Sprintf("- chat_id: %s\n", s.ChatID))
	}
	if s.SenderID != "" {
		sb.WriteString(fmt.Sprintf("- sender_id: %s\n", s.SenderID))
	}
	sb.WriteString(fmt.Sprintf("- iteration: %d\n", s.Iteration))
	sb.WriteString(fmt.Sprintf("- tool: %s\n", s.Tool))
	if s.ToolCallID != "" {
		sb.WriteString(fmt.Sprintf("- tool_call_id: %s\n", s.ToolCallID))
	}
	if strings.TrimSpace(s.PolicyDecision) != "" {
		sb.WriteString(fmt.Sprintf("- policy_decision: %s\n", strings.TrimSpace(s.PolicyDecision)))
	}
	if strings.TrimSpace(s.IdempotencyKey) != "" {
		sb.WriteString(fmt.Sprintf("- idempotency_key: %s\n", strings.TrimSpace(s.IdempotencyKey)))
	}
	if len(s.PolicyTags) > 0 {
		// Keep it compact and deterministic.
		sb.WriteString(fmt.Sprintf("- policy_tags: %s\n", toolPolicyTagsToString(s.PolicyTags)))
	}
	sb.WriteString(fmt.Sprintf("- duration_ms: %d\n", s.DurationMS))
	if s.Result != nil {
		sb.WriteString(fmt.Sprintf("- is_error: %v\n", s.Result.IsError))
		sb.WriteString(fmt.Sprintf("- async: %v\n", s.Result.Async))
		sb.WriteString(fmt.Sprintf("- silent: %v\n", s.Result.Silent))
		if len(s.Result.Media) > 0 {
			sb.WriteString(fmt.Sprintf("- media_count: %d\n", len(s.Result.Media)))
		}
	}
	if strings.TrimSpace(s.ErrString) != "" {
		sb.WriteString(fmt.Sprintf("- err: %s\n", strings.TrimSpace(s.ErrString)))
	}
	if len(s.HookActions) > 0 {
		sb.WriteString(fmt.Sprintf("- hook_actions: %d\n", len(s.HookActions)))
	}

	sb.WriteString("\n## Arguments\n\n```json\n")
	if len(s.Args) > 0 {
		sb.WriteString(string(s.Args))
	} else {
		sb.WriteString("{}")
	}
	sb.WriteString("\n```\n\n")

	if s.Result != nil {
		if strings.TrimSpace(s.Result.ForLLM) != "" {
			sb.WriteString("## ForLLM\n\n```\n")
			sb.WriteString(s.Result.ForLLM)
			sb.WriteString("\n```\n\n")
		}
		if strings.TrimSpace(s.Result.ForUser) != "" {
			sb.WriteString("## ForUser\n\n```\n")
			sb.WriteString(s.Result.ForUser)
			sb.WriteString("\n```\n\n")
		}
	}

	if len(s.HookActions) > 0 {
		sb.WriteString("## Hook Actions\n\n```json\n")
		if payload, err := json.MarshalIndent(s.HookActions, "", "  "); err == nil && len(payload) > 0 {
			sb.Write(payload)
		} else {
			sb.WriteString("[]")
		}
		sb.WriteString("\n```\n\n")
	}

	return sb.String()
}

func normalizeHookActions(in []ToolHookAction) []ToolHookAction {
	if len(in) == 0 {
		return nil
	}

	out := make([]ToolHookAction, 0, len(in))
	for _, a := range in {
		a.Hook = strings.TrimSpace(a.Hook)
		a.Stage = strings.TrimSpace(a.Stage)
		a.Decision = strings.TrimSpace(a.Decision)
		a.Reason = utils.Truncate(strings.TrimSpace(a.Reason), 400)
		if a.Hook == "" && a.Stage == "" && a.Decision == "" && a.Reason == "" {
			continue
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

var safeTokenRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// SafePathToken converts an arbitrary string into a filesystem-friendly token.
// It is used for tool trace directory names and per-call filenames.
func SafePathToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = safeTokenRe.ReplaceAllString(s, "_")
	s = strings.Trim(s, "._-")
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}
