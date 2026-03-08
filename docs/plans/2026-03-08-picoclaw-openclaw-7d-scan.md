# 2026-03-08 Picoclaw / OpenClaw 近 7 天更新扫描

## 1. 范围与方法

- 时间窗口：`2026-03-01` 至 `2026-03-08`。
- 观察对象：
  - `sipeed/picoclaw`
  - `openclaw/openclaw`
- 数据来源：本地浅拉取后的仓库历史，而不是仅看 GitHub 页面样本。
- 全量步骤：
  - 对近 7 天窗口内**全部提交**导出 `sha / 时间 / subject / 触达文件`。
  - 对全部提交做：类型统计、scope 统计、一级/二级热点目录统计、主题关键词聚类。
  - 再对高频主题抽代表性提交，补看 `git show --stat` 和关键 diff。
- 说明：
  - 这是一版“**全量提交元数据 + 高频主题代表 diff 深读**”的工程扫描。
  - 它仍然不是逐条人工阅读 `2346` 个完整 diff，但已经不是前一版那种样本式浏览。
  - 个别提交作者时间带有时区差异，所以 `git log --since=2026-03-01` 返回窗口里会看到少量显示为 `2026-02-28` 的 author date；本文统一把它视为落在同一 7 天窗口里的边缘提交。

## 2. 活跃度概览

| Repo | 近 7 天可见提交量 | 主要类型 | 主要热点范围 | 粗结论 |
| --- | ---: | --- | --- | --- |
| `sipeed/picoclaw` | 约 `223` | `merge 61` / `fix 59` / `feat 37` / `refactor 10` | `pkg/`、`cmd/`、`docs/`；scope 以 `wecom`、`agent`、`openai_compat`、`config`、`discord`、`feishu` 为主 | 仍然是 Go 运行时内核、渠道层、provider/auth、命令面并行推进 |
| `openclaw/openclaw` | 约 `2123` | `fix 908` / `refactor 318` / `test 189` / `docs 120` / `feat 64` | `src/`、`extensions/`、`apps/`、`docs/`；scope 以 `agents`、`gateway`、`telegram`、`discord`、`security`、`cron`、`feishu`、`browser`、`cli`、`memory` 为主 | 大体量产品化项目，最近一周主要在修 failover、schema、聊天历史、渠道线程语义和测试基建 |

### 2.0 全量复核补充

- 本轮全量窗口统计结果：
  - `picoclaw`：`223` 条提交
  - `openclaw`：`2123` 条提交
  - 合计：`2346` 条提交
- 这意味着：
  - `picoclaw` 可以相对接近“按主题覆盖主要有效改动”。
  - `openclaw` 不现实做逐条人工细读，但完全可以做**全量聚类 + 热点目录 + 高频主题代表 commit** 的系统扫描。

### 2.0.1 代码热点（二级路径，过滤 changelog / README / docs 等噪音后）

- `picoclaw` 代码热点：
  - `pkg/channels: 69`
  - `pkg/config: 44`
  - `pkg/agent: 43`
  - `pkg/providers: 41`
  - `pkg/tools: 31`
  - `cmd/picoclaw: 23`
  - `pkg/auth: 17`
  - `pkg/routing: 17`
  - `pkg/cron: 15`
- `openclaw` 代码热点：
  - `src/agents: 331`
  - `src/gateway: 176`
  - `src/config: 142`
  - `src/auto-reply: 141`
  - `src/infra: 140`
  - `src/telegram: 134`
  - `src/discord: 125`
  - `src/cli: 115`
  - `src/commands: 114`
  - `src/cron: 113`
  - `extensions/feishu: 104`

### 2.0.1b 子系统热度矩阵（更接近工程落地）

- `picoclaw`：
  - `agent`: `43` 次触达，核心热点文件：`pkg/agent/loop.go`、`pkg/agent/context.go`、`pkg/agent/instance.go`
  - `channels`: `69` 次触达，核心热点文件：`pkg/channels/telegram/telegram.go`、`pkg/channels/discord/discord.go`、`pkg/channels/manager.go`
  - `providers`: `42` 次触达，核心热点文件：`pkg/providers/openai_compat/provider.go`、`pkg/providers/factory.go`
  - `config`: `44` 次触达，核心热点文件：`pkg/config/config.go`、`pkg/config/defaults.go`、`pkg/config/migration.go`
  - `tools`: `31` 次触达，核心热点文件：`pkg/tools/web.go`、`pkg/tools/shell.go`、`pkg/tools/cron.go`
  - `cron`: `15` 次触达，核心热点文件：`pkg/cron/service.go`
  - `routing`: `17` 次触达，核心热点文件：`pkg/routing/route.go`、`pkg/routing/agent_id.go`、`pkg/routing/router.go`
