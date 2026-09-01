//! Parses and persists command execution output.
use crate::{cursor::protocol::proto::agent::v1 as pb, model::ToolCall, Error, Result};

pub(super) fn output(
    message: &pb::exec_client_message::Message,
    call: &ToolCall,
) -> Result<(String, bool)> {
    use pb::exec_client_message::Message;
    match message {
        Message::ShellResult(value) | Message::MiniSweAgentBashResult(value) => shell(value),
        Message::ReadResult(value) | Message::RedactedReadResult(value) => read(value),
        Message::WriteResult(value) => write(value),
        Message::DeleteResult(value) => delete(value),
        Message::GrepResult(value) => grep(value),
        Message::DiagnosticsResult(value) => diagnostics(value),
        Message::McpResult(value) => mcp(value),
        Message::ReadMcpResourceExecResult(value) => read_mcp(value),
        Message::SubagentResult(value) => task(value, call),
        Message::ConversationSearchResult(value) => conversation_search(value),
        Message::ListMcpResourcesExecResult(value) => list_mcp_resources(value),
        Message::WriteShellStdinResult(value) => write_shell_stdin(value),
        _ => Err(Error::Protocol(
            "unsupported terminal ExecClientMessage".into(),
        )),
    }
}

fn shell(value: &pb::ShellResult) -> Result<(String, bool)> {
    use pb::shell_result::Result as R;
    let output = match value.result.as_ref().ok_or_else(|| missing("shell"))? {
        R::Success(success) if value.is_background == Some(true) => {
            let mut fields = vec![format!("shell_id={}", success.shell_id.unwrap_or_default())];
            if let Some(pid) = success.pid.or(value.pid) {
                fields.push(format!("pid={pid}"));
            }
            if let Some(folder) = value.terminals_folder.as_deref().filter(|v| !v.is_empty()) {
                fields.push(format!("terminals_folder={folder}"));
            }
            let output = streams(&success.stdout, &success.stderr);
            let prefix = format!("shell running in background {}", fields.join(" "));
            return Ok((
                if output == "shell completed without output" {
                    prefix
                } else {
                    format!("{prefix}\n{output}")
                },
                false,
            ));
        }
        R::Success(success) => return Ok((streams(&success.stdout, &success.stderr), false)),
        R::Failure(failure) => streams(&failure.stdout, &failure.stderr),
        R::Timeout(timeout) => format!(
            "shell timed out after {}ms in {}",
            timeout.timeout_ms, timeout.working_directory
        ),
        R::Rejected(rejected) => rejected.reason.clone(),
        R::SpawnError(error) => error.error.clone(),
        R::PermissionDenied(denied) => denied.error.clone(),
    };
    Ok((output, true))
}

fn streams(stdout: &str, stderr: &str) -> String {
    match (stdout.is_empty(), stderr.is_empty()) {
        (false, false) => format!("{stdout}\n\n<stderr>\n{stderr}\n</stderr>"),
        (false, true) => stdout.into(),
        (true, false) => stderr.into(),
        (true, true) => "shell completed without output".into(),
    }
}

fn read(value: &pb::ReadResult) -> Result<(String, bool)> {
    use pb::{read_result::Result as R, read_success::Output};
    match value.result.as_ref().ok_or_else(|| missing("read"))? {
        R::Success(success) => Ok((
            match success.output.as_ref() {
                Some(Output::Content(text)) => text.clone(),
                Some(Output::Data(bytes)) => format!("read binary bytes={}", bytes.len()),
                None => format!("read success path={}", success.path),
            },
            false,
        )),
        R::Error(error) => Ok((error.error.clone(), true)),
        R::Rejected(rejected) => Ok((rejected.reason.clone(), true)),
        R::FileNotFound(value) => Ok((format!("file not found: {}", value.path), true)),
        R::PermissionDenied(value) => Ok((format!("permission denied: {}", value.path), true)),
        R::InvalidFile(value) => Ok((value.reason.clone(), true)),
    }
}

