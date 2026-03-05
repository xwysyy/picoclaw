#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

# In sandboxed environments, ~/.cache can be read-only. Go defaults GOCACHE to
# ~/.cache/go-build, which then breaks builds/tests. Use /tmp by default.
if [[ -z "${GOCACHE:-}" ]]; then
  export GOCACHE="/tmp/go-build"
fi
mkdir -p "$GOCACHE"

echo "GOCACHE=$GOCACHE"

# Keep the suite stable in constrained environments.
# Prefer the new X-Claw env var, but keep the legacy env var name as backward-compat.
export X_CLAW_TEST_MEMLIMIT="${X_CLAW_TEST_MEMLIMIT:-${PICOCLAW_TEST_MEMLIMIT:-268435456}}" # 256MiB
# The Go compiler is also a Go program; in tight memory environments it can be
# OOM-killed when building large packages (e.g. pkg/agent). Keep a conservative
# memory limit and GC target to reduce peak RSS.
export GOMEMLIMIT="${GOMEMLIMIT:-192MiB}"
export GOGC="${GOGC:-30}"
# Reduce peak RSS in both the compiler and test binaries.
export GOMAXPROCS="${GOMAXPROCS:-1}"
export GODEBUG="${GODEBUG:-madvdontneed=1}"

run_go_test_with_oom_retry() {
  local label="$1"
  shift

  local attempt=0
  while true; do
    local tmp
    tmp="$(mktemp)"

    set +e
    if [[ $attempt -eq 0 ]]; then
      "$@" >"$tmp" 2>&1
    elif [[ $attempt -eq 1 ]]; then
      GOMEMLIMIT="${GOMEMLIMIT_RETRY:-160MiB}" \
        GOGC="${GOGC_RETRY:-20}" \
        "$@" >"$tmp" 2>&1
    else
      GOMEMLIMIT="${GOMEMLIMIT_RETRY2:-128MiB}" \
        GOGC="${GOGC_RETRY2:-10}" \
        "$@" >"$tmp" 2>&1
    fi
    local rc=$?
    cat "$tmp"
    set -e

    local killed=false
    if [[ $rc -eq 137 ]]; then
      killed=true
    elif grep -q "signal: killed" "$tmp"; then
      killed=true
    fi
    rm -f "$tmp"

    if [[ $rc -eq 0 ]]; then
      return 0
    fi
    if [[ $killed != true ]]; then
      exit $rc
    fi

    if [[ $attempt -eq 0 ]]; then
      echo "$label tests were killed (likely OOM); retrying with more aggressive GC..."
    elif [[ $attempt -eq 1 ]]; then
      echo "$label tests were killed again; retrying with even more aggressive GC..."
    else
      echo "$label tests still killed after retries; failing."
      exit $rc
    fi
    attempt=$((attempt + 1))
  done
}

go test -p 1 ./internal/archcheck -count=1
go test -p 1 ./internal/core/routing -count=1
go test -p 1 ./pkg/utils -count=1

# Agent full-suite may be too heavy in CI sandboxes; run key regression tests.
# Run them as separate processes to avoid peak-RSS accumulation across tests.
run_go_test_with_oom_retry "pkg/agent (reasoning channel)" \
  go test -p 1 ./pkg/agent -run 'TestTargetReasoningChannelID_AllChannels' -count=1
run_go_test_with_oom_retry "pkg/agent (reasoning handler)" \
  go test -p 1 ./pkg/agent -run 'TestHandleReasoning' -count=1
run_go_test_with_oom_retry "pkg/agent (history sanitize)" \
  go test -p 1 ./pkg/agent -run 'TestSanitizeHistoryForProvider' -count=1

# Tools full-suite can be killed in memory-constrained environments; run key regression tests.
run_go_test_with_oom_retry "pkg/tools" \
  go test -p 1 ./pkg/tools -run 'TestExecuteToolCalls_MaxResultChars_UsesHeadTailTruncation' -count=1
run_go_test_with_oom_retry "pkg/tools" \
  go test -p 1 ./pkg/tools -run 'TestExecBackground_WithProcessPoll' -count=1
run_go_test_with_oom_retry "pkg/tools" \
  go test -p 1 ./pkg/tools -run 'TestShellTool_TimeoutKillsChildProcess' -count=1

go test -p 1 ./cmd/x-claw/internal/gateway -count=1
