package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// WorkingState tracks the agent's current task progress as structured data.
// Instead of relying on the LLM to extract state from long conversation history,
// this provides an explicit, maintained state object that is injected into the
// context on each LLM call.
//
// This is the "working memory" layer — more structured than conversation history
// (short-term), less persistent than MEMORY.md (long-term).
type WorkingState struct {
	mu             sync.RWMutex
	OriginalTask   string          `json:"original_task"`
	CurrentPlan    []PlanStep      `json:"current_plan,omitempty"`
	CompletedSteps []CompletedStep `json:"completed_steps,omitempty"`
	CollectedData  map[string]string `json:"collected_data,omitempty"`
	OpenQuestions  []string        `json:"open_questions,omitempty"`
	NextAction     string          `json:"next_action,omitempty"`
	ToolCallCount  int             `json:"tool_call_count"`
	ErrorCount     int             `json:"error_count"`
}

// PlanStep represents a single step in the agent's execution plan.
type PlanStep struct {
	Description string `json:"description"`
	Status      string `json:"status"` // pending, running, done, failed, skipped
	ToolNeeded  string `json:"tool_needed,omitempty"`
}

// CompletedStep records a finished step with its outcome.
type CompletedStep struct {
	Description string `json:"description"`
	Outcome     string `json:"outcome"`
	ToolUsed    string `json:"tool_used,omitempty"`
}

// NewWorkingState creates a new WorkingState for the given task.
func NewWorkingState(task string) *WorkingState {
	return &WorkingState{
		OriginalTask:  task,
		CollectedData: make(map[string]string),
	}
}

// RecordToolCall increments the tool call counter and tracks errors.
func (ws *WorkingState) RecordToolCall(toolName string, isError bool) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.ToolCallCount++
	if isError {
		ws.ErrorCount++
	}
}

// SetNextAction updates the planned next action.
func (ws *WorkingState) SetNextAction(action string) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.NextAction = action
}

// AddCollectedData records a key piece of data gathered during execution.
func (ws *WorkingState) AddCollectedData(key, value string) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.CollectedData[key] = value
}

// AddCompletedStep records a finished step.
func (ws *WorkingState) AddCompletedStep(description, outcome, toolUsed string) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.CompletedSteps = append(ws.CompletedSteps, CompletedStep{
		Description: description,
		Outcome:     outcome,
		ToolUsed:    toolUsed,
	})
}

// FormatForContext returns a concise summary suitable for injection into the
// LLM context. Only includes non-empty sections to save tokens.
func (ws *WorkingState) FormatForContext() string {
	ws.mu.RLock()
	defer ws.mu.RUnlock()

	if ws.OriginalTask == "" {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Working State\n\n")
	fmt.Fprintf(&sb, "**Task**: %s\n", ws.OriginalTask)
	fmt.Fprintf(&sb, "**Progress**: %d tool calls, %d errors\n", ws.ToolCallCount, ws.ErrorCount)

	if len(ws.CompletedSteps) > 0 {
		sb.WriteString("\n**Completed**:\n")
		// Show only last 5 steps to save tokens
		start := 0
		if len(ws.CompletedSteps) > 5 {
			start = len(ws.CompletedSteps) - 5
			fmt.Fprintf(&sb, "- (%d earlier steps omitted)\n", start)
		}
		for _, step := range ws.CompletedSteps[start:] {
			fmt.Fprintf(&sb, "- %s → %s\n", step.Description, step.Outcome)
		}
	}

	if len(ws.CollectedData) > 0 {
		sb.WriteString("\n**Collected Data**:\n")
		for k, v := range ws.CollectedData {
			// Truncate long values
			if len(v) > 200 {
				v = v[:200] + "..."
			}
			fmt.Fprintf(&sb, "- %s: %s\n", k, v)
		}
	}

	if len(ws.OpenQuestions) > 0 {
		sb.WriteString("\n**Open Questions**:\n")
		for _, q := range ws.OpenQuestions {
			fmt.Fprintf(&sb, "- %s\n", q)
		}
	}

	if ws.NextAction != "" {
		fmt.Fprintf(&sb, "\n**Next Action**: %s\n", ws.NextAction)
	}

	return sb.String()
}

// ToJSON serializes the working state for persistence.
func (ws *WorkingState) ToJSON() ([]byte, error) {
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	return json.Marshal(ws)
}