fn write(value: &pb::WriteResult) -> Result<(String, bool)> {
    use pb::write_result::Result as R;
    match value.result.as_ref().ok_or_else(|| missing("write"))? {
        R::Success(success) => Ok((
            success.file_content_after_write.clone().unwrap_or_else(|| {
                format!(
                    "write success path={} lines={}",
                    success.path, success.lines_created
                )
            }),
            false,
        )),
        R::PermissionDenied(value) => Ok((value.error.clone(), true)),
        R::NoSpace(value) => Ok((format!("no space left: {}", value.path), true)),
        R::Error(value) => Ok((value.error.clone(), true)),
        R::Rejected(value) => Ok((value.reason.clone(), true)),
    }
}

fn delete(value: &pb::DeleteResult) -> Result<(String, bool)> {
    use pb::delete_result::Result as R;
    match value.result.as_ref().ok_or_else(|| missing("delete"))? {
        R::Success(value) => Ok((format!("delete success path={}", value.path), false)),
        R::FileNotFound(value) => Ok((format!("file not found: {}", value.path), true)),
        R::NotFile(value) => Ok((format!("not file: {}", value.path), true)),
        R::PermissionDenied(value) => Ok((value.client_visible_error.clone(), true)),
        R::FileBusy(value) => Ok((format!("file busy: {}", value.path), true)),
        R::Rejected(value) => Ok((value.reason.clone(), true)),
        R::Error(value) => Ok((value.error.clone(), true)),
    }
}

fn grep(value: &pb::GrepResult) -> Result<(String, bool)> {
    use pb::grep_result::Result as R;
    match value.result.as_ref().ok_or_else(|| missing("grep"))? {
        R::Success(value) => Ok((grep_success(value), false)),
        R::Error(value) => Ok((value.error.clone(), true)),
    }
}

fn grep_success(value: &pb::GrepSuccess) -> String {
    let mut lines = Vec::new();
    if let Some(result) = &value.active_editor_result {
        grep_union(result, &mut lines);
    }
    let mut workspaces = value.workspace_results.iter().collect::<Vec<_>>();
    workspaces.sort_unstable_by_key(|(name, _)| *name);
    for (_, result) in workspaces {
        grep_union(result, &mut lines);
    }
    if lines.is_empty() {
        format!(
            "No matches found for pattern `{}` in {}",
            value.pattern, value.path
        )
    } else {
        lines.join("\n")
    }
}

fn grep_union(value: &pb::GrepUnionResult, lines: &mut Vec<String>) {
    use pb::grep_union_result::Result as R;
    match value.result.as_ref() {
        Some(R::Files(value)) => {
            lines.extend(value.files.iter().cloned());
            grep_truncation(
                value.client_truncated,
                value.ripgrep_truncated,
                value.total_files,
                "files",
                lines,
            );
        }
        Some(R::Count(value)) => {
            lines.extend(
                value
                    .counts
                    .iter()
                    .map(|count| format!("{}:{}", count.file, count.count)),
            );
            grep_truncation(
                value.client_truncated,
                value.ripgrep_truncated,
                value.total_matches,
                "matches",
                lines,
            );
        }
        Some(R::Content(value)) => {
            for file in &value.matches {
                lines.extend(file.matches.iter().map(|matched| {
                    let separator = if matched.is_context_line { '-' } else { ':' };
                    let truncated = if matched.content_truncated {
                        " [line truncated]"
                    } else {
                        ""
                    };
                    format!(
                        "{}{separator}{}{separator}{}{truncated}",
                        file.file, matched.line_number, matched.content
                    )
                }));
            }
            grep_truncation(
                value.client_truncated,
                value.ripgrep_truncated,
                value.total_matched_lines,
                "matched lines",
                lines,
            );
        }
        None => {}
    }
}

fn grep_truncation(
    client_truncated: bool,
    ripgrep_truncated: bool,
    total: i32,
    unit: &str,
    lines: &mut Vec<String>,
) {
    if client_truncated || ripgrep_truncated {
        lines.push(format!("[Results truncated; {total} total {unit}]"));
    }
}

fn diagnostics(value: &pb::DiagnosticsResult) -> Result<(String, bool)> {
    use pb::diagnostics_result::Result as R;
    match value
        .result
        .as_ref()
        .ok_or_else(|| missing("diagnostics"))?
    {
        R::Success(value) => Ok((diagnostics_success(value), false)),
        R::Error(value) => Ok((value.error.clone(), true)),
        R::Rejected(value) => Ok((value.reason.clone(), true)),
        R::FileNotFound(value) => Ok((format!("file not found: {}", value.path), true)),
        R::PermissionDenied(value) => Ok((format!("permission denied: {}", value.path), true)),
    }
}

