package export

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/xwysyy/X-Claw/cmd/x-claw/internal"
	"github.com/xwysyy/X-Claw/cmd/x-claw/internal/cliutil"
	"github.com/xwysyy/X-Claw/pkg/session"
	"github.com/xwysyy/X-Claw/pkg/state"
	"github.com/xwysyy/X-Claw/pkg/tools"
)

type ExportOptions struct {
	SessionKey    string
	UseLastActive bool
	OutPath       string

	IncludeTrace  bool
	IncludeCron   bool
	IncludeState  bool
	IncludeConfig bool
}

type ExportResult struct {
	OutputPath string
	SessionKey string
	BundleRoot string
}

type exportManifest struct {
	Kind       string `json:"kind"`
	Version    int    `json:"version"`
	ExportedAt string `json:"exported_at"`

	XClaw struct {
		Version string `json:"version"`
	} `json:"x_claw"`

	Workspace struct {
		Path string `json:"path"`
	} `json:"workspace"`

	LastActive struct {
		Raw     string `json:"raw,omitempty"`
		Channel string `json:"channel,omitempty"`
		ChatID  string `json:"chat_id,omitempty"`
	} `json:"last_active"`

	Session struct {
		Key          string `json:"key"`
		Kind         string `json:"kind,omitempty"`
		CreatedAt    string `json:"created_at,omitempty"`
		UpdatedAt    string `json:"updated_at,omitempty"`
		MessageCount int    `json:"message_count,omitempty"`
		SummaryChars int    `json:"summary_chars,omitempty"`
		SelectedBy   string `json:"selected_by,omitempty"`
	} `json:"session"`

	Includes struct {
		Trace  bool `json:"trace"`
		Cron   bool `json:"cron"`
		State  bool `json:"state"`
		Config bool `json:"config_redacted"`
	} `json:"includes"`

	Files   []exportFileRecord `json:"files,omitempty"`
	Skipped []exportFileRecord `json:"skipped,omitempty"`
	Notes   []string           `json:"notes,omitempty"`
}

