// X-Claw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 X-Claw contributors

package agent

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xwysyy/X-Claw/pkg/fileutil"
	"github.com/xwysyy/X-Claw/pkg/utils"
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

func parsePositiveInt(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty int")
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid int %q", s)
	}
	return n, nil
}

func mergeMemoryHits(a, b []MemoryVectorHit, topK int) []MemoryVectorHit {
	if topK <= 0 {
		topK = defaultMemoryVectorTopK
	}

	merged := make(map[string]MemoryVectorHit, len(a)+len(b))

	add := func(hit MemoryVectorHit) {
		hit.Source = strings.TrimSpace(hit.Source)
		hit.Text = strings.TrimSpace(hit.Text)
		if hit.Source == "" || hit.Text == "" {
			return
		}
		if existing, ok := merged[hit.Source]; ok {
			// Keep the higher-scoring hit; if scores tie, prefer the longer snippet.
			if hit.Score > existing.Score || (hit.Score == existing.Score && len(hit.Text) > len(existing.Text)) {
				merged[hit.Source] = hit
			}
			return
		}
		merged[hit.Source] = hit
	}

	for _, hit := range a {
		add(hit)
	}
	for _, hit := range b {
		add(hit)
	}

	out := make([]MemoryVectorHit, 0, len(merged))
	for _, hit := range merged {
		out = append(out, hit)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Source < out[j].Source
		}
		return out[i].Score > out[j].Score
	})
	if len(out) > topK {
		out = out[:topK]
	}
	return out
}

func mergeHybridMemoryHits(ftsHits, vecHits []MemoryVectorHit, topK int, hybrid MemoryHybridSettings) []MemoryVectorHit {
	if topK <= 0 {
		topK = defaultMemoryVectorTopK
	}
	hybrid = normalizeMemoryHybridSettings(hybrid)

	merged := make(map[string]MemoryVectorHit, len(ftsHits)+len(vecHits))

	add := func(hit MemoryVectorHit) {
		hit.Source = strings.TrimSpace(hit.Source)
		hit.Text = strings.TrimSpace(hit.Text)
		if hit.Source == "" || hit.Text == "" {
			return
		}

		if existing, ok := merged[hit.Source]; ok {
			// Merge signals.
			if hit.HasFTS && (!existing.HasFTS || hit.FTSScore > existing.FTSScore) {
				existing.HasFTS = true
				existing.FTSScore = hit.FTSScore
			}
			if hit.HasVector && (!existing.HasVector || hit.VectorScore > existing.VectorScore) {
				existing.HasVector = true
				existing.VectorScore = hit.VectorScore
			}
			// Prefer longer snippet when both refer to same source.
			if len(hit.Text) > len(existing.Text) {
				existing.Text = hit.Text
			}
			merged[hit.Source] = existing
			return
		}

		merged[hit.Source] = hit
	}

	for _, hit := range ftsHits {
		if !hit.HasFTS {
			hit.HasFTS = true
			hit.FTSScore = hit.Score
		}
		hit.MatchKind = "fts"
		add(hit)
	}
	for _, hit := range vecHits {
		if !hit.HasVector {
			hit.HasVector = true
			hit.VectorScore = hit.Score
		}
		hit.MatchKind = "vector"
		add(hit)
	}

	out := make([]MemoryVectorHit, 0, len(merged))
	for _, hit := range merged {
		// Re-score deterministically.
		switch {
		case hit.HasFTS && hit.HasVector:
			hit.MatchKind = "hybrid"
			hit.Score = hybrid.FTSWeight*hit.FTSScore + hybrid.VectorWeight*hit.VectorScore
		case hit.HasFTS:
			hit.MatchKind = "fts"
			hit.Score = hit.FTSScore
		case hit.HasVector:
			hit.MatchKind = "vector"
			hit.Score = hit.VectorScore
		default:
			continue
		}
		out = append(out, hit)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Source < out[j].Source
		}
		return out[i].Score > out[j].Score
	})

	if len(out) > topK {
		out = out[:topK]
	}
	return out
}

// MemoryReader is the minimal read-only surface area required by the agent
// prompt builder and memory tools.
//
// It is intentionally narrower than *MemoryStore so we can provide a composite
// view (agent + scoped) without changing how writes are routed.
type memoryScopeKind string

