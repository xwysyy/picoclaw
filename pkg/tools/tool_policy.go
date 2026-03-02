package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/utils"
)

type toolPolicyDecision string

const (
	toolPolicyDecisionAllow            toolPolicyDecision = "allow"
	toolPolicyDecisionDeny             toolPolicyDecision = "deny"
	toolPolicyDecisionConfirmRequired  toolPolicyDecision = "confirm_required"
	toolPolicyDecisionIdempotentReplay toolPolicyDecision = "idempotent_replay"
)

type toolPolicy struct {
	cfg config.ToolPolicyConfig

	workspace  string
	sessionKey string
	runID      string
	isResume   bool

	enabled bool

	allowSet      map[string]struct{}
	allowPrefixes []string
	denySet       map[string]struct{}
	denyPrefixes  []string

	timeout time.Duration

	redactEnabled     bool
	redactApplyToLLM  bool
	redactApplyToUser bool
	redactFields      map[string]struct{}
	redactPatterns    []*regexp.Regexp

	confirmEnabled  bool
	confirmMode     string
	confirmTools    map[string]struct{}
	confirmPrefixes []string
	confirmExpires  time.Duration

	idempotencyEnabled     bool
	idempotencyCacheResult bool
	idempotencyTools       map[string]struct{}
	idempotencyPrefixes    []string

	auditTags map[string]string

	store *toolPolicyStore
}

func newToolPolicy(workspace, sessionKey, runID string, isResume bool, cfg config.ToolPolicyConfig) *toolPolicy {
	p := &toolPolicy{
		cfg:        cfg,
		workspace:  strings.TrimSpace(workspace),
		sessionKey: strings.TrimSpace(sessionKey),
		runID:      strings.TrimSpace(runID),
		isResume:   isResume,
		enabled:    cfg.Enabled,

		auditTags: copyStringMap(cfg.Audit.Tags),
	}

	p.allowSet, p.allowPrefixes = normalizeToolMatchers(cfg.Allow, cfg.AllowPrefixes)
	p.denySet, p.denyPrefixes = normalizeToolMatchers(cfg.Deny, cfg.DenyPrefixes)

	if cfg.TimeoutMS > 0 {
		p.timeout = time.Duration(cfg.TimeoutMS) * time.Millisecond
	}

	p.redactEnabled = cfg.Redact.Enabled
	p.redactApplyToLLM = cfg.Redact.ApplyToLLM
	p.redactApplyToUser = cfg.Redact.ApplyToUser
	p.redactFields = make(map[string]struct{})
	for _, f := range cfg.Redact.JSONFields {
		f = strings.ToLower(strings.TrimSpace(f))
		if f != "" {
			p.redactFields[f] = struct{}{}
		}
	}
	p.redactPatterns = compileRedactionRegexes(cfg.Redact.Patterns)

	p.confirmEnabled = cfg.Confirm.Enabled
	p.confirmMode = strings.ToLower(strings.TrimSpace(cfg.Confirm.Mode))
	p.confirmTools, p.confirmPrefixes = normalizeToolMatchers(cfg.Confirm.Tools, cfg.Confirm.ToolPrefixes)
	if cfg.Confirm.ExpiresSeconds > 0 {
		p.confirmExpires = time.Duration(cfg.Confirm.ExpiresSeconds) * time.Second
	} else {
		p.confirmExpires = 15 * time.Minute
	}

	p.idempotencyEnabled = cfg.Idempotency.Enabled
	p.idempotencyCacheResult = cfg.Idempotency.CacheResult
	p.idempotencyTools, p.idempotencyPrefixes = normalizeToolMatchers(cfg.Idempotency.Tools, cfg.Idempotency.ToolPrefixes)

	if p.enabled && (p.confirmEnabled || p.idempotencyEnabled) {
		p.store = getToolPolicyStore(p.workspace, p.sessionKey, p.runID)
	}

	return p
}

func normalizeToolMatchers(exact []string, prefixes []string) (set map[string]struct{}, normalizedPrefixes []string) {
	set = make(map[string]struct{})
	for _, v := range exact {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "" {
			set[v] = struct{}{}
		}
	}
	normalizedPrefixes = make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			normalizedPrefixes = append(normalizedPrefixes, p)
		}
	}
	// Deterministic ordering: longest prefix first to avoid accidental shadowing surprises.
	sort.Slice(normalizedPrefixes, func(i, j int) bool { return len(normalizedPrefixes[i]) > len(normalizedPrefixes[j]) })
	return set, normalizedPrefixes
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
			logger.WarnCF("tools", "tool policy: invalid redact regex", map[string]any{
				"pattern": raw,
				"error":   err.Error(),
			})
			continue
		}
		out = append(out, re)
	}
	return out
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out[k] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (p *toolPolicy) policyDisabled() bool {
	return p == nil || !p.enabled
}

