## Why

用户 `~/.cursor-local-assistant-v2/config.yaml` 已至少三次被整份覆盖成 `DefaultConfig()`（`modelAdapters: []`，约 400 字节），本地模型列表消失。最近一次发生在 2026-08-09 18:23 菜单栏热重启后。根因是配置存储在 Load 时会把空文件/残缺 YAML 规范化后写回磁盘，且 `Save` / `SaveLastAgentModelHash` 会无条件整文件覆盖。

## What Changes

- `config.Store.Load`：**禁止**在空文件、解析失败或 `modelAdapters` 从 N>0 变成 0 时把 DefaultConfig 落盘。
- `config.Store.Save` / `saveLocked`：拒绝用空 adapter 列表覆盖已有非空列表；写回前把上一份非空配置存为 `config.yaml.bak-last-good`。
- 原子写改用 **pid+nano 唯一临时文件**，避免多进程共用 `config.yaml.tmp` 互相踩踏。
- 菜单栏 surgical 写（log / proxy）复用同一原子写。
- legacy 迁移：**不得覆盖**已存在的 v2 `config.yaml`。
- 若当前文件为空或无法解析，且存在带 adapters 的 last-good 备份，启动时自动恢复。

## Non-goals

- 不改 Cursor.app / bundle。
- 不做跨进程 flock（先靠唯一 tmp + 拒绝破坏性覆盖；后续需要再加）。
- 不把 `lastAgentModelHash` / `log` 改成 surgical yaml.Node 更新（可后续再做，本次先堵住整文件写坏）。
- 不自动恢复「合法的空 DefaultConfig」（避免对抗用户真的清空模型）；只恢复空文件/损坏文件。

## Capabilities

### New Capabilities

- `config-write-guard`: 配置落盘前检查 adapter 数量，拒绝 N→0 覆盖；空/坏文件不写 Default；保留 last-good 备份并在损坏时恢复。

### Modified Capabilities

无（`openspec/specs/` 下尚无已归档基线规格）。

## Impact

- `internal/backend/server/config/store.go`：Load/Save 守卫与备份。
- `internal/appdata/atomic_write.go`：共享原子写。
- `internal/appdata/migrate.go`：禁止覆盖已有 v2 配置。
- `cmd/menubar/proxy_config_darwin.go`：`writeConfigRoot` 改用唯一 tmp。
- 证据：`store.go` `shouldPersistNormalizedConfig` + 缺 `backendListenAddr` 即 `saveLocked(DefaultConfig)`；`saveLocked` 固定 `path+".tmp"`；18:23:56 磁盘文件与 `DefaultConfig` marshal 一致。
