package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/constants"
	"github.com/xwysyy/X-Claw/pkg/logger"
	"github.com/xwysyy/X-Claw/pkg/providers"
	"github.com/xwysyy/X-Claw/pkg/utils"
)

// maybeSummarize triggers summarization if the session history exceeds thresholds.
func (al *AgentLoop) maybeSummarize(agent *AgentInstance, sessionKey, channel, chatID string) {
	newHistory := agent.Sessions.GetHistory(sessionKey)
	tokenEstimate := al.estimateTokens(newHistory)
	threshold := agent.ContextWindow * 75 / 100

	if len(newHistory) > 100 || tokenEstimate > threshold {
		summarizeKey := agent.ID + ":" + sessionKey
		if _, loading := al.summarizing.LoadOrStore(summarizeKey, true); !loading {
			go func() {
				defer al.summarizing.Delete(summarizeKey)
				logger.Debug("Memory threshold reached. Optimizing conversation history...")
				ctx, cancel := al.safeCompactionContext()
				defer cancel()

				if agent != nil && agent.Compaction.NotifyUser && al.bus != nil && channel != "" && chatID != "" && !constants.IsInternalChannel(channel) {
					if err := al.bus.PublishOutbound(ctx, bus.OutboundMessage{
						Channel: channel,
						ChatID:  chatID,
						Content: "Memory threshold reached. Optimizing conversation history...",
					}); err != nil {
						logger.WarnCF("agent", "Failed to publish compaction notice (best-effort)", map[string]any{
							"channel": channel,
							"chat_id": chatID,
							"error":   err.Error(),
						})
					}
				}

				if flushed, err := al.maybeFlushMemoryBeforeCompaction(
					ctx,
					agent,
					sessionKey,
					tokenEstimate,
				); err != nil {
					logger.WarnCF("agent", "Background memory flush failed", map[string]any{
						"session_key": sessionKey,
						"error":       err.Error(),
					})
				} else if flushed {
					logger.InfoCF("agent", "Background memory flush completed", map[string]any{
						"session_key": sessionKey,
					})
				}

				if compacted, err := al.compactWithSafeguard(ctx, agent, sessionKey); err != nil {
					logger.WarnCF("agent", "Background compaction cancelled", map[string]any{
						"session_key": sessionKey,
						"error":       err.Error(),
					})
				} else if compacted {
					logger.InfoCF("agent", "Background compaction completed", map[string]any{
						"session_key": sessionKey,
					})
				}
			}()
		}
	}
}

// forceCompression aggressively reduces context when the limit is hit.
// It drops the oldest 50% of messages (keeping system prompt and last user message).
func (al *AgentLoop) forceCompression(agent *AgentInstance, sessionKey string) {
	history := agent.Sessions.GetHistory(sessionKey)
	if len(history) <= 4 {
		return
	}

	// Keep system prompt (usually [0]) and the very last message (user's trigger)
	// We want to drop the oldest half of the *conversation*
	// Assuming [0] is system, [1:] is conversation
	conversation := history[1 : len(history)-1]
	if len(conversation) == 0 {
		return
	}

	// Helper to find the mid-point of the conversation
	mid := len(conversation) / 2

	// New history structure:
	// 1. System Prompt (with compression note appended)
	// 2. Second half of conversation
	// 3. Last message

	droppedCount := mid
	keptConversation := conversation[mid:]

	newHistory := make([]providers.Message, 0, 1+len(keptConversation)+1)

	// Append compression note to the original system prompt instead of adding a new system message
	// This avoids having two consecutive system messages which some APIs (like Zhipu) reject
	compressionNote := fmt.Sprintf(
		"\n\n[System Note: Emergency compression dropped %d oldest messages due to context limit]",
		droppedCount,
	)
	enhancedSystemPrompt := history[0]
	enhancedSystemPrompt.Content = enhancedSystemPrompt.Content + compressionNote
	newHistory = append(newHistory, enhancedSystemPrompt)

	newHistory = append(newHistory, keptConversation...)
	newHistory = append(newHistory, history[len(history)-1]) // Last message

	// Update session
	agent.Sessions.SetHistory(sessionKey, newHistory)
	agent.Sessions.Save(sessionKey)

	logger.WarnCF("agent", "Forced compression executed", map[string]any{
		"session_key":  sessionKey,
		"dropped_msgs": droppedCount,
		"new_count":    len(newHistory),
	})
}

