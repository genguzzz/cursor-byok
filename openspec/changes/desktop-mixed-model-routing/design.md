## Context

桌面本地助手今天是「一开就全盘接管」：

```
Cursor ──HTTP/1.1 proxy──► MITM :18080
                              │  *.cursor.sh 解密后全部塞进 backend
                              ▼
                         host.go 路由
                    AvailableModels = 仅注入模型
                    Bidi/RunSSE     = 永远本地 forwarder
                    Auth/Stripe     = 假 Ultra
                    未知 RPC        = 404
```

客户端 `cursor-always-local` 本来就是「工具在本地、Agent 循环在服务端」。官方模型与注入模型可以共用同一套客户端协议，只是服务端实现不同：我们或 Cursor 云。

约束：不改 Cursor.app；桌面已强制 HTTP/1.1；CLI/api5 不做。

## Goals / Non-Goals

**Goals:**

- 同一桌面 Cursor 里，picker 同时出现官方模型与注入模型，两者都能跑通 Agent。
- 注入模型：现有 local forwarder 质量不降。
- 官方模型：真实账号回源 api2，工具仍由 always-local 执行。
- MITM 仍覆盖全部 `*.cursor.sh`（否则看不见 model_id）。
- 可用 yaml kill switch 回到旧「全本地 mock」行为。

**Non-Goals:**

- CLI / api5 H2。
- 同会话跨后端换模型并合并历史。
- Repository/FileSync 双写。
- 修改已安装客户端。

## Decisions

### D1: 仍 MITM 全部 `*.cursor.sh`，在 backend 分流（不在 CONNECT 分流）

**选择**: CONNECT 对白名单 host 一律 MITM；解密后按 RPC/模型决定 local vs upstream。

**理由**: `model_id` 在 `BidiAppendRequest.data` 的 protobuf 里，不在 Host/Path。同一 keep-alive 连接混着拉模型、Bidi、用量。CONNECT 时无法知道本轮模型。

**Evidence**:

- `internal/mitm/service.go` `isWhitelistedRelayHost` + `OnRequest`：`*.cursor.sh` 解密后 `forwardToServer`。
- `proto/aiserver_v1.proto` `BidiAppendRequest`：`data` / `request_id` / `append_seqno`。
- `internal/backend/forwarder/service.go` `extractRequestedModelID`：从 `run_request` / `prewarm_request` 取 model。

**备选（拒绝）**: 按模型决定是否 MITM — 不可行。

### D1.1: CLI 直连 `:18090`，不把 `:18080` MITM 当分流器

**选择**: 官方 `agent` 继续 `-e http://127.0.0.1:18090 --agent-endpoint http://127.0.0.1:18090`。分流只在 backend 做。MITM `:18080`（不是 18000）只给 Cursor.app HTTPS 解密；不能当 CLI `-e` 目标，也不能在 CONNECT 上判断模型。

CLI 直连没有 `X-Server-Upstream-URL`。混合回源时若 TargetURL 缺 scheme/host 或指向 localhost，补成 `https://api2.cursor.sh` + 原 path。MITM 带来的 `api2` / `api5` 主机一律保留。

**理由**: GetServerConfig mock 已把 CLI 打成与桌面相同的 HTTP/1.1 `RunSSE`+`BidiAppend`。再绕一圈 `HTTPS_PROXY=:18080` 只是多一次解密，决策点不变，还容易把 localhost 拐进 Surge。

**备选（拒绝）**: 让 CLI 走 18080 再决定 local vs remote — MITM 在 CONNECT 仍看不见 `model_id`，最终还是进 18090。

测试只起 backend、不要注入 Cursor：

```bash
go run ./cmd/cli --backend-only
```

不要开菜单栏本地模式、不要写 `http.proxy`。对照官方 api5 用 `cmd/h2-agent-proxy :8443`。

路由核心在 `internal/backend/mixed` + `upstream.AgentRouter`：local forwarder 仍是「永远本地」的 handler。MITM CONNECT 看不见 model_id；CLI `--backend-only` 也没有 18080，所以分流挂在 18090 入口，代理层只做解密转发。日后若桌面要在 MITM 进程直连 api2，应复用 mixed 策略函数，而不是再写一套。

预检 RPC（AvailableModels / GetUsableModels / GetMe / GetDefaultModelForCli）先打官方再 merge/回落本地 mock，这样 CLI 无 token 也能列注入模型，有登录则官方+注入并存。

