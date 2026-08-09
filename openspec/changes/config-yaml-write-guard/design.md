# Design: config.yaml write guard

## Evidence

- `internal/backend/server/config/store.go`：`Load` 在文件不存在时写 `DefaultConfig`；YAML 能 Unmarshal 成零值时，缺 `backendListenAddr` 会 `shouldPersistNormalizedConfig` → `saveLocked(normalized)`，把空 adapters 写死。
- 空文件 / `open(path,'w')` 截断窗口：`yaml.Unmarshal` 成功得到零值 Config，随后整文件覆盖 93KB 用户配置。
- `saveLocked` 与菜单栏 `writeConfigRoot` 共用 `config.yaml.tmp`，多进程 rename 会丢内容。
- `SaveLastAgentModelHash` / `SetObservabilityLogEnabled` 走整份 `Save`；热加载一旦读到空文件，内存变 Default，再 Save 即永久写坏。
- `migrate.go` `copyLegacyFile` 对已存在的 v2 目标使用 `O_TRUNC`，legacy 残留时会覆盖。
- 2026-08-09 18:23:56 现场：403 字节 DefaultConfig vs 18:08 的 93KB / 41 adapters。

## Decision

**D1**: Load 只在 **文件不存在** 时初始化 DefaultConfig。空文件、仅空白、解析失败：**返回错误且不写盘**。热加载已有「失败则保留内存旧值」行为，正好吃掉截断竞态。

**D2**: 任何 `saveLocked`：若磁盘 probe 到 `modelAdapters` 数量 > 0，而即将写入数量 == 0，返回错误。覆盖所有 Save / Load persist / lastAgentModelHash 路径。

**D3**: 覆盖非空 adapters 配置前，把当前字节拷到 `config.yaml.bak-last-good`。当前文件为空/损坏且 last-good 有 adapters 时，Load 自动恢复。

**D4**: 原子写使用 `.<basename>.<pid>.<nano>.tmp` 后 rename；菜单栏 surgical 写共用 `appdata.WriteFileAtomic`。

**D5**: `copyLegacyFile` 若目标已存在则跳过。

## Non-Goals

- flock
- 把 hash/log 改成字段级 patch
- 自动恢复「合法空 DefaultConfig」
