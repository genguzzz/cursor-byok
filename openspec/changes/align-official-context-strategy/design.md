# Design: Align localserver context strategy with official Cursor Agent

## Decision Drivers

1. 客户端 UI（Context Usage / Summarize / Explorer）只认协议字段，不认本地私有启发式。
2. 模型可见上下文形状决定压缩时机与 cache 行为，必须可对照官方抓包。
3. 本地个性化有价值，但不能污染「官方策略基线」的默认路径。
4. 不修改已安装 Cursor 客户端；对齐落在本仓库 forwarder / prompt / projector。

## Architecture Overview

```text
                    ┌─────────────────────────────┐
   Cursor Desktop   │  BidiAppend / RunSSE         │
                    └─────────────┬───────────────┘
                                  │
              ┌───────────────────┼───────────────────┐
              ▼                   ▼                   ▼
        Official cloud      Local MITM :18080    Local Agent :18090
        (agent-exec)        (passthrough/mix)    (forwarder+prompt)
              │                                       │
              │  conversation_checkpoint_update       │
              │  token_details + usage_tree           │
              │  preCompact / summary_*               │
              └───────────────────┬───────────────────┘
                                  ▼
                         Same UI expectations
```

本地应对齐的「策略面」而非「字节级云端实现」：

| 策略面 | 官方真相源 | 本地落点 |
|--------|------------|----------|
| Token UI | checkpoint `token_details` | `rewriteCheckpointTokenDetailsForClient` |
| 压缩触发 | `floor(0.9*maxTokens)` | `resolveCompactionReserveTokensForWindow` |
| System 文本 | agent-exec 模板 | `prompt/*` + `ReadPrompt` |
| 动态上下文 | request_context 段落 | `prompt/engine.go` |
| Tool 防爆 | fair char alloc + omit | projector + `tool_result_replay_truncation.go` |
| 历史真相 | 云端 conversation state | `state.json` + `context.json` |

## Decision 1: System prompt = Official baseline + optional local overlay

### Decision

- **System 文本**：以 **抓包计量契约** 为准——官方 `token_details.breakdown.system_prompt` ≈ 477 tokens / 1877 chars；各模式短英文写入既有 `prompt/<mode>/prompt.md`（`<communication>` / `<tool_calling>` / …）。
- **模式约束**：Ask / Plan / Debug 的差异主要在 reminder 与 tools，不另建 overlay 目录或配置开关。
- **动态段保持现状**：`<user_info>` `<rules>` `<agent_skills>` `<mcp_file_system>` 继续由 `engine.go` / projector 从 `RequestContext` 生成。

### Evidence

- 抓包 fixture：`fixtures/official-token-details.json`（system_prompt 1877 chars）
- 客户端模板源：`/Applications/Cursor.app/.../cursor-agent-exec/dist/main.js`（压缩包约 9 行；`powerful agentic` / Plan·Debug reminder 等在第 2 行超长列）
- **本地 `prompt/<mode>/prompt.md` 是 best-effort 重建**（参考 agent-exec 结构 + 计量量级），**不是**云端 system 逐字 dump；Bidi/RunSSE 上下行不带 system 明文，无法用抓包验收正文。
- **不要**把 agent-exec 完整骨架当当前云端 `system_prompt` 桶字节真相：静态还原约 3.5KB+，抓包计量恒约 1877 chars
- 本地旧路径：`common_prefix` + 中文 mode prompt → system ~9KB+

### Consequences

- 对齐后 Context Usage 的 System prompt 占比接近官方。
- 用户若依赖 CTF/FP 等本地风格，需显式打开 overlay。

## Decision 2: Protocol metering must include usage tree

### Decision

在 `rewriteCheckpointTokenDetailsForClient` 中除 `breakdown` 外填充：

```text
token_details.prompt_context_usage_tree
  schema_version
  nodes[]: id, parent_id?, kind, label, category_id, estimated_tokens, character_count, ...
```

最小可用树：

1. 每类 breakdown category 一个父节点。
2. Skills / Rules / MCP servers / 关键 tools 作为子节点（能拿到路径/名称时填 `source` / `inline_content` 可选）。

### Evidence

- Proto：`proto/agent_v1.proto` `PromptContextUsageTree` / `PromptContextNode`
- 客户端 UI：`CATEGORY_HELP` + Context Explorer 依赖 tree；仅有 breakdown 时 View Report 能力不完整
- 本地：当前无 Go 引用填充 `prompt_context_usage_tree`

### Consequences