// summarizeSession summarizes the conversation history for a session.
func (al *AgentLoop) summarizeSession(agent *AgentInstance, sessionKey string) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	history := agent.Sessions.GetHistory(sessionKey)
	summary := agent.Sessions.GetSummary(sessionKey)

	// Keep last 4 messages for continuity
	if len(history) <= 4 {
		return
	}

	toSummarize := history[:len(history)-4]

	// Oversized Message Guard
	maxMessageTokens := agent.ContextWindow / 2
	validMessages := make([]providers.Message, 0)
	omitted := false

	for _, m := range toSummarize {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		msgTokens := len(m.Content) / 2
		if msgTokens > maxMessageTokens {
			omitted = true
			continue
		}
		validMessages = append(validMessages, m)
	}

	if len(validMessages) == 0 {
		return
	}

	// Multi-Part Summarization
	var finalSummary string
	if len(validMessages) > 10 {
		mid := len(validMessages) / 2
		part1 := validMessages[:mid]
		part2 := validMessages[mid:]

		s1, _ := al.summarizeBatch(ctx, agent, part1, "")
		s2, _ := al.summarizeBatch(ctx, agent, part2, "")

		mergePrompt := fmt.Sprintf(
			"Merge these two conversation summaries into one cohesive summary:\n\n1: %s\n\n2: %s",
			s1,
			s2,
		)
		resp, err := agent.Provider.Chat(
			ctx,
			[]providers.Message{{Role: "user", Content: mergePrompt}},
			nil,
			agent.Model,
			map[string]any{
				"max_tokens":       1024,
				"temperature":      0.3,
				"prompt_cache_key": agent.ID,
			},
		)
		if err == nil {
			finalSummary = resp.Content
		} else {
			finalSummary = s1 + " " + s2
		}
	} else {
		finalSummary, _ = al.summarizeBatch(ctx, agent, validMessages, summary)
	}

	if omitted && finalSummary != "" {
		finalSummary += "\n[Note: Some oversized messages were omitted from this summary for efficiency.]"
	}

	if finalSummary != "" {
		agent.Sessions.SetSummary(sessionKey, finalSummary)
		agent.Sessions.TruncateHistory(sessionKey, 4)
		agent.Sessions.Save(sessionKey)
	}
}

// summarizeBatch summarizes a batch of messages.
func (al *AgentLoop) summarizeBatch(
	ctx context.Context,
	agent *AgentInstance,
	batch []providers.Message,
	existingSummary string,
) (string, error) {
	var sb strings.Builder
	sb.WriteString(
		"Provide a concise summary of this conversation segment, preserving core context and key points.\n",
	)
	if existingSummary != "" {
		sb.WriteString("Existing context: ")
		sb.WriteString(existingSummary)
		sb.WriteString("\n")
	}
	sb.WriteString("\nCONVERSATION:\n")
	for _, m := range batch {
		fmt.Fprintf(&sb, "%s: %s\n", m.Role, m.Content)
	}
	prompt := sb.String()

	response, err := agent.Provider.Chat(
		ctx,
		[]providers.Message{{Role: "user", Content: prompt}},
		nil,
		agent.Model,
		map[string]any{
			"max_tokens":       1024,
			"temperature":      0.3,
			"prompt_cache_key": agent.ID,
		},
	)
	if err != nil {
		return "", err
	}
	return response.Content, nil
}

// estimateTokens estimates the number of tokens in a message list.
// Delegates to the shared estimateTotalTokens helper in context.go.
func (al *AgentLoop) estimateTokens(messages []providers.Message) int {
	return estimateTotalTokens("", messages)
}

// estimateMessageTokens estimates the number of tokens in a single message.
// Kept as a thin wrapper for compaction helpers that operate message-by-message.
func (al *AgentLoop) estimateMessageTokens(message providers.Message) int {
	return estimateTotalTokens("", []providers.Message{message})
}

