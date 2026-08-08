## 1. Mixed CmdK / 官方 UX 回源

- [x] 1.1 `host.go` 注册 `CmdKService/*`、`ChatService/*`、`BackgroundComposerService/*` → `ForwardOrNotFound`
- [x] 1.2 更新 selective-agent-routing spec / mixed handlers 测试说明未知 RPC 含 CmdKService
- [x] 1.3 单测：mixed 开时 CmdK 路径走 forward，mixed 关时 404
- [x] 1.4 NameTab/NameAgent：`tabRenamer.enabled=false` 时 mixed catch-all 回源；`true` 时仍本地

## 2. 调试面板多 server + 双端流量

- [x] 2.1 `TargetHost` 支持多 host / `*.cursor.sh`；默认与 MITM 白名单对齐
- [x] 2.2 Exchange 增加 `captureSource`（client|upstream）；UI 展示 host/source 过滤
- [x] 2.3 `upstream.TrafficCapture` hook + `ForwardToUpstream` 异步 ingest
- [x] 2.4 Menubar 启停调试时注册/注销 capture
- [x] 2.5 更新 README / coding-guidance 简述

## 3. 验证

- [x] 3.1 `go test` 相关包通过
- [x] 3.2 用日志证据确认根因是 CmdKService 404（已有），修复后路径可回源
