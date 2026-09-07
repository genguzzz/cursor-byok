# Cursor CLI Direct Run Compatibility

## Problem

Current Cursor CLI releases call the bidirectional Connect endpoint
`/agent.v1.AgentService/Run`. The Rust backend only owns `RunSSE`; it forwards
`Run` to Cursor upstream. A configured local model therefore appears in the
model list but is rejected upstream as an unknown model ID.

## Change

Add a local `Run` bridge that reads `AgentClientMessage` Connect frames,
selects the route from the first `AgentRunRequest`, and sends local messages
through the existing transport and conversation runtime. Its output is the
same Connect-framed `AgentServerMessage` stream used by `RunSSE`.

Official models retain the existing upstream proxy behavior.

## Validation

- Unit-test Connect-frame routing for a local model.
- Run Rust server tests and build a Linux ARM64 binary.
- Deploy to tutu and execute `agent --print --model <PC model hash>` until a
  response is returned.
