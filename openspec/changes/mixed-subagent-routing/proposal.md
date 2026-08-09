# Mixed Subagent Routing

## Problem

桌面混合路由只按父 run 的 `requested_model` 分流。客户端拉起的 child run（`subagent_type_name=explore` 等）常把 `requested_model` 写成父模型，而真实目标在 `subagent_model_overrides`。结果：

- 官方父 + 本地 explore → child 整单 upstream，官方改成 `cursor-grok-*`
- 本地父 + 官方 explore → child 可能仍按父渠道 hash 走 local

## Solution

对 **child run**（`subagent_type_name` 非空）在分流前把 `requested_model` 对齐到对应 override（`model` 选择），再按渠道 hash / 官方名分流；回源前重编码 Bidi body，本地 forwarder 再防御性 apply 一次。

## Scope

- `AgentRouter` + `forwarder` intent 模型解析
- 不改 Cursor.app；不合并跨后端 history
