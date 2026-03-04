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
export PICOCLAW_TEST_MEMLIMIT="${PICOCLAW_TEST_MEMLIMIT:-268435456}" # 256MiB
# The Go compiler is also a Go program; in tight memory environments it can be
# OOM-killed when building large packages (e.g. pkg/agent). Keep a conservative
# memory limit and GC target to reduce peak RSS.
export GOMEMLIMIT="${GOMEMLIMIT:-192MiB}"
export GOGC="${GOGC:-30}"
# Reduce peak RSS in both the compiler and test binaries.
export GOMAXPROCS="${GOMAXPROCS:-1}"
export GODEBUG="${GODEBUG:-madvdontneed=1}"

go test -p 1 ./internal/archcheck -count=1
go test -p 1 ./pkg/utils -count=1

# Agent full-suite may be too heavy in CI sandboxes; run key regression tests.
set +e
go test -p 1 ./pkg/agent -run 'TestTargetReasoningChannelID_AllChannels|TestHandleReasoning|TestSanitizeHistoryForProvider' -count=1
rc=$?
set -e
if [[ $rc -ne 0 ]]; then
  if [[ $rc -eq 137 ]]; then
    echo "pkg/agent tests were OOM-killed (rc=137); retrying with more aggressive GC..."
    GOMEMLIMIT="${GOMEMLIMIT_RETRY:-160MiB}" \
      GOGC="${GOGC_RETRY:-20}" \
      go test -p 1 ./pkg/agent -run 'TestTargetReasoningChannelID_AllChannels|TestHandleReasoning|TestSanitizeHistoryForProvider' -count=1
  else
    exit $rc
  fi
fi

# Tools full-suite can be killed in memory-constrained environments; run key regression tests.
go test -p 1 ./pkg/tools -run 'TestExecuteToolCalls_MaxResultChars_UsesHeadTailTruncation' -count=1
go test -p 1 ./pkg/tools -run 'TestExecBackground_WithProcessPoll' -count=1
go test -p 1 ./pkg/tools -run 'TestShellTool_TimeoutKillsChildProcess' -count=1

go test -p 1 ./cmd/picoclaw/internal/gateway -count=1
