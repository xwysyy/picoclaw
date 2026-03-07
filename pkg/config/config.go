package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
)

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
