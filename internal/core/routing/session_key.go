package routing

import (
	"fmt"
	"strings"
)

// DMScope controls DM session isolation granularity.
type DMScope string

const (
	DMScopeMain                  DMScope = "main"
	DMScopePerPeer               DMScope = "per-peer"
	DMScopePerChannelPeer        DMScope = "per-channel-peer"
	DMScopePerAccountChannelPeer DMScope = "per-account-channel-peer"
)

// RoutePeer represents a chat peer with kind and ID.
type RoutePeer struct {
	Kind string // "direct", "group", "channel"
	ID   string
}

// SessionKeyParams holds all inputs for session key construction.
type SessionKeyParams struct {
	AgentID       string
	Channel       string
	AccountID     string
	Peer          *RoutePeer
	ThreadID      string // optional: thread/topic identifier (Telegram forum topic, Slack thread, etc)
	DMScope       DMScope
	IdentityLinks map[string][]string
}

// ParsedSessionKey is the result of parsing an agent-scoped session key.
type ParsedSessionKey struct {
	AgentID string
	Rest    string
}

// BuildAgentMainSessionKey returns "agent:<agentId>:main".
func BuildAgentMainSessionKey(agentID string) string {
	return fmt.Sprintf("agent:%s:%s", NormalizeAgentID(agentID), DefaultMainKey)
}

// BuildConversationMainSessionKey returns "conv:main".
//
// Conversation-scoped session keys intentionally do NOT embed an agent identifier.
// This enables Swarm-style agent handoffs while keeping a single shared conversation
// history across agents.
func BuildConversationMainSessionKey() string {
	return fmt.Sprintf("conv:%s", DefaultMainKey)
}

// BuildAgentPeerSessionKey constructs a session key based on agent, channel, peer, and DM scope.
func BuildAgentPeerSessionKey(params SessionKeyParams) string {
	agentID := NormalizeAgentID(params.AgentID)

	peer := params.Peer
	if peer == nil {
		peer = &RoutePeer{Kind: "direct"}
	}
	peerKind := strings.TrimSpace(peer.Kind)
	if peerKind == "" {
		peerKind = "direct"
	}

	if peerKind == "direct" {
		dmScope := params.DMScope
		if dmScope == "" {
			dmScope = DMScopeMain
		}
		peerID := strings.TrimSpace(peer.ID)

		// Resolve identity links (cross-platform collapse)
		if dmScope != DMScopeMain && peerID != "" {
			if linked := resolveLinkedPeerID(params.IdentityLinks, params.Channel, peerID); linked != "" {
				peerID = linked
			}
		}
		peerID = strings.ToLower(peerID)

		switch dmScope {
		case DMScopePerAccountChannelPeer:
			if peerID != "" {
				channel := normalizeChannel(params.Channel)
				accountID := NormalizeAccountID(params.AccountID)
				return fmt.Sprintf("agent:%s:%s:%s:direct:%s", agentID, channel, accountID, peerID)
			}
		case DMScopePerChannelPeer:
			if peerID != "" {
				channel := normalizeChannel(params.Channel)
				return fmt.Sprintf("agent:%s:%s:direct:%s", agentID, channel, peerID)
			}
		case DMScopePerPeer:
			if peerID != "" {
				return fmt.Sprintf("agent:%s:direct:%s", agentID, peerID)
			}
		}
		return BuildAgentMainSessionKey(agentID)
	}

	// Group/channel peers always get per-peer sessions
	channel := normalizeChannel(params.Channel)
	peerID := strings.ToLower(strings.TrimSpace(peer.ID))
	if peerID == "" {
		peerID = "unknown"
	}
	key := fmt.Sprintf("agent:%s:%s:%s:%s", agentID, channel, peerKind, peerID)
	if threadID := strings.ToLower(strings.TrimSpace(params.ThreadID)); threadID != "" {
		key += ":thread:" + threadID
	}
	return key
}

