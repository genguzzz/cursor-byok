# Proposal: H2 TLS Intercept Proxy for Cursor Agent API Capture

## Protocol Confirmed

`agentn.global.api5.cursor.sh:443` responds to `openssl s_client -alpn h2` with `ALPN protocol: h2` — this is **TLS + h2**, not h2c prior knowledge. The "prior knowledge" mentioned in community docs refers to HTTP/2 happening without HTTP/1.1 Upgrade, which is standard behavior for a known H2 endpoint (no Upgrade: h2c header).

## Problem

Cursor CLI model inference traffic goes to `agentn.global.api5.cursor.sh` via TLS + h2. Existing MITM tools (mitmproxy 12.x, Surge) fail to proxy H2 connections when the client opens them as pure H2 (no HTTP/1.1 upgrade). Additionally, Cloudflare likely rejects or rate-limits connections that show MITM fingerprints.

## Solution

Go-based TLS-intercepting H2 proxy using `net/http` + `golang.org/x/net/http2`:

1. Listen on `:8443`, present a **dynamic TLS certificate** for SNI `agentn.global.api5.cursor.sh` signed by mitmproxy CA
2. Agent CLI connects with hidden flag `--agent-endpoint https://127.0.0.1:8443` (still present on `2026.08.04-aaa8809`; `-k` skips leaf verify). Optional `NODE_EXTRA_CA_CERTS` for the MITM CA.
3. After TLS termination, `http2.Server` handles H2 frames
4. Upstream forwarded via `http2.Transport` with TLS to real api5
5. All request/response bodies saved as binary (not JSON-encoded) for later protobuf decoding

## Scope

- Single-direction intercept for `agentn.global.api5.cursor.sh` only
- Capture-only: no modification or replay
- Reuses existing mitmproxy CA certificate and private key

## Non-goals

- No gRPC/Connect-RPC decoding (save raw binary)
- No replacement for mitmproxy's api2 capture
- No HTTP/3 support