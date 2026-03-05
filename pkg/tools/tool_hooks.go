package tools

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/providers"
)

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

	// Phase F2: prevent implicit handoffs executed by background subagents.
	hooks = append(hooks, NewSubagentHandoffBlockHook())
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
