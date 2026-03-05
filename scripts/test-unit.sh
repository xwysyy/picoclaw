#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${root_dir}"

# Use repo-local Go caches by default.
#
# Why:
# - Keeps `go test` functional in restricted environments (no writes to $HOME).
# - Avoids re-downloading modules/toolchains on CI hosts with ephemeral home dirs.
# - `.cache/` is gitignored, so it does not pollute commits.
cache_dir="${X_CLAW_GO_CACHE_DIR:-${root_dir}/.cache}"
export GOMODCACHE="${GOMODCACHE:-${cache_dir}/gomod}"
export GOPATH="${GOPATH:-${cache_dir}/gopath}"
export GOCACHE="${GOCACHE:-${cache_dir}/gocache}"

mkdir -p "${GOMODCACHE}" "${GOPATH}" "${GOCACHE}"

# Some environments (small VMs / SBCs) may OOM-kill `go test ./...`.
# Running packages one-by-one is more memory stable.
#
# Also default to a single OS thread unless the caller explicitly overrides it,
# which further reduces peak memory usage during tests.
export GOMAXPROCS="${GOMAXPROCS:-1}"

# Additional safety valves for constrained environments:
# - `GOMEMLIMIT` nudges the Go runtime (including compiler/linker processes) to
#   keep heap usage under a soft cap.
# - Lower `GOGC` trades some CPU for less peak memory.
#
# Callers can override by exporting these vars before running the script.
export GOMEMLIMIT="${GOMEMLIMIT:-1024MiB}"
export GOGC="${GOGC:-50}"

bins_dir="${X_CLAW_GO_TEST_BINS_DIR:-${cache_dir}/testbins}"
mkdir -p "${bins_dir}"

packages="${X_CLAW_TEST_PKGS:-}"
if [[ -z "${packages}" ]]; then
  packages="$(go list ./... 2>/dev/null)"
fi
if [[ -z "${packages}" ]]; then
  echo "Failed to resolve packages via 'go list ./...'" >&2
  go list ./... >&2
  exit 1
fi

total="$(echo "${packages}" | wc -w | tr -d ' ')"
idx=0

# Derive on-disk package directories without an extra `go list` per package.
module_path="$(go list -m -f '{{.Path}}' 2>/dev/null || true)"
if [[ -z "${module_path}" ]]; then
  # Best-effort fallback: most repos are run from module root.
  module_path=""
fi

# Convert a subset of `go test` flags to test-binary (`-test.*`) flags when we
# run the compiled test binary directly. This allows us to build and run in
# separate processes (lower peak memory) without surprising callers.
testbin_args=()
argv=("$@")
skip_next=0
for ((i = 0; i < ${#argv[@]}; i++)); do
  if ((skip_next > 0)); then
    skip_next=$((skip_next - 1))
    continue
  fi

  a="${argv[i]}"
  case "${a}" in
    -v)
      testbin_args+=("-test.v")
      ;;
    -short)
      testbin_args+=("-test.short")
      ;;
    -failfast)
      testbin_args+=("-test.failfast")
      ;;
    -run=*)
      testbin_args+=("-test.run=${a#-run=}")
      ;;
    -run)
      if ((i + 1 < ${#argv[@]})); then
        testbin_args+=("-test.run=${argv[i + 1]}")
        skip_next=1
      fi
      ;;
    -timeout=*)
      testbin_args+=("-test.timeout=${a#-timeout=}")
      ;;
    -timeout)
      if ((i + 1 < ${#argv[@]})); then
        testbin_args+=("-test.timeout=${argv[i + 1]}")
        skip_next=1
      fi
      ;;
    -parallel=*)
      testbin_args+=("-test.parallel=${a#-parallel=}")
      ;;
    -parallel)
      if ((i + 1 < ${#argv[@]})); then
        testbin_args+=("-test.parallel=${argv[i + 1]}")
        skip_next=1
      fi
      ;;
    -coverprofile=*)
      testbin_args+=("-test.coverprofile=${a#-coverprofile=}")
      ;;
    -coverprofile)
      if ((i + 1 < ${#argv[@]})); then
        testbin_args+=("-test.coverprofile=${argv[i + 1]}")
        skip_next=1
      fi
      ;;
  esac
done

for pkg in ${packages}; do
  idx=$((idx + 1))
  echo "=== [${idx}/${total}] ${pkg}"

  # `-p` limits how many packages are built in parallel (including dependencies),
  # which helps avoid swap storms and OOM kills on low-memory machines.
  #
  # We compile the test binary and run it as a separate process to keep peak
  # memory lower than `go test` (which may hold build state while executing).
  bin_name="${pkg//\//_}"
  bin_name="${bin_name//./_}"
  bin_path="${bins_dir}/${bin_name}.test"
  rm -f "${bin_path}"

  go test -p "${X_CLAW_GO_TEST_P:-1}" -c -o "${bin_path}" "$@" "${pkg}"

  # Packages with no tests won't produce a binary even with `-c`.
  if [[ ! -f "${bin_path}" ]]; then
    continue
  fi

  pkg_dir="${root_dir}"
  if [[ -n "${module_path}" && "${pkg}" == "${module_path}"* ]]; then
    rel="${pkg#${module_path}}"
    rel="${rel#/}"
    if [[ -n "${rel}" ]]; then
      pkg_dir="${root_dir}/${rel}"
    fi
  fi

  (cd "${pkg_dir}" && "${bin_path}" "${testbin_args[@]}")
  echo "ok   ${pkg}"
done
