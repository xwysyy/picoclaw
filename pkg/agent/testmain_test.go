package agent

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
)

// TestMain applies conservative GC/memory knobs to keep the agent test suite
// stable in memory-constrained environments. Some tests exercise memory and
// embedding paths that can temporarily increase heap usage.
//
// To disable, set X_CLAW_TEST_MEMLIMIT=0 (or legacy PICOCLAW_TEST_MEMLIMIT=0).
func TestMain(m *testing.M) {
	memLimit := int64(384 << 20) // 384 MiB default
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
		debug.SetMemoryLimit(memLimit)
		debug.SetGCPercent(20)
	}

	os.Exit(m.Run())
}
