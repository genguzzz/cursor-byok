# Design

## Current

```
Cursor state.vscdb
  ItemTable cursorAuth/*
        │
        ▼
local_app/account.rs
  无 token 时注入本地假账号
        │
        ▼
官方登录会覆盖这份登录台，且只留一份
```

## Target

```
Cursor state.vscdb
  ItemTable cursorAuth/*          当前登录台（唯一生效份）
        │
        │  读：捕获 / 校验当前
        │  写：切换
        ▼
server/src/local_app/account.rs   登录台读写、身份解析、已登录不注入
        │
        ▼
server/src/local_app/accounts.rs  捕获、列表、校验、切换、删除
        │
        ▼
store/cursor_accounts.rs
  cursor_accounts                 多账号库（含完整登录台 JSON）
        │
        ▼
GET/POST/DELETE /__byok-api__/api/cursor-accounts
        │
        ▼
apps/desktop /accounts            Tab：当前、有效、切换、删除
```

## Identity

- 主键：`account_id`（UUID）
- 去重：JWT `sub`；没有 `sub` 时用 `email:{email}`
- 临时账号：`sub=cursor-local-user` / `email=cursor@ai.com`，列表默认保证存在，可切换，不可删除
- 同一身份再次登录：更新登录台，不新增行

## Auth snapshot

保存 `ItemTable` 里全部 `cursorAuth/%` 键值。API 列表不回传 token 或快照。

## Validity

| 状态 | 含义 |
|---|---|
| `valid` | Cursor 鉴权接口接受该 token |
| `expired` | JWT `exp` 已过 |
| `invalid` | 鉴权接口拒绝 |
| `unknown` | 网络失败，且 JWT 未过期 |

临时账号不探测官方接口，本地 JWT 未过期即视为有效。账号页打开时用 `?probe=1` 探测官方账号。

## Detection

只在打开账号 Tab（列表接口）时读一次当前登录台；库中没有就插入。已登录则不注入临时账号。启用集成时仍只在无 token 时注入。

## Switch / delete

- 切换：先把当前登录台按身份入库（避免覆盖丢失），再把目标快照写回 `state.vscdb`
- 删除：只删 BYOK 库。当前账号不能删，需先切走
- Cursor 若正打开 `state.vscdb`，写入使用 busy timeout；失败则报错

## API

```
GET    /__byok-api__/api/cursor-accounts[?probe=1]
POST   /__byok-api__/api/cursor-accounts/{id}/switch
DELETE /__byok-api__/api/cursor-accounts/{id}
```
