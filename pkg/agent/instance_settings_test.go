package agent

import (
	"testing"

	"github.com/xwysyy/X-Claw/pkg/config"
)

func TestResolveCompaction_Defaults(t *testing.T) {
	got := resolveCompaction(config.AgentCompactionConfig{})

	if got.Mode != "safeguard" {
		t.Fatalf("Mode = %q, want %q", got.Mode, "safeguard")
	}
	if got.ReserveTokens != 2048 {
		t.Fatalf("ReserveTokens = %d, want %d", got.ReserveTokens, 2048)
	}
	if got.KeepRecentTokens != 2048 {
		t.Fatalf("KeepRecentTokens = %d, want %d", got.KeepRecentTokens, 2048)
	}
	if got.MaxHistoryShare != 0.5 {
		t.Fatalf("MaxHistoryShare = %v, want %v", got.MaxHistoryShare, 0.5)
	}

	// Default behavior: memory flush is enabled unless explicitly configured.
	if got.MemoryFlushEnabled != true {
		t.Fatalf("MemoryFlushEnabled = %v, want true", got.MemoryFlushEnabled)
	}
	if got.MemoryFlushSoftThreshold != 1500 {
		t.Fatalf("MemoryFlushSoftThreshold = %d, want %d", got.MemoryFlushSoftThreshold, 1500)
	}
}

func TestResolveCompaction_MemoryFlushExplicitDisable(t *testing.T) {
	got := resolveCompaction(config.AgentCompactionConfig{
		MemoryFlush: config.AgentCompactionMemoryFlushConfig{
			Enabled:             false,
			SoftThresholdTokens: 100,
		},
	})

	if got.MemoryFlushEnabled != false {
		t.Fatalf("MemoryFlushEnabled = %v, want false", got.MemoryFlushEnabled)
	}
	if got.MemoryFlushSoftThreshold != 100 {
		t.Fatalf("MemoryFlushSoftThreshold = %d, want %d", got.MemoryFlushSoftThreshold, 100)
	}
}

func TestResolveContextPruning_DefaultsAndBounds(t *testing.T) {
	got := resolveContextPruning(config.AgentContextPruningConfig{
		// Intentionally set out-of-range values to exercise fallback.
		Mode:         "",
		TriggerRatio: 1, // invalid (hi exclusive)
	})

	if got.Mode != "tools_only" {
		t.Fatalf("Mode = %q, want %q", got.Mode, "tools_only")
	}
	if got.SoftToolChars != 2000 {
		t.Fatalf("SoftToolChars = %d, want %d", got.SoftToolChars, 2000)
	}
	if got.HardToolChars != 350 {
		t.Fatalf("HardToolChars = %d, want %d", got.HardToolChars, 350)
	}
	if got.TriggerRatio != 0.8 {
		t.Fatalf("TriggerRatio = %v, want %v", got.TriggerRatio, 0.8)
	}
}

func TestResolveMemoryVector_Defaults(t *testing.T) {
	got := resolveMemoryVector(config.AgentMemoryVectorConfig{})

	if got.Enabled {
		t.Fatalf("Enabled = true, want false")
	}
	if got.Dimensions != defaultMemoryVectorDimensions {
		t.Fatalf("Dimensions = %d, want %d", got.Dimensions, defaultMemoryVectorDimensions)
	}
	if got.TopK != defaultMemoryVectorTopK {
		t.Fatalf("TopK = %d, want %d", got.TopK, defaultMemoryVectorTopK)
	}
	if got.MinScore != defaultMemoryVectorMinScore {
		t.Fatalf("MinScore = %v, want %v", got.MinScore, defaultMemoryVectorMinScore)
	}
	if got.MaxContextChars != defaultMemoryVectorMaxContextChars {
		t.Fatalf("MaxContextChars = %d, want %d", got.MaxContextChars, defaultMemoryVectorMaxContextChars)
	}
	if got.RecentDailyDays != defaultMemoryVectorRecentDailyDays {
		t.Fatalf("RecentDailyDays = %d, want %d", got.RecentDailyDays, defaultMemoryVectorRecentDailyDays)
	}
}
