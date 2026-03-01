#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${root_dir}"

# Keep toolchain selection deterministic.
#
# In this environment the system `go` wrapper may be older than the version
# required by go.mod. With `GOTOOLCHAIN=auto`, some coverage invocations can
# fail with:
#   compile: version "goX.Y.Z" does not match go tool version "goA.B.C"
#
# Pinning to the version declared in go.mod avoids partial toolchain switching.
if [[ -z "${GOTOOLCHAIN:-}" || "${GOTOOLCHAIN}" == "auto" ]]; then
  go_mod_version="$(awk '$1=="go"{print $2; exit}' go.mod 2>/dev/null || true)"
  if [[ -n "${go_mod_version}" ]]; then
    export GOTOOLCHAIN="go${go_mod_version}"
  fi
fi

# Use repo-local Go caches by default (same rationale as scripts/test-unit.sh).
cache_dir="${PICOCLAW_GO_CACHE_DIR:-${root_dir}/.cache}"
export GOMODCACHE="${GOMODCACHE:-${cache_dir}/gomod}"
export GOPATH="${GOPATH:-${cache_dir}/gopath}"
export GOCACHE="${GOCACHE:-${cache_dir}/gocache}"

mkdir -p "${GOMODCACHE}" "${GOPATH}" "${GOCACHE}"

# Coverage runs are heavier than normal tests. Keep it conservative by default.
export GOMAXPROCS="${GOMAXPROCS:-1}"
export GOMEMLIMIT="${GOMEMLIMIT:-1024MiB}"
export GOGC="${GOGC:-50}"

cover_dir="${PICOCLAW_COVER_DIR:-${cache_dir}/coverage}"
profiles_dir="${cover_dir}/profiles"
bins_dir="${cover_dir}/bins"

mkdir -p "${profiles_dir}" "${bins_dir}"

# Clean previous run artifacts (avoid mixing old/new profiles).
rm -f "${profiles_dir}/"*.out 2>/dev/null || true
rm -f "${bins_dir}/"*.test 2>/dev/null || true

packages="${PICOCLAW_TEST_PKGS:-}"
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

module_path="$(go list -m -f '{{.Path}}' 2>/dev/null || true)"
if [[ -z "${module_path}" ]]; then
  module_path=""
fi

# Map a small set of common `go test` flags to `-test.*` flags for the compiled
# test binary. (Coverage output paths are handled by this script.)
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
  esac
done

covermode="${PICOCLAW_COVERMODE:-set}"
merged_profile="${PICOCLAW_COVERPROFILE:-${root_dir}/coverage.out}"
html_out="${PICOCLAW_COVERHTML:-${root_dir}/coverage.html}"
threshold="${PICOCLAW_COVER_THRESHOLD:-}"

profiles=()

for pkg in ${packages}; do
  idx=$((idx + 1))
  echo "=== [${idx}/${total}] ${pkg}"

  bin_name="${pkg//\//_}"
  bin_name="${bin_name//./_}"
  bin_path="${bins_dir}/${bin_name}.test"
  profile_path="${profiles_dir}/${bin_name}.out"

  rm -f "${bin_path}" "${profile_path}"

  # Compile with coverage instrumentation.
  go test -p "${PICOCLAW_GO_TEST_P:-1}" -c -cover -covermode="${covermode}" -o "${bin_path}" "${pkg}"

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

  # Run the test binary and write a per-package cover profile.
  (cd "${pkg_dir}" && "${bin_path}" -test.coverprofile="${profile_path}" "${testbin_args[@]}")
  profiles+=("${profile_path}")
done

if (( ${#profiles[@]} == 0 )); then
  echo "No coverage profiles were produced (no test binaries built)." >&2
  exit 1
fi

# Merge per-package cover profiles into a single coverprofile file.
#
# coverprofile format:
#   mode: <mode>
#   <file>:<blocks> <numstmt> <count>
#
# All profiles must use the same mode; we enforce it via -covermode above.
echo "mode: ${covermode}" > "${merged_profile}"
for p in "${profiles[@]}"; do
  # Skip header line. (Some packages may generate an empty profile in edge cases.)
  if [[ -f "${p}" ]]; then
    tail -n +2 "${p}" >> "${merged_profile}" || true
  fi
done

total_line="$(go tool cover -func="${merged_profile}" | tail -n 1)"
echo "${total_line}"

go tool cover -html="${merged_profile}" -o "${html_out}"
echo "coverage profile: ${merged_profile}"
echo "coverage report:  ${html_out}"

if [[ -n "${threshold}" ]]; then
  percent="$(echo "${total_line}" | awk '{print $NF}' | tr -d '%')"
  if ! awk -v p="${percent}" -v t="${threshold}" 'BEGIN { exit (p+0 < t+0) }'; then
    echo "coverage ${percent}% is below threshold ${threshold}%" >&2
    exit 1
  fi
fi
