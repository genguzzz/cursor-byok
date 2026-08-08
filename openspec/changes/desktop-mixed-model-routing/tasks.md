## 1. Config and identity

- [x] 1.1 Add `features.mixedModelRouting.enabled` (default true) to config types/normalize/tests
- [x] 1.2 Skip fake `InjectCursorUserInfo` when mixed routing is enabled; still apply proxy/CA/HTTP1 settings
- [x] 1.3 Add in-place Statsig gate patch that does not rewrite auth tokens; cover with unit tests
- [x] 1.4 Add `ForwardOptions.PreserveClientAuth` and skip `LocalRelayToken` rewrite; unit test header behavior

## 2. Model catalog merge

- [x] 2.1 Implement upstream fetch + proto merge for `AvailableModels` (preserve upstream feature configs, append channel entries, skip name collisions)
- [x] 2.2 Fall back to injected-only payload when upstream fetch/decode fails; unit tests for merge/fallback/collision
- [x] 2.3 Merge `GetUsableModels`; keep injected channel IDs on default-model nudge `modelsWithNoDefaultSwitch`
- [x] 2.4 Wire mixed catalog actions in `host.go`; keep legacy mock builders when kill switch is off

## 3. Agent request routing

- [x] 3.1 Implement Connect/`application/proto` peek decoder for `BidiAppendRequest` + `extractRequestedModelID`
- [x] 3.2 Implement `request_id` affinity table with channel-id classifier, local-history fallback, and 2s RunSSE wait
- [x] 3.3 Dispatch local forwarder vs raw upstream forward without rewriting bodies; do not wrap upstream RunSSE with local SSE header helper
- [x] 3.4 Unit tests: channel hash → local, official name → upstream, sticky exec follow-up, wait timeout defaults upstream

## 4. Default passthrough and host wiring

- [x] 4.1 When mixed routing is on, change unmatched AiService/Cpp/FileSync/Dashboard/auth catch-alls from 404 to client-auth forward
- [x] 4.2 Keep `GetServerConfig` HTTP/2 force-disable and Statsig mocks in mixed mode
- [x] 4.3 Pass mixed-routing flag from config loader into host rebuild so kill switch hot-reloads with config

## 5. Verify

- [x] 5.1 Run targeted unit tests for config, upstream auth, catalog merge, and agent router
- [x] 5.2 Run a broader `go test` slice for `internal/backend/...` and `internal/client/...` / `internal/cursor/...` and fix regressions