// ThinkingLevel controls how the provider sends thinking parameters.
//
//   - "adaptive": sends {thinking: {type: "adaptive"}} + output_config.effort (Claude 4.6+)
//   - "low"/"medium"/"high"/"xhigh": sends {thinking: {type: "enabled", budget_tokens: N}} (all models)
//   - "off": disables thinking
type ThinkingLevel string

const (
	ThinkingOff      ThinkingLevel = "off"
	ThinkingLow      ThinkingLevel = "low"
	ThinkingMedium   ThinkingLevel = "medium"
	ThinkingHigh     ThinkingLevel = "high"
	ThinkingXHigh    ThinkingLevel = "xhigh"
	ThinkingAdaptive ThinkingLevel = "adaptive"
)

// parseThinkingLevel normalizes a config string to a ThinkingLevel.
// Case-insensitive and whitespace-tolerant for user-facing config values.
// Returns ThinkingOff for unknown or empty values.
func parseThinkingLevel(level string) ThinkingLevel {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "adaptive":
		return ThinkingAdaptive
	case "low":
		return ThinkingLow
	case "medium":
		return ThinkingMedium
	case "high":
		return ThinkingHigh
	case "xhigh":
		return ThinkingXHigh
	default:
		return ThinkingOff
	}
}

// Consolidated from compaction.go

func (al *AgentLoop) maybeFlushMemoryBeforeCompaction(
	ctx context.Context,
	agent *AgentInstance,
	sessionKey string,
	tokenEstimate int,
) (bool, error) {
	if !isCompactionModeEnabled(agent.Compaction.Mode) {
		return false, nil
	}
	if !agent.Compaction.MemoryFlushEnabled || sessionKey == "" {
		return false, nil
	}

	triggerPoint := agent.ContextWindow - agent.Compaction.ReserveTokens - agent.Compaction.MemoryFlushSoftThreshold
	if triggerPoint < agent.ContextWindow/3 {
		triggerPoint = agent.ContextWindow / 3
	}
	if tokenEstimate < triggerPoint {
		return false, nil
	}

	compactionCount, flushedAtCount, _ := agent.Sessions.GetCompactionState(sessionKey)
	if flushedAtCount == compactionCount {
		return false, nil
	}

	if err := al.flushMemorySnapshot(ctx, agent, sessionKey); err != nil {
		return false, err
	}

	agent.Sessions.MarkMemoryFlush(sessionKey, compactionCount)
	_ = agent.Sessions.Save(sessionKey)
	return true, nil
}

func (al *AgentLoop) flushMemorySnapshot(ctx context.Context, agent *AgentInstance, sessionKey string) error {
	history := agent.Sessions.GetHistory(sessionKey)
	if len(history) == 0 {
		return nil
	}

	recent := make([]providers.Message, 0, 12)
	for i := len(history) - 1; i >= 0 && len(recent) < 12; i-- {
		msg := history[i]
		if (msg.Role != "user" && msg.Role != "assistant") || strings.TrimSpace(msg.Content) == "" {
			continue
		}
		recent = append([]providers.Message{msg}, recent...)
	}
	if len(recent) == 0 {
		return nil
	}

	var prompt strings.Builder
	prompt.WriteString("Extract durable memory from this chat — facts worth remembering long-term.\n")
	prompt.WriteString("Return concise markdown bullets under these headings only:\n")
	prompt.WriteString("## Profile\n## Long-term Facts\n## Active Goals\n## Constraints\n## Open Threads\n## Deprecated/Resolved\n\n")
	prompt.WriteString("Rules:\n")
	prompt.WriteString("- Only extract information that would be useful in FUTURE conversations.\n")
	prompt.WriteString("- Skip transient details (greetings, acknowledgements, single-use commands).\n")
	prompt.WriteString("- Each bullet should be self-contained and understandable without context.\n")
	prompt.WriteString("- Omit sections with nothing to report.\n")
	prompt.WriteString("\nCHAT:\n")
	for _, m := range recent {
		prompt.WriteString(m.Role)
		prompt.WriteString(": ")
		prompt.WriteString(m.Content)
		prompt.WriteString("\n")
	}

	resp, err := agent.Provider.Chat(
		ctx,
		[]providers.Message{{Role: "user", Content: prompt.String()}},
		nil,
		agent.Model,
		map[string]any{
			"max_tokens":  700,
			"temperature": 0.2,
		},
	)
	if err != nil {
		return err
	}
	if strings.TrimSpace(resp.Content) == "" {
		return fmt.Errorf("empty memory flush response")
	}

	memory := (*MemoryStore)(nil)
	if agent.ContextBuilder != nil {
		memory = agent.ContextBuilder.MemoryForSession(sessionKey, "", "")
	}
	if memory == nil {
		memory = NewMemoryStore(agent.Workspace)
	}
	return memory.OrganizeWriteback(resp.Content)
}

