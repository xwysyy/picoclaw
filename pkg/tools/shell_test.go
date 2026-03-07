package tools

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xwysyy/X-Claw/pkg/config"
)

// TestShellTool_Success verifies successful command execution
func TestShellTool_Success(t *testing.T) {
	tool, err := NewExecTool("", false)
	if err != nil {
		t.Errorf("unable to configure exec tool: %s", err)
	}

	ctx := context.Background()
	args := map[string]any{
		"command": "echo 'hello world'",
	}

	result := tool.Execute(ctx, args)

	// Success should not be an error
	if result.IsError {
		t.Errorf("Expected success, got IsError=true: %s", result.ForLLM)
	}

	// ForUser should contain command output
	if !strings.Contains(result.ForUser, "hello world") {
		t.Errorf("Expected ForUser to contain 'hello world', got: %s", result.ForUser)
	}

	// ForLLM should contain full output
	if !strings.Contains(result.ForLLM, "hello world") {
		t.Errorf("Expected ForLLM to contain 'hello world', got: %s", result.ForLLM)
	}
}

// TestShellTool_Failure verifies failed command execution
func TestShellTool_Failure(t *testing.T) {
	tool, err := NewExecTool("", false)
	if err != nil {
		t.Errorf("unable to configure exec tool: %s", err)
	}

	ctx := context.Background()
	args := map[string]any{
		"command": "ls /nonexistent_directory_12345",
	}

	result := tool.Execute(ctx, args)

	// Failure should be marked as error
	if !result.IsError {
		t.Errorf("Expected error for failed command, got IsError=false")
	}

	// ForUser should contain error information
	if result.ForUser == "" {
		t.Errorf("Expected ForUser to contain error info, got empty string")
	}

	// ForLLM should contain exit code or error
	if !strings.Contains(result.ForLLM, "Exit code") && result.ForUser == "" {
		t.Errorf("Expected ForLLM to contain exit code or error, got: %s", result.ForLLM)
	}
}

// TestShellTool_Timeout verifies command timeout handling
func TestShellTool_Timeout(t *testing.T) {
	tool, err := NewExecTool("", false)
	if err != nil {
		t.Errorf("unable to configure exec tool: %s", err)
	}

	tool.SetTimeout(100 * time.Millisecond)

	ctx := context.Background()
	args := map[string]any{
		"command": "sleep 10",
	}

	result := tool.Execute(ctx, args)

	// Timeout should be marked as error
	if !result.IsError {
		t.Errorf("Expected error for timeout, got IsError=false")
	}

	// Should mention timeout
	if !strings.Contains(result.ForLLM, "timed out") && !strings.Contains(result.ForUser, "timed out") {
		t.Errorf("Expected timeout message, got ForLLM: %s, ForUser: %s", result.ForLLM, result.ForUser)
	}
}

// TestShellTool_WorkingDir verifies custom working directory
func TestShellTool_WorkingDir(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("test content"), 0o644)

	tool, err := NewExecTool("", false)
	if err != nil {
		t.Errorf("unable to configure exec tool: %s", err)
	}

	ctx := context.Background()
	args := map[string]any{
		"command":     "cat test.txt",
		"working_dir": tmpDir,
	}

	result := tool.Execute(ctx, args)

	if result.IsError {
		t.Errorf("Expected success in custom working dir, got error: %s", result.ForLLM)
	}

	if !strings.Contains(result.ForUser, "test content") {
		t.Errorf("Expected output from custom dir, got: %s", result.ForUser)
	}
}

// TestShellTool_DangerousCommand verifies safety guard blocks dangerous commands
func TestShellTool_DangerousCommand(t *testing.T) {
	tool, err := NewExecTool("", false)
	if err != nil {
		t.Errorf("unable to configure exec tool: %s", err)
	}

	ctx := context.Background()
	args := map[string]any{
		"command": "rm -rf /",
	}

	result := tool.Execute(ctx, args)

	// Dangerous command should be blocked
	if !result.IsError {
		t.Errorf("Expected dangerous command to be blocked (IsError=true)")
	}

	if !strings.Contains(result.ForLLM, "blocked") && !strings.Contains(result.ForUser, "blocked") {
		t.Errorf("Expected 'blocked' message, got ForLLM: %s, ForUser: %s", result.ForLLM, result.ForUser)
	}
}

