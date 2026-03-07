package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/xwysyy/X-Claw/pkg/config"
	"github.com/xwysyy/X-Claw/pkg/logger"
)

type ExecTool struct {
	workingDir          string
	timeout             time.Duration
	denyPatterns        []*regexp.Regexp
	allowPatterns       []*regexp.Regexp
	customAllowPatterns []*regexp.Regexp
	restrictToWorkspace bool
	processes           *ProcessManager

	backend string // host | docker

	envMode  string
	envAllow map[string]bool

	hostMemoryMB   int
	hostCPUSeconds int
	hostFileSizeMB int
	hostNProc      int

	dockerImage          string
	dockerNetwork        string
	dockerReadOnlyRootFS bool
	dockerMemoryMB       int
	dockerCPUs           float64
	dockerPidsLimit      int
}

var (
	defaultDenyPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\brm\s+-[rf]{1,2}\b`),
		regexp.MustCompile(`\bdel\s+/[fq]\b`),
		regexp.MustCompile(`\brmdir\s+/s\b`),
		// Match disk wiping commands (must be followed by space/args).
		regexp.MustCompile(`\b(format|mkfs|diskpart)\b\s`),
		regexp.MustCompile(`\bdd\s+if=`),
		// Block writes to block devices (all common naming schemes).
		regexp.MustCompile(
			`>\s*/dev/(sd[a-z]|hd[a-z]|vd[a-z]|xvd[a-z]|nvme\d|mmcblk\d|loop\d|dm-\d|md\d|sr\d|nbd\d)`,
		),
		regexp.MustCompile(`\b(shutdown|reboot|poweroff)\b`),
		regexp.MustCompile(`:\(\)\s*\{.*\};\s*:`),
		regexp.MustCompile(`\$\([^)]+\)`),
		regexp.MustCompile(`\$\{[^}]+\}`),
		regexp.MustCompile("`[^`]+`"),
		regexp.MustCompile(`\|\s*sh\b`),
		regexp.MustCompile(`\|\s*bash\b`),
		regexp.MustCompile(`;\s*rm\s+-[rf]`),
		regexp.MustCompile(`&&\s*rm\s+-[rf]`),
		regexp.MustCompile(`\|\|\s*rm\s+-[rf]`),
		regexp.MustCompile(`<<\s*EOF`),
		regexp.MustCompile(`\$\(\s*cat\s+`),
		regexp.MustCompile(`\$\(\s*curl\s+`),
		regexp.MustCompile(`\$\(\s*wget\s+`),
		regexp.MustCompile(`\$\(\s*which\s+`),
		regexp.MustCompile(`\bsudo\b`),
		regexp.MustCompile(`\bchmod\s+[0-7]{3,4}\b`),
		regexp.MustCompile(`\bchown\b`),
		regexp.MustCompile(`\bpkill\b`),
		regexp.MustCompile(`\bkillall\b`),
		regexp.MustCompile(`\bkill\s+-[9]\b`),
		regexp.MustCompile(`\bcurl\b.*\|\s*(sh|bash)`),
		regexp.MustCompile(`\bwget\b.*\|\s*(sh|bash)`),
		regexp.MustCompile(`\bnpm\s+install\s+-g\b`),
		regexp.MustCompile(`\bpip\s+install\s+--user\b`),
		regexp.MustCompile(`\bapt\s+(install|remove|purge)\b`),
		regexp.MustCompile(`\byum\s+(install|remove)\b`),
		regexp.MustCompile(`\bdnf\s+(install|remove)\b`),
		regexp.MustCompile(`\bdocker\s+run\b`),
		regexp.MustCompile(`\bdocker\s+exec\b`),
		regexp.MustCompile(`\bgit\s+push\b`),
		regexp.MustCompile(`\bgit\s+force\b`),
		regexp.MustCompile(`\bssh\b.*@`),
		regexp.MustCompile(`\beval\b`),
		regexp.MustCompile(`\bsource\s+.*\.sh\b`),
	}

	// absolutePathPattern matches absolute file paths in commands (Unix and Windows).
	absolutePathPattern = regexp.MustCompile(`[A-Za-z]:\\[^\\\"']+|/[^\s\"']+`)

	envVarNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

	// safePaths are kernel pseudo-devices that are always safe to reference in
	// commands, regardless of workspace restriction. They contain no user data
	// and cannot cause destructive writes.
	safePaths = map[string]bool{
		"/dev/null":    true,
		"/dev/zero":    true,
		"/dev/random":  true,
		"/dev/urandom": true,
		"/dev/stdin":   true,
		"/dev/stdout":  true,
		"/dev/stderr":  true,
	}
)

func NewExecTool(workingDir string, restrict bool) (*ExecTool, error) {
	return NewExecToolWithConfig(workingDir, restrict, nil)
}

