## Context

```
Cursor Cmd+K
  → CONNECT api2.cursor.sh via MITM :18080
  → backend :18090
  → 缺路由 → chi 404
  → 客户端 ConnectError Unimplemented / Connection Error
```

官方快速编辑不走 Agent `BidiAppend`/`RunSSE`，而是独立 `CmdKService/StreamCmdK` 流式 RPC。

调试链路（菜单栏「调试模式」）：

```
Cursor → :9092 (capture) → [:18080 若本地模式开] → :18090
                              │
                              └─ ForwardToUpstream → api*.cursor.sh  (第二跳，原先不可见)
```

## Decisions

### D1: 补 `CmdKService` catch-all 回源

与 `CppService` / `FileSyncService` 相同：`mixed.ForwardOrNotFound`。Mixed 关时仍 404（全本地 mock 行为不变）。

同批补上桌面常见、尚未挂 catch-all 的官方 UX service，避免下一个「Connection Error」：

- `CmdKService`
- `ChatService`
- `BackgroundComposerService`

### D2: 调试代理默认 MITM `*.cursor.sh`

用与 `internal/mitm.isWhitelistedRelayHost` 一致的 host 匹配，替代单 `TargetHost=api2.cursor.sh`。保留 `-target-host` 兼容：可传逗号分隔列表，或 `*.cursor.sh`。

### D3: 同进程第二跳 ingest

Menubar 与 backend 同进程。调试代理 `Start` 后注册 `upstream.SetTrafficCapture`；`ForwardToUpstream` 完成后异步写入调试 store，`captureSource=upstream`。客户端 MITM 抓到的记为 `client`。Standalone `go run ./cmd/cursor-proxy-debugger` 无 hook 时行为不变。

### D4: UI

列表增加 Host、Source（client/upstream）列与过滤；endpoint 过滤增加 CmdK。

## Risks

| 风险 | 缓解 |
|---|---|
| StreamCmdK 长流回源中断 | 复用已有 `copyResponse`+Flush |
| 第二跳 body 过大 | 沿用 2MiB capture 上限；ingest 可只带摘要 |
| capture hook 影响热路径 | 异步、失败忽略 |
