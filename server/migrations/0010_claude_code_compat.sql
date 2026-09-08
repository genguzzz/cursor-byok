-- Add a model-level opt-in for the Claude Code CLI compatibility path used by
-- the tclaude daemon. When enabled for an Anthropic model, the provider injects
-- the tclaude/claude-code aligned headers (Authorization: Bearer, anthropic-beta,
-- X-Stainless-*, session id) and resolves the `tclaude-daemon` hostname against
-- ~/.tclaude/daemon.json.
ALTER TABLE model_configs ADD COLUMN claude_code_compat INTEGER NOT NULL DEFAULT 0 CHECK(claude_code_compat IN (0, 1));
