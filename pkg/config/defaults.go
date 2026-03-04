// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package config

import (
	"os"
	"path/filepath"
)

// DefaultConfig returns the default configuration for PicoClaw.
func DefaultConfig() *Config {
	// Determine the base path for the workspace.
	// Priority: $PICOCLAW_HOME > ~/.picoclaw
	var homePath string
	if picoclawHome := os.Getenv("PICOCLAW_HOME"); picoclawHome != "" {
		homePath = picoclawHome
	} else {
		userHome, _ := os.UserHomeDir()
		homePath = filepath.Join(userHome, ".picoclaw")
	}
	workspacePath := filepath.Join(homePath, "workspace")

	return &Config{
		Agents: AgentsConfig{
			Defaults: AgentDefaults{
				Workspace:           workspacePath,
				RestrictToWorkspace: true,
				Provider:            "",
				Model:               "",
				SessionModelAutoDowngrade: SessionModelAutoDowngradeConfig{
					Enabled:       false,
					Threshold:     2,
					WindowMinutes: 15,
					TTLMinutes:    60,
				},
				MaxTokens:         32768,
				Temperature:       nil, // nil means use provider default
				MaxToolIterations: 50,
				Compaction: AgentCompactionConfig{
					Mode:             "safeguard",
					ReserveTokens:    2048,
					KeepRecentTokens: 2048,
					MaxHistoryShare:  0.5,
					MemoryFlush: AgentCompactionMemoryFlushConfig{
						Enabled:             true,
						SoftThresholdTokens: 1500,
					},
				},
				ContextPruning: AgentContextPruningConfig{
					Mode:                "tools_only",
					IncludeOldChitChat:  true,
					SoftToolResultChars: 2000,
					HardToolResultChars: 350,
					TriggerRatio:        0.8,
				},
				BootstrapSnapshot: AgentBootstrapSnapshotConfig{
					Enabled: true,
				},
				MemoryVector: AgentMemoryVectorConfig{
					Enabled:         true,
					Dimensions:      256,
					TopK:            6,
					MinScore:        0.15,
					MaxContextChars: 1800,
					RecentDailyDays: 14,
					Hybrid: AgentMemoryHybridConfig{
						FTSWeight:    0.6,
						VectorWeight: 0.4,
					},
				},
			},
		},
		Bindings: []AgentBinding{},
		Session: SessionConfig{
			DMScope: "per-channel-peer",
		},
		Channels: ChannelsConfig{
			WhatsApp: WhatsAppConfig{
				Enabled:          false,
				BridgeURL:        "ws://localhost:3001",
				UseNative:        false,
				SessionStorePath: "",
				AllowFrom:        FlexibleStringSlice{},
			},
			Telegram: TelegramConfig{
				Enabled:   false,
				Token:     "",
				AllowFrom: FlexibleStringSlice{},
				GroupTrigger: GroupTriggerConfig{
					CommandBypass:   true,
					CommandPrefixes: []string{"/"},
				},
				Typing: TypingConfig{Enabled: true},
				Placeholder: PlaceholderConfig{
					Enabled: true,
					Text:    "Thinking... 💭",
					DelayMS: 2500,
				},
			},
			Feishu: FeishuConfig{
				Enabled:           false,
				AppID:             "",
				AppSecret:         "",
				EncryptKey:        "",
				VerificationToken: "",
				AllowFrom:         FlexibleStringSlice{},
				GroupTrigger: GroupTriggerConfig{
					// Safe-by-default in groups: require @mention.
					MentionOnly: true,
					// Allow slash-commands without @mention to keep ops usable.
					CommandBypass:   true,
					CommandPrefixes: []string{"/"},
					Mentionless:     false,
					Prefixes:        []string{},
				},
				Typing:      TypingConfig{Enabled: false},
				Placeholder: PlaceholderConfig{Enabled: false, DelayMS: 2500},
			},
			Discord: DiscordConfig{
				Enabled:     false,
				Token:       "",
				AllowFrom:   FlexibleStringSlice{},
				MentionOnly: false,
				Placeholder: PlaceholderConfig{Enabled: false, DelayMS: 2500},
			},
			QQ: QQConfig{
				Enabled:   false,
				AppID:     "",
				AppSecret: "",
				AllowFrom: FlexibleStringSlice{},
			},
			DingTalk: DingTalkConfig{
				Enabled:      false,
				ClientID:     "",
				ClientSecret: "",
				AllowFrom:    FlexibleStringSlice{},
			},
			Slack: SlackConfig{
				Enabled:   false,
				BotToken:  "",
				AppToken:  "",
				AllowFrom: FlexibleStringSlice{},
				Placeholder: PlaceholderConfig{
					Enabled: false,
					DelayMS: 2500,
				},
			},
			LINE: LINEConfig{
				Enabled:            false,
				ChannelSecret:      "",
				ChannelAccessToken: "",
				WebhookHost:        "0.0.0.0",
				WebhookPort:        18791,
				WebhookPath:        "/webhook/line",
				AllowFrom:          FlexibleStringSlice{},
				GroupTrigger:       GroupTriggerConfig{MentionOnly: true},
				Placeholder:        PlaceholderConfig{Enabled: false, DelayMS: 2500},
			},
			OneBot: OneBotConfig{
				Enabled:            false,
				WSUrl:              "ws://127.0.0.1:3001",
				AccessToken:        "",
				ReconnectInterval:  5,
				GroupTriggerPrefix: []string{},
				AllowFrom:          FlexibleStringSlice{},
				Placeholder:        PlaceholderConfig{Enabled: false, DelayMS: 2500},
			},
			WeCom: WeComConfig{
				Enabled:        false,
				Token:          "",
				EncodingAESKey: "",
				WebhookURL:     "",
				WebhookHost:    "0.0.0.0",
				WebhookPort:    18793,
				WebhookPath:    "/webhook/wecom",
				AllowFrom:      FlexibleStringSlice{},
				ReplyTimeout:   5,
			},
			WeComApp: WeComAppConfig{
				Enabled:        false,
				CorpID:         "",
				CorpSecret:     "",
				AgentID:        0,
				Token:          "",
				EncodingAESKey: "",
				WebhookHost:    "0.0.0.0",
				WebhookPort:    18792,
				WebhookPath:    "/webhook/wecom-app",
				AllowFrom:      FlexibleStringSlice{},
				ReplyTimeout:   5,
			},
			WeComAIBot: WeComAIBotConfig{
				Enabled:        false,
				Token:          "",
				EncodingAESKey: "",
				WebhookPath:    "/webhook/wecom-aibot",
				AllowFrom:      FlexibleStringSlice{},
				ReplyTimeout:   5,
				MaxSteps:       10,
				WelcomeMessage: "Hello! I'm your AI assistant. How can I help you today?",
			},
			Pico: PicoConfig{
				Enabled:        false,
				Token:          "",
				PingInterval:   30,
				ReadTimeout:    60,
				WriteTimeout:   10,
				MaxConnections: 100,
				AllowFrom:      FlexibleStringSlice{},
				Placeholder:    PlaceholderConfig{Enabled: false, DelayMS: 2500},
			},
		},
		Providers: ProvidersConfig{
			OpenAI: OpenAIProviderConfig{WebSearch: true},
		},
		ModelList: []ModelConfig{
			// ============================================
			// Add your API key to the model you want to use
			// ============================================

			// Zhipu AI (智谱) - https://open.bigmodel.cn/usercenter/apikeys
			{
				ModelName: "glm-4.7",
				Model:     "zhipu/glm-4.7",
				APIBase:   "https://open.bigmodel.cn/api/paas/v4",
				APIKey:    "",
			},

			// OpenAI - https://platform.openai.com/api-keys
			{
				ModelName: "gpt-5.2",
				Model:     "openai/gpt-5.2",
				APIBase:   "https://api.openai.com/v1",
				APIKey:    "",
			},

			// Anthropic Claude - https://console.anthropic.com/settings/keys
			{
				ModelName: "claude-sonnet-4.6",
				Model:     "anthropic/claude-sonnet-4.6",
				APIBase:   "https://api.anthropic.com/v1",
				APIKey:    "",
			},

			// DeepSeek - https://platform.deepseek.com/
			{
				ModelName: "deepseek-chat",
				Model:     "deepseek/deepseek-chat",
				APIBase:   "https://api.deepseek.com/v1",
				APIKey:    "",
			},

			// Google Gemini - https://ai.google.dev/
			{
				ModelName: "gemini-2.0-flash",
				Model:     "gemini/gemini-2.0-flash-exp",
				APIBase:   "https://generativelanguage.googleapis.com/v1beta",
				APIKey:    "",
			},

			// Qwen (通义千问) - https://dashscope.console.aliyun.com/apiKey
			{
				ModelName: "qwen-plus",
				Model:     "qwen/qwen-plus",
				APIBase:   "https://dashscope.aliyuncs.com/compatible-mode/v1",
				APIKey:    "",
			},

			// Moonshot (月之暗面) - https://platform.moonshot.cn/console/api-keys
			{
				ModelName: "moonshot-v1-8k",
				Model:     "moonshot/moonshot-v1-8k",
				APIBase:   "https://api.moonshot.cn/v1",
				APIKey:    "",
			},

			// Groq - https://console.groq.com/keys
			{
				ModelName: "llama-3.3-70b",
				Model:     "groq/llama-3.3-70b-versatile",
				APIBase:   "https://api.groq.com/openai/v1",
				APIKey:    "",
			},

			// OpenRouter (100+ models) - https://openrouter.ai/keys
			{
				ModelName: "openrouter-auto",
				Model:     "openrouter/auto",
				APIBase:   "https://openrouter.ai/api/v1",
				APIKey:    "",
			},
			{
				ModelName: "openrouter-gpt-5.2",
				Model:     "openrouter/openai/gpt-5.2",
				APIBase:   "https://openrouter.ai/api/v1",
				APIKey:    "",
			},

			// NVIDIA - https://build.nvidia.com/
			{
				ModelName: "nemotron-4-340b",
				Model:     "nvidia/nemotron-4-340b-instruct",
				APIBase:   "https://integrate.api.nvidia.com/v1",
				APIKey:    "",
			},

			// Cerebras - https://inference.cerebras.ai/
			{
				ModelName: "cerebras-llama-3.3-70b",
				Model:     "cerebras/llama-3.3-70b",
				APIBase:   "https://api.cerebras.ai/v1",
				APIKey:    "",
			},

			// Volcengine (火山引擎) - https://console.volcengine.com/ark
			{
				ModelName: "doubao-pro",
				Model:     "volcengine/doubao-pro-32k",
				APIBase:   "https://ark.cn-beijing.volces.com/api/v3",
				APIKey:    "",
			},

			// ShengsuanYun (神算云)
			{
				ModelName: "deepseek-v3",
				Model:     "shengsuanyun/deepseek-v3",
				APIBase:   "https://api.shengsuanyun.com/v1",
				APIKey:    "",
			},

			// Antigravity (Google Cloud Code Assist) - OAuth only
			{
				ModelName:  "gemini-flash",
				Model:      "antigravity/gemini-3-flash",
				AuthMethod: "oauth",
			},

			// GitHub Copilot - https://github.com/settings/tokens
			{
				ModelName:  "copilot-gpt-5.2",
				Model:      "github-copilot/gpt-5.2",
				APIBase:    "http://localhost:4321",
				AuthMethod: "oauth",
			},

			// Ollama (local) - https://ollama.com
			{
				ModelName: "llama3",
				Model:     "ollama/llama3",
				APIBase:   "http://localhost:11434/v1",
				APIKey:    "ollama",
			},

			// Mistral AI - https://console.mistral.ai/api-keys
			{
				ModelName: "mistral-small",
				Model:     "mistral/mistral-small-latest",
				APIBase:   "https://api.mistral.ai/v1",
				APIKey:    "",
			},

			// VLLM (local) - http://localhost:8000
			{
				ModelName: "local-model",
				Model:     "vllm/custom-model",
				APIBase:   "http://localhost:8000/v1",
				APIKey:    "",
			},
		},
		Gateway: GatewayConfig{
			Host:   "127.0.0.1",
			Port:   18790,
			APIKey: "",
			InboundQueue: GatewayInboundQueueConfig{
				Enabled:          true,
				MaxConcurrency:   4,
				PerSessionBuffer: 32,
			},
			Reload: GatewayReloadConfig{
				Enabled:         true,
				Watch:           false,
				IntervalSeconds: 2,
			},
		},
		Notify: NotifyConfig{
			OnTaskComplete: false,
		},
		Tools: ToolsConfig{
			Policy: ToolPolicyConfig{
				Enabled: false,
				// Conservative defaults: no allow/deny restrictions, no forced timeouts.
				// Enable and tune in config.json when running in production.
				TimeoutMS: 0,
				Redact: ToolPolicyRedactConfig{
					Enabled:     true,
					ApplyToLLM:  false,
					ApplyToUser: false,
					JSONFields: []string{
						"api_key", "apikey",
						"token", "access_token", "refresh_token",
						"secret", "password",
						"authorization", "cookie",
					},
					Patterns: []string{
						`(?i)(authorization\s*:\s*)(bearer\s+)[^\s]+`,
						`(?i)(api[_-]?key\s*[:=]\s*)[^\s]+`,
						`(?i)(access[_-]?token\s*[:=]\s*)[^\s]+`,
						`(?i)(refresh[_-]?token\s*[:=]\s*)[^\s]+`,
						`\bsk-[A-Za-z0-9]{16,}\b`,
					},
				},
				Confirm: ToolPolicyConfirmConfig{
					Enabled:        false,
					Mode:           "resume_only",
					ExpiresSeconds: 15 * 60,
					Tools: []string{
						"exec",
						"write_file",
						"edit_file",
						"append_file",
					},
					ToolPrefixes: []string{"mcp_"},
				},
				Idempotency: ToolPolicyIdempotencyConfig{
					Enabled:     true,
					CacheResult: true,
					Tools: []string{
						"exec",
						"write_file",
						"edit_file",
						"append_file",
					},
					ToolPrefixes: []string{"mcp_"},
				},
				Audit: ToolPolicyAuditConfig{
					Tags: map[string]string{},
				},
			},
			Hooks: ToolHooksConfig{
				Enabled: true,
				Redact: ToolPolicyRedactConfig{
					Enabled:     true,
					ApplyToLLM:  true,
					ApplyToUser: false,
					JSONFields: []string{
						"api_key", "apikey",
						"token", "access_token", "refresh_token",
						"secret", "password",
						"authorization", "cookie",
					},
					Patterns: []string{
						`(?i)(authorization\s*:\s*)(bearer\s+)[^\s]+`,
						`(?i)(api[_-]?key\s*[:=]\s*)[^\s]+`,
						`(?i)(access[_-]?token\s*[:=]\s*)[^\s]+`,
						`(?i)(refresh[_-]?token\s*[:=]\s*)[^\s]+`,
						`\bsk-[A-Za-z0-9]{16,}\b`,
					},
				},
			},
			PlanMode: PlanModeConfig{
				Enabled:     true,
				DefaultMode: "run",
				// Group chats are less trusted by default: require explicit user approval
				// (e.g. /switch plan to run) before side-effect tools are allowed.
				DefaultModeGroup: "plan",
				RestrictedTools: []string{
					"exec",
					"write_file",
					"edit_file",
					"append_file",
				},
				RestrictedPrefixes: []string{},
			},
			Estop: EstopConfig{
				Enabled:    true,
				FailClosed: true,
			},
			MediaCleanup: MediaCleanupConfig{
				Enabled:  true,
				MaxAge:   30,
				Interval: 5,
			},
			Web: WebToolsConfig{
				Proxy:           "",
				FetchLimitBytes: 10 * 1024 * 1024, // 10MB by default
				FetchCache: WebFetchCacheConfig{
					Enabled:       true,
					TTLSeconds:    120,
					MaxEntries:    32,
					MaxEntryChars: 80_000,
				},
				Brave: BraveConfig{
					Enabled:    false,
					APIKey:     "",
					MaxResults: 5,
				},
				DuckDuckGo: DuckDuckGoConfig{
					Enabled:    true,
					MaxResults: 5,
				},
				Evidence: WebEvidenceModeConfig{
					Enabled:    false,
					MinDomains: 2,
				},
				Grok: GrokConfig{
					Enabled:      false,
					APIKey:       "",
					Endpoint:     "https://api.x.ai/v1/chat/completions",
					DefaultModel: "grok-4",
					MaxResults:   5,
				},
			},
			Trace: ToolTraceConfig{
				Enabled:               false,
				Dir:                   "",
				WritePerCallFiles:     true,
				MaxArgPreviewChars:    200,
				MaxResultPreviewChars: 400,
			},
			ErrorTemplate: ToolErrorTemplateConfig{
				Enabled:       true,
				IncludeSchema: true,
			},
			Cron: CronToolsConfig{
				ExecTimeoutMinutes: 5,
			},
			Exec: ExecConfig{
				EnableDenyPatterns: true,
				Backend:            "host",
				Env: ExecEnvConfig{
					Mode: "allowlist",
					EnvAllow: []string{
						// Minimal, non-secret defaults.
						"PATH",
						"HOME",
						"USER",
						"LOGNAME",
						"SHELL",
						"LANG",
						"LC_ALL",
						"LC_CTYPE",
						"TERM",
						"TZ",
						"TMPDIR",
						// Proxy support for package installs/downloads.
						"HTTP_PROXY",
						"HTTPS_PROXY",
						"NO_PROXY",
						"http_proxy",
						"https_proxy",
						"no_proxy",
					},
				},
				Docker: ExecDockerConfig{
					Image:          "alpine:3.20",
					Network:        "none",
					ReadOnlyRootFS: true,
				},
			},
			Skills: SkillsToolsConfig{
				Registries: SkillsRegistriesConfig{
					ClawHub: ClawHubRegistryConfig{
						Enabled: true,
						BaseURL: "https://clawhub.ai",
					},
				},
				MaxConcurrentSearches: 2,
				SearchCache: SearchCacheConfig{
					MaxSize:    50,
					TTLSeconds: 300,
				},
			},
			MCP: MCPConfig{
				Enabled: false,
				Servers: map[string]MCPServerConfig{},
			},
		},
		Heartbeat: HeartbeatConfig{
			Enabled:  true,
			Interval: 5,
		},
		Orchestration: OrchestrationConfig{
			Enabled:                   false,
			MaxSpawnDepth:             3,
			MaxParallelWorkers:        8,
			MaxTasksPerAgent:          20,
			DefaultTaskTimeoutSeconds: 180,
			RetryLimitPerTask:         2,
			ToolCallsParallelEnabled:  true,
			MaxToolCallConcurrency:    8,
			ParallelToolsMode:         "read_only_only",
			ToolParallelOverrides:     map[string]string{},
		},
		Limits: LimitsConfig{
			Enabled:               true,
			MaxRunWallTimeSeconds: 300,
			MaxToolCallsPerRun:    80,
			MaxToolResultChars:    30000,
			MaxReadFileBytes:      30000,
		},
		AuditLog: AuditLogConfig{
			Enabled:    true,
			Dir:        "",
			MaxBytes:   5 * 1024 * 1024,
			MaxBackups: 10,
		},
		Audit: AuditConfig{
			Enabled:                     false,
			IntervalMinutes:             30,
			LookbackMinutes:             180,
			MinConfidence:               0.75,
			InconsistencyPolicy:         "strict",
			AutoRemediation:             "safe_only",
			MaxAutoRemediationsPerCycle: 3,
			RemediationCooldownMinutes:  10,
			RemediationAgentID:          "",
			NotifyChannel:               "last_active",
			Supervisor: AuditSupervisorConfig{
				Enabled: false,
				Model: &AgentModelConfig{
					Primary:   "gpt-5.2",
					Fallbacks: []string{"claude-sonnet-4.6"},
				},
				MaxTokens: 2048,
				Mode:      "escalate",
				MaxTasks:  5,
			},
		},
	}
}
