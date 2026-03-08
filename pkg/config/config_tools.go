package config

type ToolConfig struct {
	Enabled bool `json:"enabled" env:"ENABLED"`
}

type BraveConfig struct {
	Enabled    bool        `json:"enabled"     env:"X_CLAW_TOOLS_WEB_BRAVE_ENABLED"`
	APIKey     SecretRef   `json:"api_key"     env:"X_CLAW_TOOLS_WEB_BRAVE_API_KEY"`
	APIKeys    []SecretRef `json:"api_keys,omitempty"`
	MaxResults int         `json:"max_results" env:"X_CLAW_TOOLS_WEB_BRAVE_MAX_RESULTS"`
}

type TavilyConfig struct {
	Enabled    bool        `json:"enabled"     env:"X_CLAW_TOOLS_WEB_TAVILY_ENABLED"`
	APIKey     SecretRef   `json:"api_key"     env:"X_CLAW_TOOLS_WEB_TAVILY_API_KEY"`
	APIKeys    []SecretRef `json:"api_keys,omitempty"`
	BaseURL    string      `json:"base_url"    env:"X_CLAW_TOOLS_WEB_TAVILY_BASE_URL"`
	MaxResults int         `json:"max_results" env:"X_CLAW_TOOLS_WEB_TAVILY_MAX_RESULTS"`
}

type DuckDuckGoConfig struct {
	Enabled    bool `json:"enabled"     env:"X_CLAW_TOOLS_WEB_DUCKDUCKGO_ENABLED"`
	MaxResults int  `json:"max_results" env:"X_CLAW_TOOLS_WEB_DUCKDUCKGO_MAX_RESULTS"`
}

