package config

import (
	"encoding/json"
	"time"
)

type AgentsConfig struct {
	Defaults AgentDefaults `json:"defaults"`
	List     []AgentConfig `json:"list,omitempty"`
}

// AgentModelConfig supports both string and structured model config.
// String format: "gpt-4" (just primary, no fallbacks)
// Object format: {"primary": "gpt-4", "fallbacks": ["claude-haiku"]}
type AgentModelConfig struct {
	Primary   string   `json:"primary,omitempty"`
	Fallbacks []string `json:"fallbacks,omitempty"`
}

func (m *AgentModelConfig) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		m.Primary = s
		m.Fallbacks = nil
		return nil
	}
	type raw struct {
		Primary   string   `json:"primary"`
		Fallbacks []string `json:"fallbacks"`
	}
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	m.Primary = r.Primary
	m.Fallbacks = r.Fallbacks
	return nil
}

func (m AgentModelConfig) MarshalJSON() ([]byte, error) {
	if len(m.Fallbacks) == 0 && m.Primary != "" {
		return json.Marshal(m.Primary)
	}
	type raw struct {
		Primary   string   `json:"primary,omitempty"`
		Fallbacks []string `json:"fallbacks,omitempty"`
	}
	return json.Marshal(raw{Primary: m.Primary, Fallbacks: m.Fallbacks})
}

type AgentConfig struct {
	ID        string            `json:"id"`
	Default   bool              `json:"default,omitempty"`
	Name      string            `json:"name,omitempty"`
	Workspace string            `json:"workspace,omitempty"`
	Model     *AgentModelConfig `json:"model,omitempty"`
	Skills    []string          `json:"skills,omitempty"`
}

// SessionModelAutoDowngradeConfig controls automatic session model override updates.
//
// ROADMAP_V2 Phase J2:
//   - When the fallback chain repeatedly fails over from the primary model,
//     automatically set a per-session model override (TTL) to the successful fallback.
//   - This reduces repeated timeouts / errors on every run while keeping behavior auditable.
//
// Default: disabled.
type SessionModelAutoDowngradeConfig struct {
	Enabled bool `json:"enabled,omitempty"`

	// Threshold is the number of consecutive triggers required before switching.
	// 0 uses a small built-in default (2).
	Threshold int `json:"threshold,omitempty"`

	// WindowMinutes defines how long strikes remain valid for the "consecutive" check.
	// 0 uses a built-in default (15 minutes).
	WindowMinutes int `json:"window_minutes,omitempty"`

	// TTLMinutes is the TTL applied to the session model override when switching.
	// 0 uses a built-in default (60 minutes).
	TTLMinutes int `json:"ttl_minutes,omitempty"`
}

type PeerMatch struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type BindingMatch struct {
	Channel   string     `json:"channel"`
	AccountID string     `json:"account_id,omitempty"`
	Peer      *PeerMatch `json:"peer,omitempty"`
	// ThreadID optionally matches a thread/topic identifier within the peer.
	// Examples:
	// - Telegram forum topic: message_thread_id
	// - Slack thread: thread_ts (if mapped into a stable thread identifier)
	ThreadID string `json:"thread_id,omitempty"`
	GuildID  string `json:"guild_id,omitempty"`
	TeamID   string `json:"team_id,omitempty"`
}

type AgentBinding struct {
	AgentID string       `json:"agent_id"`
	Match   BindingMatch `json:"match"`
}

type SessionConfig struct {
	DMScope       string              `json:"dm_scope,omitempty"`
	IdentityLinks map[string][]string `json:"identity_links,omitempty"`
	MaxSessions   int                 `json:"max_sessions,omitempty"`
	TTLHours      int                 `json:"ttl_hours,omitempty"`
}

const (
	DefaultSessionMaxSessions = 1000
	DefaultSessionTTLHours    = 168
)

func (c SessionConfig) EffectiveMaxSessions() int {
	if c.MaxSessions <= 0 {
		return DefaultSessionMaxSessions
	}
	return c.MaxSessions
}

func (c SessionConfig) EffectiveTTL() time.Duration {
	hours := c.TTLHours
	if hours <= 0 {
		hours = DefaultSessionTTLHours
	}
	return time.Duration(hours) * time.Hour
}

