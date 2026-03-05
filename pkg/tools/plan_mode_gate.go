package tools

import (
	"fmt"
	"sort"
	"strings"
)

type planModeGate struct {
	enabled bool

	restrictedSet      map[string]struct{}
	restrictedPrefixes []string
}

func newPlanModeGate(enabled bool, restrictedTools []string, restrictedPrefixes []string) *planModeGate {
	if !enabled {
		return nil
	}
	g := &planModeGate{
		enabled:            true,
		restrictedSet:      make(map[string]struct{}),
		restrictedPrefixes: nil,
	}

	for _, name := range restrictedTools {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		g.restrictedSet[name] = struct{}{}
	}

	for _, p := range restrictedPrefixes {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		g.restrictedPrefixes = append(g.restrictedPrefixes, p)
	}
	// Deterministic: longest prefix first.
	sort.Slice(g.restrictedPrefixes, func(i, j int) bool { return len(g.restrictedPrefixes[i]) > len(g.restrictedPrefixes[j]) })

	// If no restrictions provided, keep enabled but effectively allow all tools.
	return g
}

func (g *planModeGate) Enabled() bool {
	return g != nil && g.enabled
}

func (g *planModeGate) Allows(toolName string) (bool, string) {
	if !g.Enabled() {
		return true, ""
	}

	name := strings.ToLower(strings.TrimSpace(toolName))
	if name == "" {
		return false, "tool name is empty"
	}
	if _, ok := g.restrictedSet[name]; ok {
		return false, "tool restricted in plan mode"
	}
	if hasAnyPrefix(name, g.restrictedPrefixes) {
		return false, "tool restricted by prefix in plan mode"
	}
	return true, ""
}

func (g *planModeGate) DeniedResult(toolName, reason string) *ToolResult {
	name := strings.TrimSpace(toolName)
	if name == "" {
		name = "<unknown>"
	}
	msg := fmt.Sprintf(
		"PLAN_MODE_DENY: tool %q is disabled while in plan mode (%s). "+
			"Provide a plan and ask the user to approve execution (send /approve or /run), then retry.",
		name,
		strings.TrimSpace(reason),
	)
	return ErrorResult(msg)
}
