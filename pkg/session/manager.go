package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

type Session struct {
	Key                        string              `json:"key"`
	Messages                   []providers.Message `json:"messages"`
	Summary                    string              `json:"summary,omitempty"`
	CompactionCount            int                 `json:"compaction_count,omitempty"`
	MemoryFlushAt              time.Time           `json:"memory_flush_at,omitempty"`
	MemoryFlushCompactionCount int                 `json:"memory_flush_compaction_count,omitempty"`
	Created                    time.Time           `json:"created"`
	Updated                    time.Time           `json:"updated"`

	// LastEventID tracks the last appended JSONL session event for parent linking.
	// It is not required for correctness, but improves tree reconstruction and reload.
	LastEventID string `json:"last_event_id,omitempty"`
}

type SessionManager struct {
	sessions map[string]*Session
	mu       sync.RWMutex
	storage  string
}

func NewSessionManager(storage string) *SessionManager {
	sm := &SessionManager{
		sessions: make(map[string]*Session),
		storage:  storage,
	}

	if storage != "" {
		os.MkdirAll(storage, 0o755)
		sm.loadSessions()
	}

	return sm
}

func (sm *SessionManager) GetOrCreate(key string) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[key]
	if ok {
		return session
	}

	session = &Session{
		Key:      key,
		Messages: []providers.Message{},
		Created:  time.Now(),
		Updated:  time.Now(),
	}
	sm.sessions[key] = session

	return session
}

func (sm *SessionManager) AddMessage(sessionKey, role, content string) {
	sm.AddFullMessage(sessionKey, providers.Message{
		Role:    role,
		Content: content,
	})
}

// AddFullMessage adds a complete message with tool calls and tool call ID to the session.
// This is used to save the full conversation flow including tool calls and tool results.
func (sm *SessionManager) AddFullMessage(sessionKey string, msg providers.Message) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[sessionKey]
	if !ok {
		session = &Session{
			Key:      sessionKey,
			Messages: []providers.Message{},
			Created:  time.Now(),
		}
		sm.sessions[sessionKey] = session
	}

	now := time.Now()
	if session.Created.IsZero() {
		session.Created = now
	}
	session.Messages = append(session.Messages, msg)
	session.Updated = now

	if sm.storage == "" {
		return
	}

	// Append durable JSONL event (session tree).
	msgCopy := msg
	ev := SessionEvent{
		Type:       EventSessionMessage,
		ID:         newEventID(),
		ParentID:   strings.TrimSpace(session.LastEventID),
		TS:         now.UTC().Format(time.RFC3339Nano),
		TSMS:       now.UnixMilli(),
		SessionKey: strings.TrimSpace(sessionKey),
		Message:    &msgCopy,
	}
	if path := sm.eventsPath(sessionKey); path != "" {
		if err := appendJSONLEvent(path, ev); err == nil {
			session.LastEventID = ev.ID
		}
	}

	// Best-effort meta snapshot for the gateway console.
	if path := sm.metaPath(sessionKey); path != "" {
		_ = writeMetaFile(path, buildSessionMeta(session))
	}
}

func (sm *SessionManager) GetHistory(key string) []providers.Message {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, ok := sm.sessions[key]
	if !ok {
		return []providers.Message{}
	}

	history := make([]providers.Message, len(session.Messages))
	copy(history, session.Messages)
	return history
}

func (sm *SessionManager) GetSummary(key string) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, ok := sm.sessions[key]
	if !ok {
		return ""
	}
	return session.Summary
}

func (sm *SessionManager) SetSummary(key string, summary string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[key]
	if ok {
		now := time.Now()
		session.Summary = summary
		session.Updated = now

		if sm.storage == "" {
			return
		}

		ev := SessionEvent{
			Type:       EventSessionSummary,
			ID:         newEventID(),
			ParentID:   strings.TrimSpace(session.LastEventID),
			TS:         now.UTC().Format(time.RFC3339Nano),
			TSMS:       now.UnixMilli(),
			SessionKey: strings.TrimSpace(key),
			Summary:    summary,
		}
		if path := sm.eventsPath(key); path != "" {
			if err := appendJSONLEvent(path, ev); err == nil {
				session.LastEventID = ev.ID
			}
		}
		if path := sm.metaPath(key); path != "" {
			_ = writeMetaFile(path, buildSessionMeta(session))
		}
	}
}

