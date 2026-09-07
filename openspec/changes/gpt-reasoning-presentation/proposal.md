# GPT 推理摘要展示优化

## 背景

当前 OpenAI Responses 请求使用 `reasoning.summary: "auto"`。已验证的 GPT-5.6 Terra 流只返回短英文 `response.reasoning_summary_text.delta`，不会返回可显示的完整推理过程。服务端又把所有摘要标成默认思维样式，导致 Cursor 无法按 GPT-5 样式展示。

## 目标

- 对启用推理的 OpenAI Responses 请求请求 `detailed` 推理摘要。
- 明确要求模型将可见推理摘要写成简体中文。
- 将 GPT-5 系列模型的摘要标记为 `THINKING_STYLE_GPT5`，其他模型保持默认样式。
- 保持完整推理和加密 replay state 不可见、不可改写；只优化供应商已公开返回的摘要。

## 非目标

- 不伪造、翻译或存储模型的隐藏思维链。
- 不承诺 Cursor 客户端一定提供逐节点的可展开视图；上游仍未返回节点或完整推理文本。
- 不修改既有会话历史或 encrypted reasoning replay state。
