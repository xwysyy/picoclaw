package tools

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
)

// TestMain sets conservative runtime knobs so `go test ./pkg/tools` is stable in
// memory-constrained environments. This package executes many unit tests and
// also forks child processes (exec tool), which can trigger the OOM killer when
// the test binary's RSS grows too large.
//
// To disable these knobs, set X_CLAW_TEST_MEMLIMIT=0 (or legacy PICOCLAW_TEST_MEMLIMIT=0).
func TestMain(m *testing.M) {
	memLimit := int64(256 << 20) // 256 MiB default
	raw := strings.TrimSpace(os.Getenv("X_CLAW_TEST_MEMLIMIT"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("PICOCLAW_TEST_MEMLIMIT"))
	}
	if raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			memLimit = n
		}
	}

	if memLimit > 0 {
		// Keep RSS lower to reduce OOM-kill risk at fork/exec time.
		debug.SetMemoryLimit(memLimit)
		// More aggressive GC than default (100) to reduce peak heap size.
		debug.SetGCPercent(20)
	}

	os.Exit(m.Run())
}
