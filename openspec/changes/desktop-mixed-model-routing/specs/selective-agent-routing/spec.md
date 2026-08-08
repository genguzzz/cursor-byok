## ADDED Requirements

### Requirement: Injected model agent traffic stays local
When mixed model routing is enabled and a `BidiAppend` `run_request` or `prewarm_request` names a configured adapter channel ID, the backend MUST handle that `request_id` with the local forwarder.

#### Scenario: Channel hash routes local
- **WHEN** the requested model id equals a configured adapter channel ID (or its thinking-effort variant prefix)
- **THEN** subsequent `BidiAppend` messages and `RunSSE` for the same `request_id` MUST be served by the local forwarder
- **THEN** the original request body MUST be passed to the local handler without requiring the client to change protocol

### Requirement: Cursor native model agent traffic goes upstream
When mixed model routing is enabled and the requested model id is not a configured channel ID, the backend MUST forward `BidiAppend` and `RunSSE` for that `request_id` to the original Cursor upstream URL with the client Authorization.

#### Scenario: Official model name routes upstream
- **WHEN** the requested model id is a non-empty value that is not a configured channel ID
- **THEN** the backend MUST forward the raw request to `api2.cursor.sh` (or the MITM-provided upstream URL)
- **THEN** it MUST NOT wrap the upstream `RunSSE` response with the local `text/event-stream` header override unless the upstream itself uses that content type

### Requirement: Request id affinity is sticky
After a `request_id` is classified, later messages that lack a model id MUST follow the same backend.

#### Scenario: Tool result follows original backend
- **WHEN** a later `BidiAppend` for the same `request_id` contains only `exec_client_message` or `interaction_response`
- **THEN** the backend MUST route it to the previously chosen local or upstream backend

#### Scenario: RunSSE waits briefly for classification
- **WHEN** `RunSSE` arrives before any classifying `BidiAppend` for that `request_id`
- **THEN** the backend MUST wait up to 2 seconds for classification
- **THEN** if still unclassified it MUST default to upstream

### Requirement: Unclassified empty model defaults safely
When mixed routing is enabled and a new `request_id` has no model id, the backend MUST prefer an existing local conversation if the conversation id is already in local history; otherwise it MUST route upstream.

#### Scenario: Resume local conversation without model id
- **WHEN** the inbound message has a conversation id that exists under `history/<conversationId>/`
- **THEN** the backend MUST classify the `request_id` as local

#### Scenario: Unknown conversation without model id
- **WHEN** the inbound message has no model id and no local history conversation
- **THEN** the backend MUST classify the `request_id` as upstream

### Requirement: Unknown desktop RPCs pass through
When mixed model routing is enabled, unmatched `AiService`, `CmdKService`, `ChatService`, `BackgroundComposerService`, `CppService`, `FileSyncService`, `DashboardService`, and `/auth/*` routes that are not explicitly local MUST forward upstream instead of returning 404.

#### Scenario: Unknown AiService procedure
- **WHEN** the client calls an `aiserver.v1.AiService` procedure that has no dedicated local handler
- **THEN** the backend MUST forward the request upstream with client auth

#### Scenario: NameTab NameAgent respect tabRenamer opt-in
- **WHEN** mixed model routing is enabled and `features.tabRenamer.enabled` is false
- **THEN** `aiserver.v1.AiService/NameTab` and `NameAgent` MUST forward upstream with client auth
- **WHEN** mixed model routing is enabled and `features.tabRenamer.enabled` is true
- **THEN** those procedures MUST be handled by the local tab renamer

#### Scenario: CmdKService StreamCmdK
- **WHEN** the client calls `aiserver.v1.CmdKService/StreamCmdK`
- **THEN** the backend MUST forward the request upstream with client auth instead of returning 404

### Requirement: Cross-backend history is not merged
The local history store MUST remain the source of truth only for locally routed conversations. Upstream Cursor conversations MUST NOT be written into `state.json` / `context.json` as if they were local runs.

#### Scenario: Upstream run does not create local provider replay
- **WHEN** a request_id is routed upstream
- **THEN** the local forwarder MUST NOT start a provider pass or append provider replay entries for that request
