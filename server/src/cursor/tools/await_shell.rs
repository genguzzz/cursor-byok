//! AwaitShell argument decoding, terminal-file snapshotting, and proto mapping.
//!
//! AwaitShell is the official Cursor wait primitive. It either blocks on a
//! backgrounded shell id (polling the terminal file for completion or a regex
//! match) or, when no id is supplied, performs a pure sleep for the full
//! `block_until_ms` window. This mirrors the official semantics described in
//! the Go reference implementation `await_shell_tool.go` and
//! `docs/cursor-official-capability-matrix.md`.

use serde::Serialize;
use serde_json::Value;

use crate::{cursor::protocol::proto::agent::v1 as pb, model::ToolCall, Error, Result};

/// Official maximum block duration: 7140000ms (119 minutes).
pub(crate) const MAX_BLOCK_UNTIL_MS: i64 = 7_140_000;
/// Poll cadence used while waiting on a backgrounded shell id.
pub(crate) const POLL_INTERVAL_MS: u64 = 500;
/// Maximum bytes of terminal output returned to the model.
pub(crate) const OUTPUT_LIMIT: usize = 16 * 1024;

/// `wake_reason` value emitted when the wait is cut short because a new user
/// message is arriving (the official client uses `context_injection`).
pub(crate) const WAKE_REASON_CONTEXT_INJECTION: &str = "context_injection";

#[derive(Debug, Clone)]
pub(crate) struct AwaitShellArgs {
    pub shell_id: String,
    pub block_until_ms: i64,
    pub block_until_explicit: bool,
    pub pattern: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "snake_case")]
pub(crate) struct AwaitShellOutcome {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub shell_id: String,
    pub status: String,
    pub matched: bool,
    pub timed_out: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub exit_code: Option<i32>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub stdout: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub stderr: String,
    #[serde(skip_serializing_if = "is_zero")]
    pub stdout_offset: u64,
    #[serde(skip_serializing_if = "is_zero")]
    pub stderr_offset: u64,
    #[serde(skip_serializing_if = "is_zero")]
    pub runtime_ms: u64,
    #[serde(skip_serializing_if = "is_zero")]
    pub output_length: u64,
    #[serde(skip_serializing_if = "is_false")]
    pub regex_requested: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub regex_match: Option<String>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub message: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub wake_reason: Option<String>,
}

pub(crate) fn decode_args(call: &ToolCall) -> Result<AwaitShellArgs> {
    let shell_id = string_arg(call, "shell_id");
    let task_id = string_arg(call, "task_id");
    let shell_id = if shell_id.is_empty() { task_id } else { shell_id };
    let pattern = string_arg(call, "pattern");

    let block_until_explicit = call.arguments.get("block_until_ms").is_some();
    let block_until_ms = match call.arguments.get("block_until_ms") {
        None => 30_000,
        Some(value) => match value.as_i64() {
            Some(integer) => integer,
            None => {
                let value = value.as_f64().ok_or_else(|| {
                    Error::Protocol("AwaitShell block_until_ms must be an integer".into())
                })?;
                if !value.is_finite() || value.fract() != 0.0 {
                    return Err(Error::Protocol(
                        "AwaitShell block_until_ms must be an integer".into(),
                    ));
                }
                if value < i64::MIN as f64 || value > i64::MAX as f64 {
                    return Err(Error::Protocol(
                        "AwaitShell block_until_ms is out of range".into(),
                    ));
                }
                value as i64
            }
        },
    };
    if block_until_ms < 0 {
        return Err(Error::Protocol(
            "AwaitShell block_until_ms is out of range".into(),
        ));
    }
    let block_until_ms = block_until_ms.min(MAX_BLOCK_UNTIL_MS);
    if block_until_ms == 0 && shell_id.is_empty() {
        return Err(Error::Protocol(
            "AwaitShell shell_id is required when block_until_ms is 0".into(),
        ));
    }
    Ok(AwaitShellArgs {
        shell_id,
        block_until_ms,
        block_until_explicit,
        pattern,
    })
}

