package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAgentModelConfig_UnmarshalString(t *testing.T) {
	var m AgentModelConfig
	if err := json.Unmarshal([]byte(`"gpt-4"`), &m); err != nil {
		t.Fatalf("unmarshal string: %v", err)
	}
	if m.Primary != "gpt-4" {
		t.Errorf("Primary = %q, want 'gpt-4'", m.Primary)
	}
	if m.Fallbacks != nil {
		t.Errorf("Fallbacks = %v, want nil", m.Fallbacks)
	}
}

func TestAgentModelConfig_UnmarshalObject(t *testing.T) {
	var m AgentModelConfig
	data := `{"primary": "claude-opus", "fallbacks": ["gpt-4o-mini", "haiku"]}`
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		t.Fatalf("unmarshal object: %v", err)
	}
	if m.Primary != "claude-opus" {
		t.Errorf("Primary = %q, want 'claude-opus'", m.Primary)
	}
	if len(m.Fallbacks) != 2 {
		t.Fatalf("Fallbacks len = %d, want 2", len(m.Fallbacks))
	}
	if m.Fallbacks[0] != "gpt-4o-mini" || m.Fallbacks[1] != "haiku" {
		t.Errorf("Fallbacks = %v", m.Fallbacks)
	}
}

func TestAgentModelConfig_MarshalString(t *testing.T) {
	m := AgentModelConfig{Primary: "gpt-4"}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != `"gpt-4"` {
		t.Errorf("marshal = %s, want '\"gpt-4\"'", string(data))
	}
}

func TestAgentModelConfig_MarshalObject(t *testing.T) {
	m := AgentModelConfig{Primary: "claude-opus", Fallbacks: []string{"haiku"}}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var result map[string]any
	json.Unmarshal(data, &result)
	if result["primary"] != "claude-opus" {
		t.Errorf("primary = %v", result["primary"])
	}
}