- Context Explorer 与官方一致可用。
- 树构建成本需控制在 checkpoint 路径可接受范围内（避免每次全量重读 skill 文件）。

## Decision 3: Truncation strategy — map official behavior onto local projector

### Decision

分两层对齐，而不是只保留 per-tool 常数：

1. **Keep（已有）**：`limitProjectedToolResultReplay` 作为单工具硬上限（防单条炸裂）。
2. **Add（对齐官方）**：在 compiled messages 送 provider 前，增加 **prompt-level budget trim**：
   - 预算与官方同量级（抓包/agent-exec：`V ≈ 3.2e6` 字符级 fair allocation 思路）。
   - 优先保护：system baseline、最近 user_query、最近 tool_result tail。
   - 对更早的大 tool_result / 附件做 middle-omit 或整段 omit，文案对齐官方
     `"[N message(s) omitted due to size limits; ...]"` / 本地已有 `[truncated: ...]` 需建立映射表。
3. **Storage**：继续 append-only 全量落库（符合 prefix-cache skill）；截断只影响 model-visible projection。

### Evidence

- 本地：`tool_result_replay_truncation.go`；history 中 >100KB `tool_result` 且存档无 truncated 标记
- 官方：`compute_maxmin_fair_allocations` + omit 文案（agent-exec）
- Prefix-cache：`.agents/skills/prefix-cache-stability/SKILL.md` 要求历史位置稳定；截断必须是「投影时裁剪」而非重排历史

### Consequences

- 大仓库多次 Read 时，本地更接近官方「还没到 90% 也可能先裁旧工具输出」的体验。
- 需增加单元测试：多条超大 tool_result 下最终 compiled 字符数有上界。

## Decision 4: Compaction pipeline — keep 0.9 trigger; align summary contract

### Decision

- 触发阈值保持官方 `0.9`（已落地），不采用更激进的 0.2 除非配置覆盖。
- 摘要 system prompt：以官方「只产替换摘要、不对话用户」合同为准；`prompt/compaction/prompt.md` 已接近，补齐与官方字段/禁止项对照。
- 保留策略：继续保留当前轮 user + 最新 tool pair；历史轮进 summary（已有）。
- 增加可观测性：compaction_request entry 记录 `context_window` / `reserve` / `trigger_ratio`，便于与官方 PreCompact query 对照。

### Evidence

- 官方：`resolveSummaryTokenLimit` → `Math.floor(0.9 * maxTokens)`
- 本地：`resolveCompactionReserveTokensForWindow`
- PreCompact query 字段双方已存在：`contextUsagePercent` / `contextTokens` / `contextWindowSize`

## Decision 5: Golden traffic harness

### Decision

建立「官方 upstream vs 本地 client」对照清单（可用现有 `cursor-proxy-debugger`）：

对同一用户动作记录：

1. 是否出现 `conversation_checkpoint_update.token_details.breakdown` 八类。
2. 是否出现非空 `prompt_context_usage_tree`。
3. system 开场句是否匹配 baseline（overlay off）。
4. 压缩触发时 `used/max` 是否 ≥ ~0.9。
5. 超大 tool 后 provider body 是否含截断/omit 标记且总字符受控。

### Evidence

- 调试器已能区分 `server=official|local`、`captureSource=upstream|client`
- 本地 history `debug/runsse.jsonl` 可作回归样本

## Identity / Auth / Passthrough Risks

- 本 change **不改变** upstream 鉴权与 mixed routing；只改本地 Agent 后端投影与 prompt。
- 若 overlay 默认关闭，用户可能误以为「模型变笨/变英文」——需要菜单/文案说明。
- Stream passthrough（官方模型走 upstream）时，本地不应再改写其 checkpoint；对齐逻辑仅作用于本地推理渠道。

## Alternatives Considered

1. **继续只改 breakdown、不改 system**  
   UI 好看但模型行为仍偏离官方 → 拒绝作为终态。
2. **硬编码完整官方 system 长文一次性拷贝**  
   版本漂移成本高 → 采用「基线文件 + 抓包刷新脚本/文档」而非一次性神话。
3. **存档时截断 tool_result**  
   破坏可重放与排障 → 拒绝；只在投影层截断。

## Rollout

1. 默认：官方 baseline ON，local overlay OFF（或保留 overlay ON 但提供「官方对齐模式」开关——实现时二选一，推荐 **对齐模式默认对本地渠道开启 baseline**）。
2. 先合计量树 + 截断预算，再切默认 prompt，降低一次行为巨变。
