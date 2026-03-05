package tools

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xwysyy/X-Claw/internal/core/events"
	"github.com/xwysyy/X-Claw/pkg/logger"
	"github.com/xwysyy/X-Claw/pkg/utils"
)

type toolPolicyLedgerResult struct {
	ForLLM  string   `json:"for_llm,omitempty"`
	ForUser string   `json:"for_user,omitempty"`
	Silent  bool     `json:"silent,omitempty"`
	Async   bool     `json:"async,omitempty"`
	IsError bool     `json:"is_error,omitempty"`
	Error   string   `json:"error,omitempty"`
	Media   []string `json:"media,omitempty"`
}

type toolPolicyLedgerEvent struct {
	Type events.Type `json:"type"`

	TS   string `json:"ts"`
	TSMS int64  `json:"ts_ms"`

	RunID      string `json:"run_id,omitempty"`
	SessionKey string `json:"session_key,omitempty"`

	Tool       string `json:"tool,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`

	IdempotencyKey string `json:"idempotency_key,omitempty"`
	ConfirmKey     string `json:"confirm_key,omitempty"`
	ExpiresAtMS    int64  `json:"expires_at_ms,omitempty"`
	Note           string `json:"note,omitempty"`

	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`

	ArgsPreview string                  `json:"args_preview,omitempty"`
	Result      *toolPolicyLedgerResult `json:"result,omitempty"`
}

type toolPolicyStoredConfirmation struct {
	ExpiresAtMS int64
	Note        string
}

type toolPolicyStoredExecution struct {
	Tool       string
	ToolCallID string
	Result     toolPolicyLedgerResult
}

type toolPolicyStore struct {
	scope string

	enabled bool
	path    string

	maxResultChars int

	mu            sync.Mutex
	loaded        bool
	confirmations map[string]toolPolicyStoredConfirmation
	executions    map[string]toolPolicyStoredExecution
}

var toolPolicyStores sync.Map // key -> *toolPolicyStore

func toolPolicyStoreKey(workspace, sessionKey, runID string) string {
	return strings.TrimSpace(workspace) + "\n" + utils.CanonicalSessionKey(sessionKey) + "\n" + strings.TrimSpace(runID)
}

func policyRunDir(workspace, sessionKey, runID string) (string, bool) {
	workspace = strings.TrimSpace(workspace)
	sessionKey = utils.CanonicalSessionKey(sessionKey)
	runID = strings.TrimSpace(runID)
	if workspace == "" || sessionKey == "" || runID == "" {
		return "", false
	}

	dirKey := SafePathToken(sessionKey)
	if dirKey == "" {
		dirKey = "unknown"
	}
	runKey := SafePathToken(runID)
	if runKey == "" {
		runKey = "unknown"
	}

	return filepath.Join(workspace, ".x-claw", "audit", "runs", dirKey, "runs", runKey), true
}

func getToolPolicyStore(workspace, sessionKey, runID string) *toolPolicyStore {
	dir, ok := policyRunDir(workspace, sessionKey, runID)
	if !ok {
		return &toolPolicyStore{enabled: false}
	}

	key := toolPolicyStoreKey(workspace, sessionKey, runID)
	if existing, ok := toolPolicyStores.Load(key); ok {
		if store, ok := existing.(*toolPolicyStore); ok && store != nil {
			return store
		}
	}

	store := &toolPolicyStore{
		scope: "tool_policy",

		enabled: true,
		path:    filepath.Join(dir, "policy.jsonl"),

		maxResultChars: 12000,

		confirmations: make(map[string]toolPolicyStoredConfirmation),
		executions:    make(map[string]toolPolicyStoredExecution),
	}

	toolPolicyStores.Store(key, store)
	return store
}

func (s *toolPolicyStore) Load() {
	if s == nil || !s.enabled {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.loaded {
		return
	}
	s.loaded = true

	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		logger.WarnCF(s.scope, "Tool policy store: failed to open ledger", map[string]any{
			"path": s.path,
			"err":  err.Error(),
		})
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev toolPolicyLedgerEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}

		switch ev.Type {
		case events.ToolConfirmed:
			key := strings.TrimSpace(ev.ConfirmKey)
			if key == "" {
				continue
			}
			s.confirmations[key] = toolPolicyStoredConfirmation{
				ExpiresAtMS: ev.ExpiresAtMS,
				Note:        ev.Note,
			}
		case events.ToolExecuted:
			key := strings.TrimSpace(ev.IdempotencyKey)
			if key == "" || ev.Result == nil {
				continue
			}
			s.executions[key] = toolPolicyStoredExecution{
				Tool:       strings.TrimSpace(ev.Tool),
				ToolCallID: strings.TrimSpace(ev.ToolCallID),
				Result:     *ev.Result,
			}
		}
	}
}

func (s *toolPolicyStore) IsConfirmed(confirmKey string) bool {
	if s == nil || !s.enabled {
		return false
	}
	confirmKey = strings.TrimSpace(confirmKey)
	if confirmKey == "" {
		return false
	}

	s.Load()

	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.confirmations[confirmKey]
	if !ok {
		return false
	}
	if c.ExpiresAtMS > 0 && time.Now().UnixMilli() > c.ExpiresAtMS {
		delete(s.confirmations, confirmKey)
		return false
	}
	return true
}

func (s *toolPolicyStore) RecordConfirmation(runID, sessionKey, confirmKey, toolName, toolCallID, note string, expiresAt time.Time) error {
	if s == nil || !s.enabled {
		return nil
	}
	confirmKey = strings.TrimSpace(confirmKey)
	if confirmKey == "" {
		return fmt.Errorf("confirm key is empty")
	}

	s.Load()

	ts := time.Now()
	ev := toolPolicyLedgerEvent{
		Type: events.ToolConfirmed,

		TS:   ts.UTC().Format(time.RFC3339Nano),
		TSMS: ts.UnixMilli(),

		RunID:      strings.TrimSpace(runID),
		SessionKey: utils.CanonicalSessionKey(sessionKey),

		Tool:       strings.TrimSpace(toolName),
		ToolCallID: strings.TrimSpace(toolCallID),

		ConfirmKey:  confirmKey,
		ExpiresAtMS: expiresAt.UnixMilli(),
		Note:        utils.Truncate(strings.TrimSpace(note), 800),
	}

	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Write(payload); err != nil {
		return err
	}
	_ = f.Sync()

	s.confirmations[confirmKey] = toolPolicyStoredConfirmation{
		ExpiresAtMS: expiresAt.UnixMilli(),
		Note:        ev.Note,
	}

	return nil
}

func (s *toolPolicyStore) GetCachedExecution(idempotencyKey string) (toolPolicyStoredExecution, bool) {
	if s == nil || !s.enabled {
		return toolPolicyStoredExecution{}, false
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return toolPolicyStoredExecution{}, false
	}

	s.Load()

	s.mu.Lock()
	defer s.mu.Unlock()
	ex, ok := s.executions[idempotencyKey]
	return ex, ok
}

func (s *toolPolicyStore) RecordExecution(
	runID, sessionKey string,
	tcName, tcID string,
	idempotencyKey string,
	argsPreview string,
	result *ToolResult,
) error {
	if s == nil || !s.enabled {
		return nil
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return fmt.Errorf("idempotency key is empty")
	}

	s.Load()

	ts := time.Now()

	ledgerRes := (*toolPolicyLedgerResult)(nil)
	if result != nil {
		errorText := ""
		if result.Err != nil {
			errorText = result.Err.Error()
		}
		if strings.TrimSpace(errorText) == "" && result.IsError {
			errorText = result.ForLLM
		}
		ledgerRes = &toolPolicyLedgerResult{
			ForLLM:  utils.Truncate(result.ForLLM, s.maxResultChars),
			ForUser: utils.Truncate(result.ForUser, s.maxResultChars),
			Silent:  result.Silent,
			Async:   result.Async,
			IsError: result.IsError,
			Error:   utils.Truncate(strings.TrimSpace(errorText), 1200),
			Media:   append([]string(nil), result.Media...),
		}
	}

	ev := toolPolicyLedgerEvent{
		Type: events.ToolExecuted,

		TS:   ts.UTC().Format(time.RFC3339Nano),
		TSMS: ts.UnixMilli(),

		RunID:      strings.TrimSpace(runID),
		SessionKey: utils.CanonicalSessionKey(sessionKey),

		Tool:       strings.TrimSpace(tcName),
		ToolCallID: strings.TrimSpace(tcID),

		IdempotencyKey: idempotencyKey,
		ArgsPreview:    utils.Truncate(strings.TrimSpace(argsPreview), 500),

		Result: ledgerRes,
	}

	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Write(payload); err != nil {
		return err
	}
	_ = f.Sync()

	if ledgerRes != nil {
		s.executions[idempotencyKey] = toolPolicyStoredExecution{
			Tool:       strings.TrimSpace(tcName),
			ToolCallID: strings.TrimSpace(tcID),
			Result:     *ledgerRes,
		}
	}
	return nil
}

func toolIdempotencyKey(toolName string, argsJSON []byte) string {
	toolName = strings.ToLower(strings.TrimSpace(toolName))
	h := sha256.New()
	h.Write([]byte(toolName))
	h.Write([]byte{'\n'})
	h.Write(bytesTrimSpace(argsJSON))
	sum := h.Sum(nil)
	return fmt.Sprintf("%s:%s", toolName, hex.EncodeToString(sum[:12]))
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
