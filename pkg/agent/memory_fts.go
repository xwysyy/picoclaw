package agent

import (
	"context"
	"database/sql"
	"fmt"
	iofs "io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const memoryFTSDriver = "sqlite"

type memoryFTSStore struct {
	memoryDir  string
	memoryFile string
	indexPath  string

	mu              sync.Mutex
	settings        MemoryVectorSettings
	db              *sql.DB
	lastFingerprint string
}

func newMemoryFTSStore(memoryDir, memoryFile string, settings MemoryVectorSettings) *memoryFTSStore {
	settings = normalizeMemoryVectorSettings(settings)
	return &memoryFTSStore{
		memoryDir:  memoryDir,
		memoryFile: memoryFile,
		indexPath:  filepath.Join(memoryDir, "fts", "index.sqlite"),
		settings:   settings,
	}
}

func (ms *MemoryStore) Close() error {
	if ms == nil || ms.fts == nil {
		return nil
	}
	return ms.fts.Close()
}

func (fs *memoryFTSStore) Close() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.db == nil {
		return nil
	}
	err := fs.db.Close()
	fs.db = nil
	return err
}

func (fs *memoryFTSStore) SetSettings(settings MemoryVectorSettings) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	normalized := normalizeMemoryVectorSettings(settings)
	if fs.settings != normalized {
		fs.lastFingerprint = ""
	}
	fs.settings = normalized
}

func (fs *memoryFTSStore) MarkDirty() {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.lastFingerprint = ""
}

