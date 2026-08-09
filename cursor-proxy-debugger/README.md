# Cursor 协议调试器

[中文](README.md) | [English](README.en.md)

这是一个独立运行的本地 HTTPS 调试代理，用于观察 Cursor 的 `BidiAppend`、`RunSSE` 和 Fork Chat 相关通信。它不会修改 Cursor、系统代理或已安装客户端。

## 启动

在仓库根目录运行：

```bash
go run ./cmd/cursor-proxy-debugger
```

默认监听：

- HTTP/HTTPS 代理：`127.0.0.1:9092`（避开 Proxyman 常用的 9090）
- 调试界面：`http://127.0.0.1:9091`
- MITM 目标：`*.cursor.sh`（与桌面 MITM 白名单一致，支持多 server）
- 可选上游：`-upstream-proxy http://127.0.0.1:18080`（本地模式抓包）
- 菜单栏调试 + 本地模式同进程时，还会显示 backend → 官方的第二跳（来源「官方回源」）

启动后会自动打开调试界面。

## 配置 Cursor

工具不会自动修改 Cursor（菜单栏「调试模式」会一键完成）。独立启动后需要手动完成以下配置：

1. 打开 Cursor 的代理设置，将代理地址修改为工具启动时显示的地址，默认是 `http://127.0.0.1:9092`。
2. 打开 Cursor 的 Network 设置，启用 HTTP/1.1。
3. 从 `http://127.0.0.1:9091/api/ca.crt` 下载代理 CA 证书，并确保 Cursor 信任该证书。

调试结束后，请恢复原来的 Cursor 代理和 Network 设置，以免影响正常网络请求。

## 构建

```bash
go build -o bin/cursor-proxy-debugger ./cmd/cursor-proxy-debugger
```

## 参数

```text
-proxy-addr         代理监听地址，默认 127.0.0.1:9092
-ui-addr            调试界面监听地址，默认 127.0.0.1:9091
-target-host        需要解密的目标主机，默认 *.cursor.sh（逗号分隔或通配）
-upstream-proxy     可选上游代理，例如本地模式 http://127.0.0.1:18080
-max-store-bytes    抓包内存预算（字节），默认 209715200（200MiB）；超出丢弃最早记录
-max-exchanges      可选条数上限；0（默认）表示不按条数限制
-open               启动后是否打开浏览器，默认 true
```

## 查询 API

```text
GET /api/status
  → store.usedBytes / maxStoreBytes / count

GET /api/exchanges/query
  过滤：server, captureSource, requestKind|kind, responseKind, method,
        host|hostContains, path|pathContains, requestId, id, status,
        q|query（模糊匹配 id/path/kind/decoded/frames）,
        hasRaw, hasDecoded, minRequestBytes, minResponseBytes,
        since, until (RFC3339), limit, offset,
        include=summary|decoded|raw|frames|full
  例：
  /api/exchanges/query?server=official&kind=run_request&include=decoded&limit=20
  /api/exchanges/query?path=RunSSE&hasRaw=true&include=raw

GET /api/exchanges/{id}?include=full|decoded|raw|frames|summary
GET /api/exchanges/{id}/raw?side=request|response&format=hex|bin|json
```

## 数据处理

- 对 `target-host` 匹配的主机执行 HTTPS MITM（默认全部 `*.cursor.sh`），其他 CONNECT 流量直接透传。
- 列表有 **Server** 列：`本地`（经本地助手 / MITM）与 `官方`（直连或 backend 回源官方）；可用顶部过滤只看一侧。
- `RunSSE` 按 5 字节 Connect 帧头增量拆帧，支持逐帧 gzip 解压。
- `BidiAppendRequest.data` 会继续解码为 `agent.v1.AgentClientMessage`。
- Fork Chat 相关的 `ForkBackgroundComposer`、`NotifyConversationClone` 和 `UploadConversationBlobs` 会双向解码为 protobuf JSON。
- 本地 Fork Chat 主要在客户端完成，只有启用克隆 blob 同步且隐私设置允许时才会产生 `NotifyConversationClone` 和 `UploadConversationBlobs` 流量。
- 请求列表支持按抓包时间正序/倒序排列，并可按协议中的 `request_id`、CmdK、来源过滤。
- 调试界面支持简体中文和英文，可跟随浏览器语言并记住手动选择。
- 抓包只保留在当前进程内存中（默认最多约 200MiB，满则丢最早）；关闭进程后消失。
- `Authorization`、`Cookie`、`Set-Cookie` 等 HTTP 头在界面中默认隐藏。
- 单侧原始正文默认最多保留 16 MiB；代理转发的数据不会被截断。
- `RunSSE`：本地 MITM 流式拆帧；官方 upstream 整包回灌后在 `finishResponseBody` 做同等 Connect offline 解码，两边都应有 `response.frames`。
