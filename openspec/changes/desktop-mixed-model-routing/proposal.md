## Why

当前桌面本地模式把 `*.cursor.sh` 全部解密后送进本地 backend，并 **整表替换** 模型列表、鉴权与 Agent 主链路。用户无法在同一 Cursor 客户端里同时使用官方模型和注入的 BYOK 模型。协议上模型 id 已可区分，缺的是解密后的分流，而不是再做一层「按模型决定是否 MITM」。

## What Changes

- 桌面本地助手开启后，**保留用户真实 Cursor 登录态**，不再写入假 Ultra token。**BREAKING**：依赖假 Ultra 绕过付费墙的用法失效；官方模型按真实套餐计费。
- `AvailableModels` / `GetUsableModels` / 默认模型相关 RPC：**上游官方列表 ∪ 注入渠道**，不再只返回 adapter。
- `BidiAppend` + `RunSSE`：按 `model_id`（渠道 hash vs 官方名）分流；同一 `request_id` 粘性跟随。注入模型走本地 forwarder，官方模型用客户端原始 `Authorization` 回源 `api2.cursor.sh`。
- 未识别的 `*.cursor.sh` RPC **默认回源**，不再 404；`GetServerConfig.http2Config` 仍强制禁用 HTTP/2（MITM 前提）。
- 回源时 **停止** 用 `LocalRelayToken` 覆盖 `Authorization`（否则官方模型必 401）。
- 增加 `features.mixedModelRouting.enabled`（默认 `true`）作为回滚开关。

## Non-goals

- 不支持 Cursor CLI / `agentn.global.api5.cursor.sh` H2 分流（独立栈，见 `h2-agent-proxy`）。
- 不修改已安装 Cursor.app / bundle。
- 不支持同一 `conversation_id` 中途在官方模型与注入模型之间切换并共享历史。
- 不实现 Repository/FileSync 双写；索引 RPC 第一期保持现状并记录限制。
- 不把 Cursor 云端 checkpoint 吸入本地 `state.json` + `context.json`。

## Capabilities

### New Capabilities

- `real-cursor-identity`: 启动时不再注入假账号；控制面/鉴权/用量走真实会话；仅保留 MITM 所需的 HTTP/1.1 与少量 always-local 稳定性补丁。
- `merged-model-catalog`: 用真实账号拉上游模型目录，追加注入渠道条目（hash id），上游失败时回退为仅注入列表。
- `selective-agent-routing`: 解密后窥探 `BidiAppend` 的 `model_id`，建立 `request_id → local|upstream` 亲和；`RunSSE` 跟随；官方流量流式透传且保留客户端鉴权。

### Modified Capabilities

无（`openspec/specs/` 下尚无已归档基线规格）。

## Impact

- `internal/client/lifecycle.go`：停止 `InjectCursorUserInfo`（或仅在 kill switch 关闭时保留旧行为）。
- `internal/backend/server/upstream/client.go`：`shouldRewriteHost` 不得再覆盖官方回源的 `Authorization`。
- `internal/backend/server/upstream/mocks.go` + 新 merge/router：模型目录合并；Bidi/RunSSE 分流。
- `internal/backend/host.go`：未知 RPC 从 404 改为 `ForwardAction`；保留 `GetServerConfig` HTTP/2 强制关闭。
- `internal/backend/server/config/types.go`：`features.mixedModelRouting`。
- `internal/cursor/settings.go`：仍写 `http.proxy` + `cursor.general.disableHttp2`。
- 证据锚点：`internal/mitm/service.go`（host 级 MITM）、`extractRequestedModelID`（`service.go`）、`buildAvailableModelEntries`（`name`/`serverModelName` = channelID）、`ForwardToUpstream` + `copyResponse` 已带 `Flush`。
