# 设计：PC 模型导入

## 目录架构

```text
cursor-byok/
├── .agents/skills/pc-model-import/
│   ├── SKILL.md                 # Agent 触发规则、操作约束和命令模板
│   ├── pricing.json             # 官方价格数据与明确的模型别名
│   └── scripts/import_models.py # 端点发现、倍率计算、API 写入和排序
└── openspec/changes/pc-model-import/
    ├── proposal.md              # 背景和范围
    ├── design.md                # 本设计
    └── tasks.md                 # 实施任务
```

## 数据流

```text
OpenAI-compatible /models
        │  Authorization 只从本机环境或 skill/.env 读取
        ▼
import_models.py ── pricing.json ── 官方价格映射
        │
        ├── dry-run：输出模型、倍率、输入/输出价格和未知项
        └── --apply
              │
              ├── GET /models：按 PC + base_url + model_id 识别已有记录
              ├── PUT /models/:hash：更新已有记录的显示名和提示信息
              ├── POST /models：批量创建新增记录
              └── PUT /models/order：PC 组置前
```

## 倍率口径

官方价格单位为每 1M token。以标准模式短上下文为准，使用同等 1M 输入和 1M 输出的合计价格：

```text
multiplier(model) = (input_price + output_price)
                    / (luna_input_price + luna_output_price)
```

因此 Luna 的基准为 `$0.20 + $1.20 = $1.40`，倍率为 `1.00x`。长上下文、Batch、Flex、Fast 和中转站折扣不混入该显示倍率；价格文件保留官方来源 URL，价格发生变化时重新更新文件。

## 更新策略

- 脚本只创建或更新本组、本端点、本模型 ID 的记录。
- 远端列表中消失的本地记录保留，避免把模型下线误判为删除请求。
- 有明确官方 alias 的模型使用 alias 的价格并保留原始模型 ID；没有价格的模型使用 `x?`。
- 所有新模型使用 OpenAI Responses 兼容配置，因为该端点已验证支持 `/v1/responses`。
- 密钥不出现在 skill、pricing 文件、命令输出或报告中。