type exportFileRecord struct {
	Path   string `json:"path"`
	Source string `json:"source,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func RunExport(opts ExportOptions) (*ExportResult, error) {
	cfg, err := internal.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("error loading config: %w", err)
	}
	workspace := strings.TrimSpace(cfg.WorkspacePath())
	if workspace == "" {
		return nil, errors.New("workspace path is empty")
	}

	sessionsDir := filepath.Join(workspace, "sessions")
	sm := session.NewSessionManager(sessionsDir)

	selection := "explicit"
	sessionKey := strings.TrimSpace(opts.SessionKey)
	if sessionKey == "" {
		if !opts.UseLastActive {
			return nil, errors.New("either --session or --last-active is required")
		}
		var picked string
		var by string
		picked, by, err = selectSessionFromLastActive(workspace, sm)
		if err != nil {
			return nil, err
		}
		sessionKey = picked
		selection = by
	}

	snapshot, ok := sm.GetSessionSnapshot(sessionKey)
	if !ok {
		return nil, fmt.Errorf("session %q not found under %s", sessionKey, sessionsDir)
	}

	ts := time.Now().UTC().Format("20060102T150405Z")
	bundleRoot := fmt.Sprintf("x_claw_export_%s_%s", ts, tools.SafePathToken(sessionKey))

	outPath := strings.TrimSpace(opts.OutPath)
	if outPath == "" {
		outDir := filepath.Join(workspace, "exports")
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create exports dir: %w", err)
		}
		outPath = filepath.Join(outDir, bundleRoot+".zip")
	} else {
		if !strings.HasSuffix(strings.ToLower(outPath), ".zip") {
			outPath += ".zip"
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return nil, fmt.Errorf("failed to create output dir: %w", err)
		}
	}

	f, err := os.Create(outPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create output file: %w", err)
	}
	defer func() { _ = f.Close() }()

	zw := zip.NewWriter(f)
	builder := &zipBundleBuilder{
		zw:         zw,
		bundleRoot: bundleRoot,
	}

	manifest := exportManifest{
		Kind:       "x_claw_export",
		Version:    1,
		ExportedAt: time.Now().Format(time.RFC3339Nano),
		Files:      []exportFileRecord{},
		Skipped:    []exportFileRecord{},
		Notes:      []string{},
	}
	manifest.XClaw.Version = internal.FormatVersion()
	manifest.Workspace.Path = workspace
	manifest.Session.Key = sessionKey
	manifest.Session.Kind = classifySessionKind(sessionKey)
	manifest.Session.SelectedBy = selection
	manifest.Session.CreatedAt = snapshot.Created.Format(time.RFC3339)
	manifest.Session.UpdatedAt = snapshot.Updated.Format(time.RFC3339)
	manifest.Session.MessageCount = len(snapshot.Messages)
	manifest.Session.SummaryChars = len(snapshot.Summary)
	manifest.Includes.Trace = opts.IncludeTrace
	manifest.Includes.Cron = opts.IncludeCron
	manifest.Includes.State = opts.IncludeState
	manifest.Includes.Config = opts.IncludeConfig

	lastRaw, lastCh, lastID := readLastActive(workspace)
	manifest.LastActive.Raw = lastRaw
	manifest.LastActive.Channel = lastCh
	manifest.LastActive.ChatID = lastID

	// 1) manifest is written last (after files list is populated).

	// 2) session snapshot
	sessionPayload, err := cliutil.MarshalIndentNoEscape(snapshot)
	if err != nil {
		return nil, fmt.Errorf("failed to encode session snapshot: %w", err)
	}
	builder.addBytes(&manifest, "session.json", "sessions/"+sessionKey, sessionPayload, 0o644)

	// 3) tool traces
	if opts.IncludeTrace {
		traceDir := filepath.Join(workspace, ".x-claw", "audit", "tools", tools.SafePathToken(sessionKey))
		if err := builder.addDir(&manifest, "tool_trace", traceDir); err != nil {
			return nil, err
		}

		// Phase E1: run-level checkpoint trace (append-only events).
		runDir := filepath.Join(workspace, ".x-claw", "audit", "runs", tools.SafePathToken(sessionKey))
		if err := builder.addDir(&manifest, "run_trace", runDir); err != nil {
			return nil, err
		}
	}

	// 4) cron store
	if opts.IncludeCron {
		cronPath := filepath.Join(workspace, "cron", "jobs.json")
		builder.addFile(&manifest, "cron/jobs.json", cronPath)
	}

	// 5) state file (last_active)
	if opts.IncludeState {
		statePath := filepath.Join(workspace, "state", "state.json")
		builder.addFile(&manifest, "state/state.json", statePath)
	}

	// 6) config snapshot (redacted)
	if opts.IncludeConfig {
		configPath := internal.GetConfigPath()
		redacted, redactionNote, err := readRedactedConfig(configPath)
		if err != nil {
			builder.skip(&manifest, "config.redacted.json", configPath, err.Error())
		} else {
			if redactionNote != "" {
				manifest.Notes = append(manifest.Notes, redactionNote)
			}
			builder.addBytes(&manifest, "config.redacted.json", configPath, redacted, 0o600)
		}
	}

	manifestBytes, err := cliutil.MarshalIndentNoEscape(manifest)
	if err != nil {
		return nil, fmt.Errorf("failed to encode manifest: %w", err)
	}
	builder.addBytes(&manifest, "manifest.json", "", manifestBytes, 0o644)

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("failed to finalize zip: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("failed to close output file: %w", err)
	}

	return &ExportResult{
		OutputPath: outPath,
		SessionKey: sessionKey,
		BundleRoot: bundleRoot,
	}, nil
}

type zipBundleBuilder struct {
	zw         *zip.Writer
	bundleRoot string
}

func (b *zipBundleBuilder) zipPath(rel string) string {
	rel = strings.TrimLeft(rel, "/")
	if strings.TrimSpace(rel) == "" {
		return b.bundleRoot
	}
	return filepath.ToSlash(filepath.Join(b.bundleRoot, rel))
}

func (b *zipBundleBuilder) addBytes(manifest *exportManifest, destRelPath string, source string, data []byte, mode fs.FileMode) {
	if manifest == nil || b == nil || b.zw == nil {
		return
	}
	zp := b.zipPath(destRelPath)
	hdr := &zip.FileHeader{
		Name:   zp,
		Method: zip.Deflate,
	}
	hdr.SetMode(mode)
	hdr.Modified = time.Now()

	w, err := b.zw.CreateHeader(hdr)
	if err != nil {
		manifest.Skipped = append(manifest.Skipped, exportFileRecord{
			Path:   destRelPath,
			Source: source,
			Reason: err.Error(),
		})
		return
	}
	if _, err := w.Write(data); err != nil {
		manifest.Skipped = append(manifest.Skipped, exportFileRecord{
			Path:   destRelPath,
			Source: source,
			Reason: err.Error(),
		})
		return
	}

	manifest.Files = append(manifest.Files, exportFileRecord{
		Path:   destRelPath,
		Source: source,
		Bytes:  int64(len(data)),
	})
}

func (b *zipBundleBuilder) addFile(manifest *exportManifest, destRelPath string, srcPath string) {
	if manifest == nil || b == nil || b.zw == nil {
		return
	}

	info, err := os.Stat(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			b.skip(manifest, destRelPath, srcPath, "not found")
			return
		}
		b.skip(manifest, destRelPath, srcPath, err.Error())
		return
	}
	if info.IsDir() {
		b.skip(manifest, destRelPath, srcPath, "is a directory")
		return
	}

	f, err := os.Open(srcPath)
	if err != nil {
		b.skip(manifest, destRelPath, srcPath, err.Error())
		return
	}
	defer func() { _ = f.Close() }()

	zp := b.zipPath(destRelPath)
	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		b.skip(manifest, destRelPath, srcPath, err.Error())
		return
	}
	hdr.Name = zp
	hdr.Method = zip.Deflate

	w, err := b.zw.CreateHeader(hdr)
	if err != nil {
		b.skip(manifest, destRelPath, srcPath, err.Error())
		return
	}

	n, err := io.Copy(w, f)
	if err != nil {
		b.skip(manifest, destRelPath, srcPath, err.Error())
		return
	}

	manifest.Files = append(manifest.Files, exportFileRecord{
		Path:   destRelPath,
		Source: srcPath,
		Bytes:  n,
	})
}

func (b *zipBundleBuilder) addDir(manifest *exportManifest, destRelDir string, srcDir string) error {
	if manifest == nil || b == nil || b.zw == nil {
		return nil
	}

	info, err := os.Stat(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			b.skip(manifest, destRelDir+"/", srcDir, "not found")
			return nil
		}
		return fmt.Errorf("stat %s: %w", srcDir, err)
	}
	if !info.IsDir() {
		b.skip(manifest, destRelDir+"/", srcDir, "not a directory")
		return nil
	}

	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		dest := filepath.ToSlash(filepath.Join(destRelDir, rel))
		b.addFile(manifest, dest, path)
		return nil
	})
}

func (b *zipBundleBuilder) skip(manifest *exportManifest, destRelPath, srcPath, reason string) {
	if manifest == nil {
		return
	}
	manifest.Skipped = append(manifest.Skipped, exportFileRecord{
		Path:   destRelPath,
		Source: srcPath,
		Reason: reason,
	})
}

func classifySessionKind(key string) string {
	k := strings.ToLower(strings.TrimSpace(key))
	switch {
	case k == "":
		return "other"
	case strings.HasPrefix(k, "agent:") && strings.HasSuffix(k, ":main"):
		return "main"
	case strings.Contains(k, ":subagent:"):
		return "subagent"
	case strings.Contains(k, ":group:"):
		return "group"
	case strings.Contains(k, ":channel:"):
		return "channel"
	case strings.Contains(k, ":direct:"):
		return "direct"
	case strings.HasPrefix(k, "cron:") || strings.HasPrefix(k, "cron-"):
		return "cron"
	case strings.HasPrefix(k, "hook:"):
		return "hook"
	case strings.HasPrefix(k, "node-"):
		return "node"
	case k == "heartbeat":
		return "heartbeat"
	default:
		return "other"
	}
}

func selectSessionFromLastActive(workspace string, sm *session.SessionManager) (sessionKey string, selectedBy string, err error) {
	raw, ch, chatID := readLastActive(workspace)
	if strings.TrimSpace(raw) == "" || ch == "" || chatID == "" {
		return "", "", errors.New("last_active is empty; send one message to the bot (Feishu/Telegram) then retry with --last-active")
	}

	// Strategy 1: exact match for agent-scoped group/channel sessions.
	snapshots := sm.ListSessionSnapshots()
	for _, s := range snapshots {
		if matchesChannelChatID(s.Key, ch, chatID) {
			return s.Key, "last_active.exact", nil
		}
	}

	// Strategy 2: fuzzy contains
	lch := strings.ToLower(ch)
	lid := strings.ToLower(chatID)
	for _, s := range snapshots {
		k := strings.ToLower(s.Key)
		if strings.Contains(k, ":"+lch+":") && strings.Contains(k, ":"+lid) {
			return s.Key, "last_active.contains", nil
		}
	}

	// Strategy 3: fallback to most recently updated session (still useful for bug reports).
	if len(snapshots) > 0 {
		return snapshots[0].Key, "last_active.fallback_recent", nil
	}

	return "", "", fmt.Errorf("no sessions found under %s", filepath.Join(workspace, "sessions"))
}

func matchesChannelChatID(sessionKey, channel, chatID string) bool {
	parts := strings.Split(strings.TrimSpace(sessionKey), ":")
	if len(parts) != 5 {
		return false
	}
	if parts[0] != "agent" {
		return false
	}
	if strings.ToLower(parts[2]) != strings.ToLower(strings.TrimSpace(channel)) {
		return false
	}
	if strings.ToLower(parts[4]) != strings.ToLower(strings.TrimSpace(chatID)) {
		return false
	}
	switch strings.ToLower(parts[3]) {
	case "group", "channel":
		return true
	default:
		return false
	}
}

func readLastActive(workspace string) (raw string, channel string, chatID string) {
	sm := state.NewManager(workspace)
	raw = strings.TrimSpace(sm.GetLastChannel())
	if raw == "" {
		return "", "", ""
	}
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return raw, "", ""
	}
	channel = strings.TrimSpace(parts[0])
	chatID = strings.TrimSpace(parts[1])
	return raw, channel, chatID
}

func readRedactedConfig(configPath string) (data []byte, note string, err error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, "", err
	}

	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// Keep the raw bytes as a fallback (still better than failing export entirely).
		return raw, "config.redacted.json is raw (failed to parse JSON, no redaction applied)", nil
	}

	redactSensitiveJSON(v)

	out, err := cliutil.MarshalIndentNoEscape(v)
	if err != nil {
		return nil, "", err
	}
	if bytes.Contains(out, []byte("sk-")) {
		note = "redaction is best-effort; double-check exported config before sharing"
	}
	return out, note, nil
}

var sensitiveKeyRe = regexp.MustCompile(`(?i)(api[_-]?key|app[_-]?secret|encrypt[_-]?key|verification[_-]?token|refresh[_-]?token|access[_-]?token|bot[_-]?token|app[_-]?token|corp[_-]?secret|client[_-]?secret|pat|password|gateway[_-]?api[_-]?key|signing[_-]?secret)`)

func redactSensitiveJSON(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, vv := range t {
			if sensitiveKeyRe.MatchString(k) {
				// Preserve empty values to avoid misleading "redacted" when it was unset.
				if s, ok := vv.(string); ok {
					if strings.TrimSpace(s) == "" {
						continue
					}
				}
				t[k] = "<redacted>"
				continue
			}
			redactSensitiveJSON(vv)
		}
	case []any:
		for i := range t {
			redactSensitiveJSON(t[i])
		}
	}
}
