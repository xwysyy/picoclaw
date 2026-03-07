package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/providers"
)

func nestedSessionMessage() providers.Message {
	return providers.Message{
		Role:    "assistant",
		Content: "hello",
		Media:   []string{"media://one"},
		SystemParts: []providers.ContentBlock{{
			Type:         "text",
			Text:         "system",
			CacheControl: &providers.CacheControl{Type: "ephemeral"},
		}},
		ToolCalls: []providers.ToolCall{{
			ID:   "tc-1",
			Name: "read_file",
			Arguments: map[string]any{
				"path": "README.md",
				"nested": map[string]any{
					"count": 2,
				},
				"list": []any{"a", map[string]any{"id": "b"}},
			},
			Function: &providers.FunctionCall{Name: "read_file", Arguments: `{"path":"README.md"}`},
			ExtraContent: &providers.ExtraContent{Google: &providers.GoogleExtra{
				ThoughtSignature: "sig-original",
			}},
		}},
		ToolCallID: "tc-1",
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"telegram:123456", "telegram_123456"},
		{"discord:987654321", "discord_987654321"},
		{"slack:C01234", "slack_C01234"},
		{"no-colons-here", "no-colons-here"},
		{"multiple:colons:here", "multiple_colons_here"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeFilename(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSave_WithColonInKey(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSessionManager(tmpDir)

	// Create a session with a key containing colon (typical channel session key).
	key := "telegram:123456"
	sm.GetOrCreate(key)
	sm.AddMessage(key, "user", "hello")

	// Save should succeed even though the key contains ':'
	if err := sm.Save(key); err != nil {
		t.Fatalf("Save(%q) failed: %v", key, err)
	}

	// Files on disk should use sanitized name:
	// - events: <stem>.jsonl (append-only)
	// - meta:   <stem>.meta.json (lightweight snapshot)
	expectedEvents := filepath.Join(tmpDir, "telegram_123456.jsonl")
	if _, err := os.Stat(expectedEvents); os.IsNotExist(err) {
		t.Fatalf("expected session events file %s to exist", expectedEvents)
	}
	expectedMeta := filepath.Join(tmpDir, "telegram_123456.meta.json")
	if _, err := os.Stat(expectedMeta); os.IsNotExist(err) {
		t.Fatalf("expected session meta file %s to exist", expectedMeta)
	}

	// Load into a fresh manager and verify the session round-trips.
	sm2 := NewSessionManager(tmpDir)
	history := sm2.GetHistory(key)
	if len(history) != 1 {
		t.Fatalf("expected 1 message after reload, got %d", len(history))
	}
	if history[0].Content != "hello" {
		t.Errorf("expected message content %q, got %q", "hello", history[0].Content)
	}
}

func TestSave_RejectsPathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSessionManager(tmpDir)

	badKeys := []string{"", ".", "..", "foo/bar", "foo\\bar"}
	for _, key := range badKeys {
		sm.GetOrCreate(key)
		if err := sm.Save(key); err == nil {
			t.Errorf("Save(%q) should have failed but didn't", key)
		}
	}
}

func TestGetSessionSnapshot_IsDeepCopy(t *testing.T) {
	sm := NewSessionManager(t.TempDir())
	key := "agent:main:main"
	sm.AddFullMessage(key, nestedSessionMessage())

	snapshot, ok := sm.GetSessionSnapshot(key)
	if !ok {
		t.Fatalf("expected snapshot for key %q", key)
	}
	if len(snapshot.Messages) != 1 {
		t.Fatalf("expected 1 message in snapshot, got %d", len(snapshot.Messages))
	}

	// Mutate returned snapshot; internal state should remain unchanged.
	snapshot.Messages[0].Content = "mutated"
	snapshot.Messages[0].Media[0] = "media://mutated"
	snapshot.Messages[0].SystemParts[0].Text = "changed"
	snapshot.Messages[0].SystemParts[0].CacheControl.Type = "persist"
	snapshot.Messages[0].ToolCalls[0].Function.Name = "write_file"
	snapshot.Messages[0].ToolCalls[0].Arguments["path"] = "mutated.txt"
	snapshot.Messages[0].ToolCalls[0].Arguments["nested"].(map[string]any)["count"] = 99
	snapshot.Messages[0].ToolCalls[0].Arguments["list"].([]any)[1].(map[string]any)["id"] = "changed"
	snapshot.Messages[0].ToolCalls[0].ExtraContent.Google.ThoughtSignature = "sig-mutated"
	history := sm.GetHistory(key)
	if history[0].Content != "hello" {
		t.Fatalf("snapshot mutation leaked into manager state, got %q", history[0].Content)
	}
	if history[0].Media[0] != "media://one" {
		t.Fatalf("media mutation leaked into manager state, got %q", history[0].Media[0])
	}
	if history[0].SystemParts[0].Text != "system" || history[0].SystemParts[0].CacheControl.Type != "ephemeral" {
		t.Fatalf("system part mutation leaked into manager state, got %+v", history[0].SystemParts[0])
	}
	if history[0].ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("function mutation leaked into manager state, got %q", history[0].ToolCalls[0].Function.Name)
	}
	if got := history[0].ToolCalls[0].Arguments["path"]; got != "README.md" {
		t.Fatalf("arguments mutation leaked into manager state, got %v", got)
	}
	if got := history[0].ToolCalls[0].Arguments["nested"].(map[string]any)["count"]; got != 2 {
		t.Fatalf("nested arguments mutation leaked into manager state, got %v", got)
	}
	if got := history[0].ToolCalls[0].Arguments["list"].([]any)[1].(map[string]any)["id"]; got != "b" {
		t.Fatalf("list arguments mutation leaked into manager state, got %v", got)
	}
	if got := history[0].ToolCalls[0].ExtraContent.Google.ThoughtSignature; got != "sig-original" {
		t.Fatalf("extra content mutation leaked into manager state, got %q", got)
	}
}

func TestGetHistory_DeepCopiesNestedFields(t *testing.T) {
	sm := NewSessionManager(t.TempDir())
	key := "agent:main:main"
	sm.AddFullMessage(key, nestedSessionMessage())

	history := sm.GetHistory(key)
	history[0].Media[0] = "media://history-mutated"
	history[0].ToolCalls[0].Arguments["path"] = "history-mutated.txt"
	history[0].ToolCalls[0].Arguments["nested"].(map[string]any)["count"] = 42

	refetched := sm.GetHistory(key)
	if got := refetched[0].Media[0]; got != "media://one" {
		t.Fatalf("media mutation leaked into manager state, got %q", got)
	}
	if got := refetched[0].ToolCalls[0].Arguments["path"]; got != "README.md" {
		t.Fatalf("arguments mutation leaked into manager state, got %v", got)
	}
	if got := refetched[0].ToolCalls[0].Arguments["nested"].(map[string]any)["count"]; got != 2 {
		t.Fatalf("nested arguments mutation leaked into manager state, got %v", got)
	}
}

func TestAddFullMessage_DeepCopiesInputNestedFields(t *testing.T) {
	sm := NewSessionManager(t.TempDir())
	key := "agent:main:main"
	msg := nestedSessionMessage()

	sm.AddFullMessage(key, msg)
	msg.Content = "mutated-input"
	msg.Media[0] = "media://input-mutated"
	msg.SystemParts[0].CacheControl.Type = "persist"
	msg.ToolCalls[0].Function.Name = "write_file"
	msg.ToolCalls[0].Arguments["path"] = "mutated-input.txt"
	msg.ToolCalls[0].Arguments["nested"].(map[string]any)["count"] = 77

	got := sm.GetHistory(key)
	if got[0].Content != "hello" {
		t.Fatalf("input content mutation leaked into manager state, got %q", got[0].Content)
	}
	if got[0].Media[0] != "media://one" {
		t.Fatalf("input media mutation leaked into manager state, got %q", got[0].Media[0])
	}
	if got[0].SystemParts[0].CacheControl.Type != "ephemeral" {
		t.Fatalf("input system part mutation leaked into manager state, got %q", got[0].SystemParts[0].CacheControl.Type)
	}
	if got[0].ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("input function mutation leaked into manager state, got %q", got[0].ToolCalls[0].Function.Name)
	}
	if got[0].ToolCalls[0].Arguments["path"] != "README.md" {
		t.Fatalf("input argument mutation leaked into manager state, got %v", got[0].ToolCalls[0].Arguments["path"])
	}
	if got[0].ToolCalls[0].Arguments["nested"].(map[string]any)["count"] != 2 {
		t.Fatalf("input nested mutation leaked into manager state, got %v", got[0].ToolCalls[0].Arguments["nested"].(map[string]any)["count"])
	}
}