type GrokConfig struct {
	Enabled      bool        `json:"enabled"       env:"X_CLAW_TOOLS_WEB_GROK_ENABLED"`
	APIKey       SecretRef   `json:"api_key"       env:"X_CLAW_TOOLS_WEB_GROK_API_KEY"`
	APIKeys      []SecretRef `json:"api_keys,omitempty"`
	Endpoint     string      `json:"endpoint"      env:"X_CLAW_TOOLS_WEB_GROK_ENDPOINT"`
	DefaultModel string      `json:"default_model" env:"X_CLAW_TOOLS_WEB_GROK_DEFAULT_MODEL"`
	MaxResults   int         `json:"max_results"   env:"X_CLAW_TOOLS_WEB_GROK_MAX_RESULTS"`
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

type GLMSearchConfig struct {
	Enabled bool      `json:"enabled"  env:"X_CLAW_TOOLS_WEB_GLM_ENABLED"`
	APIKey  SecretRef `json:"api_key"  env:"X_CLAW_TOOLS_WEB_GLM_API_KEY"`
	BaseURL string    `json:"base_url" env:"X_CLAW_TOOLS_WEB_GLM_BASE_URL"`
	// SearchEngine specifies the search backend: "search_std" (default),
	// "search_pro", "search_pro_sogou", or "search_pro_quark".
	SearchEngine string `json:"search_engine" env:"X_CLAW_TOOLS_WEB_GLM_SEARCH_ENGINE"`
	MaxResults   int    `json:"max_results"   env:"X_CLAW_TOOLS_WEB_GLM_MAX_RESULTS"`
}

type PerplexityConfig struct {
	Enabled    bool      `json:"enabled"     env:"X_CLAW_TOOLS_WEB_PERPLEXITY_ENABLED"`
	APIKey     SecretRef `json:"api_key"     env:"X_CLAW_TOOLS_WEB_PERPLEXITY_API_KEY"`
	MaxResults int       `json:"max_results" env:"X_CLAW_TOOLS_WEB_PERPLEXITY_MAX_RESULTS"`
}

type WebToolsConfig struct {
	ToolConfig `envPrefix:"X_CLAW_TOOLS_WEB_"`
	Brave      BraveConfig           `json:"brave"`
	Tavily     TavilyConfig          `json:"tavily"`
	DuckDuckGo DuckDuckGoConfig      `json:"duckduckgo"`
	Perplexity PerplexityConfig      `json:"perplexity"`
	GLMSearch  GLMSearchConfig       `json:"glm_search"`
	Grok       GrokConfig            `json:"grok"`
	Evidence   WebEvidenceModeConfig `json:"evidence_mode,omitempty"`
	// Proxy is an optional proxy URL for web tools (http/https/socks5/socks5h).
	// For authenticated proxies, prefer HTTP_PROXY/HTTPS_PROXY env vars instead of embedding credentials in config.
	Proxy           string `json:"proxy,omitempty"             env:"X_CLAW_TOOLS_WEB_PROXY"`
	FetchLimitBytes int64  `json:"fetch_limit_bytes,omitempty" env:"X_CLAW_TOOLS_WEB_FETCH_LIMIT_BYTES"`

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
	ToolConfig         `    envPrefix:"X_CLAW_TOOLS_CRON_"`
	ExecTimeoutMinutes int `                                 env:"X_CLAW_TOOLS_CRON_EXEC_TIMEOUT_MINUTES" json:"exec_timeout_minutes"` // 0 means no timeout
}

// ToolTraceConfig controls on-disk tracing of tool calls for debugging and replay.
//
// When enabled, X-Claw appends an event stream (JSONL) and (optionally) writes
// per-call snapshots (JSON/Markdown) under the agent workspace.
type ToolTraceConfig struct {
	Enabled bool `json:"enabled" env:"X_CLAW_TOOLS_TRACE_ENABLED"`
	// Dir overrides the default trace directory.
	// When empty, traces are written under: <workspace>/.x-claw/audit/tools/<session>/
	Dir string `json:"dir,omitempty" env:"X_CLAW_TOOLS_TRACE_DIR"`

	// WritePerCallFiles controls writing one JSON + one Markdown file per tool call.
	WritePerCallFiles bool `json:"write_per_call_files" env:"X_CLAW_TOOLS_TRACE_WRITE_PER_CALL_FILES"`

	// MaxArgPreviewChars controls args_preview truncation in the JSONL event stream.
	MaxArgPreviewChars int `json:"max_arg_preview_chars" env:"X_CLAW_TOOLS_TRACE_MAX_ARG_PREVIEW_CHARS"`
	// MaxResultPreviewChars controls output previews truncation in the JSONL event stream.
	MaxResultPreviewChars int `json:"max_result_preview_chars" env:"X_CLAW_TOOLS_TRACE_MAX_RESULT_PREVIEW_CHARS"`
}

// ToolErrorTemplateConfig controls executor-level tool error wrapping for the LLM.
//
// When enabled, tool failures are wrapped into a structured JSON payload with
// minimal recovery hints. This helps the model self-correct by adjusting args
// or switching tools, without changing each individual tool implementation.
type ToolErrorTemplateConfig struct {
	Enabled bool `json:"enabled" env:"X_CLAW_TOOLS_ERROR_TEMPLATE_ENABLED"`

	// IncludeSchema adds a small summary of tool parameters (required + known keys)
	// when the tool definition is available.
	IncludeSchema bool `json:"include_schema" env:"X_CLAW_TOOLS_ERROR_TEMPLATE_INCLUDE_SCHEMA"`
}

type ExecConfig struct {
	ToolConfig `envPrefix:"X_CLAW_TOOLS_EXEC_"`

	EnableDenyPatterns  bool     `json:"enable_deny_patterns"  env:"X_CLAW_TOOLS_EXEC_ENABLE_DENY_PATTERNS"`
	CustomDenyPatterns  []string `json:"custom_deny_patterns"  env:"X_CLAW_TOOLS_EXEC_CUSTOM_DENY_PATTERNS"`
	CustomAllowPatterns []string `json:"custom_allow_patterns" env:"X_CLAW_TOOLS_EXEC_CUSTOM_ALLOW_PATTERNS"`

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
	Mode string `json:"mode,omitempty" env:"X_CLAW_TOOLS_EXEC_ENV_MODE"`

	// EnvAllow is the allow-list of environment variable names (exact match).
	EnvAllow []string `json:"allow,omitempty" env:"X_CLAW_TOOLS_EXEC_ENV_ALLOW"`
}

// ExecHostLimitsConfig controls optional OS-level hard limits for host exec tool backend.
// All fields are optional; zero/negative values disable that limit.
type ExecHostLimitsConfig struct {
	// MemoryMB maps to ulimit -v (virtual memory) on Unix. It is approximate.
	MemoryMB int `json:"memory_mb,omitempty" env:"X_CLAW_TOOLS_EXEC_HOST_MEMORY_MB"`
	// CPUSeconds maps to ulimit -t (CPU time) on Unix.
	CPUSeconds int `json:"cpu_seconds,omitempty" env:"X_CLAW_TOOLS_EXEC_HOST_CPU_SECONDS"`
	// FileSizeMB maps to ulimit -f (max file size) on Unix.
	FileSizeMB int `json:"file_size_mb,omitempty" env:"X_CLAW_TOOLS_EXEC_HOST_FILE_SIZE_MB"`
	// NProc maps to ulimit -u (max user processes) on Unix.
	NProc int `json:"nproc,omitempty" env:"X_CLAW_TOOLS_EXEC_HOST_NPROC"`
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
	MemoryMB int `json:"memory_mb,omitempty" env:"X_CLAW_TOOLS_EXEC_DOCKER_MEMORY_MB"`

	// CPUs maps to docker run --cpus (e.g. 0.5). 0 disables the limit.
	CPUs float64 `json:"cpus,omitempty" env:"X_CLAW_TOOLS_EXEC_DOCKER_CPUS"`

	// PidsLimit maps to docker run --pids-limit. 0 disables the limit.
	PidsLimit int `json:"pids_limit,omitempty" env:"X_CLAW_TOOLS_EXEC_DOCKER_PIDS_LIMIT"`
}

// EstopConfig enables a global kill switch for tool execution (Policy-first Tools).
//
// ROADMAP.md:1138 - estop subsystem as a durable safety control plane.
// When enabled, X-Claw reads <workspace>/.x-claw/state/estop.json and denies
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
	ToolConfig `    envPrefix:"X_CLAW_MEDIA_CLEANUP_"`
	MaxAge     int `                                    env:"X_CLAW_MEDIA_CLEANUP_MAX_AGE"  json:"max_age_minutes"`
	Interval   int `                                    env:"X_CLAW_MEDIA_CLEANUP_INTERVAL" json:"interval_minutes"`
}

