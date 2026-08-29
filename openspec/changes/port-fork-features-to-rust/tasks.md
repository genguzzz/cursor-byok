# Tasks

## Phase 0 — 基线（已完成）

- [x] 新建工作区 `/Users/gugen/Code/cursor-byok-rust`，分支 `feat/port-to-rust-0.1.x` @ `upstream/main` (`88f258f`)
- [x] 安装 Rust toolchain（rustup，cargo 1.98.0 / rustc 1.98.0）
- [x] `cargo build --workspace` 基线通过（exit 0，3m01s）
- [x] 摸清上游架构：`server`(axum+hudsucker MITM) / `apps/desktop`(Tauri 2) / `crates/semble-core`
- [x] 四个能力域的 gap 分析

## Phase 1 — 快速修复（低风险，先落地）

- [ ] 1.1 `run/engine.rs`: compaction 触发阈值改为 `max(10_000, window / 10)`；补测试
- [ ] 1.2 `result/exec/render.rs` + `output.rs`: `McpResult::Approved` 改为非终止
- [ ] 1.3 `tools/dispatch/exec.rs`: CallMcpTool 参数归一化（snake_case / nested arguments / `user-` 前缀回退）
- [ ] 1.4 `cursor/sessions.rs::wait_route`: 加 2s 上限，超时默认 upstream
- [ ] 1.5 `bidi_append.rs`: 首条无 model_id 时回退 upstream，不再 `Error::Protocol`
- [ ] 1.6 模型匹配剥离 `:variant` 后缀
- [ ] `cargo test --workspace` 通过

## Phase 2 — codebuddy-provider

- [ ] 2.1 `model/configuration.rs`: `ModelType::CodeBuddy` + `ProviderType::CodeBuddy` + endpoint 归一化默认 `/custom`
- [ ] 2.2 新建 `provider/codebuddy.rs`: header 表（静态 + 动态 + JWT `sub` → `X-User-Id`）
- [ ] 2.3 body 装饰：`reasoning_summary`、`verbosity` 推导、gzip、禁 `Accept-Encoding`
- [ ] 2.4 user 内联图片剥离为 `<selected_images>` + Read 提示
- [ ] 2.5 `provider/router.rs::build_inner` 接线
- [ ] 2.6 单测：header 全量断言、`custom_headers` 优先级、verbosity 映射表
- [ ] `cargo test -p cursor-server` 通过

## Phase 3 — shell-background-tasks

- [x] 3.1 `codec/response.rs`: 8 KB 分片，优先换行切点，字符边界安全；`ClientExecEvent::Delta` 改为 `Vec`
- [x] 3.5 `prompt/cursor/tools.json`: Shell 描述补 `<shellId>.txt` 命名约定
- [x] 3.6 测试：分片边界（空 / 短 / 超长 / 换行偏好 / 多字节不切裂 / 单字符宽于上限）
- [ ] 3.2 终端文件读取器：`<shellId>.txt`，解析 `---` 元数据块 — **推迟**
- [ ] 3.3 后台 shell 状态注册表 + 250ms 轮询增量推流 — **推迟**
- [x] 3.4 `request/background.rs::project` 的 `TASK_PROGRESS` — **不做**，见下

### 关于 3.2 / 3.3 / 3.4 的决定

读代码后改判：上游 `background.rs::project` 里有明确注释——progress 与 reparenting 通知只是信息性事件，**客户端会把它们和真正的 finish 通知一起批量下发**。也就是说上游选的是「客户端推」而非「服务端拉」：后台 shell 结束时由 `BackgroundTaskCompletionAction` 唤醒父轮次，服务端不需要维护后台状态机。

Go 世代那套（后台状态注册表 + 250ms 轮询终端文件 + `AwaitShell` actor 定时器）是为「服务端拉」设计的。在推模型上重建它会与 `BackgroundTaskCompletion` 双写同一份状态，且 `AwaitShell` 已被上游在 `compat.rs` 显式标为移除。

代价是明确的：**后台化之后气泡不再有增量输出**，直到任务结束。这是可感知的体验回退，但属于上游既有行为，不是本次移植引入的。若要补，正确做法是单独立项，先确认上游是否有意在客户端侧补这段推流，避免我们和上游各修一半。

## Phase 4 — mixed-mode-routing（子 agent）

- [ ] 4.1 `bidi_append.rs::DecodedAppend`: 解析 `subagent_type_name` + `subagent_model_overrides`
- [ ] 4.2 有效模型解析：override 优先，含别名折叠（`generalPurpose`↔`explore`、`browserUse`↔`browser-use`）
- [ ] 4.3 bidi body 重写：re-hex `data`、按入站形状重编码、修 `Content-Length`、去 `Content-Encoding`
- [ ] 4.4 测试：官方父 + 本地 explore、本地父 + 官方 explore 两个方向

## Phase 5 — menubar-coexistence

- [ ] 5.1 `tray.rs`: 菜单项扩展（本地模式 ✓ / 状态显示 / 出站代理 ✓ / 强制恢复 / 调试 ✓ / 退出）
- [ ] 5.2 2s 轮询 `harness.status()` 刷新菜单标题与勾选态
- [ ] 5.3 控制 API：批量写 model proxy 字段（对应 Go 的 9090 代理开关）
- [ ] 5.4 macOS: 窗口隐藏时 `set_dock_visibility(false)`，仅留菜单栏
- [ ] 5.5 验证共存：菜单栏拉起窗口、关窗后菜单栏仍驻留、退出彻底清理
- [ ] 5.6 约束：菜单项全用纯文本，不设图标（避免 macOS 同级缩进）

## Phase 6 — 内存与性能

- [ ] 6.1 `sessions.rs::OutputHub`: transient/durable 分类，transient 限量保留
- [ ] 6.2 `blob_sync.rs` 超时分类：run 已完成时判无害并正常收尾
- [ ] 6.3 `result/gate.rs::gate_read`: 图片放宽到 384 KiB + JPEG 压缩阶梯

## Phase 7 — 收尾

- [ ] 7.1 `make check`（fmt + clippy -D warnings + test + npm check）
- [ ] 7.2 `make build-desktop` 产出 macOS bundle，实机验证菜单栏
- [ ] 7.3 `/review-bugbot` 复审
- [ ] 7.4 归档 openspec 变更