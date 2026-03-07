package config

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

type SecurityConfig struct {
	// BreakGlass gates unsafe configuration states behind an explicit boolean.
	// This makes "unsafe but intentional" deployments auditable and prevents
	// accidental exposure from simple config edits.
	BreakGlass BreakGlassConfig `json:"break_glass,omitempty"`
}

type BreakGlassConfig struct {
	// AllowPublicGateway acknowledges that gateway.host binds to a non-loopback address
	// (e.g., 0.0.0.0 or a LAN IP). When false, such configs are rejected.
	AllowPublicGateway bool `json:"allow_public_gateway,omitempty" env:"X_CLAW_SECURITY_BREAK_GLASS_ALLOW_PUBLIC_GATEWAY"`

	// AllowUnsafeWorkspace disables workspace-only filesystem restrictions. This is high risk:
	// tools can read/write arbitrary host paths if other guards are also loosened.
	AllowUnsafeWorkspace bool `json:"allow_unsafe_workspace,omitempty" env:"X_CLAW_SECURITY_BREAK_GLASS_ALLOW_UNSAFE_WORKSPACE"`

	// AllowUnsafeExec acknowledges disabling deny patterns for the exec tool.
	AllowUnsafeExec bool `json:"allow_unsafe_exec,omitempty" env:"X_CLAW_SECURITY_BREAK_GLASS_ALLOW_UNSAFE_EXEC"`

	// AllowExecInheritEnv acknowledges passing the full host environment into exec tool commands.
	// Prefer tools.exec.env.mode="allowlist" to avoid leaking host secrets into subprocesses.
	AllowExecInheritEnv bool `json:"allow_exec_inherit_env,omitempty" env:"X_CLAW_SECURITY_BREAK_GLASS_ALLOW_EXEC_INHERIT_ENV"`

	// AllowDockerNetwork acknowledges enabling networking for the exec docker sandbox
	// (tools.exec.docker.network != "none").
	AllowDockerNetwork bool `json:"allow_docker_network,omitempty" env:"X_CLAW_SECURITY_BREAK_GLASS_ALLOW_DOCKER_NETWORK"`
}
