package agent

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultMemoryVectorDimensions      = 256
	defaultMemoryVectorTopK            = 6
	defaultMemoryVectorMinScore        = 0.15
	defaultMemoryVectorMaxContextChars = 1800
	defaultMemoryVectorRecentDailyDays = 14
	memoryVectorChunkChars             = 280
)

// MemoryVectorSettings controls semantic memory indexing and retrieval behavior.
type MemoryVectorSettings struct {
	Enabled         bool
	Dimensions      int
	TopK            int
	MinScore        float64
	MaxContextChars int
	RecentDailyDays int
	Embedding       MemoryVectorEmbeddingSettings
	Hybrid          MemoryHybridSettings
}

type MemoryVectorHit struct {
	Source string
	Text   string
	Score  float64

	// MatchKind indicates which retriever produced this hit:
	// - "fts": SQLite FTS keyword match
	// - "vector": semantic vector search
	// - "hybrid": both signals available
	MatchKind string

	HasFTS      bool
	FTSScore    float64
	HasVector   bool
	VectorScore float64
}

type MemoryHybridSettings struct {
	FTSWeight    float64
	VectorWeight float64
}

type memoryVectorDocument struct {
	ID     string    `json:"id"`
	Source string    `json:"source"`
	Text   string    `json:"text"`
	Vector []float32 `json:"vector"`
}

type memoryVectorIndex struct {
	Version     int                    `json:"version"`
	BuiltAt     string                 `json:"built_at"`
	Fingerprint string                 `json:"fingerprint"`
	EmbedderSig string                 `json:"embedder_signature,omitempty"`
	Dimensions  int                    `json:"dimensions"`
	Documents   []memoryVectorDocument `json:"documents"`
}

type memoryVectorSourceFile struct {
	Path    string
	RelPath string
	Size    int64
	ModUnix int64
}

type memoryVectorStore struct {
	memoryDir  string
	memoryFile string
	indexPath  string

	mu       sync.Mutex
	settings MemoryVectorSettings
	embedder memoryVectorEmbedder
	cache    *memoryVectorIndex
}

func defaultMemoryVectorSettings() MemoryVectorSettings {
	return MemoryVectorSettings{
		Enabled:         true,
		Dimensions:      defaultMemoryVectorDimensions,
		TopK:            defaultMemoryVectorTopK,
		MinScore:        defaultMemoryVectorMinScore,
		MaxContextChars: defaultMemoryVectorMaxContextChars,
		RecentDailyDays: defaultMemoryVectorRecentDailyDays,
		Hybrid: MemoryHybridSettings{
			FTSWeight:    0.6,
			VectorWeight: 0.4,
		},
	}
}

func normalizeMemoryVectorSettings(settings MemoryVectorSettings) MemoryVectorSettings {
	if settings.Dimensions <= 0 {
		settings.Dimensions = defaultMemoryVectorDimensions
	}
	if settings.TopK <= 0 {
		settings.TopK = defaultMemoryVectorTopK
	}
	if settings.MinScore < 0 || settings.MinScore >= 1 {
		settings.MinScore = defaultMemoryVectorMinScore
	}
	if settings.MaxContextChars <= 0 {
		settings.MaxContextChars = defaultMemoryVectorMaxContextChars
	}
	if settings.RecentDailyDays <= 0 {
		settings.RecentDailyDays = defaultMemoryVectorRecentDailyDays
	}
	settings.Embedding = normalizeMemoryVectorEmbeddingSettings(settings.Embedding)
	settings.Hybrid = normalizeMemoryHybridSettings(settings.Hybrid)
	return settings
}

func normalizeMemoryHybridSettings(settings MemoryHybridSettings) MemoryHybridSettings {
	if settings.FTSWeight < 0 || settings.FTSWeight > 1 {
		settings.FTSWeight = 0
	}
	if settings.VectorWeight < 0 || settings.VectorWeight > 1 {
		settings.VectorWeight = 0
	}
	if settings.FTSWeight == 0 && settings.VectorWeight == 0 {
		settings.FTSWeight = 0.6
		settings.VectorWeight = 0.4
	}
	sum := settings.FTSWeight + settings.VectorWeight
	if sum > 0 {
		settings.FTSWeight /= sum
		settings.VectorWeight /= sum
	}
	return settings
}