### D2: 渠道 hash vs 官方 model 名作为判别器

**选择**: `model_id ∈ 已配置 adapter.ID`（含 legacy hash）→ local；否则 → upstream。Variant `channelID:high` 先按现有逻辑拆出 channel。

**Evidence**:

- `internal/backend/server/upstream/mocks.go` `buildAvailableModelEntries`：`name` / `serverModelName` = `channelID`。
- `internal/modelchannel/identity.go`：渠道 ID = SHA-256 前 16 hex，与 `claude-*` / `gpt-*` 几乎不可能碰撞。
- `extractRequestedModelIDFromRequestedModel`：已处理 `IsVariantStringRepresentation`。

**备选（拒绝）**: 用 displayName / provider modelID — 会与官方名撞车。

### D3: 停止假身份注入；回源保留客户端 Authorization

**选择**: `features.mixedModelRouting.enabled=true`（默认）时：

1. 不调用 `InjectCursorUserInfo`。
2. `ForwardToUpstream` 对混合模式回源 **不** 执行 `shouldRewriteHost` 的假 token 覆盖。
3. Auth / Stripe / 用量 / 未点名的 Dashboard 走客户端原 header 回源。
4. 仍写入 proxy + `cursor.general.disableHttp2` + CA。
5. 仍 mock `GetServerConfig.http2Config = HTTP2_CONFIG_FORCE_ALL_DISABLED`。
6. 仍 mock `BootstrapStatsig`，并继续关掉 `decompose_always_local_ext_host`（vscdb 可只打 gate、不动 auth key）。

**理由**: 假 JWT 回源官方 Agent 必失败。`shouldRewriteHost` 会把所有 `*.cursor.sh` 回源改成 `LocalRelayToken`，即使客户端带了真 token。

**Evidence**:

- `internal/client/lifecycle.go` 启动注入假账号。
- `internal/runtime/defaults.go` `InjectAuthToken` / `LocalRelayToken`。
- `internal/backend/server/upstream/client.go` `shouldRewriteHost` + 覆盖 `Authorization`。
- `internal/cursor/settings.go` 强制 `disableHttp2`。
- `internal/backend/server/upstream/mocks.go` `buildServerConfigPayload`。
- `internal/backend/server/upstream/mocks_test.go` Statsig gate 单测。

**身份/鉴权风险**: 免费账号选官方模型会收到 Cursor 云真实错误，这是正确行为。注入模型不依赖 Cursor 套餐。Kill switch `enabled=false` 恢复旧假 Ultra 全本地模式。

**备选（拒绝）**: 界面假 Ultra、回源换成 `cursoraccount.Manager` 第二套号 — UI/配额/风控不一致。

### D4: 模型目录 merge，不 replace

**选择**:

1. 用客户端原 body + 原 `Authorization` 拉上游 `AvailableModels`。
2. `proto.Unmarshal` 后把注入条目 append 到 `models`；`name` 已存在则跳过。
3. **保留** 上游 `composerModelConfig` / `cmdKModelConfig` / defaults，不改成「全部默认我们的模型」。
4. 上游失败：回退当前「仅注入」payload，保证未登录也能用 BYOK。
5. 同步 merge `GetUsableModels`；`GetDefaultModel` / nudge 优先透传，并把我们的 channelID 加入 `modelsWithNoDefaultSwitch`，避免官方「升级」掉 hash 模型。

**Evidence**:

- `AvailableModelsResponse.AvailableModel` 字段齐全，现有 `encodeMockProto` + protojson 已能编出合法条目。
- 当前 `buildAvailableModelsPayload` 整包替换，会抹掉官方 Auto/Composer 默认。

### D5: `request_id` 亲和表 + RunSSE 短等

**选择**:

```
classify(BidiAppend):
  if affinity[request_id]: return it
  model_id = extractRequestedModelID(msg)
  if isOurChannel(model_id): local
  else if model_id != "": upstream
  else if conversation in local history: local
  else: upstream
  store affinity[request_id]

RunSSE:
  if affinity known: follow
  else wait ≤ 2s for first BidiAppend
  else default upstream
```

窥探后把 **原始字节** 交给 local connect handler 或 `ForwardToUpstream`（可解码，不改写 body）。