func TestAgentConfig_FullParse(t *testing.T) {
	jsonData := `{
		"agents": {
			"defaults": {
				"workspace": "~/.x-claw/workspace",
				"model": "glm-4.7",
				"max_tokens": 8192,
				"max_tool_iterations": 20
			},
			"list": [
				{
					"id": "sales",
					"default": true,
					"name": "Sales Bot",
					"model": "gpt-4"
				},
				{
					"id": "support",
					"name": "Support Bot",
					"model": {
						"primary": "claude-opus",
						"fallbacks": ["haiku"]
					},
					"subagents": {
						"allow_agents": ["sales"]
					}
				}
			]
		},
			"bindings": [
				{
					"agent_id": "support",
					"match": {
						"channel": "telegram",
						"account_id": "*",
						"peer": {"kind": "direct", "id": "user123"},
						"thread_id": "42"
					}
				}
			],
		"session": {
			"dm_scope": "per-peer",
			"identity_links": {
				"john": ["telegram:123", "discord:john#1234"]
			}
		}
	}`

	cfg := DefaultConfig()
	if err := json.Unmarshal([]byte(jsonData), cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(cfg.Agents.List) != 2 {
		t.Fatalf("agents.list len = %d, want 2", len(cfg.Agents.List))
	}

	sales := cfg.Agents.List[0]
	if sales.ID != "sales" || !sales.Default || sales.Name != "Sales Bot" {
		t.Errorf("sales = %+v", sales)
	}
	if sales.Model == nil || sales.Model.Primary != "gpt-4" {
		t.Errorf("sales.Model = %+v", sales.Model)
	}

	support := cfg.Agents.List[1]
	if support.ID != "support" || support.Name != "Support Bot" {
		t.Errorf("support = %+v", support)
	}
	if support.Model == nil || support.Model.Primary != "claude-opus" {
		t.Errorf("support.Model = %+v", support.Model)
	}
	if len(support.Model.Fallbacks) != 1 || support.Model.Fallbacks[0] != "haiku" {
		t.Errorf("support.Model.Fallbacks = %v", support.Model.Fallbacks)
	}
	if support.Subagents == nil || len(support.Subagents.AllowAgents) != 1 {
		t.Errorf("support.Subagents = %+v", support.Subagents)
	}

	if len(cfg.Bindings) != 1 {
		t.Fatalf("bindings len = %d, want 1", len(cfg.Bindings))
	}
	binding := cfg.Bindings[0]
	if binding.AgentID != "support" || binding.Match.Channel != "telegram" {
		t.Errorf("binding = %+v", binding)
	}
	if binding.Match.Peer == nil || binding.Match.Peer.Kind != "direct" || binding.Match.Peer.ID != "user123" {
		t.Errorf("binding.Match.Peer = %+v", binding.Match.Peer)
	}
	if binding.Match.ThreadID != "42" {
		t.Errorf("binding.Match.ThreadID = %q, want %q", binding.Match.ThreadID, "42")
	}

	if cfg.Session.DMScope != "per-peer" {
		t.Errorf("Session.DMScope = %q", cfg.Session.DMScope)
	}
	if len(cfg.Session.IdentityLinks) != 1 {
		t.Errorf("Session.IdentityLinks = %v", cfg.Session.IdentityLinks)
	}
	links := cfg.Session.IdentityLinks["john"]
	if len(links) != 2 {
		t.Errorf("john links = %v", links)
	}
}

func TestConfig_BackwardCompat_NoAgentsList(t *testing.T) {
	jsonData := `{
		"agents": {
			"defaults": {
				"workspace": "~/.x-claw/workspace",
				"model": "glm-4.7",
				"max_tokens": 8192,
				"max_tool_iterations": 20
			}
		}
	}`

	cfg := DefaultConfig()
	if err := json.Unmarshal([]byte(jsonData), cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(cfg.Agents.List) != 0 {
		t.Errorf("agents.list should be empty for backward compat, got %d", len(cfg.Agents.List))
	}
	if len(cfg.Bindings) != 0 {
		t.Errorf("bindings should be empty, got %d", len(cfg.Bindings))
	}
}

// TestDefaultConfig_HeartbeatEnabled verifies heartbeat is enabled by default
func TestDefaultConfig_HeartbeatEnabled(t *testing.T) {
	cfg := DefaultConfig()

	if !cfg.Heartbeat.Enabled {
		t.Error("Heartbeat should be enabled by default")
	}
}

// TestDefaultConfig_WorkspacePath verifies workspace path is correctly set
func TestDefaultConfig_WorkspacePath(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.Workspace == "" {
		t.Error("Workspace should not be empty")
	}
}

// TestDefaultConfig_Model verifies model is set
func TestDefaultConfig_Model(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.Model != "" {
		t.Error("Model should be empty")
	}
}

// TestDefaultConfig_MaxTokens verifies max tokens has default value
func TestDefaultConfig_MaxTokens(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.MaxTokens == 0 {
		t.Error("MaxTokens should not be zero")
	}
}

// TestDefaultConfig_MaxToolIterations verifies max tool iterations has default value
func TestDefaultConfig_MaxToolIterations(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.MaxToolIterations == 0 {
		t.Error("MaxToolIterations should not be zero")
	}
}

// TestDefaultConfig_Temperature verifies temperature has default value
func TestDefaultConfig_Temperature(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.Temperature != nil {
		t.Error("Temperature should be nil when not provided")
	}
}

// TestDefaultConfig_Gateway verifies gateway defaults
func TestDefaultConfig_Gateway(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Gateway.Host != "127.0.0.1" {
		t.Error("Gateway host should have default value")
	}
	if cfg.Gateway.Port == 0 {
		t.Error("Gateway port should have default value")
	}
}

// TestDefaultConfig_Providers verifies provider structure
func TestDefaultConfig_Providers(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Providers.Anthropic.APIKey.Present() {
		t.Error("Anthropic API key should be empty by default")
	}
	if cfg.Providers.OpenAI.APIKey.Present() {
		t.Error("OpenAI API key should be empty by default")
	}
	if cfg.Providers.OpenRouter.APIKey.Present() {
		t.Error("OpenRouter API key should be empty by default")
	}
}

// TestDefaultConfig_Channels verifies channels are disabled by default
func TestDefaultConfig_Channels(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Channels.Telegram.Enabled {
		t.Error("Telegram should be disabled by default")
	}
	if cfg.Channels.Discord.Enabled {
		t.Error("Discord should be disabled by default")
	}
	if cfg.Channels.Slack.Enabled {
		t.Error("Slack should be disabled by default")
	}
}

// TestDefaultConfig_WebTools verifies web tools config
func TestDefaultConfig_WebTools(t *testing.T) {
	cfg := DefaultConfig()

	// Verify web tools defaults
	if cfg.Tools.Web.Brave.MaxResults != 5 {
		t.Error("Expected Brave MaxResults 5, got ", cfg.Tools.Web.Brave.MaxResults)
	}
	if cfg.Tools.Web.Brave.APIKey.Present() {
		t.Error("Brave API key should be empty by default")
	}
	if cfg.Tools.Web.DuckDuckGo.MaxResults != 5 {
		t.Error("Expected DuckDuckGo MaxResults 5, got ", cfg.Tools.Web.DuckDuckGo.MaxResults)
	}
}

func TestSaveConfig_FilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not enforced on Windows")
	}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.json")

	cfg := DefaultConfig()
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("config file has permission %04o, want 0600", perm)
	}
}