- `openclaw`：
  - `agents`: `331` 次触达，核心热点文件：`src/agents/pi-embedded-runner/run/attempt.ts`、`src/agents/pi-embedded-helpers/errors.ts`
  - `gateway`: `300` 次触达，核心热点文件：`src/gateway/server-http.ts`、`src/gateway/server.impl.ts`、`src/daemon/systemd.ts`
  - `config`: `199` 次触达，核心热点文件：`src/config/schema.help.ts`、`src/config/zod-schema.ts`、`src/config/zod-schema.providers-core.ts`
  - `channels`: `564` 次触达，核心热点文件：`src/telegram/bot-message-context.ts`、`src/discord/monitor/provider.ts`、`extensions/feishu/src/bot.ts`、`extensions/mattermost/src/channel.ts`
  - `cron`: `113` 次触达，核心热点文件：`src/cron/service.issue-regressions.test.ts`、`src/cron/service/jobs.ts`
  - `commands`: `114` 次触达，核心热点文件：`src/commands/agent.ts`、`src/commands/status.scan.ts`
  - `ui_chat`: `200` 次触达，核心热点文件：`src/auto-reply/reply/session.ts`、`src/auto-reply/reply/acp-projector.ts`、`ui/src/ui/...`
  - `plugins`: `169` 次触达，核心热点文件：`src/plugin-sdk/index.ts`、`src/plugins/loader.ts`、`extensions/acpx/src/runtime.ts`

### 2.0.2 全量主题聚类（基于 subject 关键词与 scope 的粗聚类）

- 说明：下面是“可重叠主题簇”，一个提交可能同时命中多个主题，所以计数不能相加为总提交数。
- `picoclaw` 粗聚类计数（只看相对密度）：
  - `tools / exec / shell`: `24`
  - `channel / thread / reply`: `23`
  - `config / schema / provider config`: `23`
  - `tests / ci / coverage`: `21`
  - `provider / model / compat`: `20`
  - `auth / oauth / token`: `3`
  - `cron / schedule / job`: `2`
- `openclaw` 粗聚类计数（只看相对密度）：
  - `tests / ci / coverage`: `653`
  - `channel / thread / reply`: `403`
  - `chat history / ui / sanitize`: `237`
  - `gateway / daemon / health / probe`: `235`
  - `tools / exec / mcp / shell`: `189`
  - `config / schema / browser profile`: `151`
  - `auth / oauth / token`: `135`
  - `provider / model / catalog`: `120`
  - `cron / jobs / schedule`: `114`
  - `agent failover / billing / cooldown`: `75`
- 用这套粗聚类再回看代码热点，可以比较稳定地得出：
  - `picoclaw` 的变化中心仍然是 Go runtime 内核和渠道/tool/provider 边界。
  - `openclaw` 的变化中心则是成熟产品的渠道语义保护、gateway/daemon 诊断、配置 schema、测试基建和 agent failover 细化。

### 2.1 主题密度判断

- `picoclaw` 近 7 天的信号很清晰：
  - 不是“做大功能”主导，而是**围绕 agent loop、channels、cron、auth、provider 和命令面做边界收敛与缺陷修复**。
  - 子代理对全量 `223` 条提交复核后，进一步确认高价值主线集中在：
    - `agent loop` 的最终回复兜底、并行 tool-call 与 data race 加固
    - `channels` 的消息分片、reply context、平台语义修复
    - `providers/config/auth` 的错误体兼容与配置事实来源收口
    - `tools/exec/cron/background tasks` 的运行时硬边界
  - 很多提交都落在 `pkg/agent/loop.go`、`pkg/cron/service.go`、`pkg/channels/*`、`pkg/providers/*` 这些 X-Claw 也正在收口的区域，参考价值很高。
