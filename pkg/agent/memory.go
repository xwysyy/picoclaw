// X-Claw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 X-Claw contributors

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xwysyy/picoclaw/pkg/fileutil"
)

// MemoryStore manages persistent memory for the agent.
// - Long-term memory: memory/MEMORY.md
// - Daily notes: memory/YYYYMM/YYYYMMDD.md
type MemoryStore struct {
	memoryDir  string
	memoryFile string
	vector     *memoryVectorStore
	fts        *memoryFTSStore
	settings   MemoryVectorSettings
}

var memorySectionOrder = []string{
	"Profile",
	"Long-term Facts",
	"Active Goals",
	"Constraints",
	"Open Threads",
	"Deprecated/Resolved",
}

var memorySectionAliases = map[string]string{
	"profile":             "Profile",
	"long-term memory":    "Long-term Facts",
	"long term memory":    "Long-term Facts",
	"long-term facts":     "Long-term Facts",
	"active goals":        "Active Goals",
	"constraints":         "Constraints",
	"open threads":        "Open Threads",
	"open tasks":          "Open Threads",
	"pending tasks":       "Open Threads",
	"deprecated/resolved": "Deprecated/Resolved",
	"resolved":            "Deprecated/Resolved",
}

// NewMemoryStore creates a new MemoryStore with the given workspace path.
// It ensures the memory directory exists.
func NewMemoryStore(workspace string) *MemoryStore {
	return NewMemoryStoreAt(filepath.Join(workspace, "memory"))
}

// NewMemoryStoreAt creates a new MemoryStore rooted at memoryDir.
//
// memoryDir is expected to contain MEMORY.md and daily notes (YYYYMM/YYYYMMDD.md).
// This is used for scoped memory (per-user/per-group/per-session) within an agent workspace.
func NewMemoryStoreAt(memoryDir string) *MemoryStore {
	memoryFile := filepath.Join(memoryDir, "MEMORY.md")

	// Ensure memory directory exists
	_ = os.MkdirAll(memoryDir, 0o755)

	vectorSettings := defaultMemoryVectorSettings()
	vectorSettings = normalizeMemoryVectorSettings(vectorSettings)

	return &MemoryStore{
		memoryDir:  memoryDir,
		memoryFile: memoryFile,
		vector:     newMemoryVectorStore(memoryDir, memoryFile, vectorSettings),
		fts:        newMemoryFTSStore(memoryDir, memoryFile, vectorSettings),
		settings:   vectorSettings,
	}
}

// getTodayFile returns the path to today's daily note file (memory/YYYYMM/YYYYMMDD.md).
func (ms *MemoryStore) getTodayFile() string {
	today := time.Now().Format("20060102") // YYYYMMDD
	monthDir := today[:6]                  // YYYYMM
	filePath := filepath.Join(ms.memoryDir, monthDir, today+".md")
	return filePath
}

// ReadLongTerm reads the long-term memory (MEMORY.md).
// Returns empty string if the file doesn't exist.
func (ms *MemoryStore) ReadLongTerm() string {
	if data, err := os.ReadFile(ms.memoryFile); err == nil {
		return string(data)
	}
	return ""
}

// WriteLongTerm writes content to the long-term memory file (MEMORY.md).
func (ms *MemoryStore) WriteLongTerm(content string) error {
	// Use unified atomic write utility with explicit sync for flash storage reliability.
	// Using 0o600 (owner read/write only) for secure default permissions.
	if err := fileutil.WriteFileAtomic(ms.memoryFile, []byte(content), 0o600); err != nil {
		return err
	}
	ms.refreshVectorIndex()
	return nil
}

// ReadToday reads today's daily note.
// Returns empty string if the file doesn't exist.
func (ms *MemoryStore) ReadToday() string {
	todayFile := ms.getTodayFile()
	if data, err := os.ReadFile(todayFile); err == nil {
		return string(data)
	}
	return ""
}

// AppendToday appends content to today's daily note.
// If the file doesn't exist, it creates a new file with a date header.
func (ms *MemoryStore) AppendToday(content string) error {
	todayFile := ms.getTodayFile()

	// Ensure month directory exists
	monthDir := filepath.Dir(todayFile)
	if err := os.MkdirAll(monthDir, 0o755); err != nil {
		return err
	}

	var existingContent string
	if data, err := os.ReadFile(todayFile); err == nil {
		existingContent = string(data)
	}

	var newContent string
	if existingContent == "" {
		// Add header for new day
		header := fmt.Sprintf("# %s\n\n", time.Now().Format("2006-01-02"))
		newContent = header + content
	} else {
		// Append to existing content
		newContent = existingContent + "\n" + content
	}

	// Use unified atomic write utility with explicit sync for flash storage reliability.
	if err := fileutil.WriteFileAtomic(todayFile, []byte(newContent), 0o600); err != nil {
		return err
	}
	ms.refreshVectorIndex()
	return nil
}