/// Snapshot the awaited job now. For a missing shell id this reports the pure
/// "waited" state; the caller is responsible for actually sleeping.
pub(crate) fn outcome(args: &AwaitShellArgs, terminals_folder: &str) -> AwaitShellOutcome {
    if args.shell_id.is_empty() {
        return AwaitShellOutcome {
            shell_id: String::new(),
            status: "waited".into(),
            matched: false,
            timed_out: false,
            exit_code: None,
            stdout: String::new(),
            stderr: String::new(),
            stdout_offset: 0,
            stderr_offset: 0,
            runtime_ms: args.block_until_ms.max(0) as u64,
            output_length: 0,
            regex_requested: !args.pattern.is_empty(),
            regex_match: None,
            message: format!("waited {}ms", args.block_until_ms.max(0)),
            wake_reason: None,
        };
    }

    let Some(snapshot) = read_terminal_file(terminals_folder, &args.shell_id) else {
        return AwaitShellOutcome {
            shell_id: args.shell_id.clone(),
            status: "unknown".into(),
            matched: false,
            timed_out: false,
            exit_code: None,
            stdout: String::new(),
            stderr: String::new(),
            stdout_offset: 0,
            stderr_offset: 0,
            runtime_ms: 0,
            output_length: 0,
            regex_requested: !args.pattern.is_empty(),
            regex_match: None,
            message: "unknown or expired shell_id".into(),
            wake_reason: None,
        };
    };

    let terminal = snapshot.exit_code.is_some();
    let status = if terminal {
        "completed"
    } else if snapshot.output.is_empty() {
        "backgrounded"
    } else {
        "running"
    };
    let runtime_ms = if terminal {
        snapshot.elapsed_ms.or(snapshot.running_for_ms).unwrap_or(0)
    } else {
        snapshot.running_for_ms.unwrap_or(0)
    };

    let (matched, regex_match) = match pattern_match(&args.pattern, &snapshot.output) {
        Ok(value) => value,
        Err(error) => {
            return AwaitShellOutcome {
                shell_id: args.shell_id.clone(),
                status: "error".into(),
                matched: false,
                timed_out: false,
                exit_code: snapshot.exit_code,
                stdout: truncate_output(&snapshot.output),
                stderr: String::new(),
                stdout_offset: 0,
                stderr_offset: 0,
                runtime_ms,
                output_length: snapshot.output.len() as u64,
                regex_requested: !args.pattern.is_empty(),
                regex_match: None,
                message: error,
                wake_reason: None,
            }
        }
    };

    let timed_out = args.block_until_ms > 0 && !matched && !terminal;
    AwaitShellOutcome {
        shell_id: args.shell_id.clone(),
        status: status.into(),
        matched,
        timed_out,
        exit_code: snapshot.exit_code,
        stdout: truncate_output(&snapshot.output),
        stderr: String::new(),
        stdout_offset: 0,
        stderr_offset: 0,
        runtime_ms,
        output_length: snapshot.output.len() as u64,
        regex_requested: !args.pattern.is_empty(),
        regex_match,
        message: String::new(),
        wake_reason: None,
    }
}

pub(crate) fn is_terminal(outcome: &AwaitShellOutcome) -> bool {
    outcome.matched
        || matches!(
            outcome.status.as_str(),
            "completed" | "rejected" | "permission_denied" | "transport_closed" | "error" | "unknown"
        )
}

pub(crate) fn build_await_args(args: &AwaitShellArgs) -> pb::AwaitArgs {
    pb::AwaitArgs {
        task_id: args.shell_id.clone(),
        block_until_ms: args
            .block_until_explicit
            .then_some(args.block_until_ms.max(0) as u32),
        regex: (!args.pattern.is_empty()).then(|| args.pattern.clone()),
    }
}

pub(crate) fn build_await_result(outcome: &AwaitShellOutcome) -> pb::AwaitResult {
    use pb::await_result::Result;
    let result = match outcome.status.as_str() {
        "error" | "unknown" => Result::Error(pb::AwaitError {
            error: if outcome.message.is_empty() {
                "unknown or expired shell_id".into()
            } else {
                outcome.message.clone()
            },
        }),
        // Official pure wait (no shell_id) surfaces as an AwaitSuccess with an
        // empty task_id, so the client renders "Slept for Ns" instead of
        // "Task still running".
        "waited" => Result::Success(pb::AwaitSuccess {
            await_result: Some(pb::await_success::AwaitResult::Complete(
                complete(outcome),
            )),
        }),
        "completed" | "rejected" | "permission_denied" | "transport_closed"
            if !outcome.matched =>
        {
            Result::Complete(complete(outcome))
        }
        _ if outcome.matched => Result::Complete(complete(outcome)),
        _ => Result::StillRunning(still_running(outcome)),
    };
    pb::AwaitResult { result: Some(result) }
}

fn complete(outcome: &AwaitShellOutcome) -> pb::AwaitTaskComplete {
    pb::AwaitTaskComplete {
        task_id: outcome.shell_id.clone(),
        runtime_ms: outcome.runtime_ms,
        output_file_path: String::new(),
        output_length: outcome.output_length,
        regex_requested: outcome.regex_requested,
        regex_match: outcome.regex_match.clone(),
        exit_code: outcome.exit_code,
        wake_reason: outcome.wake_reason.clone(),
    }
}