// TestShellTool_MissingCommand verifies error handling for missing command
func TestShellTool_MissingCommand(t *testing.T) {
	tool, err := NewExecTool("", false)
	if err != nil {
		t.Errorf("unable to configure exec tool: %s", err)
	}

	ctx := context.Background()
	args := map[string]any{}

	result := tool.Execute(ctx, args)

	// Should return error result
	if !result.IsError {
		t.Errorf("Expected error when command is missing")
	}
}

// TestShellTool_StderrCapture verifies stderr is captured and included
func TestShellTool_StderrCapture(t *testing.T) {
	tool, err := NewExecTool("", false)
	if err != nil {
		t.Errorf("unable to configure exec tool: %s", err)
	}

	ctx := context.Background()
	args := map[string]any{
		"command": "sh -c 'echo stdout; echo stderr >&2'",
	}

	result := tool.Execute(ctx, args)

	// Both stdout and stderr should be in output
	if !strings.Contains(result.ForLLM, "stdout") {
		t.Errorf("Expected stdout in output, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "stderr") {
		t.Errorf("Expected stderr in output, got: %s", result.ForLLM)
	}
}

// TestShellTool_OutputTruncation verifies long output is truncated
func TestShellTool_OutputTruncation(t *testing.T) {
	tool, err := NewExecTool("", false)
	if err != nil {
		t.Errorf("unable to configure exec tool: %s", err)
	}

	ctx := context.Background()
	// Generate long output (>10000 chars)
	args := map[string]any{
		"command": "python3 -c \"print('x' * 20000)\" || echo " + strings.Repeat("x", 20000),
	}

	result := tool.Execute(ctx, args)

	// Should have truncation message or be truncated
	if len(result.ForLLM) > 15000 {
		t.Errorf("Expected output to be truncated, got length: %d", len(result.ForLLM))
	}
}

// TestShellTool_WorkingDir_OutsideWorkspace verifies that working_dir cannot escape the workspace directly
func TestShellTool_WorkingDir_OutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	outsideDir := filepath.Join(root, "outside")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("failed to create outside dir: %v", err)
	}

	tool, err := NewExecTool(workspace, true)
	if err != nil {
		t.Errorf("unable to configure exec tool: %s", err)
	}

	result := tool.Execute(context.Background(), map[string]any{
		"command":     "pwd",
		"working_dir": outsideDir,
	})

	if !result.IsError {
		t.Fatalf("expected working_dir outside workspace to be blocked, got output: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "blocked") {
		t.Errorf("expected 'blocked' in error, got: %s", result.ForLLM)
	}
}

// TestShellTool_WorkingDir_SymlinkEscape verifies that a symlink inside the workspace
// pointing outside cannot be used as working_dir to escape the sandbox.
func TestShellTool_WorkingDir_SymlinkEscape(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	secretDir := filepath.Join(root, "secret")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatalf("failed to create secret dir: %v", err)
	}
	os.WriteFile(filepath.Join(secretDir, "secret.txt"), []byte("top secret"), 0o644)

	// symlink lives inside the workspace but resolves to secretDir outside it
	link := filepath.Join(workspace, "escape")
	if err := os.Symlink(secretDir, link); err != nil {
		t.Skipf("symlinks not supported in this environment: %v", err)
	}

	tool, err := NewExecTool(workspace, true)
	if err != nil {
		t.Errorf("unable to configure exec tool: %s", err)
	}

	result := tool.Execute(context.Background(), map[string]any{
		"command":     "cat secret.txt",
		"working_dir": link,
	})

	if !result.IsError {
		t.Fatalf("expected symlink working_dir escape to be blocked, got output: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "blocked") {
		t.Errorf("expected 'blocked' in error, got: %s", result.ForLLM)
	}
}

// TestShellTool_RestrictToWorkspace verifies workspace restriction
func TestShellTool_RestrictToWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	tool, err := NewExecTool(tmpDir, false)
	if err != nil {
		t.Errorf("unable to configure exec tool: %s", err)
	}

	tool.SetRestrictToWorkspace(true)

	ctx := context.Background()
	args := map[string]any{
		"command": "cat ../../etc/passwd",
	}

	result := tool.Execute(ctx, args)

	// Path traversal should be blocked
	if !result.IsError {
		t.Errorf("Expected path traversal to be blocked with restrictToWorkspace=true")
	}

	if !strings.Contains(result.ForLLM, "blocked") && !strings.Contains(result.ForUser, "blocked") {
		t.Errorf(
			"Expected 'blocked' message for path traversal, got ForLLM: %s, ForUser: %s",
			result.ForLLM,
			result.ForUser,
		)
	}
}

