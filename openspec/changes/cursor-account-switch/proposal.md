# Cursor 账号切换

## Why

官方 Cursor 一次只保留一份 `state.vscdb` 登录态。换账号必须重新登录，之前的鉴权会丢掉。BYOK 已经能读写这份登录台，需要把它变成可保存、可切换的账号库。

## What Changes

1. 新增桌面 Tab「账号」，列出已保存的官方 Cursor 登录。
2. BYOK 在检测到 Cursor 当前登录且库中没有该账号时，写入账号信息与完整 `cursorAuth/*` 登录台。
3. 列表给出当前账号与登录是否有效；支持切换回指定账号、从库中删除。

## Impact

- `server/migrations`：新表 `cursor_accounts`
- `server/src/store`、`server/src/local_app`、`server/src/control`：捕获、校验、切换、删除
- `apps/desktop`：侧栏 Tab、账号页、管理 API 客户端
- 不改插件账号，不把本地注入的 `cursor@ai.com` 当成可切换官方账号
