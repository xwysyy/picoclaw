package agent

import (
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/xwysyy/X-Claw/pkg/utils"
)

type memoryScopeKind string

const (
	memoryScopeAgent   memoryScopeKind = "agent"
	memoryScopeUser    memoryScopeKind = "user"
	memoryScopeSession memoryScopeKind = "session"
)

type memoryScope struct {
	Kind  memoryScopeKind
	RawID string
}

func deriveMemoryScope(sessionKey, channel, chatID string) memoryScope {
	raw := utils.CanonicalSessionKey(sessionKey)
	lower := raw

	if lower == "" {
		return memoryScope{Kind: memoryScopeAgent, RawID: "agent"}
	}

	if idx := strings.Index(lower, ":direct:"); idx >= 0 {
		peer := strings.TrimSpace(raw[idx+len(":direct:"):])
		if peer == "" {
			peer = strings.TrimSpace(chatID)
		}
		if peer == "" {
			peer = raw
		}
		return memoryScope{Kind: memoryScopeUser, RawID: peer}
	}

	if idx := strings.Index(lower, ":group:"); idx >= 0 {
		peer := strings.TrimSpace(raw[idx+len(":group:"):])
		if peer == "" {
			peer = strings.TrimSpace(chatID)
		}
		ch := strings.TrimSpace(channel)
		if ch == "" {
			ch = extractChannelFromSessionKey(raw)
		}
		rawID := strings.Trim(strings.TrimSpace(ch)+":group:"+strings.TrimSpace(peer), ":")
		if rawID == "" {
			rawID = raw
		}
		return memoryScope{Kind: memoryScopeSession, RawID: rawID}
	}

	if idx := strings.Index(lower, ":channel:"); idx >= 0 {
		peer := strings.TrimSpace(raw[idx+len(":channel:"):])
		if peer == "" {
			peer = strings.TrimSpace(chatID)
		}
		ch := strings.TrimSpace(channel)
		if ch == "" {
			ch = extractChannelFromSessionKey(raw)
		}
		rawID := strings.Trim(strings.TrimSpace(ch)+":channel:"+strings.TrimSpace(peer), ":")
		if rawID == "" {
			rawID = raw
		}
		return memoryScope{Kind: memoryScopeSession, RawID: rawID}
	}

	// Default to agent-scoped memory for main/cron/heartbeat and similar runtime tasks.
	return memoryScope{Kind: memoryScopeAgent, RawID: "agent"}
}

func extractChannelFromSessionKey(sessionKey string) string {
	lower := utils.CanonicalSessionKey(sessionKey)
	if !strings.HasPrefix(lower, "agent:") {
		return ""
	}

	parts := strings.Split(lower, ":")
	if len(parts) < 4 {
		return ""
	}

	candidate := strings.TrimSpace(parts[2])
	if candidate == "" {
		return ""
	}

	kind := strings.TrimSpace(parts[3])
	switch kind {
	case "direct", "group", "channel":
		return candidate
	}

	// Per-account session keys: agent:<id>:<channel>:<account>:direct:<peer>
	if len(parts) >= 5 && strings.TrimSpace(parts[4]) == "direct" {
		return candidate
	}

	return ""
}

var memoryScopeTokenRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func memoryScopeToken(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "unknown"
	}

	sanitized := memoryScopeTokenRe.ReplaceAllString(raw, "_")
	sanitized = strings.Trim(sanitized, "._-")
	if sanitized == "" {
		sanitized = "unknown"
	}

	sum := sha1.Sum([]byte(raw))
	hash := hex.EncodeToString(sum[:])[:8]

	// Keep directory tokens reasonably short to avoid path length issues, but
	// always append a hash to prevent collisions from truncation/sanitization.
	const maxLen = 80
	maxBase := maxLen - 1 - len(hash) // "_" + hash
	if maxBase < 1 {
		maxBase = 1
	}
	if len(sanitized) > maxBase {
		sanitized = sanitized[:maxBase]
		sanitized = strings.TrimRight(sanitized, "._-")
		if sanitized == "" {
			sanitized = "unknown"
		}
	}

	return sanitized + "_" + hash
}
