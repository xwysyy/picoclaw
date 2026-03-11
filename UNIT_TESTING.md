# Unit Testing & TDD Guide

To prevent regressions and improve maintainability, we treat high-quality unit tests as first-class citizens:

- Cover critical paths and boundary conditions
- Test failure paths thoroughly (error returns, timeouts, empty/invalid input, resource limits, etc.)
- Keep tests deterministic (repeatable, no external dependencies, predictable runtime)

This document describes X-Claw's testing conventions, recommended commands, TDD workflow, and common test patterns.

## Recommended Commands

### 1. Batch-Oriented Test Runner (Recommended for Constrained Environments)

If your machine hits `SIGKILL(137)` during `go test ./...` or `-race`, prefer:

```bash
./scripts/test-batches.sh
```

This script will:
- Run `go build ./...` and `go vet ./...` first
- Perform a compile-only pass via `go test ./... -run '^$'`
- Execute `go test` per package in separate processes
- Split `pkg/agent` into per-test batches to reduce peak memory

Common usage:

```bash
./scripts/test-batches.sh
./scripts/test-batches.sh --dry-run
./scripts/test-batches.sh --race-safe
X_CLAW_TEST_PKGS='github.com/xwysyy/X-Claw/pkg/config github.com/xwysyy/X-Claw/pkg/httpapi' ./scripts/test-batches.sh --skip-build --skip-vet
```

### 2. Full Unit Tests (Recommended)

Some environments (low-memory VMs / SBCs / CI) may get OOM-killed during `go test ./...`.
For stability, this repo provides a per-package sequential runner:

```bash
make test
```

Equivalent to:

```bash
./scripts/test-unit.sh
```

The script runs two steps per package:

- `go test -c` compiles the test binary (reduces peak memory vs compile+run simultaneously)
- Runs the test binary directly (less likely to be OOM-killed)

You can append standard `go test` flags (applied to each package; the script converts some flags to `-test.*` format):

```bash
./scripts/test-unit.sh -race
./scripts/test-unit.sh -run TestSanitizeHistoryForProvider
./scripts/test-unit.sh -v
```

Note: `-race` requires `CGO_ENABLED=1` and a C compiler (gcc/clang).
Race detection significantly increases CPU/memory usage; on low-memory machines, enable it only for critical packages:

```bash
CGO_ENABLED=1 X_CLAW_TEST_PKGS='./pkg/agent ./pkg/tools' ./scripts/test-unit.sh -race
```

To test specific packages only:

```bash
X_CLAW_TEST_PKGS='./pkg/agent ./pkg/tools' ./scripts/test-unit.sh
```

### 3. Fast Parallel (Faster but More Memory-Hungry)

```bash
make test-fast
```

### 4. Single Package / Single Test

```bash
go test ./pkg/agent -count=1
go test ./pkg/agent -run TestSanitizeHistoryForProvider -count=1
```

### 5. Coverage

Repository-wide coverage (recommended, more memory-stable):

```bash
make cover
```

This generates:

- `coverage.out` (coverprofile)
- `coverage.html` (visual report)

Equivalent to:

```bash
./scripts/cover-unit.sh
```

Single-package coverage:

```bash
go test ./pkg/agent -cover -count=1
```

Generate a visual report (single package):

```bash
go test ./pkg/agent -coverprofile=coverage.out -count=1
go tool cover -html=coverage.out -o coverage.html
```

Note: Repository-wide coverage aggregation (`go test ./... -coverprofile=...`) can be heavy on some machines. Prefer evaluating per-package and incrementally adding boundary test cases.

## Dependency Download & Proxy

If you need to download Go dependencies or toolchains in a non-interactive shell:

```bash
source ~/.zshrc && proxy_on
go test ./... -count=1
```

Note: `proxy_on` is defined in `~/.zshrc` and is not auto-loaded in non-interactive environments.

## TDD (Test-Driven Development) Workflow

Follow the classic Red-Green-Refactor cycle:

1. **Red**: Write a failing test that clearly describes expected behavior.
2. **Green**: Write the minimal implementation to make the test pass.
3. **Refactor**: Refactor under test protection (deduplicate, extract functions, improve naming, adjust abstractions).
4. **Add boundaries**: Cover real-world bad inputs (empty strings, nil, timeouts, error codes, permissions, duplicate calls, etc.).

For regression bugs:

- First write a test that reliably reproduces the bug (lock it down)
- Then fix the implementation
- Finally add adjacent boundary cases to prevent similar regressions

## Test Patterns (Project Conventions)

### 1. Table-Driven Tests

Prefer table-driven tests for boundary coverage:

- Empty / missing fields
- Different combinations (missing params, type errors, range errors)
- Key branches (success / failure / timeout)

### 2. No Real External Dependencies

Unit tests should not depend on:

- Real network (use `httptest.NewServer`)
- Real API keys
- Long real-time waits (use short timeouts + context)

For HTTP logic: use `httptest` to assert request shape and return fake responses.

### 3. Filesystem & Temp Directories

Use `t.TempDir()` to avoid polluting the workspace:

- Create temporary workspaces
- Write test files
- Assert outputs

### 4. Context & Timeouts

All potentially blocking logic (channel publish, HTTP, long tasks) should:

- Use `context.WithTimeout` in tests
- Assert completion within a reasonable time
- Treat timeout as failure (prevent hanging tests)

### 5. Mock / Fake Priority

- Prefer fakes when possible (lighter, more deterministic)
- Only mock complex components when necessary

Existing reference implementations:

- `pkg/agent/mock_provider_test.go`: LLM provider mock
- `pkg/agent/loop_test.go`: bus/channel integration test examples

## Quality Checklist (Self-Review for New Tests)

- Test names are clear — failures are immediately understandable
- Boundary conditions are covered (especially error paths)
- No reliance on randomness or global state (fix seeds, use temp dirs when needed)
- No long sleeps (use channels, context, controllable timeouts instead)
- No new external dependencies unless genuinely needed and reliably cached
