use crate::{
    cursor::{
        interaction,
        proto::agent::v1 as pb,
        tools::{
            edit,
            result::{self, ToolCompletion},
            runtime::{CursorToolRuntime, ExecStage, PendingExec},
        },
    },
    model::ToolCall,
    Error, Result,
};

use super::request::edit_write_request;

pub enum ClientExecEvent {
    /// Shell output can arrive in bursts far larger than the client will
    /// re-render in one go, so a single burst may fan out into several deltas.
    Delta(Vec<pb::AgentServerMessage>),
    Message(Box<pb::AgentServerMessage>),
    Completed(Box<ToolCompletion>),
    Pending,
}

/// Cursor leaves a tool bubble stale when a single delta is very large, so
/// split bursts into chunks it will actually redraw.
const SHELL_DELTA_CHUNK_LIMIT: usize = 8 * 1024;

pub async fn client_event(
    message: &pb::ExecClientMessage,
    pending: &CursorToolRuntime,
) -> Result<ClientExecEvent> {
    if pending.is_interrupted(message.id).await {
        if message.message.as_ref().is_some_and(is_terminal) {
            pending.discard_exec(message.id).await;
        }
        return Ok(ClientExecEvent::Pending);
    }
    let call = match pending.exec_call(message.id).await {
        Some(call) => call,
        None if pending.completed_call(message.id).await.is_some() => {
            return Err(Error::Protocol(format!(
                "duplicate terminal ExecClientMessage id: {}",
                message.id
            )))
        }
        None => {
            return Err(Error::Protocol(format!(
                "unknown ExecClientMessage id: {}",
                message.id
            )))
        }
    };
    let Some(wire_result) = &message.message else {
        return Ok(ClientExecEvent::Pending);
    };
    // An MCP approval only tells us the user granted permission; the real
    // result arrives in a later message.  Keep the exec pending so the tool
    // call can still complete instead of failing the whole turn.
    if is_mcp_approval(wire_result) {
        return Ok(ClientExecEvent::Pending);
    }
    let pb::exec_client_message::Message::ShellStream(stream) = wire_result else {
        let entry = take(message.id, pending).await?;
        return match entry.stage {
            ExecStage::EditRead => advance_edit(entry, wire_result, pending).await,
            ExecStage::Direct | ExecStage::DynamicMcp(_) | ExecStage::EditWrite(_) => {
                completed(entry, wire_result.clone())
            }
        };
    };
    use pb::shell_stream::Event;
    let event = match &stream.event {
        Some(Event::Stdout(stdout)) => {
            if pending.append_stdout(message.id, &stdout.data).await {
                ClientExecEvent::Delta(shell_deltas(&call, true, &stdout.data))
            } else {
                ClientExecEvent::Pending
            }
        }
        Some(Event::Stderr(stderr)) => {
            if pending.append_stderr(message.id, &stderr.data).await {
                ClientExecEvent::Delta(shell_deltas(&call, false, &stderr.data))
            } else {
                ClientExecEvent::Pending
            }
        }
        Some(Event::Start(_)) | Some(Event::HookContext(_)) => ClientExecEvent::Pending,
        Some(Event::Exit(exit)) => {
            let entry = take(message.id, pending).await?;
            let result = shell_exit_result(message, exit, &entry.stdout, &entry.stderr);
            completed(entry, pb::exec_client_message::Message::ShellResult(result))?
        }
        Some(Event::Backgrounded(backgrounded)) => {
            let entry = take(message.id, pending).await?;
            let result = shell_backgrounded_result(
                backgrounded,
                &entry.stdout,
                &entry.stderr,
                &entry.context.terminals_folder,
            );
            completed(entry, pb::exec_client_message::Message::ShellResult(result))?
        }
        Some(Event::Rejected(value)) => {
            let result = pb::ShellResult {
                result: Some(pb::shell_result::Result::Rejected(value.clone())),
                ..Default::default()
            };
            complete(
                message.id,
                pending,
                pb::exec_client_message::Message::ShellResult(result),
            )
            .await?
        }
        Some(Event::PermissionDenied(value)) => {
            let result = pb::ShellResult {
                result: Some(pb::shell_result::Result::PermissionDenied(value.clone())),
                ..Default::default()
            };
            complete(
                message.id,
                pending,
                pb::exec_client_message::Message::ShellResult(result),
            )
            .await?
        }
        Some(Event::SandboxUnsupported(value)) => {
            let result = pb::ShellResult {
                result: Some(pb::shell_result::Result::SpawnError(pb::ShellSpawnError {
                    command: value.command.clone(),
                    working_directory: value.working_directory.clone(),
                    error: value.reason.clone(),
                })),
                ..Default::default()
            };
            complete(
                message.id,
                pending,
                pb::exec_client_message::Message::ShellResult(result),
            )
            .await?
        }
        None => ClientExecEvent::Pending,
    };
    Ok(event)
}