const (
	memoryScopeAgent   memoryScopeKind = "agent"
	memoryScopeUser    memoryScopeKind = "user"
	memoryScopeSession memoryScopeKind = "session"
)

type memoryScope struct {
	Kind  memoryScopeKind
	RawID string
}

func deriveMemoryScope(sessionKey, channel, chatID string) memoryScope {
	raw := utils.CanonicalSessionKey(sessionKey)
	lower := raw

	if lower == "" {
		return memoryScope{Kind: memoryScopeAgent, RawID: "agent"}
	}

	if idx := strings.Index(lower, ":direct:"); idx >= 0 {
		peer := strings.TrimSpace(raw[idx+len(":direct:"):])
		if peer == "" {
			peer = strings.TrimSpace(chatID)
		}
		if peer == "" {
			peer = raw
		}
		return memoryScope{Kind: memoryScopeUser, RawID: peer}
	}

	if idx := strings.Index(lower, ":group:"); idx >= 0 {
		peer := strings.TrimSpace(raw[idx+len(":group:"):])
		if peer == "" {
			peer = strings.TrimSpace(chatID)
		}
		ch := strings.TrimSpace(channel)
		if ch == "" {
			ch = extractChannelFromSessionKey(raw)
		}
		rawID := strings.Trim(strings.TrimSpace(ch)+":group:"+strings.TrimSpace(peer), ":")
		if rawID == "" {
			rawID = raw
		}
		return memoryScope{Kind: memoryScopeSession, RawID: rawID}
	}

	if idx := strings.Index(lower, ":channel:"); idx >= 0 {
		peer := strings.TrimSpace(raw[idx+len(":channel:"):])
		if peer == "" {
			peer = strings.TrimSpace(chatID)
		}
		ch := strings.TrimSpace(channel)
		if ch == "" {
			ch = extractChannelFromSessionKey(raw)
		}
		rawID := strings.Trim(strings.TrimSpace(ch)+":channel:"+strings.TrimSpace(peer), ":")
		if rawID == "" {
			rawID = raw
		}
		return memoryScope{Kind: memoryScopeSession, RawID: rawID}
	}

	// Default to agent-scoped memory for main/cron/heartbeat and similar runtime tasks.
	return memoryScope{Kind: memoryScopeAgent, RawID: "agent"}
}

func extractChannelFromSessionKey(sessionKey string) string {
	lower := utils.CanonicalSessionKey(sessionKey)
	if !strings.HasPrefix(lower, "agent:") {
		return ""
	}

	parts := strings.Split(lower, ":")
	if len(parts) < 4 {
		return ""
	}

	candidate := strings.TrimSpace(parts[2])
	if candidate == "" {
		return ""
	}

	kind := strings.TrimSpace(parts[3])
	switch kind {
	case "direct", "group", "channel":
		return candidate
	}

	// Per-account session keys: agent:<id>:<channel>:<account>:direct:<peer>
	if len(parts) >= 5 && strings.TrimSpace(parts[4]) == "direct" {
		return candidate
	}

	return ""
}

var memoryScopeTokenRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func memoryScopeToken(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "unknown"
	}

	sanitized := memoryScopeTokenRe.ReplaceAllString(raw, "_")
	sanitized = strings.Trim(sanitized, "._-")
	if sanitized == "" {
		sanitized = "unknown"
	}

	sum := sha1.Sum([]byte(raw))
	hash := hex.EncodeToString(sum[:])[:8]

	// Keep directory tokens reasonably short to avoid path length issues, but
	// always append a hash to prevent collisions from truncation/sanitization.
	const maxLen = 80
	maxBase := maxLen - 1 - len(hash) // "_" + hash
	if maxBase < 1 {
		maxBase = 1
	}
	if len(sanitized) > maxBase {
		sanitized = sanitized[:maxBase]
		sanitized = strings.TrimRight(sanitized, "._-")
		if sanitized == "" {
			sanitized = "unknown"
		}
	}

	return sanitized + "_" + hash
}

type MemoryReader interface {
	GetMemoryContext() string
	SearchRelevant(ctx context.Context, query string, topK int, minScore float64) ([]MemoryVectorHit, error)
	GetBySource(ctx context.Context, source string) (MemoryVectorHit, bool, error)
}

