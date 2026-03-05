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

	coresession "github.com/xwysyy/X-Claw/internal/core/session"
	"github.com/xwysyy/X-Claw/pkg/providers"
	"github.com/xwysyy/X-Claw/pkg/utils"
)

// Type aliases keep existing imports stable while moving canonical session domain
// types into internal/core.
type Session = coresession.Session

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
	key = utils.CanonicalSessionKey(key)

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
	sessionKey = utils.CanonicalSessionKey(sessionKey)
	if sessionKey == "" {
		return
	}

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
		SessionKey: sessionKey,
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
	key = utils.CanonicalSessionKey(key)
	if key == "" {
		return []providers.Message{}
	}

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
	key = utils.CanonicalSessionKey(key)
	if key == "" {
		return ""
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, ok := sm.sessions[key]
	if !ok {
		return ""
	}
	return session.Summary
}

// GetActiveAgentID returns the active agent id for this session, if any.
func (sm *SessionManager) GetActiveAgentID(key string) string {
	key = utils.CanonicalSessionKey(key)
	if key == "" {
		return ""
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, ok := sm.sessions[key]
	if !ok || session == nil {
		return ""
	}
	return strings.TrimSpace(session.ActiveAgentID)
}

// SetActiveAgentID updates the active agent id for this session.
// It creates the session if it does not exist, and persists the change via JSONL/meta when storage is enabled.
func (sm *SessionManager) SetActiveAgentID(key, agentID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	key = utils.CanonicalSessionKey(key)
	if key == "" {
		return
	}

	session, ok := sm.sessions[key]
	if !ok || session == nil {
		session = &Session{
			Key:      key,
			Messages: []providers.Message{},
			Created:  time.Now(),
		}
		sm.sessions[key] = session
	}

	now := time.Now()
	session.ActiveAgentID = strings.TrimSpace(agentID)
	session.Updated = now

	if sm.storage == "" {
		return
	}

	ev := SessionEvent{
		Type:          EventSessionActiveAgent,
		ID:            newEventID(),
		ParentID:      strings.TrimSpace(session.LastEventID),
		TS:            now.UTC().Format(time.RFC3339Nano),
		TSMS:          now.UnixMilli(),
		SessionKey:    key,
		ActiveAgentID: session.ActiveAgentID,
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

func (sm *SessionManager) SetSummary(key string, summary string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	key = utils.CanonicalSessionKey(key)
	if key == "" {
		return
	}

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
			SessionKey: key,
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

	key = utils.CanonicalSessionKey(key)
	if key == "" {
		return
	}

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
		SessionKey: key,
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

	key = utils.CanonicalSessionKey(key)
	if key == "" {
		return 0
	}

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
			SessionKey:      key,
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

	key = utils.CanonicalSessionKey(key)
	if key == "" {
		return
	}

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
			SessionKey:                 key,
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
	key = utils.CanonicalSessionKey(key)
	if key == "" {
		return 0, 0, time.Time{}
	}

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
	stem := sanitizeFilename(utils.CanonicalSessionKey(key))
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
		ActiveAgentID: strings.TrimSpace(s.ActiveAgentID),
		Created:       s.Created,
		Updated:       s.Updated,
		LastEventID:   strings.TrimSpace(s.LastEventID),
		MessagesCount: len(s.Messages),
		ModelOverride: strings.TrimSpace(s.ModelOverride),
	}
	if s.ModelOverrideExpiresAtMS != nil && *s.ModelOverrideExpiresAtMS > 0 {
		expires := *s.ModelOverrideExpiresAtMS
		meta.ModelOverrideExpiresAtMS = &expires
	}
	return meta
}

func (sm *SessionManager) Save(key string) error {
	if sm.storage == "" {
		return nil
	}

	key = utils.CanonicalSessionKey(key)
	if key == "" {
		return os.ErrInvalid
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

	applyEvents := func(sess *Session, path string, leafHint string) error {
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

		replayed, effectiveLeaf := replayEvents(events, leafHint)
		if effectiveLeaf == "" {
			return nil
		}

		sess.Messages = replayed.Messages
		sess.Summary = replayed.Summary
		sess.ActiveAgentID = replayed.ActiveAgentID
		sess.CompactionCount = replayed.CompactionCount
		sess.MemoryFlushAt = replayed.MemoryFlushAt
		sess.MemoryFlushCompactionCount = replayed.MemoryFlushCompactionCount
		sess.LastEventID = effectiveLeaf

		if sess.Created.IsZero() && !replayed.Created.IsZero() {
			sess.Created = replayed.Created
		}
		if sess.Updated.IsZero() && !replayed.Updated.IsZero() {
			sess.Updated = replayed.Updated
		} else if !replayed.Updated.IsZero() && sess.Updated.Before(replayed.Updated) {
			sess.Updated = replayed.Updated
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
			}
			if meta != nil {
				sess.Summary = strings.TrimSpace(meta.Summary)
				sess.ActiveAgentID = strings.TrimSpace(meta.ActiveAgentID)
				sess.ModelOverride = strings.TrimSpace(meta.ModelOverride)
				if meta.ModelOverrideExpiresAtMS != nil && *meta.ModelOverrideExpiresAtMS > 0 {
					expires := *meta.ModelOverrideExpiresAtMS
					sess.ModelOverrideExpiresAtMS = &expires
				}
				sess.Created = meta.Created
				sess.Updated = meta.Updated
				sess.LastEventID = strings.TrimSpace(meta.LastEventID)
			}

			_ = applyEvents(sess, sf.jsonl, sess.LastEventID)
			if sess.Created.IsZero() {
				sess.Created = time.Now()
			}
			if sess.Updated.IsZero() {
				sess.Updated = sess.Created
			}

			sess.Key = utils.CanonicalSessionKey(sess.Key)
			if sess.Key != "" {
				sm.sessions[sess.Key] = sess
			}
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

			sess.Key = utils.CanonicalSessionKey(sess.Key)
			if sess.Key != "" {
				sm.sessions[sess.Key] = sess
			}
			continue
		}
	}

	return nil
}

func (sm *SessionManager) migrateLegacyToJSONL(sess *Session) error {
	if sm == nil || strings.TrimSpace(sm.storage) == "" || sess == nil {
		return nil
	}
	key := utils.CanonicalSessionKey(sess.Key)
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

	key = utils.CanonicalSessionKey(key)
	if key == "" {
		return
	}

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
			SessionKey: key,
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
	key = utils.CanonicalSessionKey(key)
	if key == "" {
		return nil, false
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	stored, ok := sm.sessions[key]
	if !ok {
		return nil, false
	}

	snapshot := Session{
		Key:                        stored.Key,
		Summary:                    stored.Summary,
		ActiveAgentID:              stored.ActiveAgentID,
		CompactionCount:            stored.CompactionCount,
		MemoryFlushAt:              stored.MemoryFlushAt,
		MemoryFlushCompactionCount: stored.MemoryFlushCompactionCount,
		Created:                    stored.Created,
		Updated:                    stored.Updated,
		ModelOverride:              stored.ModelOverride,
	}
	if stored.ModelOverrideExpiresAtMS != nil && *stored.ModelOverrideExpiresAtMS > 0 {
		expires := *stored.ModelOverrideExpiresAtMS
		snapshot.ModelOverrideExpiresAtMS = &expires
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
			ActiveAgentID:              stored.ActiveAgentID,
			CompactionCount:            stored.CompactionCount,
			MemoryFlushAt:              stored.MemoryFlushAt,
			MemoryFlushCompactionCount: stored.MemoryFlushCompactionCount,
			Created:                    stored.Created,
			Updated:                    stored.Updated,
			ModelOverride:              stored.ModelOverride,
		}
		if stored.ModelOverrideExpiresAtMS != nil && *stored.ModelOverrideExpiresAtMS > 0 {
			expires := *stored.ModelOverrideExpiresAtMS
			snapshot.ModelOverrideExpiresAtMS = &expires
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