// GetRecentDailyNotes returns daily notes from the last N days.
// Contents are joined with "---" separator.
func (ms *MemoryStore) GetRecentDailyNotes(days int) string {
	var sb strings.Builder
	first := true

	for i := range days {
		date := time.Now().AddDate(0, 0, -i)
		dateStr := date.Format("20060102") // YYYYMMDD
		monthDir := dateStr[:6]            // YYYYMM
		filePath := filepath.Join(ms.memoryDir, monthDir, dateStr+".md")

		if data, err := os.ReadFile(filePath); err == nil {
			if !first {
				sb.WriteString("\n\n---\n\n")
			}
			sb.Write(data)
			first = false
		}
	}

	return sb.String()
}

// GetMemoryContext returns formatted memory context for the agent prompt.
// Includes long-term memory and recent daily notes.
func (ms *MemoryStore) GetMemoryContext() string {
	longTerm := ms.ReadLongTerm()
	recentNotes := ms.GetRecentDailyNotes(3)

	if longTerm == "" && recentNotes == "" {
		return ""
	}

	var sb strings.Builder

	if longTerm != "" {
		sb.WriteString("## Long-term Memory\n\n")
		sb.WriteString(longTerm)
	}

	if recentNotes != "" {
		if longTerm != "" {
			sb.WriteString("\n\n---\n\n")
		}
		sb.WriteString("## Recent Daily Notes\n\n")
		sb.WriteString(recentNotes)
	}

	return sb.String()
}

// OrganizeWriteback rewrites MEMORY.md using stable blocks with guardrails:
// - persona/human/projects/facts
// - read-only protection for core blocks
// - hard character limits to prevent prompt bloat
func (ms *MemoryStore) OrganizeWriteback(extracted string) error {
	base := parseMemoryAsBlocks(ms.ReadLongTerm())
	incoming := parseMemoryAsBlocks(extracted)

	for _, spec := range memoryBlockSpecs {
		label := spec.Label
		base[label] = sanitizeMemoryText(base[label])
		incoming[label] = sanitizeMemoryText(incoming[label])
	}

	for _, spec := range memoryBlockSpecs {
		label := spec.Label
		if spec.ReadOnly {
			if strings.TrimSpace(base[label]) != "" && spec.Limit > 0 {
				base[label] = truncateRunes(strings.TrimSpace(base[label]), spec.Limit)
			}
			continue
		}

		entries := mergeBlockEntries(base[label], incoming[label])
		entries = clipEntriesToLimit(entries, spec.Limit)
		base[label] = renderEntries(entries)
	}

	return ms.WriteLongTerm(renderMemoryBlocks(base))
}

func (ms *MemoryStore) SetVectorSettings(settings MemoryVectorSettings) {
	settings = normalizeMemoryVectorSettings(settings)
	ms.settings = settings
	if ms.vector == nil {
		// still allow FTS-only settings updates
	} else {
		ms.vector.SetSettings(settings)
	}
	if ms.fts != nil {
		ms.fts.SetSettings(settings)
	}
}

// SearchRelevant runs semantic retrieval over MEMORY.md + recent daily notes.
func (ms *MemoryStore) SearchRelevant(ctx context.Context, query string, topK int, minScore float64) ([]MemoryVectorHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}

	var ftsHits []MemoryVectorHit
	var ftsErr error
	if ms.fts != nil {
		ftsHits, ftsErr = ms.fts.Search(ctx, query, topK)
	}

	var vecHits []MemoryVectorHit
	var vecErr error
	if ms.vector != nil {
		vecHits, vecErr = ms.vector.Search(ctx, query, topK, minScore)
	}

	hits := mergeHybridMemoryHits(ftsHits, vecHits, topK, ms.settings.Hybrid)
	if len(hits) == 0 && ftsErr != nil && vecErr != nil {
		return nil, fmt.Errorf("memory search unavailable: fts=%v; vector=%v", ftsErr, vecErr)
	}

	// Best-effort: return whatever we have. Vector embedding failures should not
	// take down deterministic keyword lookup (FTS), and vice versa.
	return hits, nil
}