Connect unary：兼容 `application/proto` 与 5 字节 envelope（`h2-agent-proxy/decode.go` 已有同类拆帧）。桌面强制 HTTP/1.1 后现网 mock 已用 `application/proto`。

**Evidence**:

- `BidiAppend` 是 unary；`RunSSE` 只有 `BidiRequestId`（`service.go` `RunSSE`）。
- `NewLegacyRunSSEHandler` 把本地响应写成 `text/event-stream`；**回源时不得包这层 wrapper**，原样拷贝上游 Content-Type。
- `copyResponse` 已按 chunk `Flush`，可扛 SSE。
- `host.go` `netproxy.NewHTTPClient(30000 * time.Second)` 覆盖长流（约 8.3h）。

**流式透传风险**: Go TLS 指纹可能被 Cloudflare 干扰；Dashboard `AuthenticatedForwardAction` 已证明 unary 回源可行。RunSSE 是主要残余风险。缓解：保留客户端 HTTP/1.1；回源可用 HTTP/2；超时用长 Client.Timeout；失败打明确日志。

### D6: 未知 RPC 默认回源，只拦截白名单

**选择**: mixed 开启时路由哲学反过来：

| 仍本地 / 改写 | 原因 |
|---|---|
| `GetServerConfig` | 强制关 HTTP/2 |
| `BootstrapStatsig` / `GetFirstWindowStatsigDecision` | 稳住 always-local |
| `AvailableModels` 及相关 catalog | merge |
| `BidiAppend` / `RunSSE` | 分流 |
| 已实现的 local-only（注入会话的 thought/usage；NameTab/NameAgent 仅当 `features.tabRenamer.enabled`） | 按 request_id / 配置；tabRenamer 关则回源官方命名 |

其余 `AiService/*`、`CppService/*` 未点名、`FileSync/*` 未点名、`Dashboard/*`、`/auth/*` → `ForwardAction` 且 **PreserveClientAuth**。

**Evidence**: `host.go` 大量 `NotFound` catch-all，官方模型会缺 RPC。Marketplace 的独立账号转发在 mixed 下改为客户端原 token，避免双身份。

**索引限制**: `RepositoryService` 第一期仍走现有本地 handler。官方云端 `@codebase` 语义索引可能变弱；文件类能力仍走 always-local `exec_server_message`。不在本期做双写。

### D7: 配置与回滚

```yaml
features:
  mixedModelRouting:
    enabled: true   # 默认开；false = 旧全本地 mock
```

停服务路径不变：清 proxy、若曾备份则 `RestoreCursorAuthState`。mixed 下不注入则通常无备份可恢复。

### D8: 会话与历史隔离

本地 `history/<id>/state.json+context.json` 与 Cursor 云 checkpoint 不是同一事实源。跨后端换模型不迁移历史。客户端若带 checkpoint，本地 forwarder 仍不把它当真相（既有规则）。

## Risks / Trade-offs

| 风险 | 缓解 |
|---|---|
| 假 token / `shouldRewriteHost` 导致官方 401 | D3：不注入、不覆盖 Authorization |
| RunSSE 早于首包 Bidi | 等 ≤2s；超时默认 upstream，避免误吞官方会话 |
| Cloudflare 长连接指纹 | unary 已有先例；SSE 失败可日志 + kill switch |
| 免费用户官方模型不可用 | 预期行为；注入模型不受影响 |
| 依赖假 Ultra 的老用户 | kill switch；文档写明 BREAKING |
| 官方 `@codebase` 索引变弱 | D6 记录；工具链仍可用 |
| Statsig 全透传可能拆掉 always-local | 继续 mock Statsig + 关 decompose gate |
| 同会话跨后端换模型历史错乱 | 明确不支持 |

## Migration Plan

1. worktree 分支落地；默认 `enabled=true`。
2. 升级后重启助手；Cursor 需已登录真实账号才能用官方模型。
3. 回滚：yaml `enabled: false` 并重启，或停助手恢复 proxy。
4. 若机器上残留假 token 备份：停一次助手走 `RestoreCursorAuthState`。

## Open Questions

- 官方 RunSSE 在 HTTP/1.1 + 真账号下是否稳定（需真实桌面 e2e，单测无法替代）。
- `GetDefaultModel` 透传后，picker 初始选中是否总是官方默认（可接受；用户可手选注入模型）。