func TestShellTool_RestrictToWorkspace_AllowsEnvAssignmentWithSlash(t *testing.T) {
	tmpDir := t.TempDir()
	tool, err := NewExecTool(tmpDir, true)
	if err != nil {
		t.Fatalf("unable to configure exec tool: %v", err)
	}

	guardErr := tool.guardCommand("TZ=Asia/Shanghai date '+%Y-%m-%d'", tmpDir)
	if guardErr != "" {
		t.Fatalf("expected env assignment path to be ignored, got guard error: %s", guardErr)
	}
}

func TestShellTool_RestrictToWorkspace_StillBlocksRealPathAfterEnvAssignment(t *testing.T) {
	tmpDir := t.TempDir()
	tool, err := NewExecTool(tmpDir, true)
	if err != nil {
		t.Fatalf("unable to configure exec tool: %v", err)
	}

	guardErr := tool.guardCommand("TZ=Asia/Shanghai cat /etc/passwd", tmpDir)
	if guardErr == "" {
		t.Fatalf("expected real path access to be blocked")
	}
	if !strings.Contains(guardErr, "outside working dir") {
		t.Fatalf("expected outside working dir error, got: %s", guardErr)
	}
}

func TestShellTool_RestrictToWorkspace_AllowsHTTPSURLPathSegments(t *testing.T) {
	tmpDir := t.TempDir()
	tool, err := NewExecTool(tmpDir, true)
	if err != nil {
		t.Fatalf("unable to configure exec tool: %v", err)
	}

	guardErr := tool.guardCommand("git clone https://github.com/xwysyy/Claw-Paper-Notes.git", tmpDir)
	if guardErr != "" {
		t.Fatalf("expected URL path segments to be ignored by guard, got: %s", guardErr)
	}
}

func TestShellTool_RestrictToWorkspace_AllowsRelativePathWithSlash(t *testing.T) {
	tmpDir := t.TempDir()
	tool, err := NewExecTool(tmpDir, true)
	if err != nil {
		t.Fatalf("unable to configure exec tool: %v", err)
	}

	// Regression: absolutePathPattern used to match "/arxiv-watcher/..." inside a
	// relative path like "skills/arxiv-watcher/..." and incorrectly block it.
	guardErr := tool.guardCommand(`bash skills/arxiv-watcher/scripts/search_arxiv.sh "cat:cs.AI"`, tmpDir)
	if guardErr != "" {
		t.Fatalf("expected relative path with slashes to be allowed, got: %s", guardErr)
	}
}

// TestShellTool_DevNullAllowed verifies that /dev/null redirections are not blocked (issue #964).
func TestShellTool_DevNullAllowed(t *testing.T) {
	tmpDir := t.TempDir()
	tool, err := NewExecTool(tmpDir, true)
	if err != nil {
		t.Fatalf("unable to configure exec tool: %s", err)
	}

	commands := []string{
		"echo hello 2>/dev/null",
		"echo hello >/dev/null",
		"echo hello > /dev/null",
		"echo hello 2> /dev/null",
		"echo hello >/dev/null 2>&1",
		"find " + tmpDir + " -name '*.go' 2>/dev/null",
	}

	for _, cmd := range commands {
		result := tool.Execute(context.Background(), map[string]any{"command": cmd})
		if result.IsError && strings.Contains(result.ForLLM, "blocked") {
			t.Errorf("command should not be blocked: %s\n  error: %s", cmd, result.ForLLM)
		}
	}
}

// TestShellTool_BlockDevices verifies that writes to block devices are blocked (issue #965).
func TestShellTool_BlockDevices(t *testing.T) {
	tool, err := NewExecTool("", false)
	if err != nil {
		t.Fatalf("unable to configure exec tool: %s", err)
	}

	blocked := []string{
		"echo x > /dev/sda",
		"echo x > /dev/hda",
		"echo x > /dev/vda",
		"echo x > /dev/xvda",
		"echo x > /dev/nvme0n1",
		"echo x > /dev/mmcblk0",
		"echo x > /dev/loop0",
		"echo x > /dev/dm-0",
		"echo x > /dev/md0",
		"echo x > /dev/sr0",
		"echo x > /dev/nbd0",
	}

	for _, cmd := range blocked {
		result := tool.Execute(context.Background(), map[string]any{"command": cmd})
		if !result.IsError {
			t.Errorf("expected block device write to be blocked: %s", cmd)
		}
	}
}

