package security

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type securityReport struct {
	Kind      string `json:"kind"`
	Workspace string `json:"workspace,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`

	Sandbox struct {
		RestrictToWorkspace       bool `json:"restrict_to_workspace,omitempty"`
		AllowReadOutsideWorkspace bool `json:"allow_read_outside_workspace,omitempty"`

		ExecBackend          string `json:"exec_backend,omitempty"`
		DockerImage          string `json:"docker_image,omitempty"`
		DockerNetwork        string `json:"docker_network,omitempty"`
		DockerReadOnlyRootFS bool   `json:"docker_read_only_rootfs,omitempty"`
	} `json:"sandbox,omitempty"`

	Limits struct {
		Enabled               bool `json:"enabled,omitempty"`
		MaxRunWallTimeSeconds int  `json:"max_run_wall_time_seconds,omitempty"`
		MaxToolCallsPerRun    int  `json:"max_tool_calls_per_run,omitempty"`
		MaxToolResultChars    int  `json:"max_tool_result_chars,omitempty"`
		MaxReadFileBytes      int  `json:"max_read_file_bytes,omitempty"`
	} `json:"limits,omitempty"`

	AuditLog struct {
		Enabled    bool   `json:"enabled,omitempty"`
		Dir        string `json:"dir,omitempty"`
		Path       string `json:"path,omitempty"`
		MaxBytes   int    `json:"max_bytes,omitempty"`
		MaxBackups int    `json:"max_backups,omitempty"`
		Exists     bool   `json:"exists,omitempty"`
	} `json:"audit_log,omitempty"`

	Estop struct {
		Enabled    bool             `json:"enabled,omitempty"`
		FailClosed bool             `json:"fail_closed,omitempty"`
		State      tools.EstopState `json:"state,omitempty"`
		StateError string           `json:"state_error,omitempty"`
	} `json:"estop,omitempty"`

	Warnings []string `json:"warnings,omitempty"`
}