func TestSaveConfig_IncludesEmptyLegacyModelField(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.json")

	cfg := DefaultConfig()
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if !strings.Contains(string(data), `"model": ""`) {
		t.Fatalf("saved config should include empty legacy model field, got: %s", string(data))
	}
}

// TestConfig_Complete verifies all config fields are set
func TestConfig_Complete(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.Workspace == "" {
		t.Error("Workspace should not be empty")
	}
	if cfg.Agents.Defaults.Model != "" {
		t.Error("Model should be empty")
	}
	if cfg.Agents.Defaults.Temperature != nil {
		t.Error("Temperature should be nil when not provided")
	}
	if cfg.Agents.Defaults.MaxTokens == 0 {
		t.Error("MaxTokens should not be zero")
	}
	if cfg.Agents.Defaults.MaxToolIterations == 0 {
		t.Error("MaxToolIterations should not be zero")
	}
	if cfg.Gateway.Host != "127.0.0.1" {
		t.Error("Gateway host should have default value")
	}
	if cfg.Gateway.Port == 0 {
		t.Error("Gateway port should have default value")
	}
	if !cfg.Heartbeat.Enabled {
		t.Error("Heartbeat should be enabled by default")
	}
}

func TestDefaultConfig_OpenAIWebSearchEnabled(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Providers.OpenAI.WebSearch {
		t.Fatal("DefaultConfig().Providers.OpenAI.WebSearch should be true")
	}
}

func TestDefaultConfig_OrchestrationAndAuditDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Orchestration.MaxSpawnDepth != 3 {
		t.Fatalf("MaxSpawnDepth = %d, want 3", cfg.Orchestration.MaxSpawnDepth)
	}
	if cfg.Orchestration.MaxParallelWorkers != 8 {
		t.Fatalf("MaxParallelWorkers = %d, want 8", cfg.Orchestration.MaxParallelWorkers)
	}
	if cfg.Orchestration.DefaultTaskTimeoutSeconds != 180 {
		t.Fatalf(
			"DefaultTaskTimeoutSeconds = %d, want 180",
			cfg.Orchestration.DefaultTaskTimeoutSeconds,
		)
	}
	if !cfg.Orchestration.ToolCallsParallelEnabled {
		t.Fatal("ToolCallsParallelEnabled should be true by default")
	}
	if cfg.Orchestration.MaxToolCallConcurrency != 8 {
		t.Fatalf("MaxToolCallConcurrency = %d, want 8", cfg.Orchestration.MaxToolCallConcurrency)
	}
	if cfg.Orchestration.ParallelToolsMode != "read_only_only" {
		t.Fatalf("ParallelToolsMode = %q, want %q", cfg.Orchestration.ParallelToolsMode, "read_only_only")
	}
	if len(cfg.Orchestration.ToolParallelOverrides) != 0 {
		t.Fatalf("ToolParallelOverrides len = %d, want 0", len(cfg.Orchestration.ToolParallelOverrides))
	}
	if cfg.Audit.IntervalMinutes != 30 {
		t.Fatalf("Audit.IntervalMinutes = %d, want 30", cfg.Audit.IntervalMinutes)
	}
	if cfg.Audit.LookbackMinutes != 180 {
		t.Fatalf("Audit.LookbackMinutes = %d, want 180", cfg.Audit.LookbackMinutes)
	}
	if cfg.Audit.Supervisor.Model == nil || cfg.Audit.Supervisor.Model.Primary == "" {
		t.Fatal("Audit supervisor model should be initialized in defaults")
	}

	if !cfg.Limits.Enabled {
		t.Fatal("limits.enabled should be true by default")
	}
	if cfg.Limits.MaxRunWallTimeSeconds <= 0 {
		t.Fatalf("limits.max_run_wall_time_seconds = %d, want > 0", cfg.Limits.MaxRunWallTimeSeconds)
	}
	if cfg.Limits.MaxToolCallsPerRun <= 0 {
		t.Fatalf("limits.max_tool_calls_per_run = %d, want > 0", cfg.Limits.MaxToolCallsPerRun)
	}
	if cfg.Limits.MaxToolResultChars <= 0 {
		t.Fatalf("limits.max_tool_result_chars = %d, want > 0", cfg.Limits.MaxToolResultChars)
	}
	if cfg.Limits.MaxReadFileBytes <= 0 {
		t.Fatalf("limits.max_read_file_bytes = %d, want > 0", cfg.Limits.MaxReadFileBytes)
	}

	if !cfg.AuditLog.Enabled {
		t.Fatal("audit_log.enabled should be true by default")
	}
	if cfg.AuditLog.MaxBytes <= 0 {
		t.Fatalf("audit_log.max_bytes = %d, want > 0", cfg.AuditLog.MaxBytes)
	}
}