// TestShellTool_SafePathsInWorkspaceRestriction verifies that safe kernel pseudo-devices
// are allowed even when workspace restriction is active.
func TestShellTool_SafePathsInWorkspaceRestriction(t *testing.T) {
	tmpDir := t.TempDir()
	tool, err := NewExecTool(tmpDir, true)
	if err != nil {
		t.Fatalf("unable to configure exec tool: %s", err)
	}

	// These reference paths outside workspace but should be allowed via safePaths.
	commands := []string{
		"cat /dev/urandom | head -c 16 | od",
		"echo test > /dev/null",
		"dd if=/dev/zero bs=1 count=1",
	}

	for _, cmd := range commands {
		result := tool.Execute(context.Background(), map[string]any{"command": cmd})
		if result.IsError && strings.Contains(result.ForLLM, "path outside working dir") {
			t.Errorf("safe path should not be blocked by workspace check: %s\n  error: %s", cmd, result.ForLLM)
		}
	}
}

// TestShellTool_CustomAllowPatterns verifies that custom allow patterns exempt
// commands from deny pattern checks.
func TestShellTool_CustomAllowPatterns(t *testing.T) {
	cfg := &config.Config{
		Tools: config.ToolsConfig{
			Exec: config.ExecConfig{
				EnableDenyPatterns:  true,
				CustomAllowPatterns: []string{`\bgit\s+push\s+origin\b`},
			},
		},
	}

	tool, err := NewExecToolWithConfig("", false, cfg)
	if err != nil {
		t.Fatalf("unable to configure exec tool: %s", err)
	}

	// "git push origin main" should be allowed by custom allow pattern.
	tmpDir := t.TempDir()
	guardErr := tool.guardCommand("git push origin main", tmpDir)
	if guardErr != "" {
		t.Errorf("custom allow pattern should exempt 'git push origin main', got guard error: %s", guardErr)
	}

	// "git push upstream main" should still be blocked (does not match allow pattern).
	guardErr = tool.guardCommand("git push upstream main", tmpDir)
	if guardErr == "" {
		t.Errorf("'git push upstream main' should still be blocked by deny pattern")
	}
}

func TestShellTool_DockerBackend_RejectsBackgroundAndYield(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.Backend = "docker"
	cfg.Tools.Exec.Docker.Image = "alpine:3.20"

	tool, err := NewExecToolWithConfig(t.TempDir(), true, cfg)
	if err != nil {
		t.Fatalf("unable to configure exec tool: %s", err)
	}

	res := tool.Execute(context.Background(), map[string]any{
		"command":    "echo hi",
		"background": true,
	})
	if !res.IsError || !strings.Contains(res.ForLLM, "background/yield") {
		t.Fatalf("expected background to be rejected, got: %+v", res)
	}

	res = tool.Execute(context.Background(), map[string]any{
		"command":         "echo hi",
		"yield_ms":        10,
		"timeout_seconds": 1,
	})
	if !res.IsError || !strings.Contains(res.ForLLM, "background/yield") {
		t.Fatalf("expected yield_ms to be rejected, got: %+v", res)
	}
}

func TestShellTool_DockerBackend_RequiresRestrictToWorkspace(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.Backend = "docker"
	cfg.Tools.Exec.Docker.Image = "alpine:3.20"

	tool, err := NewExecToolWithConfig(t.TempDir(), false, cfg)
	if err != nil {
		t.Fatalf("unable to configure exec tool: %s", err)
	}

	res := tool.Execute(context.Background(), map[string]any{"command": "echo hi"})
	if !res.IsError || !strings.Contains(res.ForLLM, "restrict_to_workspace") {
		t.Fatalf("expected restrict_to_workspace enforcement, got: %+v", res)
	}
}

func TestShellTool_DockerBackend_RequiresImage(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.Backend = "docker"
	cfg.Tools.Exec.Docker.Image = ""

	tool, err := NewExecToolWithConfig(t.TempDir(), true, cfg)
	if err != nil {
		t.Fatalf("unable to configure exec tool: %s", err)
	}

	res := tool.Execute(context.Background(), map[string]any{"command": "echo hi"})
	if !res.IsError || !strings.Contains(res.ForLLM, "docker.image") {
		t.Fatalf("expected missing image error, got: %+v", res)
	}
}

func TestShellTool_DockerBackend_InvalidBackendRejected(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.Backend = "invalid_backend"

	if _, err := NewExecToolWithConfig(t.TempDir(), true, cfg); err == nil {
		t.Fatalf("expected invalid backend to be rejected")
	}
}