fn diagnostics_success(value: &pb::DiagnosticsSuccess) -> String {
    if value.diagnostics.is_empty() {
        return format!("No diagnostics found in {}", value.path);
    }
    let mut lines = value
        .diagnostics
        .iter()
        .map(|diagnostic| {
            let location = diagnostic_location(&value.path, diagnostic.range.as_ref());
            let mut labels = vec![diagnostic_severity(diagnostic.severity)];
            if !diagnostic.source.is_empty() {
                labels.push(diagnostic.source.as_str());
            }
            if !diagnostic.code.is_empty() {
                labels.push(diagnostic.code.as_str());
            }
            if diagnostic.is_stale {
                labels.push("stale");
            }
            format!(
                "{}: [{}] {}",
                location,
                labels.join(" "),
                diagnostic.message
            )
        })
        .collect::<Vec<_>>();
    if value.total_diagnostics != value.diagnostics.len() as i32 {
        lines.push(format!(
            "[Reported {} diagnostics; received {} details]",
            value.total_diagnostics,
            value.diagnostics.len()
        ));
    }
    lines.join("\n")
}

fn diagnostic_location(path: &str, range: Option<&pb::Range>) -> String {
    let Some(range) = range else {
        return path.into();
    };
    let Some(start) = &range.start else {
        return path.into();
    };
    let mut location = format!(
        "{}:{}:{}",
        path,
        start.line.saturating_add(1),
        start.column.saturating_add(1)
    );
    if let Some(end) = &range.end {
        location.push_str(&format!(
            "-{}:{}",
            end.line.saturating_add(1),
            end.column.saturating_add(1)
        ));
    }
    location
}

fn diagnostic_severity(value: i32) -> &'static str {
    match pb::DiagnosticSeverity::try_from(value) {
        Ok(pb::DiagnosticSeverity::Error) => "error",
        Ok(pb::DiagnosticSeverity::Warning) => "warning",
        Ok(pb::DiagnosticSeverity::Information) => "information",
        Ok(pb::DiagnosticSeverity::Hint) => "hint",
        Ok(pb::DiagnosticSeverity::Unspecified) | Err(_) => "diagnostic",
    }
}

fn mcp(value: &pb::McpResult) -> Result<(String, bool)> {
    use pb::mcp_result::Result as R;
    match value.result.as_ref().ok_or_else(|| missing("mcp"))? {
        R::Success(value) => Ok((mcp_content(value)?, value.is_error)),
        R::Error(value) => Ok((value.error.clone(), true)),
        R::Rejected(value) => Ok((value.reason.clone(), true)),
        R::PermissionDenied(value) => Ok((value.error.clone(), true)),
        R::ToolNotFound(value) => Ok((format!("MCP tool not found: {}", value.name), true)),
        R::ServerNotFound(value) => Ok((format!("MCP server not found: {}", value.name), true)),
        R::Approved(_) => Err(Error::Protocol("MCP approval is not terminal".into())),
    }
}

fn mcp_content(success: &pb::McpSuccess) -> Result<String> {
    let mut content = Vec::new();
    for item in &success.content {
        match item.content.as_ref() {
            Some(pb::mcp_tool_result_content_item::Content::Text(text)) => {
                if !text.text.is_empty() {
                    content.push(text.text.clone());
                }
                if let Some(location) = &text.output_location {
                    content.push(format!(
                        "MCP output file: {} ({} bytes, {} lines)",
                        location.file_path, location.size_bytes, location.line_count
                    ));
                }
            }
            Some(pb::mcp_tool_result_content_item::Content::Image(image)) => content.push(format!(
                "MCP image: {} ({} bytes)",
                image.mime_type,
                image.data.len()
            )),
            None => {}
        }
    }
    if let Some(structured) = &success.structured_content {
        let value = serde_json::Value::Object(
            structured
                .fields
                .iter()
                .map(|(key, value)| (key.clone(), super::super::prost_json(value)))
                .collect(),
        );
        content.push(serde_json::to_string_pretty(&value)?);
    }
    Ok(if content.is_empty() {
        "MCP tool completed without content".into()
    } else {
        content.join("\n\n")
    })
}

