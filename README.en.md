# PicoClaw

<p align="center">
  <img src="assets/logo.svg" alt="PicoClaw logo" width="120" />
</p>

[中文](README.md) | [Roadmap](ROADMAP.md)

PicoClaw is a lightweight personal AI assistant written in Go.

This repository contains the core CLI, gateway service, tool system, and channel integrations.

## Scope

PicoClaw supports:
- CLI chat (`agent` mode)
- Long-running gateway service (`gateway` mode)
- Multi-model configuration via `model_list`
- Tool calling (filesystem, exec, web, cron, memory, skills)
- Session and workspace persistence

## Project Status

This project is under active development.

- Expect behavior changes between versions.
- Review config changes when upgrading.
- Avoid exposing it directly to the public internet without your own security controls.

## Requirements

- Linux host recommended (x86_64 / ARM64 / RISC-V)
- Go toolchain for source builds
- At least one model provider API key (or a local compatible endpoint)

## Quick Start (Local Build)

```bash
git clone https://github.com/sipeed/picoclaw.git
cd picoclaw
make deps
make build
```

Initialize workspace/config:

```bash
./build/picoclaw onboard
```

Edit config:

```bash
vim ~/.picoclaw/config.json
```

Note: PicoClaw runtime configuration is **file-only** (`config.json`, default: `~/.picoclaw/config.json`; Docker deployments typically mount `config/config.json` to that path). Environment-variable overrides for config fields are intentionally not supported to keep behavior reproducible.

Minimal example:

```json
{
  "agents": {
    "defaults": {
      "workspace": "~/.picoclaw/workspace",
      "model": "gpt-5.2",
      "max_tokens": 8192,
      "max_tool_iterations": 20
    }
  },
  "model_list": [
    {
      "model_name": "gpt-5.2",
      "model": "openai/gpt-5.2",
      "api_key": "YOUR_API_KEY",
      "api_base": "https://api.openai.com/v1"
    }
  ]
}
```

Run one-shot chat:

```bash
./build/picoclaw agent -m "hello"
```

Run interactive chat:

```bash
./build/picoclaw agent
```

## Gateway Mode

Start gateway:

```bash
./build/picoclaw gateway
```

Health endpoint:

```bash
curl -sS http://127.0.0.1:18790/health
```

Check runtime status (includes `last_active` / cron / trace, etc.):

```bash
./build/picoclaw status
./build/picoclaw status --json
```

### Notification API (`/api/notify`)

Gateway also exposes a lightweight notification endpoint so external systems (CI / scripts / daemons) can push a reminder to you via configured channels (e.g. Feishu).

Send to a specific channel + recipient:

```bash
curl -sS -X POST http://127.0.0.1:18790/api/notify \
  -H 'Content-Type: application/json' \
  -d '{"channel":"feishu","to":"oc_xxx","content":"Task completed"}'
```

If you omit `channel/to`, it defaults to the most recent conversation (`last_active`):

```bash
curl -sS -X POST http://127.0.0.1:18790/api/notify \
  -H 'Content-Type: application/json' \
  -d '{"content":"Task completed (last_active)"}'
```

Note: on a fresh gateway start, if there has been no external conversation yet (`last_active` is empty), omitting `channel/to` will return `channel is required`. Send one message to the bot via Feishu/Telegram to establish `last_active`, or specify `channel/to` explicitly.

