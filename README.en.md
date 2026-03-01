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

Security notes:
- If `gateway.api_key` is empty, only loopback requests are allowed (e.g. `127.0.0.1`)
- If `gateway.api_key` is set, include `Authorization: Bearer <api_key>` (otherwise you'll get 401)

Public exposure (remote / cross-machine notifications):
- Never expose `/api/notify` to the public internet with an empty `gateway.api_key`
- Prefer HTTPS reverse proxy or private networking (e.g. Tailscale) for remote access
- If you must bind it publicly: set `gateway.host` to `0.0.0.0` and configure a strong random `gateway.api_key`

For external agents (Claude Code / Codex) to notify PicoClaw, see: `extensions/picoclaw-notify/SKILL.md` (calls `/api/notify`).

## Docker Compose

This repo ships `docker/docker-compose.yml` with profiles:
- `gateway` for long-running service
- `agent` for one-shot/manual CLI runs

Use your local config at `config/config.json` (mounted read-only into the container).

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
