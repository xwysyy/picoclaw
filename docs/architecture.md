# Architecture Guardrails (WIP)

This repo is actively being refactored towards a "ports/adapters + layered" shape to avoid
accidental coupling and long-term "big-ball-of-mud" drift.

## Layers (Target)

- **Core**: durable concepts and interfaces (ports), minimal types, event taxonomy.
  - Goal: pure logic, easy to unit-test.
  - Rule: must not depend on infra implementations (channels/http/media/etc).
- **Extended**: optional features built on core (memory/skills/cron-ish features).
  - Rule: should depend on core, not infra.
- **Infra**: adapters and implementations (channels/providers/tools I/O, persistence, HTTP).
  - Rule: implements core ports.
- **App**: wiring/bootstrapping (CLI, gateway process, dependency injection).

## Current Guardrails (Enforced)

- `internal/archcheck/archcheck_test.go` enforces:
  - `pkg/agent` must not import `pkg/channels`, `pkg/httpapi`, or `pkg/media`.
  - Rationale: agent loop should only depend on *ports* (interfaces) for infra access.

## Ports Introduced So Far

- `internal/core/ports/channel_directory.go`
  - `ChannelDirectory` is the minimal surface agent core needs for channel lookup.
  - `pkg/channels.Manager` implements it as an adapter shim.
- `internal/core/ports/media.go`
  - `MediaResolver` resolves `media://...` refs to local paths + metadata.
  - `pkg/media.AsMediaResolver(store)` adapts `media.MediaStore` to this port.
- `internal/core/ports/session_store.go`
  - `SessionStore` is the durable conversation state surface (history/summary/active agent/model override/tree).
  - `pkg/session.SessionManager` implements it.
- `internal/core/ports/event_sink.go`
  - `EventSink` is a generic event consumer interface (JSONL trace, SSE/ws, channel placeholder, etc).

## Core Algorithms Moved So Far

- `internal/core/routing/*`
  - Agent ID/account ID normalization and session key construction now live in core.
  - `pkg/routing` remains as a thin facade to keep existing imports stable during migration.

## Core Domain Types Moved So Far

- `internal/core/provider/*`
  - canonical provider protocol types live at `internal/core/provider/protocoltypes`
  - `pkg/providers/protocoltypes` is a thin facade to preserve import paths
- `internal/core/session/*`
  - canonical session domain types live at `internal/core/session`
  - `pkg/session` re-exports these via type aliases to preserve import paths

## Canonical Event Taxonomy

- `internal/core/events/types.go` defines stable string constants for trace/event types.
  - `pkg/agent` run traces and `pkg/tools` tool traces now use these constants to avoid drift.

## Practical Rules While Refactoring

- When core needs something from infra, add/extend a **port** in `internal/core/ports`,
  then implement it via a small adapter in the infra package.
- Prefer "facade" migration:
  - keep old packages compiling,
  - move canonical interfaces/types into `internal/core`,
  - then gradually rewire call sites.

## Testing

- Minimal stable test suite for constrained environments:
  - `./scripts/test.sh`
- Batch-oriented verification for environments where `go test ./...` may hit `SIGKILL(137)`:
  - `./scripts/test-batches.sh`
- Full CLI compile/tests:
  - `go test ./cmd/x-claw/...`