fn still_running(outcome: &AwaitShellOutcome) -> pb::AwaitTaskStillRunning {
    pb::AwaitTaskStillRunning {
        task_id: outcome.shell_id.clone(),
        runtime_ms: outcome.runtime_ms,
        output_file_path: String::new(),
        output_length: outcome.output_length,
        regex_requested: outcome.regex_requested,
        regex_match: outcome.regex_match.clone(),
        wake_reason: outcome.wake_reason.clone(),
    }
}

fn string_arg(call: &ToolCall, name: &str) -> String {
    call.arguments
        .get(name)
        .and_then(|value| match value {
            Value::String(text) => Some(text.trim().to_string()),
            // Go coerces numeric identifiers (e.g. shell_id: 340173) to their
            // string spelling so callers that expect a string id still resolve.
            Value::Number(number) => Some(number.to_string()),
            _ => None,
        })
        .filter(|value| !value.is_empty())
        .unwrap_or_default()
}

fn pattern_match(pattern: &str, output: &str) -> std::result::Result<(bool, Option<String>), String> {
    let trimmed = pattern.trim();
    if trimmed.is_empty() {
        return Ok((false, None));
    }
    let expression = format!("(?m){trimmed}");
    let regex = regex::Regex::new(&expression)
        .map_err(|error| format!("invalid AwaitShell pattern: {error}"))?;
    match regex.find(output) {
        Some(matched) => Ok((true, Some(matched.as_str().to_string()))),
        None => Ok((false, None)),
    }
}

fn truncate_output(value: &str) -> String {
    if value.len() <= OUTPUT_LIMIT {
        return value.to_string();
    }
    let mut start = value.len() - OUTPUT_LIMIT;
    while start < value.len() && !value.is_char_boundary(start) {
        start += 1;
    }
    value[start..].to_string()
}

struct TerminalSnapshot {
    exit_code: Option<i32>,
    running_for_ms: Option<u64>,
    elapsed_ms: Option<u64>,
    output: String,
}

fn read_terminal_file(terminals_folder: &str, shell_id: &str) -> Option<TerminalSnapshot> {
    let id: u32 = shell_id.trim().parse().ok()?;
    let folder = std::path::Path::new(terminals_folder.trim());
    if !folder.is_absolute() {
        return None;
    }
    let path = folder.join(format!("{id}.txt"));
    let data = std::fs::read_to_string(path).ok()?;
    parse_terminal_file(&data)
}

fn parse_terminal_file(raw: &str) -> Option<TerminalSnapshot> {
    let normalized = raw.replace("\r\n", "\n");
    let lines: Vec<&str> = normalized.split('\n').collect();
    let separators: Vec<usize> = lines
        .iter()
        .enumerate()
        .filter_map(|(index, line)| (line.trim() == "---").then_some(index))
        .collect();
    if separators.len() < 2 {
        return None;
    }
    let header = &lines[separators[0] + 1..separators[1]];
    let running_for_ms = metadata_u64(header, "running_for_ms");
    let output_start = separators[1] + 1;
    let mut output_end = lines.len();
    let mut exit_code = None;
    let mut elapsed_ms = None;
    for index in (1..separators.len() - 1).rev() {
        let block = &lines[separators[index] + 1..separators[index + 1]];
        if metadata_key(block, "exit_code").is_some() || has_metadata_key(block, "elapsed_ms") {
            exit_code = metadata_key(block, "exit_code");
            elapsed_ms = metadata_u64(block, "elapsed_ms");
            output_end = separators[index];
            break;
        }
    }
    let output = if output_start < output_end {
        lines[output_start..output_end]
            .join("\n")
            .trim_end_matches('\n')
            .to_string()
    } else {
        String::new()
    };
    Some(TerminalSnapshot {
        exit_code,
        running_for_ms,
        elapsed_ms,
        output,
    })
}

fn metadata_key(lines: &[&str], key: &str) -> Option<i32> {
    lines.iter().find_map(|line| {
        let (name, value) = line.split_once(':')?;
        (name.trim() == key).then(|| unquote(value.trim()).parse::<i32>().ok())?
    })
}

fn metadata_u64(lines: &[&str], key: &str) -> Option<u64> {
    lines.iter().find_map(|line| {
        let (name, value) = line.split_once(':')?;
        (name.trim() == key).then(|| unquote(value.trim()).parse::<u64>().ok())?
    })
}