pub async fn stream_closed(id: u32, pending: &CursorToolRuntime) -> Result<Option<ToolCompletion>> {
    if pending.is_interrupted(id).await {
        pending.discard_exec(id).await;
        return Ok(None);
    }
    let Some(entry) = pending.take_exec(id).await else {
        return Ok(None);
    };
    let error = "Cursor Exec stream closed before returning a terminal result";
    if entry.call.name.eq_ignore_ascii_case("Shell") {
        let command = entry
            .call
            .arguments
            .get("command")
            .and_then(serde_json::Value::as_str)
            .unwrap_or_default()
            .to_string();
        let working_directory = entry
            .call
            .arguments
            .get("working_directory")
            .and_then(serde_json::Value::as_str)
            .unwrap_or_default()
            .to_string();
        return Ok(Some(result::from_exec(
            entry,
            &pb::exec_client_message::Message::ShellResult(pb::ShellResult {
                result: Some(pb::shell_result::Result::SpawnError(pb::ShellSpawnError {
                    command,
                    working_directory,
                    error: error.into(),
                })),
                ..Default::default()
            }),
        )?));
    }
    let rendered = match &entry.stage {
        ExecStage::DynamicMcp(definition) => {
            interaction::render_dynamic_mcp(&entry.call, definition, false)
        }
        _ => interaction::render_tool_call(&entry.call, false)?,
    };
    Ok(Some(ToolCompletion::from_rendered(
        &entry.call,
        entry.started_at_ms,
        error.into(),
        true,
        rendered,
    )?))
}

fn is_mcp_approval(message: &pb::exec_client_message::Message) -> bool {
    matches!(
        message,
        pb::exec_client_message::Message::McpResult(result)
            if matches!(
                result.result.as_ref(),
                Some(pb::mcp_result::Result::Approved(_))
            )
    )
}

fn is_terminal(message: &pb::exec_client_message::Message) -> bool {
    use pb::{exec_client_message::Message, shell_stream::Event};

    match message {
        Message::ShellStream(stream) => matches!(
            stream.event.as_ref(),
            Some(Event::Exit(_))
                | Some(Event::Backgrounded(_))
                | Some(Event::Rejected(_))
                | Some(Event::PermissionDenied(_))
                | Some(Event::SandboxUnsupported(_))
        ),
        message if is_mcp_approval(message) => false,
        _ => true,
    }
}

async fn advance_edit(
    entry: PendingExec,
    result: &pb::exec_client_message::Message,
    registry: &CursorToolRuntime,
) -> Result<ClientExecEvent> {
    let read = match result {
        pb::exec_client_message::Message::ReadResult(result)
        | pb::exec_client_message::Message::RedactedReadResult(result) => result,
        _ => {
            return Err(Error::Protocol(format!(
                "expected ReadResult for edit tool {}",
                entry.call.name
            )))
        }
    };
    let write = match edit::after_read(&entry.call, read) {
        Ok(write) => write,
        Err(error) => {
            return Ok(ClientExecEvent::Completed(Box::new(result::edit_failure(
                entry, error,
            )?)))
        }
    };
    let id = registry
        .reserve_edit_write(
            &entry.call,
            &entry.context,
            write.clone(),
            entry.started_at_ms,
        )
        .await?;
    Ok(ClientExecEvent::Message(Box::new(edit_write_request(
        id,
        &entry.call,
        &write,
    )?)))
}

