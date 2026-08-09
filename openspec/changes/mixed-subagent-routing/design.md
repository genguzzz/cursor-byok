# Design: Mixed Subagent Routing

## Evidence

抓包（2026-08-09）：

- 父 `requested_model=grok-4.5` → `mixed agent route backend=upstream`
- Child `subagent_type_name=explore`，`requested_model` 仍为 `grok-4.5`，但 `overrides=explore→752cecad0d652981`（本地 DeepSeek Pro）
- 官方 Task 显示 `model=cursor-grok-4.5-high-fast`
- Child 为独立 `conversation_id` + `conversation_group_id=parent`；客户端用 Exec `SubagentArgs/Result` 汇合，不要求父子同 backend

## Decision

**D1**: 仅当 `subagent_type_name` 非空时解析 override；父 run 不受 explore override 影响。

**D2**: `selection=model` 时用 `proto.Clone` 覆盖 `requested_model`（含 parameters）；`inherit` / `disabled` / 无匹配不改写。

**D3**: Router 在 classify 前改写并重编码 BidiAppend body（去 gzip，connect 帧或 raw proto），保证 upstream 与 local 都看到有效模型。

**D4**: Forwarder `decodeInboundIntent` 再 apply 一次，覆盖直连/改写失败后的 local 路径。

## Non-Goals

- 云端 `can_create_cloud_subagents` BC 路径
- 同会话跨后端 history 合并
- 修改客户端 bundle
