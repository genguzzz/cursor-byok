## ADDED Requirements

### Requirement: CmdKService forwards under mixed routing
When mixed model routing is enabled, unmatched `aiserver.v1.CmdKService` procedures MUST forward upstream with the client Authorization instead of returning HTTP 404.

#### Scenario: StreamCmdK reaches official Cursor
- **WHEN** the desktop client calls `aiserver.v1.CmdKService/StreamCmdK` while mixed routing is enabled
- **THEN** the backend MUST forward the raw request to the MITM-provided upstream URL (typically `api2.cursor.sh`) with `PreserveClientAuth`
- **THEN** it MUST NOT return a local 404 / Connect Unimplemented for that procedure

#### Scenario: Mixed routing disabled keeps local-only behavior
- **WHEN** mixed model routing is disabled
- **THEN** unmatched `CmdKService` procedures MAY return 404 as in the legacy full-local mock mode

### Requirement: Related official UX services also pass through
When mixed model routing is enabled, unmatched `ChatService` and `BackgroundComposerService` routes MUST likewise forward upstream with client auth.

#### Scenario: Unknown ChatService procedure
- **WHEN** the client calls an `aiserver.v1.ChatService` procedure with no dedicated local handler
- **THEN** the backend MUST forward upstream with client auth
