# PicoClaw

PicoClaw is a lightweight personal AI assistant written in Go.

This repository contains the core CLI, gateway service, tool system, and channel integrations.

If you want full channel/provider details, use:
- English docs: `docs/README.md`
- Chinese docs: `docs/README.zh.md`

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

For complete tool/channel/provider options, see:
- `docs/tools_configuration.md`
- `docs/channels/*`
- `docs/migration/model-list-migration.md`

## Troubleshooting

Use:
- `docs/troubleshooting.md`
- `docker compose ... logs -f picoclaw-gateway`

## License

MIT
