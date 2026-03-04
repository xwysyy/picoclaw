package httpapi

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type SecurityHandlerOptions struct {
	APIKey string

	Workspace string

	Config *config.Config
}

type SecurityHandler struct {
	apiKey    string
	workspace string
	cfg       *config.Config
}

func NewSecurityHandler(opts SecurityHandlerOptions) *SecurityHandler {
	ws := strings.TrimSpace(opts.Workspace)
	if ws == "" && opts.Config != nil {
		ws = strings.TrimSpace(opts.Config.WorkspacePath())
	}
	return &SecurityHandler{
		apiKey:    strings.TrimSpace(opts.APIKey),
		workspace: ws,
		cfg:       opts.Config,
	}
}

type securityResponse struct {
	OK        bool     `json:"ok"`
	Error     string   `json:"error,omitempty"`
	Workspace string   `json:"workspace,omitempty"`
	Timestamp string   `json:"timestamp,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`

	Gateway struct {
		Host       string `json:"host,omitempty"`
		Port       int    `json:"port,omitempty"`
		PublicBind bool   `json:"public_bind,omitempty"`
		APIKeySet  bool   `json:"api_key_set,omitempty"`
	} `json:"gateway,omitempty"`

	Sandbox struct {
		RestrictToWorkspace       bool `json:"restrict_to_workspace,omitempty"`
		AllowReadOutsideWorkspace bool `json:"allow_read_outside_workspace,omitempty"`

		ExecBackend          string `json:"exec_backend,omitempty"`
		ExecEnvMode          string `json:"exec_env_mode,omitempty"`
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

	BreakGlass struct {
		AllowPublicGateway   bool `json:"allow_public_gateway,omitempty"`
		AllowUnsafeWorkspace bool `json:"allow_unsafe_workspace,omitempty"`
		AllowUnsafeExec      bool `json:"allow_unsafe_exec,omitempty"`
		AllowExecInheritEnv  bool `json:"allow_exec_inherit_env,omitempty"`
		AllowDockerNetwork   bool `json:"allow_docker_network,omitempty"`
	} `json:"break_glass,omitempty"`
}

func (h *SecurityHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if h == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(securityResponse{OK: false, Error: "security service not configured"})
		return
	}

	// Same auth policy as /api/notify: if api_key is unset, only allow loopback.
	if strings.TrimSpace(h.apiKey) == "" {
		if !isLoopbackRemote(r.RemoteAddr) {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(securityResponse{OK: false, Error: "unauthorized"})
			return
		}
	} else {
		authorized := strings.TrimSpace(r.Header.Get("X-API-Key")) == h.apiKey
		if !authorized {
			auth := strings.TrimSpace(r.Header.Get("Authorization"))
			if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
				token := strings.TrimSpace(auth[7:])
				authorized = token != "" && token == h.apiKey
			}
		}
		if !authorized {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(securityResponse{OK: false, Error: "unauthorized"})
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		// ok
	default:
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(securityResponse{OK: false, Error: "method not allowed"})
		return
	}

	resp := securityResponse{
		OK:        true,
		Workspace: strings.TrimSpace(h.workspace),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Warnings:  []string{},
	}

	cfg := h.cfg
	if cfg != nil {
		resp.Workspace = strings.TrimSpace(cfg.WorkspacePath())

		resp.Gateway.Host = strings.TrimSpace(cfg.Gateway.Host)
		resp.Gateway.Port = cfg.Gateway.Port
		resp.Gateway.APIKeySet = strings.TrimSpace(cfg.Gateway.APIKey) != ""
		resp.Gateway.PublicBind = !isLoopbackHost(resp.Gateway.Host)

		resp.Sandbox.RestrictToWorkspace = cfg.Agents.Defaults.RestrictToWorkspace
		resp.Sandbox.AllowReadOutsideWorkspace = cfg.Agents.Defaults.AllowReadOutsideWorkspace
		resp.Sandbox.ExecBackend = strings.TrimSpace(cfg.Tools.Exec.Backend)
		resp.Sandbox.ExecEnvMode = strings.TrimSpace(cfg.Tools.Exec.Env.Mode)
		resp.Sandbox.DockerImage = strings.TrimSpace(cfg.Tools.Exec.Docker.Image)
		resp.Sandbox.DockerNetwork = strings.TrimSpace(cfg.Tools.Exec.Docker.Network)
		resp.Sandbox.DockerReadOnlyRootFS = cfg.Tools.Exec.Docker.ReadOnlyRootFS

		resp.Limits.Enabled = cfg.Limits.Enabled
		resp.Limits.MaxRunWallTimeSeconds = cfg.Limits.MaxRunWallTimeSeconds
		resp.Limits.MaxToolCallsPerRun = cfg.Limits.MaxToolCallsPerRun
		resp.Limits.MaxToolResultChars = cfg.Limits.MaxToolResultChars
		resp.Limits.MaxReadFileBytes = cfg.Limits.MaxReadFileBytes

		resp.AuditLog.Enabled = cfg.AuditLog.Enabled
		resp.AuditLog.Dir = strings.TrimSpace(cfg.AuditLog.Dir)
		resp.AuditLog.MaxBytes = cfg.AuditLog.MaxBytes
		resp.AuditLog.MaxBackups = cfg.AuditLog.MaxBackups

		resp.Estop.Enabled = cfg.Tools.Estop.Enabled
		resp.Estop.FailClosed = cfg.Tools.Estop.FailClosed

		resp.BreakGlass.AllowPublicGateway = cfg.Security.BreakGlass.AllowPublicGateway
		resp.BreakGlass.AllowUnsafeWorkspace = cfg.Security.BreakGlass.AllowUnsafeWorkspace
		resp.BreakGlass.AllowUnsafeExec = cfg.Security.BreakGlass.AllowUnsafeExec
		resp.BreakGlass.AllowExecInheritEnv = cfg.Security.BreakGlass.AllowExecInheritEnv
		resp.BreakGlass.AllowDockerNetwork = cfg.Security.BreakGlass.AllowDockerNetwork
	}

	workspace := strings.TrimSpace(resp.Workspace)
	if workspace == "" {
		resp.OK = false
		resp.Error = "workspace is not configured"
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	auditDir := strings.TrimSpace(resp.AuditLog.Dir)
	if auditDir == "" {
		auditDir = filepath.Join(workspace, ".picoclaw", "audit")
	}
	resp.AuditLog.Path = filepath.ToSlash(filepath.Join(auditDir, "audit.jsonl"))
	if _, err := os.Stat(filepath.Join(auditDir, "audit.jsonl")); err == nil {
		resp.AuditLog.Exists = true
	}

	if resp.Estop.Enabled {
		st, err := tools.LoadEstopState(workspace)
		if err != nil && resp.Estop.FailClosed {
			st = tools.EstopState{Mode: tools.EstopModeKillAll, Note: "fail-closed: " + err.Error()}.Normalized()
			err = nil
		}
		if err != nil {
			resp.Estop.StateError = err.Error()
		}
		resp.Estop.State = st
	}

	if !resp.Limits.Enabled {
		resp.Warnings = append(resp.Warnings, "limits are disabled (risk of runaway runs / OOM)")
	}
	if !resp.AuditLog.Enabled {
		resp.Warnings = append(resp.Warnings, "audit_log is disabled (replay/audit visibility reduced)")
	} else if !resp.AuditLog.Exists {
		resp.Warnings = append(resp.Warnings, "audit_log is enabled but audit.jsonl does not exist yet")
	}
	if !resp.Estop.Enabled {
		resp.Warnings = append(resp.Warnings, "estop is disabled (no global kill switch)")
	}
	if resp.Sandbox.ExecBackend == "docker" && strings.TrimSpace(resp.Sandbox.DockerImage) == "" {
		resp.Warnings = append(resp.Warnings, "exec backend is docker but tools.exec.docker.image is empty")
	}
	if resp.Sandbox.ExecBackend == "host" && !resp.Sandbox.RestrictToWorkspace {
		resp.Warnings = append(resp.Warnings, "exec backend is host while restrict_to_workspace=false (high risk)")
	}
	if resp.Sandbox.AllowReadOutsideWorkspace {
		resp.Warnings = append(resp.Warnings, "allow_read_outside_workspace=true (may leak host files into context)")
	}
	if strings.EqualFold(strings.TrimSpace(resp.Sandbox.ExecEnvMode), "inherit") {
		resp.Warnings = append(resp.Warnings, "exec tool inherits full host env (consider tools.exec.env.mode=\"allowlist\")")
	}
	if resp.Gateway.PublicBind && !resp.BreakGlass.AllowPublicGateway {
		resp.Warnings = append(resp.Warnings, "gateway binds non-loopback but break-glass allow_public_gateway is false (config would be rejected)")
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}