type ToolsConfig struct {
	AllowReadPaths  []string `json:"allow_read_paths"  env:"X_CLAW_TOOLS_ALLOW_READ_PATHS"`
	AllowWritePaths []string `json:"allow_write_paths" env:"X_CLAW_TOOLS_ALLOW_WRITE_PATHS"`

	Web          WebToolsConfig     `json:"web"`
	Cron         CronToolsConfig    `json:"cron"`
	Exec         ExecConfig         `json:"exec"`
	Skills       SkillsToolsConfig  `json:"skills"`
	MediaCleanup MediaCleanupConfig `json:"media_cleanup"`
	MCP          MCPConfig          `json:"mcp,omitempty"`

	// Governance / safety controls (policy-first tools).
	Policy        ToolPolicyConfig        `json:"policy,omitempty"`
	Hooks         ToolHooksConfig         `json:"hooks,omitempty"`
	PlanMode      PlanModeConfig          `json:"plan_mode,omitempty"`
	Estop         EstopConfig             `json:"estop,omitempty"`
	Trace         ToolTraceConfig         `json:"trace,omitempty"`
	ErrorTemplate ToolErrorTemplateConfig `json:"error_template,omitempty"`

	// Tool-level toggles (coarse enable/disable per tool name).
	AppendFile ToolConfig `json:"append_file,omitempty"   envPrefix:"X_CLAW_TOOLS_APPEND_FILE_"`
	EditFile   ToolConfig `json:"edit_file,omitempty"     envPrefix:"X_CLAW_TOOLS_EDIT_FILE_"`
	I2C        ToolConfig `json:"i2c,omitempty"           envPrefix:"X_CLAW_TOOLS_I2C_"`
	ListDir    ToolConfig `json:"list_dir,omitempty"      envPrefix:"X_CLAW_TOOLS_LIST_DIR_"`
	Message    ToolConfig `json:"message,omitempty"       envPrefix:"X_CLAW_TOOLS_MESSAGE_"`
	ReadFile   ToolConfig `json:"read_file,omitempty"     envPrefix:"X_CLAW_TOOLS_READ_FILE_"`
	SendFile   ToolConfig `json:"send_file,omitempty"     envPrefix:"X_CLAW_TOOLS_SEND_FILE_"`
	SPI        ToolConfig `json:"spi,omitempty"           envPrefix:"X_CLAW_TOOLS_SPI_"`
	WebFetch   ToolConfig `json:"web_fetch,omitempty"     envPrefix:"X_CLAW_TOOLS_WEB_FETCH_"`
	WriteFile  ToolConfig `json:"write_file,omitempty"    envPrefix:"X_CLAW_TOOLS_WRITE_FILE_"`
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

// ToolHooksConfig enables lightweight tool call hooks/extensions.
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
	ToolConfig `envPrefix:"X_CLAW_TOOLS_SKILLS_"`

	Registries            SkillsRegistriesConfig `json:"registries"`
	MaxConcurrentSearches int                    `json:"max_concurrent_searches" env:"X_CLAW_TOOLS_SKILLS_MAX_CONCURRENT_SEARCHES"`
	SearchCache           SearchCacheConfig      `json:"search_cache"`
}