func (al *AgentLoop) compactWithSafeguard(
	ctx context.Context,
	agent *AgentInstance,
	sessionKey string,
) (bool, error) {
	switch normalizeCompactionMode(agent.Compaction.Mode) {
	case "off":
		return false, nil
	case "legacy":
		beforeHistory := len(agent.Sessions.GetHistory(sessionKey))
		beforeSummary := strings.TrimSpace(agent.Sessions.GetSummary(sessionKey))
		al.summarizeSession(agent, sessionKey)
		afterHistory := len(agent.Sessions.GetHistory(sessionKey))
		afterSummary := strings.TrimSpace(agent.Sessions.GetSummary(sessionKey))
		if afterHistory < beforeHistory || afterSummary != beforeSummary {
			agent.Sessions.IncrementCompactionCount(sessionKey)
			_ = agent.Sessions.Save(sessionKey)
			return true, nil
		}
		return false, nil
	}

	history := sanitizeHistoryForProvider(agent.Sessions.GetHistory(sessionKey))
	if len(history) <= 6 {
		return false, nil
	}

	historyTokens := al.estimateTokens(history)
	historyBudget := int(float64(agent.ContextWindow) * agent.Compaction.MaxHistoryShare)
	if historyBudget <= 0 {
		historyBudget = agent.ContextWindow / 2
	}
	if historyTokens <= historyBudget && len(history) < 24 {
		return false, nil
	}

	keepRecentTokens := agent.Compaction.KeepRecentTokens
	if keepRecentTokens <= 0 {
		keepRecentTokens = maxInt(1024, agent.ContextWindow/4)
	}

	keepStart := len(history) - 4
	if keepStart < 1 {
		keepStart = 1
	}
	acc := 0
	for i := len(history) - 1; i >= 1; i-- {
		acc += al.estimateMessageTokens(history[i])
		if acc >= keepRecentTokens {
			keepStart = i
			break
		}
	}
	if keepStart <= 0 || keepStart >= len(history) {
		return false, nil
	}

	toSummarize := history[:keepStart]
	kept := history[keepStart:]
	if len(toSummarize) == 0 || len(kept) == 0 {
		return false, nil
	}

	existingSummary := agent.Sessions.GetSummary(sessionKey)
	summary, err := al.generateCompactionSummary(ctx, agent, toSummarize, existingSummary)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(summary) == "" {
		return false, fmt.Errorf("compaction summary unavailable")
	}

	summary = strings.TrimSpace(summary) + "\n\n[Post-compaction refresh: Re-check AGENTS.md and MEMORY.md before continuing.]"
	agent.Sessions.SetSummary(sessionKey, summary)
	agent.Sessions.SetHistory(sessionKey, kept)
	agent.Sessions.IncrementCompactionCount(sessionKey)
	_ = agent.Sessions.Save(sessionKey)

	logger.InfoCF("agent", "Compaction safeguard completed", map[string]any{
		"session_key":           sessionKey,
		"history_tokens_before": historyTokens,
		"kept_messages":         len(kept),
		"summarized_messages":   len(toSummarize),
	})
	return true, nil
}

