# 任务

- [x] 确认 PC 端点 `/v1/models` 的模型列表和 OpenAI Responses 协议。
- [x] 从 OpenAI 官方价格页整理标准短上下文输入/输出价格。
- [x] 创建 `.agents/skills/pc-model-import/SKILL.md` 和本地密钥模板。
- [x] 创建 `pricing.json`，记录基准、价格口径、官方来源和明确 alias。
- [x] 创建无第三方依赖的 `import_models.py`，支持 dry-run、批量创建、已有模型更新和 PC 组排序。
- [x] 用脚本 dry-run 验证当前端点模型和倍率输出。
- [x] 将当前 12 个 PC 模型显示名更新为倍率格式。
- [x] 验证数据库数量、PC 排序、未知价格项和至少一个模型的连通性。
- [x] 修复重复运行时的全字段更新、规范化端点匹配和价格差异报告。