- `openclaw` 近 7 天的信号则更偏“成熟产品迭代”：
  - **大量 fix / refactor / test**，说明它在持续把边界条件、配置 schema、渠道线程回复、计费错误恢复、daemon/gateway 诊断做实。
  - 子代理对全量 `2123` 条提交复核后，进一步确认高价值主线集中在：
    - `channels / plugins / allowlist`（约 `717` 次命中）
    - `agents / failover / subagent runtime`（约 `516` 次命中）
    - `cron / auto-reply / routing`（约 `364` 次命中）
    - `gateway / daemon / auth`（约 `349` 次命中）
    - `provider / config / model defaults`（约 `317` 次命中）
    - `exec / sandbox / process boundary`（约 `204` 次命中）
  - 对 X-Claw 的价值不在“照抄功能”，而在于**借鉴错误分类、测试 seam、配置防漂移、线程回复语义和 gateway/daemon 诊断的工程化做法**。

## 3. Picoclaw 近 7 天值得关注的更新

### 3.1 Agent / Routing：更像“收口与兜底”而不是加功能

1. **无 tool-call 时允许从 reasoning content 回退为最终回复**  
   提交：`66e6fb6`  
   链接：<https://github.com/sipeed/picoclaw/commit/66e6fb6c79e6f3d1bbc6f714ba89c8c070f83096>

   核心变化：
   - 当 LLM 没有返回 tool calls 时，原来只取 `response.Content`。
   - 现在如果 `Content` 为空而 `ReasoningContent` 非空，会把 `ReasoningContent` 作为最终回复。

   判断：
   - 这是一个非常小但非常实用的兜底。
   - 它解决的是“模型实际上给了内容，但放到了 reasoning 字段里”的 provider 兼容问题。

2. **Routing 增补 CJK 估算与 observability**  
   提交：`b84adac`  
   链接：<https://github.com/sipeed/picoclaw/commit/b84adacc2f302aa68c3ccd88bc5815ff51904273>

   触达文件：
   - `pkg/routing/features.go`
   - `pkg/routing/router.go`
   - `pkg/routing/router_test.go`
   - `pkg/agent/loop.go`

   判断：
   - 重点不是“新路由功能”，而是**复杂度估算与日志可观测性**。
   - 这类改动对后续定位“为什么选了这个模型/为什么降级”很有帮助。

3. **并行执行相关修正继续集中在 agent loop**  
   提交：`a32a4e0`（PR merge，实际改动落在 `pkg/agent/loop.go`）  
   链接：<https://github.com/sipeed/picoclaw/commit/a32a4e007d264cbf3f6d82ab5c041771a925d65b>

   判断：
   - 这说明它最近还在修 agent loop 的竞争边界。
   - 对 X-Claw 的启发不是“照搬代码”，而是：**agent loop 周边的每一个“看起来只是整理”的动作，都应该伴随并发/重入路径回归测试**。

### 3.2 Channels / Commands：把“命令”“分片”“回复上下文”都做成明确契约

1. **集中式命令注册表 + 子命令路由**  
   提交：`b716b8a`  
   链接：<https://github.com/sipeed/picoclaw/commit/b716b8a053a4d1e163fc43f6832aa081fb748152>

   触达文件很多，重点包括：
   - `pkg/commands/registry.go`
   - `pkg/commands/executor.go`
   - `pkg/channels/telegram/command_registration.go`
   - `pkg/agent/loop.go`

   判断：
   - 这不是简单加几个命令，而是把“命令定义 / 执行 / 渠道路由”拆成独立层。
   - 对未来有大量聊天内命令的项目很值钱。

2. **Telegram chunking 去掉冗余 SplitMessage**  
   提交：`f07dbd1`  
   链接：<https://github.com/sipeed/picoclaw/commit/f07dbd1db2d88fd270bab07aea119e8861b5ba71>

   触达文件：
   - `pkg/channels/telegram/telegram.go`
   - `pkg/channels/telegram/telegram_test.go`

   判断：
   - 非常典型的“分片职责只能有一个事实来源”。
   - 和 X-Claw 这轮对 channel manager / send path 的边界收敛思路非常一致。

3. **Discord reply context 修复**  
   提交：`4d965f2`（PR merge），实际 PR 头提交：`e061636`  
   链接：<https://github.com/sipeed/picoclaw/commit/4d965f2c81b3aeffe4f478d720c8d2c41639d3ed>

   实际触达：
   - `pkg/channels/discord/discord.go`
   - `pkg/channels/discord/discord_resolve_test.go`

   判断：
   - 它关注的是**避免回复上下文重复展开 / 错绑**。
   - 这类改动与 X-Claw 的 reply binding、placeholder edit、message edit 契约是同一类问题。

