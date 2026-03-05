package session

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/pkg/utils"

	coresession "github.com/xwysyy/X-Claw/internal/core/session"
	"github.com/xwysyy/X-Claw/pkg/providers"
)

// Type aliases keep existing imports stable while moving canonical session domain
// types into internal/core.
type (
	TreeNode    = coresession.TreeNode
	SessionTree = coresession.SessionTree
)

func (sm *SessionManager) LeafEventID(key string) string {
	key = utils.CanonicalSessionKey(key)

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	sess, ok := sm.sessions[key]
	if !ok || sess == nil {
		return ""
	}
	return strings.TrimSpace(sess.LastEventID)
}

func (sm *SessionManager) GetTree(key string, limit int) (*SessionTree, error) {
	key = utils.CanonicalSessionKey(key)
	if key == "" {
		return nil, fmt.Errorf("session key is empty")
	}

	leaf := sm.LeafEventID(key)
	path := sm.eventsPath(key)
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("session storage is not configured")
	}

	events, err := readJSONLEvents(path)
	if err != nil {
		return nil, err
	}

	byID, orderedIDs, tailID := indexEvents(events)
	effectiveLeaf := resolveLeafID(leaf, byID, tailID)

	onBranch := make(map[string]bool)
	if effectiveLeaf != "" {
		for _, id := range collectPathIDs(byID, effectiveLeaf) {
			onBranch[id] = true
		}
	}

	maxLimit := 200
	if limit <= 0 {
		limit = 30
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	nodes := make([]TreeNode, 0, min(limit, len(orderedIDs)))
	start := 0
	if len(orderedIDs) > limit {
		start = len(orderedIDs) - limit
	}
	for _, id := range orderedIDs[start:] {
		ev, ok := byID[id]
		if !ok {
			continue
		}

		role := ""
		preview := ""
		if ev.Message != nil {
			role = strings.TrimSpace(ev.Message.Role)
			preview = sanitizePreview(utils.Truncate(strings.TrimSpace(ev.Message.Content), 160))
		} else if strings.TrimSpace(ev.Summary) != "" {
			preview = sanitizePreview(utils.Truncate(strings.TrimSpace(ev.Summary), 160))
		} else if strings.TrimSpace(ev.ActiveAgentID) != "" {
			preview = strings.TrimSpace(ev.ActiveAgentID)
		} else if ev.KeepLast != 0 {
			preview = "keep_last=" + strconv.Itoa(ev.KeepLast)
		}

		nodes = append(nodes, TreeNode{
			ID:       strings.TrimSpace(ev.ID),
			ParentID: strings.TrimSpace(ev.ParentID),
			Type:     ev.Type,
			TS:       strings.TrimSpace(ev.TS),
			TSMS:     ev.TSMS,
			Role:     role,
			Preview:  preview,
			OnBranch: onBranch[id],
			IsLeaf:   id == effectiveLeaf,
		})
	}

	return &SessionTree{
		SessionKey: key,
		LeafID:     effectiveLeaf,
		Total:      len(orderedIDs),
		Nodes:      nodes,
	}, nil
}

func (sm *SessionManager) SwitchLeaf(key, leafID string) (fromLeaf string, toLeaf string, err error) {
	key = utils.CanonicalSessionKey(key)
	leafID = strings.TrimSpace(leafID)
	if key == "" {
		return "", "", fmt.Errorf("session key is empty")
	}
	if leafID == "" {
		return "", "", fmt.Errorf("leaf_id is empty")
	}

	eventsPath := sm.eventsPath(key)
	if strings.TrimSpace(eventsPath) == "" {
		return "", "", fmt.Errorf("session storage is not configured")
	}

	events, err := readJSONLEvents(eventsPath)
	if err != nil {
		return "", "", err
	}

	byID, _, _ := indexEvents(events)
	if _, ok := byID[leafID]; !ok {
		return "", "", fmt.Errorf("event id %q not found in session", leafID)
	}

	replayed, effectiveLeaf := replayEvents(events, leafID)
	if strings.TrimSpace(effectiveLeaf) == "" {
		return "", "", fmt.Errorf("unable to resolve leaf event")
	}

	now := time.Now()

	sm.mu.Lock()
	defer sm.mu.Unlock()

	sess, ok := sm.sessions[key]
	if !ok || sess == nil {
		return "", "", fmt.Errorf("session %q not found", key)
	}

	oldLeaf := strings.TrimSpace(sess.LastEventID)

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
	sess.Updated = now

	if sm.storage != "" {
		if path := sm.metaPath(key); path != "" {
			_ = writeMetaFile(path, buildSessionMeta(sess))
		}
	}

	return oldLeaf, effectiveLeaf, nil
}