fn read_mcp(value: &pb::ReadMcpResourceExecResult) -> Result<(String, bool)> {
    use pb::read_mcp_resource_exec_result::Result as R;
    match value
        .result
        .as_ref()
        .ok_or_else(|| missing("read MCP resource"))?
    {
        R::Success(value) => Ok((
            match value.content.as_ref() {
                Some(pb::read_mcp_resource_success::Content::Text(text)) => text.clone(),
                Some(pb::read_mcp_resource_success::Content::Blob(blob)) => {
                    format!("read MCP resource blob={}", blob.len())
                }
                None => format!("read MCP resource uri={}", value.uri),
            },
            false,
        )),
        R::Error(value) => Ok((value.error.clone(), true)),
        R::Rejected(value) => Ok((value.reason.clone(), true)),
        R::NotFound(value) => Ok((format!("MCP resource not found: {}", value.uri), true)),
    }
}

fn task(value: &pb::SubagentResult, call: &ToolCall) -> Result<(String, bool)> {
    use pb::subagent_result::Result as R;
    match value.result.as_ref().ok_or_else(|| missing("subagent"))? {
        R::Success(value) if creates_subagent(call) => {
            let name = call
                .arguments
                .get("description")
                .and_then(serde_json::Value::as_str)
                .filter(|name| !name.is_empty())
                .ok_or_else(|| Error::Protocol("Task call is missing description".into()))?;
            if value.agent_id.is_empty() {
                return Err(Error::Protocol("Task result is missing agent_id".into()));
            }
            let identity = format!("Subagent name: {name}\nSubagent ID: {}", value.agent_id);
            let content = value
                .final_message
                .as_deref()
                .filter(|message| !message.is_empty())
                .map_or(identity.clone(), |message| {
                    format!("{identity}\n\n{message}")
                });
            Ok((content, false))
        }
        R::Success(value) => Ok((value.final_message.clone().unwrap_or_default(), false)),
        R::Error(value) => Ok((value.error.clone(), true)),
    }
}

fn creates_subagent(call: &ToolCall) -> bool {
    matches!(
        call.arguments
            .get("resume")
            .and_then(serde_json::Value::as_str),
        None | Some("self")
    )
}

fn conversation_search(value: &pb::ConversationSearchResult) -> Result<(String, bool)> {
    use pb::conversation_search_result::Result as R;
    match value.result.as_ref().ok_or_else(|| missing("conversation search"))? {
        R::Success(success) => Ok((conversation_search_success(success), false)),
        R::Error(value) => Ok((value.error.clone(), true)),
    }
}

fn conversation_search_success(value: &pb::ConversationSearchSuccess) -> String {
    if value.hits.is_empty() {
        return "No matching conversations found.".into();
    }
    let mut lines = vec![format!("Found {} conversation(s):", value.hits.len())];
    for (index, hit) in value.hits.iter().enumerate() {
        lines.push(format!(
            "{}. [{}] {} ({})",
            index + 1,
            conversation_search_source(hit.source),
            hit.title,
            hit.conversation_id
        ));
        if hit.updated_at_ms > 0 {
            lines.push(format!("   updated_at_ms={}", hit.updated_at_ms));
        }
        if let Some(snippet) = hit.snippet.as_deref().filter(|snippet| !snippet.is_empty()) {
            lines.push(format!("   snippet: {snippet}"));
        }
    }
    let mut notes = Vec::new();
    if value.truncated {
        notes.push("truncated");
    }
    if value.partial {
        notes.push("partial");
    }
    if value.rebuilding {
        notes.push("rebuilding");
    }
    if !notes.is_empty() {
        lines.push(format!("[{}]", notes.join(", ")));
    }
    lines.join("\n")
}

fn conversation_search_source(value: i32) -> &'static str {
    match pb::ConversationSearchSource::try_from(value) {
        Ok(pb::ConversationSearchSource::Local) => "local",
        Ok(pb::ConversationSearchSource::CloudCache) => "cloud_cache",
        Ok(pb::ConversationSearchSource::Unspecified) | Err(_) => "unknown",
    }
}