func (al *AgentLoop) generateCompactionSummary(
	ctx context.Context,
	agent *AgentInstance,
	history []providers.Message,
	existingSummary string,
) (string, error) {
	safeMessages := make([]providers.Message, 0, len(history))
	for _, msg := range history {
		if msg.Role != "user" && msg.Role != "assistant" && msg.Role != "tool" {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		if msg.Role == "tool" && len(content) > 1200 {
			const maxLen = 1100
			const tailMin = 300
			content = utils.TruncateHeadTailWithMarker(content, maxLen, "\n...\n[tool result condensed]\n...\n", tailMin)
		}
		msg.Content = content
		safeMessages = append(safeMessages, msg)
	}
	if len(safeMessages) == 0 {
		return strings.TrimSpace(existingSummary), nil
	}

	maxChunkTokens := int(float64(agent.ContextWindow)*0.35) - 1024
	if maxChunkTokens < 512 {
		maxChunkTokens = 512
	}

	chunks := al.splitMessagesByTokenBudget(safeMessages, maxChunkTokens)
	summary := strings.TrimSpace(existingSummary)
	for _, chunk := range chunks {
		next, err := al.summarizeBatchStructured(ctx, agent, chunk, summary)
		if err != nil {
			return "", err
		}
		summary = strings.TrimSpace(next)
	}
	return summary, nil
}

func (al *AgentLoop) summarizeBatchStructured(
	ctx context.Context,
	agent *AgentInstance,
	batch []providers.Message,
	existingSummary string,
) (string, error) {
	var sb strings.Builder
	sb.WriteString("Summarize this conversation segment for future continuity.\n")
	sb.WriteString("Output EXACTLY in this format (keep each section to 1-3 bullet points):\n\n")
	sb.WriteString("## Intent\n- <what the user wants to achieve>\n\n")
	sb.WriteString("## Decisions\n- <key decisions made during the conversation>\n\n")
	sb.WriteString("## Tool Results\n- <important tool outputs with key data points>\n\n")
	sb.WriteString("## Pending Actions\n- <what still needs to be done>\n\n")
	sb.WriteString("## Constraints\n- <any limitations or requirements discovered>\n\n")
	sb.WriteString("Keep total output under 300 words. Prioritize actionable information over conversational details.\n")
	sb.WriteString("Omit empty sections. Do NOT include greetings, pleasantries, or meta-commentary.\n")
	if existingSummary != "" {
		sb.WriteString("\nExisting summary:\n")
		sb.WriteString(existingSummary)
		sb.WriteString("\n")
	}
	sb.WriteString("\nConversation:\n")
	for _, m := range batch {
		sb.WriteString(m.Role)
		sb.WriteString(": ")
		sb.WriteString(m.Content)
		sb.WriteString("\n")
	}

	resp, err := agent.Provider.Chat(
		ctx,
		[]providers.Message{{Role: "user", Content: sb.String()}},
		nil,
		agent.Model,
		map[string]any{
			"max_tokens":  800,
			"temperature": 0.2,
		},
	)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

func (al *AgentLoop) splitMessagesByTokenBudget(
	messages []providers.Message,
	maxTokens int,
) [][]providers.Message {
	if len(messages) == 0 || maxTokens <= 0 {
		return nil
	}
	chunks := make([][]providers.Message, 0, 4)
	current := make([]providers.Message, 0, 8)
	currentTokens := 0
	for _, msg := range messages {
		msgTokens := al.estimateMessageTokens(msg)
		if len(current) > 0 && currentTokens+msgTokens > maxTokens {
			chunks = append(chunks, current)
			current = make([]providers.Message, 0, 8)
			currentTokens = 0
		}
		current = append(current, msg)
		currentTokens += msgTokens
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (al *AgentLoop) safeCompactionContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 90*time.Second)
}

func normalizeCompactionMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "safeguard":
		return "safeguard"
	case "off", "none", "disabled":
		return "off"
	case "legacy":
		return "legacy"
	default:
		return "safeguard"
	}
}

func isCompactionModeEnabled(mode string) bool {
	return normalizeCompactionMode(mode) != "off"

}
