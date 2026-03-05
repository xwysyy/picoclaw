package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

// rrCounter is a global counter for round-robin load balancing across models.
var rrCounter atomic.Uint64

// FlexibleStringSlice is a []string that also accepts JSON numbers,
// so allow_from can contain both "123" and 123.
type FlexibleStringSlice []string

func (f *FlexibleStringSlice) UnmarshalJSON(data []byte) error {
	// Try []string first
	var ss []string
	if err := json.Unmarshal(data, &ss); err == nil {
		*f = ss
		return nil
	}

	// Try []interface{} to handle mixed types
	var raw []any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	result := make([]string, 0, len(raw))
	for _, v := range raw {
		switch val := v.(type) {
		case string:
			result = append(result, val)
		case float64:
			result = append(result, fmt.Sprintf("%.0f", val))
		default:
			result = append(result, fmt.Sprintf("%v", val))
		}
	}
	*f = result
	return nil
}

type Config struct {
	Agents        AgentsConfig        `json:"agents"`
	Bindings      []AgentBinding      `json:"bindings,omitempty"`
	Session       SessionConfig       `json:"session,omitempty"`
	Channels      ChannelsConfig      `json:"channels"`
	Providers     ProvidersConfig     `json:"providers,omitempty"`
	ModelList     []ModelConfig       `json:"model_list"` // New model-centric provider configuration
	Gateway       GatewayConfig       `json:"gateway"`
	Notify        NotifyConfig        `json:"notify,omitempty"`
	Tools         ToolsConfig         `json:"tools"`
	Heartbeat     HeartbeatConfig     `json:"heartbeat"`
	Orchestration OrchestrationConfig `json:"orchestration,omitempty"`
	Limits        LimitsConfig        `json:"limits,omitempty"`
	AuditLog      AuditLogConfig      `json:"audit_log,omitempty"`
	Audit         AuditConfig         `json:"audit,omitempty"`
	Security      SecurityConfig      `json:"security,omitempty"`

	// SourcePath is the config.json path used to load this config (best-effort).
	// It is not persisted when saving config.
	SourcePath string `json:"-"`
}

// NotifyConfig controls optional notification hooks.
//
// Phase PR-3 in ROADMAP.md (ROADMAP.md:1226): make "task completed" reminders
// a configurable workflow hook.
type NotifyConfig struct {
	// OnTaskComplete sends a short completion notification when a run finishes
	// in an internal channel (e.g., system/cli), using the message tool routed to
	// the last active external conversation.
	OnTaskComplete bool `json:"on_task_complete,omitempty"`
}