func TestListSessionSnapshots_SortedByUpdatedDesc(t *testing.T) {
	sm := NewSessionManager(t.TempDir())
	sm.AddMessage("session-a", "user", "old")
	time.Sleep(10 * time.Millisecond)
	sm.AddMessage("session-b", "user", "new")

	snapshots := sm.ListSessionSnapshots()
	if len(snapshots) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snapshots))
	}
	if snapshots[0].Key != "session-b" {
		t.Fatalf("expected newest session first, got %q", snapshots[0].Key)
	}

	// Ensure returned slices are copies.
	snapshots[0].Messages[0].Content = "changed"
	history := sm.GetHistory("session-b")
	if history[0].Content != "new" {
		t.Fatalf("snapshot mutation leaked into manager state, got %q", history[0].Content)
	}
}

func TestCompactionStateLifecycle(t *testing.T) {
	sm := NewSessionManager(t.TempDir())
	key := "agent:main:main"
	sm.AddMessage(key, "user", "hello")

	count, flushedCount, flushAt := sm.GetCompactionState(key)
	if count != 0 || flushedCount != 0 || !flushAt.IsZero() {
		t.Fatalf("initial compaction state unexpected: count=%d flushed=%d flushAt=%v", count, flushedCount, flushAt)
	}

	next := sm.IncrementCompactionCount(key)
	if next != 1 {
		t.Fatalf("IncrementCompactionCount = %d, want 1", next)
	}

	sm.MarkMemoryFlush(key, next)
	count, flushedCount, flushAt = sm.GetCompactionState(key)
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if flushedCount != 1 {
		t.Fatalf("flushedCount = %d, want 1", flushedCount)
	}
	if flushAt.IsZero() {
		t.Fatal("flushAt should be set")
	}
}

