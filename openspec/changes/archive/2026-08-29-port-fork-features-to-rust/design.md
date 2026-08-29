# Design

## 1. menubar-coexistence

### 决策：不移植独立的 Go 菜单栏二进制，改为扩展上游 tray

上游已有 `apps/desktop/src-tauri/src/tray.rs`（`TrayIconBuilder`，菜单只有「打开 Cursor BYOK」「退出」），且 `desktop.rs` 已实现关窗不退出（`CloseRequested → prevent_close + window.hide()`）。这就是「桌面端推后台、菜单栏保持驻留」的基座。

再单独编一个 Go `cursor-menubar` 进程会导致两个进程争抢同一份 Cursor `settings.json` 与 CA、两套端口分配，正是 Go 世代 `2680884` 修过的那类端口污染。所以：

- 菜单栏能力 = 扩展 `tray.rs` 菜单项 + 新增后端控制 API
- 「拉起官方桌面端」= `show_main_window`（已有）
- 「推后台保活」= 已有 `prevent_close`；补 macOS `set_dock_visibility(false)` 的运行时切换，使窗口隐藏后 Dock 图标也消失、仅剩菜单栏

### 菜单项映射（Go → Tauri）

| Go 菜单项 | Tauri 落点 | 后端依赖 |
|---|---|---|
| 本地模式 ✓/✗ | 新 `MenuItem` + checkmark | `harness.enable()/disable()`、`harness.status()` |
| 状态: … | 禁用的显示项，标题运行时重写 | `harness.status()` |
| 模型出站代理 (9090) | 新 `MenuItem` + checkmark | 新控制 API：批量写 model 的 proxy 字段 |
| 强制恢复账号与设置 | 新 `MenuItem` | `harness.cleanup_stale_settings()` + account restore |
| 调试模式 | 新 `MenuItem` + checkmark | 上游无 MITM 抓包 UI，**降级为仅切 tracing level**，抓包另议 |
| 退出 | 已有 | — |

状态刷新用一个 `tokio` 轮询任务（2s）读 `harness.status()` 并 `MenuItem::set_text` / `CheckMenuItem::set_checked`。Go 世代那个 500ms 轮询 `config.yaml` 的双向耦合不移植——上游配置在 SQLite，控制面直接可写，不需要靠文件轮询做 IPC。

ObjC use-after-free（`b1b6542`）、菜单项全灰（`de014ad`）这两类 bug 在 Tauri 下不存在：`tray-icon` 持有菜单所有权，字符串是 `String` 传值。

### 关键风险
`tray-icon` 的 `CheckMenuItem` 与 `MenuItem` 在 macOS 上混用时，带图标的项会给同级项加缩进（Go 世代 `88bc8c5` 踩过）。对策：全部用纯文本，不设图标。

## 2. codebuddy-provider

### 结论：CodeBuddy 不是独立协议，是 OpenAI Chat 的一层包装

Go 侧 `CodeBuddyAdapter.Stream` 改完 header/body 就直接调 `OpenAIAdapter.Stream`。所以 Rust 侧不新建 `Provider` 实现，而是：

- `ProviderType` 新增 `CodeBuddy`（serde `"codebuddy"`）
- `ModelType` 新增 `CodeBuddy`；`provider_type()` 映射到 `ProviderType::CodeBuddy`
- `provider/router.rs::build_inner` 对 `CodeBuddy` 构造 `OpenAiChatProvider`，但注入一个 `CodeBuddyDecoration`
- 装饰点：header 注入 + body 追加 + gzip + 用户内联图片剥离

### Header 表（全量，来自 Go `CodeBuddyStandardHeaders` + `applyCodeBuddyDynamicHeaders`）

静态常量：
```
CLI 版本 2.127.0 / stainless 6.25.0 / node v23.11.1
User-Agent: "CLI/2.127.0 CodeBuddy/2.127.0"
X-IDE-Type: CLI          X-IDE-Name: CLI         X-IDE-Version: 2.127.0
X-Product: SaaS          X-Agent-Intent: craft   X-Agent-Purpose: conversation
X-Private-Data: false
X-Enterprise-Id / X-Tenant-Id: etahzsqej0n4
X-Domain: tencent.sso.copilot.tencent.com
x-requested-with: XMLHttpRequest
x-stainless-{arch=arm64,lang=js,os=MacOS,package-version,runtime=node,runtime-version,retry-count=0}
```

每请求动态：`X-Conversation-ID`(uuid) / `X-Conversation-Request-ID`(去横线或 16B hex) / `X-Request-ID` / `X-Conversation-Message-ID` / `X-Root-Request-ID` / `x-codebuddy-request: 1`，以及 W3C+B3 链路头 `traceparent: 00-{trace32}-{span16}-01`、`b3: {trace}-{span}-1-{parent}`、`X-B3-{TraceId,SpanId,ParentSpanId,Sampled}`、`X-Trace-ID`。

`X-User-Id` 由 API key（JWT）payload 的 `sub` 解出，base64url 无填充。

**语义要求**：全部用「不存在才写」(`set_if_absent`, 大小写不敏感)，用户配置的 `custom_headers` 永远优先。

### Body 约定
- 始终追加 `reasoning_summary: "auto"`
- `verbosity` 由 reasoning effort 推导：low→low, medium→medium, high/xhigh/max→high, 无→省略
- 用户 `openai_extra_params` 覆盖以上二者
- 请求体 gzip + `Content-Encoding: gzip`，且**不发 `Accept-Encoding`**
- 不硬编码 `temperature`
- user 角色内联图片剥离为 `<selected_images><image path=…/></selected_images>` + 提示模型改用 Read；真实像素只走 tool-result 通道

