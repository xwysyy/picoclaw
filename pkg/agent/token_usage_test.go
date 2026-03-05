package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xwysyy/X-Claw/pkg/providers"
)

func TestTokenUsageStoreRecord_IgnoresEmptyUsage(t *testing.T) {
	workspace := t.TempDir()
	store := newTokenUsageStore(workspace)
	if store == nil {
		t.Fatalf("expected store")
	}

	store.Record("gpt-x", &providers.UsageInfo{})

	if _, err := os.Stat(filepath.Join(workspace, "state", "token_usage.json")); err == nil {
		t.Fatalf("expected token_usage.json not to be created for empty usage")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat token_usage.json: %v", err)
	}
}

func TestTokenUsageStoreRecord_ComputesTotalWhenMissing(t *testing.T) {
	workspace := t.TempDir()
	store := newTokenUsageStore(workspace)
	if store == nil {
		t.Fatalf("expected store")
	}

	store.Record("m1", &providers.UsageInfo{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      0,
	})

	data, err := os.ReadFile(filepath.Join(workspace, "state", "token_usage.json"))
	if err != nil {
		t.Fatalf("read token_usage.json: %v", err)
	}

	var snap tokenUsageSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}

	if got, want := snap.Totals.TotalTokens, int64(15); got != want {
		t.Fatalf("totals.total_tokens = %d, want %d", got, want)
	}
	if got, want := snap.ByModel["m1"].TotalTokens, int64(15); got != want {
		t.Fatalf("by_model[m1].total_tokens = %d, want %d", got, want)
	}
}

func TestTokenUsageStoreRecord_AccumulatesByModelAndTotals(t *testing.T) {
	workspace := t.TempDir()
	store := newTokenUsageStore(workspace)
	if store == nil {
		t.Fatalf("expected store")
	}

	store.Record("m1", &providers.UsageInfo{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5})
	store.Record("m2", &providers.UsageInfo{PromptTokens: 7, CompletionTokens: 11, TotalTokens: 18})
	store.Record("m1", &providers.UsageInfo{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2})

	data, err := os.ReadFile(filepath.Join(workspace, "state", "token_usage.json"))
	if err != nil {
		t.Fatalf("read token_usage.json: %v", err)
	}

	var snap tokenUsageSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}

	if got, want := snap.Totals.Requests, int64(3); got != want {
		t.Fatalf("totals.requests = %d, want %d", got, want)
	}
	if got, want := snap.Totals.PromptTokens, int64(10); got != want {
		t.Fatalf("totals.prompt_tokens = %d, want %d", got, want)
	}
	if got, want := snap.Totals.CompletionTokens, int64(15); got != want {
		t.Fatalf("totals.completion_tokens = %d, want %d", got, want)
	}
	if got, want := snap.Totals.TotalTokens, int64(25); got != want {
		t.Fatalf("totals.total_tokens = %d, want %d", got, want)
	}

	if got, want := snap.ByModel["m1"].Requests, int64(2); got != want {
		t.Fatalf("by_model[m1].requests = %d, want %d", got, want)
	}
	if got, want := snap.ByModel["m1"].TotalTokens, int64(7); got != want {
		t.Fatalf("by_model[m1].total_tokens = %d, want %d", got, want)
	}
	if got, want := snap.ByModel["m2"].Requests, int64(1); got != want {
		t.Fatalf("by_model[m2].requests = %d, want %d", got, want)
	}
	if got, want := snap.ByModel["m2"].TotalTokens, int64(18); got != want {
		t.Fatalf("by_model[m2].total_tokens = %d, want %d", got, want)
	}
}
