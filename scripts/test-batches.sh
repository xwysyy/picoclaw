#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${root_dir}"

usage() {
  cat <<'USAGE'
Usage: scripts/test-batches.sh [options] [-- <extra go test flags>]

Runs a memory-stable Go test workflow for this repository:
1. `go build ./...`
2. `go vet ./...`
3. `go test ./... -run '^$'` (compile-only, all packages)
4. Per-package test execution in separate processes
5. `pkg/agent` runs one top-level test per batch to avoid SIGKILL(137)
6. Optional race-safe batch for packages that are stable in this environment

Options:
  --dry-run       Print commands without executing them
  --skip-build    Skip `go build ./...`
  --skip-vet      Skip `go vet ./...`
  --race-safe     Also run stable race batches (`pkg/session`, `pkg/httpapi`)
  -h, --help      Show this help

Environment overrides:
  GOMAXPROCS                    Default: 1
  GOMEMLIMIT                    Default: 1024MiB
  GOGC                          Default: 50
  X_CLAW_GO_CACHE_DIR           Default: <repo>/.cache
  X_CLAW_GO_TEST_P              Default: 1
  X_CLAW_TEST_PARALLEL          Default: 1
  X_CLAW_TEST_PKGS              Optional whitespace-separated package list override
  X_CLAW_AGENT_TESTS_PER_BATCH  Default: 1

Examples:
  scripts/test-batches.sh
  scripts/test-batches.sh --dry-run
  scripts/test-batches.sh --race-safe
  X_CLAW_TEST_PKGS='./pkg/channels ./pkg/httpapi' scripts/test-batches.sh -- -run TestFoo
USAGE
}

dry_run=0
skip_build=0
skip_vet=0
race_safe=0
extra_go_test_args=()

while (($#)); do
  case "$1" in
    --dry-run)
      dry_run=1
      shift
      ;;
    --skip-build)
      skip_build=1
      shift
      ;;
    --skip-vet)
      skip_vet=1
      shift
      ;;
    --race-safe)
      race_safe=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --)
      shift
      extra_go_test_args=("$@")
      break
      ;;
    *)
      extra_go_test_args+=("$1")
      shift
      ;;
  esac
done

cache_dir="${X_CLAW_GO_CACHE_DIR:-${root_dir}/.cache}"
export GOMODCACHE="${GOMODCACHE:-${cache_dir}/gomod}"
export GOPATH="${GOPATH:-${cache_dir}/gopath}"
export GOCACHE="${GOCACHE:-${cache_dir}/gocache}"
mkdir -p "${GOMODCACHE}" "${GOPATH}" "${GOCACHE}"

export GOMAXPROCS="${GOMAXPROCS:-1}"
export GOMEMLIMIT="${GOMEMLIMIT:-1024MiB}"
export GOGC="${GOGC:-50}"

go_test_p="${X_CLAW_GO_TEST_P:-1}"
go_test_parallel="${X_CLAW_TEST_PARALLEL:-1}"
agent_tests_per_batch="${X_CLAW_AGENT_TESTS_PER_BATCH:-1}"

run_cmd() {
  local label="$1"
  shift
  echo
  echo "== ${label} =="
  echo "+ $*"
  if [[ "${dry_run}" -eq 1 ]]; then
    return 0
  fi
  "$@"
}

join_regex() {
  local regex=""
  local name
  for name in "$@"; do
    if [[ -n "${regex}" ]]; then
      regex+="|"
    fi
    regex+="${name}"
  done
  printf '^(%s)$' "${regex}"
}

packages_with_tests() {
  if [[ -n "${X_CLAW_TEST_PKGS:-}" ]]; then
    printf '%s\n' ${X_CLAW_TEST_PKGS}
    return 0
  fi
  go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... | sed '/^$/d'
}

run_compile_only_all() {
  if [[ -n "${X_CLAW_TEST_PKGS:-}" ]]; then
    local -a compile_pkgs=()
    while IFS= read -r pkg; do
      [[ -n "${pkg}" ]] || continue
      compile_pkgs+=("${pkg}")
    done < <(packages_with_tests)
    run_cmd \
      "go test compile-only selected packages" \
      go test "${compile_pkgs[@]}" -run '^$' -count=1 -p "${go_test_p}" -parallel "${go_test_parallel}" "${extra_go_test_args[@]}"
    return 0
  fi

  run_cmd \
    "go test compile-only all" \
    go test ./... -run '^$' -count=1 -p "${go_test_p}" -parallel "${go_test_parallel}" "${extra_go_test_args[@]}"
}

run_package_tests() {
  local pkg="$1"
  run_cmd \
    "${pkg}" \
    go test "${pkg}" -count=1 -p "${go_test_p}" -parallel "${go_test_parallel}" "${extra_go_test_args[@]}"
}

run_agent_batched() {
  local pkg="./pkg/agent"
  local -a tests=()
  local line

  while IFS= read -r line; do
    [[ "${line}" == Test* ]] || continue
    tests+=("${line}")
  done < <(go test -list . "${pkg}" 2>/dev/null)

  if ((${#tests[@]} == 0)); then
    echo
    echo "== ${pkg} =="
    echo "(no tests discovered)"
    return 0
  fi

  local total="${#tests[@]}"
  local index=0
  while ((index < total)); do
    local -a batch=()
    local n=0
    while ((index < total && n < agent_tests_per_batch)); do
      batch+=("${tests[index]}")
      index=$((index + 1))
      n=$((n + 1))
    done
    local regex
    regex="$(join_regex "${batch[@]}")"
    run_cmd \
      "${pkg} batch ${index}/${total}" \
      go test "${pkg}" -run "${regex}" -count=1 -p "${go_test_p}" -parallel "${go_test_parallel}" "${extra_go_test_args[@]}"
  done
}

run_race_safe_batches() {
  run_cmd \
    "race pkg/session" \
    go test -race ./pkg/session -count=1 -p 1 -parallel 1
  run_cmd \
    "race pkg/httpapi" \
    go test -race ./pkg/httpapi -count=1 -p 1 -parallel 1
}

if [[ "${skip_build}" -eq 0 ]]; then
  run_cmd "go build" go build ./...
fi

if [[ "${skip_vet}" -eq 0 ]]; then
  run_cmd "go vet" go vet ./...
fi

run_compile_only_all

while IFS= read -r pkg; do
  [[ -n "${pkg}" ]] || continue
  if [[ "${pkg}" == "github.com/xwysyy/X-Claw/pkg/agent" ]]; then
    continue
  fi
  run_package_tests "${pkg}"
done < <(packages_with_tests)

should_run_agent=1
if [[ -n "${X_CLAW_TEST_PKGS:-}" ]]; then
  should_run_agent=0
  while IFS= read -r pkg; do
    if [[ "${pkg}" == "github.com/xwysyy/X-Claw/pkg/agent" || "${pkg}" == "./pkg/agent" ]]; then
      should_run_agent=1
      break
    fi
  done < <(printf '%s
' ${X_CLAW_TEST_PKGS})
fi

if [[ "${should_run_agent}" -eq 1 ]]; then
  run_agent_batched
fi

if [[ "${race_safe}" -eq 1 ]]; then
  run_race_safe_batches
fi
