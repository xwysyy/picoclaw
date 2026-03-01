package protocoltypes

import (
	"encoding/json"
	"testing"
)

func TestToolCall_JSONExcludesInternalFields(t *testing.T) {
	t.Parallel()

	tc := ToolCall{
		ID:   "call-1",
		Type: "function",
		Function: &FunctionCall{
			Name:             "do",
			Arguments:        `{"x":1}`,
			ThoughtSignature: "sig-fn",
		},
		Name:             "internal-name",
		Arguments:        map[string]any{"x": 1},
		ThoughtSignature: "sig",
		ExtraContent: &ExtraContent{
			Google: &GoogleExtra{ThoughtSignature: "sig-google"},
		},
	}

	b, err := json.Marshal(tc)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	if _, ok := m["id"]; !ok {
		t.Fatalf("missing id: %s", string(b))
	}
	if _, ok := m["type"]; !ok {
		t.Fatalf("missing type: %s", string(b))
	}
	if _, ok := m["function"]; !ok {
		t.Fatalf("missing function: %s", string(b))
	}
	if _, ok := m["extra_content"]; !ok {
		t.Fatalf("missing extra_content: %s", string(b))
	}

	// Internal-only fields must not appear in JSON.
	for _, forbidden := range []string{
		"Name", "name",
		"Arguments", "arguments",
		"ThoughtSignature", "thought_signature",
	} {
		if _, ok := m[forbidden]; ok {
			t.Fatalf("unexpected %q field in JSON: %s", forbidden, string(b))
		}
	}
}

func TestMessage_JSONOmitempty(t *testing.T) {
	t.Parallel()

	msg := Message{Role: "user", Content: "hi"}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}

	if m["role"] != "user" || m["content"] != "hi" {
		t.Fatalf("unexpected JSON: %s", string(b))
	}

	// Omitempty fields should be absent when empty.
	for _, k := range []string{"reasoning_content", "system_parts", "tool_calls", "tool_call_id"} {
		if _, ok := m[k]; ok {
			t.Fatalf("expected %q to be omitted: %s", k, string(b))
		}
	}
}
