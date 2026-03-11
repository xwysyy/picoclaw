# Contributing to X-Claw

Thank you for your interest in contributing to X-Claw! This document provides
guidelines and instructions for contributing.

## Development Setup

### Prerequisites

- **Go 1.25+** — download from [go.dev](https://go.dev/dl/)
- **GNU Make** — for running project commands

### Getting Started

1. Fork and clone the repository:
   ```bash
   git clone https://github.com/<your-username>/X-Claw.git
   cd X-Claw
   ```
2. Install dependencies and build:
   ```bash
   make deps
   make build
   ```
3. Verify everything works: `make test`

## Code Style

We use [golangci-lint](https://golangci-lint.run/) to enforce consistent style.
Run `make lint` before submitting a PR.

- Follow standard Go conventions and [Effective Go](https://go.dev/doc/effective_go).
- Keep functions focused and reasonably sized.
- Add comments for exported types, functions, and non-obvious logic.

## Testing

All changes should include appropriate tests. See
[UNIT_TESTING.md](./UNIT_TESTING.md) for detailed testing guidelines.

```bash
make test                    # full test suite
./scripts/test-batches.sh   # batch testing for large suites
```

- Write unit tests for new functionality.
- Use table-driven tests where appropriate.
- Mock external dependencies using interfaces from `internal/core/ports/`.

## Commit Convention

Format: `type: short description` (subject line under 72 characters).

| Type       | Description                                              |
| ---------- | -------------------------------------------------------- |
| `feat`     | A new feature                                            |
| `fix`      | A bug fix                                                |
| `refactor` | Code change that neither fixes a bug nor adds a feature  |
| `docs`     | Documentation only changes                               |
| `test`     | Adding or updating tests                                 |
| `chore`    | Maintenance tasks, dependency updates, CI, etc.          |

Examples:

```
feat: add Telegram channel support
fix: prevent session leak on gateway shutdown
refactor: extract token counting into dedicated module
chore: bump Go version to 1.25.7
```

## Pull Request Process

1. **Fork** the repository and create a feature branch from `main`:
   ```bash
   git checkout -b feat/my-feature main
   ```
2. **Make your changes** in small, focused commits.
3. **Push** your branch and **open a Pull Request** against `main`.
4. Fill out the PR template with a summary, change list, and test plan.
5. Wait for CI checks to pass and address review feedback.

Tips for a smooth review:

- Keep PRs small and focused — one logical change per PR.
- Link related issues (e.g., `Fixes #42`).
- Rebase on `main` if your branch falls behind.

## Architecture Constraints

X-Claw follows a **ports and adapters** (hexagonal) architecture. See
[docs/architecture.md](./docs/architecture.md) for details.

- **`internal/core/ports/`** — zero-dependency interface definitions; must not
  import any other internal package.
- **`pkg/agent`** must not import `pkg/channels` (and vice versa).
  Communication goes through port interfaces.
- **No circular dependencies.** Architecture guard tests in
  `internal/archcheck/` enforce this automatically.
- New integrations: define the interface in `ports/` first, then implement the
  adapter in the appropriate package.

## Reporting Issues

Use our [GitHub Issue templates](https://github.com/xwysyy/X-Claw/issues/new/choose):

- **Bug Report** — for unexpected behavior or errors.
- **Feature Request** — for new features or improvements.

When reporting bugs, include version, run mode, channel, steps to reproduce,
and relevant logs (with secrets redacted).

## License

By contributing to X-Claw, you agree that your contributions will be licensed
under the [MIT License](./LICENSE).