func NewExecToolWithConfig(workingDir string, restrict bool, config *config.Config) (*ExecTool, error) {
	denyPatterns := make([]*regexp.Regexp, 0)
	customAllowPatterns := make([]*regexp.Regexp, 0)
	backend := "host"
	envMode := "inherit"
	envAllow := map[string]bool{}
	hostMemoryMB := 0
	hostCPUSeconds := 0
	hostFileSizeMB := 0
	hostNProc := 0
	dockerImage := ""
	dockerNetwork := ""
	dockerReadOnly := false
	dockerMemoryMB := 0
	dockerCPUs := 0.0
	dockerPidsLimit := 0

	if config != nil {
		execConfig := config.Tools.Exec
		enableDenyPatterns := execConfig.EnableDenyPatterns
		if enableDenyPatterns {
			denyPatterns = append(denyPatterns, defaultDenyPatterns...)
			if len(execConfig.CustomDenyPatterns) > 0 {
				logger.InfoCF("tools/shell", "Using custom deny patterns", map[string]any{
					"patterns": execConfig.CustomDenyPatterns,
				})
				for _, pattern := range execConfig.CustomDenyPatterns {
					re, err := regexp.Compile(pattern)
					if err != nil {
						return nil, fmt.Errorf("invalid custom deny pattern %q: %w", pattern, err)
					}
					denyPatterns = append(denyPatterns, re)
				}
			}
		} else {
			logger.WarnCF("tools/shell", "Deny patterns disabled, all commands allowed", nil)
		}
		for _, pattern := range execConfig.CustomAllowPatterns {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf("invalid custom allow pattern %q: %w", pattern, err)
			}
			customAllowPatterns = append(customAllowPatterns, re)
		}

		backend = strings.ToLower(strings.TrimSpace(execConfig.Backend))
		if backend == "" {
			backend = "host"
		}
		switch backend {
		case "host", "docker":
			// ok
		default:
			return nil, fmt.Errorf("invalid tools.exec.backend %q (expected \"host\" or \"docker\")", execConfig.Backend)
		}

		envMode = strings.ToLower(strings.TrimSpace(execConfig.Env.Mode))
		if envMode == "" {
			envMode = "inherit"
		}
		switch envMode {
		case "inherit", "allowlist":
			// ok
		default:
			return nil, fmt.Errorf("invalid tools.exec.env.mode %q (expected \"inherit\" or \"allowlist\")", execConfig.Env.Mode)
		}
		envAllow = buildExecEnvAllowMap(execConfig.Env.EnvAllow)

		hostMemoryMB = execConfig.HostLimits.MemoryMB
		hostCPUSeconds = execConfig.HostLimits.CPUSeconds
		hostFileSizeMB = execConfig.HostLimits.FileSizeMB
		hostNProc = execConfig.HostLimits.NProc

		dockerImage = strings.TrimSpace(execConfig.Docker.Image)
		dockerNetwork = strings.TrimSpace(execConfig.Docker.Network)
		if dockerNetwork == "" {
			dockerNetwork = "none"
		}
		dockerReadOnly = execConfig.Docker.ReadOnlyRootFS
		dockerMemoryMB = execConfig.Docker.MemoryMB
		dockerCPUs = execConfig.Docker.CPUs
		dockerPidsLimit = execConfig.Docker.PidsLimit
	} else {
		denyPatterns = append(denyPatterns, defaultDenyPatterns...)
	}

	return &ExecTool{
		workingDir:           workingDir,
		timeout:              60 * time.Second,
		denyPatterns:         denyPatterns,
		allowPatterns:        nil,
		customAllowPatterns:  customAllowPatterns,
		restrictToWorkspace:  restrict,
		processes:            NewProcessManager(defaultProcessMaxOutputChars),
		backend:              backend,
		envMode:              envMode,
		envAllow:             envAllow,
		hostMemoryMB:         hostMemoryMB,
		hostCPUSeconds:       hostCPUSeconds,
		hostFileSizeMB:       hostFileSizeMB,
		hostNProc:            hostNProc,
		dockerImage:          dockerImage,
		dockerNetwork:        dockerNetwork,
		dockerReadOnlyRootFS: dockerReadOnly,
		dockerMemoryMB:       dockerMemoryMB,
		dockerCPUs:           dockerCPUs,
		dockerPidsLimit:      dockerPidsLimit,
	}, nil
}

var platformExecEnvAllow = []string{
	"PATH",
	"HOME",
	"USER",
	"LOGNAME",
	"SHELL",
	"LANG",
	"LC_ALL",
	"LC_CTYPE",
	"TERM",
	"TZ",
	"TMPDIR",
	// Proxy support.
	"HTTP_PROXY",
	"HTTPS_PROXY",
	"NO_PROXY",
	"http_proxy",
	"https_proxy",
	"no_proxy",
}

func buildExecEnvAllowMap(configAllow []string) map[string]bool {
	allow := make(map[string]bool, len(platformExecEnvAllow)+len(configAllow))
	for _, name := range platformExecEnvAllow {
		name = strings.TrimSpace(name)
		if name != "" {
			allow[name] = true
		}
	}
	for _, name := range configAllow {
		name = strings.TrimSpace(name)
		if envVarNamePattern.MatchString(name) {
			allow[name] = true
		}
	}
	return allow
}

func filterExecEnv(env []string, allow map[string]bool) []string {
	if len(env) == 0 || len(allow) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(env))
	for _, kv := range env {
		if kv == "" {
			continue
		}
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		name := kv[:eq]
		if allow[name] {
			filtered = append(filtered, kv)
		}
	}
	return filtered
}

func (t *ExecTool) applyEnvPolicy(cmd *exec.Cmd) {
	if t == nil || cmd == nil {
		return
	}
	// Only apply env filtering for host backend. For docker backend, the executed
	// user command runs inside a container and we do not pass env; filtering the
	// docker CLI environment can break rootless/remote docker setups.
	if strings.EqualFold(strings.TrimSpace(t.backend), "docker") {
		return
	}
	if strings.EqualFold(strings.TrimSpace(t.envMode), "inherit") {
		// Leave cmd.Env nil → inherit everything.
		return
	}
	cmd.Env = filterExecEnv(os.Environ(), t.envAllow)
}

func (t *ExecTool) Name() string {
	return "exec"
}

func (t *ExecTool) Description() string {
	return "Execute a shell command in the workspace directory and return stdout/stderr. " +
		"Input: command (string, required). " +
		"Output: stdout content, with stderr appended if present. Includes exit code on failure. " +
		"Constraints: Dangerous commands (rm -rf, sudo, etc.) are blocked. " +
		"Commands are restricted to the workspace directory. " +
		"Default timeout: 60 seconds (override with timeout_seconds). " +
		"Use background=true for long-running commands. " +
		"When NOT to use: for reading file content (use read_file instead), for writing files (use write_file/edit_file)."
}

func (t *ExecTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command to execute",
			},
			"working_dir": map[string]any{
				"type":        "string",
				"description": "Optional working directory for the command",
			},
			"background": map[string]any{
				"type":        "boolean",
				"description": "Start command in background and manage it via process tool",
			},
			"yield_ms": map[string]any{
				"type":        "integer",
				"description": "Wait this many milliseconds before returning running status",
				"minimum":     0.0,
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "Override command timeout in seconds (0 disables timeout)",
				"minimum":     0.0,
			},
		},
		"required": []string{"command"},
	}
}

