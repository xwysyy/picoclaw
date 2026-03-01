# 飞书

飞书（国际版名称：Lark）是字节跳动旗下的企业协作平台。

PicoClaw 的飞书频道使用 **WebSocket 长连接**接收消息（无需公网 IP / Webhook），并通过飞书开放平台 OpenAPI 发送消息与收发媒体。

## 配置

```json
{
  "channels": {
    "feishu": {
      "enabled": true,
      "app_id": "cli_xxx",
      "app_secret": "xxx",
      "encrypt_key": "",
      "verification_token": "",
      "bot_id": "",
      "typing": {
        "enabled": false
      },
      "placeholder": {
        "enabled": false,
        "text": "正在思考..."
      },
      "group_trigger": {
        "mention_only": false,
        "prefixes": []
      },
      "allow_from": []
    }
  }
}
```

| 字段               | 类型   | 必填 | 描述 |
| ------------------ | ------ | ---- | ---- |
| enabled            | bool   | 是   | 是否启用飞书频道 |
| app_id             | string | 是   | 飞书应用的 App ID（以 `cli_` 开头） |
| app_secret         | string | 是   | 飞书应用的 App Secret |
| encrypt_key        | string | 否   | 事件回调加密密钥（WebSocket 模式可不填） |
| verification_token | string | 否   | 事件回调验签 Token（WebSocket 模式可不填） |
| bot_id             | string | 否   | 机器人自身 ID（`open_id` / `user_id` / `union_id` 之一），用于群聊 `group_trigger.mention_only` 精准判断是否 @ 到机器人；不填会退化为“尽力而为”的判断 |
| typing             | object | 否   | 打字态配置（当前飞书通道预留配置位） |
| placeholder        | object | 否   | 占位消息配置：开启后会先发送占位文本，再由最终回复覆盖（依赖消息编辑能力） |
| group_trigger      | object | 否   | 群聊触发规则（详见下方说明） |
| allow_from         | array  | 否   | 允许的用户白名单，空表示允许所有用户 |

### group_trigger（群聊触发）

- `mention_only=true`：仅当群聊中 @ 机器人时才响应（建议同时配置 `bot_id`）。
- `prefixes=["!", "/bot "]`：仅当消息以这些前缀开头时才响应（会自动剥离前缀）。
- 优先级：**@机器人 > mention_only > prefixes > 默认全响应**（逻辑在 `pkg/channels/base.go`）。

## 设置流程

1. 前往飞书开放平台创建「企业自建应用」，获取 **App ID / App Secret**
2. 在「能力」中启用 **机器人**
3. 在「权限管理」中为应用开通权限（见下方“权限建议”）
4. 在「事件与回调」中选择订阅方式为 **长连接（WebSocket）**，订阅 **接收消息 v2.0**
5. 在「应用发布」中创建版本并发布（否则权限/事件可能不生效）
6. 将 App ID / App Secret 填入 PicoClaw 的 `config.json` 并启动

> 注意：飞书开放平台对权限与事件的生效往往要求“发布版本”。如果你在开放平台界面已经勾选了权限但仍然报缺权限，优先检查是否已发布版本。

## 权限建议（Scopes）

飞书权限较多且有“敏感权限”。以下为常用建议（不同企业管理员策略不同，可能需要额外审批）：

- **基础收发消息**：`im:message`（收发消息相关）
- **收群消息（敏感）**：`im:message.group_msg`（若未开通，机器人在群聊里可能只能收到 @ 机器人的消息）
- **读单聊消息（只读）**：`im:message.p2p_msg:readonly`
- **收发/下载媒体资源**：`im:resource`（用于图片/文件上传下载，PicoClaw 的飞书媒体收发依赖它）
- （可选）**读用户昵称**：`contact:user.base:readonly`（仅当你需要在日志中显示更友好的用户名时才需要）

## 备注

- PicoClaw 飞书频道依赖飞书 Go SDK，目前 **仅支持 64 位架构**（amd64/arm64 等）；32 位系统会提示不支持。
