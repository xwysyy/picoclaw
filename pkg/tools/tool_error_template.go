package tools

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"github.com/xwysyy/X-Claw/pkg/providers"
	"github.com/xwysyy/X-Claw/pkg/utils"
)

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
