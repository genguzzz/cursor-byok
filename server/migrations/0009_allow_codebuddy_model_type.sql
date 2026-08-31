-- Relax model_configs.model_type CHECK to admit CodeBuddy models imported
-- from the old Go version's config.yaml (which writes type: codebuddy).
-- Preserves the group_name column added by 0008.
PRAGMA foreign_keys = OFF;

CREATE TABLE model_configs_new (
    model_hash TEXT PRIMARY KEY,
    sort_order INTEGER NOT NULL DEFAULT 0,
    display_name TEXT NOT NULL,
    model_type TEXT NOT NULL CHECK(model_type IN ('openai', 'anthropic', 'codebuddy')),
    base_url TEXT NOT NULL,
    use_full_url INTEGER NOT NULL DEFAULT 0 CHECK(use_full_url IN (0, 1)),
    api_key TEXT NOT NULL,
    tooltip_data TEXT NOT NULL,
    model_id TEXT NOT NULL,
    reasoning_effort TEXT,
    openai_endpoint TEXT NOT NULL DEFAULT '',
    openai_extra_params_enabled INTEGER NOT NULL DEFAULT 0 CHECK(openai_extra_params_enabled IN (0, 1)),
    openai_extra_params_json TEXT NOT NULL DEFAULT '{}',
    custom_headers_enabled INTEGER NOT NULL DEFAULT 0 CHECK(custom_headers_enabled IN (0, 1)),
    custom_headers_json TEXT NOT NULL DEFAULT '{}',
    anthropic_extra_params_enabled INTEGER NOT NULL DEFAULT 0 CHECK(anthropic_extra_params_enabled IN (0, 1)),
    anthropic_extra_params_json TEXT NOT NULL DEFAULT '{}',
    group_name TEXT,
    context_window_tokens INTEGER,
    max_completion_tokens INTEGER,
    anthropic_max_tokens INTEGER,
    anthropic_thinking_effort TEXT,
    thinking_budget_tokens INTEGER,
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL
);

INSERT INTO model_configs_new (
    model_hash,
    sort_order,
    display_name,
    model_type,
    base_url,
    use_full_url,
    api_key,
    tooltip_data,
    model_id,
    reasoning_effort,
    openai_endpoint,
    openai_extra_params_enabled,
    openai_extra_params_json,
    custom_headers_enabled,
    custom_headers_json,
    anthropic_extra_params_enabled,
    anthropic_extra_params_json,
    group_name,
    context_window_tokens,
    max_completion_tokens,
    anthropic_max_tokens,
    anthropic_thinking_effort,
    thinking_budget_tokens,
    created_at_ms,
    updated_at_ms
)
SELECT
    model_hash,
    sort_order,
    display_name,
    model_type,
    base_url,
    use_full_url,
    api_key,
    tooltip_data,
    model_id,
    reasoning_effort,
    openai_endpoint,
    openai_extra_params_enabled,
    openai_extra_params_json,
    custom_headers_enabled,
    custom_headers_json,
    anthropic_extra_params_enabled,
    anthropic_extra_params_json,
    group_name,
    context_window_tokens,
    max_completion_tokens,
    anthropic_max_tokens,
    anthropic_thinking_effort,
    thinking_budget_tokens,
    created_at_ms,
    updated_at_ms
FROM model_configs;

DROP TABLE model_configs;
ALTER TABLE model_configs_new RENAME TO model_configs;

CREATE INDEX model_configs_sort ON model_configs(sort_order, display_name);
CREATE INDEX model_configs_created ON model_configs(created_at_ms DESC);

PRAGMA foreign_keys = ON;
