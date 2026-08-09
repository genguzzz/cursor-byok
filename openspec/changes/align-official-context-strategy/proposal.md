# Proposal: Align localserver context strategy with official Cursor Agent

## Status

Proposed

## Problem Summary

本地模式（`127.0.0.1:18080/18090`）已能承接桌面 Agent 的 `BidiAppend` + `RunSSE`，并在近期补齐了官方风格的
`token_details.breakdown` 八类分类与 `floor(0.9 * maxTokens)` 压缩触发。但对照调试抓包与官方
`cursor-agent-exec` 实现后，**上下文计量、系统提示词、tool 结果防爆、压缩流水线、Explorer 细节树**
仍与云端存在系统性偏差。结果是：

1. 同一对话在官方模型 vs DeepSeek 本地渠道上，Context Usage UI / 压缩时机 / 模型可见上下文形状不一致。
2. 本地 `common_prefix.md` 把强个性化策略叠在「类官方」`prompt/agent/prompt.md` 之上，system prompt
   体积与官方「System prompt」分类（抓包约 477 tokens / ~1.8KB）严重不同。
3. Tool 大结果：本地「history 全量落库 + replay 按工具字节截断」；官方另有 prompt 级 max-min fair
   字符分配与整段 message omit。两者都能防炸，但策略不对齐。

目标：让 **本地 localserver 与官方云端 Agent 在「协议上下行语义 + 上下文组装/截断/压缩策略」上可对照、可验收地对齐**，
从而在同一客户端 UI 上行为可预期。

## Evidence Snapshot（已核实）

### A. 协议：Context Usage

| 来源 | 消息 | 内容 |
|------|------|------|
| 官方抓包 `run_request.conversation_state.token_details` | BidiAppend 上行带回的上一轮 state | 8 类 `breakdown`：`system_prompt` / `tools` / `rules` / `skills` / `mcp` / `subagents` / `summarized_conversation` / `conversation`，含 `character_count` |
| 本地安装后 DeepSeek `history/.../debug/runsse.jsonl`（≥05:17Z） | `conversation_checkpoint_update.token_details` | **已对齐** 同样 8 类 id/label；`max_tokens=500000` |
| 本地安装前同会话 | 同上 | 仅 3 类粗粒度 + 旧 label（`System Prompt`/`Tools`） |
| 官方/本地 | `prompt_context_usage_tree` | 官方 Context Explorer / View Report 使用；**本地基本未填** |

承载消息（双方一致）：

```text
AgentServerMessage.conversation_checkpoint_update
  → ConversationStateStructure.token_details
      → used_tokens / max_tokens
      → breakdown (PromptTokenBreakdownSnapshot)
      → prompt_context_usage_tree (PromptContextUsageTree)  // 本地缺口
```

### B. 协议：压缩

| 项 | 官方 | 本地 |
|----|------|------|
| 触发 | `cursor-agent-exec`：`usedTokens >= floor(0.9 * maxTokens)`（self-summary 默认） | `reserve = max(10k, window - floor(0.9*window))` → 等价触发线（**已对齐**） |
| Hook | `ExecuteHookRequest.preCompact` + SummaryStarted/Completed | 有 PreCompact / Summary 流式更新 |
| 摘要生成 | 官方 summarization pipeline（background / self） | `prompt/compaction/prompt.md` + provider 再请求 |
| 历史替换 | summary 替换大段 messages，保留 tail | `compacted_summary` entry + 可选保留当前轮 |

### C. Tool 结果防爆

| 项 | 官方 | 本地 |
|----|------|------|
| 存档 | 云端侧策略（客户端/服务端协同） | `context.json` **全量**存 `tool_result`（实测单条 >100KB） |
| 模型可见 | prompt truncation：max-min fair 字符分配（常量约 `3.2e6` chars）+ 整段 omit 提示 | `limitProjectedToolResultReplay`：按工具字节上限截断（Read 64KB / Shell 128KB / Grep·MCP 32KB 等） |
| 压缩后再截 | 摘要化 | auto-compaction 保留轮次 tool result：16KB / 兜底 4KB |

本地截断发生在 **projector replay**（组装 provider messages 时），不是 exec 回包丢弃。

### D. 系统提示词

