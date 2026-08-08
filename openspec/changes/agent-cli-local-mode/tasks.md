# Tasks: Agent CLI 本地模式

## 1. OpenSpec 与目录

- [x] 建立 `openspec/changes/agent-cli-local-mode/{proposal,design,tasks}.md`
- [x] 将 `h2-agent-proxy` 改为 library 包，入口移到 `cmd/h2-agent-proxy`
- [x] 更新 `.gitignore`、README、coding-guidance 中的 `go run` 路径

## 2. CLI 模型面

- [x] `GetUsableModels` / `GetDefaultModelForCli` 暴露 modelID、displayName、aliases
- [x] resolver 匹配 displayName（config + runtime 两处）
- [x] 更新 `mocks_test.go` 与 `modelchannel` 单测

## 3. 默认入口

- [x] `internal/agentcli`：拼 `-e` / `--agent-endpoint` / `--trust`，清代理环境变量，健康检查
- [x] `cursor-local-assistant agent -- …` 子命令
- [x] 本地模式启动日志 / CLI 横幅打印 Agent endpoint

## 4. 测试

- [x] Host：healthz + GetServerConfig + 列模型 + GetMe
- [x] Host：Ask 对话 RunSSE + BidiAppend（mock provider）
- [x] `go test` 相关包全部通过

## 5. 文档

- [x] `h2-agent-proxy/LOCAL-INTERCEPT.md` 改为已落地说明
- [x] coding-guidance 写清「本地模式 vs 抓包 proxy」
