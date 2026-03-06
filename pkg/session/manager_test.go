package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xwysyy/X-Claw/pkg/providers"
)

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
	sm.AddMessage(key, "user", "hello")

	snapshot, ok := sm.GetSessionSnapshot(key)
	if !ok {
		t.Fatalf("expected snapshot for key %q", key)
	}
	if len(snapshot.Messages) != 1 {
		t.Fatalf("expected 1 message in snapshot, got %d", len(snapshot.Messages))
	}

	// Mutate returned snapshot; internal state should remain unchanged.
	snapshot.Messages[0].Content = "mutated"
	history := sm.GetHistory(key)
	if history[0].Content != "hello" {
		t.Fatalf("snapshot mutation leaked into manager state, got %q", history[0].Content)
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

	history := []providers.Message{{Role: "user", Content: "hello"}}
	sm.SetHistory(key, history)
	history[0].Content = "mutated"

	got := sm.GetHistory(key)
	if len(got) != 1 {
		t.Fatalf("expected 1 history item, got %d", len(got))
	}
	if got[0].Content != "hello" {
		t.Fatalf("history was not deep-copied, got %q", got[0].Content)
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
