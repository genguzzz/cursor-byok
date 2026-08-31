//! Asynchronous dispatch for AwaitShell.

use tokio::{
    sync::watch,
    time::{sleep, Duration},
};

use crate::{model::ToolCall, Result};

use super::ToolStart;
use crate::cursor::tools::{
    await_shell::{self, AwaitShellArgs, POLL_INTERVAL_MS, WAKE_REASON_CONTEXT_INJECTION},
    runtime::now_ms,
    tool_call_result::{self, ToolResultSender},
};

pub(super) fn start(
    results: &ToolResultSender,
    call: &ToolCall,
    terminals_folder: &str,
    mut wake: watch::Receiver<u64>,
) -> Result<ToolStart> {
    let args = await_shell::decode_args(call)?;
    let call = call.clone();
    let results = results.clone();
    let terminals_folder = terminals_folder.to_string();
    let started_at_ms = now_ms();
    tokio::spawn(async move {
        let outcome = wait(&args, &terminals_folder, &mut wake).await;
        let completion = tool_call_result::await_shell::complete(&call, started_at_ms, &args, &outcome);
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

async fn wait(
    args: &AwaitShellArgs,
    terminals_folder: &str,
    wake: &mut watch::Receiver<u64>,
) -> await_shell::AwaitShellOutcome {
    // Pure wait: no shell id means sleep the full block_until_ms window. A new
    // user message still cuts the sleep short, matching the official
    // context-injection early release.
    if args.shell_id.is_empty() {
        let duration = Duration::from_millis(args.block_until_ms.max(0) as u64);
        if duration.is_zero() {
            return await_shell::outcome(args, terminals_folder);
        }
        let mut woke_early = false;
        tokio::select! {
            _ = sleep(duration) => {}
            _ = released(wake) => woke_early = true,
        }
        let mut outcome = await_shell::outcome(args, terminals_folder);
        if woke_early {
            outcome.wake_reason = Some(WAKE_REASON_CONTEXT_INJECTION.to_string());
        }
        return outcome;
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
        let poll = sleep(remaining.min(Duration::from_millis(POLL_INTERVAL_MS)));
        tokio::select! {
            _ = poll => {}
            _ = released(wake) => {
                let mut outcome = await_shell::outcome(args, terminals_folder);
                outcome.wake_reason = Some(WAKE_REASON_CONTEXT_INJECTION.to_string());
                return outcome;
            }
        }
    }
}

async fn released(wake: &mut watch::Receiver<u64>) {
    let _ = wake.changed().await;
}

// Kept central here so future "wait released early by a new user message"
// paths can produce the same wake_reason the official client does.
#[cfg(test)]
mod tests {
    use super::*;
    use crate::cursor::tools::await_shell::decode_args;
    use serde_json::json;
    use tokio::sync::watch;

    fn call(arguments: serde_json::Value) -> ToolCall {
        ToolCall {
            index: 0,
            call_id: "call-1".into(),
            model_call_id: "model-1".into(),
            name: "AwaitShell".into(),
            arguments_text: arguments.to_string(),
            arguments,
        }
    }

    #[tokio::test]
    async fn pure_wait_releases_early_with_wake_reason() {
        let args = decode_args(&call(json!({ "block_until_ms": 60_000 }))).unwrap();
        let (tx, mut rx) = watch::channel(0u64);

        let handle = tokio::spawn(async move {
            wait(&args, "", &mut rx).await
        });

        // Give the wait a moment to enter its select, then signal a new user
        // message and confirm it wakes before the full sleep elapses.
        tokio::time::sleep(Duration::from_millis(50)).await;
        tx.send_modify(|generation| *generation += 1);

        let outcome = tokio::time::timeout(Duration::from_secs(2), handle)
            .await
            .expect("pure wait should wake before block_until_ms")
            .expect("wait task should not panic");

        assert_eq!(outcome.wake_reason.as_deref(), Some("context_injection"));
        assert_eq!(outcome.status, "waited");
    }

    #[tokio::test]
    async fn shell_wait_releases_early_with_wake_reason() {
        let dir = tempfile::tempdir().expect("temp terminals dir");
        std::fs::write(
            dir.path().join("4242.txt"),
            "---\npid: 1\nrunning_for_ms: 1234\n---\nrunning output\n",
        )
        .expect("write terminal file");

        let args = decode_args(&call(json!({ "shell_id": "4242", "block_until_ms": 60_000 }))).unwrap();
        let (tx, mut rx) = watch::channel(0u64);
        let terminals_folder = dir.path().to_str().expect("utf8 path").to_string();

        let handle = tokio::spawn(async move {
            wait(&args, &terminals_folder, &mut rx).await
        });

        tokio::time::sleep(Duration::from_millis(50)).await;
        tx.send_modify(|generation| *generation += 1);

        let outcome = tokio::time::timeout(Duration::from_secs(2), handle)
            .await
            .expect("shell wait should wake before block_until_ms")
            .expect("wait task should not panic");

        assert_eq!(outcome.wake_reason.as_deref(), Some("context_injection"));
        assert_eq!(outcome.status, "running");
    }
}
