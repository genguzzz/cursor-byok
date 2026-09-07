# 任务

- [x] 为推理 delta 增加显式展示样式，并在 provider、run 和 Cursor 协议层无损传递。
- [x] 对 GPT-5 模型发出 `THINKING_STYLE_GPT5`，其他模型保持默认样式。
- [x] 将 OpenAI Responses 的推理摘要请求改为 `detailed`，并附加固定的简体中文可见摘要规则。
- [x] 添加请求体和协议编码单元测试。
- [ ] 运行 Rust 格式化、聚焦测试、server 检查及桌面 Tauri 构建。
- [ ] 安装构建出的桌面应用，并用新的实际请求验证上游及 RunSSE 的语言、摘要长度和样式。
