# Cursor CLI → 本地 backend

基于 2026-08-08 抓包（`agent` `2026.08.04-aaa8809`）与后续落地：CLI 走本仓库本地模式时 **不要** 经本目录 h2-proxy 转官方 api5。

解码旧抓包：

```bash
go run ./cmd/h2-agent-proxy -decode /tmp/h2-agent-captures/live
```

## 结论（已落地）

云端 CLI 的 `AgentClientMessage` / `AgentServerMessage` 与 forwarder 是同一套 proto。本地 `GetServerConfig.http2Config=FORCE_ALL_DISABLED` 后，CLI 把 `Run` remap 成现有的 **HTTP/1.1 `RunSSE` + `BidiAppend`**。

本地模式（菜单栏或 `cursor-local-assistant`）一旦启动 backend，Agent CLI 默认可用，无额外开关。

```bash
# 推荐：包装官方 agent，自动注入 -e / --agent-endpoint / --trust，并清掉代理环境变量
cursor-local-assistant agent -- models
cursor-local-assistant agent -- --list-models
cursor-local-assistant agent -- -p "Reply with exactly: LOCAL-OK" --mode ask --output-format json
```

等价手写（backend 默认 `127.0.0.1:18090`，以 config.yaml 为准）：

```bash
HTTPS_PROXY= HTTP_PROXY= ALL_PROXY= https_proxy= http_proxy= all_proxy= \
CURSOR_API_ENDPOINT=http://127.0.0.1:18090 \
agent \
  -e http://127.0.0.1:18090 \
  --agent-endpoint http://127.0.0.1:18090 \
  --trust \
  -p "Reply with exactly: LOCAL-OK" \
  --mode ask \
  --output-format json
```

`--agent-endpoint` / `-k` 是隐藏参数。不要指到桌面 MITM `:18080`，不要用 `https://`（backend 是 http）。

`agent models` 列出的是本地 `modelAdapters` 的 `modelID`（以及 displayName / 渠道 hash 别名）。`--model composer-2.5` 只有在某个 adapter 的 `modelID` 或 displayName 配成该名字时才会命中。

## h2-proxy 分工

- **本目录 / `cmd/h2-agent-proxy`**：抓官方 api5 TLS+h2，upstream 仍是 Cloudflare。
- **本地拦截**：CLI 直打 `:18090`。不要把 h2-proxy upstream 改成本地。