func (fs *memoryFTSStore) Search(ctx context.Context, query string, topK int) ([]MemoryVectorHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	fs.mu.Lock()
	defer fs.mu.Unlock()

	if err := fs.ensureIndexLocked(ctx); err != nil {
		return nil, err
	}
	if fs.db == nil {
		return nil, fmt.Errorf("fts db unavailable")
	}

	if topK <= 0 {
		topK = defaultMemoryVectorTopK
	}

	match := buildFTSMatchQuery(query)
	if match == "" {
		return nil, nil
	}

	rows, err := fs.db.QueryContext(ctx,
		`SELECT source, text, bm25(docs) as rank FROM docs WHERE docs MATCH ? ORDER BY rank LIMIT ?`,
		match,
		topK,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]MemoryVectorHit, 0, topK)
	for rows.Next() {
		var source string
		var text string
		var rank float64
		if err := rows.Scan(&source, &text, &rank); err != nil {
			return nil, err
		}

		// Rank conversion: bm25() is not normalized and may be negative depending on SQLite build/config.
		// Convert to a stable [0,1] score for sorting/merging.
		r := math.Abs(rank)
		score := 1.0 / (1.0 + r)
		if score <= 0 {
			score = 0.01
		}
		if score > 0.999 {
			score = 0.999
		}

		out = append(out, MemoryVectorHit{
			Source:    strings.TrimSpace(source),
			Text:      strings.TrimSpace(text),
			Score:     score,
			MatchKind: "fts",
			HasFTS:    true,
			FTSScore:  score,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}

func (fs *memoryFTSStore) ensureIndexLocked(ctx context.Context) error {
	sources, fingerprint, err := fs.collectSourceFilesLocked()
	if err != nil {
		return err
	}

	if fs.lastFingerprint != "" && fs.lastFingerprint == fingerprint {
		return nil
	}

	db, err := fs.openDBLocked()
	if err != nil {
		return err
	}
	if db == nil {
		return fmt.Errorf("fts db not available")
	}

	if err := fs.ensureSchemaLocked(ctx); err != nil {
		return err
	}

	current, _ := fs.readMetaLocked(ctx, "fingerprint")
	if strings.TrimSpace(current) == fingerprint {
		fs.lastFingerprint = fingerprint
		return nil
	}

	if err := fs.rebuildLocked(ctx, sources, fingerprint); err != nil {
		return err
	}
	fs.lastFingerprint = fingerprint
	return nil
}

func (fs *memoryFTSStore) openDBLocked() (*sql.DB, error) {
	if fs.db != nil {
		return fs.db, nil
	}

	if err := os.MkdirAll(filepath.Dir(fs.indexPath), 0o755); err != nil {
		return nil, err
	}

	// modernc.org/sqlite uses "file:" DSN style (same as in whatsapp_native).
	dsn := "file:" + fs.indexPath + "?_foreign_keys=on"
	db, err := sql.Open(memoryFTSDriver, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA synchronous = NORMAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set synchronous mode: %w", err)
	}

	fs.db = db
	return fs.db, nil
}

func (fs *memoryFTSStore) ensureSchemaLocked(ctx context.Context) error {
	if fs.db == nil {
		return fmt.Errorf("fts db unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// meta stores fingerprint and build info.
	if _, err := fs.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return err
	}

	// docs is the FTS5 index of memory chunks/entries.
	// source is UNINDEXED so it can be returned but doesn't affect ranking.
	_, err := fs.db.ExecContext(ctx, `CREATE VIRTUAL TABLE IF NOT EXISTS docs USING fts5(source UNINDEXED, text, tokenize='unicode61')`)
	if err != nil {
		return err
	}

	return nil
}

func (fs *memoryFTSStore) readMetaLocked(ctx context.Context, key string) (string, error) {
	if fs.db == nil {
		return "", fmt.Errorf("fts db unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var v string
	err := fs.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if err != nil {
		return "", err
	}
	return v, nil
}

func (fs *memoryFTSStore) writeMetaLocked(ctx context.Context, key, value string) error {
	if fs.db == nil {
		return fmt.Errorf("fts db unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	_, err := fs.db.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key,
		value,
	)
	return err
}

func (fs *memoryFTSStore) rebuildLocked(ctx context.Context, sources []memoryVectorSourceFile, fingerprint string) error {
	if fs.db == nil {
		return fmt.Errorf("fts db unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	docs, err := fs.buildDocumentsLocked(sources)
	if err != nil {
		return err
	}

	tx, err := fs.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, `DELETE FROM docs`); err != nil {
		return err
	}

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO docs(source, text) VALUES(?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, doc := range docs {
		if strings.TrimSpace(doc.Source) == "" || strings.TrimSpace(doc.Text) == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, doc.Source, doc.Text); err != nil {
			return err
		}
	}

	// meta writes are outside of the transaction because meta is not necessarily
	// updated under tx when using separate connections (depending on driver). Use the same tx.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		"fingerprint",
		fingerprint,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		"built_at",
		time.Now().Format(time.RFC3339),
	); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	rollback = false
	return nil
}

type memoryFTSDoc struct {
	Source string
	Text   string
}

func (fs *memoryFTSStore) buildDocumentsLocked(sources []memoryVectorSourceFile) ([]memoryFTSDoc, error) {
	docs := make([]memoryFTSDoc, 0, len(sources)*8)

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
					docs = append(docs, memoryFTSDoc{
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
			docs = append(docs, memoryFTSDoc{
				Source: fmt.Sprintf("%s#%d", src.RelPath, idx+1),
				Text:   chunk,
			})
		}
	}

	return docs, nil
}

func (fs *memoryFTSStore) collectSourceFilesLocked() ([]memoryVectorSourceFile, string, error) {
	sources := make([]memoryVectorSourceFile, 0, 64)

	skipDir := func(name string) bool {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "", ".", "..":
			return true
		case "fts", "vector", "scopes":
			return true
		}
		if strings.HasPrefix(name, ".") {
			return true
		}
		return false
	}

	walkErr := filepath.WalkDir(fs.memoryDir, func(path string, entry iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != fs.memoryDir && skipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		if !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			return nil
		}

		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if info.IsDir() {
			return nil
		}

		rel, relErr := filepath.Rel(fs.memoryDir, path)
		if relErr != nil {
			rel = filepath.Base(path)
		}

		sources = append(sources, memoryVectorSourceFile{
			Path:    path,
			RelPath: filepath.ToSlash(rel),
			Size:    info.Size(),
			ModUnix: info.ModTime().Unix(),
		})
		return nil
	})
	if walkErr != nil {
		return nil, "", walkErr
	}

	sort.Slice(sources, func(i, j int) bool {
		return sources[i].RelPath < sources[j].RelPath
	})

	fingerprint := buildSourceFingerprint(sources, "fts:v1")
	return sources, fingerprint, nil
}

func buildFTSMatchQuery(query string) string {
	parts := strings.Fields(strings.TrimSpace(query))
	if len(parts) == 0 {
		return ""
	}

	// Build a conservative AND query: "foo" AND "bar".
	// This avoids surprising operator behavior and keeps results stable.
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"'`)
		if p == "" {
			continue
		}
		// Remove characters that break the MATCH grammar.
		// Keep basic punctuation used in identifiers.
		p = strings.Map(func(r rune) rune {
			switch r {
			case '"', '\'', '`':
				return -1
			}
			if r == '\n' || r == '\r' || r == '\t' {
				return ' '
			}
			return r
		}, p)
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, `"`+p+`"`)
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, " AND ")
}
