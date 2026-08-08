## ADDED Requirements

### Requirement: Debugger MITMs all Cursor relay hosts
The menu-bar / standalone protocol debugger MUST decrypt and capture HTTPS traffic for the same host whitelist used by the desktop MITM (`api2.cursor.sh`, `api3.cursor.sh`, and `*.cursor.sh`), not only a single hard-coded `api2.cursor.sh`.

#### Scenario: api3 or other cursor.sh host appears in the panel
- **WHEN** Cursor opens a TLS connection to a whitelisted `*.cursor.sh` host through the debug proxy
- **THEN** the debugger MUST MITM that host and list the exchange with its Host field

### Requirement: Panel shows client and upstream hops
When the debugger runs in-process with the local assistant (menu-bar debug mode + local mode), upstream forwards performed by the backend MUST appear in the same exchange list, distinguished from client→local captures.

#### Scenario: ForwardToUpstream is visible
- **WHEN** mixed routing forwards a request to official Cursor and a traffic capture sink is registered
- **THEN** the debug panel MUST show an exchange with `captureSource=upstream` for that hop
- **THEN** client MITM captures for the same logical request MUST remain listed with `captureSource=client`
