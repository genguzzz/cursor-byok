# Allow v0.0.49 CodeBuddy Model Import

## Problem

Importing models from the old Go version's `config.yaml` (`~/.cursor-local-assistant-v2/config.yaml`) fails with:

```
configuration error: unsupported v0.0.49 model type: codebuddy
```

The Rust rewrite already supports CodeBuddy end to end (`ModelType::CodeBuddy`,
`ProviderType::CodeBuddy`, `provider/codebuddy.rs`, model discovery, UI editor,
model catalog), but two gaps block old-version imports:

1. `server/src/store/legacy_config.rs::model_input` only maps `openai` /
   `anthropic` when parsing the legacy `type` field, so `codebuddy` falls into
   the "unsupported v0.0.49 model type" error even though
   `legacy_request_configuration` already handles `ModelType::CodeBuddy`.
2. `server/migrations/0004_flatten_model_configuration.sql` creates
   `model_configs` with `CHECK(model_type IN ('openai','anthropic'))`, so the
   database rejects any `codebuddy` row regardless of the import parser.

## Solution

1. Map `"codebuddy"` → `ModelType::CodeBuddy` in `model_input`.
2. Add migration `0005_allow_codebuddy_model_type.sql` that rebuilds the
   `model_configs` table without the `codebuddy`-exclusion check, preserving
   all existing rows, indexes, and foreign-key wiring.
3. Add import + round-trip tests for a realistic v0.0.49 CodeBuddy adapter.

## Scope

- `server/src/store/legacy_config.rs`
- `server/migrations/0005_allow_codebuddy_model_type.sql` (new)
- Tests only, no API or UI shape changes.