4. **Feishu + tools：新增 send_file 出站媒体投递**  
   提交：`c368b5b`  
   链接：<https://github.com/sipeed/picoclaw/commit/c368b5b3599c918fb2c1c7cf99639b63c61264d9>

   触达文件：
   - `pkg/tools/send_file.go`
   - `pkg/tools/send_file_test.go`
   - `pkg/agent/loop.go`
   - `pkg/agent/registry.go`
   - `pkg/config/config.go`

   判断：
   - 这是一个很强的“产品化小能力”：**把工作区文件投递为渠道媒体消息**。
   - 它不是重写渠道层，而是把现有媒体总线能力抬升成一个一等 tool。

### 3.3 Runtime / Cron / Auth / Provider：更多是“可诊断”和“可恢复”

1. **Cron 增加执行生命周期日志**  
   提交：`1945436`  
   链接：<https://github.com/sipeed/picoclaw/commit/1945436dd44817bb3eca9919e63dc8d7f1325c25>

   核心变化：
   - job 开始时打日志
   - job 失败时记录耗时与错误
   - job 完成时记录耗时与下次运行时间

   判断：
   - 不是“多打点”那么简单，而是把 cron run 的状态机可见化。
   - 对线上排查“没跑 / 跑挂 / 跑完后没再触发”很有用。

2. **reload-config selfkill guard + 去掉冗余 kill -9 模式**  
   代表提交：`3738040`、`aa2d6b3`  
   链接：
   - <https://github.com/sipeed/picoclaw/commit/37380409870d8b985e812e548fd1a84145c76241>
   - <https://github.com/sipeed/picoclaw/commit/aa2d6b39f523a0e606c38609d2195bd5ed7e918f>

   判断：
   - 它在处理的是“热重载 / 进程控制 / tool 执行”中的误杀风险。
   - 这是非常典型的守边界思路：**避免用粗暴 kill 来掩盖状态机缺陷**。

3. **Anthropic OAuth setup-token 登录**  
   提交：`23abbb6`  
   链接：<https://github.com/sipeed/picoclaw/commit/23abbb67ea378d59d9384ce88bc32a1d1aa2ad9a>

   判断：
   - 这条对 X-Claw 不是最高优先级，但说明 provider auth 最近在往“多入口登录 + 自动写配置”的方向成熟。
   - 如果 X-Claw 后续要补更完整的 provider onboarding，这条有参考价值。

4. **Provider / OpenAI-compat / Vivgrid 的推理逻辑修正**  
   代表提交：`4df4138`、`6eaa49f`、`53cba73`  
   链接：
   - <https://github.com/sipeed/picoclaw/commit/4df413866381a7cc417e35c7d37c371241969db7>
   - <https://github.com/sipeed/picoclaw/commit/6eaa49f7ab1f488ba8b3df39539cb191339b0799>
   - <https://github.com/sipeed/picoclaw/commit/53cba73283e53e1bf6933cf01a42de3b696bc298>

   判断：
   - 说明 provider 兼容层仍然是高频 bug 区。
   - 对 X-Claw 的意义主要是：**alias / HTML 响应 / provider 特化错误面都需要持续用回归测试锁住**。

## 4. OpenClaw 近 7 天值得关注的更新

### 4.1 Agents / Failover：重点是“错误分类做细，恢复路径做活”

1. **拓宽 402 temporary-limit 检测，并允许 billing cooldown probe**  
   提交：`92648f9`  
   链接：<https://github.com/openclaw/openclaw/commit/92648f9ba9d1ba1ee441b805cf6cb17ed9b68358>

   触达文件：
   - `src/agents/model-fallback.ts`
   - `src/agents/pi-embedded-helpers/errors.ts`
   - `src/agents/pi-embedded-runner/run.ts`
   - 多个 failover / billing 相关测试

   核心信号：
   - 不是把所有 `402` 一律当硬性余额不足。
   - 会区分临时 spend-limit / 真正 insufficient-credit，并允许做 cooldown probe。

   判断：
   - 这是非常成熟的 failover 思路：**别把短时计费限流误判成长期封禁**。

2. **模型 catalog 的前向兼容 / 退场模型清理**  
   代表提交：`59102a1`、`5d22bd0`、`a035a3c`  
   链接：
   - <https://github.com/openclaw/openclaw/commit/59102a1ff7686668c41cdc0fa8f6557a2f8384e7>
   - <https://github.com/openclaw/openclaw/commit/5d22bd0297a6bbfe1a122e64c76b89d806ea3497>
   - <https://github.com/openclaw/openclaw/commit/a035a3ce48e45b925f45bd0e289ba07b7e2b5991>

   判断：
   - 大项目在 provider catalog 上的一个核心经验就是：**兼容新增 alias，比追着报错更重要；及时删掉已移除模型，也能减少误诊**。

