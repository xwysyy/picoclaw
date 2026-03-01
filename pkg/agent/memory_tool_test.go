package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemorySearchTool_Execute(t *testing.T) {
	workspace := t.TempDir()
	ms := NewMemoryStore(workspace)
	ms.SetVectorSettings(MemoryVectorSettings{
		Enabled:         true,
		Dimensions:      128,
		TopK:            5,
		MinScore:        0.01,
		MaxContextChars: 1200,
		RecentDailyDays: 7,
	})

	if err := os.MkdirAll(filepath.Join(workspace, "memory"), 0o755); err != nil {
		t.Fatalf("mkdir memory dir: %v", err)
	}
	content := `# MEMORY

## Long-term Facts
- Favorite editor: neovim
`
	if err := os.WriteFile(filepath.Join(workspace, "memory", "MEMORY.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write memory: %v", err)
	}

	tool := NewMemorySearchTool(ms, 3, 0.01)
	result := tool.Execute(context.Background(), map[string]any{
		"query": "what editor do I use",
	})

	if result.IsError {
		t.Fatalf("expected successful tool result, got error: %s", result.ForLLM)
	}
	if !result.Silent {
		t.Fatalf("expected memory_search to be silent")
	}

	var parsed struct {
		Kind string `json:"kind"`
		Hits []struct {
			ID         string  `json:"id"`
			Score      float64 `json:"score"`
			Snippet    string  `json:"snippet"`
			Source     string  `json:"source"`
			SourcePath string  `json:"source_path"`
		} `json:"hits"`
	}
	if err := json.Unmarshal([]byte(result.ForLLM), &parsed); err != nil {
		t.Fatalf("expected JSON tool output, got unmarshal error: %v\nraw=%s", err, result.ForLLM)
	}
	if parsed.Kind != "memory_search_result" {
		t.Fatalf("kind = %q, want %q", parsed.Kind, "memory_search_result")
	}
	if len(parsed.Hits) == 0 {
		t.Fatalf("expected at least 1 hit, got 0")
	}
	if !strings.Contains(strings.ToLower(parsed.Hits[0].Snippet), "neovim") {
		t.Fatalf("expected first hit snippet to include neovim, got: %s", parsed.Hits[0].Snippet)
	}
}

func TestMemoryGetTool_Execute(t *testing.T) {
	workspace := t.TempDir()
	ms := NewMemoryStore(workspace)
	ms.SetVectorSettings(MemoryVectorSettings{
		Enabled:         true,
		Dimensions:      128,
		TopK:            5,
		MinScore:        0.01,
		MaxContextChars: 1200,
		RecentDailyDays: 7,
	})

	if err := os.MkdirAll(filepath.Join(workspace, "memory"), 0o755); err != nil {
		t.Fatalf("mkdir memory dir: %v", err)
	}
	content := `# MEMORY

## Long-term Facts
- Favorite editor: neovim
`
	if err := os.WriteFile(filepath.Join(workspace, "memory", "MEMORY.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write memory: %v", err)
	}

	search := NewMemorySearchTool(ms, 3, 0.01)
	searchResult := search.Execute(context.Background(), map[string]any{
		"query": "favorite editor",
	})
	if searchResult.IsError {
		t.Fatalf("memory_search failed: %s", searchResult.ForLLM)
	}

	get := NewMemoryGetTool(ms)
	getResult := get.Execute(context.Background(), map[string]any{
		"source": "MEMORY.md#Long-term Facts",
	})
	if getResult.IsError {
		t.Fatalf("memory_get failed: %s", getResult.ForLLM)
	}

	var parsed struct {
		Kind  string `json:"kind"`
		Found bool   `json:"found"`
		Hit   struct {
			ID      string `json:"id"`
			Source  string `json:"source"`
			Content string `json:"content"`
		} `json:"hit"`
	}
	if err := json.Unmarshal([]byte(getResult.ForLLM), &parsed); err != nil {
		t.Fatalf("expected JSON tool output, got unmarshal error: %v\nraw=%s", err, getResult.ForLLM)
	}
	if parsed.Kind != "memory_get_result" {
		t.Fatalf("kind = %q, want %q", parsed.Kind, "memory_get_result")
	}
	if !parsed.Found {
		t.Fatalf("expected found=true")
	}
	if parsed.Hit.Source != "MEMORY.md#Long-term Facts" {
		t.Fatalf("unexpected hit source: %q", parsed.Hit.Source)
	}
	if !strings.Contains(strings.ToLower(parsed.Hit.Content), "neovim") {
		t.Fatalf("expected memory_get content to include neovim, got: %s", parsed.Hit.Content)
	}
}
