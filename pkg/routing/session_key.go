package routing

import corerouting "github.com/sipeed/picoclaw/internal/core/routing"

// Type aliases keep existing imports stable while moving the canonical
// definitions into internal/core.
type (
	DMScope          = corerouting.DMScope
	RoutePeer        = corerouting.RoutePeer
	SessionKeyParams = corerouting.SessionKeyParams
	ParsedSessionKey = corerouting.ParsedSessionKey
)

const (
	DMScopeMain                  DMScope = corerouting.DMScopeMain
	DMScopePerPeer               DMScope = corerouting.DMScopePerPeer
	DMScopePerChannelPeer        DMScope = corerouting.DMScopePerChannelPeer
	DMScopePerAccountChannelPeer DMScope = corerouting.DMScopePerAccountChannelPeer
)

func BuildAgentMainSessionKey(agentID string) string {
	return corerouting.BuildAgentMainSessionKey(agentID)
}

func BuildConversationMainSessionKey() string {
	return corerouting.BuildConversationMainSessionKey()
}

func BuildAgentPeerSessionKey(params SessionKeyParams) string {
	return corerouting.BuildAgentPeerSessionKey(params)
}

func BuildConversationPeerSessionKey(params SessionKeyParams) string {
	return corerouting.BuildConversationPeerSessionKey(params)
}

func ParseAgentSessionKey(sessionKey string) *ParsedSessionKey {
	return corerouting.ParseAgentSessionKey(sessionKey)
}

func IsSubagentSessionKey(sessionKey string) bool {
	return corerouting.IsSubagentSessionKey(sessionKey)
}
