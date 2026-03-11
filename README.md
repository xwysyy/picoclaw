# X-Claw

<p align="center">
  <img src="assets/logo.svg" alt="X-Claw logo" width="120" />
</p>

[中文](README.zh.md)

X-Claw is a lightweight personal AI assistant Gateway service written in Go, supporting multi-channel integration, tool calling, semantic memory, and scheduled tasks.

## Highlights

- **Gateway-first architecture**: multi-session concurrency with per-session serial inbound queue
- **Multi-channel integration**: Feishu, Telegram, Discord, Slack, WeCom, LINE, DingTalk, etc.
- **Tool calling**: file I/O, command execution (with Docker Sandbox), web search, MCP Bridge
- **Semantic memory**: local hashed or remote OpenAI-compatible embeddings
- **Scheduled tasks**: operable cron state, NO_UPDATE silent convention
- **Safety guardrails**: Tool Policy (allow/deny, redaction, idempotent replay), Plan Mode
- **Observability**: Tool Trace + Run Trace, Gateway Console (Web UI)
- **Hot reload**: SIGHUP / file polling, no container restart needed

## Requirements

- Linux host recommended (x86_64 / ARM64 / RISC-V)
- Go toolchain for source builds
- At least one model provider API key

## Quick Start

```bash
git clone https://github.com/xwysyy/X-Claw.git x-claw
cd x-claw
make deps
make build
```

Edit config:

```bash
vim ~/.x-claw/config.json
```

Minimal config example:

```json
{
  "agents": {
    "defaults": {
      "workspace": "~/.x-claw/workspace",
      "model_name": "gpt-5.2-medium",
      "max_tokens": 8192,
      "max_tool_iterations": 20
    }
  },
  "model_list": [
    {
      "model_name": "gpt-5.2-medium",
      "model": "openai/gpt-5.2-medium",
      "api_key": "YOUR_API_KEY",
      "api_base": "https://api.openai.com/v1"
    }
  ]
}
```

Local debugging:

```bash
./build/x-claw agent -m "hello"
./build/x-claw agent
```

> For a more complete onboarding guide, see [Getting Started](docs/getting-started.md).

## Gateway Mode

```bash
./build/x-claw gateway
curl -sS http://127.0.0.1:18790/health
```

The recommended entry point is Gateway mode, which handles Feishu / Telegram integration and dispatch centrally. See [Gateway Guide](docs/gateway.md) for details.

## Docker Quick Start

```bash
docker compose -p x-claw -f docker/docker-compose.yml --profile gateway up -d --build
curl -sS http://127.0.0.1:18790/health
```

See [Deployment Guide](docs/deployment.md) for detailed instructions.

## Documentation

| Document | Description |
|----------|-------------|
| [Getting Started](docs/getting-started.md) | Build and run from scratch |
| [Gateway Guide](docs/gateway.md) | Console, notifications, hot reload, inbound queue |
| [Tool System](docs/tools.md) | Tool config, Trace, Policy, MCP Bridge |
| [Memory System](docs/memory.md) | Semantic memory, embeddings config |
| [Scheduled Tasks](docs/cron.md) | Cron state, NO_UPDATE convention |
| [Configuration Reference](docs/configuration.md) | Full config.json field reference |
| [Deployment Guide](docs/deployment.md) | Docker Compose, secure deployment |
| [Operations](docs/operations.md) | Resume, Export, Console API |
| [Troubleshooting](docs/troubleshooting.md) | Common issues and solutions |
| [Channel System](docs/channels/) | Per-channel integration docs |
| [API Reference](docs/api-reference.md) | HTTP endpoint listing |
| [Architecture](docs/architecture.md) | Design and constraints |
| [UNIT_TESTING.md](UNIT_TESTING.md) | Unit testing and TDD workflow |
| [SECURITY.md](SECURITY.md) | Security policy |

## Common Commands

| Command | Description |
|---------|-------------|
| `x-claw agent` | Interactive chat |
| `x-claw agent -m "..."` | One-shot chat |
| `x-claw gateway` | Start gateway service |
| `x-claw version` | Show version info |

## Contributing

Contributions are welcome! Please read [CONTRIBUTING.md](CONTRIBUTING.md) for the development workflow and guidelines.

## License

MIT