func (t *ExecTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	command, ok := args["command"].(string)
	if !ok {
		return ErrorResult("command is required")
	}

	cwd := t.workingDir
	if wd, ok := args["working_dir"].(string); ok && wd != "" {
		if t.restrictToWorkspace && t.workingDir != "" {
			resolvedWD, err := validatePath(wd, t.workingDir, true)
			if err != nil {
				return ErrorResult("Command blocked by safety guard (" + err.Error() + ")")
			}
			cwd = resolvedWD
		} else {
			cwd = wd
		}
	}

	if cwd == "" {
		wd, err := os.Getwd()
		if err == nil {
			cwd = wd
		}
	}

	if guardError := t.guardCommand(command, cwd); guardError != "" {
		return ErrorResult(guardError)
	}

	background, err := parseBoolArg(args, "background", false)
	if err != nil {
		return ErrorResult(err.Error())
	}

	yieldMS, err := parseOptionalIntArg(args, "yield_ms", 0, 0, 60*60*1000)
	if err != nil {
		return ErrorResult(err.Error())
	}

	timeoutSeconds, hasTimeoutOverride, err := readOptionalIntArg(args, "timeout_seconds", 0, 24*60*60)
	if err != nil {
		return ErrorResult(err.Error())
	}

	timeout := t.timeout
	if hasTimeoutOverride {
		if timeoutSeconds == 0 {
			timeout = 0
		} else {
			timeout = time.Duration(timeoutSeconds) * time.Second
		}
	}

	if !background && yieldMS <= 0 {
		if strings.EqualFold(strings.TrimSpace(t.backend), "docker") {
			if !t.restrictToWorkspace {
				return ErrorResult("docker sandbox requires restrict_to_workspace=true")
			}
			return t.executeDockerSync(ctx, command, cwd, timeout)
		}
		return t.executeSync(ctx, command, cwd, timeout)
	}

	if strings.EqualFold(strings.TrimSpace(t.backend), "docker") {
		return ErrorResult("docker exec backend does not support background/yield mode (set tools.exec.backend=host)")
	}

	return t.executeManaged(
		ctx,
		command,
		cwd,
		background,
		time.Duration(yieldMS)*time.Millisecond,
		timeout,
	)
}

func (t *ExecTool) executeDockerSync(ctx context.Context, command, cwd string, timeout time.Duration) *ToolResult {
	image := strings.TrimSpace(t.dockerImage)
	if image == "" {
		return ErrorResult("docker backend selected but tools.exec.docker.image is empty")
	}
	workspace := strings.TrimSpace(t.workingDir)
	if workspace == "" {
		return ErrorResult("docker backend requires a non-empty workingDir")
	}

	// timeout == 0 means no timeout.
	var cmdCtx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		cmdCtx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		cmdCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	containerWD := "/workspace"
	if strings.TrimSpace(cwd) != "" && strings.TrimSpace(workspace) != "" {
		if rel, err := filepath.Rel(workspace, cwd); err == nil {
			rel = strings.TrimSpace(rel)
			if rel != "" && rel != "." && !strings.HasPrefix(rel, "..") {
				containerWD = filepath.ToSlash(filepath.Join(containerWD, rel))
			}
		}
	}

	network := strings.TrimSpace(t.dockerNetwork)
	if network == "" {
		network = "none"
	}

	args := []string{
		"run",
		"--rm",
		"--init",
		"--network", network,
		"-v", fmt.Sprintf("%s:/workspace", workspace),
		"-w", containerWD,
	}
	if t.dockerMemoryMB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", t.dockerMemoryMB))
	}
	if t.dockerCPUs > 0 {
		args = append(args, "--cpus", fmt.Sprintf("%g", t.dockerCPUs))
	}
	if t.dockerPidsLimit > 0 {
		args = append(args, "--pids-limit", fmt.Sprintf("%d", t.dockerPidsLimit))
	}
	if t.dockerReadOnlyRootFS {
		args = append(args,
			"--read-only",
			"--tmpfs", "/tmp:rw,size=64m",
			"--tmpfs", "/var/tmp:rw,size=64m",
		)
	}
	// Run the command via a shell for compatibility with existing behavior.
	args = append(args, image, "sh", "-lc", command)

	cmd := exec.CommandContext(cmdCtx, "docker", args...)
	prepareCommandForTermination(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return ErrorResult(fmt.Sprintf("failed to start docker sandbox: %v", err))
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	var err error
	select {
	case err = <-done:
	case <-cmdCtx.Done():
		if termErr := terminateProcessTree(cmd); termErr != nil {
			logger.DebugCF("tools/shell", "terminate process tree failed", map[string]any{"error": termErr.Error()})
		}
		select {
		case err = <-done:
		case <-time.After(2 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			err = <-done
		}
	}

	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\nSTDERR:\n" + stderr.String()
	}

	if err != nil {
		if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
			msg := fmt.Sprintf("Command timed out after %v", timeout)
			return &ToolResult{
				ForLLM:  msg,
				ForUser: msg,
				IsError: true,
			}
		}
		output += fmt.Sprintf("\nExit code: %v", err)
	}

	output = truncateExecOutput(output)

	if err != nil {
		return &ToolResult{
			ForLLM:  output,
			ForUser: output,
			IsError: true,
		}
	}

	return &ToolResult{
		ForLLM:  output,
		ForUser: output,
		IsError: false,
	}
}

