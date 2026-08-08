# Proposal: Agent CLI 本地模式

## Status

Completed

## Problem Summary

Cursor CLI（`agent`）默认把 preflight 打到 `api2.cursor.sh`、把推理打到 `agentn.global.api5.cursor.sh`（TLS+h2 `AgentService/Run`）。本仓库本地模式已经在 `127.0.0.1:18090` 提供桌面同构的 `GetServerConfig`（强制关 HTTP/2）、模型 mock、`RunSSE` + `BidiAppend`，但：

1. `h2-agent-proxy` 仍是根目录 `package main`，和 `cursor-proxy-debugger` 的「库 + `cmd/`」惯例不一致。
2. CLI 可见模型只暴露渠道 hash，`--model composer-2.5` / `agent models` 对不上本地 adapter。
3. 没有默认开启的产品入口：每次都要手写 `-e` + `--agent-endpoint`，也没有回归测试覆盖「列模型 + 对话」。

目标：**本地模式开启时，Agent CLI 默认走本仓库 backend，不经过官方 api5**。

## Scope

- 把 `h2-agent-proxy` 收成库包 + `cmd/h2-agent-proxy`（抓官方 api5 的调试工具，**不是**本地拦截层）。
- 本地模式 backend 作为 Agent CLI 的默认 endpoint（无额外开关）。
- `cursor-local-assistant agent -- …` 包装官方 `agent`，注入 `-e` / `--agent-endpoint` / `--trust`，并清掉会拐走 localhost 的代理环境变量。
- `GetUsableModels` / `GetDefaultModelForCli` 暴露 adapter `modelID`、displayName 与渠道 hash 别名；resolver 能按这些名字选渠道。
- 集成测试覆盖：列模型 RPC、GetServerConfig、一次 Ask 对话（RunSSE + BidiAppend）。

## Non-goals

- 不实现本地 H2 bidi `AgentService/Run`。
- 不修改已安装的 `agent` 二进制或 Cursor.app。
- 不把 h2-proxy 默认挂到本地模式启动路径上（那条路仍指向 Cloudflare，用于抓包对比）。
- 不伪造官方 `composer-2.5` 全局别名；只有用户把 adapter `modelID` 配成该名字时才匹配。
