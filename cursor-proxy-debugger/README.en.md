# Cursor Protocol Debugger

[中文](README.md) | [English](README.en.md)

This standalone local HTTPS debugging proxy captures Cursor's `BidiAppend`, `RunSSE`, and Fork Chat traffic. It does not modify Cursor, the system proxy, or the installed client.

## Start

Run the following command from the repository root:

```bash
go run ./cmd/cursor-proxy-debugger
```

Default addresses:

- HTTP/HTTPS proxy: `127.0.0.1:9090`
- Debugging UI: `http://127.0.0.1:9091`
- MITM target: `*.cursor.sh` (multi-server, same whitelist as desktop MITM)

The debugging UI opens automatically after startup.

## Configure Cursor

The tool does not modify Cursor automatically. After starting it, configure Cursor manually:

1. Open Cursor's proxy settings and set the proxy to the address printed by the tool. The default is `http://127.0.0.1:9090`.
2. Open Cursor's Network settings and enable HTTP/1.1.
3. Download the proxy CA certificate from `http://127.0.0.1:9091/api/ca.crt` and make sure Cursor trusts it.

Restore the original Cursor proxy and Network settings after debugging to avoid affecting normal network requests.

## Build

```bash
go build -o bin/cursor-proxy-debugger ./cmd/cursor-proxy-debugger
```

## Options

```text
-proxy-addr         Proxy listen address; default: 127.0.0.1:9092
-ui-addr            Debugging UI listen address; default: 127.0.0.1:9091
-target-host        Hosts to decrypt/capture; default: *.cursor.sh (comma-separated or wildcard)
-upstream-proxy     Optional upstream proxy, e.g. local-mode http://127.0.0.1:18080
-max-store-bytes    Capture memory budget in bytes; default: 209715200 (200MiB); evict oldest when exceeded
-max-exchanges      Optional count cap; 0 (default) means budget-only
-open               Open the browser after startup; default: true
```

## Query API

```text
GET /api/status
GET /api/exchanges?limit=2000
GET /api/exchanges/query
  Filters include server/path/kind/requestId, q (metadata-only by default),
  qBody=1 to scan decodedJson (64KiB cap), limit<=200, include=summary|decoded|raw|frames|full
GET /api/exchanges/{id}?include=full
GET /api/exchanges/{id}/raw?side=request|response&format=hex|bin|json
```

## Data Handling

- HTTPS MITM is applied only to `target-host`; other CONNECT traffic passes through unchanged.
- `RunSSE` is decoded incrementally using the 5-byte Connect frame header; SSE publishes are batched to avoid per-frame UI/store thrash.
- `BidiAppend` is unary (not a Connect stream): body lands in `decodedJson`, plus one synthetic `request.frames` entry so tools do not report "0 frames / undecoded". Connect unary envelopes are unwrapped when needed.
- `BidiAppendRequest.data` is further decoded as `agent.v1.AgentClientMessage`.
- Fork Chat's `ForkBackgroundComposer`, `NotifyConversationClone`, and `UploadConversationBlobs` traffic is decoded bidirectionally as protobuf JSON.
- Local Fork Chat is primarily client-side and only emits `NotifyConversationClone` and `UploadConversationBlobs` when clone blob synchronization is enabled and privacy settings allow it.
- Requests can be sorted chronologically or in reverse chronological order and filtered by protocol `request_id`.
- The UI supports Simplified Chinese and English, follows the browser language, and remembers a manual selection.
- Captured traffic is stored only in process memory (default ~200MiB budget; oldest evicted first) and is discarded when the process exits.
- Sensitive HTTP headers such as `Authorization`, `Cookie`, and `Set-Cookie` are hidden in the UI by default.
- Raw bodies are retained up to 16 MiB per side by default; forwarded traffic is never truncated.
- Successful Connect frames omit `rawHex` to save memory; query body search is opt-in (`qBody=1`).
- `RunSSE`: local MITM decodes frames incrementally; official upstream hops are offline-decoded the same way after ingest so both sides expose `response.frames`.
