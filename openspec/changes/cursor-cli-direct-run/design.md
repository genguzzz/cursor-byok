# Design

```text
Cursor CLI
  └── POST /agent.v1.AgentService/Run (Connect bidirectional stream)
        ├── first AgentRunRequest selects a configured model
        │     └── Rust transport → conversation runtime → configured provider
        │           └── Connect-framed AgentServerMessage response stream
        └── otherwise
              └── existing Cursor upstream proxy
```

`Run` and `RunSSE` share the existing `TransportRegistry` and conversation
runtime. The direct bridge assigns one internal request ID to the stream,
converts each incoming `AgentClientMessage` into the existing transport
append command, and subscribes to the existing output hub.

The bridge only changes local model routing. It does not duplicate model
configuration, provider execution, or tool-result processing.
