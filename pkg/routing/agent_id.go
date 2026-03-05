package routing

import corerouting "github.com/xwysyy/X-Claw/internal/core/routing"

const (
	DefaultAgentID   = corerouting.DefaultAgentID
	DefaultMainKey   = corerouting.DefaultMainKey
	DefaultAccountID = corerouting.DefaultAccountID
	MaxAgentIDLength = corerouting.MaxAgentIDLength
)

// NormalizeAgentID is a facade for internal/core/routing.NormalizeAgentID.
func NormalizeAgentID(id string) string {
	return corerouting.NormalizeAgentID(id)
}

// NormalizeAccountID is a facade for internal/core/routing.NormalizeAccountID.
func NormalizeAccountID(id string) string {
	return corerouting.NormalizeAccountID(id)
}