func (ms *MemoryStore) GetBySource(ctx context.Context, source string) (MemoryVectorHit, bool, error) {
	_ = ctx

	src := strings.TrimSpace(source)
	if src == "" {
		return MemoryVectorHit{}, false, nil
	}

	filePart, anchor, _ := strings.Cut(src, "#")
	filePart = strings.TrimSpace(filePart)
	anchor = strings.TrimSpace(anchor)
	if filePart == "" {
		return MemoryVectorHit{}, false, nil
	}

	rel := filepath.Clean(filepath.FromSlash(filePart))
	if rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return MemoryVectorHit{}, false, fmt.Errorf("invalid memory source path %q", filePart)
	}

	path := filepath.Join(ms.memoryDir, rel)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return MemoryVectorHit{}, false, nil
		}
		return MemoryVectorHit{}, false, err
	}
	content := string(data)
	if strings.TrimSpace(content) == "" {
		return MemoryVectorHit{}, false, nil
	}

	if strings.EqualFold(rel, "MEMORY.md") {
		if anchor == "" {
			return MemoryVectorHit{Source: filePart, Text: content, Score: 1}, true, nil
		}

		label := ""
		if spec, ok := lookupMemoryBlockSpec(anchor); ok {
			label = spec.Label
		} else if section, ok := normalizeMemorySectionName(anchor); ok {
			if mapped, ok := memoryBlockLabelForLegacySection(section); ok {
				label = mapped
			} else {
				return MemoryVectorHit{}, false, nil
			}
		} else {
			return MemoryVectorHit{}, false, nil
		}

		blocks := parseMemoryAsBlocks(content)
		blockContent := strings.TrimSpace(blocks[label])
		if blockContent == "" {
			return MemoryVectorHit{}, false, nil
		}

		canonicalPath := filepath.ToSlash(rel)
		out := strings.TrimSpace("## " + label + "\n" + blockContent)
		if out == "" {
			return MemoryVectorHit{}, false, nil
		}
		return MemoryVectorHit{Source: fmt.Sprintf("%s#%s", canonicalPath, label), Text: out, Score: 1}, true, nil
	}

	// Non-MEMORY.md sources use chunk indexes for stable retrieval.
	if anchor == "" {
		return MemoryVectorHit{Source: filePart, Text: content, Score: 1}, true, nil
	}

	chunkIdx, convErr := parsePositiveInt(anchor)
	if convErr != nil || chunkIdx <= 0 {
		return MemoryVectorHit{}, false, nil
	}

	chunks := chunkMarkdownForVectors(content, memoryVectorChunkChars)
	if chunkIdx-1 >= len(chunks) {
		return MemoryVectorHit{}, false, nil
	}
	chunk := strings.TrimSpace(chunks[chunkIdx-1])
	if chunk == "" {
		return MemoryVectorHit{}, false, nil
	}

	return MemoryVectorHit{Source: fmt.Sprintf("%s#%d", filePart, chunkIdx), Text: chunk, Score: 1}, true, nil
}

func (ms *MemoryStore) refreshVectorIndex() {
	if ms.vector == nil {
		// still allow FTS-only
	} else {
		ms.vector.MarkDirty()
	}
	if ms.fts != nil {
		ms.fts.MarkDirty()
	}
}

func parseMemorySections(content string) map[string][]string {
	sections := make(map[string][]string, len(memorySectionOrder))
	if strings.TrimSpace(content) == "" {
		return sections
	}

	current := "Long-term Facts"
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			heading := strings.TrimSpace(strings.TrimLeft(line, "#"))
			if normalized, ok := normalizeMemorySectionName(heading); ok {
				current = normalized
			}
			continue
		}

		entry := strings.TrimSpace(strings.TrimLeft(line, "-*+"))
		if entry == "" {
			continue
		}
		sections[current] = append(sections[current], entry)
	}
	return sections
}

func normalizeMemorySectionName(name string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return "", false
	}
	if section, ok := memorySectionAliases[key]; ok {
		return section, true
	}
	for _, section := range memorySectionOrder {
		if strings.EqualFold(section, name) {
			return section, true
		}
	}
	return "", false
}

func normalizeMemorySections(sections map[string][]string) {
	for section, entries := range sections {
		seen := map[string]struct{}{}
		deduped := make([]string, 0, len(entries))
		for _, entry := range entries {
			clean := strings.TrimSpace(entry)
			if clean == "" {
				continue
			}
			key := strings.ToLower(clean)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			deduped = append(deduped, clean)
		}
		sort.Strings(deduped)
		sections[section] = deduped
	}
}

func renderMemorySections(sections map[string][]string) string {
	var sb strings.Builder
	sb.WriteString("# MEMORY\n\n")
	sb.WriteString(fmt.Sprintf("_Last organized: %s_\n\n", time.Now().Format("2006-01-02 15:04")))

	wroteSection := false
	for _, section := range memorySectionOrder {
		entries := sections[section]
		if len(entries) == 0 {
			continue
		}
		wroteSection = true
		sb.WriteString("## ")
		sb.WriteString(section)
		sb.WriteString("\n")
		for _, entry := range entries {
			sb.WriteString("- ")
			sb.WriteString(entry)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	if !wroteSection {
		sb.WriteString("## Long-term Facts\n")
		sb.WriteString("- (no durable facts recorded yet)\n")
	}

	return strings.TrimSpace(sb.String()) + "\n"
}