// memoryReadStack overlays a scoped memory store on top of the agent (root) store.
//
// Read behavior:
// - SearchRelevant: merges hits from agent + scoped stores (scoped sources are prefixed).
// - GetBySource: prefers explicit scoped-prefixed sources; otherwise resolves against agent store.
// - GetMemoryContext: concatenates agent + scoped contexts with clear headings.
//
// Write behavior is NOT implemented here; callers should continue to use the
// scoped *MemoryStore returned by ContextBuilder.MemoryForSession().
type memoryReadStack struct {
	root *MemoryStore

	scoped       *MemoryStore
	scopedKind   memoryScopeKind
	scopedPrefix string // e.g. "scopes/session/<token>" (relative to root memory dir)
}

func prefixMemorySource(prefix, source string) string {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	source = strings.TrimSpace(source)
	if prefix == "" || source == "" {
		return source
	}

	filePart, anchor, _ := strings.Cut(source, "#")
	filePart = strings.TrimSpace(filePart)
	anchor = strings.TrimSpace(anchor)
	if filePart == "" {
		return source
	}

	prefixedFile := filepath.ToSlash(filepath.Join(prefix, filepath.FromSlash(filePart)))
	if anchor == "" {
		return prefixedFile
	}
	return prefixedFile + "#" + anchor
}

func stripMemorySourcePrefix(prefix, source string) (string, bool) {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	source = strings.TrimSpace(source)
	if prefix == "" || source == "" {
		return "", false
	}

	filePart, anchor, _ := strings.Cut(source, "#")
	filePart = strings.TrimSpace(filePart)
	anchor = strings.TrimSpace(anchor)
	if filePart == "" {
		return "", false
	}

	want := prefix + "/"
	if !strings.HasPrefix(filePart, want) {
		return "", false
	}

	strippedFile := strings.TrimPrefix(filePart, want)
	if strippedFile == "" {
		return "", false
	}
	if anchor == "" {
		return strippedFile, true
	}
	return strippedFile + "#" + anchor, true
}

func (ms *memoryReadStack) GetMemoryContext() string {
	if ms == nil {
		return ""
	}

	rootCtx := ""
	if ms.root != nil {
		rootCtx = ms.root.GetMemoryContext()
	}

	scopedCtx := ""
	if ms.scoped != nil && ms.scoped != ms.root {
		scopedCtx = ms.scoped.GetMemoryContext()
	}

	if strings.TrimSpace(rootCtx) == "" {
		return scopedCtx
	}
	if strings.TrimSpace(scopedCtx) == "" {
		return rootCtx
	}

	kindLabel := "Scoped"
	switch ms.scopedKind {
	case memoryScopeUser:
		kindLabel = "User"
	case memoryScopeSession:
		kindLabel = "Session"
	}

	var sb strings.Builder
	sb.WriteString("## Agent Memory (shared)\n\n")
	sb.WriteString(strings.TrimSpace(rootCtx))
	sb.WriteString("\n\n---\n\n")
	sb.WriteString(fmt.Sprintf("## %s Memory (isolated)\n\n", kindLabel))
	sb.WriteString(strings.TrimSpace(scopedCtx))
	return strings.TrimSpace(sb.String())
}

func (ms *memoryReadStack) SearchRelevant(ctx context.Context, query string, topK int, minScore float64) ([]MemoryVectorHit, error) {
	if ms == nil {
		return nil, nil
	}

	var rootHits []MemoryVectorHit
	var rootErr error
	if ms.root != nil {
		rootHits, rootErr = ms.root.SearchRelevant(ctx, query, topK, minScore)
	}

	var scopedHits []MemoryVectorHit
	var scopedErr error
	if ms.scoped != nil && ms.scoped != ms.root {
		scopedHits, scopedErr = ms.scoped.SearchRelevant(ctx, query, topK, minScore)
		if ms.scopedPrefix != "" {
			for i := range scopedHits {
				scopedHits[i].Source = prefixMemorySource(ms.scopedPrefix, scopedHits[i].Source)
			}
		}
	}

	hits := mergeMemoryHits(rootHits, scopedHits, topK)
	if len(hits) == 0 && rootErr != nil && scopedErr != nil {
		return nil, fmt.Errorf("memory search unavailable: agent=%v; scoped=%v", rootErr, scopedErr)
	}
	return hits, nil
}