async fn complete(
    id: u32,
    pending: &CursorToolRuntime,
    result: pb::exec_client_message::Message,
) -> Result<ClientExecEvent> {
    completed(take(id, pending).await?, result)
}

async fn take(id: u32, pending: &CursorToolRuntime) -> Result<PendingExec> {
    pending
        .take_exec(id)
        .await
        .ok_or_else(|| Error::Protocol(format!("unknown terminal Exec id: {id}")))
}

fn completed(
    pending: PendingExec,
    result: pb::exec_client_message::Message,
) -> Result<ClientExecEvent> {
    Ok(ClientExecEvent::Completed(Box::new(result::from_exec(
        pending, &result,
    )?)))
}

fn shell_exit_result(
    message: &pb::ExecClientMessage,
    exit: &pb::ShellStreamExit,
    stdout: &str,
    stderr: &str,
) -> pb::ShellResult {
    let result = if exit.code == 0 && !exit.aborted {
        pb::shell_result::Result::Success(pb::ShellSuccess {
            working_directory: exit.cwd.clone(),
            exit_code: exit.code as i32,
            stdout: stdout.into(),
            stderr: stderr.into(),
            interleaved_output: Some(format!("{stdout}{stderr}")),
            local_execution_time_ms: exit
                .local_execution_time_ms
                .or(message.local_execution_time_ms),
            ..Default::default()
        })
    } else {
        pb::shell_result::Result::Failure(pb::ShellFailure {
            working_directory: exit.cwd.clone(),
            exit_code: exit.code as i32,
            stdout: stdout.into(),
            stderr: stderr.into(),
            interleaved_output: Some(format!("{stdout}{stderr}")),
            abort_reason: exit.abort_reason,
            aborted: exit.aborted,
            local_execution_time_ms: exit
                .local_execution_time_ms
                .or(message.local_execution_time_ms),
            ..Default::default()
        })
    };
    pb::ShellResult {
        result: Some(result),
        is_background: Some(false),
        ..Default::default()
    }
}

fn shell_backgrounded_result(
    backgrounded: &pb::ShellStreamBackgrounded,
    stdout: &str,
    stderr: &str,
    terminals_folder: &str,
) -> pb::ShellResult {
    pb::ShellResult {
        result: Some(pb::shell_result::Result::Success(pb::ShellSuccess {
            command: backgrounded.command.clone(),
            working_directory: backgrounded.working_directory.clone(),
            stdout: stdout.into(),
            stderr: stderr.into(),
            shell_id: Some(backgrounded.shell_id),
            pid: backgrounded.pid,
            ms_to_wait: backgrounded.ms_to_wait,
            background_reason: backgrounded.reason,
            interleaved_output: Some(format!("{stdout}{stderr}")),
            ..Default::default()
        })),
        is_background: Some(true),
        terminals_folder: (!terminals_folder.is_empty()).then(|| terminals_folder.into()),
        pid: backgrounded.pid,
        ..Default::default()
    }
}

/// Split a burst of shell output into deltas Cursor will redraw.
///
/// Cuts prefer a line boundary once past the halfway mark so a chunk rarely
/// ends mid-line.  Byte offsets are always advanced to a character boundary:
/// the payload is a proto `string`, so a split inside a multi-byte sequence
/// would be unencodable.
fn shell_deltas(call: &ToolCall, stdout: bool, content: &str) -> Vec<pb::AgentServerMessage> {
    chunk_shell_output(content, SHELL_DELTA_CHUNK_LIMIT)
        .into_iter()
        .map(|chunk| shell_delta(call, stdout, chunk))
        .collect()
}

