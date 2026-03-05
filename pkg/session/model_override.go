package session

import (
	"fmt"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/pkg/providers"
	"github.com/xwysyy/X-Claw/pkg/utils"
)

func (sm *SessionManager) EffectiveModelOverride(key string) (string, bool) {
	key = utils.CanonicalSessionKey(key)
	if key == "" {
		return "", false
	}

	sm.mu.RLock()
	sess, ok := sm.sessions[key]
	if !ok || sess == nil {
		sm.mu.RUnlock()
		return "", false
	}
	model := strings.TrimSpace(sess.ModelOverride)
	expiresAtMS := sess.ModelOverrideExpiresAtMS
	sm.mu.RUnlock()

	if model == "" {
		return "", false
	}

	if expiresAtMS != nil && *expiresAtMS > 0 && time.Now().UnixMilli() > *expiresAtMS {
		_, _ = sm.ClearModelOverride(key)
		return "", false
	}

	return model, true
}

func (sm *SessionManager) SetModelOverride(key, model string, ttl time.Duration) (*time.Time, error) {
	key = utils.CanonicalSessionKey(key)
	model = strings.TrimSpace(model)
	if key == "" {
		return nil, fmt.Errorf("session key is empty")
	}
	if model == "" {
		return sm.ClearModelOverride(key)
	}

	now := time.Now()

	var expiresAt *time.Time
	var expiresAtMS *int64
	if ttl > 0 {
		ts := now.Add(ttl)
		expiresAt = &ts
		ms := ts.UnixMilli()
		expiresAtMS = &ms
	}

	sm.mu.Lock()
	sess, ok := sm.sessions[key]
	if !ok || sess == nil {
		sess = &Session{
			Key:      key,
			Messages: []providers.Message{},
			Created:  now,
		}
		sm.sessions[key] = sess
	}

	sess.ModelOverride = model
	sess.ModelOverrideExpiresAtMS = expiresAtMS
	sess.Updated = now

	var metaPath string
	if sm.storage != "" {
		metaPath = sm.metaPath(key)
	}
	meta := buildSessionMeta(sess)
	sm.mu.Unlock()

	if strings.TrimSpace(metaPath) != "" {
		_ = writeMetaFile(metaPath, meta)
	}

	return expiresAt, nil
}

func (sm *SessionManager) ClearModelOverride(key string) (*time.Time, error) {
	key = utils.CanonicalSessionKey(key)
	if key == "" {
		return nil, fmt.Errorf("session key is empty")
	}

	now := time.Now()

	sm.mu.Lock()
	sess, ok := sm.sessions[key]
	if !ok || sess == nil {
		sm.mu.Unlock()
		return nil, nil
	}

	sess.ModelOverride = ""
	sess.ModelOverrideExpiresAtMS = nil
	sess.Updated = now

	var metaPath string
	if sm.storage != "" {
		metaPath = sm.metaPath(key)
	}
	meta := buildSessionMeta(sess)
	sm.mu.Unlock()

	if strings.TrimSpace(metaPath) != "" {
		_ = writeMetaFile(metaPath, meta)
	}

	return nil, nil
}
