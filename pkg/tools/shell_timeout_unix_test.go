//go:build !windows

package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func TestShellTool_TimeoutKillsChildProcess(t *testing.T) {
	v := strings.TrimSpace(os.Getenv("X_CLAW_TEST_MEMLIMIT"))
	if v == "" {
		v = strings.TrimSpace(os.Getenv("PICOCLAW_TEST_MEMLIMIT"))
	}
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
