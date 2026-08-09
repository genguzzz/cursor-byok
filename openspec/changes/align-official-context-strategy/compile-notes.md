# 编译 / Proto 注意事项（避免拖垮 gopls）

## 原则

- **业务改动不需要重生成 19MB `aiserver_v1.pb.go`。**
- Agent 协议字段（如 `prompt_context_usage_tree`）只动：
  ```bash
  protoc -I proto --go_out=gen --go_opt=module=cursor/gen proto/agent_v1.proto
  ```
- **禁止**对 `proto/aiserver_v1.proto` 做日常 `protoc` / connect 全量重生（体积大、gopls 索引爆内存）。
- 单测优先放在**不依赖** `gen/aiserverv1` 的小包（例如 `internal/backend/promptbudget`、`cursor/prompt`）。
- 安装验收用一次 `./dev.sh install`（或 `go build -tags cli ./cmd/cli`），不要对 `./internal/backend/forwarder` 反复全量 `go test`。

## 单独补字段（比全量 regen 更好）

若 `gen/` 已有大体可用、只缺某一个 message 字段：

1. 优先只 regen `agent_v1.proto`；
2. 或手写补丁只改对应 struct / getter（确认 wire tag 与 proto 一致）；
3. 不要为了对齐 connect stub 去 regen 整个 aiserver。