func newMemoryVectorStore(memoryDir, memoryFile string, settings MemoryVectorSettings) *memoryVectorStore {
	settings = normalizeMemoryVectorSettings(settings)
	embedder := buildMemoryVectorEmbedder(settings.Embedding, settings.Dimensions)
	return &memoryVectorStore{
		memoryDir:  memoryDir,
		memoryFile: memoryFile,
		indexPath:  filepath.Join(memoryDir, "vector", "index.json"),
		settings:   settings,
		embedder:   embedder,
	}
}

func (vs *memoryVectorStore) SetSettings(settings MemoryVectorSettings) {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	prevSettings := vs.settings
	oldSig := ""
	if vs.embedder != nil {
		oldSig = vs.embedder.Signature()
	}

	normalized := normalizeMemoryVectorSettings(settings)
	newEmbedder := buildMemoryVectorEmbedder(normalized.Embedding, normalized.Dimensions)
	newSig := ""
	if newEmbedder != nil {
		newSig = newEmbedder.Signature()
	}

	if prevSettings != normalized || oldSig != newSig {
		vs.cache = nil
	}

	vs.settings = normalized
	vs.embedder = newEmbedder
}

func (vs *memoryVectorStore) MarkDirty() {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	vs.cache = nil
}

func (vs *memoryVectorStore) Rebuild(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	vs.mu.Lock()
	defer vs.mu.Unlock()
	return vs.rebuildLocked(ctx)
}

func (vs *memoryVectorStore) Search(ctx context.Context, query string, topK int, minScore float64) ([]MemoryVectorHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	vs.mu.Lock()
	defer vs.mu.Unlock()

	if !vs.settings.Enabled {
		return nil, nil
	}
	if topK <= 0 {
		topK = vs.settings.TopK
	}
	if minScore < 0 {
		minScore = 0
	}

	if err := vs.ensureIndexLocked(ctx); err != nil {
		return nil, err
	}
	if vs.cache == nil || len(vs.cache.Documents) == 0 {
		return nil, nil
	}

	if vs.embedder == nil {
		return nil, fmt.Errorf("memory embedder not configured")
	}
	queryVecs, err := vs.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(queryVecs) == 0 {
		return nil, nil
	}
	queryVec := queryVecs[0]
	queryTerms := uniqueTokenSet(tokenizeForEmbedding(query))
	if len(queryVec) == 0 {
		return nil, nil
	}

	hits := make([]MemoryVectorHit, 0, minInt(topK, len(vs.cache.Documents)))
	for _, doc := range vs.cache.Documents {
		vectorScore := cosineSimilarity(queryVec, doc.Vector)
		keywordScore := lexicalSimilarity(queryTerms, tokenizeForEmbedding(doc.Text))
		// Blend semantic and lexical signals to improve recall on terse notes and identifiers.
		score := 0.8*vectorScore + 0.2*keywordScore
		if score < minScore {
			continue
		}
		hits = append(hits, MemoryVectorHit{
			Source:      doc.Source,
			Text:        doc.Text,
			Score:       score,
			MatchKind:   "vector",
			HasVector:   true,
			VectorScore: score,
		})
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			return hits[i].Source < hits[j].Source
		}
		return hits[i].Score > hits[j].Score
	})

	if len(hits) > topK {
		hits = hits[:topK]
	}
	return hits, nil
}

func (vs *memoryVectorStore) GetBySource(ctx context.Context, source string) (MemoryVectorHit, bool, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return MemoryVectorHit{}, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	vs.mu.Lock()
	defer vs.mu.Unlock()

	if !vs.settings.Enabled {
		return MemoryVectorHit{}, false, nil
	}

	if err := vs.ensureIndexLocked(ctx); err != nil {
		return MemoryVectorHit{}, false, err
	}
	if vs.cache == nil || len(vs.cache.Documents) == 0 {
		return MemoryVectorHit{}, false, nil
	}

	for _, doc := range vs.cache.Documents {
		if doc.Source != source {
			continue
		}
		return MemoryVectorHit{
			Source: doc.Source,
			Text:   doc.Text,
			Score:  1,
		}, true, nil
	}

	return MemoryVectorHit{}, false, nil
}

