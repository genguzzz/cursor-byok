# Tasks: H2 TLS Intercept Proxy

## 1. Verify `--agent-endpoint` flag works with our proxy
- [x] Confirmed hidden flag still exists in CLI `2026.08.04-aaa8809` (`hideHelp()`, not removed)
- [x] Confirmed `-k/--insecure` is also a hidden flag and disables agent TLS verify
- [x] `strings $(which agent)` is a false negative (bash wrapper); search `index.js` instead

## 2. Create Go module scaffold
- [x] `h2-agent-proxy/` is part of the main `cursor` module (`go run ./cmd/h2-agent-proxy`)
- [x] Uses `golang.org/x/net/http2` + repo `internal/certs` embedded CA

## 3. Implement TLS termination with mitmproxy CA
- [x] Load CA cert + key from `-ca-bundle` or embedded CA
- [x] Dynamic leaf cert with DNS SAN + loopback IP SAN (`127.0.0.1`, `::1`)
- [x] `tls.Config.GetCertificate` callback; empty SNI falls back to localhost

## 4. Implement http2.Server handler with capture logging
- [x] Log request headers to `.headers.json`
- [x] Stream request body to `.body.bin` (no full-buffer / string conversion)
- [x] Log response headers to `.headers.json`
- [x] Stream response body to `.body.bin`

## 5. Implement http2.Transport upstream forwarding
- [x] H2 client with TLS to real api5
- [x] `httputil.ReverseProxy` + `FlushInterval: -1` for bidi streaming
- [x] Configurable `-listen` / `-upstream`
- [x] Preserve binary protobuf (including NUL / non-UTF8)

## 6. End-to-end test
- [x] Unit: binary body round-trip + bidi streaming does not wait for request EOF
- [x] Live: `agent --agent-endpoint https://127.0.0.1:8443 -k --trust` returned `OK` via proxy (2026.08.04-aaa8809)
- [x] Capture `01_request` is `POST /agent.v1.AgentService/Run` with prompt bytes; response 200 streaming body ~18KB
- [x] Decode Connect frames to `AgentClientMessage` / `AgentServerMessage` JSONL
- [x] Document local-intercept feasibility (`LOCAL-INTERCEPT.md`): same proto as forwarder; prefer HTTP/1.1 RunSSE+BidiAppend via local `18090`
