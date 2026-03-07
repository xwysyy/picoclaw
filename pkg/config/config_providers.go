package config

import (
	"encoding/json"
	"fmt"
)

type HeartbeatConfig struct {
	Enabled  bool `json:"enabled"  env:"X_CLAW_HEARTBEAT_ENABLED"`
	Interval int  `json:"interval" env:"X_CLAW_HEARTBEAT_INTERVAL"` // minutes, min 5
}

type OrchestrationConfig struct {
	Enabled                   bool              `json:"enabled"                      env:"X_CLAW_ORCHESTRATION_ENABLED"`
	MaxParallelWorkers        int               `json:"max_parallel_workers"         env:"X_CLAW_ORCHESTRATION_MAX_PARALLEL_WORKERS"`
	MaxTasksPerAgent          int               `json:"max_tasks_per_agent"          env:"X_CLAW_ORCHESTRATION_MAX_TASKS_PER_AGENT"`
	DefaultTaskTimeoutSeconds int               `json:"default_task_timeout_seconds" env:"X_CLAW_ORCHESTRATION_DEFAULT_TASK_TIMEOUT_SECONDS"`
	RetryLimitPerTask         int               `json:"retry_limit_per_task"         env:"X_CLAW_ORCHESTRATION_RETRY_LIMIT_PER_TASK"`
	ToolCallsParallelEnabled  bool              `json:"tool_calls_parallel_enabled"  env:"X_CLAW_ORCHESTRATION_TOOL_CALLS_PARALLEL_ENABLED"`
	MaxToolCallConcurrency    int               `json:"max_tool_call_concurrency"    env:"X_CLAW_ORCHESTRATION_MAX_TOOL_CALL_CONCURRENCY"`
	ParallelToolsMode         string            `json:"parallel_tools_mode"          env:"X_CLAW_ORCHESTRATION_PARALLEL_TOOLS_MODE"`
	ToolParallelOverrides     map[string]string `json:"tool_parallel_overrides,omitempty"`
}

type AuditConfig struct {
	Enabled             bool                  `json:"enabled"               env:"X_CLAW_AUDIT_ENABLED"`
	IntervalMinutes     int                   `json:"interval_minutes"      env:"X_CLAW_AUDIT_INTERVAL_MINUTES"`
	LookbackMinutes     int                   `json:"lookback_minutes"      env:"X_CLAW_AUDIT_LOOKBACK_MINUTES"`
	Supervisor          AuditSupervisorConfig `json:"supervisor"`
	MinConfidence       float64               `json:"min_confidence"        env:"X_CLAW_AUDIT_MIN_CONFIDENCE"`
	InconsistencyPolicy string                `json:"inconsistency_policy"  env:"X_CLAW_AUDIT_INCONSISTENCY_POLICY"`
	AutoRemediation     string                `json:"auto_remediation"      env:"X_CLAW_AUDIT_AUTO_REMEDIATION"`
	// MaxAutoRemediationsPerCycle limits how many retry/fix tasks can be spawned
	// in one audit cycle to avoid runaway loops.
	MaxAutoRemediationsPerCycle int `json:"max_auto_remediations_per_cycle" env:"X_CLAW_AUDIT_MAX_AUTO_REMEDIATIONS_PER_CYCLE"`
	// RemediationCooldownMinutes prevents re-triggering remediation for the same
	// task too frequently.
	RemediationCooldownMinutes int    `json:"remediation_cooldown_minutes"     env:"X_CLAW_AUDIT_REMEDIATION_COOLDOWN_MINUTES"`
	NotifyChannel              string `json:"notify_channel"        env:"X_CLAW_AUDIT_NOTIFY_CHANNEL"`
}

