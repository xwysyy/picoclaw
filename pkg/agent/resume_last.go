package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xwysyy/X-Claw/internal/core/events"
	"github.com/xwysyy/X-Claw/pkg/logger"
	"github.com/xwysyy/X-Claw/pkg/routing"
	"github.com/xwysyy/X-Claw/pkg/utils"
)

type ResumeCandidate struct {
	RunID      string `json:"run_id"`
	SessionKey string `json:"session_key"`
	Channel    string `json:"channel"`
	ChatID     string `json:"chat_id"`
	SenderID   string `json:"sender_id"`
	AgentID    string `json:"agent_id,omitempty"`
	Model      string `json:"model,omitempty"`

	LastEventType      string `json:"last_event_type,omitempty"`
	LastEventTSMS      int64  `json:"last_event_ts_ms,omitempty"`
	UserMessagePreview string `json:"user_message_preview,omitempty"`
}

func isInternalSessionKey(sessionKey string) bool {
	switch utils.CanonicalSessionKey(sessionKey) {
	case "heartbeat":
		return true
	default:
		return false
	}
}

func findLastUnfinishedRun(workspace string) (*ResumeCandidate, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil, fmt.Errorf("workspace is empty")
	}

	root := filepath.Join(workspace, ".x-claw", "audit", "runs")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read runs dir: %w", err)
	}

	type runState struct {
		runID      string
		sessionKey string
		channel    string
		chatID     string
		senderID   string
		agentID    string
		model      string
		userMsg    string

		lastType string
		lastTSMS int64

		endedNormally bool
	}

	byRunID := make(map[string]*runState)

	// Scan each session's events.jsonl.
	for _, e := range entries {
		if e == nil || !e.IsDir() {
			continue
		}
		// Never try to resume internal system sessions (e.g. heartbeat).
		// The resume API is designed for user-initiated runs only.
		if strings.EqualFold(strings.TrimSpace(e.Name()), "heartbeat") {
			continue
		}
		eventsPath := filepath.Join(root, e.Name(), "events.jsonl")
		f, err := os.Open(eventsPath)
		if err != nil {
			continue
		}

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var ev runTraceEvent
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				continue
			}
			rid := strings.TrimSpace(ev.RunID)
			if rid == "" {
				continue
			}

			st := byRunID[rid]
			if st == nil {
				st = &runState{runID: rid}
				byRunID[rid] = st
			}

			// Keep best-effort metadata.
			if st.sessionKey == "" && strings.TrimSpace(ev.SessionKey) != "" {
				st.sessionKey = utils.CanonicalSessionKey(ev.SessionKey)
			}
			if st.channel == "" && strings.TrimSpace(ev.Channel) != "" {
				st.channel = strings.TrimSpace(ev.Channel)
			}
			if st.chatID == "" && strings.TrimSpace(ev.ChatID) != "" {
				st.chatID = strings.TrimSpace(ev.ChatID)
			}
			if st.senderID == "" && strings.TrimSpace(ev.SenderID) != "" {
				st.senderID = strings.TrimSpace(ev.SenderID)
			}
			if st.agentID == "" && strings.TrimSpace(ev.AgentID) != "" {
				st.agentID = strings.TrimSpace(ev.AgentID)
			}
			if st.model == "" && strings.TrimSpace(ev.Model) != "" {
				st.model = strings.TrimSpace(ev.Model)
			}

			if ev.Type == events.RunStart && strings.TrimSpace(ev.UserMessagePreview) != "" {
				st.userMsg = strings.TrimSpace(ev.UserMessagePreview)
			}

			if ev.TSMS > 0 && ev.TSMS >= st.lastTSMS {
				st.lastTSMS = ev.TSMS
				st.lastType = strings.TrimSpace(string(ev.Type))
			}

			if ev.Type == events.RunEnd {
				st.endedNormally = true
			}
		}
		f.Close()
	}

	var best *runState
	for _, st := range byRunID {
		if st == nil {
			continue
		}
		if st.endedNormally {
			continue
		}
		if isInternalSessionKey(st.sessionKey) {
			continue
		}
		// Must have enough routing info to resume safely.
		if strings.TrimSpace(st.sessionKey) == "" || strings.TrimSpace(st.channel) == "" || strings.TrimSpace(st.chatID) == "" {
			continue
		}
		if best == nil || st.lastTSMS > best.lastTSMS {
			best = st
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no unfinished runs found")
	}

	return &ResumeCandidate{
		RunID:              best.runID,
		SessionKey:         best.sessionKey,
		Channel:            best.channel,
		ChatID:             best.chatID,
		SenderID:           best.senderID,
		AgentID:            best.agentID,
		Model:              best.model,
		LastEventType:      best.lastType,
		LastEventTSMS:      best.lastTSMS,
		UserMessagePreview: best.userMsg,
	}, nil
}

func resumeLastTaskPrompt() string {
	return "[resume_last_task] Continue the unfinished task from its last known state."
}

func (al *AgentLoop) ResumeLastTask(ctx context.Context) (*ResumeCandidate, string, error) {
	if al == nil || al.registry == nil {
		return nil, "", fmt.Errorf("agent loop not initialized")
	}

	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		return nil, "", fmt.Errorf("no agent available")
	}

	candidate, err := findLastUnfinishedRun(defaultAgent.Workspace)
	if err != nil {
		return nil, "", err
	}

	agent := defaultAgent
	if strings.TrimSpace(candidate.AgentID) != "" {
		if a, ok := al.registry.GetAgent(candidate.AgentID); ok && a != nil {
			agent = a
		}
	} else if parsed := routing.ParseAgentSessionKey(candidate.SessionKey); parsed != nil {
		if a, ok := al.registry.GetAgent(parsed.AgentID); ok && a != nil {
			agent = a
		}
	}

	// A synthetic user message acts as a deterministic "resume trigger".
	// It is stored in session WAL and the run trace records "run.resume".
	resumePrompt := resumeLastTaskPrompt()

	logger.InfoCF("agent", "Resuming last unfinished run", map[string]any{
		"run_id":      candidate.RunID,
		"session_key": candidate.SessionKey,
		"channel":     candidate.Channel,
		"chat_id":     candidate.ChatID,
		"agent_id":    agent.ID,
	})

	resp, err := al.runAgentLoop(ctx, agent, processOptions{
		SessionKey:      candidate.SessionKey,
		Channel:         candidate.Channel,
		ChatID:          candidate.ChatID,
		SenderID:        candidate.SenderID,
		UserMessage:     resumePrompt,
		DefaultResponse: "Resume completed.",
		EnableSummary:   true,
		SendResponse:    true,
		RunID:           candidate.RunID,
		Resume:          true,
	})
	if err != nil {
		return candidate, "", err
	}

	return candidate, resp, nil
}
