# 渠道系统

X-Claw 通过渠道适配器接入各即时通讯平台，实现统一的消息收发与会话管理。Gateway 作为统一入口，负责将不同渠道的消息路由到 Agent 进行处理。

---

## 已支持渠道

| 渠道 | 文档 | 说明 |
|------|------|------|
| 飞书 (Feishu) | [配置文档](feishu/README.zh.md) | 主部署渠道 |
| Telegram | [配置文档](telegram/README.zh.md) | 保留支持 |
| 企业微信 Bot | 见 config.example.json | 群聊 Webhook |
| 企业微信自建应用 | 见 config.example.json | 私聊，需更多配置 |
| 企业微信智能机器人 | [配置文档](wecom/wecom_aibot/README.zh.md) | 流式响应 |
| Discord | 见 config.example.json | 基础支持 |
| Slack | 见 config.example.json | 基础支持 |
| LINE | 见 config.example.json | 基础支持 |
| DingTalk | 见 config.example.json | 基础支持 |
| QQ | 见 config.example.json | 基础支持 |
| WhatsApp | 见 config.example.json | Bridge 模式 |
| OneBot | 见 config.example.json | WebSocket |

---

## 通用配置概述

所有渠道共享以下通用配置项：

### allow_from（允许列表）

控制哪些用户可以与机器人交互：

- `allow_from=[]`：允许所有人
- 非空时只允许名单中的 sender（支持 `"platform:id"`、纯数字 id、`"@username"`、`"id|username"` 等格式）

### group_trigger（群聊触发规则）

控制群聊中何时触发机器人回复，避免群消息过于嘈杂：

- `mention_only`：必须 `@机器人` 才回复
- `mentionless`：不需要 `@` 也回复
- `command_bypass`：命令可绕过 `@` 限制
- `prefixes`：指定触发前缀

详细说明见 [Gateway 功能详解](../gateway.md#群聊触发规则grouptrigger)。