func (ms *memoryReadStack) GetBySource(ctx context.Context, source string) (MemoryVectorHit, bool, error) {
	if ms == nil {
		return MemoryVectorHit{}, false, nil
	}

	source = strings.TrimSpace(source)
	if source == "" {
		return MemoryVectorHit{}, false, nil
	}

	// 1) Explicit scoped path: "scopes/<kind>/<token>/..." — route to scoped store.
	if ms.scoped != nil && ms.scoped != ms.root && ms.scopedPrefix != "" {
		if stripped, ok := stripMemorySourcePrefix(ms.scopedPrefix, source); ok {
			hit, found, err := ms.scoped.GetBySource(ctx, stripped)
			if found && err == nil && hit.Source != "" {
				hit.Source = prefixMemorySource(ms.scopedPrefix, hit.Source)
			}
			return hit, found, err
		}
	}

	// 2) Default: resolve against agent memory.
	if ms.root != nil {
		hit, found, err := ms.root.GetBySource(ctx, source)
		if found || err != nil {
			return hit, found, err
		}
	}

	// 3) Backward-compat fallback: allow unprefixed scoped sources (older sessions).
	if ms.scoped != nil && ms.scoped != ms.root {
		hit, found, err := ms.scoped.GetBySource(ctx, source)
		if found && err == nil && hit.Source != "" && ms.scopedPrefix != "" {
			hit.Source = prefixMemorySource(ms.scopedPrefix, hit.Source)
		}
		return hit, found, err
	}

	return MemoryVectorHit{}, false, nil
}

type memoryBlockSpec struct {
	Label    string
	Limit    int  // max characters (runes) allowed for the block content
	ReadOnly bool // true if agent updates must not modify this block
}

var memoryBlockSpecs = []memoryBlockSpec{
	{Label: "persona", Limit: 2400, ReadOnly: true},
	{Label: "human", Limit: 3600, ReadOnly: true},
	{Label: "projects", Limit: 6000, ReadOnly: false},
	{Label: "facts", Limit: 9000, ReadOnly: false},
}

func lookupMemoryBlockSpec(label string) (memoryBlockSpec, bool) {
	label = strings.ToLower(strings.TrimSpace(label))
	if label == "" {
		return memoryBlockSpec{}, false
	}
	for _, spec := range memoryBlockSpecs {
		if spec.Label == label {
			return spec, true
		}
	}
	return memoryBlockSpec{}, false
}

func memoryBlockLabelForLegacySection(section string) (string, bool) {
	section = strings.TrimSpace(section)
	switch section {
	case "Profile":
		return "human", true
	case "Long-term Facts", "Constraints":
		return "facts", true
	case "Active Goals", "Open Threads", "Deprecated/Resolved":
		return "projects", true
	default:
		return "", false
	}
}

func memoryBlockLabels() []string {
	out := make([]string, 0, len(memoryBlockSpecs))
	for _, spec := range memoryBlockSpecs {
		out = append(out, spec.Label)
	}
	return out
}

func parseMemoryBlocks(content string) (map[string]string, bool) {
	blocks := map[string]string{}
	lines := strings.Split(content, "\n")
	current := ""
	found := false
	var buf []string

	flush := func() {
		if current == "" {
			buf = buf[:0]
			return
		}
		blocks[current] = strings.TrimSpace(strings.Join(buf, "\n"))
		buf = buf[:0]
	}

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "##") && !strings.HasPrefix(line, "###") {
			heading := strings.TrimSpace(strings.TrimLeft(line, "#"))
			label := strings.ToLower(strings.TrimSpace(heading))
			if _, ok := lookupMemoryBlockSpec(label); ok {
				flush()
				current = label
				found = true
				continue
			}
		}
		if current != "" {
			buf = append(buf, raw)
		}
	}
	flush()
	return blocks, found
}

