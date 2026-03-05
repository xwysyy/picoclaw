package export

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xwysyy/X-Claw/pkg/session"
	"github.com/xwysyy/X-Claw/pkg/state"
	"github.com/xwysyy/X-Claw/pkg/tools"
)

func TestRunExport_WritesZipBundle(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	workspace := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("MkdirAll(workspace) error: %v", err)
	}

	// Minimal config that passes validation + contains secrets to verify redaction.
	cfgDir := filepath.Join(tmp, ".x-claw")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(cfgDir) error: %v", err)
	}
	cfgPath := filepath.Join(cfgDir, "config.json")
	cfgJSON := `{
  "agents": { "defaults": { "workspace": "` + workspace + `" } },
  "model_list": [{ "model_name": "test", "model": "openai/test", "api_key": "sk-SECRET-KEY" }],
  "gateway": { "api_key": "gw_secret" },
  "channels": { "feishu": { "enabled": true, "app_secret": "feishu_secret" } }
}`
	if err := os.WriteFile(cfgPath, []byte(cfgJSON), 0o600); err != nil {
		t.Fatalf("WriteFile(config.json) error: %v", err)
	}

	// Session
	sessionKey := "agent:main:feishu:group:oc_test"
	sm := session.NewSessionManager(filepath.Join(workspace, "sessions"))
	sm.AddMessage(sessionKey, "user", "hello")
	if err := sm.Save(sessionKey); err != nil {
		t.Fatalf("Save(session) error: %v", err)
	}

	// Tool trace
	traceDir := filepath.Join(workspace, ".x-claw", "audit", "tools", tools.SafePathToken(sessionKey))
	if err := os.MkdirAll(filepath.Join(traceDir, "calls"), 0o755); err != nil {
		t.Fatalf("MkdirAll(traceDir) error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(traceDir, "events.jsonl"), []byte("{\"type\":\"tool.start\"}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(events.jsonl) error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(traceDir, "calls", "call.json"), []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatalf("WriteFile(call.json) error: %v", err)
	}

	// Cron store
	cronDir := filepath.Join(workspace, "cron")
	if err := os.MkdirAll(cronDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(cronDir) error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cronDir, "jobs.json"), []byte(`{"version":1,"jobs":[]}`), 0o600); err != nil {
		t.Fatalf("WriteFile(cron jobs) error: %v", err)
	}

	// last_active state
	st := state.NewManager(workspace)
	if err := st.SetLastChannel("feishu:oc_test"); err != nil {
		t.Fatalf("SetLastChannel error: %v", err)
	}

	out := filepath.Join(tmp, "bundle.zip")
	res, err := RunExport(ExportOptions{
		SessionKey:    sessionKey,
		OutPath:       out,
		IncludeTrace:  true,
		IncludeCron:   true,
		IncludeState:  true,
		IncludeConfig: true,
		UseLastActive: false,
	})
	if err != nil {
		t.Fatalf("RunExport error: %v", err)
	}
	if res.OutputPath != out {
		t.Fatalf("OutputPath = %q, want %q", res.OutputPath, out)
	}

	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatalf("OpenReader error: %v", err)
	}
	defer zr.Close()

	wantFiles := []string{
		res.BundleRoot + "/manifest.json",
		res.BundleRoot + "/session.json",
		res.BundleRoot + "/config.redacted.json",
		res.BundleRoot + "/state/state.json",
		res.BundleRoot + "/cron/jobs.json",
		res.BundleRoot + "/tool_trace/events.jsonl",
		res.BundleRoot + "/tool_trace/calls/call.json",
	}

	got := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		got[f.Name] = f
	}
	for _, name := range wantFiles {
		if _, ok := got[name]; !ok {
			t.Fatalf("missing file in zip: %s", name)
		}
	}

	// Verify config redaction
	cfgFile := got[res.BundleRoot+"/config.redacted.json"]
	rc, err := cfgFile.Open()
	if err != nil {
		t.Fatalf("Open(config.redacted.json) error: %v", err)
	}
	defer rc.Close()
	cfgBytes, err := ioReadAll(rc)
	if err != nil {
		t.Fatalf("Read(config.redacted.json) error: %v", err)
	}
	cfgText := string(cfgBytes)
	if strings.Contains(cfgText, "sk-SECRET-KEY") || strings.Contains(cfgText, "gw_secret") || strings.Contains(cfgText, "feishu_secret") {
		t.Fatalf("config.redacted.json still contains secrets: %s", cfgText)
	}
	if !strings.Contains(cfgText, "<redacted>") {
		t.Fatalf("config.redacted.json did not include <redacted>: %s", cfgText)
	}

	// Quick sanity check manifest is valid JSON.
	manifestFile := got[res.BundleRoot+"/manifest.json"]
	mr, err := manifestFile.Open()
	if err != nil {
		t.Fatalf("Open(manifest.json) error: %v", err)
	}
	defer mr.Close()
	manifestBytes, err := ioReadAll(mr)
	if err != nil {
		t.Fatalf("Read(manifest.json) error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(manifestBytes, &parsed); err != nil {
		t.Fatalf("manifest.json is not valid JSON: %v", err)
	}
}

func TestSelectSessionFromLastActive_ExactMatch(t *testing.T) {
	tmp := t.TempDir()
	workspace := filepath.Join(tmp, "ws")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("MkdirAll(workspace) error: %v", err)
	}

	sessionKey := "agent:main:feishu:group:oc_test"
	sm := session.NewSessionManager(filepath.Join(workspace, "sessions"))
	sm.AddMessage(sessionKey, "user", "hello")
	if err := sm.Save(sessionKey); err != nil {
		t.Fatalf("Save(session) error: %v", err)
	}

	st := state.NewManager(workspace)
	if err := st.SetLastChannel("feishu:oc_test"); err != nil {
		t.Fatalf("SetLastChannel error: %v", err)
	}

	picked, by, err := selectSessionFromLastActive(workspace, session.NewSessionManager(filepath.Join(workspace, "sessions")))
	if err != nil {
		t.Fatalf("selectSessionFromLastActive error: %v", err)
	}
	if picked != sessionKey {
		t.Fatalf("picked = %q, want %q", picked, sessionKey)
	}
	if by != "last_active.exact" {
		t.Fatalf("selectedBy = %q, want %q", by, "last_active.exact")
	}
}

func ioReadAll(r io.Reader) ([]byte, error) {
	var buf strings.Builder
	tmp := make([]byte, 8192)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if err != nil {
			if err == io.EOF {
				return []byte(buf.String()), nil
			}
			return nil, err
		}
	}
}
