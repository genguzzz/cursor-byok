# 收尾对照：官方 server vs 本地 localserver（2026-08-09）

## 路径说明（先读这个）

当前调试模式抓到的是 **官方模型** 会话（`request_id=b028c96f-…`）：

- 客户端 → 本地 MITM `:18080` → **passthrough 回源官方**
- **不会**走本地 `ReadPrompt` / `promptbudget` / 本地 compaction 摘要生成
- 因此「官方模型流量」只能验收 **协议字段形状**（checkpoint / token_details），不能用来验收本地 system 正文

本地策略验收必须看 **本地渠道模型**（本机证据：`8405650e-…` / DeepSeek，`request_id=e16bbace-…`，install 后 `06:13Z`）。

Debugger 环形缓冲只有约 200 条；本轮官方 `RunSSE` 已滚出窗口，官方计量用同日 fixture + 此前抓包。

## 对照表

| 策略面 | 官方（云端 / fixture） | 本地 DeepSeek（install 后） | 结论 |
|--------|------------------------|-----------------------------|------|
| System 开场 | 云端组装（正文不在 Bidi 明文） | `You are a powerful agentic…`；**无**「极度务实」 | **对齐**（overlay 默认关） |
| System 桶体量 | `477` tok / `1877` chars | `538` tok / `1981` chars | **接近**（启发式估算，约 +5% chars） |
| breakdown 八类 id/label | 有 | 有 | **对齐** |
| `prompt_context_usage_tree` | Explorer 使用（schema=1，category + 子节点） | schema=`1`，54 nodes：8 category + tool_definition/rule/subagent… | **基本对齐**（本地尚无完整 message 级子树） |
| 压缩触发 | `used >= floor(0.9*max)` | 同（500k → reserve 50k） | **对齐**（代码级；本轮未手测逼近 90%） |
| Tool 防爆 | prompt 级 fair `V=3.2e6` + omit | per-tool replay + `promptbudget` fair | **策略已接**（本轮官方会话未见 omit 文案） |
| 动态段 rules/skills/mcp | 有独立桶 | 有（本会话 skills=0 属内容空，非缺字段） | **字段对齐** |
| 官方模型下本地 prompt 是否生效 | n/a | **不生效（passthrough）** | 预期行为，不是回归 |

## 本地证据摘要（DeepSeek）

- Provider system（`06:13:36Z`）：1975 chars，official opener，overlay=off  
- Checkpoint：`system_prompt=538/1981`，`max_tokens=500000`，tree `schema_version=1` nodes=54  
- 会话：`history/8405650e-be6a-4493-b6ae-a7723c06da2c`

## 官方证据摘要

- Fixture：`fixtures/official-token-details.json`（`system_prompt=477/1877`，`max=256000`）  
- 本轮官方 rid `b028c96f-…` 仍有 upstream Bidi heartbeat/exec，但 RunSSE 已出缓冲，未能再次导出完整 checkpoint

## 仍不一致 / 未验收

1. 官方 usage tree 可能含更多 kind（`skill` / `mcp_block` / `*_message`）；本地目前是 category + tools/rules/subagents 子节点。  
2. Compaction 摘要合同 / PreCompact 调试字段 / 90% 手测未做。  
3. 官方 tokenizer vs 本地启发式仍有误差（system 桶 477 vs 538）。  
4. 本轮官方 live RunSSE 未留在 debugger 缓冲，建议下次对照时立刻导出该 `RunSSE` id。