### 4.2 Gateway / Chat / Thread：修的是“语义正确性”，不是性能噱头

1. **CLI：避免没有 listener attribution 时误报 update restart failure**  
   提交：`c381034`  
   链接：<https://github.com/openclaw/openclaw/commit/c3810346f9451e4ef7089f0fc94bd9c0f902e60b>

   触达文件：
   - `src/cli/daemon-cli/restart-health.ts`
   - `src/cli/daemon-cli/restart-health.test.ts`

   判断：
   - 它在修“健康检查误报导致错误重启判断”。
   - 这类问题很像 X-Claw gateway/reload/health 的后续演进方向。

2. **保留 dashboard/chat history 里的 sender labels**  
   提交：`930caea`  
   链接：<https://github.com/openclaw/openclaw/commit/930caeaafb1e5ab281067dd7ac26ed66a32271d9>

   触达文件：
   - `src/gateway/chat-sanitize.ts`
   - `ui/src/ui/chat/message-normalizer.ts`
   - `ui/src/ui/views/chat.ts`

   判断：
   - 它修的是“历史清洗”把语义抹掉的问题。
   - 对任何有 console / web history 视图的系统都很关键。

3. **Mattermost threaded reply：replyToId 与 root_id 对齐**  
   提交：`9425209`  
   链接：<https://github.com/openclaw/openclaw/commit/9425209602143aef73288fe61edc47b23fbc5eb2>

   判断：
   - 本质上是在把“回复某条消息”和“回复某个 thread 根”的语义区分开。
   - 这跟 X-Claw 的 reply binding / placeholder edit / message edit 一样，属于典型的渠道语义保护。

### 4.3 Config Schema / 测试基建：大量小修都在降低未来维护成本

1. **Discord agentComponents config 校验补齐**  
   提交：`d902bae`  
   链接：<https://github.com/openclaw/openclaw/commit/d902bae554ca49614b642dba381a7546f134f64e>

   触达文件：
   - `src/config/zod-schema.providers-core.ts`
   - `src/config/config.discord-agent-components.test.ts`

   判断：
   - 这是典型的 schema parity 修复：**文档允许、运行时支持、schema 也必须接受**。
   - 对 X-Claw 后续 provider/channel config 扩展非常有参考价值。

2. **配置 schema 接受新的 browser profile driver 值**  
   提交：`e5fdfec`  
   链接：<https://github.com/openclaw/openclaw/commit/e5fdfec9dc1fee5a33775a10f47dc4ff90246737>

   判断：
   - 这类改动提醒一点：**配置枚举值是最容易漂移的边界之一**。
   - X-Claw 在 provider alias / protocol canonicalization 上已经做了一轮，但 channel/provider 扩展配置仍然值得继续加“接受/拒绝”测试。

3. **daemon probe auth seam test / exec timeout fixture / docker live tests**  
   代表提交：`7d2b146`、`dc78725`、`21df014`  
   链接：
   - <https://github.com/openclaw/openclaw/commit/7d2b146d8d89faf6c3aa9ca6578ecfd948f4f0b5>
   - <https://github.com/openclaw/openclaw/commit/dc78725d47934e7bb948267117a33c05746f9d1d>
   - <https://github.com/openclaw/openclaw/commit/21df014d56030eaf8a40fb137c9b5eaa89c7c1d7>

   判断：
   - 它们不是“功能提交”，但都很值钱：
     - 把 auth/probe seam 单测化
     - 把 timeout fixture 稳定化
     - 把 live docker tests 做成 mounted source 方式
   - 这类基建会明显降低回归成本。

### 4.4 Refactor：把重复 merge 逻辑收口成 helper

1. **voice-call 共享 TTS deep merge**  
   提交：`ed43743`  
   链接：<https://github.com/openclaw/openclaw/commit/ed437434afcdb5f2819f6e1f47b6c88cb7e8bf6f>

   判断：
   - 这是非常标准的“把配置合并逻辑抽成单点事实来源”。
   - 价值不在 voice-call，而在于：**任何带 nested override 的配置对象都适合这么做**。

### 4.4b OpenClaw 全量复核补充（第二轮）