fn chunk_shell_output(content: &str, limit: usize) -> Vec<&str> {
    if content.is_empty() {
        return Vec::new();
    }
    if content.len() <= limit {
        return vec![content];
    }
    let mut chunks = Vec::new();
    let mut rest = content;
    while rest.len() > limit {
        let mut end = limit;
        while end > 0 && !rest.is_char_boundary(end) {
            end -= 1;
        }
        // A newline cut reads better, but only when it does not shrink the
        // chunk so much that we churn out tiny messages.
        if let Some(newline) = rest[..end].rfind('\n') {
            if newline + 1 > limit / 2 {
                end = newline + 1;
            }
        }
        if end == 0 {
            // A single character wider than the limit: emit it whole rather
            // than looping forever.
            end = rest
                .char_indices()
                .nth(1)
                .map_or(rest.len(), |(index, _)| index);
        }
        let (chunk, remainder) = rest.split_at(end);
        chunks.push(chunk);
        rest = remainder;
    }
    if !rest.is_empty() {
        chunks.push(rest);
    }
    chunks
}

fn shell_delta(call: &ToolCall, stdout: bool, content: &str) -> pb::AgentServerMessage {
    let delta = if stdout {
        pb::shell_tool_call_delta::Delta::Stdout(pb::ShellToolCallStdoutDelta {
            content: content.into(),
        })
    } else {
        pb::shell_tool_call_delta::Delta::Stderr(pb::ShellToolCallStderrDelta {
            content: content.into(),
        })
    };
    interaction::server_interaction(pb::interaction_update::Message::ToolCallDelta(Box::new(
        pb::ToolCallDeltaUpdate {
            call_id: call.call_id.clone(),
            tool_call_delta: Some(Box::new(pb::ToolCallDelta {
                delta: Some(pb::tool_call_delta::Delta::ShellToolCallDelta(
                    pb::ShellToolCallDelta { delta: Some(delta) },
                )),
            })),
            model_call_id: call.model_call_id.clone(),
        },
    )))
}

#[cfg(test)]
mod tests {
    use super::{chunk_shell_output, SHELL_DELTA_CHUNK_LIMIT};

    #[test]
    fn short_output_stays_a_single_delta() {
        assert_eq!(chunk_shell_output("ok\n", SHELL_DELTA_CHUNK_LIMIT), vec!["ok\n"]);
        assert!(chunk_shell_output("", SHELL_DELTA_CHUNK_LIMIT).is_empty());
    }

    #[test]
    fn long_output_is_split_and_reassembles_exactly() {
        let content = "x".repeat(SHELL_DELTA_CHUNK_LIMIT * 2 + 7);
        let chunks = chunk_shell_output(&content, SHELL_DELTA_CHUNK_LIMIT);
        assert_eq!(chunks.len(), 3);
        assert!(chunks
            .iter()
            .all(|chunk| chunk.len() <= SHELL_DELTA_CHUNK_LIMIT));
        assert_eq!(chunks.concat(), content);
    }

    #[test]
    fn cuts_prefer_a_line_boundary_past_the_halfway_mark() {
        let content = format!("{}\n{}", "a".repeat(7), "b".repeat(10));
        let chunks = chunk_shell_output(&content, 10);
        assert_eq!(chunks[0], "aaaaaaa\n");
        assert_eq!(chunks.concat(), content);
    }

    #[test]
    fn an_early_newline_does_not_shrink_the_chunk() {
        // The newline sits below limit/2, so cutting there would produce tiny
        // deltas; the split falls back to the byte limit.
        let content = format!("a\n{}", "b".repeat(30));
        let chunks = chunk_shell_output(&content, 10);
        assert_eq!(chunks[0].len(), 10);
        assert_eq!(chunks.concat(), content);
    }

    #[test]
    fn multi_byte_characters_are_never_split() {
        // Every chunk must be valid UTF-8 on its own: the wire field is a
        // proto string, so a mid-sequence cut would be unencodable.
        let content = "中文输出".repeat(2_000);
        let chunks = chunk_shell_output(&content, SHELL_DELTA_CHUNK_LIMIT);
        assert!(chunks.len() > 1);
        assert_eq!(chunks.concat(), content);
        for chunk in &chunks {
            assert!(chunk.len() <= SHELL_DELTA_CHUNK_LIMIT);
            assert!(std::str::from_utf8(chunk.as_bytes()).is_ok());
        }
    }

    #[test]
    fn a_character_wider_than_the_limit_still_progresses() {
        let content = "中中中";
        let chunks = chunk_shell_output(content, 2);
        assert_eq!(chunks, vec!["中", "中", "中"]);
    }
}
