# Design: H2 TLS Intercept Proxy

## Confirmed Architecture (TLS + h2, not h2c)

```
Cursor CLI
  │  --agent-endpoint https://127.0.0.1:8443   (hidden flag, still present)
  │  -k / --insecure                           (hidden; skip leaf verify)
  │  NODE_EXTRA_CA_CERTS=<capture-dir>/ca.crt
  ▼
TLS handshake (SNI may be empty or 127.0.0.1; leaf always has loopback IP SAN)
  │  Server presents dynamic cert signed by mitmproxy or embedded CA
  │  ALPN=h2
  ▼
http2.Server (Go, configurable -listen, default 127.0.0.1:8443)
  │  httputil.ReverseProxy FlushInterval=-1 (true bidi stream, no io.ReadAll)
  │  Logs headers + body to captures/
  ▼
http2.Transport (Go, H2 client)
  │  TLS to real api5
  ▼
agentn.global.api5.cursor.sh:443
  (TLS + h2, Cloudflare)
```

`--agent-endpoint` is registered with Commander `.hideHelp()`. It is NOT missing from `2026.08.04-aaa8809`. `strings $(which agent)` only searches the bash wrapper.

## Module Layout

```
/Users/gugen/Code/cursor-byok/h2-agent-proxy/   # package h2agentproxy
├── server.go / cert.go / capture.go / decode.go
└── README.md
/Users/gugen/Code/cursor-byok/cmd/h2-agent-proxy/  # 薄 main
```

Part of the root `cursor` Go module (`go run ./cmd/h2-agent-proxy`), not a nested module. Agent CLI 本地模式见 `openspec/changes/agent-cli-local-mode/`。

## Key Design Decisions

### TLS Termination

- Load mitmproxy CA cert + key from `~/.mitmproxy/mitmproxy-ca.pem` (contains both cert and key after extraction)
- `tls.Config.GetCertificate` callback: on SNI `agentn.global.api5.cursor.sh`, sign a new leaf cert with the CA
- Use `crypto/x509` to create the leaf cert dynamically
- Agent CLI trusts this because it was told to trust the CA via `NODE_EXTRA_CA_CERTS`

### H2 Server

- `http.Server` with `http2.ConfigureServer` (not h2c)
- Handler: logs request, forwards to upstream, logs response
- Multiplexed: single connection carries multiple streams (H2 native)

### Local intercept (evaluation)

Official CLI on HTTP/2 uses bidi `AgentService/Run`. The same CLI, after `GetServerConfig.http2Config=FORCE_ALL_DISABLED`, remaps `Run` → `RunSSE` and sends client messages via `BidiAppend`. This repository already implements that pair on `127.0.0.1:18090`. See `h2-agent-proxy/LOCAL-INTERCEPT.md`.

### Capture Format

Per connection, create a timestamped directory:

```
captures/
└── 20260808_160000/
    ├── 01_request.headers.json   # {"method":"POST","path":"/agent.v1.AgentService/Run","headers":{...}}
    ├── 01_request.body.bin       # raw protobuf body
    ├── 01_response.headers.json  # {"status":200,"headers":{...}}
    └── 01_response.body.bin      # raw protobuf body
```

- `.body.bin` files are raw binary for later protobuf decoding with the scripts from anthropic-cursor-proxy
- `.headers.json` files include `content-type` for identifying connect-rpc vs grpc

### Upstream Forwarding

```go
transport := &http2.Transport{
    TLSClientConfig: &tls.Config{
        ServerName: "agentn.global.api5.cursor.sh",
    },
}
client := &http.Client{Transport: transport}
```

- `AllowHTTP: false` (default) ensures we only do TLS
- `ServerName` matches the real hostname for SNI

## Risk: Cloudflare Anti-MITM

Cloudflare may detect MITM via:
- JA3 fingerprint of Go's TLS client
- Certificate chain depth/issuer differences
- Timing patterns

If Cloudflare rejects forwarded connections, fallback options:
1. Use `utls` library to mimic Chrome's TLS fingerprint
2. Switch to packet-level capture (tcpdump + decrypt) instead of MITM