func (vs *memoryVectorStore) ensureIndexLocked(ctx context.Context) error {
	sources, fingerprint, err := vs.collectSourceFilesLocked(time.Now())
	if err != nil {
		return err
	}

	if vs.cache != nil && vs.cache.Fingerprint == fingerprint {
		return nil
	}

	if disk, loadErr := vs.loadIndexLocked(); loadErr == nil && disk != nil {
		if disk.Fingerprint == fingerprint {
			vs.cache = disk
			return nil
		}
	}

	return vs.rebuildFromSourcesLocked(ctx, sources, fingerprint)
}

func (vs *memoryVectorStore) rebuildLocked(ctx context.Context) error {
	sources, fingerprint, err := vs.collectSourceFilesLocked(time.Now())
	if err != nil {
		return err
	}
	return vs.rebuildFromSourcesLocked(ctx, sources, fingerprint)
}

func (vs *memoryVectorStore) rebuildFromSourcesLocked(
	ctx context.Context,
	sources []memoryVectorSourceFile,
	fingerprint string,
) error {
	if ctx == nil {
		ctx = context.Background()
	}

	docs, err := vs.buildDocumentsLocked(sources)
	if err != nil {
		return err
	}

	// Attempt to reuse existing embeddings when the embedder signature matches.
	var reuse map[string][]float32
	if disk, loadErr := vs.loadIndexLocked(); loadErr == nil && disk != nil {
		wantSig := ""
		if vs.embedder != nil {
			wantSig = vs.embedder.Signature()
		}
		if strings.TrimSpace(wantSig) != "" && disk.EmbedderSig == wantSig && len(disk.Documents) > 0 {
			reuse = make(map[string][]float32, len(disk.Documents))
			for _, doc := range disk.Documents {
				if doc.ID == "" || len(doc.Vector) == 0 {
					continue
				}
				reuse[doc.ID] = doc.Vector
			}
		}
	}

	if vs.embedder == nil {
		return fmt.Errorf("memory embedder not configured")
	}

	toEmbed := make([]string, 0, len(docs))
	embedIdx := make([]int, 0, len(docs))
	for i := range docs {
		if reuse != nil {
			if vec, ok := reuse[docs[i].ID]; ok && len(vec) > 0 {
				docs[i].Vector = vec
				continue
			}
		}
		toEmbed = append(toEmbed, docs[i].Text)
		embedIdx = append(embedIdx, i)
	}

	if len(toEmbed) > 0 {
		vecs, err := vs.embedder.Embed(ctx, toEmbed)
		if err != nil {
			return err
		}
		if len(vecs) != len(toEmbed) {
			return fmt.Errorf("embedding backend returned %d vectors for %d inputs", len(vecs), len(toEmbed))
		}
		for j, vec := range vecs {
			docs[embedIdx[j]].Vector = vec
		}
	}

	dims := 0
	for _, doc := range docs {
		if len(doc.Vector) > 0 {
			dims = len(doc.Vector)
			break
		}
	}

	embedderSig := ""
	if vs.embedder != nil {
		embedderSig = vs.embedder.Signature()
	}

	index := &memoryVectorIndex{
		Version:     2,
		BuiltAt:     time.Now().Format(time.RFC3339),
		Fingerprint: fingerprint,
		EmbedderSig: embedderSig,
		Dimensions:  dims,
		Documents:   docs,
	}
	if err := vs.saveIndexLocked(index); err != nil {
		return err
	}
	vs.cache = index
	return nil
}

