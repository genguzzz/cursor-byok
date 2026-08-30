# Design: v0.0.49 CodeBuddy Model Import

## Context

The old Go version (`v0.0.49`) writes `type: codebuddy` into
`modelAdapters` in `~/.cursor-local-assistant-v2/config.yaml`. The Rust
rewrite's import path (`Store::import_v0049_model_config` →
`preview_v0049_model_config` → `load_v0049_model_config` →
`model_input`) is the single entry point that must understand that type.

## Parser change

`model_input` currently:

```rust
match model.model_type.trim().to_ascii_lowercase().as_str() {
    "openai" => ModelType::OpenAi,
    "anthropic" => ModelType::Anthropic,
    value => return Err(...unsupported...),
}
```

Add `"codebuddy" => ModelType::CodeBuddy`. The downstream
`legacy_request_configuration` already contains a `ModelType::CodeBuddy`
branch that mirrors v0.0.49's written shape (`/custom` endpoint,
versioned base URL with `/chat/completions` appended), so no other import
logic changes.

## Schema change

`model_configs.model_type` is constrained by a CHECK in
`0004_flatten_model_configuration.sql`:

```sql
model_type TEXT NOT NULL CHECK(model_type IN ('openai', 'anthropic'))
```

SQLite cannot alter a CHECK constraint in place, so migration
`0005_allow_codebuddy_model_type.sql` recreates the table:

1. `CREATE TABLE model_configs_new (...)` with the same columns and
   `CHECK(model_type IN ('openai','anthropic','codebuddy'))`.
2. Copy all rows (`INSERT INTO ... SELECT ...`).
3. `DROP TABLE model_configs` then `ALTER TABLE model_configs_new RENAME TO
   model_configs`.
4. Recreate `model_configs_sort` and `model_configs_created` indexes plus the
   `llm_calls.model_hash` foreign key (the FK is preserved by table rename
   only if re-added explicitly; use `PRAGMA foreign_keys=OFF` around the
   rebuild, matching migration 0004's `defer_foreign_keys` pattern).

Existing rows are all `openai`/`anthropic`, so the copy is safe and the
relaxed CHECK cannot reject historical data.

## Tests

- `legacy_config.rs`: `manually_imports_v0049_codebuddy_models` — writes a
  realistic v0.0.49 CodeBuddy adapter, previews, imports, and asserts the
  stored `ModelType::CodeBuddy`, normalized `request_url` at
  `https://copilot.tencent.com/v2/chat/completions`, and extra params.

## Out of scope

- No API or UI changes: the editor, cards, discovery, provider, and catalog
  already handle `codebuddy`.