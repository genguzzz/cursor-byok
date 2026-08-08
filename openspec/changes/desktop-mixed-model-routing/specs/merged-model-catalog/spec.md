## ADDED Requirements

### Requirement: AvailableModels merges upstream and injected channels
When mixed model routing is enabled, `AiService/AvailableModels` MUST return Cursor's upstream model list plus configured local channel adapters.

#### Scenario: Successful merge
- **WHEN** upstream AvailableModels succeeds and at least one valid model adapter is configured
- **THEN** the response `models` list MUST contain every upstream model name and every adapter channel ID
- **THEN** injected entries MUST use the channel ID as `name` and `serverModelName`
- **THEN** upstream composer/cmdK/default model config fields MUST be preserved from the upstream payload

#### Scenario: Channel ID collision
- **WHEN** an injected channel ID equals an upstream model `name`
- **THEN** the system MUST keep the upstream entry and MUST NOT append a duplicate injected name

#### Scenario: Upstream failure falls back to injected-only catalog
- **WHEN** the upstream AvailableModels call fails
- **THEN** the system MUST return the legacy injected-only AvailableModels payload so BYOK remains usable

### Requirement: Usable models catalog also merges
When mixed model routing is enabled, `AiService/GetUsableModels` MUST expose injected channel IDs in addition to any upstream usable models.

#### Scenario: Usable models include injected channels
- **WHEN** mixed routing is enabled and adapters are configured
- **THEN** `GetUsableModels` MUST include each adapter channel ID as a usable model id

### Requirement: Default-model nudge does not evict injected channels
When mixed routing is enabled, injected channel IDs MUST be listed in `GetDefaultModelNudgeData.modelsWithNoDefaultSwitch` if that RPC is served locally or merged.

#### Scenario: Injected models are not auto-upgraded away
- **WHEN** the client requests default-model nudge data
- **THEN** every configured adapter channel ID MUST appear in `modelsWithNoDefaultSwitch`

### Requirement: Kill switch restores replace-not-merge catalog
When mixed model routing is disabled, AvailableModels MUST keep the legacy injected-only replacement payload.

#### Scenario: Legacy catalog
- **WHEN** mixed routing is disabled
- **THEN** AvailableModels MUST NOT require a successful upstream fetch
- **THEN** the returned `models` MUST contain only configured adapters