func (t *ExecTool) executeSync(ctx context.Context, command, cwd string, timeout time.Duration) *ToolResult {
	// timeout == 0 means no timeout.
	var cmdCtx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		cmdCtx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		cmdCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	cmd := shellCommand(cmdCtx, t.withHostLimits(command))
	if cwd != "" {
		cmd.Dir = cwd
	}
	t.applyEnvPolicy(cmd)

	prepareCommandForTermination(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return ErrorResult(fmt.Sprintf("failed to start command: %v", err))
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	var err error
	select {
	case err = <-done:
	case <-cmdCtx.Done():
		if termErr := terminateProcessTree(cmd); termErr != nil {
			logger.DebugCF("tools/shell", "terminate process tree failed", map[string]any{"error": termErr.Error()})
		}
		select {
		case err = <-done:
		case <-time.After(2 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			err = <-done
		}
	}

	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\nSTDERR:\n" + stderr.String()
	}

	if err != nil {
		if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
			msg := fmt.Sprintf("Command timed out after %v", timeout)
			return &ToolResult{
				ForLLM:  msg,
				ForUser: msg,
				IsError: true,
			}
		}
		output += fmt.Sprintf("\nExit code: %v", err)
	}

	output = truncateExecOutput(output)

	if err != nil {
		return &ToolResult{
			ForLLM:  output,
			ForUser: output,
			IsError: true,
		}
	}

	return &ToolResult{
		ForLLM:  output,
		ForUser: output,
		IsError: false,
	}
}

func (t *ExecTool) executeManaged(
	ctx context.Context,
	command, cwd string,
	background bool,
	yield time.Duration,
	timeout time.Duration,
) *ToolResult {
	if t.processes == nil {
		return t.executeSync(ctx, command, cwd, timeout)
	}

	baseCtx := context.Background()
	var cmdCtx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		cmdCtx, cancel = context.WithTimeout(baseCtx, timeout)
	} else {
		cmdCtx, cancel = context.WithCancel(baseCtx)
	}

	cmd := shellCommand(cmdCtx, t.withHostLimits(command))
	if cwd != "" {
		cmd.Dir = cwd
	}
	t.applyEnvPolicy(cmd)
	prepareCommandForTermination(cmd)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return ErrorResult(fmt.Sprintf("failed to attach stdout: %v", err))
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return ErrorResult(fmt.Sprintf("failed to attach stderr: %v", err))
	}
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return ErrorResult(fmt.Sprintf("failed to attach stdin: %v", err))
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return ErrorResult(fmt.Sprintf("failed to start command: %v", err))
	}

	sessionID := t.processes.StartSession(command, cwd, cmd, stdinPipe, cancel)
	done := make(chan struct{})
	go t.watchManagedCommand(sessionID, cmdCtx, cmd, stdoutPipe, stderrPipe, done)

	if background {
		return t.runningSessionResult(sessionID)
	}

	if yield <= 0 {
		yield = 10 * time.Second
	}
	timer := time.NewTimer(yield)
	defer timer.Stop()

	select {
	case <-done:
		pollResult, err := t.processes.Poll(sessionID, 0)
		if err != nil {
			return ErrorResult(fmt.Sprintf("failed to read managed command result: %v", err))
		}
		return formatManagedCompletion(pollResult, timeout)
	case <-timer.C:
		return t.runningSessionResult(sessionID)
	case <-ctx.Done():
		_, _ = t.processes.Kill(sessionID)
		return ErrorResult(fmt.Sprintf("command canceled: %v", ctx.Err()))
	}
}

func (t *ExecTool) watchManagedCommand(
	sessionID string,
	cmdCtx context.Context,
	cmd *exec.Cmd,
	stdoutPipe, stderrPipe io.ReadCloser,
	done chan<- struct{},
) {
	defer close(done)

	stdoutDone := make(chan struct{})
	stderrDone := make(chan struct{})
	go func() {
		t.streamManagedOutput(sessionID, stdoutPipe, false)
		close(stdoutDone)
	}()
	go func() {
		t.streamManagedOutput(sessionID, stderrPipe, true)
		close(stderrDone)
	}()

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	var waitErr error
	select {
	case waitErr = <-waitDone:
	case <-cmdCtx.Done():
		if termErr := terminateProcessTree(cmd); termErr != nil {
			logger.DebugCF("tools/shell", "terminate process tree failed", map[string]any{"error": termErr.Error()})
		}
		select {
		case waitErr = <-waitDone:
		case <-time.After(2 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			waitErr = <-waitDone
		}
	}

	<-stdoutDone
	<-stderrDone

	t.processes.MarkExited(sessionID, waitErr, errors.Is(cmdCtx.Err(), context.DeadlineExceeded))
}

func (t *ExecTool) streamManagedOutput(sessionID string, reader io.ReadCloser, stderr bool) {
	defer reader.Close()

	buf := make([]byte, 4096)
	wroteStderrHeader := false
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			if stderr && !wroteStderrHeader {
				t.processes.AppendOutput(sessionID, "\nSTDERR:\n")
				wroteStderrHeader = true
			}
			t.processes.AppendOutput(sessionID, string(buf[:n]))
		}
		if err != nil {
			return
		}
	}
}

func (t *ExecTool) runningSessionResult(sessionID string) *ToolResult {
	payload := map[string]any{
		"status":     "running",
		"session_id": sessionID,
	}
	if snap, ok := t.processes.GetSnapshot(sessionID); ok {
		payload["pid"] = snap.PID
		payload["started_at"] = snap.StartedAt
		payload["command"] = snap.Command
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to encode background exec result: %v", err))
	}
	return SilentResult(string(data))
}

func formatManagedCompletion(result ProcessPollResult, timeout time.Duration) *ToolResult {
	output := truncateExecOutput(result.Output)
	status := strings.ToLower(result.Session.Status)
	if status == "" {
		status = "completed"
	}

	switch status {
	case "completed":
		return UserResult(output)
	case "timeout":
		msg := fmt.Sprintf("Command timed out after %v", timeout)
		if output != "(no output)" {
			msg = output + "\n" + msg
		}
		return ErrorResult(msg)
	default:
		if result.Session.ExitError != "" && !strings.Contains(output, result.Session.ExitError) {
			output += "\nExit code: " + result.Session.ExitError
		}
		return &ToolResult{
			ForLLM:  output,
			ForUser: output,
			IsError: true,
		}
	}
}

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}