func TestConfig_UnmarshalAuditAndOrchestration(t *testing.T) {
	jsonData := `{
		"orchestration": {
			"enabled": true,
			"max_spawn_depth": 4,
			"max_parallel_workers": 2,
			"tool_calls_parallel_enabled": false,
			"max_tool_call_concurrency": 3,
			"parallel_tools_mode": "all",
			"tool_parallel_overrides": {
				"write_file": "serial_only",
				"exec": "parallel_read_only"
			}
		},
		"audit": {
			"enabled": true,
			"interval_minutes": 15,
			"lookback_minutes": 60,
			"min_confidence": 0.85,
			"supervisor": {
				"enabled": true,
				"model": {
					"primary": "gpt-5.2",
					"fallbacks": ["claude-sonnet-4.6"]
				},
				"max_tokens": 1024
			}
		}
	}`

	cfg := DefaultConfig()
	if err := json.Unmarshal([]byte(jsonData), cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !cfg.Orchestration.Enabled {
		t.Fatal("orchestration.enabled should be true")
	}
	if cfg.Orchestration.MaxSpawnDepth != 4 {
		t.Fatalf("max_spawn_depth = %d, want 4", cfg.Orchestration.MaxSpawnDepth)
	}
	if cfg.Orchestration.MaxParallelWorkers != 2 {
		t.Fatalf("max_parallel_workers = %d, want 2", cfg.Orchestration.MaxParallelWorkers)
	}
	if cfg.Orchestration.ToolCallsParallelEnabled {
		t.Fatal("tool_calls_parallel_enabled should be false")
	}
	if cfg.Orchestration.MaxToolCallConcurrency != 3 {
		t.Fatalf("max_tool_call_concurrency = %d, want 3", cfg.Orchestration.MaxToolCallConcurrency)
	}
	if cfg.Orchestration.ParallelToolsMode != "all" {
		t.Fatalf("parallel_tools_mode = %q, want %q", cfg.Orchestration.ParallelToolsMode, "all")
	}
	if cfg.Orchestration.ToolParallelOverrides["write_file"] != "serial_only" {
		t.Fatalf(
			"tool_parallel_overrides.write_file = %q, want %q",
			cfg.Orchestration.ToolParallelOverrides["write_file"], "serial_only",
		)
	}
	if cfg.Orchestration.ToolParallelOverrides["exec"] != "parallel_read_only" {
		t.Fatalf(
			"tool_parallel_overrides.exec = %q, want %q",
			cfg.Orchestration.ToolParallelOverrides["exec"], "parallel_read_only",
		)
	}
	if !cfg.Audit.Enabled {
		t.Fatal("audit.enabled should be true")
	}
	if cfg.Audit.MinConfidence != 0.85 {
		t.Fatalf("min_confidence = %v, want 0.85", cfg.Audit.MinConfidence)
	}
	if cfg.Audit.Supervisor.Model == nil || cfg.Audit.Supervisor.Model.Primary != "gpt-5.2" {
		t.Fatalf("unexpected supervisor model: %+v", cfg.Audit.Supervisor.Model)
	}
}

func TestDefaultConfig_MemoryVectorDefaults(t *testing.T) {
	cfg := DefaultConfig()
	mv := cfg.Agents.Defaults.MemoryVector

	if !mv.Enabled {
		t.Fatal("memory vector should be enabled by default")
	}
	if mv.Dimensions <= 0 {
		t.Fatal("memory vector dimensions should be > 0")
	}
	if mv.TopK <= 0 {
		t.Fatal("memory vector top_k should be > 0")
	}
	if mv.MinScore < 0 || mv.MinScore >= 1 {
		t.Fatal("memory vector min_score should be in [0,1)")
	}
}

func TestLoadConfig_OpenAIWebSearchDefaultsTrueWhenUnset(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"providers":{"openai":{"api_base":""}}}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if !cfg.Providers.OpenAI.WebSearch {
		t.Fatal("OpenAI codex web search should remain true when unset in config file")
	}
}