type AgentDefaults struct {
	Workspace                 string                          `json:"workspace"                       env:"X_CLAW_AGENTS_DEFAULTS_WORKSPACE"`
	RestrictToWorkspace       bool                            `json:"restrict_to_workspace"           env:"X_CLAW_AGENTS_DEFAULTS_RESTRICT_TO_WORKSPACE"`
	AllowReadOutsideWorkspace bool                            `json:"allow_read_outside_workspace"    env:"X_CLAW_AGENTS_DEFAULTS_ALLOW_READ_OUTSIDE_WORKSPACE"`
	Provider                  string                          `json:"provider"                        env:"X_CLAW_AGENTS_DEFAULTS_PROVIDER"`
	ModelName                 string                          `json:"model_name,omitempty"            env:"X_CLAW_AGENTS_DEFAULTS_MODEL_NAME"`
	Model                     string                          `json:"model"                           env:"X_CLAW_AGENTS_DEFAULTS_MODEL"` // Deprecated: use model_name instead
	ModelFallbacks            []string                        `json:"model_fallbacks,omitempty"`
	SessionModelAutoDowngrade SessionModelAutoDowngradeConfig `json:"session_model_auto_downgrade,omitempty"`
	ImageModel                string                          `json:"image_model,omitempty"           env:"X_CLAW_AGENTS_DEFAULTS_IMAGE_MODEL"`
	ImageModelFallbacks       []string                        `json:"image_model_fallbacks,omitempty"`
	MaxTokens                 int                             `json:"max_tokens"                      env:"X_CLAW_AGENTS_DEFAULTS_MAX_TOKENS"`
	Temperature               *float64                        `json:"temperature,omitempty"           env:"X_CLAW_AGENTS_DEFAULTS_TEMPERATURE"`
	MaxToolIterations         int                             `json:"max_tool_iterations"             env:"X_CLAW_AGENTS_DEFAULTS_MAX_TOOL_ITERATIONS"`
	// Legacy summarization controls (still supported for compatibility).
	// Prefer compaction/context_pruning for more predictable behavior.
	SummarizeMessageThreshold int                          `json:"summarize_message_threshold,omitempty" env:"X_CLAW_AGENTS_DEFAULTS_SUMMARIZE_MESSAGE_THRESHOLD"`
	SummarizeTokenPercent     int                          `json:"summarize_token_percent,omitempty"     env:"X_CLAW_AGENTS_DEFAULTS_SUMMARIZE_TOKEN_PERCENT"`
	MaxMediaSize              int                          `json:"max_media_size,omitempty"        env:"X_CLAW_AGENTS_DEFAULTS_MAX_MEDIA_SIZE"`
	Compaction                AgentCompactionConfig        `json:"compaction,omitempty"`
	ContextPruning            AgentContextPruningConfig    `json:"context_pruning,omitempty"`
	BootstrapSnapshot         AgentBootstrapSnapshotConfig `json:"bootstrap_snapshot,omitempty"`
	MemoryVector              AgentMemoryVectorConfig      `json:"memory_vector,omitempty"`
}

const DefaultMaxMediaSize = 20 * 1024 * 1024 // 20 MB

func (d *AgentDefaults) GetMaxMediaSize() int {
	if d.MaxMediaSize > 0 {
		return d.MaxMediaSize
	}
	return DefaultMaxMediaSize
}

type AgentCompactionConfig struct {
	Mode             string                           `json:"mode,omitempty"`
	ReserveTokens    int                              `json:"reserve_tokens,omitempty"`
	KeepRecentTokens int                              `json:"keep_recent_tokens,omitempty"`
	MaxHistoryShare  float64                          `json:"max_history_share,omitempty"`
	MemoryFlush      AgentCompactionMemoryFlushConfig `json:"memory_flush,omitempty"`

	// NotifyUser controls whether the agent publishes a chat message when background
	// compaction/summarization kicks in. Default: false (quiet-by-default).
	NotifyUser bool `json:"notify_user,omitempty"`
}

type AgentCompactionMemoryFlushConfig struct {
	Enabled             bool `json:"enabled,omitempty"`
	SoftThresholdTokens int  `json:"soft_threshold_tokens,omitempty"`
}

type AgentContextPruningConfig struct {
	Mode                string  `json:"mode,omitempty"`
	IncludeOldChitChat  bool    `json:"include_old_chitchat,omitempty"`
	SoftToolResultChars int     `json:"soft_tool_result_chars,omitempty"`
	HardToolResultChars int     `json:"hard_tool_result_chars,omitempty"`
	TriggerRatio        float64 `json:"trigger_ratio,omitempty"`
}

type AgentBootstrapSnapshotConfig struct {
	Enabled bool `json:"enabled,omitempty"`
}

// EmbeddingConfig describes an OpenAI-compatible embeddings endpoint.
//
// This config is currently used by semantic memory, but may be reused by future RAG features.
// NOTE: api_key is considered sensitive and will be redacted in export bundles.
type EmbeddingConfig struct {
	// Kind selects the embedding backend:
	// - "" / "hashed": local deterministic embedding (default)
	// - "openai_compat": OpenAI-compatible /v1/embeddings endpoint
	Kind string `json:"kind,omitempty"`

	APIKey  SecretRef `json:"api_key,omitempty"`
	APIBase string    `json:"api_base,omitempty"`
	Model   string    `json:"model,omitempty"`
	Proxy   string    `json:"proxy,omitempty"`

	BatchSize             int `json:"batch_size,omitempty"`
	RequestTimeoutSeconds int `json:"request_timeout_seconds,omitempty"`
}

type AgentMemoryVectorConfig struct {
	Enabled         bool    `json:"enabled,omitempty"           env:"X_CLAW_AGENTS_DEFAULTS_MEMORY_VECTOR_ENABLED"`
	Dimensions      int     `json:"dimensions,omitempty"        env:"X_CLAW_AGENTS_DEFAULTS_MEMORY_VECTOR_DIMENSIONS"`
	TopK            int     `json:"top_k,omitempty"             env:"X_CLAW_AGENTS_DEFAULTS_MEMORY_VECTOR_TOP_K"`
	MinScore        float64 `json:"min_score,omitempty"         env:"X_CLAW_AGENTS_DEFAULTS_MEMORY_VECTOR_MIN_SCORE"`
	MaxContextChars int     `json:"max_context_chars,omitempty" env:"X_CLAW_AGENTS_DEFAULTS_MEMORY_VECTOR_MAX_CONTEXT_CHARS"`
	RecentDailyDays int     `json:"recent_daily_days,omitempty" env:"X_CLAW_AGENTS_DEFAULTS_MEMORY_VECTOR_RECENT_DAILY_DAYS"`

	// Embedding controls how semantic vectors are generated.
	// When omitted, X-Claw uses a fast local hashing embedder (no network).
	Embedding EmbeddingConfig `json:"embedding,omitempty"`

	// Hybrid controls how FTS vs vector signals are blended when both are available.
	Hybrid AgentMemoryHybridConfig `json:"hybrid,omitempty"`
}

type AgentMemoryHybridConfig struct {
	FTSWeight    float64 `json:"fts_weight,omitempty"`
	VectorWeight float64 `json:"vector_weight,omitempty"`
}
