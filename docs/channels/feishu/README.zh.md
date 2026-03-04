# 飞书（Feishu）通道

> 目标：把“能跑起来 + 少踩坑”的关键点写清楚。

## 配置示例

```json
{
  "channels": {
    "feishu": {
      "enabled": true,
      "app_id": "YOUR_APP_ID",
      "app_secret": "YOUR_APP_SECRET",
      "verification_token": "YOUR_VERIFICATION_TOKEN",
      "encrypt_key": "YOUR_ENCRYPT_KEY",
      "allow_from": [],
      "group_trigger": {
        "mention_only": true,
        "command_bypass": true,
        "prefixes": []
      },
      "placeholder": {
        "enabled": true,
        "text": "Thinking... 💭",
        "delay_ms": 2500
      }
    }
  }
}
```

说明：
- `allow_from` 为空数组表示允许所有用户（建议生产环境按需收紧）。
- `group_trigger` 默认更保守：群聊优先要求 `@机器人`（或命令 bypass / 前缀触发）。
- `placeholder.delay_ms` 用来避免“很快就回复 → 先发占位符又立刻被编辑”的闪烁。

## 常见坑：图片/文件下载失败（“资源共享给机器人”/权限不足）

### 症状

- 用户发了图片/文件，但 Agent 看不到附件内容（无法做图像理解/文件解析）。
- 日志里可能出现类似：
  - `Resource download api error`（带 code/msg）
  - 或 “Failed to download resource”

### 原因（高频）

飞书对消息资源（图片/文件等）的下载存在权限/可见性限制：
- 机器人需要具备相应的 API 权限；
- 某些场景下资源需要对机器人“可见”（可理解为需要被共享给机器人/具备访问权限），否则下载接口会失败。

### PicoClaw 的行为

当飞书消息类型是图片/文件/音频/视频，并且下载不到资源时：
- PicoClaw 会在入站文本里追加一个轻量提示：
  - `[media: unavailable - 请确认图片/文件已共享给机器人且机器人具备下载权限]`
- 这样 Agent 至少知道“用户确实发了附件，只是拿不到”，可以主动引导用户排障。

### 排障建议（Checklist）

1) 确认机器人已被加入对应群聊/会话（至少能收到消息）。  
2) 检查飞书应用权限：是否开通了消息/资源下载相关权限。  
3) 若仍失败：尝试让用户把资源“共享给机器人/可访问”，或改用可公开访问的链接（例如网盘链接、可直接下载的 URL）。

> 提示：具体权限名称/控制台入口可能随飞书版本变化，优先以你当前飞书开发者后台为准。

