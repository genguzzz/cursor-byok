# Port Fork Features to the Rust Rewrite

## Problem

上游 `leookun/cursor-byok` 在 `4053a7f refactor: rebuild desktop app with Tauri` 之后把整个项目从 Go 重写为 **Rust + Tauri**（`server/` 为 axum 后端，`apps/desktop` 为 Tauri 壳，`crates/semble-core` 为检索引擎）。当前上游 tip 为 `88f258f`（`v0.1.5-beta.1-23`）。

本 fork（`main`）积累了 155 个本地提交，全部落在已被上游删除的 Go 代码树上。老的 `merge/upstream-0.0.48` 路线只能停留在 Go 世代的 `v0.0.49` 归档分支，无法再吸收上游演进。因此改为**在新工作区把本地能力移植到 Rust 世代**。

已完成的基线核对：新工作区 `/Users/gugen/Code/cursor-byok-rust`（分支 `feat/port-to-rust-0.1.x`，基于 `upstream/main`）`cargo build --workspace` 通过（3m01s，exit 0）。

## Solution

按四个能力域移植，每域一份 spec：

1. **menubar-coexistence** — 保留 macOS 菜单栏常驻入口，与官方 Tauri 桌面窗口**共存**：菜单栏可拉起桌面窗口、可把窗口收进后台而菜单栏继续驻留。
2. **codebuddy-provider** — 新增 `ProviderType::CodeBuddy`，携带完整的 CodeBuddy/CLI 出站 header 集合与 body 约定。
3. **shell-background-tasks** — 后台 shell 的服务端状态、终端文件回读、增量输出分片。
4. **mixed-mode-routing** — 官方 Cursor 后端与本地 BYOK 双模式共存的补齐项（子 agent 路由、变体模型匹配、RunSSE 有界等待）。

另有一批**独立的 bug 移植**（不单独立 spec，见 `tasks.md`）：MCP `Approved` 非终止处理、MCP 参数归一化、compaction 触发阈值、Read 图片压缩、`OutputHub` 历史无界增长、checkpoint 超时分类。

## Scope

- 目标分支：`feat/port-to-rust-0.1.x`（新工作区），不动 `main`
- 涉及：`server/src/{provider,model,cursor,harness}`、`apps/desktop/src-tauri`、`server/prompt/cursor`
- 不做：Go 代码树的任何继续开发；`merge/upstream-0.0.48` 路线就地封存
- 不做：官方 identity 立场的翻转（上游当前伪造 Ultra，本 fork 原先坚持真实身份）——单独决策，不在本次范围