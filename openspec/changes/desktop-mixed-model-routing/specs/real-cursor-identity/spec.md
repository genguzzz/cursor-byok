## ADDED Requirements

### Requirement: Mixed mode keeps real Cursor login
When `features.mixedModelRouting.enabled` is true, starting the desktop local assistant MUST NOT replace `cursorAuth/accessToken`, `cursorAuth/refreshToken`, or cached email/membership in Cursor `state.vscdb` with the fake local Ultra identity.

#### Scenario: Start with mixed routing enabled
- **WHEN** the user starts the local assistant and mixed model routing is enabled
- **THEN** the process MUST NOT call the fake-account injector that writes `InjectAuthToken`
- **THEN** Cursor settings MUST still receive the local MITM proxy URL, CA trust, and `cursor.general.disableHttp2=true`

#### Scenario: Kill switch restores legacy identity rewrite
- **WHEN** `features.mixedModelRouting.enabled` is false
- **THEN** start-up MAY keep the legacy fake Ultra identity injection behavior

### Requirement: Upstream forwards preserve client Authorization
When mixed model routing is enabled, backend forwards to `*.cursor.sh` MUST use the Authorization header supplied by the Cursor client and MUST NOT overwrite it with `LocalRelayToken`.

#### Scenario: Native agent passthrough auth
- **WHEN** a request is forwarded upstream under mixed routing
- **THEN** the outbound `Authorization` header MUST match the inbound client header (after hop-by-hop filtering only)

#### Scenario: Legacy mode may still rewrite auth
- **WHEN** mixed model routing is disabled
- **THEN** the legacy `shouldRewriteHost` LocalRelayToken rewrite MAY still apply

### Requirement: Billing and auth control plane stay honest
When mixed model routing is enabled, `/auth/*` stripe/session endpoints and dashboard usage/plan RPCs that are not explicitly local-only MUST be forwarded to Cursor with the client session rather than mocked as unlimited Ultra.

#### Scenario: Stripe profile is real
- **WHEN** mixed routing is enabled and the client requests `/auth/stripe_profile` or `/auth/full_stripe_profile`
- **THEN** the backend MUST forward the request upstream instead of returning the local Ultra mock

### Requirement: HTTP/2 remains forced off for MITM
Regardless of mixed routing, `GetServerConfig` MUST continue to report `HTTP2_CONFIG_FORCE_ALL_DISABLED`, and desktop settings injection MUST continue to set `cursor.general.disableHttp2`.

#### Scenario: Server config still disables HTTP/2
- **WHEN** the client calls `AiService/GetServerConfig` or `ServerConfigService/GetServerConfig` while the assistant is running
- **THEN** the response `http2Config` MUST be `HTTP2_CONFIG_FORCE_ALL_DISABLED`

### Requirement: Always-local stability gates stay disabled
When mixed routing is enabled, the assistant MUST still ensure `decompose_always_local_ext_host` is disabled without replacing the user's auth tokens.

#### Scenario: Statsig gate patched without fake login
- **WHEN** mixed routing starts
- **THEN** the system MUST keep `decompose_always_local_ext_host` disabled via Statsig mock and/or an in-place vscdb gate patch
- **THEN** the system MUST NOT require rewriting access/refresh tokens to achieve that gate patch