func TestLoadConfig_OpenAIWebSearchCanBeDisabled(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"providers":{"openai":{"web_search":false}}}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg.Providers.OpenAI.WebSearch {
		t.Fatal("OpenAI codex web search should be false when disabled in config file")
	}
}

func TestLoadConfig_WebToolsProxy(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	configJSON := `{
  "agents": {"defaults":{"workspace":"./workspace","model":"gpt4","max_tokens":8192,"max_tool_iterations":20}},
  "model_list": [{"model_name":"gpt4","model":"openai/gpt-5.2","api_key":"x"}],
  "tools": {"web":{"proxy":"http://127.0.0.1:7890"}}
}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg.Tools.Web.Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("Tools.Web.Proxy = %q, want %q", cfg.Tools.Web.Proxy, "http://127.0.0.1:7890")
	}
}

func TestLoadConfig_IgnoresEnvOverrides(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	configJSON := `{
  "agents": {"defaults":{"workspace":"./workspace","model":"gpt4","max_tokens":1234,"max_tool_iterations":20}},
  "model_list": [{"model_name":"gpt4","model":"openai/gpt-5.2","api_key":"x"}]
}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error: %v", err)
	}

	t.Setenv("X_CLAW_AGENTS_DEFAULTS_MAX_TOKENS", "9999")
	t.Setenv("X_CLAW_CHANNELS_TELEGRAM_ENABLED", "true")

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if cfg.Agents.Defaults.MaxTokens != 1234 {
		t.Fatalf("Agents.Defaults.MaxTokens = %d, want %d", cfg.Agents.Defaults.MaxTokens, 1234)
	}
	if cfg.Channels.Telegram.Enabled {
		t.Fatal("Channels.Telegram.Enabled should remain false when only enabled via env")
	}
}

// TestDefaultConfig_DMScope verifies the default dm_scope value
// TestDefaultConfig_SummarizationThresholds verifies summarization defaults
func TestDefaultConfig_SummarizationThresholds(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Agents.Defaults.SummarizeMessageThreshold != 20 {
		t.Errorf("SummarizeMessageThreshold = %d, want 20", cfg.Agents.Defaults.SummarizeMessageThreshold)
	}
	if cfg.Agents.Defaults.SummarizeTokenPercent != 75 {
		t.Errorf("SummarizeTokenPercent = %d, want 75", cfg.Agents.Defaults.SummarizeTokenPercent)
	}
}

func TestDefaultConfig_DMScope(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Session.DMScope != "per-channel-peer" {
		t.Errorf("Session.DMScope = %q, want 'per-channel-peer'", cfg.Session.DMScope)
	}
}

func TestDefaultConfig_WorkspacePath_Default(t *testing.T) {
	// Unset to ensure we test the default
	t.Setenv("X_CLAW_HOME", "")
	t.Setenv("PICOCLAW_HOME", "")
	// Set a known home for consistent test results
	t.Setenv("HOME", "/tmp/home")

	cfg := DefaultConfig()
	want := filepath.Join("/tmp/home", ".x-claw", "workspace")

	if cfg.Agents.Defaults.Workspace != want {
		t.Errorf("Default workspace path = %q, want %q", cfg.Agents.Defaults.Workspace, want)
	}
}

func TestDefaultConfig_WorkspacePath_WithXClawHome(t *testing.T) {
	t.Setenv("X_CLAW_HOME", "/custom/x-claw/home")

	cfg := DefaultConfig()
	want := "/custom/x-claw/home/workspace"

	if cfg.Agents.Defaults.Workspace != want {
		t.Errorf("Workspace path with X_CLAW_HOME = %q, want %q", cfg.Agents.Defaults.Workspace, want)
	}
}

