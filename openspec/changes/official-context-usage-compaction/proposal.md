# Proposal: Align context usage breakdown and compaction threshold with official Cursor

## Status

In progress

## Problem Summary

1. Local `conversation_checkpoint_update` → `token_details.breakdown` only emits coarse categories (`system_prompt` / `tools` / `conversation`), so the Context Usage tray cannot show Rules / Skills / MCP / Subagents like official cloud.
2. Auto compaction uses a fixed 10k reserve against `contextWindowTokens`. On a 500k DeepSeek channel this only triggers near 490k, so users rarely see compaction.

## Evidence

- Official `run_request.conversation_state.token_details.breakdown` includes eight categories with fixed ids/labels and `character_count`.
- Official agent-exec self-summary default: `usedTokens >= Math.floor(0.9 * maxTokens)`.

## Scope

- Emit official-shaped `PromptTokenBreakdownSnapshot` from checkpoint rewrite.
- Resolve compaction reserve as `max(10k, window - floor(0.9 * window))`.
- Unit tests for category attribution and reserve math.

## Non-goals

- Full `prompt_context_usage_tree` explorer parity (optional follow-up).
- Changing provider usage accounting.
