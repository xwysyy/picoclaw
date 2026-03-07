package bus

import (
	"strings"
	"sync"
	"time"
)

const (
	replyContextTTL        = 7 * 24 * time.Hour
	replyContextMaxEntries = 4096
)

type replyContextEntry struct {
	ctx       ReplyContext
	updatedAt time.Time
}

type replyContextStore struct {
	mu      sync.Mutex
	entries map[string]replyContextEntry
}

var replyContextStores sync.Map

func replyStoreFor(mb *MessageBus) *replyContextStore {
	if mb == nil {
		return nil
	}
	if store, ok := replyContextStores.Load(mb); ok {
		return store.(*replyContextStore)
	}
	store := &replyContextStore{entries: make(map[string]replyContextEntry)}
	actual, _ := replyContextStores.LoadOrStore(mb, store)
	return actual.(*replyContextStore)
}

func replyContextKey(channel, chatID, messageID string) string {
	channel = strings.ToLower(strings.TrimSpace(channel))
	chatID = strings.TrimSpace(chatID)
	messageID = strings.TrimSpace(messageID)
	if channel == "" || chatID == "" || messageID == "" {
		return ""
	}
	return channel + "\x00" + chatID + "\x00" + messageID
}

func (mb *MessageBus) BindReplyContext(channel, chatID, messageID string, ctx ReplyContext) {
	key := replyContextKey(channel, chatID, messageID)
	if key == "" {
		return
	}
	ctx.SessionKey = strings.TrimSpace(ctx.SessionKey)
	if ctx.SessionKey == "" {
		return
	}
	store := replyStoreFor(mb)
	if store == nil {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.pruneLocked(time.Now())
	store.entries[key] = replyContextEntry{ctx: ctx, updatedAt: time.Now()}
}

func (mb *MessageBus) LookupReplyContext(channel, chatID, messageID string) (ReplyContext, bool) {
	key := replyContextKey(channel, chatID, messageID)
	if key == "" {
		return ReplyContext{}, false
	}
	store := replyStoreFor(mb)
	if store == nil {
		return ReplyContext{}, false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.pruneLocked(time.Now())
	entry, ok := store.entries[key]
	if !ok {
		return ReplyContext{}, false
	}
	entry.updatedAt = time.Now()
	store.entries[key] = entry
	return entry.ctx, true
}

func (s *replyContextStore) pruneLocked(now time.Time) {
	if len(s.entries) == 0 {
		return
	}
	cutoff := now.Add(-replyContextTTL)
	for key, entry := range s.entries {
		if entry.updatedAt.Before(cutoff) {
			delete(s.entries, key)
		}
	}
	for len(s.entries) > replyContextMaxEntries {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range s.entries {
			if oldestKey == "" || entry.updatedAt.Before(oldest) {
				oldestKey = key
				oldest = entry.updatedAt
			}
		}
		if oldestKey == "" {
			break
		}
		delete(s.entries, oldestKey)
	}
}