func (t *ExecTool) withHostLimits(command string) string {
	if t == nil {
		return command
	}
	if runtime.GOOS == "windows" {
		return command
	}
	if !strings.EqualFold(strings.TrimSpace(t.backend), "host") {
		return command
	}

	memMB := t.hostMemoryMB
	cpuSeconds := t.hostCPUSeconds
	fileSizeMB := t.hostFileSizeMB
	nproc := t.hostNProc

	parts := make([]string, 0, 4)
	if memMB > 0 {
		memKB := memMB * 1024
		parts = append(parts, fmt.Sprintf(
			"ulimit -v %d || { echo 'exec host_limits: failed to set ulimit -v' 1>&2; exit 1; }",
			memKB,
		))
	}
	if cpuSeconds > 0 {
		parts = append(parts, fmt.Sprintf(
			"ulimit -t %d || { echo 'exec host_limits: failed to set ulimit -t' 1>&2; exit 1; }",
			cpuSeconds,
		))
	}
	if fileSizeMB > 0 {
		// ulimit -f uses 512-byte blocks on most shells.
		blocks := fileSizeMB * 2048
		parts = append(parts, fmt.Sprintf(
			"ulimit -f %d || { echo 'exec host_limits: failed to set ulimit -f' 1>&2; exit 1; }",
			blocks,
		))
	}
	if nproc > 0 {
		parts = append(parts, fmt.Sprintf(
			"ulimit -u %d || { echo 'exec host_limits: failed to set ulimit -u' 1>&2; exit 1; }",
			nproc,
		))
	}

	if len(parts) == 0 {
		return command
	}

	return strings.Join(parts, " && ") + " && " + command
}

func truncateExecOutput(output string) string {
	if output == "" {
		output = "(no output)"
	}

	const maxLen = 10000
	if len(output) > maxLen {
		output = output[:maxLen] + fmt.Sprintf("\n... (truncated, %d more chars)", len(output)-maxLen)
	}
	return output
}

func readOptionalIntArg(args map[string]any, key string, minVal, maxVal int) (int, bool, error) {
	raw, exists := args[key]
	if !exists {
		return 0, false, nil
	}

	n, err := toInt(raw)
	if err != nil {
		return 0, true, fmt.Errorf("%s must be an integer", key)
	}
	if n < minVal || n > maxVal {
		return 0, true, fmt.Errorf("%s must be between %d and %d", key, minVal, maxVal)
	}
	return n, true, nil
}

func (t *ExecTool) guardCommand(command, cwd string) string {
	cmd := strings.TrimSpace(command)
	lower := strings.ToLower(cmd)

	// Custom allow patterns exempt a command from deny checks.
	explicitlyAllowed := false
	for _, pattern := range t.customAllowPatterns {
		if pattern.MatchString(lower) {
			explicitlyAllowed = true
			break
		}
	}

	if !explicitlyAllowed {
		for _, pattern := range t.denyPatterns {
			if pattern.MatchString(lower) {
				return "Command blocked by safety guard (dangerous pattern detected)"
			}
		}
	}

	if len(t.allowPatterns) > 0 {
		allowed := false
		for _, pattern := range t.allowPatterns {
			if pattern.MatchString(lower) {
				allowed = true
				break
			}
		}
		if !allowed {
			return "Command blocked by safety guard (not in allowlist)"
		}
	}

	if t.restrictToWorkspace {
		if strings.Contains(cmd, "..\\") || strings.Contains(cmd, "../") {
			return "Command blocked by safety guard (path traversal detected). " +
				"Suggestion: Use absolute paths within the workspace directory, " +
				"or use read_file/list_dir tools to access files."
		}

		cwdPath, err := filepath.Abs(cwd)
		if err != nil {
			return ""
		}

		matches := absolutePathPattern.FindAllStringIndex(cmd, -1)
		for _, match := range matches {
			raw := cmd[match[0]:match[1]]
			if !pathMatchHasTokenBoundary(cmd, match[0]) {
				// absolutePathPattern is intentionally broad and can match substrings
				// inside relative paths (e.g. "skills/foo/bar" contains "/foo/bar").
				// Only treat this match as an absolute path when it begins at a token
				// boundary, otherwise skip it.
				continue
			}
			if pathMatchIsEnvAssignmentValue(cmd, match[0]) || pathMatchIsURLSegment(cmd, raw, match[0]) {
				continue
			}

			p, err := filepath.Abs(raw)
			if err != nil {
				continue
			}

			if safePaths[p] {
				continue
			}

			rel, err := filepath.Rel(cwdPath, p)
			if err != nil {
				continue
			}

			if strings.HasPrefix(rel, "..") {
				return "Command blocked by safety guard (path outside working dir). " +
					"Suggestion: Only access files within the workspace directory. " +
					"Use list_dir to see available files."
			}
		}
	}

	return ""
}

func pathMatchHasTokenBoundary(command string, matchStart int) bool {
	if matchStart <= 0 {
		return true
	}
	if matchStart > len(command) {
		return false
	}

	prev := command[matchStart-1]
	// Whitespace and common shell metacharacters delimit tokens.
	if prev <= ' ' {
		return true
	}
	switch prev {
	case '"', '\'', '(', ')', '[', ']', '{', '}', '<', '>', '|', '&', ';', '=':
		return true
	default:
		return false
	}
}

func pathMatchIsEnvAssignmentValue(command string, matchStart int) bool {
	if matchStart <= 0 || matchStart > len(command) {
		return false
	}

	tokenStart := strings.LastIndexAny(command[:matchStart], " \t\r\n") + 1
	if tokenStart < 0 || tokenStart >= matchStart {
		return false
	}

	prefix := command[tokenStart:matchStart]
	eq := strings.Index(prefix, "=")
	if eq <= 0 {
		return false
	}

	return envVarNamePattern.MatchString(prefix[:eq])
}

// pathMatchIsURLSegment detects path-like regex matches that are actually
// URL path segments (e.g. "/owner/repo.git" in "https://github.com/owner/repo.git").
func pathMatchIsURLSegment(command, raw string, matchStart int) bool {
	if matchStart <= 0 || matchStart > len(command) {
		return false
	}

	tokenStart := strings.LastIndexAny(command[:matchStart], " \t\r\n") + 1
	if tokenStart < 0 || tokenStart >= matchStart {
		return false
	}

	prefix := command[tokenStart:matchStart]
	if strings.Contains(prefix, "://") {
		return true
	}

	return strings.HasPrefix(raw, "//") && strings.HasSuffix(prefix, ":")
}

