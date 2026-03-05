package tools

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/pkg/fileutil"
)

type EstopMode string

const (
	EstopModeOff         EstopMode = "off"
	EstopModeKillAll     EstopMode = "kill_all"
	EstopModeNetworkKill EstopMode = "network_kill"
)

type EstopState struct {
	Mode EstopMode `json:"mode,omitempty"`

	// BlockedDomains denies web_fetch to matching domains.
	// Entries are compared case-insensitively against URL hosts (exact or suffix match).
	BlockedDomains []string `json:"blocked_domains,omitempty"`

	// FrozenTools denies tools by exact name (case-insensitive).
	FrozenTools []string `json:"frozen_tools,omitempty"`

	// FrozenPrefixes denies tools by prefix (case-insensitive).
	FrozenPrefixes []string `json:"frozen_prefixes,omitempty"`

	Note string `json:"note,omitempty"`

	UpdatedAt   string `json:"updated_at,omitempty"`
	UpdatedAtMS int64  `json:"updated_at_ms,omitempty"`
}

func DefaultEstopState() EstopState {
	return EstopState{Mode: EstopModeOff}
}

func normalizeEstopMode(mode EstopMode) EstopMode {
	switch strings.ToLower(strings.TrimSpace(string(mode))) {
	case string(EstopModeKillAll):
		return EstopModeKillAll
	case string(EstopModeNetworkKill):
		return EstopModeNetworkKill
	default:
		return EstopModeOff
	}
}

func normalizeListLower(xs []string) []string {
	if len(xs) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(xs))
	out := make([]string, 0, len(xs))
	for _, raw := range xs {
		item := strings.ToLower(strings.TrimSpace(raw))
		if item == "" {
			continue
		}
		item = strings.TrimPrefix(item, "https://")
		item = strings.TrimPrefix(item, "http://")
		item = strings.TrimPrefix(item, ".")
		item = strings.TrimRight(item, "/")
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	slices.Sort(out)
	return out
}

func (s EstopState) Normalized() EstopState {
	s.Mode = normalizeEstopMode(s.Mode)
	s.BlockedDomains = normalizeListLower(s.BlockedDomains)
	s.FrozenTools = normalizeListLower(s.FrozenTools)
	s.FrozenPrefixes = normalizeListLower(s.FrozenPrefixes)
	s.Note = strings.TrimSpace(s.Note)
	return s
}

func estopStatePath(workspace string) (string, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return "", fmt.Errorf("workspace is required")
	}
	return filepath.Join(workspace, ".x-claw", "state", "estop.json"), nil
}

func LoadEstopState(workspace string) (EstopState, error) {
	path, err := estopStatePath(workspace)
	if err != nil {
		return DefaultEstopState(), err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultEstopState(), nil
		}
		return DefaultEstopState(), err
	}

	var st EstopState
	if err := json.Unmarshal(data, &st); err != nil {
		return DefaultEstopState(), err
	}
	return st.Normalized(), nil
}

func SaveEstopState(workspace string, st EstopState) (EstopState, error) {
	path, err := estopStatePath(workspace)
	if err != nil {
		return DefaultEstopState(), err
	}

	now := time.Now()
	st = st.Normalized()
	st.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
	st.UpdatedAtMS = now.UnixMilli()

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return DefaultEstopState(), err
	}
	if err := fileutil.WriteFileAtomic(path, data, 0o600); err != nil {
		return DefaultEstopState(), err
	}
	return st, nil
}

func (s EstopState) deniesDomain(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	if idx := strings.Index(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	for _, blocked := range s.BlockedDomains {
		b := strings.ToLower(strings.TrimSpace(blocked))
		if b == "" {
			continue
		}
		if host == b {
			return true
		}
		if strings.HasSuffix(host, "."+b) {
			return true
		}
	}
	return false
}

func (s EstopState) DeniesTool(toolName string, args map[string]any) (bool, string) {
	s = s.Normalized()
	name := strings.ToLower(strings.TrimSpace(toolName))

	if s.Mode == EstopModeKillAll {
		return true, "estop kill_all engaged"
	}

	for _, frozen := range s.FrozenTools {
		if frozen != "" && name == frozen {
			return true, "estop frozen tool: " + frozen
		}
	}
	for _, prefix := range s.FrozenPrefixes {
		if prefix != "" && strings.HasPrefix(name, prefix) {
			return true, "estop frozen tool prefix: " + prefix
		}
	}

	if s.Mode == EstopModeNetworkKill {
		if name == "web_search" || name == "web_fetch" {
			return true, "estop network_kill engaged (web tools disabled)"
		}
		if strings.HasPrefix(name, "mcp_") {
			return true, "estop network_kill engaged (mcp tools disabled)"
		}
	}

	if name == "web_fetch" && len(s.BlockedDomains) > 0 {
		if raw, ok := args["url"].(string); ok {
			if u, err := url.Parse(strings.TrimSpace(raw)); err == nil {
				if s.deniesDomain(u.Host) {
					return true, "estop blocked domain: " + u.Host
				}
			}
		}
	}

	return false, ""
}