func TestShellTool_HostLimits_WrapsCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("host_limits wrapping is Unix-only")
	}

	cfg := config.DefaultConfig()
	cfg.Tools.Exec.Backend = "host"
	cfg.Tools.Exec.HostLimits = config.ExecHostLimitsConfig{
		MemoryMB:   64,
		CPUSeconds: 2,
		FileSizeMB: 8,
		NProc:      32,
	}

	tool, err := NewExecToolWithConfig(t.TempDir(), true, cfg)
	if err != nil {
		t.Fatalf("unable to configure exec tool: %s", err)
	}

	got := tool.withHostLimits("echo hi")
	if !strings.Contains(got, "ulimit -v") {
		t.Fatalf("expected wrapped command to include ulimit -v, got: %q", got)
	}
	if !strings.Contains(got, "ulimit -t") {
		t.Fatalf("expected wrapped command to include ulimit -t, got: %q", got)
	}
	if !strings.Contains(got, "ulimit -f") {
		t.Fatalf("expected wrapped command to include ulimit -f, got: %q", got)
	}
	if !strings.Contains(got, "ulimit -u") {
		t.Fatalf("expected wrapped command to include ulimit -u, got: %q", got)
	}
	if !strings.HasSuffix(got, "echo hi") {
		t.Fatalf("expected wrapped command to end with original command, got: %q", got)
	}
}

func decodeSessionID(t *testing.T, payload string) string {
	t.Helper()
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		t.Fatalf("failed to decode exec payload: %v", err)
	}
	if out.SessionID == "" {
		t.Fatalf("missing session_id in payload: %s", payload)
	}
	return out.SessionID
}

func TestExecBackground_WithProcessPoll(t *testing.T) {
	execTool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatalf("unable to configure exec tool: %v", err)
	}
	processTool := NewProcessTool(execTool.ProcessManager())

	debug.FreeOSMemory()
	start := execTool.Execute(context.Background(), map[string]any{
		"command":    "sleep 0.2; echo done",
		"background": true,
	})
	if start.IsError {
		t.Fatalf("exec background failed: %s", start.ForLLM)
	}
	sessionID := decodeSessionID(t, start.ForLLM)

	poll := processTool.Execute(context.Background(), map[string]any{
		"action":     "poll",
		"session_id": sessionID,
		"timeout_ms": 3000,
	})
	if poll.IsError {
		t.Fatalf("process poll failed: %s", poll.ForLLM)
	}

	var payload struct {
		Output  string `json:"output"`
		Session struct {
			Status string `json:"status"`
		} `json:"session"`
	}
	if err := json.Unmarshal([]byte(poll.ForLLM), &payload); err != nil {
		t.Fatalf("failed to decode poll payload: %v", err)
	}
	if payload.Session.Status == "running" {
		poll = processTool.Execute(context.Background(), map[string]any{
			"action":     "poll",
			"session_id": sessionID,
			"timeout_ms": 2000,
		})
		if poll.IsError {
			t.Fatalf("second poll failed: %s", poll.ForLLM)
		}
		if err := json.Unmarshal([]byte(poll.ForLLM), &payload); err != nil {
			t.Fatalf("failed to decode second poll payload: %v", err)
		}
	}
	if !strings.Contains(payload.Output, "done") && payload.Session.Status != "completed" {
		t.Fatalf("expected output to contain done or completed status, got: status=%q output=%q", payload.Session.Status, payload.Output)
	}
	if payload.Session.Status != "completed" {
		t.Fatalf("expected completed status, got %q", payload.Session.Status)
	}
}

func TestProcessTool_KillAndRemove(t *testing.T) {
	execTool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatalf("unable to configure exec tool: %v", err)
	}
	processTool := NewProcessTool(execTool.ProcessManager())

	debug.FreeOSMemory()
	start := execTool.Execute(context.Background(), map[string]any{
		"command":    "sleep 60",
		"background": true,
	})
	if start.IsError {
		t.Fatalf("exec background failed: %s", start.ForLLM)
	}
	sessionID := decodeSessionID(t, start.ForLLM)

	kill := processTool.Execute(context.Background(), map[string]any{
		"action":     "kill",
		"session_id": sessionID,
	})
	if kill.IsError {
		t.Fatalf("process kill failed: %s", kill.ForLLM)
	}

	poll := processTool.Execute(context.Background(), map[string]any{
		"action":     "poll",
		"session_id": sessionID,
		"timeout_ms": 3000,
	})
	if poll.IsError {
		t.Fatalf("process poll after kill failed: %s", poll.ForLLM)
	}

	var pollPayload struct {
		Session struct {
			Status string `json:"status"`
		} `json:"session"`
	}
	if err := json.Unmarshal([]byte(poll.ForLLM), &pollPayload); err != nil {
		t.Fatalf("failed to decode poll payload: %v", err)
	}
	if pollPayload.Session.Status == "running" {
		t.Fatalf("expected killed session to stop running")
	}

	remove := processTool.Execute(context.Background(), map[string]any{
		"action":     "remove",
		"session_id": sessionID,
	})
	if remove.IsError {
		t.Fatalf("process remove failed: %s", remove.ForLLM)
	}
}

