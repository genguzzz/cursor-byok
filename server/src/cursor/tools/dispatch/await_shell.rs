//! Asynchronous dispatch for AwaitShell.

use tokio::time::{sleep, Duration};

use crate::{model::ToolCall, Result};

use super::ToolStart;
use crate::cursor::tools::{
    await_shell::{self, AwaitShellArgs, POLL_INTERVAL_MS, WAKE_REASON_CONTEXT_INJECTION},
    result::{self, ToolResultSender},
    runtime::now_ms,
};

pub(super) fn start(
    results: &ToolResultSender,
    call: &ToolCall,
    terminals_folder: &str,
) -> Result<ToolStart> {
    let args = await_shell::decode_args(call)?;
    let call = call.clone();
    let results = results.clone();
    let terminals_folder = terminals_folder.to_string();
    let started_at_ms = now_ms();
    tokio::spawn(async move {
        let outcome = wait(&args, &terminals_folder).await;
        let completion = result::await_shell::complete(&call, started_at_ms, &args, &outcome);
        match completion {
            Ok(completion) => results.send(completion),
            Err(error) => results.send_error(error),
        }
    });
    Ok(ToolStart {
        messages: Vec::new(),
        completion: None,
    })
}

async fn wait(args: &AwaitShellArgs, terminals_folder: &str) -> await_shell::AwaitShellOutcome {
    // Pure wait: no shell id means sleep the full block_until_ms window.
    if args.shell_id.is_empty() {
        sleep(Duration::from_millis(args.block_until_ms.max(0) as u64)).await;
        return await_shell::outcome(args, terminals_folder);
    }

    let deadline =
        tokio::time::Instant::now() + Duration::from_millis(args.block_until_ms.max(0) as u64);
    loop {
        let outcome = await_shell::outcome(args, terminals_folder);
        if await_shell::is_terminal(&outcome) {
            return outcome;
        }
        let now = tokio::time::Instant::now();
        if now >= deadline {
            return outcome;
        }
        let remaining = deadline.saturating_duration_since(now);
        sleep(remaining.min(Duration::from_millis(POLL_INTERVAL_MS))).await;
    }
}

// Kept central here so future "wait released early by a new user message"
// paths can produce the same wake_reason the official client does.
#[allow(dead_code)]
pub(super) fn context_injection_wake_reason() -> &'static str {
    WAKE_REASON_CONTEXT_INJECTION
}