func (sm *SessionManager) TruncateHistory(key string, keepLast int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[key]
	if !ok {
		return
	}

	now := time.Now()
	if keepLast <= 0 {
		session.Messages = []providers.Message{}
		session.Updated = now
	} else {
		if len(session.Messages) <= keepLast {
			return
		}

		session.Messages = session.Messages[len(session.Messages)-keepLast:]
		session.Updated = now
	}

	if sm.storage == "" {
		return
	}

	ev := SessionEvent{
		Type:       EventSessionHistoryTrunc,
		ID:         newEventID(),
		ParentID:   strings.TrimSpace(session.LastEventID),
		TS:         now.UTC().Format(time.RFC3339Nano),
		TSMS:       now.UnixMilli(),
		SessionKey: strings.TrimSpace(key),
		KeepLast:   keepLast,
	}
	if path := sm.eventsPath(key); path != "" {
		if err := appendJSONLEvent(path, ev); err == nil {
			session.LastEventID = ev.ID
		}
	}
	if path := sm.metaPath(key); path != "" {
		_ = writeMetaFile(path, buildSessionMeta(session))
	}
}

func (sm *SessionManager) IncrementCompactionCount(key string) int {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[key]
	if !ok {
		return 0
	}
	session.CompactionCount++
	now := time.Now()
	session.Updated = now

	if sm.storage != "" {
		ev := SessionEvent{
			Type:            EventSessionCompactionInc,
			ID:              newEventID(),
			ParentID:        strings.TrimSpace(session.LastEventID),
			TS:              now.UTC().Format(time.RFC3339Nano),
			TSMS:            now.UnixMilli(),
			SessionKey:      strings.TrimSpace(key),
			CompactionCount: session.CompactionCount,
		}
		if path := sm.eventsPath(key); path != "" {
			if err := appendJSONLEvent(path, ev); err == nil {
				session.LastEventID = ev.ID
			}
		}
		if path := sm.metaPath(key); path != "" {
			_ = writeMetaFile(path, buildSessionMeta(session))
		}
	}
	return session.CompactionCount
}

func (sm *SessionManager) MarkMemoryFlush(key string, compactionCount int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[key]
	if !ok {
		return
	}
	now := time.Now()
	session.MemoryFlushAt = now
	session.MemoryFlushCompactionCount = compactionCount
	session.Updated = now

	if sm.storage != "" {
		ev := SessionEvent{
			Type:                       EventSessionMemoryFlush,
			ID:                         newEventID(),
			ParentID:                   strings.TrimSpace(session.LastEventID),
			TS:                         now.UTC().Format(time.RFC3339Nano),
			TSMS:                       now.UnixMilli(),
			SessionKey:                 strings.TrimSpace(key),
			MemoryFlushAt:              session.MemoryFlushAt,
			MemoryFlushCompactionCount: session.MemoryFlushCompactionCount,
		}
		if path := sm.eventsPath(key); path != "" {
			if err := appendJSONLEvent(path, ev); err == nil {
				session.LastEventID = ev.ID
			}
		}
		if path := sm.metaPath(key); path != "" {
			_ = writeMetaFile(path, buildSessionMeta(session))
		}
	}
}

func (sm *SessionManager) GetCompactionState(key string) (count int, flushCount int, flushAt time.Time) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, ok := sm.sessions[key]
	if !ok {
		return 0, 0, time.Time{}
	}
	return session.CompactionCount, session.MemoryFlushCompactionCount, session.MemoryFlushAt
}

// sanitizeFilename converts a session key into a cross-platform safe filename.
// Session keys use "channel:chatID" (e.g. "telegram:123456") but ':' is the
// volume separator on Windows, so filepath.Base would misinterpret the key.
// We replace it with '_'. The original key is preserved inside the JSON file,
// so loadSessions still maps back to the right in-memory key.
func sanitizeFilename(key string) string {
	return strings.ReplaceAll(key, ":", "_")
}