func TestSetHistory_DeepCopiesInput(t *testing.T) {
	sm := NewSessionManager(t.TempDir())
	key := "agent:main:main"
	sm.AddMessage(key, "user", "seed")

	history := []providers.Message{nestedSessionMessage()}
	sm.SetHistory(key, history)
	history[0].Content = "mutated"
	history[0].Media[0] = "media://sethistory-mutated"
	history[0].ToolCalls[0].Arguments["path"] = "sethistory-mutated.txt"
	history[0].ToolCalls[0].Arguments["nested"].(map[string]any)["count"] = -1

	got := sm.GetHistory(key)
	if len(got) != 1 {
		t.Fatalf("expected 1 history item, got %d", len(got))
	}
	if got[0].Content != "hello" {
		t.Fatalf("history was not deep-copied, got %q", got[0].Content)
	}
	if got[0].Media[0] != "media://one" {
		t.Fatalf("media was not deep-copied, got %q", got[0].Media[0])
	}
	if got[0].ToolCalls[0].Arguments["path"] != "README.md" {
		t.Fatalf("arguments were not deep-copied, got %v", got[0].ToolCalls[0].Arguments["path"])
	}
	if got[0].ToolCalls[0].Arguments["nested"].(map[string]any)["count"] != 2 {
		t.Fatalf("nested arguments were not deep-copied, got %v", got[0].ToolCalls[0].Arguments["nested"].(map[string]any)["count"])
	}
}

