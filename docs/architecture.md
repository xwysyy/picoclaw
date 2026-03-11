# Architecture

## Overview

X-Claw is structured as a layered application with clear separation between core logic, infrastructure adapters, and application wiring.

## Component Relationships

```
cmd/x-claw/          -> CLI entry points (gateway, agent, version)
  └─ internal/gateway/  -> Gateway-specific wiring (HTTP API registration)
pkg/agent/            -> Agent loop orchestrator (depends on core ports only)
pkg/channels/         -> Channel adapters (Feishu, Telegram, etc.)
pkg/config/           -> Configuration loading and validation
pkg/httpapi/          -> HTTP API handlers (Console, Notify, Resume)
pkg/providers/        -> LLM provider implementations
pkg/session/          -> Session management and persistence
pkg/tools/            -> Tool executor and registry
pkg/health/           -> Health/readiness server
internal/core/        -> Zero-dependency core interfaces and types
  ├─ ports/           -> Port interfaces (ChannelDirectory, MediaResolver, SessionStore, EventSink)
  ├─ events/          -> Canonical event taxonomy
  ├─ routing/         -> Session key construction and normalization
  ├─ provider/        -> Provider protocol types
  └─ session/         -> Session domain types
```

## Package Dependency Rules

- `internal/core/` has zero external dependencies -- only stdlib
- `pkg/agent/` depends on `internal/core/ports/` for infra access, never on `pkg/channels/`, `pkg/httpapi/`, or `pkg/media/` directly
- `pkg/channels/` implements `internal/core/ports/ChannelDirectory`
- `pkg/session/` implements `internal/core/ports/SessionStore`
- `cmd/x-claw/` is the only place that wires all packages together

## Data Flow

1. **Inbound**: Channel receives message -> Gateway inbound queue -> Agent loop processes
2. **Outbound**: Agent produces response -> Channel sends to user
3. **Tools**: Agent calls tool -> Tool executor (policy check -> trace -> execute -> error template)
4. **Persistence**: Sessions, traces, cron state -> workspace filesystem

---

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
