# 快速入门指南

本文档帮助你从零开始运行 X-Claw。

## 前提条件

- **Go 1.25+** 工具链（用于源码构建）
- **Linux** 推荐（x86_64 / ARM64 / RISC-V），macOS 也可用
- 至少一个可用的 **模型 API Key**（OpenAI、Anthropic、DeepSeek、智谱等，或兼容的本地/代理端点）

## 从源码构建

```bash
git clone https://github.com/xwysyy/X-Claw.git x-claw
cd x-claw
make deps
make build
```

构建产物位于 `./build/x-claw`。

## 最小配置

X-Claw 的配置文件路径为 `~/.x-claw/config.json`。如果该文件不存在，X-Claw 会使用内置默认值，但你至少需要配置一个模型才能实际使用。

创建配置文件：

```bash
mkdir -p ~/.x-claw
vim ~/.x-claw/config.json
```

填入最小配置：

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

关键说明：
- `agents.defaults.model_name` 必须与 `model_list` 中某一项的 `model_name` 匹配
- `model` 字段的格式为 `provider/model`，X-Claw 据此选择对应的 provider 实现
- `api_key` 填你的真实密钥；`api_base` 可按需修改为代理或兼容端点

你也可以从 `config/config.example.json` 复制一份完整模板再修改：

```bash
cp config/config.example.json ~/.x-claw/config.json
```

## 第一次运行：Agent 模式

Agent 模式是本地调试入口，不启动 Gateway 服务。

单轮对话（直接传入消息）：

```bash
./build/x-claw agent -m "hello, what can you do?"
```

交互式对话：

```bash
./build/x-claw agent
```

如果一切正常，你将看到模型的回复。如果报错，请检查：
- `model_name` 是否与 `model_list` 匹配
- `api_key` 是否有效
- 网络是否可达（必要时配置代理）

## 启动 Gateway

Gateway 是 X-Claw 的常驻服务模式，支持多渠道接入和 Console Web UI。

```bash
./build/x-claw gateway
```

验证健康状态：

```bash
curl -sS http://127.0.0.1:18790/health
# 预期返回: {"status":"ok"}
```

访问 Console：

```
http://127.0.0.1:18790/console/
```

## 连接第一个渠道

以 Telegram 为例，在配置中添加：

```json
{
  "channels": {
    "telegram": {
      "enabled": true,
      "token": "YOUR_TELEGRAM_BOT_TOKEN",
      "allow_from": ["YOUR_TELEGRAM_USER_ID"]
    }
  }
}
```

获取 Bot Token：通过 Telegram 的 [@BotFather](https://t.me/BotFather) 创建机器人。
获取 User ID：向 [@userinfobot](https://t.me/userinfobot) 发消息。

然后重启 Gateway（或利用热更新：`kill -HUP <pid>`），向你的 Bot 发送消息即可。

更多渠道配置请参阅 `docs/channels/` 目录：
- [Feishu](channels/feishu/)
- [Telegram](channels/telegram/)
- [WeCom](channels/wecom/)

## 下一步

- [配置参考](configuration.md) -- 所有配置块的详细说明
- [工具系统](tools.md) -- Web 搜索、命令执行、MCP Bridge、Tool Policy 等
- [API 参考](api-reference.md) -- Gateway HTTP 端点一览
- [架构说明](architecture.md) -- 项目结构与包依赖规则
