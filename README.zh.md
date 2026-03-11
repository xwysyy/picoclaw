# X-Claw

<p align="center">
  <img src="assets/logo.svg" alt="X-Claw logo" width="120" />
</p>

[English](README.md)

X-Claw 是一个使用 Go 编写的轻量级个人 AI 助手 Gateway 服务，支持多渠道接入、工具调用、语义记忆和定时任务。

## 特性亮点

- **Gateway-first 架构**：多会话并发、按 session 串行的 inbound 队列
- **多渠道接入**：飞书、Telegram、Discord、Slack、企业微信、LINE、DingTalk 等
- **工具调用**：文件读写、命令执行（含 Docker Sandbox）、Web 搜索、MCP Bridge
- **语义记忆**：本地 hashed 或远程 OpenAI-compatible embeddings
- **定时任务**：Cron 可运营状态、NO_UPDATE 静默约定
- **安全护栏**：Tool Policy（allow/deny、脱敏、幂等重放）、Plan Mode
- **可观测性**：Tool Trace + Run Trace、Gateway Console (Web UI)
- **配置热更新**：SIGHUP / 文件轮询，无需重启容器

## 环境要求

- Linux 主机推荐（x86_64 / ARM64 / RISC-V）
- 源码构建需要 Go 工具链
- 至少一个可用模型 API Key

## 快速开始

```bash
git clone https://github.com/xwysyy/X-Claw.git x-claw
cd x-claw
make deps
make build
```

编辑配置：

```bash
vim ~/.x-claw/config.json
```

最小配置示例：

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

本地调试：

```bash
./build/x-claw agent -m "hello"
./build/x-claw agent
```

> 更完整的入门流程请参阅 [快速入门指南](docs/getting-started.md)。

## Gateway 模式

```bash
./build/x-claw gateway
curl -sS http://127.0.0.1:18790/health
```

当前推荐把 Gateway 作为主入口，Feishu / Telegram 由 Gateway 统一接入与分发。详细功能说明见 [Gateway 功能详解](docs/gateway.md)。

## Docker 快速启动

```bash
docker compose -p x-claw -f docker/docker-compose.yml --profile gateway up -d --build
curl -sS http://127.0.0.1:18790/health
```

详细部署说明见 [部署指南](docs/deployment.md)。

## 文档导航

| 文档 | 说明 |
|------|------|
| [快速入门指南](docs/getting-started.md) | 从零开始搭建和运行 |
| [Gateway 功能详解](docs/gateway.md) | Console、通知、热更新、inbound 队列等 |
| [工具系统](docs/tools.md) | 工具配置、Trace、Policy、MCP Bridge |
| [记忆系统](docs/memory.md) | 语义记忆、Embeddings 配置 |
| [定时任务](docs/cron.md) | Cron 状态、NO_UPDATE 约定 |
| [配置参考](docs/configuration.md) | config.json 完整字段说明 |
| [部署指南](docs/deployment.md) | Docker Compose、安全部署 |
| [运维操作](docs/operations.md) | Resume、Export、Console API |
| [故障排查](docs/troubleshooting.md) | 常见问题与解决方案 |
| [渠道系统](docs/channels/) | 各渠道接入文档 |
| [API Reference](docs/api-reference.md) | HTTP 端点列表 |
| [Architecture](docs/architecture.md) | 架构设计与约束 |
| [UNIT_TESTING.md](UNIT_TESTING.md) | 单元测试与 TDD 工作流 |
| [SECURITY.md](SECURITY.md) | 安全策略 |

## 常用命令

| 命令 | 说明 |
|------|------|
| `x-claw agent` | 交互式对话 |
| `x-claw agent -m "..."` | 单轮对话 |
| `x-claw gateway` | 启动网关服务 |
| `x-claw version` | 查看版本信息 |

## 贡献

欢迎贡献！请阅读 [CONTRIBUTING.md](CONTRIBUTING.md) 了解开发流程和规范。

## 许可证

MIT