type SearchCacheConfig struct {
	MaxSize    int `json:"max_size"    env:"X_CLAW_SKILLS_SEARCH_CACHE_MAX_SIZE"`
	TTLSeconds int `json:"ttl_seconds" env:"X_CLAW_SKILLS_SEARCH_CACHE_TTL_SECONDS"`
}

type SkillsRegistriesConfig struct {
	ClawHub ClawHubRegistryConfig `json:"clawhub"`
}

type ClawHubRegistryConfig struct {
	Enabled         bool      `json:"enabled"           env:"X_CLAW_SKILLS_REGISTRIES_CLAWHUB_ENABLED"`
	BaseURL         string    `json:"base_url"          env:"X_CLAW_SKILLS_REGISTRIES_CLAWHUB_BASE_URL"`
	AuthToken       SecretRef `json:"auth_token"        env:"X_CLAW_SKILLS_REGISTRIES_CLAWHUB_AUTH_TOKEN"`
	SearchPath      string    `json:"search_path"       env:"X_CLAW_SKILLS_REGISTRIES_CLAWHUB_SEARCH_PATH"`
	SkillsPath      string    `json:"skills_path"       env:"X_CLAW_SKILLS_REGISTRIES_CLAWHUB_SKILLS_PATH"`
	DownloadPath    string    `json:"download_path"     env:"X_CLAW_SKILLS_REGISTRIES_CLAWHUB_DOWNLOAD_PATH"`
	Timeout         int       `json:"timeout"           env:"X_CLAW_SKILLS_REGISTRIES_CLAWHUB_TIMEOUT"`
	MaxZipSize      int       `json:"max_zip_size"      env:"X_CLAW_SKILLS_REGISTRIES_CLAWHUB_MAX_ZIP_SIZE"`
	MaxResponseSize int       `json:"max_response_size" env:"X_CLAW_SKILLS_REGISTRIES_CLAWHUB_MAX_RESPONSE_SIZE"`
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
	ToolConfig `envPrefix:"X_CLAW_TOOLS_MCP_"`
	// Servers is a map of server name to server configuration
	Servers map[string]MCPServerConfig `json:"servers,omitempty"`
}