func (p *toolPolicy) isToolAllowed(toolName string) (allowed bool, reason string) {
	name := strings.ToLower(strings.TrimSpace(toolName))
	if name == "" {
		return false, "tool name is empty"
	}

	if len(p.allowSet) > 0 || len(p.allowPrefixes) > 0 {
		if _, ok := p.allowSet[name]; ok {
			// ok
		} else if hasAnyPrefix(name, p.allowPrefixes) {
			// ok
		} else {
			return false, "tool not in allow list"
		}
	}

	if _, ok := p.denySet[name]; ok {
		return false, "tool in deny list"
	}
	if hasAnyPrefix(name, p.denyPrefixes) {
		return false, "tool matches deny prefix"
	}

	return true, ""
}

func hasAnyPrefix(name string, prefixes []string) bool {
	if name == "" || len(prefixes) == 0 {
		return false
	}
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func (p *toolPolicy) shouldRequireConfirmation(toolName string) bool {
	if p == nil || !p.enabled || !p.confirmEnabled {
		return false
	}
	switch p.confirmMode {
	case "", "resume_only":
		if !p.isResume {
			return false
		}
	case "always":
		// ok
	case "never":
		return false
	default:
		// Unknown mode: safe default is "resume_only".
		if !p.isResume {
			return false
		}
	}

	name := strings.ToLower(strings.TrimSpace(toolName))
	if name == "" {
		return false
	}
	if _, ok := p.confirmTools[name]; ok {
		return true
	}
	if hasAnyPrefix(name, p.confirmPrefixes) {
		return true
	}
	return false
}

func (p *toolPolicy) shouldBeIdempotent(toolName string) bool {
	if p == nil || !p.enabled || !p.idempotencyEnabled {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(toolName))
	if name == "" {
		return false
	}
	if _, ok := p.idempotencyTools[name]; ok {
		return true
	}
	if hasAnyPrefix(name, p.idempotencyPrefixes) {
		return true
	}
	return false
}

func (p *toolPolicy) toolTimeoutContext(ctx context.Context) (context.Context, context.CancelFunc, int) {
	if p == nil || !p.enabled || p.timeout <= 0 {
		return ctx, func() {}, 0
	}

	// Respect a tighter existing deadline. Only shorten.
	deadline, hasDeadline := ctx.Deadline()
	if hasDeadline {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return ctx, func() {}, 0
		}
		if remaining <= p.timeout {
			return ctx, func() {}, int(remaining.Milliseconds())
		}
	}

	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	return cctx, cancel, int(p.timeout.Milliseconds())
}

func (p *toolPolicy) redactJSONBytes(data []byte) []byte {
	if p == nil || !p.enabled || !p.redactEnabled {
		return data
	}
	data = bytesTrimSpace(data)
	if len(data) == 0 {
		return data
	}

	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		// Fallback: regex-based redaction on raw string.
		return []byte(p.redactText(string(data)))
	}

	redacted := p.redactJSONValue(v)
	out, err := json.Marshal(redacted)
	if err != nil {
		return []byte(p.redactText(string(data)))
	}
	return out
}

func (p *toolPolicy) redactJSONValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			kn := strings.ToLower(strings.TrimSpace(k))
			if _, ok := p.redactFields[kn]; ok {
				out[k] = "[REDACTED]"
				continue
			}
			out[k] = p.redactJSONValue(vv)
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, vv := range t {
			out = append(out, p.redactJSONValue(vv))
		}
		return out
	case string:
		return p.redactText(t)
	default:
		return v
	}
}

