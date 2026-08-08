# Proposal: Mixed CmdK 回源 + 调试面板多 server

## Why

桌面 mixed 模式开启 MITM 后，官方「快速编辑 / Cmd+K」失败：

- 证据：`generationUUID=680636bc-e2d5-4636-8679-9069cf32aba7`
- 客户端走 `aiserver.v1.CmdKService/StreamCmdK` → `api2.cursor.sh`
- 本地 backend 只对 `AiService` / `CppService` / `FileSync` / `Dashboard` 等做了 catch-all 回源，**没有** `CmdKService`
- 结果：HTTP 404 → Connect `[unimplemented]` → UI「Connection Error / Connection failed」

同时，菜单栏调试抓包面板默认只 MITM `api2.cursor.sh`，且看不到 backend → 官方的第二跳，不利于排查 mixed 分流问题。

## What Changes

1. Mixed 开启时，`CmdKService/*`（及同类缺失官方 UX service）默认 ClientAuth 回源，不再 404。
2. Mixed 开启时，`NameTab` / `NameAgent` 跟随 `features.tabRenamer.enabled`：关则回源官方命名，开则本地生成（不再永远拦截为空标题）。
3. `cursor-proxy-debugger` 支持多 host（`*.cursor.sh`），UI 可按 host / 来源过滤。
4. Menubar 同进程下，backend `ForwardToUpstream` 把第二跳（官方 upstream）ingest 进调试面板，与客户端→本地流量并列展示。

## Impact

- `internal/backend/host.go`、`internal/backend/mixed`
- `internal/backend/server/upstream`（可选 capture hook）
- `cursor-proxy-debugger/*`、`cmd/menubar/debug_toggle.go`
- OpenSpec：`desktop-mixed-model-routing` 的 unknown RPC 范围需覆盖 `CmdKService`