func TestProcessTool_Write(t *testing.T) {
	execTool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatalf("unable to configure exec tool: %v", err)
	}
	processTool := NewProcessTool(execTool.ProcessManager())

	debug.FreeOSMemory()
	start := execTool.Execute(context.Background(), map[string]any{
		"command":    "cat",
		"background": true,
	})
	if start.IsError {
		t.Fatalf("exec background failed: %s", start.ForLLM)
	}
	sessionID := decodeSessionID(t, start.ForLLM)

	write := processTool.Execute(context.Background(), map[string]any{
		"action":     "write",
		"session_id": sessionID,
		"data":       "ping\n",
		"eof":        true,
	})
	if write.IsError {
		t.Fatalf("process write failed: %s", write.ForLLM)
	}

	poll := processTool.Execute(context.Background(), map[string]any{
		"action":     "poll",
		"session_id": sessionID,
		"timeout_ms": 3000,
	})
	if poll.IsError {
		t.Fatalf("poll after write failed: %s", poll.ForLLM)
	}

	var payload struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal([]byte(poll.ForLLM), &payload); err != nil {
		t.Fatalf("failed to decode poll payload: %v", err)
	}
	if !strings.Contains(payload.Output, "ping") {
		t.Fatalf("expected output to contain ping, got %q", payload.Output)
	}
}

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func TestShellTool_TimeoutKillsChildProcess(t *testing.T) {
	v := strings.TrimSpace(os.Getenv("X_CLAW_TEST_MEMLIMIT"))
	if v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 && n < (512<<20) {
			t.Skipf("skipping fork-heavy timeout test in constrained env (X_CLAW_TEST_MEMLIMIT=%d)", n)
		}
	}

	tool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Errorf("unable to configure exec tool: %s", err)
	}

	tool.SetTimeout(500 * time.Millisecond)

	args := map[string]any{
		// Spawn a child process that would outlive the shell unless process-group kill is used.
		"command": "sleep 5 & echo $! > child.pid; wait",
	}

	// This test forks a child process. In memory-constrained environments,
	// forcing a GC + returning pages to the OS avoids the OOM killer targeting
	// the test binary at fork time.
	debug.FreeOSMemory()

	result := tool.Execute(context.Background(), args)
	if !result.IsError {
		t.Fatalf("expected timeout error, got success: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "timed out") {
		t.Fatalf("expected timeout message, got: %s", result.ForLLM)
	}

	childPIDPath := filepath.Join(tool.workingDir, "child.pid")
	data, err := os.ReadFile(childPIDPath)
	if err != nil {
		t.Fatalf("failed to read child pid file: %v", err)
	}

	childPID, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("failed to parse child pid: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(childPID) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("child process %d is still running after timeout", childPID)
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return string(data)
}

func TestNewExecToolWithConfig_DoesNotWriteCustomDenyPatternsToStdout(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Exec.EnableDenyPatterns = true
	cfg.Tools.Exec.CustomDenyPatterns = []string{"echo.*"}

	stdout := captureStdout(t, func() {
		tool, err := NewExecToolWithConfig(t.TempDir(), false, cfg)
		if err != nil {
			t.Fatalf("NewExecToolWithConfig error: %v", err)
		}
		if tool == nil {
			t.Fatal("expected tool")
		}
	})

	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("expected no stdout output, got %q", stdout)
	}
}

func TestNewExecToolWithConfig_DoesNotWriteDisabledWarningToStdout(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Exec.EnableDenyPatterns = false

	stdout := captureStdout(t, func() {
		tool, err := NewExecToolWithConfig(t.TempDir(), false, cfg)
		if err != nil {
			t.Fatalf("NewExecToolWithConfig error: %v", err)
		}
		if tool == nil {
			t.Fatal("expected tool")
		}
	})

	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("expected no stdout output, got %q", stdout)
	}
}
