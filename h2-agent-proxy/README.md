# Cursor CLI H2 流量拦截

拦截 Cursor CLI (`agent`) 发往 `agentn.global.api5.cursor.sh` 的 **TLS + HTTP/2** 推理流量（`agent.v1.AgentService/Run` 双向流），落盘原始 protobuf。

api2（`api2.cursor.sh` 的 preflight RPC）请继续用 mitmproxy reverse proxy，本工具只覆盖 api5。

## 为什么不用 mitmproxy / Surge

CLI 对 api5 直接开 TLS ALPN=`h2`，没有 HTTP/1.1 `CONNECT` / Upgrade。mitmproxy 12.x 和 Surge 都吃不下这种 prior-knowledge H2。

## 启动

在仓库根目录：

```bash
# 默认使用仓库 embedded CA
go run ./cmd/h2-agent-proxy

# 或复用 mitmproxy CA（与 api2 抓包同一条信任链）
go run ./cmd/h2-agent-proxy -ca-bundle /tmp/mitmproxy-ca-bundle.pem
```

常用参数：

| 参数 | 默认 | 说明 |
|---|---|---|
| `-listen` | `127.0.0.1:8443` | TLS+h2 监听地址，可改成 `:443` 做 hosts 劫持 |
| `-upstream` | `agentn.global.api5.cursor.sh` | 真实 api5 |
| `-ca-bundle` | 空（embedded CA） | mitmproxy 风格 cert+key PEM |
| `-capture-dir` | `./captures/<timestamp>` | 抓包目录 |
| `-decode` | 空 | 解码已有抓包目录后退出 |

## 跑 agent

`--agent-endpoint` 和 `-k/--insecure` 是 **隐藏参数**，`agent --help` 不显示，但 `2026.08.04-aaa8809` 可用。不要用 `strings $(which agent)` 判断——那是 bash wrapper。

```bash
# 只拦 api5（api2 直连）
HTTPS_PROXY= HTTP_PROXY= ALL_PROXY= \
NODE_EXTRA_CA_CERTS="$PWD/h2-agent-proxy/captures/<ts>/ca.crt" \
agent \
  --agent-endpoint https://127.0.0.1:8443 \
  -k \
  --trust \
  -p "Reply with exactly: OK" \
  --mode ask \
  --model composer-2.5 \
  --output-format json
```

同时抓 api2 + api5：

```bash
# 终端 1：api2
cd /tmp/anthropic-cursor-proxy
mitmdump -s scripts/capture_cursor_api.py --mode "reverse:https://api2.cursor.sh@8080"

# 终端 2：api5
cd /path/to/cursor-byok
go run ./cmd/h2-agent-proxy -listen 127.0.0.1:8443 -ca-bundle /tmp/mitmproxy-ca-bundle.pem

# 终端 3
HTTPS_PROXY= HTTP_PROXY= ALL_PROXY= \
NODE_EXTRA_CA_CERTS=$HOME/.mitmproxy/mitmproxy-ca-cert.pem \
agent \
  -e http://127.0.0.1:8080 \
  --agent-endpoint https://127.0.0.1:8443 \
  -k --trust \
  -p "Reply with exactly: OK" \
  --mode ask --model composer-2.5 --output-format json
```

准备 mitmproxy CA bundle（只需一次）：

```bash
openssl pkcs12 -in ~/.mitmproxy/mitmproxy-ca.p12 -nocerts -nodes -passin pass: \
  > /tmp/mitmproxy-ca-key.pem
cat ~/.mitmproxy/mitmproxy-ca-cert.pem /tmp/mitmproxy-ca-key.pem \
  > /tmp/mitmproxy-ca-bundle.pem
```

## 抓包目录

```
captures/20260808_160000/
  ca.crt
  01_request.headers.json
  01_request.body.bin
  01_request.frames.jsonl
  01_response.headers.json
  01_response.body.bin
  01_response.frames.jsonl
  01_summary.json
```

`.body.bin` 是 Connect-RPC 原始帧。代理在流结束后会自动写出：

- `NN_request.frames.jsonl` / `NN_response.frames.jsonl` — 逐帧 protobuf JSON（`AgentClientMessage` / `AgentServerMessage`）
- `NN_summary.json` — kind 列表

事后解码已有目录：

```bash
go run ./cmd/h2-agent-proxy -decode /tmp/h2-agent-captures/live
```

把 CLI **改打到本仓库本地 backend、不走官方 api5** 已落地：本地模式开启后用 `cursor-local-assistant agent -- …`。评估与用法见 [LOCAL-INTERCEPT.md](./LOCAL-INTERCEPT.md)。

## 备选：DNS 劫持

若不想用 `--agent-endpoint`，把监听改成 443 并写 hosts：

```bash
sudo go run ./cmd/h2-agent-proxy -listen :443 -ca-bundle /tmp/mitmproxy-ca-bundle.pem
sudo sh -c 'echo "127.0.0.1 agentn.global.api5.cursor.sh" >> /etc/hosts'
```

不要把 `127.0.0.0/8` 从 Surge `bypass-tun` 里拿掉，否则 localhost 会被 VIF 拐走。
