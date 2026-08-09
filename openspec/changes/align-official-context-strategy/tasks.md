# Tasks: Align official context strategy

## 0. Baseline freeze

- [x] 0.1 固化对照矩阵到 `design.md`（协议 / prompt / 截断 / 压缩）并用最新抓包各附 1 条证据路径
- [x] 0.2 增加「官方对齐模式」验收清单（见 `acceptance-compare-2026-08-09.md`）

## 1. System prompt alignment

- [x] 1.1 从官方 **抓包计量契约**（system_prompt ≈ 477 tok / 1877 chars）整理各模式短英文 system，直接写入 `prompt/{agent,ask,plan,debug,multitask}/prompt.md`
- [x] 1.2 `ReadPrompt` 只读 `prompt/<mode>/prompt.md`（无 overlay / 无独立 official_baseline 目录）
- [x] 1.3 模式约束走既有 reminder 组装（Ask AF / Plan system_reminder / Debug system_reminder_*）
- [x] 1.4 单测：overlay off 时 system 不含「极度务实」；on 时包含（`prompt/embed_test.go`）
- [x] 1.5 抓包验收：overlay off 时 `breakdown.system_prompt` 体量接近官方量级（本地 1981 vs 官方 1877 chars；见 acceptance-compare）

## 2. Metering & Explorer

- [x] 2.1 实现 `prompt_context_usage_tree` 构建（category 父节点 + skills/rules/mcp/tools 子节点）
- [x] 2.2 `rewriteCheckpointTokenDetailsForClient` 同时写 breakdown + tree
- [x] 2.3 单测：八类 id/label/`character_count`；tree `schema_version` 与节点 `category_id` 合法（已写；勿对整个 forwarder 包做全量反复编译拖垮 gopls）
- [ ] 2.4 UI 手测：Context Usage 八类 + View Report 可打开

## 3. Truncation / prompt budget

- [x] 3.1 保留 per-tool replay limit
- [x] 3.2 增加 compiled prompt 级 budget trim（官方 max-min fair，`V=3.2e6`），保护 system；实现于独立小包 `internal/backend/promptbudget`
- [x] 3.3 统一截断文案映射（官方 omit / `[... truncated, N chars]` + 本地 per-message `[truncated: ...]`）
- [x] 3.4 单测：多条超大 tool_result 时 compiled 总字符有上界（`promptbudget` 包，避免编译 aiserver）
- [x] 3.5 确认截断只发生在投影层（`guardCompiledConversationForProvider`），不改 `context.json`

## 4. Compaction parity

- [ ] 4.1 确认/文档化 0.9 触发与官方一致（已实现则补回归测试）
- [ ] 4.2 对照并必要时改写 `prompt/compaction/prompt.md` 合同条款
- [ ] 4.3 compaction_request 调试字段对齐 PreCompact query（window/used/percent/reserve）
- [ ] 4.4 手测：逼近 90% 时出现 SummaryStarted/Completed，且后续 breakdown 含 `summarized_conversation`

## 5. Verification & docs

- [x] 5.1 用 `cursor-proxy-debugger` 跑一遍官方 vs 本地对照清单并附样本 id（官方 rid `b028c96f-…`；本地 DeepSeek `e16bbace-…` / conv `8405650e-…`）
- [ ] 5.2 更新 coding-guidance / 简短用户说明：官方对齐模式与 overlay 含义
- [ ] 5.3 Archive 前确认 Non-goals 未被突破（未 patch Cursor.app）