func sanitizePreview(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

func indexEvents(events []SessionEvent) (byID map[string]SessionEvent, orderedIDs []string, tailID string) {
	byID = make(map[string]SessionEvent, len(events))
	orderedIDs = make([]string, 0, len(events))
	for _, ev := range events {
		id := strings.TrimSpace(ev.ID)
		if id == "" {
			continue
		}
		byID[id] = ev
		orderedIDs = append(orderedIDs, id)
		tailID = id
	}
	return byID, orderedIDs, tailID
}

func resolveLeafID(preferred string, byID map[string]SessionEvent, fallback string) string {
	preferred = strings.TrimSpace(preferred)
	if preferred != "" {
		if _, ok := byID[preferred]; ok {
			return preferred
		}
	}
	fallback = strings.TrimSpace(fallback)
	if fallback != "" {
		if _, ok := byID[fallback]; ok {
			return fallback
		}
	}
	return ""
}

func collectPathIDs(byID map[string]SessionEvent, leafID string) []string {
	leafID = strings.TrimSpace(leafID)
	if leafID == "" {
		return nil
	}

	visited := make(map[string]bool)
	out := make([]string, 0, 32)
	cur := leafID
	for cur != "" {
		cur = strings.TrimSpace(cur)
		if cur == "" || visited[cur] {
			break
		}
		visited[cur] = true
		out = append(out, cur)
		ev, ok := byID[cur]
		if !ok {
			break
		}
		cur = strings.TrimSpace(ev.ParentID)
	}
	return out
}

func replayEvents(events []SessionEvent, leafID string) (Session, string) {
	byID, _, tailID := indexEvents(events)
	effectiveLeaf := resolveLeafID(leafID, byID, tailID)
	if effectiveLeaf == "" {
		return Session{Messages: []providers.Message{}}, ""
	}

	pathIDs := collectPathIDs(byID, effectiveLeaf)
	if len(pathIDs) == 0 {
		return Session{Messages: []providers.Message{}}, ""
	}

	replayed := Session{
		Messages: []providers.Message{},
	}

	var minTSMS, maxTSMS int64
	for i := len(pathIDs) - 1; i >= 0; i-- {
		id := pathIDs[i]
		ev, ok := byID[id]
		if !ok {
			continue
		}

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
				replayed.Messages = append(replayed.Messages, *ev.Message)
			}
		case EventSessionSummary:
			replayed.Summary = ev.Summary
		case EventSessionActiveAgent:
			if strings.TrimSpace(ev.ActiveAgentID) != "" {
				replayed.ActiveAgentID = strings.TrimSpace(ev.ActiveAgentID)
			}
		case EventSessionHistorySet:
			if ev.History != nil {
				msgs := make([]providers.Message, len(ev.History))
				copy(msgs, ev.History)
				replayed.Messages = msgs
			}
		case EventSessionHistoryTrunc:
			if ev.KeepLast <= 0 {
				replayed.Messages = []providers.Message{}
			} else if len(replayed.Messages) > ev.KeepLast {
				replayed.Messages = replayed.Messages[len(replayed.Messages)-ev.KeepLast:]
			}
		case EventSessionCompactionInc:
			if ev.CompactionCount > replayed.CompactionCount {
				replayed.CompactionCount = ev.CompactionCount
			}
		case EventSessionMemoryFlush:
			if !ev.MemoryFlushAt.IsZero() {
				replayed.MemoryFlushAt = ev.MemoryFlushAt
			}
			if ev.MemoryFlushCompactionCount != 0 {
				replayed.MemoryFlushCompactionCount = ev.MemoryFlushCompactionCount
			}
		default:
		}

		if strings.TrimSpace(ev.ID) != "" {
			replayed.LastEventID = strings.TrimSpace(ev.ID)
		}
	}

	if minTSMS > 0 {
		replayed.Created = time.UnixMilli(minTSMS)
	}
	if maxTSMS > 0 {
		replayed.Updated = time.UnixMilli(maxTSMS)
	}

	return replayed, effectiveLeaf
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
