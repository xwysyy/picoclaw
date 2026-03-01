package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/utils"
)

// MemorySearchTool performs semantic lookup over persisted memory files.
type MemorySearchTool struct {
	memory          *MemoryStore
	defaultTopK     int
	defaultMinScore float64
}

func NewMemorySearchTool(memory *MemoryStore, defaultTopK int, defaultMinScore float64) *MemorySearchTool {
	if defaultTopK <= 0 {
		defaultTopK = defaultMemoryVectorTopK
	}
	if defaultMinScore < 0 || defaultMinScore >= 1 {
		defaultMinScore = defaultMemoryVectorMinScore
	}
	return &MemorySearchTool{
		memory:          memory,
		defaultTopK:     defaultTopK,
		defaultMinScore: defaultMinScore,
	}
}

func (t *MemorySearchTool) Name() string {
	return "memory_search"
}

func (t *MemorySearchTool) Description() string {
	return "Semantically search MEMORY.md and recent daily notes for relevant facts. Returns structured JSON hits for stable LLM consumption."
}

func (t *MemorySearchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Natural-language query to search semantic memory",
			},
			"top_k": map[string]any{
				"type":        "integer",
				"description": "Maximum number of hits to return (default from agent settings)",
			},
			"min_score": map[string]any{
				"type":        "number",
				"description": "Minimum cosine similarity in [0,1), lower means broader recall",
			},
		},
		"required": []string{"query"},
	}
}

func (t *MemorySearchTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	_ = ctx

	if t.memory == nil {
		return tools.ErrorResult("memory store unavailable")
	}

	query, ok := args["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return tools.ErrorResult("query is required")
	}

	topK := t.defaultTopK
	if raw, ok := args["top_k"]; ok {
		switch v := raw.(type) {
		case int:
			if v > 0 {
				topK = v
			}
		case int64:
			if v > 0 {
				topK = int(v)
			}
		case float64:
			if int(v) > 0 {
				topK = int(v)
			}
		}
	}

	minScore := t.defaultMinScore
	if raw, ok := args["min_score"]; ok {
		if v, ok := raw.(float64); ok && v >= 0 && v < 1 {
			minScore = v
		}
	}

	hits, err := t.memory.SearchRelevant(query, topK, minScore)
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("memory search failed: %v", err)).WithError(err)
	}

	type memoryHit struct {
		ID         string   `json:"id"`
		Score      float64  `json:"score"`
		Snippet    string   `json:"snippet"`
		Source     string   `json:"source"`
		SourcePath string   `json:"source_path"`
		Tags       []string `json:"tags"`
	}
	type memorySearchResult struct {
		Kind     string      `json:"kind"`
		Query    string      `json:"query"`
		TopK     int         `json:"top_k"`
		MinScore float64     `json:"min_score"`
		Hits     []memoryHit `json:"hits"`
	}

	result := memorySearchResult{
		Kind:     "memory_search_result",
		Query:    query,
		TopK:     topK,
		MinScore: minScore,
		Hits:     make([]memoryHit, 0, len(hits)),
	}

	for _, hit := range hits {
		sourcePath := hit.Source
		if before, _, ok := strings.Cut(hit.Source, "#"); ok && strings.TrimSpace(before) != "" {
			sourcePath = before
		}
		result.Hits = append(result.Hits, memoryHit{
			ID:         hit.Source,
			Score:      hit.Score,
			Snippet:    utils.Truncate(hit.Text, 240),
			Source:     hit.Source,
			SourcePath: sourcePath,
			Tags:       []string{},
		})
	}

	payload, err := json.Marshal(result)
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("memory search failed: %v", err)).WithError(err)
	}

	// Keep a human-readable summary (for traces / debugging) while returning JSON to the LLM.
	var summary strings.Builder
	if len(hits) == 0 {
		summary.WriteString("No relevant memory hits found.")
	} else {
		summary.WriteString("Memory search hits:\n")
		for _, hit := range hits {
			summary.WriteString(fmt.Sprintf("- (score=%.2f, source=%s) %s\n", hit.Score, hit.Source, hit.Text))
		}
	}

	return &tools.ToolResult{
		ForLLM:  string(payload),
		ForUser: strings.TrimSpace(summary.String()),
		Silent:  true,
		IsError: false,
		Async:   false,
	}
}

// MemoryGetTool returns a specific memory item by its source citation.
type MemoryGetTool struct {
	memory *MemoryStore
}

func NewMemoryGetTool(memory *MemoryStore) *MemoryGetTool {
	return &MemoryGetTool{memory: memory}
}

func (t *MemoryGetTool) Name() string {
	return "memory_get"
}

func (t *MemoryGetTool) Description() string {
	return "Retrieve one memory entry by source citation returned from memory_search. Returns structured JSON for stable LLM consumption."
}

func (t *MemoryGetTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"source": map[string]any{
				"type":        "string",
				"description": "Citation source like MEMORY.md#Long-term Facts",
			},
		},
		"required": []string{"source"},
	}
}

func (t *MemoryGetTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	_ = ctx

	if t.memory == nil {
		return tools.ErrorResult("memory store unavailable")
	}

	source, ok := args["source"].(string)
	if !ok || strings.TrimSpace(source) == "" {
		return tools.ErrorResult("source is required")
	}

	hit, found, err := t.memory.GetBySource(source)
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("memory get failed: %v", err)).WithError(err)
	}

	type memoryGetResult struct {
		Kind  string `json:"kind"`
		Found bool   `json:"found"`
		Hit   struct {
			ID         string   `json:"id,omitempty"`
			Source     string   `json:"source,omitempty"`
			SourcePath string   `json:"source_path,omitempty"`
			Content    string   `json:"content,omitempty"`
			Tags       []string `json:"tags,omitempty"`
		} `json:"hit"`
	}

	result := memoryGetResult{
		Kind:  "memory_get_result",
		Found: found,
	}
	if found {
		sourcePath := hit.Source
		if before, _, ok := strings.Cut(hit.Source, "#"); ok && strings.TrimSpace(before) != "" {
			sourcePath = before
		}

		result.Hit.ID = hit.Source
		result.Hit.Source = hit.Source
		result.Hit.SourcePath = sourcePath
		result.Hit.Content = hit.Text
		result.Hit.Tags = []string{}
	}

	payload, err := json.Marshal(result)
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("memory get failed: %v", err)).WithError(err)
	}

	userSummary := "Memory source not found."
	if found {
		userSummary = fmt.Sprintf("Memory entry:\n- source=%s\n- content=%s", hit.Source, hit.Text)
	}

	return &tools.ToolResult{
		ForLLM:  string(payload),
		ForUser: userSummary,
		Silent:  true,
		IsError: false,
		Async:   false,
	}
}
