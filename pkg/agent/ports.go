package agent

import "github.com/xwysyy/X-Claw/internal/core/ports"

// Type aliases for core ports. This keeps existing agent APIs readable while
// moving the canonical interface definitions into internal/core.
type (
	ChannelDirectory = ports.ChannelDirectory
	MediaResolver    = ports.MediaResolver
	MediaMeta        = ports.MediaMeta
)
