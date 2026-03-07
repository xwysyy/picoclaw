package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryFTSStore_OpenDBLocked_PragmaFailureReturnsError(t *testing.T) {
	memoryDir := t.TempDir()
	indexPath := filepath.Join(memoryDir, "fts", "index.sqlite")
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		t.Fatalf("mkdir index dir: %v", err)
	}
	if err := os.WriteFile(indexPath, []byte("not a sqlite database"), 0o644); err != nil {
		t.Fatalf("seed invalid sqlite file: %v", err)
	}

	store := &memoryFTSStore{
		memoryDir:  memoryDir,
		memoryFile: filepath.Join(memoryDir, "MEMORY.md"),
		indexPath:  indexPath,
		settings:   defaultMemoryVectorSettings(),
	}

	db, err := store.openDBLocked()
	if err == nil {
		if db != nil {
			_ = db.Close()
		}
		t.Fatal("expected pragma failure to return error")
	}
	if store.db != nil {
		defer store.db.Close()
		t.Fatal("expected store.db to remain nil on open failure")
	}

	if _, err := store.Search(context.Background(), "query", 3); err == nil {
		t.Fatal("expected search to fail when index db cannot be opened")
	}
}

func TestMemoryStore_CRUDAndContext(t *testing.T) {
	workspace := t.TempDir()
	ms := NewMemoryStore(workspace)

	longTerm := "# MEMORY\n\n## Long-term Facts\n- Favorite editor: neovim\n"
	if err := ms.WriteLongTerm(longTerm); err != nil {
		t.Fatalf("WriteLongTerm() error: %v", err)
	}
	if got := ms.ReadLongTerm(); got != longTerm {
		t.Fatalf("ReadLongTerm() = %q, want %q", got, longTerm)
	}

	todayNote := "- Call the dentist"
	if err := ms.AppendToday(todayNote); err != nil {
		t.Fatalf("AppendToday() error: %v", err)
	}
	if got := ms.ReadToday(); !strings.Contains(got, todayNote) {
		t.Fatalf("ReadToday() = %q, want contains %q", got, todayNote)
	}

	recent := ms.GetRecentDailyNotes(1)
	if !strings.Contains(recent, todayNote) {
		t.Fatalf("GetRecentDailyNotes() = %q, want contains %q", recent, todayNote)
	}

	ctxText := ms.GetMemoryContext()
	if !strings.Contains(ctxText, "## Long-term Memory") || !strings.Contains(ctxText, "## Recent Daily Notes") {
		t.Fatalf("GetMemoryContext() missing sections: %q", ctxText)
	}
	if !strings.Contains(ctxText, "Favorite editor: neovim") || !strings.Contains(ctxText, todayNote) {
		t.Fatalf("GetMemoryContext() = %q, want both long-term and daily note content", ctxText)
	}
}

func TestMemoryStore_SetVectorSettings_SwitchesEmbedderAndClearsCache(t *testing.T) {
	workspace := t.TempDir()
	ms := NewMemoryStore(workspace)
	if ms.vector == nil || ms.vector.embedder == nil {
		t.Fatal("expected vector store embedder to be initialized")
	}

	ms.vector.cache = &memoryVectorIndex{Fingerprint: "cached"}
	oldSig := ms.vector.embedder.Signature()

	ms.SetVectorSettings(MemoryVectorSettings{
		Enabled:    true,
		Dimensions: 128,
		Embedding: MemoryVectorEmbeddingSettings{
			Kind:    "openai_compat",
			APIBase: "https://api.example.com/v1/",
			Model:   "text-embedding-3-small",
		},
	})

	if ms.vector.embedder == nil {
		t.Fatal("expected embedder after SetVectorSettings")
	}
	newSig := ms.vector.embedder.Signature()
	if oldSig == newSig {
		t.Fatalf("expected embedder signature to change, old=%q new=%q", oldSig, newSig)
	}
	if ms.vector.cache != nil {
		t.Fatalf("expected vector cache to be cleared on embedder switch, got %#v", ms.vector.cache)
	}
	if ms.settings.Embedding.Kind != "openai_compat" {
		t.Fatalf("settings embedding kind = %q, want %q", ms.settings.Embedding.Kind, "openai_compat")
	}
}

func TestMemoryStore_SearchRelevant_HybridMarksCombinedHits(t *testing.T) {
	workspace := t.TempDir()
	ms := NewMemoryStore(workspace)
	ms.SetVectorSettings(MemoryVectorSettings{
		Enabled:         true,
		Dimensions:      128,
		TopK:            5,
		MinScore:        0.01,
		MaxContextChars: 1200,
		RecentDailyDays: 7,
		Hybrid: MemoryHybridSettings{
			FTSWeight:    0.6,
			VectorWeight: 0.4,
		},
	})

	if err := ms.WriteLongTerm("# MEMORY\n\n## Long-term Facts\n- Passport renewal checklist for Tokyo trip\n"); err != nil {
		t.Fatalf("WriteLongTerm() error: %v", err)
	}
	hits, err := ms.SearchRelevant(context.Background(), "passport renewal checklist", 3, 0.01)
	if err != nil {
		t.Fatalf("SearchRelevant() error: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hybrid hit")
	}
	if hits[0].MatchKind != "hybrid" {
		t.Fatalf("first hit MatchKind = %q, want %q", hits[0].MatchKind, "hybrid")
	}
	if !hits[0].HasFTS || !hits[0].HasVector {
		t.Fatalf("expected first hit to include both fts and vector signals, got %+v", hits[0])
	}
	if !strings.Contains(strings.ToLower(hits[0].Text), "passport") {
		t.Fatalf("first hit text = %q, want contains passport", hits[0].Text)
	}
}
