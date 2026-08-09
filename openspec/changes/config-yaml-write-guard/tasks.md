## 1. Atomic write + migrate

- [x] 1.1 `appdata.WriteFileAtomic` + 单测（完整落盘、并发不产生半文件）
- [x] 1.2 `copyLegacyFile` 跳过已存在目标 + 单测

## 2. Store write guard

- [x] 2.1 Load：空/坏文件不写 Default；可从 `.bak-last-good` 恢复
- [x] 2.2 saveLocked：拒绝 N→0；写前备份 last-good
- [x] 2.3 唯一 tmp；菜单栏 `writeConfigRoot` 复用 atomic write
- [x] 2.4 store 单测覆盖：截断不覆盖、N→0 拒绝、缺失 listen 仍保留 adapters、损坏恢复

## 3. Verify

- [x] 3.1 `go test` config / appdata / menubar darwin 相关包
