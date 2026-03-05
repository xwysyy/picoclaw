package provider

import (
	"context"

	"github.com/xwysyy/X-Claw/internal/core/provider/protocoltypes"
)

// LLMProvider is the core port for a model provider.
//
// Keep this interface small and stable. Higher-level features (fallback chains,
// retry policies, error classification) should wrap this interface from outside
// the core boundary.
type LLMProvider interface {
	Chat(
		ctx context.Context,
		messages []protocoltypes.Message,
		tools []protocoltypes.ToolDefinition,
		model string,
		options map[string]any,
	) (*protocoltypes.LLMResponse, error)

	GetDefaultModel() string
}

// StatefulProvider is an optional interface for providers that need cleanup.
type StatefulProvider interface {
	LLMProvider
	Close()
}