fn list_mcp_resources(value: &pb::ListMcpResourcesExecResult) -> Result<(String, bool)> {
    use pb::list_mcp_resources_exec_result::Result as R;
    match value
        .result
        .as_ref()
        .ok_or_else(|| missing("list MCP resources"))?
    {
        R::Success(success) => Ok((list_mcp_resources_success(success), false)),
        R::Error(value) => Ok((value.error.clone(), true)),
        R::Rejected(value) => Ok((value.reason.clone(), true)),
    }
}

fn list_mcp_resources_success(value: &pb::ListMcpResourcesSuccess) -> String {
    if value.resources.is_empty() {
        return "No MCP resources found.".into();
    }
    value
        .resources
        .iter()
        .map(|resource| {
            let mut parts = vec![format!("{} ({})", resource.uri, resource.server)];
            if let Some(name) = resource.name.as_deref().filter(|name| !name.is_empty()) {
                parts.push(format!("name: {name}"));
            }
            if let Some(description) = resource
                .description
                .as_deref()
                .filter(|description| !description.is_empty())
            {
                parts.push(format!("description: {description}"));
            }
            if let Some(mime_type) = resource
                .mime_type
                .as_deref()
                .filter(|mime_type| !mime_type.is_empty())
            {
                parts.push(format!("mime_type: {mime_type}"));
            }
            parts.join(" | ")
        })
        .collect::<Vec<_>>()
        .join("\n")
}

fn write_shell_stdin(value: &pb::WriteShellStdinResult) -> Result<(String, bool)> {
    use pb::write_shell_stdin_result::Result as R;
    match value
        .result
        .as_ref()
        .ok_or_else(|| missing("write shell stdin"))?
    {
        R::Success(success) => Ok((
            format!(
                "wrote stdin to shell_id={} terminal_file_length_before_input_written={}",
                success.shell_id, success.terminal_file_length_before_input_written
            ),
            false,
        )),
        R::Error(value) => Ok((value.error.clone(), true)),
    }
}

fn missing(name: &str) -> Error {
    Error::Protocol(format!("{name} returned no result"))
}

#[cfg(test)]
mod tests {
    use super::{conversation_search_success, list_mcp_resources_success, write_shell_stdin};
    use crate::cursor::protocol::proto::agent::v1 as pb;

    #[test]
    fn conversation_search_success_formats_hits_for_the_model() {
        let text = conversation_search_success(&pb::ConversationSearchSuccess {
            hits: vec![pb::ConversationSearchHit {
                conversation_id: "conv-1".into(),
                title: "BYOK tools".into(),
                source: pb::ConversationSearchSource::Local as i32,
                updated_at_ms: 1_700_000_000_000,
                snippet: Some("SearchConversations missing".into()),
            }],
            truncated: true,
            partial: false,
            rebuilding: false,
        });
        assert!(text.contains("BYOK tools"));
        assert!(text.contains("conv-1"));
        assert!(text.contains("SearchConversations missing"));
        assert!(text.contains("[truncated]"));
    }

    #[test]
    fn list_mcp_resources_success_formats_resources_for_the_model() {
        let text = list_mcp_resources_success(&pb::ListMcpResourcesSuccess {
            resources: vec![pb::list_mcp_resources_exec_result::McpResource {
                uri: "note://vault/readme".into(),
                name: Some("readme".into()),
                description: Some("Vault readme".into()),
                mime_type: Some("text/markdown".into()),
                server: "user-obsidian".into(),
                annotations: Default::default(),
            }],
        });
        assert!(text.contains("note://vault/readme"));
        assert!(text.contains("user-obsidian"));
        assert!(text.contains("Vault readme"));
    }

    #[test]
    fn write_shell_stdin_success_formats_confirmation_for_the_model() {
        let (text, is_error) =
            write_shell_stdin(&pb::WriteShellStdinResult {
                result: Some(pb::write_shell_stdin_result::Result::Success(
                    pb::WriteShellStdinSuccess {
                        shell_id: 7,
                        terminal_file_length_before_input_written: 42,
                    },
                )),
            })
            .unwrap();
        assert!(!is_error);
        assert!(text.contains("shell_id=7"));
        assert!(text.contains("terminal_file_length_before_input_written=42"));
    }
}
