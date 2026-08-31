//! Cursor AwaitToolCall rendering for AwaitShell.

use crate::{
    cursor::protocol::proto::agent::v1 as pb,
    model::{ToolCall, ToolResult},
    Result,
};

use super::ToolCompletion;
use crate::cursor::tools::await_shell::{
    build_await_args, build_await_result, AwaitShellArgs, AwaitShellOutcome,
};

pub(crate) fn complete(
    call: &ToolCall,
    started_at_ms: u64,
    args: &AwaitShellArgs,
    outcome: &AwaitShellOutcome,
) -> Result<ToolCompletion> {
    let content = serde_json::to_string(outcome)?;
    Ok(ToolCompletion::new(
        call,
        started_at_ms,
        ToolResult {
            call_id: call.call_id.clone(),
            content,
            is_error: matches!(outcome.status.as_str(), "error" | "unknown"),
            image: None,
        },
        pb::tool_call::Tool::AwaitToolCall(pb::AwaitToolCall {
            args: Some(build_await_args(args)),
            result: Some(build_await_result(outcome)),
        }),
    ))
}