func renderMemoryBlocks(blocks map[string]string) string {
	now := time.Now().Format("2006-01-02 15:04")

	var sb strings.Builder
	sb.WriteString("# MEMORY\n\n")
	sb.WriteString("_Last organized: ")
	sb.WriteString(now)
	sb.WriteString("_\n\n")

	for _, label := range memoryBlockLabels() {
		sb.WriteString("## ")
		sb.WriteString(label)
		sb.WriteString("\n")
		if v := strings.TrimSpace(blocks[label]); v != "" {
			sb.WriteString(v)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	return strings.TrimSpace(sb.String()) + "\n"
}

func sanitizeMemoryText(s string) string {
	// Remove NUL bytes and other non-text junk that can break JSON/SQLite.
	return strings.Map(func(r rune) rune {
		if r == 0 {
			return -1
		}
		return r
	}, s)
}

func extractBlockEntries(content string) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimLeft(line, "-*+"))
		line = compactWhitespace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func mergeBlockEntries(base, incoming string) []string {
	baseEntries := extractBlockEntries(base)
	inEntries := extractBlockEntries(incoming)

	seen := make(map[string]struct{}, len(baseEntries)+len(inEntries))
	out := make([]string, 0, len(baseEntries)+len(inEntries))

	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		key := strings.ToLower(s)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, s)
	}

	for _, e := range baseEntries {
		add(e)
	}
	for _, e := range inEntries {
		add(e)
	}
	return out
}

func clipEntriesToLimit(entries []string, limit int) []string {
	if limit <= 0 || len(entries) == 0 {
		return entries
	}

	// Keep newest entries when trimming (tail-preserving).
	selectedRev := make([]string, 0, len(entries))
	used := 0
	for i := len(entries) - 1; i >= 0; i-- {
		entry := strings.TrimSpace(entries[i])
		if entry == "" {
			continue
		}
		// "- " + entry + "\n"
		entryLen := utf8.RuneCountInString(entry) + 3
		if used+entryLen > limit {
			if len(selectedRev) == 0 {
				// Single entry too large: truncate it to fit.
				max := maxInt(1, limit-3)
				selectedRev = append(selectedRev, truncateRunes(entry, max))
			}
			break
		}
		selectedRev = append(selectedRev, entry)
		used += entryLen
	}

	// Reverse back to chronological order.
	for i, j := 0, len(selectedRev)-1; i < j; i, j = i+1, j-1 {
		selectedRev[i], selectedRev[j] = selectedRev[j], selectedRev[i]
	}
	return selectedRev
}

func renderEntries(entries []string) string {
	var sb strings.Builder
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		sb.WriteString("- ")
		sb.WriteString(e)
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	out := make([]rune, 0, limit)
	for _, r := range s {
		if len(out) >= limit {
			break
		}
		out = append(out, r)
	}
	return strings.TrimSpace(string(out))
}

func parseMemoryAsBlocks(content string) map[string]string {
	content = strings.TrimSpace(content)
	if content == "" {
		return map[string]string{}
	}

	if blocks, ok := parseMemoryBlocks(content); ok {
		// Ensure all block keys exist for deterministic rendering.
		for _, label := range memoryBlockLabels() {
			if _, exists := blocks[label]; !exists {
				blocks[label] = ""
			}
		}
		return blocks
	}

	// Legacy format: map older sections into modern blocks.
	sections := parseMemorySections(content)
	projects := make([]string, 0, len(sections["Active Goals"])+len(sections["Open Threads"])+len(sections["Deprecated/Resolved"]))
	projects = append(projects, sections["Active Goals"]...)
	projects = append(projects, sections["Open Threads"]...)
	for _, entry := range sections["Deprecated/Resolved"] {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		projects = append(projects, "resolved: "+entry)
	}

	facts := make([]string, 0, len(sections["Long-term Facts"])+len(sections["Constraints"]))
	facts = append(facts, sections["Long-term Facts"]...)
	facts = append(facts, sections["Constraints"]...)

	blocks := map[string]string{
		"persona":  "",
		"human":    renderEntries(sections["Profile"]),
		"projects": renderEntries(projects),
		"facts":    renderEntries(facts),
	}
	// Preserve anything else by appending to facts (best-effort).
	for section, entries := range sections {
		switch section {
		case "Profile", "Active Goals", "Open Threads", "Long-term Facts", "Constraints", "Deprecated/Resolved":
			continue
		default:
			if len(entries) == 0 {
				continue
			}
			extra := renderEntries(entries)
			if extra == "" {
				continue
			}
			if blocks["facts"] != "" {
				blocks["facts"] += "\n" + extra
			} else {
				blocks["facts"] = extra
			}
		}
	}
	return blocks
}

// MemorySearchTool performs semantic lookup over persisted memory files.