func TestSessionTree_SwitchLeafBranchesHistory(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSessionManager(tmpDir)
	key := "telegram:123456"

	sm.AddMessage(key, "user", "A")
	sm.AddMessage(key, "assistant", "B")
	sm.AddMessage(key, "user", "C")

	events, err := readJSONLEvents(sm.eventsPath(key))
	if err != nil {
		t.Fatalf("read events: %v", err)
	}

	msgIDs := make([]string, 0, 3)
	for _, ev := range events {
		if ev.Type == EventSessionMessage && ev.Message != nil {
			if ev.ID != "" {
				msgIDs = append(msgIDs, ev.ID)
			}
		}
	}
	if len(msgIDs) < 3 {
		t.Fatalf("expected >=3 message events, got %d", len(msgIDs))
	}

	originalLeaf := sm.LeafEventID(key)
	if originalLeaf == "" {
		t.Fatal("expected a non-empty leaf after messages")
	}

	branchPoint := msgIDs[1]
	from, to, err := sm.SwitchLeaf(key, branchPoint)
	if err != nil {
		t.Fatalf("SwitchLeaf failed: %v", err)
	}
	if from == "" || to != branchPoint {
		t.Fatalf("unexpected leaf switch from=%q to=%q", from, to)
	}

	h1 := sm.GetHistory(key)
	if len(h1) != 2 {
		t.Fatalf("expected 2 messages after switch, got %d", len(h1))
	}
	if h1[0].Content != "A" || h1[1].Content != "B" {
		t.Fatalf("unexpected history after switch: %#v", h1)
	}

	sm.AddMessage(key, "user", "D")

	h2 := sm.GetHistory(key)
	if len(h2) != 3 {
		t.Fatalf("expected 3 messages after branching, got %d", len(h2))
	}
	if h2[0].Content != "A" || h2[1].Content != "B" || h2[2].Content != "D" {
		t.Fatalf("unexpected history after branching: %#v", h2)
	}

	if sm.LeafEventID(key) == originalLeaf {
		t.Fatal("expected new leaf after branching")
	}

	sm2 := NewSessionManager(tmpDir)
	h3 := sm2.GetHistory(key)
	if len(h3) != 3 {
		t.Fatalf("expected 3 messages after reload, got %d", len(h3))
	}
	if h3[0].Content != "A" || h3[1].Content != "B" || h3[2].Content != "D" {
		t.Fatalf("unexpected history after reload: %#v", h3)
	}
}

func TestSessionModelOverride_TTLAndPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	sm := NewSessionManager(tmpDir)
	key := "cli:direct"

	sm.AddMessage(key, "user", "hello")

	expiresAt, err := sm.SetModelOverride(key, "test-model", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("SetModelOverride failed: %v", err)
	}
	if expiresAt == nil {
		t.Fatal("expected expiresAt to be set")
	}

	if got, ok := sm.EffectiveModelOverride(key); !ok || got != "test-model" {
		t.Fatalf("expected active override, got model=%q ok=%v", got, ok)
	}

	// Reload should preserve override (until expiry).
	sm2 := NewSessionManager(tmpDir)
	if got, ok := sm2.EffectiveModelOverride(key); !ok || got != "test-model" {
		t.Fatalf("expected override after reload, got model=%q ok=%v", got, ok)
	}

	time.Sleep(25 * time.Millisecond)

	// After expiry, EffectiveModelOverride returns false and clears persisted state.
	if got, ok := sm2.EffectiveModelOverride(key); ok || got != "" {
		t.Fatalf("expected override to be expired, got model=%q ok=%v", got, ok)
	}

	sm3 := NewSessionManager(tmpDir)
	if got, ok := sm3.EffectiveModelOverride(key); ok || got != "" {
		t.Fatalf("expected override to remain cleared after reload, got model=%q ok=%v", got, ok)
	}
}

func TestSessionManager_EvictsExpiredSessionsOnCreate(t *testing.T) {
	tmpDir := t.TempDir()
	sm := newSessionManagerWithGC(tmpDir, 10, 25*time.Millisecond)

	staleKey := "telegram:stale"
	sm.AddMessage(staleKey, "user", "old")

	sm.mu.Lock()
	stale := sm.sessions[staleKey]
	stale.Updated = time.Now().Add(-time.Hour)
	sm.mu.Unlock()

	time.Sleep(30 * time.Millisecond)
	sm.GetOrCreate("telegram:new")

	if _, ok := sm.GetSessionSnapshot(staleKey); ok {
		t.Fatalf("expected expired session %q to be evicted from memory", staleKey)
	}

	reloaded := NewSessionManager(tmpDir)
	if snapshot, ok := reloaded.GetSessionSnapshot(staleKey); !ok || snapshot == nil {
		t.Fatalf("expected expired session %q to remain reloadable from disk", staleKey)
	}
}

