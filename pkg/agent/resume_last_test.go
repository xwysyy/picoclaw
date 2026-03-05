package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindLastUnfinishedRun_OnlyHeartbeatIsIgnored(t *testing.T) {
	workspace := t.TempDir()

	heartbeatDir := filepath.Join(workspace, ".x-claw", "audit", "runs", "heartbeat")
	if err := os.MkdirAll(heartbeatDir, 0o755); err != nil {
		t.Fatalf("mkdir heartbeat dir: %v", err)
	}
	// Heartbeat run: unfinished, but should be ignored by resume discovery.
	if err := os.WriteFile(filepath.Join(heartbeatDir, "events.jsonl"), []byte(
		`{"type":"run.start","ts_ms":3000,"run_id":"hb-1","session_key":"heartbeat","channel":"feishu","chat_id":"oc_hb"}`+"\n",
	), 0o600); err != nil {
		t.Fatalf("write heartbeat events: %v", err)
	}

	cand, err := findLastUnfinishedRun(workspace)
	if err == nil {
		t.Fatalf("expected error, got candidate: %+v", cand)
	}
}

func TestFindLastUnfinishedRun_PrefersNonHeartbeatEvenIfNewer(t *testing.T) {
	workspace := t.TempDir()

	// Heartbeat run (newer ts_ms): should still be ignored.
	heartbeatDir := filepath.Join(workspace, ".x-claw", "audit", "runs", "heartbeat")
	if err := os.MkdirAll(heartbeatDir, 0o755); err != nil {
		t.Fatalf("mkdir heartbeat dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(heartbeatDir, "events.jsonl"), []byte(
		`{"type":"run.start","ts_ms":9000,"run_id":"hb-2","session_key":"heartbeat","channel":"feishu","chat_id":"oc_hb"}`+"\n",
	), 0o600); err != nil {
		t.Fatalf("write heartbeat events: %v", err)
	}

	// Real user-ish session run.
	userDir := filepath.Join(workspace, ".x-claw", "audit", "runs", "agent_main_main")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatalf("mkdir user dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "events.jsonl"), []byte(
		`{"type":"run.start","ts_ms":2000,"run_id":"run-1","session_key":"agent:main:main","channel":"feishu","chat_id":"oc_user","sender_id":"u1"}`+"\n",
	), 0o600); err != nil {
		t.Fatalf("write user events: %v", err)
	}

	cand, err := findLastUnfinishedRun(workspace)
	if err != nil {
		t.Fatalf("expected candidate, got error: %v", err)
	}
	if cand.RunID != "run-1" {
		t.Fatalf("expected run_id=run-1, got %q", cand.RunID)
	}
	if cand.SessionKey != "agent:main:main" {
		t.Fatalf("expected session_key=agent:main:main, got %q", cand.SessionKey)
	}
	if cand.Channel != "feishu" || cand.ChatID != "oc_user" {
		t.Fatalf("unexpected route: channel=%q chat_id=%q", cand.Channel, cand.ChatID)
	}
}
