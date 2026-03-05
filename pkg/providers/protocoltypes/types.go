package protocoltypes

import coretypes "github.com/xwysyy/X-Claw/internal/core/provider/protocoltypes"

// This package is a thin facade for the canonical protocol types, which now
// live under internal/core. Keeping this facade preserves existing import paths
// during refactors.

type (
	ToolCall               = coretypes.ToolCall
	ExtraContent           = coretypes.ExtraContent
	GoogleExtra            = coretypes.GoogleExtra
	FunctionCall           = coretypes.FunctionCall
	LLMResponse            = coretypes.LLMResponse
	ReasoningDetail        = coretypes.ReasoningDetail
	UsageInfo              = coretypes.UsageInfo
	CacheControl           = coretypes.CacheControl
	ContentBlock           = coretypes.ContentBlock
	Message                = coretypes.Message
	ToolDefinition         = coretypes.ToolDefinition
	ToolFunctionDefinition = coretypes.ToolFunctionDefinition
)
