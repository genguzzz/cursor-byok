# 设计：GPT 推理摘要展示

## 结构

```text
server/
├── src/provider/
│   ├── openai_responses.rs  # 请求详细中文摘要；为每个摘要 delta 标注模型样式
│   └── event.rs             # Provider → run 的摘要文本及展示样式事件
├── src/run/
│   ├── event.rs             # run → Cursor 输出的摘要文本及展示样式事件
│   └── model_cycle.rs       # 无损传递摘要文本和样式
└── src/cursor/
    ├── conversation/output.rs # 将样式传入协议编码器
    └── protocol/events.rs     # 转为 Cursor ThinkingStyle 枚举
```

## 数据流

```text
OpenAI Responses body
  reasoning.summary = "detailed"
  instructions += 中文可见摘要规则
        │
        ▼
response.reasoning_summary_text.delta
        │ text + Gpt5
        ▼
ModelEvent → RunEvent → ConversationOutput
        │
        ▼
ThinkingDeltaUpdate { text, thinking_style: GPT5 }
```

`reasoning.encrypted_content` 继续仅作为后续 Responses 请求的 replay state。它既不进入摘要事件，也不写入 Cursor 的显示内容。

## 稳定性

中文摘要规则是 OpenAI Responses 序列化时附加的固定文本；同一会话的每一个请求均使用相同的 instructions 字节串。它不修改 canonical history、请求上下文、checkpoint 或 replay state，因此不破坏已有 provider-history 前缀。

## 样式分类

模型 ID 大小写无关且以 `gpt-5` 开头时使用 `Gpt5`；所有其他模型使用 `Default`。样式只影响下游 Cursor 呈现，文本和持久化推理内容保持不变。
