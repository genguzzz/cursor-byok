---
name: pc-model-import
description: 从 OpenAI 兼容端点发现模型、按官方价格计算相对倍率并批量导入 Cursor BYOK 数据库。用户提到快速导入模型、PC 模型组、模型倍率或更新模型价格标记时使用。
---

# PC 模型快速导入

## 适用范围

这个 skill 面向当前仓库的 Cursor BYOK 管理 API。它把 OpenAI 兼容 API 的 `/v1/models` 列表转换为数据库中的模型配置，并将模型分组为 `PC`。

目录职责：

```text
.agents/skills/pc-model-import/
├── SKILL.md                    # 触发条件、运行流程和安全约束
├── pricing.json                # 官方价格、上下文/输出限制和端点别名，不保存密钥
├── .env.example                # 本地密钥文件模板
└── scripts/import_models.py    # 发现、价格计算、导入、更新和排序
```

## 标准流程

1. 从官方价格和模型说明页面更新 `pricing.json`。没有官方价格的模型标记为“未公开定价”，没有官方上下文限制的模型不得猜测上限。
2. 将端点密钥放入本 skill 目录的隐藏 `.env`（格式见 `.env.example`），或通过环境变量 `PC_API_KEY` 提供。密钥不得写入脚本、价格文件、命令输出、日志、提交或回复。
3. 找到当前桌面服务的管理 API 地址。默认 API 路径是 `/__byok-api__/api`，端口是动态的；使用实际监听端口传给 `--control-url`。
4. 先执行 dry-run，检查远端模型、价格、倍率、未知价格项和已有配置更新：

```bash
python3 .agents/skills/pc-model-import/scripts/import_models.py \
  --base-url https://sub2.2006608.xyz/v1 \
  --control-url http://127.0.0.1:<动态端口>/__byok-api__/api \
  --pricing .agents/skills/pc-model-import/pricing.json
```

5. 用户确认或明确要求执行后，增加 `--apply`。脚本会：
   - 以 `PC + base_url + model_id` 识别已有记录；
   - 更新已有记录的全部配置字段（包括密钥变化），而不只是显示名；
   - 创建远端新增模型，不删除远端已消失的本地模型；
   - 将 `PC` 组整体排到最前面。

```bash
python3 .agents/skills/pc-model-import/scripts/import_models.py \
  --base-url https://sub2.2006608.xyz/v1 \
  --control-url http://127.0.0.1:<动态端口>/__byok-api__/api \
  --pricing .agents/skills/pc-model-import/pricing.json \
  --apply
```

可用 `--context-window-tokens` 和 `--max-completion-tokens` 覆盖导入元数据。未指定覆盖值时，脚本优先使用 `pricing.json` 中的官方模型限制；无官方限制时，才保留已有值或使用新模型默认值（200000 和 64000）。模型说明字段只展示“上下文”和“最大输出”，不暴露上游 URL。这些字段用于 Cursor 的模型目录与压缩阈值，不会被脚本直接作为 `context` 参数发给上游。

## 命名和倍率

- 分组固定为 `PC`。
- 显示名格式为 `[PC] 原始模型名 (x倍率)`。
- 基准模型固定为 `gpt-5.6-luna`。官方标准短上下文价格为每 1M 输入 token `$0.20`、输出 token `$1.20`，因此基准总价为 `$1.40`，倍率为 `1.00x`。
- 倍率按标准模式短上下文的 `(1M 输入 + 1M 输出)` 总价计算：

  `倍率 = (目标输入价 + 目标输出价) / (Luna 输入价 + Luna 输出价)`

- dry-run 会同时输出 `x倍率`、相对 Luna 的百分比、输入/输出价格以及 1M 输入 + 1M 输出的合计价格，便于比较价格差异。
- 这个口径不是端点的实际折扣或余额消耗比例；中转站可能使用不同的内部计费。长上下文、Batch、Flex、Fast 不混入该显示倍率。
- `pricing.json` 中没有价格的模型显示为“未公开定价”，并在报告中列出；不得用相似模型价格冒充官方价格。端点声明的别名映射记录在 `aliases`，其来源写入 `alias_sources`；映射后的价格仍以官方价格页为准。

## 安全和验证

- 不要在 shell 参数中直接拼接 API key；优先使用 skill 目录下本机隐藏文件 `.env`。
- 脚本不会打印 Authorization、完整请求头或数据库中的密钥。
- 导入完成后检查：PC 数量、模型 ID、显示名、`group_name`、请求端点和排序；再用管理 API 的单模型测试验证至少一个模型。
- 同时检查 `~/.cursor-byok-v3/logs/` 中最近日志的 `ERROR`、`WARN`、`Failed`、`unknown model`、`timeout` 和上游 HTTP 错误。日志中的搜索引擎 403/400 通常是外部搜索源问题，要与模型调用失败区分。
- 价格网页是时效性来源。价格变更时先修改 `pricing.json`，再重新运行脚本；脚本不会删除远端已经消失的数据库模型。