- 子代理基于本地 `/tmp/openclaw-review` 对全量 `2123` 条提交做了二次复核，补充确认：
  - 高价值主题不是 release/changelog，而是 `channels/plugins`、`agents runtime`、`cron/auto-reply/routing`、`gateway/daemon/auth`、`provider/config/defaults`。
  - 代表性提交包括：
    - `92648f9`：`fix(agents): broaden 402 temporary-limit detection and allow billing cooldown probe`
    - `f304ca0`：`fix(agents): sanitize strict openai-compatible turn ordering`
    - `c381034`：`CLI: avoid false update restart failures without listener attribution`
    - `9425209`：`fix(mattermost): pass payload.replyToId as root_id for threaded replies`
    - `e5fdfec`：`fix(config): accept "openclaw" as browser profile driver in Zod schema`
    - `149ae45` / `e66c418` / `9b99787`：一组 `cron` 默认值/策略 helper 收口
- 这组证据进一步说明：OpenClaw 最近最值得借鉴的不是“大功能”，而是**把 fallback / probe / schema / threaded reply / cron policy 做成小而硬的边界补丁**。

## 4.5 X-Claw 当前承接基础（按代码事实）

- `ReasoningContent`：
  - 已有 provider 解析与保留：`pkg/providers/openai_compat/provider.go`
  - 已有历史消息透传：`pkg/agent/loop_tools.go`
  - 仍缺 direct-answer 最终回复兜底：`pkg/agent/loop.go:1389`
- `send_file` / 出站媒体：
  - 已有媒体总线：`pkg/bus/types.go`、`pkg/bus/bus.go`
  - 已有 channel `SendMedia` 能力：`pkg/channels/manager_send.go`、`pkg/channels/telegram/telegram.go`、`pkg/channels/feishu/feishu_media.go`
  - 仍缺一等工具入口：`pkg/tools` 当前没有等价的 `send_file` tool
- `billing cooldown`：
  - 已有 `402 -> FailoverBilling`：`pkg/providers/error_classifier.go`
  - 已有 billing 指数退避：`pkg/providers/cooldown.go`
  - 仍缺对“temporary spend-limit vs 真正 insufficient-credit”的更细粒度区分
- `cron observability`：
  - 已有 `RunningRunID`、`RunHistory`、`LastOutputPreview`：`pkg/cron/service.go`
  - 已有 runner/schedule 分层：`pkg/cron/service_runner.go`、`pkg/cron/service_schedule.go`
  - 仍缺 start/finish/next-run 的稳定结构化日志或审计事件
- `reply / contract tests`：
  - 已有 manager 契约回归：`pkg/channels/manager_test.go`
  - 已有 console / session / auth 稳定错误面测试：`pkg/httpapi/console_test.go`、`pkg/session/manager_test.go`
  - 仍缺更显式的“thread/reply/edit”跨渠道契约矩阵
- `config / alias drift`：
  - 已有 provider alias / protocol 测试：`pkg/providers/model_ref_test.go`、`pkg/providers/factory_provider_test.go`
  - 已有 migration 测试：`pkg/config/migration_test.go`
  - 仍可继续补 channel/provider 枚举值 drift 测试

## 4.6 X-Claw 承接优先级（第二轮，按“补丁最小 + 风险收益比”排序）

- 如果只选一个最优先切入点，子代理给出的排序是：
  - `P0`：`Provider` 错误归一化 + fallback 原因细化
  - `P1`：`cron` 生命周期事件与 `run/tool trace` 关联
  - `P2`：auth bootstrap 统一入口
  - 候补：`send_file` / outbound media delivery
- 对应当前代码落点：
  - `P0`：`pkg/providers/error_classifier.go`、`pkg/providers/cooldown.go`、`pkg/providers/fallback.go`、`pkg/providers/openai_compat/provider.go`、`pkg/agent/loop_fallback.go`
  - `P1`：`pkg/cron/service.go`、`pkg/cron/service_runner.go`、`pkg/cron/service_schedule.go`、`pkg/tools/cron.go`
  - `P2`：`pkg/auth/oauth_device.go`、`pkg/auth/oauth_browser.go`、`pkg/auth/token.go`、`pkg/providers/factory_auth.go`
- 这和本文前面的“影响优先级”不冲突：
  - **影响优先级**里，`ReasoningContent` 兜底和 `send_file` 也很值得做；
  - **工程承接优先级**里，更应该先做 provider 错误归一化，因为它和 `openclaw` 最近一周最密集的 fallback/402/probe 修复主线最同构，而且 X-Claw 已经有现成基础。