// BuildConversationPeerSessionKey constructs a session key based on channel, peer, and DM scope,
// without embedding any agent identifier. This enables Swarm-style agent handoffs while keeping
// a single shared conversation history across agents.
//
// Key format (examples):
// - Direct + dm_scope=main:         "conv:main"
// - Direct + per-peer:             "conv:direct:<peer>"
// - Direct + per-channel-peer:     "conv:<channel>:direct:<peer>"
// - Direct + per-account-channel:  "conv:<channel>:<account>:direct:<peer>"
// - Group / channel peers:         "conv:<channel>:group:<id>" / "conv:<channel>:channel:<id>"
func BuildConversationPeerSessionKey(params SessionKeyParams) string {
	peer := params.Peer
	if peer == nil {
		peer = &RoutePeer{Kind: "direct"}
	}
	peerKind := strings.TrimSpace(peer.Kind)
	if peerKind == "" {
		peerKind = "direct"
	}

	if peerKind == "direct" {
		dmScope := params.DMScope
		if dmScope == "" {
			dmScope = DMScopeMain
		}
		peerID := strings.TrimSpace(peer.ID)

		// Resolve identity links (cross-platform collapse)
		if dmScope != DMScopeMain && peerID != "" {
			if linked := resolveLinkedPeerID(params.IdentityLinks, params.Channel, peerID); linked != "" {
				peerID = linked
			}
		}
		peerID = strings.ToLower(peerID)

		switch dmScope {
		case DMScopePerAccountChannelPeer:
			if peerID != "" {
				channel := normalizeChannel(params.Channel)
				accountID := NormalizeAccountID(params.AccountID)
				return fmt.Sprintf("conv:%s:%s:direct:%s", channel, accountID, peerID)
			}
		case DMScopePerChannelPeer:
			if peerID != "" {
				channel := normalizeChannel(params.Channel)
				return fmt.Sprintf("conv:%s:direct:%s", channel, peerID)
			}
		case DMScopePerPeer:
			if peerID != "" {
				return fmt.Sprintf("conv:direct:%s", peerID)
			}
		}

		return BuildConversationMainSessionKey()
	}

	// Group/channel peers always get per-peer sessions
	channel := normalizeChannel(params.Channel)
	peerID := strings.ToLower(strings.TrimSpace(peer.ID))
	if peerID == "" {
		peerID = "unknown"
	}
	key := fmt.Sprintf("conv:%s:%s:%s", channel, peerKind, peerID)
	if threadID := strings.ToLower(strings.TrimSpace(params.ThreadID)); threadID != "" {
		key += ":thread:" + threadID
	}
	return key
}

// ParseAgentSessionKey extracts agentId and rest from "agent:<agentId>:<rest>".
func ParseAgentSessionKey(sessionKey string) *ParsedSessionKey {
	raw := strings.ToLower(strings.TrimSpace(sessionKey))
	if raw == "" {
		return nil
	}
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) < 3 {
		return nil
	}
	if parts[0] != "agent" {
		return nil
	}
	agentID := strings.TrimSpace(parts[1])
	rest := parts[2]
	if agentID == "" || rest == "" {
		return nil
	}
	return &ParsedSessionKey{AgentID: agentID, Rest: rest}
}

func normalizeChannel(channel string) string {
	c := strings.TrimSpace(strings.ToLower(channel))
	if c == "" {
		return "unknown"
	}
	return c
}

func resolveLinkedPeerID(identityLinks map[string][]string, channel, peerID string) string {
	if len(identityLinks) == 0 {
		return ""
	}
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return ""
	}

	candidates := make(map[string]bool)
	rawCandidate := strings.ToLower(peerID)
	if rawCandidate != "" {
		candidates[rawCandidate] = true
	}
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel != "" {
		scopedCandidate := fmt.Sprintf("%s:%s", channel, strings.ToLower(peerID))
		candidates[scopedCandidate] = true
	}

	// If peerID is already in canonical "platform:id" format, also add the
	// bare ID part as a candidate for backward compatibility with identity_links
	// that use raw IDs (e.g. "123" instead of "telegram:123").
	if idx := strings.Index(rawCandidate, ":"); idx > 0 && idx < len(rawCandidate)-1 {
		bareID := rawCandidate[idx+1:]
		candidates[bareID] = true
	}

	if len(candidates) == 0 {
		return ""
	}

	for canonical, ids := range identityLinks {
		canonicalName := strings.TrimSpace(canonical)
		if canonicalName == "" {
			continue
		}
		for _, id := range ids {
			normalized := strings.ToLower(strings.TrimSpace(id))
			if normalized != "" && candidates[normalized] {
				return canonicalName
			}
		}
	}
	return ""
}
