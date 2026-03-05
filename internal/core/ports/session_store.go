package ports

import (
	"time"

	coreprovider "github.com/xwysyy/X-Claw/internal/core/provider/protocoltypes"
	coresession "github.com/xwysyy/X-Claw/internal/core/session"
)

// SessionStore is the minimal durable conversation state surface the agent loop,
// tools, and HTTP APIs need.
//
// It is intentionally an interface (port) so core logic does not depend on a
// specific persistence backend (JSONL, SQLite, remote store, etc).
type SessionStore interface {
	GetOrCreate(key string) *coresession.Session

	AddMessage(sessionKey, role, content string)
	AddFullMessage(sessionKey string, msg coreprovider.Message)

	GetHistory(key string) []coreprovider.Message
	SetHistory(key string, history []coreprovider.Message)

	GetSummary(key string) string
	SetSummary(key string, summary string)

	GetActiveAgentID(key string) string
	SetActiveAgentID(key, agentID string)

	Save(key string) error

	TruncateHistory(key string, keepLast int)

	IncrementCompactionCount(key string) int
	MarkMemoryFlush(key string, compactionCount int)
	GetCompactionState(key string) (count int, flushCount int, flushAt time.Time)

	EffectiveModelOverride(key string) (string, bool)
	SetModelOverride(key, model string, ttl time.Duration) (*time.Time, error)
	ClearModelOverride(key string) (*time.Time, error)

	LeafEventID(key string) string
	GetTree(key string, limit int) (*coresession.SessionTree, error)
	SwitchLeaf(key, leafID string) (fromLeaf string, toLeaf string, err error)

	GetSessionSnapshot(key string) (*coresession.Session, bool)
	ListSessionSnapshots() []coresession.Session
}
