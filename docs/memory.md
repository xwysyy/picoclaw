# 记忆系统

X-Claw 提供结构化的记忆存储与语义检索能力，支持本地哈希 embedder 和远程 OpenAI-compatible embeddings 两种模式。记忆系统让 Agent 能在多轮对话中保留关键信息，并在后续交互中高效召回。

---

## 结构化记忆输出（Memory JSON hits）

`memory_search` / `memory_get` 的工具输出对 LLM 侧返回结构化 JSON（`kind` 字段可用于回归测试与稳定引用），同时对人类侧保留简要摘要：

- `memory_search` -> `{"kind":"memory_search_result","hits":[...]}`
- `memory_get` -> `{"kind":"memory_get_result","found":...,"hit":...}`

这能显著降低"模型看不懂纯文本结果 / 引用不稳定"的概率。

### match_kind 与 signals

`memory_search_result.hits[]` 会提供 `match_kind` 与 `signals` 字段（例如 `fts_score` / `vector_score`），便于你在排查召回漂移时快速判断"这条命中是靠关键词还是靠向量"。

| 字段 | 说明 |
|------|------|
| `match_kind` | 命中类型，标识该结果是通过哪种检索方式匹配到的 |
| `signals.fts_score` | 全文搜索（FTS）得分 |
| `signals.vector_score` | 向量相似度得分 |

### 输出示例

LLM 侧收到的结构化结果（`memory_search`）：

```json
{
  "kind": "memory_search_result",
  "hits": [
    {
      "key": "project-setup",
      "content": "项目使用 Go 1.25，入口在 cmd/main.go",
      "match_kind": "hybrid",
      "signals": {
        "fts_score": 0.85,
        "vector_score": 0.72
      }
    }
  ]
}
```

人类侧则会收到简要摘要，不会暴露底层 JSON 结构。

---

## 语义记忆 Embeddings（可选远程）

X-Claw 的语义记忆（`agents.defaults.memory_vector`）默认使用本地 `hashed` embedder：快、确定性强、无需额外 API / 网络。

如果你希望更高质量的语义检索，可以让 X-Claw 调用一个 OpenAI-compatible 的 embeddings 端点（`POST <api_base>/embeddings`），例如 SiliconFlow / OpenAI / 其他兼容服务。

### Embedder 对比

| 类型 | 优点 | 缺点 |
|------|------|------|
| `hashed`（默认） | 快速、确定性强、无网络依赖、零成本 | 语义理解能力有限 |
| `openai_compat` | 高质量语义检索、支持跨语言 | 需要网络、有 API 成本、首次索引较慢 |

### 配置方式

在本项目中，embeddings 配置 **只从 `config.json` 读取**。示例（放到 `agents.defaults.memory_vector.embedding`）：

```json
{
  "agents": {
    "defaults": {
      "memory_vector": {
        "dimensions": 4096,
        "hybrid": {
          "fts_weight": 0.6,
          "vector_weight": 0.4
        },
        "embedding": {
          "kind": "openai_compat",
          "api_base": "https://api.siliconflow.cn/v1",
          "api_key": "sk-...",
          "model": "Qwen/Qwen3-Embedding-8B",
          "proxy": "",
          "batch_size": 64,
          "request_timeout_seconds": 30
        }
      }
    }
  }
}
```

### 混合检索（Hybrid Search）

X-Claw 支持混合检索模式，同时利用全文搜索（FTS）和向量检索的结果，通过权重加权合并排序：

- `hybrid.fts_weight`：全文搜索权重（默认 0.6）
- `hybrid.vector_weight`：向量搜索权重（默认 0.4）

你可以根据实际场景调整权重比例。例如，关键词精确匹配更重要时提高 `fts_weight`；语义相近但措辞不同的场景提高 `vector_weight`。

### 配置字段说明

| 字段 | 说明 |
|------|------|
| `dimensions` | 向量维度（需与所选模型输出维度一致） |
| `hybrid.fts_weight` | 全文搜索权重 |
| `hybrid.vector_weight` | 向量搜索权重 |
| `embedding.kind` | embedder 类型：`hashed`（默认）或 `openai_compat` |
| `embedding.api_base` | OpenAI-compatible API 基地址（`openai_compat` 时必填） |
| `embedding.api_key` | API 密钥 |
| `embedding.model` | embedding 模型名（`openai_compat` 时必填） |
| `embedding.proxy` | 代理地址（可选） |
| `embedding.batch_size` | 批处理大小 |
| `embedding.request_timeout_seconds` | 请求超时（秒） |

### 注意事项

- 如果 `embedding.kind` 为空或为 `hashed`，则使用本地 deterministic 的 `hashed` embedder（无网络）
- 首次触发语义检索/索引重建时会产生网络请求；索引会落盘缓存，并在源文件或 `api_base/model` 变化时自动重建
- 如果你显式把 `embedding.kind` 设为 `openai_compat`，则 `api_base` 与 `model` 为必填（否则会报错）
- `dimensions` 字段需与你选择的 embedding 模型输出维度保持一致，否则索引重建时会报错