fn unquote(value: &str) -> &str {
    value
        .strip_prefix('"')
        .and_then(|value| value.strip_suffix('"'))
        .unwrap_or(value)
}

fn has_metadata_key(lines: &[&str], key: &str) -> bool {
    lines
        .iter()
        .any(|line| line.split_once(':').is_some_and(|(name, _)| name.trim() == key))
}

fn is_zero(value: &u64) -> bool {
    *value == 0
}

fn is_false(value: &bool) -> bool {
    !*value
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn call(arguments: Value) -> ToolCall {
        ToolCall {
            index: 0,
            call_id: "call-1".into(),
            model_call_id: "model-1".into(),
            name: "AwaitShell".into(),
            arguments_text: arguments.to_string(),
            arguments,
        }
    }

    #[test]
    fn no_shell_id_is_a_pure_wait() {
        let args = decode_args(&call(json!({"block_until_ms": 1200}))).unwrap();
        let outcome = outcome(&args, "");
        assert_eq!(outcome.status, "waited");
        assert!(!outcome.matched);
        assert!(!outcome.timed_out);
        assert!(outcome.message.contains("1200ms"));

        // Official clients render "Slept for Ns" from the AwaitSuccess branch,
        // not "Task still running".
        let result = build_await_result(&outcome);
        assert!(matches!(
            result.result,
            Some(pb::await_result::Result::Success(pb::AwaitSuccess {
                await_result: Some(pb::await_success::AwaitResult::Complete(_)),
            }))
        ));
    }

    #[test]
    fn shell_id_is_required_for_a_non_blocking_check() {
        assert!(decode_args(&call(json!({"block_until_ms": 0}))).is_err());
        assert!(decode_args(&call(json!({"shell_id": "42", "block_until_ms": 0}))).is_ok());
    }

    #[test]
    fn block_until_ms_is_clamped_to_the_official_maximum() {
        let args = decode_args(&call(json!({"block_until_ms": 99_999_999}))).unwrap();
        assert_eq!(args.block_until_ms, MAX_BLOCK_UNTIL_MS);
        assert!(args.block_until_explicit);
    }

    #[test]
    fn absent_timeout_defaults_without_being_advertised() {
        let args = decode_args(&call(json!({"shell_id": "7"}))).unwrap();
        assert_eq!(args.block_until_ms, 30_000);
        assert!(!args.block_until_explicit);
        let built = build_await_args(&args);
        assert!(built.block_until_ms.is_none());
    }

    #[test]
    fn terminal_file_snapshot_extracts_footer_exit_code_and_body() {
        let raw = "---\npid: 42\ncommand: \"sleep 1\"\n---\nhello\nworld\n---\nexit_code: 0\nelapsed_ms: 1234\n---\n";
        let snapshot = parse_terminal_file(raw).unwrap();
        assert_eq!(snapshot.exit_code, Some(0));
        assert_eq!(snapshot.elapsed_ms, Some(1234));
        assert_eq!(snapshot.output, "hello\nworld");
    }

    #[test]
    fn terminal_file_header_running_for_ms_is_parsed() {
        let raw = "---\npid: 42\ncommand: \"sleep 1\"\nrunning_for_ms: 5678\n---\nhello\n";
        let snapshot = parse_terminal_file(raw).unwrap();
        assert_eq!(snapshot.running_for_ms, Some(5678));
        assert_eq!(snapshot.exit_code, None);
        assert_eq!(snapshot.output, "hello");
    }

    #[test]
    fn terminal_file_without_footer_keeps_the_full_body() {
        let raw = "---\npid: 42\n---\nrunning output\n";
        let snapshot = parse_terminal_file(raw).unwrap();
        assert_eq!(snapshot.exit_code, None);
        assert_eq!(snapshot.output, "running output");
    }

    #[test]
    fn pattern_matches_anywhere_in_the_output() {
        let (matched, text) = pattern_match("started", "boot\nserver started\nok").unwrap();
        assert!(matched);
        assert_eq!(text.as_deref(), Some("started"));
        assert!(!pattern_match("missing", "boot\nserver started\nok").unwrap().0);
    }

    #[test]
    fn unknown_shell_id_maps_to_an_error_result() {
        let args = decode_args(&call(json!({"shell_id": "999999"}))).unwrap();
        let outcome = outcome(&args, "/nonexistent/terminals");
        assert_eq!(outcome.status, "unknown");
        let result = build_await_result(&outcome);
        assert!(matches!(
            result.result,
            Some(pb::await_result::Result::Error(_))
        ));
    }
}