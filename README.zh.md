# PicoClaw

PicoClaw 是一个使用 Go 编写的轻量级个人 AI 助手。

本仓库包含核心 CLI、Gateway 服务、工具系统和多种渠道集成。

详细文档入口：
- 中文：`docs/README.zh.md`
- English：`docs/README.md`

## 项目范围

当前支持：
- CLI 对话（`agent` 模式）
- 常驻网关服务（`gateway` 模式）
- 基于 `model_list` 的多模型配置
- 工具调用（文件、命令、Web、定时、记忆、技能）
- 会话与工作区持久化

## 项目状态

项目仍在持续迭代中。

- 版本升级时可能存在行为变化
- 建议升级后检查配置兼容性
- 未加防护前，不建议直接暴露到公网

## 环境要求

- 推荐 Linux 主机（x86_64 / ARM64 / RISC-V）
- 源码构建需要 Go 工具链
- 至少一个可用模型 API Key（或兼容的本地/代理端点）

## 快速开始（本地构建）

```bash
git clone https://github.com/sipeed/picoclaw.git
cd picoclaw
make deps
make build
```

初始化工作区与配置：

```bash
./build/picoclaw onboard
```

编辑配置：

```bash
vim ~/.picoclaw/config.json
```

最小配置示例：

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

单轮问答：

```bash
./build/picoclaw agent -m "hello"
```

交互模式：

```bash
./build/picoclaw agent
```

## Gateway 模式

启动 gateway：

```bash
./build/picoclaw gateway
```

健康检查：

```bash
curl -sS http://127.0.0.1:18790/health
```

## Docker Compose

本仓库的 `docker/docker-compose.yml` 提供两个 profile：
- `gateway`：常驻服务
- `agent`：单次/手动执行

容器会挂载本地 `config/config.json`（只读）作为运行配置。

构建并启动 gateway：

```bash
docker compose -p picoclaw -f docker/docker-compose.yml --profile gateway up -d --build
docker compose -p picoclaw -f docker/docker-compose.yml ps
curl -sS http://127.0.0.1:18790/health
```

执行单次 agent：

```bash
docker compose -p picoclaw -f docker/docker-compose.yml run --rm picoclaw-agent -m "hello"
```

停止服务：

```bash
docker compose -p picoclaw -f docker/docker-compose.yml down
```

## 常用命令

- `picoclaw onboard` 初始化工作区与配置
- `picoclaw agent` 交互式对话
- `picoclaw agent -m "..."` 单轮对话
- `picoclaw gateway` 启动网关服务
- `picoclaw status` 查看运行状态
- `picoclaw cron list` 查看定时任务
- `picoclaw cron add ...` 新增定时任务

## 配置说明

- 主配置文件：`~/.picoclaw/config.json`
- 默认工作区：`~/.picoclaw/workspace`
- 配置模板：`config/config.example.json`

完整配置文档见：
- `docs/tools_configuration.md`
- `docs/channels/*`
- `docs/migration/model-list-migration.md`

## 排错

参考：
- `docs/troubleshooting.md`
- `docker compose ... logs -f picoclaw-gateway`

## 许可证

MIT
