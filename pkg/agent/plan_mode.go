package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/pkg/fileutil"
	"github.com/xwysyy/X-Claw/pkg/tools"
	"github.com/xwysyy/X-Claw/pkg/utils"
)

type sessionPermissionMode string

const (
	sessionPermissionModeRun  sessionPermissionMode = "run"
	sessionPermissionModePlan sessionPermissionMode = "plan"
)

type sessionPermissionState struct {
	Mode sessionPermissionMode `json:"mode,omitempty"`

	// PendingTask stores the last user request captured while in plan mode, so
	// /approve can execute it without retyping.
	PendingTask string `json:"pending_task,omitempty"`

	UpdatedAt   string `json:"updated_at,omitempty"`
	UpdatedAtMS int64  `json:"updated_at_ms,omitempty"`
}

func defaultSessionPermissionState() sessionPermissionState {
	return sessionPermissionState{Mode: sessionPermissionModeRun}
}

func normalizeSessionPermissionMode(mode sessionPermissionMode) sessionPermissionMode {
	switch strings.ToLower(strings.TrimSpace(string(mode))) {
	case string(sessionPermissionModePlan):
		return sessionPermissionModePlan
	default:
		return sessionPermissionModeRun
	}
}

func (s sessionPermissionState) normalized() sessionPermissionState {
	s.Mode = normalizeSessionPermissionMode(s.Mode)
	s.PendingTask = strings.TrimSpace(s.PendingTask)
	return s
}

func (s sessionPermissionState) isPlan() bool {
	return normalizeSessionPermissionMode(s.Mode) == sessionPermissionModePlan
}

func sessionPermissionStatePath(workspace, sessionKey string) (string, error) {
	workspace = strings.TrimSpace(workspace)
	sessionKey = utils.CanonicalSessionKey(sessionKey)
	if workspace == "" {
		return "", fmt.Errorf("workspace is required")
	}
	if sessionKey == "" {
		return "", fmt.Errorf("sessionKey is required")
	}

	token := tools.SafePathToken(sessionKey)
	if token == "" {
		token = "unknown"
	}

	return filepath.Join(workspace, ".x-claw", "state", "sessions", token, "permission.json"), nil
}

func loadSessionPermissionStateWithDefault(workspace, sessionKey string, defaultMode sessionPermissionMode) sessionPermissionState {
	path, err := sessionPermissionStatePath(workspace, sessionKey)
	if err != nil {
		return sessionPermissionState{Mode: normalizeSessionPermissionMode(defaultMode)}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return sessionPermissionState{Mode: normalizeSessionPermissionMode(defaultMode)}
	}

	var st sessionPermissionState
	if err := json.Unmarshal(data, &st); err != nil {
		return sessionPermissionState{Mode: normalizeSessionPermissionMode(defaultMode)}
	}
	return st.normalized()
}

func loadSessionPermissionState(workspace, sessionKey string) sessionPermissionState {
	return loadSessionPermissionStateWithDefault(workspace, sessionKey, sessionPermissionModeRun)
}

func saveSessionPermissionState(workspace, sessionKey string, st sessionPermissionState) error {
	path, err := sessionPermissionStatePath(workspace, sessionKey)
	if err != nil {
		return err
	}

	now := time.Now()
	st = st.normalized()
	st.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
	st.UpdatedAtMS = now.UnixMilli()

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(path, data, 0o600)
}