// MarshalJSON implements custom JSON marshaling for Config
// to omit providers section when empty and session when empty
func (c Config) MarshalJSON() ([]byte, error) {
	type Alias Config
	aux := &struct {
		Providers *ProvidersConfig `json:"providers,omitempty"`
		Session   *SessionConfig   `json:"session,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(&c),
	}

	// Only include providers if not empty
	if !c.Providers.IsEmpty() {
		aux.Providers = &c.Providers
	}

	// Only include session if not empty
	if c.Session.DMScope != "" || len(c.Session.IdentityLinks) > 0 {
		aux.Session = &c.Session
	}

	return json.Marshal(aux)
}

type SecurityConfig struct {
	// BreakGlass gates unsafe configuration states behind an explicit boolean.
	// This makes "unsafe but intentional" deployments auditable and prevents
	// accidental exposure from simple config edits.
	BreakGlass BreakGlassConfig `json:"break_glass,omitempty"`
}

type BreakGlassConfig struct {
	// AllowPublicGateway acknowledges that gateway.host binds to a non-loopback address
	// (e.g., 0.0.0.0 or a LAN IP). When false, such configs are rejected.
	AllowPublicGateway bool `json:"allow_public_gateway,omitempty" env:"PICOCLAW_SECURITY_BREAK_GLASS_ALLOW_PUBLIC_GATEWAY"`

	// AllowUnsafeWorkspace disables workspace-only filesystem restrictions. This is high risk:
	// tools can read/write arbitrary host paths if other guards are also loosened.
	AllowUnsafeWorkspace bool `json:"allow_unsafe_workspace,omitempty" env:"PICOCLAW_SECURITY_BREAK_GLASS_ALLOW_UNSAFE_WORKSPACE"`

	// AllowUnsafeExec acknowledges disabling deny patterns for the exec tool.
	AllowUnsafeExec bool `json:"allow_unsafe_exec,omitempty" env:"PICOCLAW_SECURITY_BREAK_GLASS_ALLOW_UNSAFE_EXEC"`

	// AllowExecInheritEnv acknowledges passing the full host environment into exec tool commands.
	// Prefer tools.exec.env.mode="allowlist" to avoid leaking host secrets into subprocesses.
	AllowExecInheritEnv bool `json:"allow_exec_inherit_env,omitempty" env:"PICOCLAW_SECURITY_BREAK_GLASS_ALLOW_EXEC_INHERIT_ENV"`

	// AllowDockerNetwork acknowledges enabling networking for the exec docker sandbox
	// (tools.exec.docker.network != "none").
	AllowDockerNetwork bool `json:"allow_docker_network,omitempty" env:"PICOCLAW_SECURITY_BREAK_GLASS_ALLOW_DOCKER_NETWORK"`
}

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
	Subagents *SubagentsConfig  `json:"subagents,omitempty"`
}

type SubagentsConfig struct {
	AllowAgents []string          `json:"allow_agents,omitempty"`
	Model       *AgentModelConfig `json:"model,omitempty"`
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
}

type AgentDefaults struct {
	Workspace                 string                          `json:"workspace"                       env:"PICOCLAW_AGENTS_DEFAULTS_WORKSPACE"`
	RestrictToWorkspace       bool                            `json:"restrict_to_workspace"           env:"PICOCLAW_AGENTS_DEFAULTS_RESTRICT_TO_WORKSPACE"`
	AllowReadOutsideWorkspace bool                            `json:"allow_read_outside_workspace"    env:"PICOCLAW_AGENTS_DEFAULTS_ALLOW_READ_OUTSIDE_WORKSPACE"`
	Provider                  string                          `json:"provider"                        env:"PICOCLAW_AGENTS_DEFAULTS_PROVIDER"`
	ModelName                 string                          `json:"model_name,omitempty"            env:"PICOCLAW_AGENTS_DEFAULTS_MODEL_NAME"`
	Model                     string                          `json:"model"                           env:"PICOCLAW_AGENTS_DEFAULTS_MODEL"` // Deprecated: use model_name instead
	ModelFallbacks            []string                        `json:"model_fallbacks,omitempty"`
	SessionModelAutoDowngrade SessionModelAutoDowngradeConfig `json:"session_model_auto_downgrade,omitempty"`
	ImageModel                string                          `json:"image_model,omitempty"           env:"PICOCLAW_AGENTS_DEFAULTS_IMAGE_MODEL"`
	ImageModelFallbacks       []string                        `json:"image_model_fallbacks,omitempty"`
	MaxTokens                 int                             `json:"max_tokens"                      env:"PICOCLAW_AGENTS_DEFAULTS_MAX_TOKENS"`
	Temperature               *float64                        `json:"temperature,omitempty"           env:"PICOCLAW_AGENTS_DEFAULTS_TEMPERATURE"`
	MaxToolIterations         int                             `json:"max_tool_iterations"             env:"PICOCLAW_AGENTS_DEFAULTS_MAX_TOOL_ITERATIONS"`
	MaxMediaSize              int                             `json:"max_media_size,omitempty"        env:"PICOCLAW_AGENTS_DEFAULTS_MAX_MEDIA_SIZE"`
	Compaction                AgentCompactionConfig           `json:"compaction,omitempty"`
	ContextPruning            AgentContextPruningConfig       `json:"context_pruning,omitempty"`
	BootstrapSnapshot         AgentBootstrapSnapshotConfig    `json:"bootstrap_snapshot,omitempty"`
	MemoryVector              AgentMemoryVectorConfig         `json:"memory_vector,omitempty"`
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
	Enabled         bool    `json:"enabled,omitempty"           env:"PICOCLAW_AGENTS_DEFAULTS_MEMORY_VECTOR_ENABLED"`
	Dimensions      int     `json:"dimensions,omitempty"        env:"PICOCLAW_AGENTS_DEFAULTS_MEMORY_VECTOR_DIMENSIONS"`
	TopK            int     `json:"top_k,omitempty"             env:"PICOCLAW_AGENTS_DEFAULTS_MEMORY_VECTOR_TOP_K"`
	MinScore        float64 `json:"min_score,omitempty"         env:"PICOCLAW_AGENTS_DEFAULTS_MEMORY_VECTOR_MIN_SCORE"`
	MaxContextChars int     `json:"max_context_chars,omitempty" env:"PICOCLAW_AGENTS_DEFAULTS_MEMORY_VECTOR_MAX_CONTEXT_CHARS"`
	RecentDailyDays int     `json:"recent_daily_days,omitempty" env:"PICOCLAW_AGENTS_DEFAULTS_MEMORY_VECTOR_RECENT_DAILY_DAYS"`

	// Embedding controls how semantic vectors are generated.
	// When omitted, PicoClaw uses a fast local hashing embedder (no network).
	Embedding EmbeddingConfig `json:"embedding,omitempty"`

	// Hybrid controls how FTS vs vector signals are blended when both are available.
	Hybrid AgentMemoryHybridConfig `json:"hybrid,omitempty"`
}

type AgentMemoryHybridConfig struct {
	FTSWeight    float64 `json:"fts_weight,omitempty"`
	VectorWeight float64 `json:"vector_weight,omitempty"`
}

// GetModelName returns the effective model name for the agent defaults.
// It prefers the new "model_name" field but falls back to "model" for backward compatibility.
func (d *AgentDefaults) GetModelName() string {
	if d.ModelName != "" {
		return d.ModelName
	}
	return d.Model
}

type ChannelsConfig struct {
	WhatsApp   WhatsAppConfig   `json:"whatsapp"`
	Telegram   TelegramConfig   `json:"telegram"`
	Feishu     FeishuConfig     `json:"feishu"`
	Discord    DiscordConfig    `json:"discord"`
	MaixCam    MaixCamConfig    `json:"maixcam"`
	QQ         QQConfig         `json:"qq"`
	DingTalk   DingTalkConfig   `json:"dingtalk"`
	Slack      SlackConfig      `json:"slack"`
	LINE       LINEConfig       `json:"line"`
	OneBot     OneBotConfig     `json:"onebot"`
	WeCom      WeComConfig      `json:"wecom"`
	WeComApp   WeComAppConfig   `json:"wecom_app"`
	WeComAIBot WeComAIBotConfig `json:"wecom_aibot"`
	Pico       PicoConfig       `json:"pico"`
}

// GroupTriggerConfig controls when the bot responds in group chats.
type GroupTriggerConfig struct {
	MentionOnly bool     `json:"mention_only,omitempty"`
	Prefixes    []string `json:"prefixes,omitempty"`

	// Mentionless allows responding in group chats without an explicit @mention or prefix.
	// Default: false (safe-by-default).
	Mentionless bool `json:"mentionless,omitempty"`

	// CommandBypass allows slash-commands (e.g., "/tree", "/switch ...") to bypass mention requirements.
	// This improves UX in groups while keeping normal chatter gated.
	CommandBypass bool `json:"command_bypass,omitempty"`

	// CommandPrefixes defines which prefixes count as commands for bypassing group triggers.
	// When empty and CommandBypass is true, defaults to ["/"].
	CommandPrefixes []string `json:"command_prefixes,omitempty"`
}

// TypingConfig controls typing indicator behavior (Phase 10).
type TypingConfig struct {
	Enabled bool `json:"enabled,omitempty"`
}

// PlaceholderConfig controls placeholder message behavior (Phase 10).
type PlaceholderConfig struct {
	Enabled bool   `json:"enabled,omitempty"`
	Text    string `json:"text,omitempty"`

	// DelayMS delays sending placeholder messages to avoid flicker for fast replies.
	// 0 means no delay. Recommended: 2500ms.
	DelayMS int `json:"delay_ms,omitempty"`
}

type WhatsAppConfig struct {
	Enabled            bool                `json:"enabled"              env:"PICOCLAW_CHANNELS_WHATSAPP_ENABLED"`
	BridgeURL          string              `json:"bridge_url"           env:"PICOCLAW_CHANNELS_WHATSAPP_BRIDGE_URL"`
	UseNative          bool                `json:"use_native"           env:"PICOCLAW_CHANNELS_WHATSAPP_USE_NATIVE"`
	SessionStorePath   string              `json:"session_store_path"   env:"PICOCLAW_CHANNELS_WHATSAPP_SESSION_STORE_PATH"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"           env:"PICOCLAW_CHANNELS_WHATSAPP_ALLOW_FROM"`
	ReasoningChannelID string              `json:"reasoning_channel_id" env:"PICOCLAW_CHANNELS_WHATSAPP_REASONING_CHANNEL_ID"`
}

type TelegramConfig struct {
	Enabled            bool                `json:"enabled"                 env:"PICOCLAW_CHANNELS_TELEGRAM_ENABLED"`
	Token              SecretRef           `json:"token"                   env:"PICOCLAW_CHANNELS_TELEGRAM_TOKEN"`
	BaseURL            string              `json:"base_url"                env:"PICOCLAW_CHANNELS_TELEGRAM_BASE_URL"`
	Proxy              string              `json:"proxy"                   env:"PICOCLAW_CHANNELS_TELEGRAM_PROXY"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"PICOCLAW_CHANNELS_TELEGRAM_ALLOW_FROM"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	Typing             TypingConfig        `json:"typing,omitempty"`
	Placeholder        PlaceholderConfig   `json:"placeholder,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"PICOCLAW_CHANNELS_TELEGRAM_REASONING_CHANNEL_ID"`
}

type FeishuConfig struct {
	Enabled            bool                `json:"enabled"                 env:"PICOCLAW_CHANNELS_FEISHU_ENABLED"`
	AppID              string              `json:"app_id"                  env:"PICOCLAW_CHANNELS_FEISHU_APP_ID"`
	AppSecret          SecretRef           `json:"app_secret"              env:"PICOCLAW_CHANNELS_FEISHU_APP_SECRET"`
	EncryptKey         SecretRef           `json:"encrypt_key"             env:"PICOCLAW_CHANNELS_FEISHU_ENCRYPT_KEY"`
	VerificationToken  SecretRef           `json:"verification_token"      env:"PICOCLAW_CHANNELS_FEISHU_VERIFICATION_TOKEN"`
	BotID              string              `json:"bot_id,omitempty"         env:"PICOCLAW_CHANNELS_FEISHU_BOT_ID"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"PICOCLAW_CHANNELS_FEISHU_ALLOW_FROM"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	Typing             TypingConfig        `json:"typing,omitempty"`
	Placeholder        PlaceholderConfig   `json:"placeholder,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"PICOCLAW_CHANNELS_FEISHU_REASONING_CHANNEL_ID"`
}

type DiscordConfig struct {
	Enabled            bool                `json:"enabled"                 env:"PICOCLAW_CHANNELS_DISCORD_ENABLED"`
	Token              SecretRef           `json:"token"                   env:"PICOCLAW_CHANNELS_DISCORD_TOKEN"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"PICOCLAW_CHANNELS_DISCORD_ALLOW_FROM"`
	MentionOnly        bool                `json:"mention_only"            env:"PICOCLAW_CHANNELS_DISCORD_MENTION_ONLY"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	Typing             TypingConfig        `json:"typing,omitempty"`
	Placeholder        PlaceholderConfig   `json:"placeholder,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"PICOCLAW_CHANNELS_DISCORD_REASONING_CHANNEL_ID"`
}

type MaixCamConfig struct {
	Enabled            bool                `json:"enabled"              env:"PICOCLAW_CHANNELS_MAIXCAM_ENABLED"`
	Host               string              `json:"host"                 env:"PICOCLAW_CHANNELS_MAIXCAM_HOST"`
	Port               int                 `json:"port"                 env:"PICOCLAW_CHANNELS_MAIXCAM_PORT"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"           env:"PICOCLAW_CHANNELS_MAIXCAM_ALLOW_FROM"`
	ReasoningChannelID string              `json:"reasoning_channel_id" env:"PICOCLAW_CHANNELS_MAIXCAM_REASONING_CHANNEL_ID"`
}

type QQConfig struct {
	Enabled            bool                `json:"enabled"                 env:"PICOCLAW_CHANNELS_QQ_ENABLED"`
	AppID              string              `json:"app_id"                  env:"PICOCLAW_CHANNELS_QQ_APP_ID"`
	AppSecret          SecretRef           `json:"app_secret"              env:"PICOCLAW_CHANNELS_QQ_APP_SECRET"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"PICOCLAW_CHANNELS_QQ_ALLOW_FROM"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"PICOCLAW_CHANNELS_QQ_REASONING_CHANNEL_ID"`
}

type DingTalkConfig struct {
	Enabled            bool                `json:"enabled"                 env:"PICOCLAW_CHANNELS_DINGTALK_ENABLED"`
	ClientID           string              `json:"client_id"               env:"PICOCLAW_CHANNELS_DINGTALK_CLIENT_ID"`
	ClientSecret       SecretRef           `json:"client_secret"           env:"PICOCLAW_CHANNELS_DINGTALK_CLIENT_SECRET"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"PICOCLAW_CHANNELS_DINGTALK_ALLOW_FROM"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"PICOCLAW_CHANNELS_DINGTALK_REASONING_CHANNEL_ID"`
}

type SlackConfig struct {
	Enabled            bool                `json:"enabled"                 env:"PICOCLAW_CHANNELS_SLACK_ENABLED"`
	BotToken           SecretRef           `json:"bot_token"               env:"PICOCLAW_CHANNELS_SLACK_BOT_TOKEN"`
	AppToken           SecretRef           `json:"app_token"               env:"PICOCLAW_CHANNELS_SLACK_APP_TOKEN"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"PICOCLAW_CHANNELS_SLACK_ALLOW_FROM"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	Typing             TypingConfig        `json:"typing,omitempty"`
	Placeholder        PlaceholderConfig   `json:"placeholder,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"PICOCLAW_CHANNELS_SLACK_REASONING_CHANNEL_ID"`
}

type LINEConfig struct {
	Enabled            bool                `json:"enabled"                 env:"PICOCLAW_CHANNELS_LINE_ENABLED"`
	ChannelSecret      SecretRef           `json:"channel_secret"          env:"PICOCLAW_CHANNELS_LINE_CHANNEL_SECRET"`
	ChannelAccessToken SecretRef           `json:"channel_access_token"    env:"PICOCLAW_CHANNELS_LINE_CHANNEL_ACCESS_TOKEN"`
	WebhookHost        string              `json:"webhook_host"            env:"PICOCLAW_CHANNELS_LINE_WEBHOOK_HOST"`
	WebhookPort        int                 `json:"webhook_port"            env:"PICOCLAW_CHANNELS_LINE_WEBHOOK_PORT"`
	WebhookPath        string              `json:"webhook_path"            env:"PICOCLAW_CHANNELS_LINE_WEBHOOK_PATH"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"PICOCLAW_CHANNELS_LINE_ALLOW_FROM"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	Typing             TypingConfig        `json:"typing,omitempty"`
	Placeholder        PlaceholderConfig   `json:"placeholder,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"PICOCLAW_CHANNELS_LINE_REASONING_CHANNEL_ID"`
}

type OneBotConfig struct {
	Enabled            bool                `json:"enabled"                 env:"PICOCLAW_CHANNELS_ONEBOT_ENABLED"`
	WSUrl              string              `json:"ws_url"                  env:"PICOCLAW_CHANNELS_ONEBOT_WS_URL"`
	AccessToken        SecretRef           `json:"access_token"            env:"PICOCLAW_CHANNELS_ONEBOT_ACCESS_TOKEN"`
	ReconnectInterval  int                 `json:"reconnect_interval"      env:"PICOCLAW_CHANNELS_ONEBOT_RECONNECT_INTERVAL"`
	GroupTriggerPrefix []string            `json:"group_trigger_prefix"    env:"PICOCLAW_CHANNELS_ONEBOT_GROUP_TRIGGER_PREFIX"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"PICOCLAW_CHANNELS_ONEBOT_ALLOW_FROM"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	Typing             TypingConfig        `json:"typing,omitempty"`
	Placeholder        PlaceholderConfig   `json:"placeholder,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"PICOCLAW_CHANNELS_ONEBOT_REASONING_CHANNEL_ID"`
}

type WeComConfig struct {
	Enabled            bool                `json:"enabled"                 env:"PICOCLAW_CHANNELS_WECOM_ENABLED"`
	Token              SecretRef           `json:"token"                   env:"PICOCLAW_CHANNELS_WECOM_TOKEN"`
	EncodingAESKey     SecretRef           `json:"encoding_aes_key"        env:"PICOCLAW_CHANNELS_WECOM_ENCODING_AES_KEY"`
	WebhookURL         string              `json:"webhook_url"             env:"PICOCLAW_CHANNELS_WECOM_WEBHOOK_URL"`
	WebhookHost        string              `json:"webhook_host"            env:"PICOCLAW_CHANNELS_WECOM_WEBHOOK_HOST"`
	WebhookPort        int                 `json:"webhook_port"            env:"PICOCLAW_CHANNELS_WECOM_WEBHOOK_PORT"`
	WebhookPath        string              `json:"webhook_path"            env:"PICOCLAW_CHANNELS_WECOM_WEBHOOK_PATH"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"PICOCLAW_CHANNELS_WECOM_ALLOW_FROM"`
	ReplyTimeout       int                 `json:"reply_timeout"           env:"PICOCLAW_CHANNELS_WECOM_REPLY_TIMEOUT"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"PICOCLAW_CHANNELS_WECOM_REASONING_CHANNEL_ID"`
}

type WeComAppConfig struct {
	Enabled            bool                `json:"enabled"                 env:"PICOCLAW_CHANNELS_WECOM_APP_ENABLED"`
	CorpID             string              `json:"corp_id"                 env:"PICOCLAW_CHANNELS_WECOM_APP_CORP_ID"`
	CorpSecret         SecretRef           `json:"corp_secret"             env:"PICOCLAW_CHANNELS_WECOM_APP_CORP_SECRET"`
	AgentID            int64               `json:"agent_id"                env:"PICOCLAW_CHANNELS_WECOM_APP_AGENT_ID"`
	Token              SecretRef           `json:"token"                   env:"PICOCLAW_CHANNELS_WECOM_APP_TOKEN"`
	EncodingAESKey     SecretRef           `json:"encoding_aes_key"        env:"PICOCLAW_CHANNELS_WECOM_APP_ENCODING_AES_KEY"`
	WebhookHost        string              `json:"webhook_host"            env:"PICOCLAW_CHANNELS_WECOM_APP_WEBHOOK_HOST"`
	WebhookPort        int                 `json:"webhook_port"            env:"PICOCLAW_CHANNELS_WECOM_APP_WEBHOOK_PORT"`
	WebhookPath        string              `json:"webhook_path"            env:"PICOCLAW_CHANNELS_WECOM_APP_WEBHOOK_PATH"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"              env:"PICOCLAW_CHANNELS_WECOM_APP_ALLOW_FROM"`
	ReplyTimeout       int                 `json:"reply_timeout"           env:"PICOCLAW_CHANNELS_WECOM_APP_REPLY_TIMEOUT"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id"    env:"PICOCLAW_CHANNELS_WECOM_APP_REASONING_CHANNEL_ID"`
}

type WeComAIBotConfig struct {
	Enabled            bool                `json:"enabled"              env:"PICOCLAW_CHANNELS_WECOM_AIBOT_ENABLED"`
	Token              SecretRef           `json:"token"                env:"PICOCLAW_CHANNELS_WECOM_AIBOT_TOKEN"`
	EncodingAESKey     SecretRef           `json:"encoding_aes_key"     env:"PICOCLAW_CHANNELS_WECOM_AIBOT_ENCODING_AES_KEY"`
	WebhookPath        string              `json:"webhook_path"         env:"PICOCLAW_CHANNELS_WECOM_AIBOT_WEBHOOK_PATH"`
	AllowFrom          FlexibleStringSlice `json:"allow_from"           env:"PICOCLAW_CHANNELS_WECOM_AIBOT_ALLOW_FROM"`
	ReplyTimeout       int                 `json:"reply_timeout"        env:"PICOCLAW_CHANNELS_WECOM_AIBOT_REPLY_TIMEOUT"`
	MaxSteps           int                 `json:"max_steps"            env:"PICOCLAW_CHANNELS_WECOM_AIBOT_MAX_STEPS"`       // Maximum streaming steps
	WelcomeMessage     string              `json:"welcome_message"      env:"PICOCLAW_CHANNELS_WECOM_AIBOT_WELCOME_MESSAGE"` // Sent on enter_chat event; empty = no welcome
	ReasoningChannelID string              `json:"reasoning_channel_id" env:"PICOCLAW_CHANNELS_WECOM_AIBOT_REASONING_CHANNEL_ID"`
}

type PicoConfig struct {
	Enabled         bool                `json:"enabled"                     env:"PICOCLAW_CHANNELS_PICO_ENABLED"`
	Token           SecretRef           `json:"token"                       env:"PICOCLAW_CHANNELS_PICO_TOKEN"`
	AllowTokenQuery bool                `json:"allow_token_query,omitempty"`
	AllowOrigins    []string            `json:"allow_origins,omitempty"`
	PingInterval    int                 `json:"ping_interval,omitempty"`
	ReadTimeout     int                 `json:"read_timeout,omitempty"`
	WriteTimeout    int                 `json:"write_timeout,omitempty"`
	MaxConnections  int                 `json:"max_connections,omitempty"`
	AllowFrom       FlexibleStringSlice `json:"allow_from"                  env:"PICOCLAW_CHANNELS_PICO_ALLOW_FROM"`
	Placeholder     PlaceholderConfig   `json:"placeholder,omitempty"`
}

type HeartbeatConfig struct {
	Enabled  bool `json:"enabled"  env:"PICOCLAW_HEARTBEAT_ENABLED"`
	Interval int  `json:"interval" env:"PICOCLAW_HEARTBEAT_INTERVAL"` // minutes, min 5
}

type OrchestrationConfig struct {
	Enabled                   bool              `json:"enabled"                      env:"PICOCLAW_ORCHESTRATION_ENABLED"`
	MaxSpawnDepth             int               `json:"max_spawn_depth"              env:"PICOCLAW_ORCHESTRATION_MAX_SPAWN_DEPTH"`
	MaxParallelWorkers        int               `json:"max_parallel_workers"         env:"PICOCLAW_ORCHESTRATION_MAX_PARALLEL_WORKERS"`
	MaxTasksPerAgent          int               `json:"max_tasks_per_agent"          env:"PICOCLAW_ORCHESTRATION_MAX_TASKS_PER_AGENT"`
	DefaultTaskTimeoutSeconds int               `json:"default_task_timeout_seconds" env:"PICOCLAW_ORCHESTRATION_DEFAULT_TASK_TIMEOUT_SECONDS"`
	RetryLimitPerTask         int               `json:"retry_limit_per_task"         env:"PICOCLAW_ORCHESTRATION_RETRY_LIMIT_PER_TASK"`
	ToolCallsParallelEnabled  bool              `json:"tool_calls_parallel_enabled"  env:"PICOCLAW_ORCHESTRATION_TOOL_CALLS_PARALLEL_ENABLED"`
	MaxToolCallConcurrency    int               `json:"max_tool_call_concurrency"    env:"PICOCLAW_ORCHESTRATION_MAX_TOOL_CALL_CONCURRENCY"`
	ParallelToolsMode         string            `json:"parallel_tools_mode"          env:"PICOCLAW_ORCHESTRATION_PARALLEL_TOOLS_MODE"`
	ToolParallelOverrides     map[string]string `json:"tool_parallel_overrides,omitempty"`
}

type AuditConfig struct {
	Enabled             bool                  `json:"enabled"               env:"PICOCLAW_AUDIT_ENABLED"`
	IntervalMinutes     int                   `json:"interval_minutes"      env:"PICOCLAW_AUDIT_INTERVAL_MINUTES"`
	LookbackMinutes     int                   `json:"lookback_minutes"      env:"PICOCLAW_AUDIT_LOOKBACK_MINUTES"`
	Supervisor          AuditSupervisorConfig `json:"supervisor"`
	MinConfidence       float64               `json:"min_confidence"        env:"PICOCLAW_AUDIT_MIN_CONFIDENCE"`
	InconsistencyPolicy string                `json:"inconsistency_policy"  env:"PICOCLAW_AUDIT_INCONSISTENCY_POLICY"`
	AutoRemediation     string                `json:"auto_remediation"      env:"PICOCLAW_AUDIT_AUTO_REMEDIATION"`
	// MaxAutoRemediationsPerCycle limits how many retry/fix tasks can be spawned
	// in one audit cycle to avoid runaway loops.
	MaxAutoRemediationsPerCycle int `json:"max_auto_remediations_per_cycle" env:"PICOCLAW_AUDIT_MAX_AUTO_REMEDIATIONS_PER_CYCLE"`
	// RemediationCooldownMinutes prevents re-triggering remediation for the same
	// task too frequently.
	RemediationCooldownMinutes int `json:"remediation_cooldown_minutes"     env:"PICOCLAW_AUDIT_REMEDIATION_COOLDOWN_MINUTES"`
	// RemediationAgentID optionally delegates remediation retries to a specific
	// agent id (requires subagent allowlist when targeting a different agent).
	RemediationAgentID string `json:"remediation_agent_id"               env:"PICOCLAW_AUDIT_REMEDIATION_AGENT_ID"`
	NotifyChannel      string `json:"notify_channel"        env:"PICOCLAW_AUDIT_NOTIFY_CHANNEL"`
}

type AuditSupervisorConfig struct {
	Enabled     bool              `json:"enabled"     env:"PICOCLAW_AUDIT_SUPERVISOR_ENABLED"`
	Model       *AgentModelConfig `json:"model,omitempty"`
	Temperature *float64          `json:"temperature,omitempty" env:"PICOCLAW_AUDIT_SUPERVISOR_TEMPERATURE"`
	MaxTokens   int               `json:"max_tokens,omitempty"  env:"PICOCLAW_AUDIT_SUPERVISOR_MAX_TOKENS"`

	// Mode controls when the supervisor LLM is used:
	// - "always" (default): review every completed task in the lookback window.
	// - "escalate": only review tasks that already have rule-based findings.
	Mode string `json:"mode,omitempty"`

	// MaxTasks caps how many tasks are reviewed per audit cycle (to cap cost/latency).
	// 0 disables the cap.
	MaxTasks int `json:"max_tasks,omitempty"`
}

// LimitsConfig defines soft resource budgets enforced by the agent/runtime.
//
// Phase H1 in ROADMAP_V2.md:
// - Per-run budgets: tool calls, wall time, output size.
// - Per-tool guards: file read limits, tool output truncation.
//
// These limits are intended to prevent runaway memory growth / OOM kills while
// keeping behavior predictable. They are "soft" in the sense that exceeding a
// limit results in a controlled, user-visible stop rather than a hard crash.
type LimitsConfig struct {
	Enabled bool `json:"enabled,omitempty" env:"PICOCLAW_LIMITS_ENABLED"`

	// MaxRunWallTimeSeconds caps a single agent run (one inbound message -> one response).
	// 0 disables the wall-time budget.
	MaxRunWallTimeSeconds int `json:"max_run_wall_time_seconds,omitempty" env:"PICOCLAW_LIMITS_MAX_RUN_WALL_TIME_SECONDS"`

	// MaxToolCallsPerRun caps the total number of tool calls executed in one run.
	// 0 disables the budget.
	MaxToolCallsPerRun int `json:"max_tool_calls_per_run,omitempty" env:"PICOCLAW_LIMITS_MAX_TOOL_CALLS_PER_RUN"`

	// MaxToolResultChars truncates ToolResult.ForLLM/ForUser to control memory usage.
	// 0 disables truncation.
	MaxToolResultChars int `json:"max_tool_result_chars,omitempty" env:"PICOCLAW_LIMITS_MAX_TOOL_RESULT_CHARS"`

	// MaxReadFileBytes limits how many bytes the read_file tool reads from disk by default.
	// 0 means use the built-in default.
	MaxReadFileBytes int `json:"max_read_file_bytes,omitempty" env:"PICOCLAW_LIMITS_MAX_READ_FILE_BYTES"`
}

// AuditLogConfig controls the append-only operational audit log (JSONL).
//
// Phase H3 in ROADMAP_V2.md:
// - Record major runtime events (tool executions, config reload, estop changes, etc.)
// - Support rotation to cap disk usage
//
// This is intentionally separate from `audit` (task auditing / supervisor checks).
type AuditLogConfig struct {
	Enabled bool `json:"enabled,omitempty" env:"PICOCLAW_AUDIT_LOG_ENABLED"`

	// Dir overrides the default audit log directory.
	// When empty, defaults to: <workspace>/.picoclaw/audit
	Dir string `json:"dir,omitempty" env:"PICOCLAW_AUDIT_LOG_DIR"`

	// MaxBytes rotates the log when it grows beyond this size.
	// 0 disables size-based rotation.
	MaxBytes int `json:"max_bytes,omitempty" env:"PICOCLAW_AUDIT_LOG_MAX_BYTES"`

	// MaxBackups controls how many rotated files to keep (best-effort).
	// 0 means keep all rotated files.
	MaxBackups int `json:"max_backups,omitempty" env:"PICOCLAW_AUDIT_LOG_MAX_BACKUPS"`

	// HMACKey enables per-line HMAC signatures on audit.jsonl entries.
	// When empty, signing is disabled. Prefer setting this via SecretRef (env/file)
	// to avoid embedding plaintext keys in config files.
	HMACKey SecretRef `json:"hmac_key,omitempty" env:"PICOCLAW_AUDIT_LOG_HMAC_KEY"`

	// HMACKeyID is an optional identifier written into each signed line
	// to support key rotation and log forensics.
	HMACKeyID string `json:"hmac_key_id,omitempty" env:"PICOCLAW_AUDIT_LOG_HMAC_KEY_ID"`
}

type ProvidersConfig struct {
	Anthropic     ProviderConfig       `json:"anthropic"`
	OpenAI        OpenAIProviderConfig `json:"openai"`
	LiteLLM       ProviderConfig       `json:"litellm"`
	OpenRouter    ProviderConfig       `json:"openrouter"`
	Groq          ProviderConfig       `json:"groq"`
	Zhipu         ProviderConfig       `json:"zhipu"`
	VLLM          ProviderConfig       `json:"vllm"`
	Gemini        ProviderConfig       `json:"gemini"`
	Nvidia        ProviderConfig       `json:"nvidia"`
	Ollama        ProviderConfig       `json:"ollama"`
	Moonshot      ProviderConfig       `json:"moonshot"`
	ShengSuanYun  ProviderConfig       `json:"shengsuanyun"`
	DeepSeek      ProviderConfig       `json:"deepseek"`
	Cerebras      ProviderConfig       `json:"cerebras"`
	VolcEngine    ProviderConfig       `json:"volcengine"`
	GitHubCopilot ProviderConfig       `json:"github_copilot"`
	Antigravity   ProviderConfig       `json:"antigravity"`
	Qwen          ProviderConfig       `json:"qwen"`
	Mistral       ProviderConfig       `json:"mistral"`
}

// IsEmpty checks if all provider configs are empty (no API keys or API bases set)
// Note: WebSearch is an optimization option and doesn't count as "non-empty"
func (p ProvidersConfig) IsEmpty() bool {
	return p.Anthropic.APIKey.IsZero() && p.Anthropic.APIBase == "" &&
		p.OpenAI.APIKey.IsZero() && p.OpenAI.APIBase == "" &&
		p.LiteLLM.APIKey.IsZero() && p.LiteLLM.APIBase == "" &&
		p.OpenRouter.APIKey.IsZero() && p.OpenRouter.APIBase == "" &&
		p.Groq.APIKey.IsZero() && p.Groq.APIBase == "" &&
		p.Zhipu.APIKey.IsZero() && p.Zhipu.APIBase == "" &&
		p.VLLM.APIKey.IsZero() && p.VLLM.APIBase == "" &&
		p.Gemini.APIKey.IsZero() && p.Gemini.APIBase == "" &&
		p.Nvidia.APIKey.IsZero() && p.Nvidia.APIBase == "" &&
		p.Ollama.APIKey.IsZero() && p.Ollama.APIBase == "" &&
		p.Moonshot.APIKey.IsZero() && p.Moonshot.APIBase == "" &&
		p.ShengSuanYun.APIKey.IsZero() && p.ShengSuanYun.APIBase == "" &&
		p.DeepSeek.APIKey.IsZero() && p.DeepSeek.APIBase == "" &&
		p.Cerebras.APIKey.IsZero() && p.Cerebras.APIBase == "" &&
		p.VolcEngine.APIKey.IsZero() && p.VolcEngine.APIBase == "" &&
		p.GitHubCopilot.APIKey.IsZero() && p.GitHubCopilot.APIBase == "" &&
		p.Antigravity.APIKey.IsZero() && p.Antigravity.APIBase == "" &&
		p.Qwen.APIKey.IsZero() && p.Qwen.APIBase == "" &&
		p.Mistral.APIKey.IsZero() && p.Mistral.APIBase == ""
}

// MarshalJSON implements custom JSON marshaling for ProvidersConfig
// to omit the entire section when empty
func (p ProvidersConfig) MarshalJSON() ([]byte, error) {
	if p.IsEmpty() {
		return []byte("null"), nil
	}
	type Alias ProvidersConfig
	return json.Marshal((*Alias)(&p))
}

type ProviderConfig struct {
	APIKey         SecretRef `json:"api_key"                   env:"PICOCLAW_PROVIDERS_{{.Name}}_API_KEY"`
	APIBase        string    `json:"api_base"                  env:"PICOCLAW_PROVIDERS_{{.Name}}_API_BASE"`
	Proxy          string    `json:"proxy,omitempty"           env:"PICOCLAW_PROVIDERS_{{.Name}}_PROXY"`
	RequestTimeout int       `json:"request_timeout,omitempty" env:"PICOCLAW_PROVIDERS_{{.Name}}_REQUEST_TIMEOUT"`
	AuthMethod     string    `json:"auth_method,omitempty"     env:"PICOCLAW_PROVIDERS_{{.Name}}_AUTH_METHOD"`
	ConnectMode    string    `json:"connect_mode,omitempty"    env:"PICOCLAW_PROVIDERS_{{.Name}}_CONNECT_MODE"` // only for Github Copilot, `stdio` or `grpc`
}

type OpenAIProviderConfig struct {
	ProviderConfig
	WebSearch bool `json:"web_search" env:"PICOCLAW_PROVIDERS_OPENAI_WEB_SEARCH"`
}

// ModelConfig represents a model-centric provider configuration.
// It allows adding new providers (especially OpenAI-compatible ones) via configuration only.
// The model field uses protocol prefix format: [protocol/]model-identifier
// Supported protocols: openai, anthropic, antigravity, claude-cli, codex-cli, github-copilot
// Default protocol is "openai" if no prefix is specified.
type ModelConfig struct {
	// Required fields
	ModelName string `json:"model_name"` // User-facing alias for the model
	Model     string `json:"model"`      // Protocol/model-identifier (e.g., "openai/gpt-4o", "anthropic/claude-sonnet-4.6")

	// HTTP-based providers
	APIBase string    `json:"api_base,omitempty"` // API endpoint URL
	APIKey  SecretRef `json:"api_key"`            // API authentication key (supports SecretRef)
	Proxy   string    `json:"proxy,omitempty"`    // HTTP proxy URL

	// Special providers (CLI-based, OAuth, etc.)
	AuthMethod  string `json:"auth_method,omitempty"`  // Authentication method: oauth, token
	ConnectMode string `json:"connect_mode,omitempty"` // Connection mode: stdio, grpc
	Workspace   string `json:"workspace,omitempty"`    // Workspace path for CLI-based providers

	// Optional optimizations
	RPM            int    `json:"rpm,omitempty"`              // Requests per minute limit
	MaxTokensField string `json:"max_tokens_field,omitempty"` // Field name for max tokens (e.g., "max_completion_tokens")
	RequestTimeout int    `json:"request_timeout,omitempty"`
}

// Validate checks if the ModelConfig has all required fields.
func (c *ModelConfig) Validate() error {
	if c.ModelName == "" {
		return fmt.Errorf("model_name is required")
	}
	if c.Model == "" {
		return fmt.Errorf("model is required")
	}
	return nil
}

type GatewayConfig struct {
	Host   string    `json:"host"    env:"PICOCLAW_GATEWAY_HOST"`
	Port   int       `json:"port"    env:"PICOCLAW_GATEWAY_PORT"`
	APIKey SecretRef `json:"api_key,omitempty" env:"PICOCLAW_GATEWAY_API_KEY"`

	// InboundQueue enables per-session serial processing with a global concurrency cap.
	InboundQueue GatewayInboundQueueConfig `json:"inbound_queue,omitempty"`

	// Reload controls runtime config hot reload behavior (gateway-only).
	Reload GatewayReloadConfig `json:"reload,omitempty"`
}

type GatewayInboundQueueConfig struct {
	Enabled bool `json:"enabled,omitempty"`

	// MaxConcurrency caps how many sessions may be processed concurrently.
	// Values <= 0 default to 1 (fully serial).
	MaxConcurrency int `json:"max_concurrency,omitempty"`

	// PerSessionBuffer caps queued inbound messages per session.
	// Values <= 0 default to 32.
	PerSessionBuffer int `json:"per_session_buffer,omitempty"`
}

type GatewayReloadConfig struct {
	// Enabled toggles hot reload support. When false, SIGHUP/watch are ignored.
	Enabled bool `json:"enabled,omitempty"`

	// Watch enables polling-based config file monitoring for automatic reloads.
	// This avoids requiring container restarts when config.json changes.
	Watch bool `json:"watch,omitempty"`

	// IntervalSeconds is the poll interval when Watch is enabled.
	// Values <= 0 default to 2 seconds.
	IntervalSeconds int `json:"interval_seconds,omitempty"`
}

type BraveConfig struct {
	Enabled    bool        `json:"enabled"     env:"PICOCLAW_TOOLS_WEB_BRAVE_ENABLED"`
	APIKey     SecretRef   `json:"api_key"     env:"PICOCLAW_TOOLS_WEB_BRAVE_API_KEY"`
	APIKeys    []SecretRef `json:"api_keys,omitempty"`
	MaxResults int         `json:"max_results" env:"PICOCLAW_TOOLS_WEB_BRAVE_MAX_RESULTS"`
}

type TavilyConfig struct {
	Enabled    bool        `json:"enabled"     env:"PICOCLAW_TOOLS_WEB_TAVILY_ENABLED"`
	APIKey     SecretRef   `json:"api_key"     env:"PICOCLAW_TOOLS_WEB_TAVILY_API_KEY"`
	APIKeys    []SecretRef `json:"api_keys,omitempty"`
	BaseURL    string      `json:"base_url"    env:"PICOCLAW_TOOLS_WEB_TAVILY_BASE_URL"`
	MaxResults int         `json:"max_results" env:"PICOCLAW_TOOLS_WEB_TAVILY_MAX_RESULTS"`
}

type DuckDuckGoConfig struct {
	Enabled    bool `json:"enabled"     env:"PICOCLAW_TOOLS_WEB_DUCKDUCKGO_ENABLED"`
	MaxResults int  `json:"max_results" env:"PICOCLAW_TOOLS_WEB_DUCKDUCKGO_MAX_RESULTS"`
}

type GrokConfig struct {
	Enabled      bool        `json:"enabled"       env:"PICOCLAW_TOOLS_WEB_GROK_ENABLED"`
	APIKey       SecretRef   `json:"api_key"       env:"PICOCLAW_TOOLS_WEB_GROK_API_KEY"`
	APIKeys      []SecretRef `json:"api_keys,omitempty"`
	Endpoint     string      `json:"endpoint"      env:"PICOCLAW_TOOLS_WEB_GROK_ENDPOINT"`
	DefaultModel string      `json:"default_model" env:"PICOCLAW_TOOLS_WEB_GROK_DEFAULT_MODEL"`
	MaxResults   int         `json:"max_results"   env:"PICOCLAW_TOOLS_WEB_GROK_MAX_RESULTS"`
}

// WebEvidenceModeConfig enables "evidence mode" for web tools.
//
// Phase W1 / ROADMAP.md: for fact / latest information queries, require at least
// N distinct sources (domains). When evidence cannot be satisfied, the assistant
// should explicitly state uncertainty and suggest verification steps.
type WebEvidenceModeConfig struct {
	Enabled bool `json:"enabled,omitempty"`

	// MinDomains is the minimum number of distinct domains required for evidence.
	// Values <= 0 default to 2.
	MinDomains int `json:"min_domains,omitempty"`
}

type WebToolsConfig struct {
	Brave      BraveConfig           `json:"brave"`
	Tavily     TavilyConfig          `json:"tavily"`
	DuckDuckGo DuckDuckGoConfig      `json:"duckduckgo"`
	Grok       GrokConfig            `json:"grok"`
	Evidence   WebEvidenceModeConfig `json:"evidence_mode,omitempty"`
	// Proxy is an optional proxy URL for web tools (http/https/socks5/socks5h).
	// For authenticated proxies, prefer HTTP_PROXY/HTTPS_PROXY env vars instead of embedding credentials in config.
	Proxy           string `json:"proxy,omitempty"             env:"PICOCLAW_TOOLS_WEB_PROXY"`
	FetchLimitBytes int64  `json:"fetch_limit_bytes,omitempty" env:"PICOCLAW_TOOLS_WEB_FETCH_LIMIT_BYTES"`

	FetchCache WebFetchCacheConfig `json:"fetch_cache,omitempty"`
}

type WebFetchCacheConfig struct {
	Enabled bool `json:"enabled,omitempty"`

	// TTLSeconds controls how long a fetched URL is cached in memory.
	// 0 uses a small built-in default; negative disables caching.
	TTLSeconds int `json:"ttl_seconds,omitempty"`

	// MaxEntries caps how many URLs are retained.
	// 0 uses a small built-in default.
	MaxEntries int `json:"max_entries,omitempty"`

	// MaxEntryChars caps cached extracted text per URL to limit memory usage.
	// 0 uses a built-in default.
	MaxEntryChars int `json:"max_entry_chars,omitempty"`
}

type CronToolsConfig struct {
	ExecTimeoutMinutes int `json:"exec_timeout_minutes" env:"PICOCLAW_TOOLS_CRON_EXEC_TIMEOUT_MINUTES"` // 0 means no timeout
}

// ToolTraceConfig controls on-disk tracing of tool calls for debugging and replay.
//
// When enabled, PicoClaw appends an event stream (JSONL) and (optionally) writes
// per-call snapshots (JSON/Markdown) under the agent workspace.
type ToolTraceConfig struct {
	Enabled bool `json:"enabled" env:"PICOCLAW_TOOLS_TRACE_ENABLED"`
	// Dir overrides the default trace directory.
	// When empty, traces are written under: <workspace>/.picoclaw/audit/tools/<session>/
	Dir string `json:"dir,omitempty" env:"PICOCLAW_TOOLS_TRACE_DIR"`

	// WritePerCallFiles controls writing one JSON + one Markdown file per tool call.
	WritePerCallFiles bool `json:"write_per_call_files" env:"PICOCLAW_TOOLS_TRACE_WRITE_PER_CALL_FILES"`

	// MaxArgPreviewChars controls args_preview truncation in the JSONL event stream.
	MaxArgPreviewChars int `json:"max_arg_preview_chars" env:"PICOCLAW_TOOLS_TRACE_MAX_ARG_PREVIEW_CHARS"`
	// MaxResultPreviewChars controls output previews truncation in the JSONL event stream.
	MaxResultPreviewChars int `json:"max_result_preview_chars" env:"PICOCLAW_TOOLS_TRACE_MAX_RESULT_PREVIEW_CHARS"`
}

// ToolErrorTemplateConfig controls executor-level tool error wrapping for the LLM.
//
// When enabled, tool failures are wrapped into a structured JSON payload with
// minimal recovery hints. This helps the model self-correct by adjusting args
// or switching tools, without changing each individual tool implementation.
type ToolErrorTemplateConfig struct {
	Enabled bool `json:"enabled" env:"PICOCLAW_TOOLS_ERROR_TEMPLATE_ENABLED"`

	// IncludeSchema adds a small summary of tool parameters (required + known keys)
	// when the tool definition is available.
	IncludeSchema bool `json:"include_schema" env:"PICOCLAW_TOOLS_ERROR_TEMPLATE_INCLUDE_SCHEMA"`
}

type ExecConfig struct {
	EnableDenyPatterns  bool     `json:"enable_deny_patterns"  env:"PICOCLAW_TOOLS_EXEC_ENABLE_DENY_PATTERNS"`
	CustomDenyPatterns  []string `json:"custom_deny_patterns"  env:"PICOCLAW_TOOLS_EXEC_CUSTOM_DENY_PATTERNS"`
	CustomAllowPatterns []string `json:"custom_allow_patterns" env:"PICOCLAW_TOOLS_EXEC_CUSTOM_ALLOW_PATTERNS"`

	Env ExecEnvConfig `json:"env,omitempty"`

	// HostLimits optionally applies OS-level ulimit-style hard limits to host exec backend.
	// NOTE: These limits are best-effort and platform dependent. Prefer docker backend for
	// stronger isolation where available.
	HostLimits ExecHostLimitsConfig `json:"host_limits,omitempty"`

	// Backend selects the exec runtime:
	// - "host" (default): run commands on the host directly
	// - "docker": run commands inside a disposable docker container (workspace mounted)
	Backend string           `json:"backend,omitempty"`
	Docker  ExecDockerConfig `json:"docker,omitempty"`
}

type ExecEnvConfig struct {
	// Mode controls which environment variables are passed to exec tool commands:
	// - "inherit": pass the full host environment (break-glass required; may leak secrets)
	// - "allowlist": pass only EnvAllow (plus platform defaults)
	Mode string `json:"mode,omitempty" env:"PICOCLAW_TOOLS_EXEC_ENV_MODE"`

	// EnvAllow is the allow-list of environment variable names (exact match).
	EnvAllow []string `json:"allow,omitempty" env:"PICOCLAW_TOOLS_EXEC_ENV_ALLOW"`
}

// ExecHostLimitsConfig controls optional OS-level hard limits for host exec tool backend.
// All fields are optional; zero/negative values disable that limit.
type ExecHostLimitsConfig struct {
	// MemoryMB maps to ulimit -v (virtual memory) on Unix. It is approximate.
	MemoryMB int `json:"memory_mb,omitempty" env:"PICOCLAW_TOOLS_EXEC_HOST_MEMORY_MB"`
	// CPUSeconds maps to ulimit -t (CPU time) on Unix.
	CPUSeconds int `json:"cpu_seconds,omitempty" env:"PICOCLAW_TOOLS_EXEC_HOST_CPU_SECONDS"`
	// FileSizeMB maps to ulimit -f (max file size) on Unix.
	FileSizeMB int `json:"file_size_mb,omitempty" env:"PICOCLAW_TOOLS_EXEC_HOST_FILE_SIZE_MB"`
	// NProc maps to ulimit -u (max user processes) on Unix.
	NProc int `json:"nproc,omitempty" env:"PICOCLAW_TOOLS_EXEC_HOST_NPROC"`
}

type ExecDockerConfig struct {
	// Image is required when Backend="docker" (e.g. "alpine:3.20").
	Image string `json:"image,omitempty"`

	// Network config for docker run. Recommended: "none".
	// Empty defaults to "none".
	Network string `json:"network,omitempty"`

	// ReadOnlyRootFS sets --read-only and mounts tmpfs for /tmp and /var/tmp.
	ReadOnlyRootFS bool `json:"read_only_rootfs,omitempty"`

	// MemoryMB maps to docker run --memory (in megabytes). 0 disables the limit.
	MemoryMB int `json:"memory_mb,omitempty" env:"PICOCLAW_TOOLS_EXEC_DOCKER_MEMORY_MB"`

	// CPUs maps to docker run --cpus (e.g. 0.5). 0 disables the limit.
	CPUs float64 `json:"cpus,omitempty" env:"PICOCLAW_TOOLS_EXEC_DOCKER_CPUS"`

	// PidsLimit maps to docker run --pids-limit. 0 disables the limit.
	PidsLimit int `json:"pids_limit,omitempty" env:"PICOCLAW_TOOLS_EXEC_DOCKER_PIDS_LIMIT"`
}

// EstopConfig enables a global kill switch for tool execution (Policy-first Tools).
//
// ROADMAP.md:1138 - estop subsystem as a durable safety control plane.
// When enabled, PicoClaw reads <workspace>/.picoclaw/state/estop.json and denies
// tools accordingly (kill_all / network_kill / frozen tools / blocked domains).
type EstopConfig struct {
	Enabled bool `json:"enabled,omitempty"`

	// FailClosed engages safe mode (kill_all) when the estop state file exists but
	// cannot be read or parsed.
	FailClosed bool `json:"fail_closed,omitempty"`
}

// PlanModeConfig controls the "plan" permission mode (Policy-first Tools).
//
// When enabled, sessions may enter plan mode, during which a restricted tool set
// is denied (typically side-effect tools like exec/edit/write). Users can then
// explicitly approve (exit plan mode) to allow execution.
type PlanModeConfig struct {
	Enabled bool `json:"enabled,omitempty"`

	// DefaultMode applies when a session has no saved mode yet.
	// Supported values: "run" (default) | "plan".
	DefaultMode string `json:"default_mode,omitempty"`

	// DefaultModeGroup applies when a group chat session has no saved mode yet.
	// Supported values: "run" | "plan" (recommended).
	// When empty, falls back to DefaultMode.
	DefaultModeGroup string `json:"default_mode_group,omitempty"`

	// RestrictedTools are denied while in plan mode.
	RestrictedTools []string `json:"restricted_tools,omitempty"`
	// RestrictedPrefixes are denied while in plan mode.
	RestrictedPrefixes []string `json:"restricted_prefixes,omitempty"`
}

type MediaCleanupConfig struct {
	Enabled  bool `json:"enabled"          env:"PICOCLAW_MEDIA_CLEANUP_ENABLED"`
	MaxAge   int  `json:"max_age_minutes"  env:"PICOCLAW_MEDIA_CLEANUP_MAX_AGE"`
	Interval int  `json:"interval_minutes" env:"PICOCLAW_MEDIA_CLEANUP_INTERVAL"`
}

type ToolsConfig struct {
	AllowReadPaths  []string                `json:"allow_read_paths"  env:"PICOCLAW_TOOLS_ALLOW_READ_PATHS"`
	AllowWritePaths []string                `json:"allow_write_paths" env:"PICOCLAW_TOOLS_ALLOW_WRITE_PATHS"`
	Web             WebToolsConfig          `json:"web"`
	MCP             MCPConfig               `json:"mcp,omitempty"`
	Policy          ToolPolicyConfig        `json:"policy,omitempty"`
	Hooks           ToolHooksConfig         `json:"hooks,omitempty"`
	PlanMode        PlanModeConfig          `json:"plan_mode,omitempty"`
	Estop           EstopConfig             `json:"estop,omitempty"`
	Trace           ToolTraceConfig         `json:"trace,omitempty"`
	ErrorTemplate   ToolErrorTemplateConfig `json:"error_template,omitempty"`
	Cron            CronToolsConfig         `json:"cron"`
	Exec            ExecConfig              `json:"exec"`
	Skills          SkillsToolsConfig       `json:"skills"`
	MediaCleanup    MediaCleanupConfig      `json:"media_cleanup"`
}

// ToolPolicyConfig defines a centralized policy/middleware for all tool calls.
//
// Phase D2 in ROADMAP.md:
// - Built-in tools and MCP tools are treated the same (single chokepoint).
// - Policy applies allow/deny, timeouts, redaction, auditing, confirmations, and idempotency.
//
// NOTE: This config is enforced at the tool executor layer (pkg/tools/toolcall_executor.go),
// so it automatically covers all tool sources (built-in, skills, MCP bridge).
type ToolPolicyConfig struct {
	Enabled bool `json:"enabled,omitempty"`

	// Allow/Deny lists support both exact tool names and prefixes.
	// If Allow or AllowPrefixes is non-empty, tools must match one of them to execute.
	Allow         []string `json:"allow,omitempty"`
	AllowPrefixes []string `json:"allow_prefixes,omitempty"`
	Deny          []string `json:"deny,omitempty"`
	DenyPrefixes  []string `json:"deny_prefixes,omitempty"`

	// TimeoutMS enforces a maximum wall time per tool call when no tighter deadline exists.
	TimeoutMS int `json:"timeout_ms,omitempty"`

	Redact      ToolPolicyRedactConfig      `json:"redact,omitempty"`
	Confirm     ToolPolicyConfirmConfig     `json:"confirm,omitempty"`
	Idempotency ToolPolicyIdempotencyConfig `json:"idempotency,omitempty"`
	Audit       ToolPolicyAuditConfig       `json:"audit,omitempty"`
}

// ToolHooksConfig enables lightweight tool call hooks/extensions (Phase N2 in ROADMAP_V2.md).
//
// Hooks run inside the tool executor chokepoint (same place as plan mode / tool policy),
// and can rewrite tool arguments, deny tool calls, or scrub tool results.
//
// This config is intentionally limited to built-in hooks (no dynamic plugin loading).
// Keep it small and explicit.
type ToolHooksConfig struct {
	Enabled bool `json:"enabled,omitempty"`

	// Redact applies best-effort redaction to tool outputs even when tools.policy.enabled=false.
	// This is useful to prevent leaking host secrets into provider prompts.
	Redact ToolPolicyRedactConfig `json:"redact,omitempty"`
}

type ToolPolicyRedactConfig struct {
	Enabled bool `json:"enabled,omitempty"`

	// JSONFields are field names to redact in JSON objects (case-insensitive).
	JSONFields []string `json:"json_fields,omitempty"`
	// Patterns are regex patterns applied to string outputs (best-effort).
	Patterns []string `json:"patterns,omitempty"`

	// ApplyToLLM controls whether redaction is applied to ToolResult.ForLLM (model input).
	// If false, redaction only affects audit logs/traces.
	ApplyToLLM bool `json:"apply_to_llm,omitempty"`
	// ApplyToUser controls whether redaction is applied to ToolResult.ForUser (user-facing).
	ApplyToUser bool `json:"apply_to_user,omitempty"`
}

type ToolPolicyConfirmConfig struct {
	Enabled bool `json:"enabled,omitempty"`

	// Mode controls when confirmation gates are active:
	// - "always": every matching tool call requires confirmation
	// - "resume_only": only during resume_last_task flows
	// - "never": disable confirmation gate
	Mode string `json:"mode,omitempty"`

	Tools        []string `json:"tools,omitempty"`
	ToolPrefixes []string `json:"tool_prefixes,omitempty"`

	// ExpiresSeconds limits how long a confirmation stays valid (per run).
	ExpiresSeconds int `json:"expires_seconds,omitempty"`
}

type ToolPolicyIdempotencyConfig struct {
	Enabled bool `json:"enabled,omitempty"`

	// Tools/ToolPrefixes declare which tool calls are treated as having side effects
	// and therefore should be idempotent across resume.
	Tools        []string `json:"tools,omitempty"`
	ToolPrefixes []string `json:"tool_prefixes,omitempty"`

	// CacheResult controls whether previously executed outputs are replayed
	// (instead of re-executing) when the same idempotency key is seen again.
	CacheResult bool `json:"cache_result,omitempty"`
}

type ToolPolicyAuditConfig struct {
	// Tags are attached to tool trace / policy ledger events for filtering.
	Tags map[string]string `json:"tags,omitempty"`
}

type SkillsToolsConfig struct {
	Registries            SkillsRegistriesConfig `json:"registries"`
	MaxConcurrentSearches int                    `json:"max_concurrent_searches" env:"PICOCLAW_SKILLS_MAX_CONCURRENT_SEARCHES"`
	SearchCache           SearchCacheConfig      `json:"search_cache"`
}

type SearchCacheConfig struct {
	MaxSize    int `json:"max_size"    env:"PICOCLAW_SKILLS_SEARCH_CACHE_MAX_SIZE"`
	TTLSeconds int `json:"ttl_seconds" env:"PICOCLAW_SKILLS_SEARCH_CACHE_TTL_SECONDS"`
}

type SkillsRegistriesConfig struct {
	ClawHub ClawHubRegistryConfig `json:"clawhub"`
}

type ClawHubRegistryConfig struct {
	Enabled         bool      `json:"enabled"           env:"PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_ENABLED"`
	BaseURL         string    `json:"base_url"          env:"PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_BASE_URL"`
	AuthToken       SecretRef `json:"auth_token"        env:"PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_AUTH_TOKEN"`
	SearchPath      string    `json:"search_path"       env:"PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_SEARCH_PATH"`
	SkillsPath      string    `json:"skills_path"       env:"PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_SKILLS_PATH"`
	DownloadPath    string    `json:"download_path"     env:"PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_DOWNLOAD_PATH"`
	Timeout         int       `json:"timeout"           env:"PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_TIMEOUT"`
	MaxZipSize      int       `json:"max_zip_size"      env:"PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_MAX_ZIP_SIZE"`
	MaxResponseSize int       `json:"max_response_size" env:"PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_MAX_RESPONSE_SIZE"`
}

// MCPServerConfig defines configuration for a single MCP server
type MCPServerConfig struct {
	// Enabled indicates whether this MCP server is active
	Enabled bool `json:"enabled"`
	// Command is the executable to run (e.g., "npx", "python", "/path/to/server")
	Command string `json:"command"`
	// Args are the arguments to pass to the command
	Args []string `json:"args,omitempty"`
	// Env are environment variables to set for the server process (stdio only)
	Env map[string]string `json:"env,omitempty"`
	// EnvFile is the path to a file containing environment variables (stdio only)
	EnvFile string `json:"env_file,omitempty"`
	// Type is "stdio", "sse", or "http" (default: stdio if command is set, sse if url is set)
	Type string `json:"type,omitempty"`
	// URL is used for SSE/HTTP transport
	URL string `json:"url,omitempty"`
	// Headers are HTTP headers to send with requests (sse/http only)
	Headers map[string]string `json:"headers,omitempty"`
}

// MCPConfig defines configuration for all MCP servers
type MCPConfig struct {
	// Enabled globally enables/disables MCP integration
	Enabled bool `json:"enabled" env:"PICOCLAW_TOOLS_MCP_ENABLED"`
	// Servers is a map of server name to server configuration
	Servers map[string]MCPServerConfig `json:"servers,omitempty"`
}

func LoadConfig(path string) (*Config, error) {
	cfg, err := loadConfigUnvalidated(path)
	if err != nil {
		return nil, err
	}
	if err := cfg.ValidateAll(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func loadConfigUnvalidated(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg.SourcePath = path
			return cfg, nil
		}
		return nil, err
	}

	// Pre-scan the JSON to check how many model_list entries the user provided.
	// Go's JSON decoder reuses existing slice backing-array elements rather than
	// zero-initializing them, so fields absent from the user's JSON (e.g. api_base)
	// would silently inherit values from the DefaultConfig template at the same
	// index position. We only reset cfg.ModelList when the user actually provides
	// entries; when count is 0 we keep DefaultConfig's built-in list as fallback.
	var tmp Config
	if err := json.Unmarshal(data, &tmp); err != nil {
		return nil, err
	}
	if len(tmp.ModelList) > 0 {
		cfg.ModelList = nil
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	// Migrate legacy channel config fields to new unified structures
	cfg.migrateChannelConfigs()

	// Auto-migrate: if only legacy providers config exists, convert to model_list
	if len(cfg.ModelList) == 0 && cfg.HasProvidersConfig() {
		cfg.ModelList = ConvertProvidersToModelList(cfg)
	}

	cfg.SourcePath = path
	cfg.NormalizeSecretRefs()

	return cfg, nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

func (c *Config) ValidateSecurity() error {
	if c == nil {
		return nil
	}

	problems := c.securityProblems()
	if len(problems) == 0 {
		return nil
	}

	msgs := make([]string, 0, len(problems))
	for _, p := range problems {
		if strings.TrimSpace(p.Message) != "" {
			msgs = append(msgs, strings.TrimSpace(p.Message))
		}
	}
	if len(msgs) == 0 {
		return fmt.Errorf("unsafe configuration (break-glass required)")
	}
	return fmt.Errorf("unsafe configuration (break-glass required): %s", strings.Join(msgs, "; "))
}

func (c *Config) migrateChannelConfigs() {
	// Discord: mention_only -> group_trigger.mention_only
	if c.Channels.Discord.MentionOnly && !c.Channels.Discord.GroupTrigger.MentionOnly {
		c.Channels.Discord.GroupTrigger.MentionOnly = true
	}

	// OneBot: group_trigger_prefix -> group_trigger.prefixes
	if len(c.Channels.OneBot.GroupTriggerPrefix) > 0 &&
		len(c.Channels.OneBot.GroupTrigger.Prefixes) == 0 {
		c.Channels.OneBot.GroupTrigger.Prefixes = c.Channels.OneBot.GroupTriggerPrefix
	}
}

func SaveConfig(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	// Use unified atomic write utility with explicit sync for flash storage reliability.
	return fileutil.WriteFileAtomic(path, data, 0o600)
}

func (c *Config) WorkspacePath() string {
	return expandHome(c.Agents.Defaults.Workspace)
}

func expandHome(path string) string {
	if path == "" {
		return path
	}
	if path[0] == '~' {
		home, _ := os.UserHomeDir()
		if len(path) > 1 && path[1] == '/' {
			return home + path[1:]
		}
		return home
	}
	return path
}

// GetModelConfig returns the ModelConfig for the given model name.
// If multiple configs exist with the same model_name, it uses round-robin
// selection for load balancing. Returns an error if the model is not found.
func (c *Config) GetModelConfig(modelName string) (*ModelConfig, error) {
	matches := c.findMatches(modelName)
	if len(matches) == 0 {
		return nil, fmt.Errorf("model %q not found in model_list or providers", modelName)
	}
	if len(matches) == 1 {
		return &matches[0], nil
	}

	// Multiple configs - use round-robin for load balancing
	idx := rrCounter.Add(1) % uint64(len(matches))
	return &matches[idx], nil
}

// findMatches finds all ModelConfig entries with the given model_name.
func (c *Config) findMatches(modelName string) []ModelConfig {
	var matches []ModelConfig
	for i := range c.ModelList {
		if c.ModelList[i].ModelName == modelName {
			matches = append(matches, c.ModelList[i])
		}
	}
	return matches
}

// HasProvidersConfig checks if any provider in the old providers config has configuration.
func (c *Config) HasProvidersConfig() bool {
	return !c.Providers.IsEmpty()
}

// ValidateModelList validates all ModelConfig entries in the model_list.
// It checks that each model config is valid.
// Note: Multiple entries with the same model_name are allowed for load balancing.
func (c *Config) ValidateModelList() error {
	if c == nil {
		return nil
	}

	problems := c.modelListProblems()
	if len(problems) == 0 {
		return nil
	}

	p := problems[0]
	path := strings.TrimSpace(p.Path)
	msg := strings.TrimSpace(p.Message)
	if path == "" {
		return fmt.Errorf("%s", msg)
	}
	if msg == "" {
		return fmt.Errorf("%s is invalid", path)
	}
	return fmt.Errorf("%s: %s", path, msg)
}
