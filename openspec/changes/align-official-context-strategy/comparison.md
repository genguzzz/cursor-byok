# Official vs Local — Traffic & Strategy Comparison

> 证据截止：2026-08-09。本地样本会话 `8405650e-be6a-4493-b6ae-a7723c06da2c`；官方样本来自 debugger `run_request.conversation_state.token_details` 与 `cursor-agent-exec`。

## 1. 上下行关键消息

| 消息 | 方向 | 官方 | 本地 | 差异 |
|------|------|------|------|------|
| `BidiAppend` / `run_request` | ↑ | 带 `conversation_state.token_details`（上回 checkpoint） | 同左 | 字段形状应对齐；内容取决于上回服务端 |
| `RunSSE` / `conversation_checkpoint_update` | ↓ | 更新 state + `token_details` | 同左 | **本地已发 8 类 breakdown**；**缺 usage_tree** |
| `ExecuteHookRequest.preCompact` | ↓ | 压缩前 hook | 有 | 字段基本同构 |
| SummaryStarted / Summary / SummaryCompleted | ↓ | 有 | 有 | 文案/时机需对照 |
| `exec_server_message` / `exec_client_message` | ↓↑ | 工具执行 | 有 | 本地 history 全量存结果 |
| Provider HTTP（模型 API） | 本地独有出站 | 云端内部 | `provider.jsonl` | 本地可观测；官方不可见 |

## 2. Context Usage 计量

| 项 | 官方 | 本地（安装后） | 状态 |
|----|------|----------------|------|
| `used_tokens` / `max_tokens` | 有 | 有（渠道 `contextWindowTokens`） | 对齐 |
| breakdown 8 类 id | 固定表 | 固定表 | **已对齐** |
| label 文案 | System prompt / Tool definitions / … | 同 | **已对齐** |
| `character_count` | 有 | 有 | **已对齐** |
| 分类归因算法 | 服务端精确 | XML/工具名启发式拆分 | **部分对齐** |
| `prompt_context_usage_tree` | Explorer 使用 | 未填 | **未对齐** |

## 3. 系统提示词

| 项 | 官方 | 本地 | 状态 |
|----|------|------|------|
| 开场 | `You are a powerful agentic AI coding assistant powered by Cursor...` | `common_prefix`「极度务实…」+ 中文 agent prompt | **未对齐** |
| 结构区块 | communication / tool_calling / making_code_changes… | system-communication / tone_and_style…（中文改写） | **部分对齐** |
| 动态 request_context | user_info / rules / skills / mcp_file_system | 同名标签 | **大体对齐** |
| System 桶体量 | ~0.5k tokens | ~9KB+ system | **未对齐** |
| 个性化策略 | 无本地 CTF/FP 长文 | `common_prefix.md` | 本地特有 |

## 4. Tool 结果与 prompt 组装

| 项 | 官方 | 本地 | 状态 |
|----|------|------|------|
| 结果是否完整回传客户端→服务端 | 协同存储 | exec 结果写入 `context.json` 全量 | 本地可查证全量 |
| 模型可见截断 | fair char allocation (~3.2M) + message omit | per-tool replay bytes limit | **部分对齐** |
| 截断发生层 | prompt 组装 | projector replay（不改存档） | 理念相近 |
| Edit/Shell 特殊压缩 | 有 | `compactProjected*Replay` | 部分自研 |

## 5. 压缩

| 项 | 官方 | 本地 | 状态 |
|----|------|------|------|
| 触发阈值 | `used >= floor(0.9*max)` | 同（reserve= max(10k, window-floor(0.9*window))） | **已对齐** |
| 摘要模型调用 | 有 | 有（compaction prompt asset） | 大体对齐 |
| 摘要后 history | summary + tail | `compacted_summary` + 可选当前轮 | 大体对齐 |
| 配置覆盖 | eval unusedTokens/Percent props | 暂无等价配置面 | 可选增强 |

## 6. 优先级建议

1. **P0** System baseline vs overlay（行为与计量双重偏差最大）
2. **P0** `prompt_context_usage_tree`（UI/协议缺口）
3. **P1** Prompt-level budget trim（大工具防爆与官方体验）
4. **P2** Compaction 摘要合同与调试字段打磨
5. **P2** 抓包 golden harness 常态化

## 7. 明确「不要误判」的点

- Context Usage 以前「只有一部分」主要是 **分类未拆**，不是 used/max 没发。
- Tool 结果「不完整」主要发生在 **给模型的 replay**，不是本地没收到 exec 回包。
- 官方 System prompt 桶很小，大量指令在动态段/工具描述；本地把个性化塞进 system，会让桶失真。