Security notes:
- If `gateway.api_key` is empty, only loopback requests are allowed (e.g. `127.0.0.1`)
- If `gateway.api_key` is set, include `Authorization: Bearer <api_key>` (otherwise you'll get 401)

Public exposure (remote / cross-machine notifications):
- Never expose `/api/notify` to the public internet with an empty `gateway.api_key`
- Prefer HTTPS reverse proxy or private networking (e.g. Tailscale) for remote access
- If you must bind it publicly: set `gateway.host` to `0.0.0.0` and configure a strong random `gateway.api_key`

For external agents (Claude Code / Codex) to notify PicoClaw, see: `extensions/picoclaw-notify/SKILL.md` (calls `/api/notify`).

### Tool Trace (replayable tool-call logs)

When `tools.trace.enabled=true`, every tool call appends to an on-disk JSONL event stream, and can optionally write per-call files. This makes it easy to debug:

- why the model decided to call a tool
- what arguments were used
- what the tool returned
- duration / error summary

Default trace locations (when `tools.trace.dir` is empty):

- ` <workspace>/.picoclaw/audit/tools/<session>/events.jsonl `
- ` <workspace>/.picoclaw/audit/tools/<session>/calls/*.json|*.md ` (when `tools.trace.write_per_call_files=true`)

Config example:

```json
{
  "tools": {
    "trace": {
      "enabled": true,
      "write_per_call_files": true
    }
  }
}
```

### Run Trace (run-level checkpoint stream)

Tool Trace answers “what happened in each tool call”. Run Trace answers “what happened in this whole user request/run” (LLM turns, tool batches, final output).

When `tools.trace.enabled=true`, PicoClaw also appends a per-session run-level JSONL event stream (append-only), used for:
- debugging / post-mortems
- durable long tasks (the foundation for resume)
- export bundles (`picoclaw export --trace`)

Default location:

- ` <workspace>/.picoclaw/audit/runs/<session>/events.jsonl `

### Tool Policy (`tools.policy`: unified guardrails + audit)

Phase D2 adds a centralized policy layer at the **tool executor chokepoint** (built-in tools and MCP tools are treated the same), covering:
- allow/deny (exact names + prefixes)
- unified timeouts per tool call
- redaction (avoid secrets leaking into traces/ledgers; optionally redact LLM/User outputs)
- side-effect confirmation (two-phase commit)
- idempotency/replay for safe resume

Example (audit redaction + timeout only):

```json
{
  "tools": {
    "policy": {
      "enabled": true,
      "timeout_ms": 60000,
      "redact": {
        "enabled": true,
        "apply_to_llm": false,
        "apply_to_user": false
      }
    }
  }
}
```

Enable “confirmation gate + idempotent replay” (recommended to confirm only on resume flows):

```json
{
  "tools": {
    "policy": {
      "enabled": true,
      "confirm": {
        "enabled": true,
        "mode": "resume_only",
        "tools": ["exec", "write_file", "edit_file", "append_file"]
      },
      "idempotency": {
        "enabled": true,
        "cache_result": true,
        "tools": ["exec", "write_file", "edit_file", "append_file"]
      }
    }
  }
}
```

Notes:
- When blocked by policy, tools return structured JSON (`kind=tool_policy_*`). The model should ask the user, then call `tool_confirm(confirm_key)`.
- The idempotency ledger is stored at: ` <workspace>/.picoclaw/audit/runs/<session>/runs/<run_id>/policy.jsonl `

### Resume last task (`/api/resume_last_task`)

Phase E2 adds a Gateway endpoint that resumes the most recent run that did **not** end normally (and continues on the same `run_id`).

```bash
curl -sS -X POST http://127.0.0.1:18790/api/resume_last_task
```

Auth is the same as `/api/notify`:
- empty `gateway.api_key` → loopback only
- non-empty `gateway.api_key` → `Authorization: Bearer <api_key>` or `X-API-Key: <api_key>`

Strongly recommended: enable `tools.policy.confirm` + `tools.policy.idempotency` to prevent side effects from being repeated on resume.

### Run/Session Export (`picoclaw export`)

When you need to file a bug report, review a conversation, or share a replay bundle, you can export a zip bundle (by default it includes: session snapshot + tool traces + cron/state + redacted config snapshot + manifest).

Common usage:

```bash
# Export the session matching workspace last_active (recommended)
./build/picoclaw export --last-active

# Or export an explicit session key
./build/picoclaw export --session 'agent:main:feishu:group:oc_xxx'
```

Default output directory:

- ` <workspace>/exports/*.zip `

### Unified Tool Error Template (`tools.error_template`)

When a tool execution fails (`is_error=true`), PicoClaw wraps the error into a small structured JSON payload (`kind=tool_error`) with minimal recovery hints (required args, available/similar tool names, etc.). This helps the model self-correct (adjust args / switch tools / read-before-write).

Notes:
- This is implemented at the executor layer (no per-tool changes required)
- It only affects the LLM-facing `ForLLM`. If a tool provides `ForUser`, the human-friendly output is preserved.

Config example (enabled by default):

```json
{
  "tools": {
    "error_template": {
      "enabled": true,
      "include_schema": true
    }
  }
}
```

### Structured Memory Output (JSON hits)

`memory_search` / `memory_get` return structured JSON to the LLM side (with a stable `kind` field for regression tests and reliable quoting), while still keeping a short human-readable summary:

- `memory_search` → `{"kind":"memory_search_result","hits":[...]}`
- `memory_get` → `{"kind":"memory_get_result","found":...,"hit":...}`

This significantly improves second-pass consumption and reduces “model misreads plain text” issues.

### Remote Embeddings for Semantic Memory (Optional)

By default, PicoClaw semantic memory (`agents.defaults.memory_vector`) uses a local deterministic `hashed` embedder: fast, stable, and no extra network/API required.

If you want higher-quality semantic retrieval, you can point PicoClaw to an OpenAI-compatible embeddings endpoint (`POST <api_base>/embeddings`), such as SiliconFlow / OpenAI / other compatible services.

In this project, embeddings settings are read **only from `config.json`**. Example (place under `agents.defaults.memory_vector.embedding`):

```json
{
  "agents": {
    "defaults": {
      "memory_vector": {
        "dimensions": 4096,
        "embedding": {
          "kind": "openai_compat",
          "api_base": "https://api.siliconflow.cn/v1",
          "api_key": "sk-...",
          "model": "Qwen/Qwen3-Embedding-8B",
          "proxy": "",
          "batch_size": 64,
          "request_timeout_seconds": 30
        }
      }
    }
  }
}
```

Notes:
- If `embedding.kind` is empty or `hashed`, PicoClaw uses the local deterministic `hashed` embedder (no network)
- The first semantic search / index rebuild will make network requests; the index is persisted and automatically rebuilt when sources or `api_base/model` changes
- If you explicitly set `embedding.kind` to `openai_compat`, then `api_base` and `model` are required (otherwise it errors)

### Operable Cron State (runHistory / lastStatus)

Cron job state is persisted under your workspace:

- ` <workspace>/cron/jobs.json `

The `state` section records:

- `lastStatus` / `lastRunAtMs` / `lastDurationMs`
- `lastOutputPreview` (truncated preview)
- `runHistory` (latest N runs)

Common CLI commands:

```bash
./build/picoclaw cron list
./build/picoclaw cron show <job_id>
./build/picoclaw cron show <job_id> --json
```

## Docker Compose

This repo ships `docker/docker-compose.yml` with profiles:
- `gateway` for long-running service
- `agent` for one-shot/manual CLI runs

Use your local config at `config/config.json` (mounted read-only into the container).
If the container logs show `permission denied` for `/home/picoclaw/.picoclaw/config.json`, your host `config/config.json` is likely too strict (e.g. `600`). Ensure it is readable by the container user (e.g. `chmod 644 config/config.json`).

Build and run gateway:

```bash
docker compose -p picoclaw -f docker/docker-compose.yml --profile gateway up -d --build
docker compose -p picoclaw -f docker/docker-compose.yml ps
curl -sS http://127.0.0.1:18790/health
```

Run one-shot agent:

```bash
docker compose -p picoclaw -f docker/docker-compose.yml run --rm picoclaw-agent -m "hello"
```

Git is available in the container image for agent-side commits/pushes. Configure PAT and identity in `config/config.json` under `tools.git`:

```json
{
  "tools": {
    "git": {
      "enabled": true,
      "username": "your-github-username",
      "pat": "github_pat_xxx",
      "user_name": "Your Name",
      "user_email": "you@example.com",
      "host": "github.com",
      "protocol": "https"
    }
  }
}
```

At container startup, this is written to `~/.git-credentials` and `~/.gitconfig` automatically.

Stop gateway:

```bash
docker compose -p picoclaw -f docker/docker-compose.yml down
```

## Common Commands

- `picoclaw onboard` initialize workspace and default config
- `picoclaw agent` interactive chat
- `picoclaw agent -m "..."` one-shot chat
- `picoclaw gateway` run channel gateway
- `picoclaw status` show runtime status
- `picoclaw cron list` list scheduled jobs
- `picoclaw cron add ...` add scheduled job

## Configuration Notes

- Main config file: `~/.picoclaw/config.json`
- Default workspace: `~/.picoclaw/workspace`
- Example config template: `config/config.example.json`

For advanced options, inspect in-code config structs under `pkg/config`.

## Unit Testing

See: [UNIT_TESTING.md](UNIT_TESTING.md) (Chinese doc, includes the recommended TDD workflow and project-specific test commands).

## Troubleshooting

Use:
- `docker compose ... logs -f picoclaw-gateway`

## License

MIT
