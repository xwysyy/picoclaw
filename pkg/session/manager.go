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
	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/logger"
	"github.com/xwysyy/X-Claw/pkg/providers"
	"github.com/xwysyy/X-Claw/pkg/utils"
)

// Type aliases keep existing imports stable while moving canonical session domain
// types into internal/core.
type Session = coresession.Session

type SessionManager struct {
	sessions    map[string]*Session
	mu          sync.RWMutex
	storage     string
	maxSessions int
	ttl         time.Duration
}

func NewSessionManager(storage string) *SessionManager {
	return newSessionManagerWithSessionConfig(storage, config.SessionConfig{})
}

func NewSessionManagerWithConfig(storage string, sessionCfg config.SessionConfig) *SessionManager {
	return newSessionManagerWithSessionConfig(storage, sessionCfg)
}

func newSessionManagerWithSessionConfig(storage string, sessionCfg config.SessionConfig) *SessionManager {
	return newSessionManagerWithGC(storage, sessionCfg.EffectiveMaxSessions(), sessionCfg.EffectiveTTL())
}

func newSessionManagerWithGC(storage string, maxSessions int, ttl time.Duration) *SessionManager {
	if maxSessions < 0 {
		maxSessions = 0
	}
	if ttl < 0 {
		ttl = 0
	}

	sm := &SessionManager{
		sessions:    make(map[string]*Session),
		storage:     storage,
		maxSessions: maxSessions,
		ttl:         ttl,
	}

	if storage != "" {
		os.MkdirAll(storage, 0o755)
		sm.loadSessions()
	}

	sm.mu.Lock()
	sm.pruneSessionsLocked(time.Now(), 0)
	sm.mu.Unlock()

	return sm
}

func (sm *SessionManager) GetOrCreate(key string) *Session {
	key = utils.CanonicalSessionKey(key)
	if key == "" {
		return nil
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	if session, ok := sm.sessions[key]; ok {
		return session
	}

	sm.pruneSessionsLocked(time.Now(), 1)
	return sm.ensureSessionLocked(key)
}

func (sm *SessionManager) pruneSessionsLocked(now time.Time, reserve int) {
	if sm == nil {
		return
	}

	ttlEvicted := 0

	if sm.ttl > 0 {
		for key, session := range sm.sessions {
			if session == nil {
				delete(sm.sessions, key)
				ttlEvicted++
				continue
			}
			updatedAt := session.Updated
			if updatedAt.IsZero() {
				updatedAt = session.Created
			}
			if updatedAt.IsZero() || now.Sub(updatedAt) <= sm.ttl {
				continue
			}
			delete(sm.sessions, key)
			ttlEvicted++
		}
		if ttlEvicted > 0 {
			logger.InfoCF("session", "Evicted expired sessions from memory", map[string]any{
				"count":     ttlEvicted,
				"ttl_hours": int(sm.ttl / time.Hour),
			})
		}
	}

	if sm.maxSessions <= 0 {
		return
	}

	allowed := sm.maxSessions - reserve
	if allowed < 0 {
		allowed = 0
	}
	if len(sm.sessions) <= allowed {
		return
	}

	type sessionEntry struct {
		key     string
		updated time.Time
	}

	entries := make([]sessionEntry, 0, len(sm.sessions))
	for key, session := range sm.sessions {
		updatedAt := time.Time{}
		if session != nil {
			updatedAt = session.Updated
			if updatedAt.IsZero() {
				updatedAt = session.Created
			}
		}
		entries = append(entries, sessionEntry{key: key, updated: updatedAt})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].updated.Before(entries[j].updated)
	})

	toEvict := len(sm.sessions) - allowed
	lruEvicted := 0
	for i := 0; i < toEvict && i < len(entries); i++ {
		delete(sm.sessions, entries[i].key)
		lruEvicted++
	}
	if lruEvicted > 0 {
		logger.InfoCF("session", "Evicted least recently updated sessions from memory", map[string]any{
			"count":        lruEvicted,
			"max_sessions": sm.maxSessions,
		})
	}
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

	now := time.Now()
	if _, ok := sm.sessions[sessionKey]; !ok {
		sm.pruneSessionsLocked(now, 1)
	}
	session := sm.ensureSessionLocked(sessionKey)
	if session.Created.IsZero() {
		session.Created = now
	}
	storedMsg := cloneMessage(msg)
	session.Messages = append(session.Messages, storedMsg)
	session.Updated = now

	if sm.storage == "" {
		return
	}

	// Append durable JSONL event (session tree).
	msgCopy := cloneMessage(msg)
	ev := sm.newEventLocked(now, sessionKey, session, EventSessionMessage)
	ev.Message = &msgCopy
	sm.persistEventAndMetaLocked(sessionKey, session, ev)
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

	return cloneMessages(session.Messages)
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

		ev := sm.newEventLocked(now, key, session, EventSessionSummary)
		ev.Summary = summary
		sm.persistEventAndMetaLocked(key, session, ev)
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

	ev := sm.newEventLocked(now, key, session, EventSessionHistoryTrunc)
	ev.KeepLast = keepLast
	sm.persistEventAndMetaLocked(key, session, ev)
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
		ev := sm.newEventLocked(now, key, session, EventSessionCompactionInc)
		ev.CompactionCount = session.CompactionCount
		sm.persistEventAndMetaLocked(key, session, ev)
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
		ev := sm.newEventLocked(now, key, session, EventSessionMemoryFlush)
		ev.MemoryFlushAt = session.MemoryFlushAt
		ev.MemoryFlushCompactionCount = session.MemoryFlushCompactionCount
		sm.persistEventAndMetaLocked(key, session, ev)
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

	if _, ok := fileStemForKey(key); !ok {
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

	return writeMetaFile(sm.metaPath(key), meta)
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
		msgs := cloneMessages(history)
		session.Messages = msgs
		now := time.Now()
		session.Updated = now

		if sm.storage == "" {
			return
		}

		ev := sm.newEventLocked(now, key, session, EventSessionHistorySet)
		ev.History = msgs
		sm.persistEventAndMetaLocked(key, session, ev)
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
		snapshot.Messages = cloneMessages(stored.Messages)
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
			ModelOverride:              stored.ModelOverride,
		}
		if stored.ModelOverrideExpiresAtMS != nil && *stored.ModelOverrideExpiresAtMS > 0 {
			expires := *stored.ModelOverrideExpiresAtMS
			snapshot.ModelOverrideExpiresAtMS = &expires
		}
		if len(stored.Messages) > 0 {
			snapshot.Messages = cloneMessages(stored.Messages)
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