func TestDefaultConfig_WorkspacePath_WithLegacyPicoclawHome(t *testing.T) {
	t.Setenv("X_CLAW_HOME", "")
	t.Setenv("PICOCLAW_HOME", "/custom/picoclaw/home")

	cfg := DefaultConfig()
	want := "/custom/picoclaw/home/workspace"

	if cfg.Agents.Defaults.Workspace != want {
		t.Errorf("Workspace path with PICOCLAW_HOME = %q, want %q", cfg.Agents.Defaults.Workspace, want)
	}
}

func TestLoadConfig_RejectsPublicGatewayWithoutBreakGlass(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"gateway":{"host":"0.0.0.0"}}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil {
		t.Fatal("LoadConfig() expected error for public gateway bind without break-glass")
	}
	if !strings.Contains(err.Error(), "allow_public_gateway") {
		t.Fatalf("LoadConfig() error = %q, want mention allow_public_gateway", err.Error())
	}
}

func TestLoadConfig_AllowsPublicGatewayWithBreakGlass(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	configJSON := `{
  "gateway": {"host": "0.0.0.0"},
  "security": {"break_glass": {"allow_public_gateway": true}}
}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	if _, err := LoadConfig(configPath); err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
}

func TestLoadConfig_RejectsUnsafeWorkspaceWithoutBreakGlass(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	configJSON := `{"agents":{"defaults":{"restrict_to_workspace":false}}}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil {
		t.Fatal("LoadConfig() expected error for unsafe workspace without break-glass")
	}
	if !strings.Contains(err.Error(), "allow_unsafe_workspace") {
		t.Fatalf("LoadConfig() error = %q, want mention allow_unsafe_workspace", err.Error())
	}
}

func TestLoadConfig_AllowsUnsafeWorkspaceWithBreakGlass(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	configJSON := `{
  "agents": {"defaults":{"restrict_to_workspace":false}},
  "security": {"break_glass": {"allow_unsafe_workspace": true}}
}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	if _, err := LoadConfig(configPath); err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
}

func TestLoadConfig_RejectsUnsafeExecWithoutBreakGlass(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	configJSON := `{"tools":{"exec":{"enable_deny_patterns":false}}}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil {
		t.Fatal("LoadConfig() expected error for unsafe exec without break-glass")
	}
	if !strings.Contains(err.Error(), "allow_unsafe_exec") {
		t.Fatalf("LoadConfig() error = %q, want mention allow_unsafe_exec", err.Error())
	}
}

func TestLoadConfig_AllowsUnsafeExecWithBreakGlass(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	configJSON := `{
  "tools": {"exec":{"enable_deny_patterns":false}},
  "security": {"break_glass": {"allow_unsafe_exec": true}}
}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	if _, err := LoadConfig(configPath); err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
}

func TestLoadConfig_RejectsDockerNetworkWithoutBreakGlass(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	configJSON := `{"tools":{"exec":{"backend":"docker","docker":{"network":"bridge"}}}}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil {
		t.Fatal("LoadConfig() expected error for docker network without break-glass")
	}
	if !strings.Contains(err.Error(), "allow_docker_network") {
		t.Fatalf("LoadConfig() error = %q, want mention allow_docker_network", err.Error())
	}
}

func TestLoadConfig_AllowsDockerNetworkWithBreakGlass(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	configJSON := `{
  "tools":{"exec":{"backend":"docker","docker":{"network":"bridge"}}},
  "security": {"break_glass": {"allow_docker_network": true}}
}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	if _, err := LoadConfig(configPath); err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
}

func TestLoadConfig_RejectsExecInheritEnvWithoutBreakGlass(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	configJSON := `{"tools":{"exec":{"env":{"mode":"inherit"}}}}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil {
		t.Fatal("LoadConfig() expected error for tools.exec.env.mode=inherit without break-glass")
	}
	if !strings.Contains(err.Error(), "allow_exec_inherit_env") {
		t.Fatalf("LoadConfig() error = %q, want mention allow_exec_inherit_env", err.Error())
	}
}

func TestLoadConfig_AllowsExecInheritEnvWithBreakGlass(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	configJSON := `{
  "tools":{"exec":{"env":{"mode":"inherit"}}},
  "security": {"break_glass": {"allow_exec_inherit_env": true}}
}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	if _, err := LoadConfig(configPath); err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
}