### 不移植
`CodeBuddyModelDiscovery` / `CodeBuddyModelIDs()` 在 Go 侧是死代码（无调用点），不移植。

## 3. shell-background-tasks

上游已有：`block_until_ms` → `ShellArgs.timeout`、`TimeoutBehavior::Background`、`hard_timeout=86_400_000`、`notify_on_output`、`terminals_folder` 回填、后台完成经 `BackgroundTaskCompletion` 唤醒父轮次。**请求侧已对齐，甚至比 Go 多带 `terminals_folder`。**

缺口按价值排序，本次只做前三项：

1. **`tool_call_delta` 分片**：上游 `codec/response.rs::shell_delta` 整块下发。Go 侧 `shellToolCallDeltaChunkLimit = 8 KB` 是为修 Glass 气泡不刷新。移植为按 8 KB 切分，优先在换行处切。UTF-8 边界问题在 Rust 不存在（proto `data` 已是 `String`），故不需要 `alignUTF8ChunkEnd`。
2. **后台化后仍推流**：上游后台化瞬间 `PendingExec` 被移除，气泡从此静默。移植 Go 的终端文件读取（`<shellId>.txt`，`---` 分隔元数据块）+ 250ms 轮询，把增量 stdout 继续以 delta 推出。
3. **`TASK_PROGRESS` 完成事件**：上游 `background.rs::project` 只保留 `TaskFinished`，丢弃进度事件。改为进度事件也折叠进状态。

**明确不移植 `AwaitShell`**：上游在 `compat.rs::failure_message` 显式把它标为「本版本已移除，请用 Shell/后台完成流程」。这是上游的架构决策（推模型替代拉模型），重新引入会与 `BackgroundTaskCompletion` 双写。Go 侧那套 actor 定时器轮询（`56e7680`）随之作废。

## 4. mixed-mode-routing

上游已有骨架：`bidi_append_handler` 按 `model_id` 查本地 catalog 决定 local/upstream，`sessions.rs::CursorRoute` 做 request_id 亲和，`model_catalog.rs` 用字节级追加合并官方与本地模型列表（比 Go 的解码-重编码更稳）。

缺口，按价值排序：

1. **子 agent 路由（最严重）**：`DecodedAppend::model_id` 完全忽略 `subagent_type_name` 与 `subagent_model_overrides`，child run 按父模型路由 → 本地 explore 被送去官方云端。需移植 `ExtractEffectiveRunModelID` + `ApplyEffectiveChildRunModel` 语义，含别名折叠（`generalPurpose`↔`explore`、`browserUse`↔`browser-use`），以及 bidi body 重写（re-hex `BidiAppendRequest.data`、按入站形状重编码 connect 帧、修 `Content-Length`、去 `Content-Encoding`）。
2. **RunSSE 有界等待**：`sessions.rs::wait_route` 无超时无默认，分类 append 不来就挂死。改为 2s 上限后默认 upstream。
3. **变体模型匹配**：上游自己的 catalog 会广告 `variant_string_representation` 与 `legacy_slugs`，但匹配是精确 hash 查表，客户端回传变体形式即失配。改为剥离 `:variant` 后缀再查。
4. **首条无模型不应硬错**：当前返回 `Error::Protocol("first BidiAppend message must select a model")`。改为回退到 upstream。

不移植：`GetServerConfig` mock、CLI local-mode 端点面、`NameTab/NameAgent`、`request_context` 文件系统回填。这些依赖 Go 侧 `internal/agentcli` 与 mock 体系，量大且与 cursor-agent CLI 强耦合，单独立项。

## 独立 bug 移植（无 spec）

| 来源 | 上游落点 | 改法 |
|---|---|---|
| `ab1db7d` | `result/exec/render.rs::mcp`、`output.rs::mcp` | `McpResult::Approved` 当前返回 `Error::Protocol` 直接失败整轮；改为非终止（保留 `PendingExec`，不产出） |
| `7f37c57` | `tools/dispatch/exec.rs::start` | `server`/`toolName` 缺失时 `required()` 抛 `Error::Protocol`；改为接受 snake_case、从 `arguments` 提升、`user-` 前缀回退，失败降级为可重试的 tool error |
| `b7b465b`(b) | `run/engine.rs::COMPACTION_RESERVE_TOKENS` | 常量 10_000 在 500K 窗口下等于 490K 才触发；改为 `max(10_000, window / 10)` |
| `f0c2e92` | `result/gate.rs::gate_read` | `READ_BINARY_LIMIT = 32 KiB` 无条件截断，截图全废；图片放宽到 384 KiB 并加 JPEG 压缩阶梯 |
| `dcb32eb`/`dc6ab5e` | `sessions.rs::OutputHub.history` | `Vec<Bytes>` 无界；区分 transient(text/thinking delta) 与 durable(checkpoint/exec/kv)，transient 限量保留 |
| `78feb7a` | `blob_sync.rs::ensure_set` → `session.rs` | 超时 `Err` 直接失败整个 run；已完成后的超时应判为无害并正常收尾 |

已确认**无需移植**（上游已修或架构上不存在）：`0342236`（无厂商特化）、`59f238c`（图片走 CAS blob，不经文本截断）、`90f8981`（上游 prompt 规则更强）、`f418479`（`auto_compaction_partition` 已保留 request-context）、`d41d84b`（recorder 不驻留 body）、`a118525`（配置在 SQLite）、`c74a2d2`/`0a79207`（无跨会话序号、无启动期文件扫描）。