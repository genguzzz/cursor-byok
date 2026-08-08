# Design: Agent CLI 本地模式

## 传输路径（已由抓包证实）

本地 `GetServerConfig.http2Config = HTTP2_CONFIG_FORCE_ALL_DISABLED` 后，CLI `2026.08.04+` 会把 `AgentService/Run` remap 成 HTTP/1.1：

```
agent -e http://127.0.0.1:18090 --agent-endpoint http://127.0.0.1:18090 --trust
        │
        ├─ api2 preflight  →  本仓库 mock（GetMe / GetServerConfig / GetUsableModels / …）
        └─ agent 传输      →  RunSSE（下行）+ BidiAppend（上行）
```

不要把 `--agent-endpoint` 指到桌面 MITM `:18080`，也不要经 h2-proxy 转回 Cloudflare。

## 目录

```
h2-agent-proxy/              # package h2agentproxy，抓官方 api5
cmd/h2-agent-proxy/          # 薄 main：go run ./cmd/h2-agent-proxy
internal/agentcli/           # 包装官方 agent、拼 endpoint、清代理环境变量
cmd/cli/                     # cursor-local-assistant agent -- …
internal/backend/            # 模型 mock + Host 集成测试
```

与 `cursor-proxy-debugger/` + `cmd/cursor-proxy-debugger/` 同一惯例。

## 默认开启

本地模式（菜单栏或 `cursor-local-assistant`）一旦拉起 backend，Agent CLI 即可用，**没有** tabRenamer 那种 opt-in 开关。

包装命令（推荐）：

```bash
cursor-local-assistant agent -- models
cursor-local-assistant agent -- --list-models
cursor-local-assistant agent -- -p "Reply with exactly: LOCAL-OK" --mode ask --output-format json
```

等价手写：

```bash
HTTPS_PROXY= HTTP_PROXY= ALL_PROXY= \
CURSOR_API_ENDPOINT=http://127.0.0.1:18090 \
agent -e http://127.0.0.1:18090 --agent-endpoint http://127.0.0.1:18090 --trust …
```

`--agent-endpoint` / `-k` 仍是 CLI 隐藏参数；不要用 `strings $(which agent)` 判断。

## 模型 ID

| RPC | `modelId` | `displayModelId` | `aliases` |
|---|---|---|---|
| `GetUsableModels` / `GetDefaultModelForCli` | **渠道 hash**（与桌面 / AgentRouter 一致） | displayName 或 provider modelID | provider modelID + displayName |
| `AvailableModels`（桌面） | **不变**，仍用渠道 hash | — | — |

`ResolveAdapterIndex` 在渠道 hash / legacy hash / `modelID` 之外，再匹配 `displayName`（重名则视为不可解析，与现有 modelID 歧义规则一致）。

## h2-proxy 分工

- **调试抓包**：`go run ./cmd/h2-agent-proxy`，upstream 仍是 api5。
- **本地拦截**：CLI 直打 `:18090`。不要把 h2-proxy upstream 改成本地 backend（本地不是 TLS+h2 bidi `Run`）。

## 测试

1. Unit：`buildCLIModelDetails` 字段、resolver displayName、`agentcli.BuildAgentArgs` / 代理环境过滤。
2. Host HTTP：`GetServerConfig`、`GetUsableModels`、`GetDefaultModelForCli`、`GetMe`、`/healthz`。
3. Host 对话：mock OpenAI chat/completions + `BidiAppend(run_request)` + `RunSSE`，断言 text delta。
4. 可选：本机有 `agent` 且 backend 已起时，手工跑包装命令（不作为 CI 硬依赖）。
