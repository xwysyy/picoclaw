package config

type GatewayConfig struct {
	Host   string    `json:"host"    env:"X_CLAW_GATEWAY_HOST"`
	Port   int       `json:"port"    env:"X_CLAW_GATEWAY_PORT"`
	APIKey SecretRef `json:"api_key,omitempty" env:"X_CLAW_GATEWAY_API_KEY"`

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
