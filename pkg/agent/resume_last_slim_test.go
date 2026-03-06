package agent

import (
	"strings"
	"testing"
)

func TestResumeLastTaskPrompt_SlimPrompt(t *testing.T) {
	prompt := resumeLastTaskPrompt()
	if strings.Contains(strings.ToLower(prompt), "tool_confirm") {
		t.Fatalf("expected resume prompt to drop tool_confirm guidance, got: %s", prompt)
	}
	if !strings.Contains(prompt, "Continue the unfinished task") {
		t.Fatalf("unexpected resume prompt: %s", prompt)
	}
}