func TestSessionManager_EvictsLeastRecentlyUpdatedSession(t *testing.T) {
	tmpDir := t.TempDir()
	sm := newSessionManagerWithGC(tmpDir, 2, 0)

	sm.AddMessage("telegram:oldest", "user", "one")
	sm.AddMessage("telegram:middle", "user", "two")

	sm.mu.Lock()
	sm.sessions["telegram:oldest"].Updated = time.Now().Add(-2 * time.Hour)
	sm.sessions["telegram:middle"].Updated = time.Now().Add(-1 * time.Hour)
	sm.mu.Unlock()

	sm.GetOrCreate("telegram:newest")

	if _, ok := sm.GetSessionSnapshot("telegram:oldest"); ok {
		t.Fatal("expected oldest session to be evicted")
	}
	if _, ok := sm.GetSessionSnapshot("telegram:middle"); !ok {
		t.Fatal("expected more recent session to remain")
	}
	if _, ok := sm.GetSessionSnapshot("telegram:newest"); !ok {
		t.Fatal("expected newly created session to remain")
	}
}

func TestSessionManager_SessionConfigDefaults(t *testing.T) {
	sm := newSessionManagerWithSessionConfig(t.TempDir(), config.SessionConfig{})

	if sm.maxSessions != config.DefaultSessionMaxSessions {
		t.Fatalf("maxSessions = %d, want %d", sm.maxSessions, config.DefaultSessionMaxSessions)
	}
	if sm.ttl != time.Duration(config.DefaultSessionTTLHours)*time.Hour {
		t.Fatalf("ttl = %v, want %v", sm.ttl, time.Duration(config.DefaultSessionTTLHours)*time.Hour)
	}
}

func TestSessionManager_SessionConfigJSONOverrides(t *testing.T) {
	var cfg struct {
		Session config.SessionConfig `json:"session"`
	}

	if err := json.Unmarshal([]byte(`{"session":{"max_sessions":7,"ttl_hours":12}}`), &cfg); err != nil {
		t.Fatalf("json.Unmarshal() error: %v", err)
	}

	sm := newSessionManagerWithSessionConfig(t.TempDir(), cfg.Session)
	if sm.maxSessions != 7 {
		t.Fatalf("maxSessions = %d, want 7", sm.maxSessions)
	}
	if sm.ttl != 12*time.Hour {
		t.Fatalf("ttl = %v, want %v", sm.ttl, 12*time.Hour)
	}
}

func TestSessionManager_NewSessionManagerWithConfigUsesOverrides(t *testing.T) {
	sm := NewSessionManagerWithConfig(t.TempDir(), config.SessionConfig{MaxSessions: 9, TTLHours: 6})
	if sm.maxSessions != 9 {
		t.Fatalf("maxSessions = %d, want 9", sm.maxSessions)
	}
	if sm.ttl != 6*time.Hour {
		t.Fatalf("ttl = %v, want %v", sm.ttl, 6*time.Hour)
	}
}

func TestSessionManager_ConcurrentGCWithReadsAndWrites(t *testing.T) {
	tmpDir := t.TempDir()
	sm := newSessionManagerWithGC(tmpDir, 8, 0)

	const (
		goroutines = 4
		iterations = 8
		keySpace   = 10
	)

	var wg sync.WaitGroup
	start := make(chan struct{})
	errCh := make(chan error, goroutines)

	for worker := 0; worker < goroutines; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for iter := 0; iter < iterations; iter++ {
				key := fmt.Sprintf("telegram:%02d", (worker+iter)%keySpace)
				session := sm.GetOrCreate(key)
				if session == nil {
					errCh <- fmt.Errorf("GetOrCreate(%q) returned nil", key)
					return
				}
				sm.AddMessage(key, "user", fmt.Sprintf("worker=%d iter=%d", worker, iter))
				_ = sm.GetHistory(key)
				_, _ = sm.GetSessionSnapshot(key)
			}
		}(worker)
	}

	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}

	sm.mu.RLock()
	inMemory := len(sm.sessions)
	sm.mu.RUnlock()
	if inMemory > sm.maxSessions {
		t.Fatalf("expected in-memory sessions <= %d, got %d", sm.maxSessions, inMemory)
	}

	reloaded := NewSessionManagerWithConfig(tmpDir, config.SessionConfig{MaxSessions: sm.maxSessions})
	found := 0
	for key := 0; key < keySpace; key++ {
		if history := reloaded.GetHistory(fmt.Sprintf("telegram:%02d", key)); len(history) > 0 {
			found++
		}
	}
	if found == 0 {
		t.Fatal("expected persisted sessions to remain reloadable after concurrent GC activity")
	}
}