func securityCmd(opts securityOptions) error {
	if !opts.Check {
		return nil
	}

	cfg, err := internal.LoadConfig()
	if err != nil {
		return fmt.Errorf("error loading config: %w", err)
	}

	report := buildSecurityReport(cfg)

	if opts.JSON {
		data, err := marshalIndentNoEscape(report)
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	fmt.Println("Security Check")
	fmt.Printf("Workspace: %s\n", report.Workspace)
	fmt.Printf("Timestamp: %s\n", report.Timestamp)
	fmt.Println()

	fmt.Println("Sandbox:")
	fmt.Printf("  restrict_to_workspace: %v\n", report.Sandbox.RestrictToWorkspace)
	fmt.Printf("  allow_read_outside_workspace: %v\n", report.Sandbox.AllowReadOutsideWorkspace)
	fmt.Printf("  exec_backend: %s\n", report.Sandbox.ExecBackend)
	if strings.TrimSpace(report.Sandbox.ExecBackend) == "docker" {
		fmt.Printf("  docker.image: %s\n", report.Sandbox.DockerImage)
		fmt.Printf("  docker.network: %s\n", report.Sandbox.DockerNetwork)
		fmt.Printf("  docker.read_only_rootfs: %v\n", report.Sandbox.DockerReadOnlyRootFS)
	}
	fmt.Println()

	fmt.Println("Limits:")
	fmt.Printf("  enabled: %v\n", report.Limits.Enabled)
	fmt.Printf("  max_run_wall_time_seconds: %d\n", report.Limits.MaxRunWallTimeSeconds)
	fmt.Printf("  max_tool_calls_per_run: %d\n", report.Limits.MaxToolCallsPerRun)
	fmt.Printf("  max_tool_result_chars: %d\n", report.Limits.MaxToolResultChars)
	fmt.Printf("  max_read_file_bytes: %d\n", report.Limits.MaxReadFileBytes)
	fmt.Println()

	fmt.Println("Audit Log:")
	fmt.Printf("  enabled: %v\n", report.AuditLog.Enabled)
	fmt.Printf("  path: %s\n", report.AuditLog.Path)
	fmt.Printf("  exists: %v\n", report.AuditLog.Exists)
	fmt.Printf("  max_bytes: %d\n", report.AuditLog.MaxBytes)
	fmt.Printf("  max_backups: %d\n", report.AuditLog.MaxBackups)
	fmt.Println()

	fmt.Println("Estop:")
	fmt.Printf("  enabled: %v\n", report.Estop.Enabled)
	fmt.Printf("  fail_closed: %v\n", report.Estop.FailClosed)
	if report.Estop.Enabled {
		fmt.Printf("  mode: %s\n", report.Estop.State.Mode)
		if strings.TrimSpace(report.Estop.StateError) != "" {
			fmt.Printf("  state_error: %s\n", report.Estop.StateError)
		}
	}
	fmt.Println()

	if len(report.Warnings) == 0 {
		fmt.Println("Warnings: (none)")
		return nil
	}

	fmt.Println("Warnings:")
	for _, w := range report.Warnings {
		if strings.TrimSpace(w) == "" {
			continue
		}
		fmt.Printf("  - %s\n", w)
	}
	return nil
}

func buildSecurityReport(cfg *config.Config) securityReport {
	report := securityReport{
		Kind:      "picoclaw_security_check",
		Workspace: strings.TrimSpace(cfg.WorkspacePath()),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Warnings:  []string{},
	}

	report.Sandbox.RestrictToWorkspace = cfg.Agents.Defaults.RestrictToWorkspace
	report.Sandbox.AllowReadOutsideWorkspace = cfg.Agents.Defaults.AllowReadOutsideWorkspace
	report.Sandbox.ExecBackend = strings.TrimSpace(cfg.Tools.Exec.Backend)
	report.Sandbox.DockerImage = strings.TrimSpace(cfg.Tools.Exec.Docker.Image)
	report.Sandbox.DockerNetwork = strings.TrimSpace(cfg.Tools.Exec.Docker.Network)
	report.Sandbox.DockerReadOnlyRootFS = cfg.Tools.Exec.Docker.ReadOnlyRootFS

	report.Limits.Enabled = cfg.Limits.Enabled
	report.Limits.MaxRunWallTimeSeconds = cfg.Limits.MaxRunWallTimeSeconds
	report.Limits.MaxToolCallsPerRun = cfg.Limits.MaxToolCallsPerRun
	report.Limits.MaxToolResultChars = cfg.Limits.MaxToolResultChars
	report.Limits.MaxReadFileBytes = cfg.Limits.MaxReadFileBytes

	report.AuditLog.Enabled = cfg.AuditLog.Enabled
	report.AuditLog.Dir = strings.TrimSpace(cfg.AuditLog.Dir)
	report.AuditLog.MaxBytes = cfg.AuditLog.MaxBytes
	report.AuditLog.MaxBackups = cfg.AuditLog.MaxBackups

	report.Estop.Enabled = cfg.Tools.Estop.Enabled
	report.Estop.FailClosed = cfg.Tools.Estop.FailClosed

	workspace := strings.TrimSpace(report.Workspace)
	if workspace != "" {
		auditDir := strings.TrimSpace(report.AuditLog.Dir)
		if auditDir == "" {
			auditDir = filepath.Join(workspace, ".picoclaw", "audit")
		}
		report.AuditLog.Path = filepath.Join(auditDir, "audit.jsonl")
		if _, err := os.Stat(report.AuditLog.Path); err == nil {
			report.AuditLog.Exists = true
		}

		if report.Estop.Enabled {
			st, err := tools.LoadEstopState(workspace)
			if err != nil && report.Estop.FailClosed {
				st = tools.EstopState{Mode: tools.EstopModeKillAll, Note: "fail-closed: " + err.Error()}.Normalized()
				err = nil
			}
			if err != nil {
				report.Estop.StateError = err.Error()
			}
			report.Estop.State = st
		}
	}

	if !report.Limits.Enabled {
		report.Warnings = append(report.Warnings, "limits are disabled (risk of runaway runs / OOM)")
	}
	if !report.AuditLog.Enabled {
		report.Warnings = append(report.Warnings, "audit_log is disabled (replay/audit visibility reduced)")
	} else if !report.AuditLog.Exists {
		report.Warnings = append(report.Warnings, "audit_log is enabled but audit.jsonl does not exist yet")
	}
	if !report.Estop.Enabled {
		report.Warnings = append(report.Warnings, "estop is disabled (no global kill switch)")
	}
	if report.Sandbox.ExecBackend == "docker" && strings.TrimSpace(report.Sandbox.DockerImage) == "" {
		report.Warnings = append(report.Warnings, "exec backend is docker but tools.exec.docker.image is empty")
	}
	if report.Sandbox.ExecBackend == "host" && !report.Sandbox.RestrictToWorkspace {
		report.Warnings = append(report.Warnings, "exec backend is host while restrict_to_workspace=false (high risk)")
	}
	if report.Sandbox.AllowReadOutsideWorkspace {
		report.Warnings = append(report.Warnings, "allow_read_outside_workspace=true (may leak host files into context)")
	}

	return report
}

func marshalIndentNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
