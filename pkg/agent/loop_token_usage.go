package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xwysyy/X-Claw/pkg/fileutil"
	"github.com/xwysyy/X-Claw/pkg/logger"
	"github.com/xwysyy/X-Claw/pkg/providers"
)

func (al *AgentLoop) tokenUsageStore(workspace string) *tokenUsageStore {
	if al == nil {
		return nil
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil
	}

	al.tokenUsageMu.Lock()
	defer al.tokenUsageMu.Unlock()

	if al.tokenUsageStores == nil {
		al.tokenUsageStores = make(map[string]*tokenUsageStore)
	}
	if s, ok := al.tokenUsageStores[workspace]; ok && s != nil {
		return s
	}
	s := newTokenUsageStore(workspace)
	al.tokenUsageStores[workspace] = s
	return s
}

type tokenUsageTotals struct {
	Requests int64 `json:"requests"`

	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`

	LastSeen string `json:"last_seen,omitempty"` // RFC3339Nano (UTC)
}

type tokenUsageSnapshot struct {
	Version   int    `json:"version"`
	UpdatedAt string `json:"updated_at,omitempty"` // RFC3339Nano (UTC)

	Totals  tokenUsageTotals            `json:"totals"`
	ByModel map[string]tokenUsageTotals `json:"by_model,omitempty"`
}

type tokenUsageStore struct {
	workspace string
	path      string

	mu     sync.Mutex
	loaded bool
	snap   tokenUsageSnapshot
}

func newTokenUsageStore(workspace string) *tokenUsageStore {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil
	}
	return &tokenUsageStore{
		workspace: workspace,
		path:      filepath.Join(workspace, "state", "token_usage.json"),
	}
}

func (s *tokenUsageStore) loadLocked() {
	if s == nil || s.loaded {
		return
	}
	s.loaded = true

	s.snap = tokenUsageSnapshot{
		Version: 1,
		ByModel: map[string]tokenUsageTotals{},
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		logger.WarnCF("agent", "token usage: failed to read snapshot (starting fresh)", map[string]any{
			"path": s.path,
			"err":  err.Error(),
		})
		return
	}

	var snap tokenUsageSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		logger.WarnCF("agent", "token usage: failed to parse snapshot (starting fresh)", map[string]any{
			"path": s.path,
			"err":  err.Error(),
		})
		return
	}

	// Best-effort normalization.
	if snap.Version <= 0 {
		snap.Version = 1
	}
	if snap.ByModel == nil {
		snap.ByModel = map[string]tokenUsageTotals{}
	}
	s.snap = snap
}

func (s *tokenUsageStore) Record(model string, usage *providers.UsageInfo) {
	if s == nil || usage == nil {
		return
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = "unknown"
	}

	prompt := int64(usage.PromptTokens)
	completion := int64(usage.CompletionTokens)
	total := int64(usage.TotalTokens)

	if prompt < 0 {
		prompt = 0
	}
	if completion < 0 {
		completion = 0
	}
	if total < 0 {
		total = 0
	}

	// Some providers may omit total_tokens. If prompt/completion are present,
	// compute a sane total for consistent accounting.
	if total == 0 && (prompt > 0 || completion > 0) {
		total = prompt + completion
	}

	// Ignore empty usage records to avoid noisy writes for providers that don't report tokens.
	if prompt == 0 && completion == 0 && total == 0 {
		return
	}

	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()

	byModel := s.snap.ByModel
	if byModel == nil {
		byModel = map[string]tokenUsageTotals{}
		s.snap.ByModel = byModel
	}

	// Update per-model.
	mt := byModel[model]
	mt.Requests++
	mt.PromptTokens += prompt
	mt.CompletionTokens += completion
	mt.TotalTokens += total
	mt.LastSeen = now.Format(time.RFC3339Nano)
	byModel[model] = mt

	// Update global totals.
	s.snap.Totals.Requests++
	s.snap.Totals.PromptTokens += prompt
	s.snap.Totals.CompletionTokens += completion
	s.snap.Totals.TotalTokens += total
	s.snap.Totals.LastSeen = now.Format(time.RFC3339Nano)

	s.snap.UpdatedAt = now.Format(time.RFC3339Nano)

	payload, err := json.MarshalIndent(s.snap, "", "  ")
	if err != nil {
		logger.WarnCF("agent", "token usage: failed to marshal snapshot", map[string]any{
			"err": err.Error(),
		})
		return
	}

	if err := fileutil.WriteFileAtomic(s.path, payload, 0o600); err != nil {
		logger.WarnCF("agent", "token usage: failed to persist snapshot", map[string]any{
			"path": s.path,
			"err":  err.Error(),
		})
		return
	}
}
