# Design: Official context usage + compaction threshold

## Protocol

Checkpoint carries:

```text
ConversationStateStructure.token_details
  used_tokens / max_tokens
  breakdown: PromptTokenBreakdownSnapshot
    total_used_tokens / max_tokens
    categories[]: id, label, estimated_tokens, character_count?
```

Official category order and labels (from captured traffic):

| id | label |
|----|-------|
| system_prompt | System prompt |
| tools | Tool definitions |
| rules | Rules |
| skills | Skills |
| mcp | MCP & dynamic tools |
| subagents | Subagent definitions |
| summarized_conversation | Summarized conversation |
| conversation | Conversation |

Empty categories are still present (`estimated_tokens` may omit as proto3 zero; `character_count` can be 0).

## Attribution algorithm

From compiled provider prompt:

1. Tool descriptors JSON:
   - MCP tool names (`CallMcpTool`, `FetchMcpResource`, `ListMcpResources`, name contains `mcp`) → `mcp`
   - `Task` tool: text from `Available subagent_types` → `subagents`; remainder → `tools`
   - other tools → `tools`
2. Message bodies: peel XML sections
   - `<rules>...</rules>` → `rules`
   - `<agent_skills>...</agent_skills>` → `skills`
   - `<mcp_file_system>...</mcp_file_system>` → `mcp`
   - conversation summary markers → `summarized_conversation`
   - leftover `system` role → `system_prompt`
   - leftover other roles → `conversation`
3. `character_count` = Unicode rune count of attributed text; tokens via existing estimator.
4. `total_used_tokens` = max(provider/conversation used, sum of categories).

## Compaction threshold

Official self-summary default: trigger at `floor(0.9 * maxTokens)`.

Local mapping:

```text
reserve = max(10000, window - floor(0.9 * window))
budget  = window - reserve
trigger when used > budget
```

For 500k → reserve 50k → trigger ~450k (not 490k). Note: user-proposed 20% reserve is **more aggressive** than official 10%; we follow official 0.9.