## 5. 对 X-Claw 的可移植建议

下面按“值得做 / 风险 / 适配成本”排序。

### 5.1 P0：低风险、高收益，建议优先看

1. **给 X-Claw 的 direct-answer 路径补 `ReasoningContent` 兜底**

   参考：`picoclaw` `66e6fb6`  
   X-Claw 现状：
   - `pkg/agent/loop.go` 在 `len(response.ToolCalls) == 0` 时只把 `response.Content` 赋给最终回复。
   - `pkg/agent/pipeline_notify.go` 只有“最终为空则用默认回复”的兜底，没有“优先用 `ReasoningContent`”的逻辑。

   结论：
   - **值得直接移植。**
   - 成本低，风险低，能覆盖一类 provider 响应兼容问题。

2. **把现有 billing cooldown 再细化为“硬性余额不足”与“临时 spend-limit / 可探测恢复”**

   参考：`openclaw` `92648f9`  
   X-Claw 现状：
   - `pkg/providers/error_classifier.go` 已经把 `402` 归类到 `FailoverBilling`。
   - `pkg/providers/cooldown.go` 已经有 billing-specific disable 与指数退避。
   - 但目前分类仍偏粗，**所有 `402` 更接近统一长冷却**。

   结论：
   - **不是从 0 到 1，而是从“已有基础”升级到“更细粒度恢复策略”。**
   - 适合后续新增：临时 billing limit 的短探测窗口、显式 insufficient-credit 的长禁用窗口。

3. **把现有 OutboundMedia 能力抬成一等 `send_file` tool**

   参考：`picoclaw` `c368b5b`  
   X-Claw 现状：
   - 已经有 `bus.PublishOutboundMedia()`、`OutboundMediaMessage`、channel `SendMedia()`。
   - 也已经有媒体 store / media worker。
   - 但**还没有一个直给 agent 用的“一等文件发送 tool”**。

   结论：
   - **很值得做。**
   - 它能立刻提升 agent 的“交付文件/截图/文档”能力，而且不用重写 channel 层。

4. **给 cron 补 start / finish / next-run 的结构化日志或审计事件**

   参考：`picoclaw` `1945436`  
   X-Claw 现状：
   - `pkg/cron/service_runner.go` 已经有 `Running`、`LastStatus`、`LastError`、`RunHistory` 等状态字段。
   - 但对 operator 而言，仍然偏“事后看状态”，不是“运行时可观察”。

   结论：
   - **值得做，而且和当前架构天然匹配。**
   - 可以优先做结构化日志；下一步再按需记 `auditlog.Record`。

### 5.2 P1：值得做，但应等核心 runtime 再稳定一些

5. **把 reply/thread 语义的契约测试做成跨渠道公共模式**

   参考：
   - `picoclaw` Discord reply context：`4d965f2`
   - `openclaw` Mattermost root_id：`9425209`

   X-Claw 现状：
   - 已有 reply binding、placeholder edit、消息编辑、media dispatch。
   - 但跨渠道的“回复哪条消息 / thread 根 / edit 到哪里”的契约，仍然主要靠局部测试守住。

   结论：
   - **很适合做一套 channels contract tests。**
   - 不一定要先加新渠道，先把 Feishu / Telegram 的回复语义锁住就很值。

6. **给 provider/channel config 补更多 schema parity / alias drift 测试**

   参考：`openclaw` `d902bae`、`e5fdfec`  
   X-Claw 现状：
   - 最近一轮已经对 provider alias / protocol canonicalization 做过收口。
   - 但“配置允许值”和“运行时支持值”长期仍可能漂移。

   结论：
   - **不是急救项，但属于维护成本很低、长期收益很高的加固。**

7. **把 gateway/daemon 的误报式重启判断进一步细化**

   参考：`openclaw` `c381034`  
   X-Claw 现状：
   - R5 已经修了一轮 reload/runtime/gateway 装配与边界收敛。
   - 但如果后续做更强的 health-monitor / restart controller，这种“没有 listener attribution 就别误判失败”的逻辑会很重要。

   结论：
   - **现在可以先记为设计注意点，不必立刻编码。**

8. **把 nested config 的 merge helper 进一步单点化**

   参考：`openclaw` `ed43743`  
   结论：
   - 如果 X-Claw 后续继续扩 voice / notify / tool defaults / channel defaults，这类 deep-merge helper 很值得抽成统一组件。