func (p *toolPolicy) redactText(s string) string {
	if p == nil || !p.enabled || !p.redactEnabled {
		return s
	}
	out := s
	for _, re := range p.redactPatterns {
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

func (p *toolPolicy) redactToolResultForAudit(result *ToolResult) *ToolResult {
	if result == nil {
		return nil
	}
	if p == nil || !p.enabled || !p.redactEnabled {
		return result
	}

	clone := *result
	clone.Err = result.Err

	// Always redact for audit/traces.
	if strings.TrimSpace(clone.ForLLM) != "" {
		clone.ForLLM = p.redactOutputString(clone.ForLLM)
	}
	if strings.TrimSpace(clone.ForUser) != "" {
		clone.ForUser = p.redactOutputString(clone.ForUser)
	}
	return &clone
}

func (p *toolPolicy) redactToolResultForReturn(result *ToolResult) *ToolResult {
	if result == nil {
		return nil
	}
	if p == nil || !p.enabled || !p.redactEnabled {
		return result
	}
	if !p.redactApplyToLLM && !p.redactApplyToUser {
		return result
	}

	clone := *result
	clone.Err = result.Err

	if p.redactApplyToLLM && strings.TrimSpace(clone.ForLLM) != "" {
		clone.ForLLM = p.redactOutputString(clone.ForLLM)
	}
	if p.redactApplyToUser && strings.TrimSpace(clone.ForUser) != "" {
		clone.ForUser = p.redactOutputString(clone.ForUser)
	}
	return &clone
}

func (p *toolPolicy) redactOutputString(s string) string {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return s
	}

	// If it's JSON, prefer structured field redaction.
	if strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[") {
		if out := p.redactJSONBytes([]byte(raw)); len(out) > 0 {
			raw = string(out)
		}
	}
	return p.redactText(raw)
}

func (p *toolPolicy) buildDeniedResult(toolName, reason string) *ToolResult {
	payload := map[string]any{
		"kind":   "tool_policy_denied",
		"tool":   strings.TrimSpace(toolName),
		"reason": strings.TrimSpace(reason),
	}
	encoded, _ := json.Marshal(payload)
	return ErrorResult(string(encoded)).WithError(fmt.Errorf("tool denied: %s", reason))
}

func (p *toolPolicy) buildConfirmRequiredResult(toolName, confirmKey, argsPreview string) *ToolResult {
	payload := map[string]any{
		"kind":         "tool_policy_confirmation_required",
		"tool":         strings.TrimSpace(toolName),
		"confirm_key":  strings.TrimSpace(confirmKey),
		"args_preview": utils.Truncate(strings.TrimSpace(argsPreview), 240),
		"next_step":    "Ask the user for confirmation, then call tool_confirm with confirm_key.",
	}
	encoded, _ := json.Marshal(payload)
	return ErrorResult(string(encoded)).WithError(fmt.Errorf("tool requires confirmation"))
}

func (p *toolPolicy) buildIdempotentReplayResult(toolName, idempotencyKey string, cached toolPolicyStoredExecution) *ToolResult {
	payload := map[string]any{
		"kind":            "tool_policy_idempotent_replay",
		"tool":            strings.TrimSpace(toolName),
		"idempotency_key": strings.TrimSpace(idempotencyKey),
		"replayed":        true,
	}

	// Prefer returning the cached tool output directly to the LLM to preserve run continuity.
	out := cached.Result.ForLLM
	if strings.TrimSpace(out) == "" {
		if encoded, err := json.Marshal(payload); err == nil {
			out = string(encoded)
		} else {
			out = fmt.Sprintf("Idempotent replay: %s", strings.TrimSpace(idempotencyKey))
		}
	}

	r := &ToolResult{
		ForLLM:  out,
		ForUser: cached.Result.ForUser,
		Silent:  cached.Result.Silent,
		IsError: cached.Result.IsError,
		Async:   cached.Result.Async,
		Media:   append([]string(nil), cached.Result.Media...),
	}
	if cached.Result.IsError && strings.TrimSpace(cached.Result.Error) != "" {
		r.Err = fmt.Errorf("%s", strings.TrimSpace(cached.Result.Error))
	}

	// Attach a small prefix if the cached result did not include replay metadata.
	if !strings.Contains(r.ForLLM, "tool_policy_idempotent_replay") {
		if encoded, err := json.Marshal(payload); err == nil {
			r.ForLLM = string(encoded) + "\n\n---\n\n" + r.ForLLM
		}
	}

	return r
}

func shouldSkipErrorTemplate(forLLM string) bool {
	s := strings.TrimSpace(forLLM)
	if !strings.HasPrefix(s, "{") {
		return false
	}
	var payload struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(s), &payload); err != nil {
		return false
	}
	return strings.HasPrefix(payload.Kind, "tool_policy_")
}

func toolPolicyTagsToString(tags map[string]string) string {
	if len(tags) == 0 {
		return ""
	}
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, tags[k]))
	}
	return strings.Join(parts, ",")
}

func toolPolicyIsSensitiveTool(toolName string) bool {
	name := strings.ToLower(strings.TrimSpace(toolName))
	if name == "" {
		return false
	}
	// Never block the confirmation tool itself.
	return name != "tool_confirm"
}

func normalizeToolList(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n != "" {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	out = slices.Compact(out)
	return out
}