func (vs *memoryVectorStore) collectSourceFilesLocked(now time.Time) ([]memoryVectorSourceFile, string, error) {
	sources := make([]memoryVectorSourceFile, 0, vs.settings.RecentDailyDays+1)

	if info, err := os.Stat(vs.memoryFile); err == nil && !info.IsDir() {
		sources = append(sources, memoryVectorSourceFile{
			Path:    vs.memoryFile,
			RelPath: "MEMORY.md",
			Size:    info.Size(),
			ModUnix: info.ModTime().Unix(),
		})
	} else if err != nil && !os.IsNotExist(err) {
		return nil, "", err
	}

	for i := 0; i < vs.settings.RecentDailyDays; i++ {
		day := now.AddDate(0, 0, -i).Format("20060102")
		candidate := filepath.Join(vs.memoryDir, day[:6], day+".md")

		info, err := os.Stat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, "", err
		}
		if info.IsDir() {
			continue
		}

		rel, relErr := filepath.Rel(vs.memoryDir, candidate)
		if relErr != nil {
			rel = filepath.Base(candidate)
		}
		sources = append(sources, memoryVectorSourceFile{
			Path:    candidate,
			RelPath: filepath.ToSlash(rel),
			Size:    info.Size(),
			ModUnix: info.ModTime().Unix(),
		})
	}

	embedderSig := ""
	if vs.embedder != nil {
		embedderSig = vs.embedder.Signature()
	}
	fingerprint := buildSourceFingerprint(sources, embedderSig)
	return sources, fingerprint, nil
}

func buildSourceFingerprint(sources []memoryVectorSourceFile, embedderSig string) string {
	h := sha1.New()
	fmt.Fprintf(h, "embedder=%s\n", strings.TrimSpace(embedderSig))
	for _, src := range sources {
		fmt.Fprintf(h, "%s|%d|%d\n", src.RelPath, src.Size, src.ModUnix)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (vs *memoryVectorStore) buildDocumentsLocked(sources []memoryVectorSourceFile) ([]memoryVectorDocument, error) {
	docs := make([]memoryVectorDocument, 0, len(sources)*8)

	for _, src := range sources {
		data, err := os.ReadFile(src.Path)
		if err != nil {
			return nil, err
		}
		content := string(data)
		if strings.TrimSpace(content) == "" {
			continue
		}

		if src.RelPath == "MEMORY.md" {
			blocks := parseMemoryAsBlocks(content)
			for _, label := range memoryBlockLabels() {
				for _, entry := range extractBlockEntries(blocks[label]) {
					text := compactWhitespace(entry)
					if text == "" {
						continue
					}
					payload := label + ": " + text
					docs = append(docs, memoryVectorDocument{
						ID:     buildMemoryVectorID(src.RelPath, label, text),
						Source: fmt.Sprintf("%s#%s", src.RelPath, label),
						Text:   payload,
					})
				}
			}
			continue
		}

		chunks := chunkMarkdownForVectors(content, memoryVectorChunkChars)
		for idx, chunk := range chunks {
			if strings.TrimSpace(chunk) == "" {
				continue
			}
			docs = append(docs, memoryVectorDocument{
				ID:     buildMemoryVectorID(src.RelPath, fmt.Sprintf("%d", idx+1), chunk),
				Source: fmt.Sprintf("%s#%d", src.RelPath, idx+1),
				Text:   chunk,
			})
		}
	}

	return docs, nil
}

func chunkMarkdownForVectors(content string, maxChars int) []string {
	if maxChars <= 0 {
		maxChars = memoryVectorChunkChars
	}

	lines := strings.Split(content, "\n")
	out := make([]string, 0, 8)
	var current strings.Builder
	currentHeading := ""

	flush := func() {
		chunk := compactWhitespace(current.String())
		if chunk != "" {
			out = append(out, chunk)
		}
		current.Reset()
	}

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || line == "---" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "#") {
			flush()
			currentHeading = strings.TrimSpace(strings.TrimLeft(line, "#"))
			continue
		}

		line = strings.TrimSpace(strings.TrimLeft(line, "-*+"))
		if line == "" {
			continue
		}
		if currentHeading != "" {
			line = currentHeading + ": " + line
		}

		if current.Len() == 0 {
			current.WriteString(line)
		} else {
			if current.Len()+1+len(line) > maxChars {
				flush()
				current.WriteString(line)
			} else {
				current.WriteString(" ")
				current.WriteString(line)
			}
		}
	}
	flush()

	return out
}

func (vs *memoryVectorStore) loadIndexLocked() (*memoryVectorIndex, error) {
	data, err := os.ReadFile(vs.indexPath)
	if err != nil {
		return nil, err
	}
	var index memoryVectorIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, err
	}
	return &index, nil
}

func (vs *memoryVectorStore) saveIndexLocked(index *memoryVectorIndex) error {
	if err := os.MkdirAll(filepath.Dir(vs.indexPath), 0o755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(vs.indexPath, payload, 0o644)
}