type AuditSupervisorConfig struct {
	Enabled     bool              `json:"enabled"     env:"X_CLAW_AUDIT_SUPERVISOR_ENABLED"`
	Model       *AgentModelConfig `json:"model,omitempty"`
	Temperature *float64          `json:"temperature,omitempty" env:"X_CLAW_AUDIT_SUPERVISOR_TEMPERATURE"`
	MaxTokens   int               `json:"max_tokens,omitempty"  env:"X_CLAW_AUDIT_SUPERVISOR_MAX_TOKENS"`

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
// These limits are intended to prevent runaway memory growth / OOM kills while
// keeping behavior predictable. They are "soft" in the sense that exceeding a
// limit results in a controlled, user-visible stop rather than a hard crash.
type LimitsConfig struct {
	Enabled bool `json:"enabled,omitempty" env:"X_CLAW_LIMITS_ENABLED"`

	// MaxRunWallTimeSeconds caps a single agent run (one inbound message -> one response).
	// 0 disables the wall-time budget.
	MaxRunWallTimeSeconds int `json:"max_run_wall_time_seconds,omitempty" env:"X_CLAW_LIMITS_MAX_RUN_WALL_TIME_SECONDS"`

	// MaxToolCallsPerRun caps the total number of tool calls executed in one run.
	// 0 disables the budget.
	MaxToolCallsPerRun int `json:"max_tool_calls_per_run,omitempty" env:"X_CLAW_LIMITS_MAX_TOOL_CALLS_PER_RUN"`

	// MaxToolResultChars truncates ToolResult.ForLLM/ForUser to control memory usage.
	// 0 disables truncation.
	MaxToolResultChars int `json:"max_tool_result_chars,omitempty" env:"X_CLAW_LIMITS_MAX_TOOL_RESULT_CHARS"`

	// MaxReadFileBytes limits how many bytes the read_file tool reads from disk by default.
	// 0 means use the built-in default.
	MaxReadFileBytes int `json:"max_read_file_bytes,omitempty" env:"X_CLAW_LIMITS_MAX_READ_FILE_BYTES"`
}

// AuditLogConfig controls the append-only operational audit log (JSONL).
//
// This log records major runtime events (tool executions, config reload, estop changes, etc.)
// and supports rotation to cap disk usage.
//
// This is intentionally separate from `audit` (task auditing / supervisor checks).
type AuditLogConfig struct {
	Enabled bool `json:"enabled,omitempty" env:"X_CLAW_AUDIT_LOG_ENABLED"`

	// Dir overrides the default audit log directory.
	// When empty, defaults to: <workspace>/.x-claw/audit
	Dir string `json:"dir,omitempty" env:"X_CLAW_AUDIT_LOG_DIR"`

	// MaxBytes rotates the log when it grows beyond this size.
	// 0 disables size-based rotation.
	MaxBytes int `json:"max_bytes,omitempty" env:"X_CLAW_AUDIT_LOG_MAX_BYTES"`

	// MaxBackups controls how many rotated files to keep (best-effort).
	// 0 means keep all rotated files.
	MaxBackups int `json:"max_backups,omitempty" env:"X_CLAW_AUDIT_LOG_MAX_BACKUPS"`

	// HMACKey enables per-line HMAC signatures on audit.jsonl entries.
	// When empty, signing is disabled. Prefer setting this via SecretRef (env/file)
	// to avoid embedding plaintext keys in config files.
	HMACKey SecretRef `json:"hmac_key,omitempty" env:"X_CLAW_AUDIT_LOG_HMAC_KEY"`

	// HMACKeyID is an optional identifier written into each signed line
	// to support key rotation and log forensics.
	HMACKeyID string `json:"hmac_key_id,omitempty" env:"X_CLAW_AUDIT_LOG_HMAC_KEY_ID"`
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
	Avian         ProviderConfig       `json:"avian"`
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
		p.Mistral.APIKey.IsZero() && p.Mistral.APIBase == "" &&
		p.Avian.APIKey.IsZero() && p.Avian.APIBase == ""
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
	APIKey         SecretRef `json:"api_key"                   env:"X_CLAW_PROVIDERS_{{.Name}}_API_KEY"`
	APIBase        string    `json:"api_base"                  env:"X_CLAW_PROVIDERS_{{.Name}}_API_BASE"`
	Proxy          string    `json:"proxy,omitempty"           env:"X_CLAW_PROVIDERS_{{.Name}}_PROXY"`
	RequestTimeout int       `json:"request_timeout,omitempty" env:"X_CLAW_PROVIDERS_{{.Name}}_REQUEST_TIMEOUT"`
	AuthMethod     string    `json:"auth_method,omitempty"     env:"X_CLAW_PROVIDERS_{{.Name}}_AUTH_METHOD"`
	ConnectMode    string    `json:"connect_mode,omitempty"    env:"X_CLAW_PROVIDERS_{{.Name}}_CONNECT_MODE"` // only for Github Copilot, `stdio` or `grpc`
}

type OpenAIProviderConfig struct {
	ProviderConfig
	WebSearch bool `json:"web_search" env:"X_CLAW_PROVIDERS_OPENAI_WEB_SEARCH"`
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
	ThinkingLevel  string `json:"thinking_level,omitempty"` // Extended thinking: off|low|medium|high|xhigh|adaptive
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