### 5.3 P2：有启发，但暂时没必要优先做

9. **集中式聊天命令注册表**

   参考：`picoclaw` `b716b8a`  
   结论：
   - 设计很干净，但 X-Claw 目前刚把 CLI/runtime surface 做瘦。
   - 如果短期不扩大量聊天内命令，这不是最优先。

10. **mounted-source 的 live docker smoke tests**

   参考：`openclaw` `21df014`  
   结论：
   - 方向很好，但更适合在 core runtime 再稳定一点后再投入。

## 6. 不建议现在跟的内容

这些更新对 `X-Claw` 当下价值较低：

- `openclaw` 的 release/appcast/secrets baseline/mobile icon 等发布链路事项
- `picoclaw` 的 MIPS32 交叉编译、Volcengine TOS 上传、二维码/文档素材更新
- 大量 changelog / contributor / release prep 清理类提交

## 7. 我对当前 X-Claw 的建议顺序

### 7.1 可以立即开做的 4 个小项

1. `pkg/agent/loop.go`：无 tool-call 且 `response.Content == ""` 时，回退到 `response.ReasoningContent`
2. `pkg/tools`：新增一等 `send_file` tool，桥接到现有 `OutboundMediaMessage`
3. `pkg/cron`：补执行生命周期结构化日志，必要时补 `auditlog.Record`
4. `pkg/providers`：把 `402` / billing cooldown 再细分成“硬禁用”与“可探测恢复”

### 7.2 下一批适合做的项

5. `pkg/channels`：补 reply/thread/edit 契约测试矩阵
6. `pkg/config` / `pkg/providers`：补 alias / enum / schema drift 测试
7. `docker/` 或 `scripts/`：补更贴近真实运行的 gateway smoke tests

## 8. 结论

如果只提炼成一句话：

- `picoclaw` 最近 7 天最值得学的是：**Go runtime 内核层的小而准的兜底、命令/渠道/cron 的职责边界收口**。
- `openclaw` 最近 7 天最值得学的是：**错误分类细粒度恢复、配置 schema 防漂移、聊天历史与线程语义保护、以及测试 seam 的工程化做法**。

对当前 `X-Claw` 来说，最值得优先吸收的不是“大功能”，而是以下四类“能立刻降低线上风险”的经验：

1. `ReasoningContent` 回复兜底
2. `send_file` 一等工具化
3. cron 运行生命周期可观测
4. 402 / billing cooldown 细粒度恢复

---

## 参考链接

### 仓库
- Picoclaw: <https://github.com/sipeed/picoclaw>
- OpenClaw: <https://github.com/openclaw/openclaw>

### 代表性提交
- Picoclaw `66e6fb6`: <https://github.com/sipeed/picoclaw/commit/66e6fb6c79e6f3d1bbc6f714ba89c8c070f83096>
- Picoclaw `1945436`: <https://github.com/sipeed/picoclaw/commit/1945436dd44817bb3eca9919e63dc8d7f1325c25>
- Picoclaw `c368b5b`: <https://github.com/sipeed/picoclaw/commit/c368b5b3599c918fb2c1c7cf99639b63c61264d9>
- Picoclaw `b716b8a`: <https://github.com/sipeed/picoclaw/commit/b716b8a053a4d1e163fc43f6832aa081fb748152>
- Picoclaw `23abbb6`: <https://github.com/sipeed/picoclaw/commit/23abbb67ea378d59d9384ce88bc32a1d1aa2ad9a>
- OpenClaw `92648f9`: <https://github.com/openclaw/openclaw/commit/92648f9ba9d1ba1ee441b805cf6cb17ed9b68358>
- OpenClaw `c381034`: <https://github.com/openclaw/openclaw/commit/c3810346f9451e4ef7089f0fc94bd9c0f902e60b>
- OpenClaw `930caea`: <https://github.com/openclaw/openclaw/commit/930caeaafb1e5ab281067dd7ac26ed66a32271d9>
- OpenClaw `d902bae`: <https://github.com/openclaw/openclaw/commit/d902bae554ca49614b642dba381a7546f134f64e>
- OpenClaw `9425209`: <https://github.com/openclaw/openclaw/commit/9425209602143aef73288fe61edc47b23fbc5eb2>
- OpenClaw `ed43743`: <https://github.com/openclaw/openclaw/commit/ed437434afcdb5f2819f6e1f47b6c88cb7e8bf6f>