func (t *ExecTool) SetTimeout(timeout time.Duration) {
	t.timeout = timeout
}

func (t *ExecTool) ProcessManager() *ProcessManager {
	return t.processes
}

func (t *ExecTool) SetRestrictToWorkspace(restrict bool) {
	t.restrictToWorkspace = restrict
}

func (t *ExecTool) SetAllowPatterns(patterns []string) error {
	t.allowPatterns = make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return fmt.Errorf("invalid allow pattern %q: %w", p, err)
		}
		t.allowPatterns = append(t.allowPatterns, re)
	}
	return nil
}

const (
	defaultProcessMaxOutputChars  = 30000
	defaultProcessMaxPendingChars = 12000
	defaultProcessLogTailLines    = 200
)

var (
	ErrProcessSessionNotFound = errors.New("process session not found")
	ErrProcessSessionRunning  = errors.New("process session is still running")
)

type processSession struct {
	ID      string
	Command string
	CWD     string

	StartedAt time.Time
	UpdatedAt time.Time
	EndedAt   time.Time

	PID       int
	Status    string
	ExitCode  *int
	ExitError string
	Truncated bool

	output        string
	pending       string
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	cancel        func()
	killRequested bool

	notify chan struct{}
	mu     sync.Mutex
}

type ProcessSessionSnapshot struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	Command   string `json:"command"`
	CWD       string `json:"cwd,omitempty"`
	PID       int    `json:"pid,omitempty"`

	StartedAt string `json:"started_at"`
	UpdatedAt string `json:"updated_at"`
	EndedAt   string `json:"ended_at,omitempty"`

	ExitCode  *int   `json:"exit_code,omitempty"`
	ExitError string `json:"exit_error,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type ProcessPollResult struct {
	Session  ProcessSessionSnapshot `json:"session"`
	Output   string                 `json:"output,omitempty"`
	TimedOut bool                   `json:"timed_out,omitempty"`
}

type ProcessLogResult struct {
	Session    ProcessSessionSnapshot `json:"session"`
	TotalLines int                    `json:"total_lines"`
	Offset     int                    `json:"offset"`
	Limit      int                    `json:"limit,omitempty"`
	Lines      []string               `json:"lines"`
	Output     string                 `json:"output"`
}

type ProcessManager struct {
	mu              sync.RWMutex
	sessions        map[string]*processSession
	nextID          atomic.Uint64
	maxOutputChars  int
	maxPendingChars int
}

func NewProcessManager(maxOutputChars int) *ProcessManager {
	if maxOutputChars <= 0 {
		maxOutputChars = defaultProcessMaxOutputChars
	}

	maxPendingChars := defaultProcessMaxPendingChars
	if maxPendingChars > maxOutputChars {
		maxPendingChars = maxOutputChars
	}

	return &ProcessManager{
		sessions:        make(map[string]*processSession),
		maxOutputChars:  maxOutputChars,
		maxPendingChars: maxPendingChars,
	}
}

func (pm *ProcessManager) StartSession(
	command, cwd string,
	cmd *exec.Cmd,
	stdin io.WriteCloser,
	cancel func(),
) string {
	id := fmt.Sprintf("proc-%d", pm.nextID.Add(1))
	now := time.Now()

	pid := 0
	if cmd != nil && cmd.Process != nil {
		pid = cmd.Process.Pid
	}

	session := &processSession{
		ID:        id,
		Command:   command,
		CWD:       cwd,
		StartedAt: now,
		UpdatedAt: now,
		PID:       pid,
		Status:    "running",
		cmd:       cmd,
		stdin:     stdin,
		cancel:    cancel,
		notify:    make(chan struct{}, 1),
	}

	pm.mu.Lock()
	pm.sessions[id] = session
	pm.mu.Unlock()

	return id
}

func (pm *ProcessManager) AppendOutput(sessionID, chunk string) {
	if chunk == "" {
		return
	}

	session, ok := pm.getSession(sessionID)
	if !ok {
		return
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	session.output, session.Truncated = appendWithCap(
		session.output,
		chunk,
		pm.maxOutputChars,
		session.Truncated,
	)
	session.pending, session.Truncated = appendWithCap(
		session.pending,
		chunk,
		pm.maxPendingChars,
		session.Truncated,
	)
	session.UpdatedAt = time.Now()
	session.signalNotifyLocked()
}

func (pm *ProcessManager) MarkExited(sessionID string, waitErr error, timedOut bool) {
	session, ok := pm.getSession(sessionID)
	if !ok {
		return
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	now := time.Now()
	session.UpdatedAt = now
	session.EndedAt = now

	switch {
	case timedOut:
		session.Status = "timeout"
	case session.killRequested:
		session.Status = "killed"
	case waitErr != nil:
		session.Status = "failed"
	default:
		session.Status = "completed"
	}

	if waitErr != nil {
		session.ExitError = waitErr.Error()
	}
	if code, ok := extractExitCode(waitErr); ok {
		session.ExitCode = &code
	} else if waitErr == nil {
		code := 0
		session.ExitCode = &code
	}

	if session.stdin != nil {
		_ = session.stdin.Close()
		session.stdin = nil
	}
	if session.cancel != nil {
		session.cancel()
		session.cancel = nil
	}
	session.cmd = nil
	session.signalNotifyLocked()
}

func (pm *ProcessManager) GetSnapshot(sessionID string) (ProcessSessionSnapshot, bool) {
	session, ok := pm.getSession(sessionID)
	if !ok {
		return ProcessSessionSnapshot{}, false
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	return session.snapshotLocked(), true
}

func (pm *ProcessManager) ListSnapshots() []ProcessSessionSnapshot {
	pm.mu.RLock()
	sessions := make([]*processSession, 0, len(pm.sessions))
	for _, session := range pm.sessions {
		sessions = append(sessions, session)
	}
	pm.mu.RUnlock()

	out := make([]ProcessSessionSnapshot, 0, len(sessions))
	for _, session := range sessions {
		session.mu.Lock()
		out = append(out, session.snapshotLocked())
		session.mu.Unlock()
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt > out[j].StartedAt
	})
	return out
}

func (pm *ProcessManager) Poll(sessionID string, timeout time.Duration) (ProcessPollResult, error) {
	session, ok := pm.getSession(sessionID)
	if !ok {
		return ProcessPollResult{}, ErrProcessSessionNotFound
	}
	if timeout < 0 {
		timeout = 0
	}

	deadline := time.Now().Add(timeout)
	for {
		output, snapshot := session.drainPending()
		if output != "" || snapshot.Status != "running" || timeout == 0 {
			return ProcessPollResult{
				Session: snapshot,
				Output:  output,
			}, nil
		}

		waitFor := time.Until(deadline)
		if waitFor <= 0 {
			return ProcessPollResult{
				Session:  snapshot,
				TimedOut: true,
			}, nil
		}

		select {
		case <-session.notify:
			// loop and re-check
		case <-time.After(waitFor):
			snapshot := session.snapshot()
			return ProcessPollResult{
				Session:  snapshot,
				TimedOut: true,
			}, nil
		}
	}
}

func (pm *ProcessManager) Log(
	sessionID string,
	offset, limit int,
	useDefaultTail bool,
) (ProcessLogResult, error) {
	session, ok := pm.getSession(sessionID)
	if !ok {
		return ProcessLogResult{}, ErrProcessSessionNotFound
	}

	output, snapshot := session.outputSnapshot()
	lines := normalizeOutputLines(output)
	total := len(lines)

	start := offset
	if start < 0 {
		start = 0
	}
	if start > total {
		start = total
	}

	end := total
	effectiveLimit := limit
	if useDefaultTail {
		if total > defaultProcessLogTailLines {
			start = total - defaultProcessLogTailLines
		} else {
			start = 0
		}
		end = total
		effectiveLimit = defaultProcessLogTailLines
	} else if effectiveLimit > 0 {
		if start+effectiveLimit < end {
			end = start + effectiveLimit
		}
	}

	window := []string{}
	if start < end {
		window = lines[start:end]
	}

	return ProcessLogResult{
		Session:    snapshot,
		TotalLines: total,
		Offset:     start,
		Limit:      effectiveLimit,
		Lines:      window,
		Output:     strings.Join(window, "\n"),
	}, nil
}

func (pm *ProcessManager) Write(sessionID, data string, eof bool) error {
	session, ok := pm.getSession(sessionID)
	if !ok {
		return ErrProcessSessionNotFound
	}

	session.mu.Lock()
	if session.Status != "running" {
		session.mu.Unlock()
		return fmt.Errorf("session %s is not running", sessionID)
	}
	stdin := session.stdin
	session.mu.Unlock()

	if stdin == nil {
		return fmt.Errorf("session %s has no writable stdin", sessionID)
	}

	if data != "" {
		if _, err := io.WriteString(stdin, data); err != nil {
			return err
		}
	}
	if eof {
		return stdin.Close()
	}
	return nil
}

func (pm *ProcessManager) Kill(sessionID string) (bool, error) {
	session, ok := pm.getSession(sessionID)
	if !ok {
		return false, ErrProcessSessionNotFound
	}

	session.mu.Lock()
	if session.Status != "running" {
		session.mu.Unlock()
		return false, nil
	}
	session.killRequested = true
	cmd := session.cmd
	cancel := session.cancel
	session.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if cmd != nil {
		if err := terminateProcessTree(cmd); err != nil {
			logger.DebugCF("tools/shell", "terminate process tree failed", map[string]any{"error": err.Error()})
		}
	}
	return true, nil
}

func (pm *ProcessManager) Clear(sessionID string) error {
	session, ok := pm.getSession(sessionID)
	if !ok {
		return ErrProcessSessionNotFound
	}

	session.mu.Lock()
	running := session.Status == "running"
	session.mu.Unlock()
	if running {
		return ErrProcessSessionRunning
	}

	pm.mu.Lock()
	delete(pm.sessions, sessionID)
	pm.mu.Unlock()
	return nil
}

func (pm *ProcessManager) Remove(sessionID string) (bool, error) {
	session, ok := pm.getSession(sessionID)
	if !ok {
		return false, ErrProcessSessionNotFound
	}

	session.mu.Lock()
	running := session.Status == "running"
	session.mu.Unlock()
	if running {
		_, err := pm.Kill(sessionID)
		return false, err
	}

	pm.mu.Lock()
	delete(pm.sessions, sessionID)
	pm.mu.Unlock()
	return true, nil
}

func (pm *ProcessManager) getSession(sessionID string) (*processSession, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	session, ok := pm.sessions[sessionID]
	return session, ok
}

func (s *processSession) snapshot() ProcessSessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *processSession) drainPending() (string, ProcessSessionSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	pending := s.pending
	s.pending = ""
	return pending, s.snapshotLocked()
}

func (s *processSession) outputSnapshot() (string, ProcessSessionSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.output, s.snapshotLocked()
}

func (s *processSession) snapshotLocked() ProcessSessionSnapshot {
	snapshot := ProcessSessionSnapshot{
		SessionID: s.ID,
		Status:    s.Status,
		Command:   s.Command,
		CWD:       s.CWD,
		PID:       s.PID,
		StartedAt: s.StartedAt.Format(time.RFC3339),
		UpdatedAt: s.UpdatedAt.Format(time.RFC3339),
		ExitCode:  s.ExitCode,
		ExitError: s.ExitError,
		Truncated: s.Truncated,
	}
	if !s.EndedAt.IsZero() {
		snapshot.EndedAt = s.EndedAt.Format(time.RFC3339)
	}
	return snapshot
}

func (s *processSession) signalNotifyLocked() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func appendWithCap(current, appendChunk string, max int, truncated bool) (string, bool) {
	if max <= 0 {
		return current + appendChunk, truncated
	}

	combined := current + appendChunk
	if len(combined) <= max {
		return combined, truncated
	}

	return combined[len(combined)-max:], true
}

func normalizeOutputLines(output string) []string {
	if output == "" {
		return []string{}
	}
	normalized := strings.ReplaceAll(output, "\r\n", "\n")
	normalized = strings.TrimSuffix(normalized, "\n")
	if normalized == "" {
		return []string{}
	}
	return strings.Split(normalized, "\n")
}

func extractExitCode(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), true
	}
	return 0, false
}

type ProcessTool struct {
	processes *ProcessManager
}

func NewProcessTool(processes *ProcessManager) *ProcessTool {
	return &ProcessTool{processes: processes}
}

func (t *ProcessTool) Name() string {
	return "process"
}

func (t *ProcessTool) Description() string {
	return "Manage background exec sessions: list, poll, log, write, kill, clear, remove."
}

func (t *ProcessTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"list", "poll", "log", "write", "kill", "clear", "remove"},
				"description": "Process action",
			},
			"session_id": map[string]any{
				"type":        "string",
				"description": "Session ID returned by exec background mode",
			},
			"data": map[string]any{
				"type":        "string",
				"description": "Input data for write action",
			},
			"eof": map[string]any{
				"type":        "boolean",
				"description": "Close stdin after write",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "Log line offset",
				"minimum":     0.0,
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Max log lines to return",
				"minimum":     0.0,
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": "Poll wait timeout in milliseconds",
				"minimum":     0.0,
			},
		},
		"required": []string{"action"},
	}
}

func (t *ProcessTool) Execute(_ context.Context, args map[string]any) *ToolResult {
	if t.processes == nil {
		return ErrorResult("process manager not configured")
	}

	action, ok := getStringArg(args, "action")
	if !ok || strings.TrimSpace(action) == "" {
		return ErrorResult("action is required")
	}
	action = strings.ToLower(strings.TrimSpace(action))

	switch action {
	case "list":
		sessions := t.processes.ListSnapshots()
		return marshalSilentJSON(map[string]any{
			"count":    len(sessions),
			"sessions": sessions,
		})
	case "poll":
		sessionID, ok := getStringArg(args, "session_id")
		if !ok || strings.TrimSpace(sessionID) == "" {
			return ErrorResult("session_id is required for poll")
		}
		timeoutMS, err := parseOptionalIntArg(args, "timeout_ms", 0, 0, 5*60*1000)
		if err != nil {
			return ErrorResult(err.Error())
		}
		result, err := t.processes.Poll(strings.TrimSpace(sessionID), time.Duration(timeoutMS)*time.Millisecond)
		if err != nil {
			return ErrorResult(err.Error())
		}
		return marshalSilentJSON(result)
	case "log":
		sessionID, ok := getStringArg(args, "session_id")
		if !ok || strings.TrimSpace(sessionID) == "" {
			return ErrorResult("session_id is required for log")
		}
		offset, err := parseOptionalIntArg(args, "offset", 0, 0, 1_000_000)
		if err != nil {
			return ErrorResult(err.Error())
		}
		limit, err := parseOptionalIntArg(args, "limit", 0, 0, 1_000_000)
		if err != nil {
			return ErrorResult(err.Error())
		}
		_, hasOffset := args["offset"]
		_, hasLimit := args["limit"]
		useDefaultTail := !hasOffset && !hasLimit
		result, err := t.processes.Log(strings.TrimSpace(sessionID), offset, limit, useDefaultTail)
		if err != nil {
			return ErrorResult(err.Error())
		}
		return marshalSilentJSON(result)
	case "write":
		sessionID, ok := getStringArg(args, "session_id")
		if !ok || strings.TrimSpace(sessionID) == "" {
			return ErrorResult("session_id is required for write")
		}
		data, _ := getStringArg(args, "data")
		eof, err := parseBoolArg(args, "eof", false)
		if err != nil {
			return ErrorResult(err.Error())
		}
		if err := t.processes.Write(strings.TrimSpace(sessionID), data, eof); err != nil {
			return ErrorResult(err.Error())
		}
		return marshalSilentJSON(map[string]any{
			"status":     "ok",
			"action":     "write",
			"session_id": strings.TrimSpace(sessionID),
		})
	case "kill":
		sessionID, ok := getStringArg(args, "session_id")
		if !ok || strings.TrimSpace(sessionID) == "" {
			return ErrorResult("session_id is required for kill")
		}
		signaled, err := t.processes.Kill(strings.TrimSpace(sessionID))
		if err != nil {
			return ErrorResult(err.Error())
		}
		return marshalSilentJSON(map[string]any{
			"status":      "ok",
			"action":      "kill",
			"session_id":  strings.TrimSpace(sessionID),
			"kill_signal": signaled,
		})
	case "clear":
		sessionID, ok := getStringArg(args, "session_id")
		if !ok || strings.TrimSpace(sessionID) == "" {
			return ErrorResult("session_id is required for clear")
		}
		if err := t.processes.Clear(strings.TrimSpace(sessionID)); err != nil {
			return ErrorResult(err.Error())
		}
		return marshalSilentJSON(map[string]any{
			"status":     "ok",
			"action":     "clear",
			"session_id": strings.TrimSpace(sessionID),
		})
	case "remove":
		sessionID, ok := getStringArg(args, "session_id")
		if !ok || strings.TrimSpace(sessionID) == "" {
			return ErrorResult("session_id is required for remove")
		}
		removed, err := t.processes.Remove(strings.TrimSpace(sessionID))
		if err != nil {
			return ErrorResult(err.Error())
		}
		return marshalSilentJSON(map[string]any{
			"status":     "ok",
			"action":     "remove",
			"session_id": strings.TrimSpace(sessionID),
			"removed":    removed,
		})
	default:
		return ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

func marshalSilentJSON(payload any) *ToolResult {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to encode process payload: %v", err))
	}
	return SilentResult(string(data))
}

func prepareCommandForTermination(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if runtime.GOOS == "windows" {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	pid := cmd.Process.Pid
	if pid <= 0 {
		return nil
	}

	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
		_ = cmd.Process.Kill()
		return nil
	}

	ownPgrp := syscall.Getpgrp()
	if pid > 1 && pid != ownPgrp {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	_ = cmd.Process.Kill()
	return nil
}