| 层 | 官方（agent-exec 模板） | 本地 |
|----|-------------------------|------|
| 开场 | `You are a powerful agentic AI coding assistant powered by Cursor...` / `You are an AI coding assistant, powered by <model>` | `prompt/common_prefix.md`（中文个性化：务实/FP/DSL/CTF…）**前置拼接** + `prompt/agent/prompt.md`（中文「你是 Cursor IDE 中的编程代理…」） |
| System 分类体量（抓包） | ~477 tokens / ~1877 chars（极短「System prompt」桶） | provider 实测 system ≈ **9.4KB**（common_prefix + agent prompt + 模式段） |
| 结构标签 | `<communication>` / `<tool_calling>` / …（英文模板，随工具动态拼装） | `<system-communication>` / `<tone_and_style>` / `<tool_calling>` / …（本地中文改写） |
| 动态上下文 | request_context → `<user_info>` `<rules>` `<agent_skills>` `<mcp_file_system>` 等 | 同名标签，由 `internal/backend/agent/prompt/engine.go` 组装（**段落级大体对齐**） |

结论：动态 XML 段落已贴近官方；**静态 system 策略文本明显本地化，未对齐官方**。

## Goals

1. **协议语义对齐**：checkpoint / compaction / token_details / usage tree 与官方字段形状、更新时机一致。
2. **上下文策略对齐**：压缩触发、tool replay 防爆、prompt 级截断与官方可对照（允许实现细节差异，但阈值与可观察行为一致）。
3. **系统提示词对齐**：直接改写 `prompt/<mode>/prompt.md` 为官方同构短英文 system（抓包 ~1877 chars 口径）；模式差异走 reminder/tools，不再做 overlay 叠加层。
4. **可验收**：用 debug 抓包（官方 upstream vs 本地 client）做 golden 对照清单。

## Non-goals

- 不 patch `/Applications/Cursor.app` 或官方 `agent` 二进制。
- 不复刻整套官方云端存储 / 计费 / eval override。
- 不要求 provider tokenizer 与官方逐 token 一致（允许启发式 token 估计误差）。
- 不把 CLI/api5 H2 路径纳入本 change（除非后续单独开 change）。
- 不强制删除用户已依赖的本地个性化能力——改为「官方基线 + 可选 overlay」。

## Scope

### In scope

1. System prompt 基线与 overlay 分层（`prompt/` + `ReadPrompt`）。
2. `prompt_context_usage_tree` 填充与 Context Explorer 可用性。
3. Tool/prompt 截断策略对照官方 fair-allocation / omit 行为（或明确兼容映射）。
4. Compaction 摘要提示词与保留策略对照。
5. 抓包对照测试 / 文档化验收清单。

### Out of scope

- Mixed-model upstream 路由本身。
- 非 Agent 路径（Tab / CMDK）prompt。

## Proposed Approach（摘要）

分四阶段，详见 `design.md` / `tasks.md`：

1. **基线审计**：固定官方/本地对照矩阵（本提案固化）。
2. **Prompt 对齐**：官方英文（或官方同构）system 直接写入各模式 `prompt.md`。
3. **计量与 Explorer**：补齐 `prompt_context_usage_tree`；breakdown 归因继续贴官方字符/分类规则。
4. **截断与压缩**：引入与官方可对照的 prompt budget 截断（或证明 per-tool limit 的等价边界）；压缩摘要 prompt/保留 tail 策略对齐。

## Success Criteria

- 同一会话步骤下，本地 checkpoint 的 `breakdown.categories[].id/label` 集合与官方一致；`prompt_context_usage_tree` 非空且 UI View Report 可用。
- 关闭 overlay 时，system 开场与官方模板同构（允许模型名占位），且 `system_prompt` 桶体量与官方同量级（KB 级而非「个性化长文」）。
- 大 Read/Shell 不会在未压缩时直接打满窗口；模型侧可见截断提示与官方语义一致（或文档化映射）。
- 500k 窗口在 ~90% 触发压缩；256k 同理；有抓包/history 证据。

## Risks

- 去掉或弱化 `common_prefix` 会改变用户已习惯的本地模型行为 → 必须可开关并默认迁移策略写清。
- Prompt 级 fair truncation 可能影响 prefix-cache 稳定性 → 需遵守 `.agents/skills/prefix-cache-stability`。
- 官方模板随 Cursor 版本漂移 → 需要「从抓包/agent-exec 刷新基线」的维护流程，而不是一次性硬编码后不管。