func fileStemForKey(key string) (string, bool) {
	stem := sanitizeFilename(strings.TrimSpace(key))
	if stem == "" || stem == "." {
		return "", false
	}
	// filepath.IsLocal rejects empty names, "..", absolute paths, and
	// OS-reserved device names (NUL, COM1 … on Windows).
	if !filepath.IsLocal(stem) {
		return "", false
	}
	// Reject any directory separators to ensure the session file is always written
	// directly inside sm.storage.
	if strings.ContainsAny(stem, `/\`) {
		return "", false
	}
	return stem, true
}

func (sm *SessionManager) eventsPath(key string) string {
	if sm == nil {
		return ""
	}
	stem, ok := fileStemForKey(key)
	if !ok {
		return ""
	}
	return filepath.Join(sm.storage, stem+".jsonl")
}

func (sm *SessionManager) metaPath(key string) string {
	if sm == nil {
		return ""
	}
	stem, ok := fileStemForKey(key)
	if !ok {
		return ""
	}
	return filepath.Join(sm.storage, stem+".meta.json")
}

func (sm *SessionManager) legacySnapshotPath(key string) string {
	if sm == nil {
		return ""
	}
	stem, ok := fileStemForKey(key)
	if !ok {
		return ""
	}
	return filepath.Join(sm.storage, stem+".json")
}

func buildSessionMeta(s *Session) SessionMeta {
	meta := SessionMeta{
		Key:           s.Key,
		Summary:       s.Summary,
		Created:       s.Created,
		Updated:       s.Updated,
		LastEventID:   strings.TrimSpace(s.LastEventID),
		MessagesCount: len(s.Messages),
	}
	return meta
}

func (sm *SessionManager) Save(key string) error {
	if sm.storage == "" {
		return nil
	}

	filename, ok := fileStemForKey(key)
	if !ok {
		return os.ErrInvalid
	}

	// Lightweight persistence: write a small meta snapshot for the console/ops UI.
	sm.mu.RLock()
	stored, ok := sm.sessions[key]
	if !ok || stored == nil {
		sm.mu.RUnlock()
		return nil
	}
	meta := buildSessionMeta(stored)
	sm.mu.RUnlock()

	return writeMetaFile(filepath.Join(sm.storage, filename+".meta.json"), meta)
}

func (sm *SessionManager) loadSessions() error {
	files, err := os.ReadDir(sm.storage)
	if err != nil {
		return err
	}

	type sessFiles struct {
		base   string
		meta   string
		jsonl  string
		legacy string
	}

	byBase := map[string]*sessFiles{}

	get := func(base string) *sessFiles {
		if base == "" {
			return nil
		}
		sf := byBase[base]
		if sf == nil {
			sf = &sessFiles{base: base}
			byBase[base] = sf
		}
		return sf
	}

	for _, ent := range files {
		if ent == nil || ent.IsDir() {
			continue
		}
		name := strings.TrimSpace(ent.Name())
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		lower := strings.ToLower(name)

		switch {
		case strings.HasSuffix(lower, ".meta.json"):
			base := strings.TrimSuffix(name, name[len(name)-len(".meta.json"):])
			if sf := get(base); sf != nil {
				sf.meta = filepath.Join(sm.storage, name)
			}
		case strings.HasSuffix(lower, ".jsonl"):
			base := strings.TrimSuffix(name, name[len(name)-len(".jsonl"):])
			if sf := get(base); sf != nil {
				sf.jsonl = filepath.Join(sm.storage, name)
			}
		case strings.HasSuffix(lower, ".json"):
			// Legacy full snapshot (*.json) from older versions.
			// Exclude meta snapshots (*.meta.json).
			if strings.HasSuffix(lower, ".meta.json") {
				continue
			}
			base := strings.TrimSuffix(name, name[len(name)-len(".json"):])
			if sf := get(base); sf != nil {
				sf.legacy = filepath.Join(sm.storage, name)
			}
		default:
			continue
		}
	}

	loadMeta := func(path string) (*SessionMeta, error) {
		if strings.TrimSpace(path) == "" {
			return nil, os.ErrNotExist
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var meta SessionMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			return nil, err
		}
		if strings.TrimSpace(meta.Key) == "" {
			return nil, fmt.Errorf("meta missing key")
		}
		return &meta, nil
	}

	loadLegacy := func(path string) (*Session, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var sess Session
		if err := json.Unmarshal(data, &sess); err != nil {
			return nil, err
		}
		if strings.TrimSpace(sess.Key) == "" {
			return nil, fmt.Errorf("legacy snapshot missing key")
		}
		if sess.Messages == nil {
			sess.Messages = []providers.Message{}
		}
		return &sess, nil
	}

	applyEvents := func(sess *Session, path string) error {
		if sess == nil {
			return fmt.Errorf("session is nil")
		}
		if strings.TrimSpace(path) == "" {
			return os.ErrNotExist
		}
		events, err := readJSONLEvents(path)
		if err != nil {
			return err
		}

		// Derive timestamps when meta is missing.
		var minTSMS, maxTSMS int64
		for _, ev := range events {
			if ev.TSMS > 0 {
				if minTSMS == 0 || ev.TSMS < minTSMS {
					minTSMS = ev.TSMS
				}
				if ev.TSMS > maxTSMS {
					maxTSMS = ev.TSMS
				}
			}

			switch ev.Type {
			case EventSessionMessage:
				if ev.Message != nil {
					sess.Messages = append(sess.Messages, *ev.Message)
				}
			case EventSessionSummary:
				sess.Summary = ev.Summary
			case EventSessionHistorySet:
				if ev.History != nil {
					msgs := make([]providers.Message, len(ev.History))
					copy(msgs, ev.History)
					sess.Messages = msgs
				}
			case EventSessionHistoryTrunc:
				if ev.KeepLast <= 0 {
					sess.Messages = []providers.Message{}
				} else if len(sess.Messages) > ev.KeepLast {
					sess.Messages = sess.Messages[len(sess.Messages)-ev.KeepLast:]
				}
			case EventSessionCompactionInc:
				if ev.CompactionCount > sess.CompactionCount {
					sess.CompactionCount = ev.CompactionCount
				}
			case EventSessionMemoryFlush:
				if !ev.MemoryFlushAt.IsZero() {
					sess.MemoryFlushAt = ev.MemoryFlushAt
				}
				if ev.MemoryFlushCompactionCount != 0 {
					sess.MemoryFlushCompactionCount = ev.MemoryFlushCompactionCount
				}
			default:
				// Ignore unknown event types for forward compatibility.
			}

			if strings.TrimSpace(ev.ID) != "" {
				sess.LastEventID = strings.TrimSpace(ev.ID)
			}
		}

		if sess.Created.IsZero() && minTSMS > 0 {
			sess.Created = time.UnixMilli(minTSMS)
		}
		if sess.Updated.IsZero() && maxTSMS > 0 {
			sess.Updated = time.UnixMilli(maxTSMS)
		} else if maxTSMS > 0 {
			// If meta-derived Updated is older than the events file, prefer the events.
			evUpdated := time.UnixMilli(maxTSMS)
			if sess.Updated.Before(evUpdated) {
				sess.Updated = evUpdated
			}
		}
		return nil
	}

	// Populate sm.sessions from JSONL or legacy snapshots.
	for _, sf := range byBase {
		if sf == nil {
			continue
		}

		var meta *SessionMeta
		if m, err := loadMeta(sf.meta); err == nil {
			meta = m
		}

		// Determine key and initial session skeleton.
		key := ""
		if meta != nil {
			key = strings.TrimSpace(meta.Key)
		}

		var sess *Session

		// Prefer JSONL when present.
		if strings.TrimSpace(sf.jsonl) != "" {
			if key == "" && meta == nil {
				// Best-effort: attempt to infer key from legacy snapshot.
				if strings.TrimSpace(sf.legacy) != "" {
					if legacy, err := loadLegacy(sf.legacy); err == nil {
						key = strings.TrimSpace(legacy.Key)
					}
				}
			}
			if key == "" {
				// Fallback: use base token (not ideal, but keeps data accessible).
				key = sf.base
			}

			sess = &Session{
				Key:      key,
				Messages: []providers.Message{},
				Created:  time.Now(),
				Updated:  time.Now(),
			}
			if meta != nil {
				sess.Summary = strings.TrimSpace(meta.Summary)
				sess.Created = meta.Created
				sess.Updated = meta.Updated
				sess.LastEventID = strings.TrimSpace(meta.LastEventID)
			}

			_ = applyEvents(sess, sf.jsonl)

			sm.sessions[sess.Key] = sess
			continue
		}

		// Legacy-only session: load full snapshot, then migrate into JSONL/meta.
		if strings.TrimSpace(sf.legacy) != "" {
			legacy, err := loadLegacy(sf.legacy)
			if err != nil {
				continue
			}
			sess = legacy

			// Best-effort: migrate legacy snapshot into JSONL for future durability.
			if sm.storage != "" {
				_ = sm.migrateLegacyToJSONL(sess)
			}

			sm.sessions[sess.Key] = sess
			continue
		}
	}

	return nil
}

func (sm *SessionManager) migrateLegacyToJSONL(sess *Session) error {
	if sm == nil || strings.TrimSpace(sm.storage) == "" || sess == nil {
		return nil
	}
	key := strings.TrimSpace(sess.Key)
	if key == "" {
		return nil
	}
	jsonlPath := sm.eventsPath(key)
	if strings.TrimSpace(jsonlPath) == "" {
		return nil
	}
	if _, err := os.Stat(jsonlPath); err == nil {
		// Already migrated.
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	now := time.Now()
	parent := strings.TrimSpace(sess.LastEventID)
	for _, msg := range sess.Messages {
		msgCopy := msg
		ev := SessionEvent{
			Type:       EventSessionMessage,
			ID:         newEventID(),
			ParentID:   parent,
			TS:         now.UTC().Format(time.RFC3339Nano),
			TSMS:       now.UnixMilli(),
			SessionKey: key,
			Message:    &msgCopy,
		}
		if err := appendJSONLEvent(jsonlPath, ev); err != nil {
			return err
		}
		parent = ev.ID
		sess.LastEventID = ev.ID
	}

	if strings.TrimSpace(sess.Summary) != "" {
		ev := SessionEvent{
			Type:       EventSessionSummary,
			ID:         newEventID(),
			ParentID:   parent,
			TS:         now.UTC().Format(time.RFC3339Nano),
			TSMS:       now.UnixMilli(),
			SessionKey: key,
			Summary:    sess.Summary,
		}
		if err := appendJSONLEvent(jsonlPath, ev); err != nil {
			return err
		}
		sess.LastEventID = ev.ID
	}

	// Write a meta snapshot for the console.
	if path := sm.metaPath(key); path != "" {
		_ = writeMetaFile(path, buildSessionMeta(sess))
	}
	return nil
}

// SetHistory updates the messages of a session.
func (sm *SessionManager) SetHistory(key string, history []providers.Message) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[key]
	if ok {
		// Create a deep copy to strictly isolate internal state
		// from the caller's slice.
		msgs := make([]providers.Message, len(history))
		copy(msgs, history)
		session.Messages = msgs
		now := time.Now()
		session.Updated = now

		if sm.storage == "" {
			return
		}

		ev := SessionEvent{
			Type:       EventSessionHistorySet,
			ID:         newEventID(),
			ParentID:   strings.TrimSpace(session.LastEventID),
			TS:         now.UTC().Format(time.RFC3339Nano),
			TSMS:       now.UnixMilli(),
			SessionKey: strings.TrimSpace(key),
			History:    msgs,
		}
		if path := sm.eventsPath(key); path != "" {
			if err := appendJSONLEvent(path, ev); err == nil {
				session.LastEventID = ev.ID
			}
		}
		if path := sm.metaPath(key); path != "" {
			_ = writeMetaFile(path, buildSessionMeta(session))
		}
	}
}

// GetSessionSnapshot returns a deep-copied snapshot of a session by key.
func (sm *SessionManager) GetSessionSnapshot(key string) (*Session, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	stored, ok := sm.sessions[key]
	if !ok {
		return nil, false
	}

	snapshot := Session{
		Key:                        stored.Key,
		Summary:                    stored.Summary,
		CompactionCount:            stored.CompactionCount,
		MemoryFlushAt:              stored.MemoryFlushAt,
		MemoryFlushCompactionCount: stored.MemoryFlushCompactionCount,
		Created:                    stored.Created,
		Updated:                    stored.Updated,
	}
	if len(stored.Messages) > 0 {
		snapshot.Messages = make([]providers.Message, len(stored.Messages))
		copy(snapshot.Messages, stored.Messages)
	} else {
		snapshot.Messages = []providers.Message{}
	}

	return &snapshot, true
}

// ListSessionSnapshots returns deep-copied snapshots of all sessions, sorted by Updated descending.
func (sm *SessionManager) ListSessionSnapshots() []Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	snapshots := make([]Session, 0, len(sm.sessions))
	for _, stored := range sm.sessions {
		snapshot := Session{
			Key:                        stored.Key,
			Summary:                    stored.Summary,
			CompactionCount:            stored.CompactionCount,
			MemoryFlushAt:              stored.MemoryFlushAt,
			MemoryFlushCompactionCount: stored.MemoryFlushCompactionCount,
			Created:                    stored.Created,
			Updated:                    stored.Updated,
		}
		if len(stored.Messages) > 0 {
			snapshot.Messages = make([]providers.Message, len(stored.Messages))
			copy(snapshot.Messages, stored.Messages)
		} else {
			snapshot.Messages = []providers.Message{}
		}
		snapshots = append(snapshots, snapshot)
	}

	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Updated.After(snapshots[j].Updated)
	})

	return snapshots
}
