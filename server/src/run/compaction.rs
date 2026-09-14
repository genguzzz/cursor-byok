//! Decides when to compact provider-visible context and builds a stable fallback summary.

use std::collections::HashSet;

use crate::{
    model::{
        estimate_context_tokens, estimate_projected_messages_tokens, CanonicalMessage, ContentPart,
        MessageContent, Origin, PreparedRun, ProjectedContent, ProjectedMessage, Role,
    },
    store::ContextUsageAnchor,
};

const FALLBACK_CHARS: usize = 12_000;

/// Fraction of the context window kept free, as a divisor: 10 = 10%.
///
/// The estimate runs behind the provider: the anchor is what the provider
/// charged for the *previous* call, and the next request re-sends request
/// context and carries provider-side overhead the message-tail estimate does
/// not model. A real conversation measured 948K estimated against 1,017,628
/// actual, a 7% shortfall that landed it over a 1M window while the check said
/// there was room. A proportional reserve absorbs that drift and scales with
/// the model: a 200K window keeps 20K free and a 1M window keeps 100K.
const CONTEXT_RESERVE_DIVISOR: u64 = 10;
pub(super) const OUTPUT_TOKENS: u64 = 4_096;
pub(super) const PREFERRED_TAIL_TURNS: usize = 4;
pub(super) const PREFERRED_TAIL_MESSAGES: usize = 32;
pub(super) const INSTRUCTIONS: &str = include_str!("../../prompt/cursor/compaction/prompt.md");

/// Usable prompt budget: the window minus the proportional reserve.
pub(super) fn context_budget(context_window: u64) -> u64 {
    context_window.saturating_sub(context_window / CONTEXT_RESERVE_DIVISOR)
}

pub(super) fn input_budget(prepared: &PreparedRun) -> Option<u64> {
    prepared.model.context_window_tokens.map(context_budget)
}

/// Whether a provider failure means the prompt did not fit.
///
/// Providers report this as a plain 400 with prose, so there is nothing
/// structured to match on. Anthropic says "prompt is too long"; OpenAI-style
/// gateways use `context_length_exceeded` or "maximum context length".
pub(super) fn is_context_overflow(message: &str) -> bool {
    let lowered = message.to_ascii_lowercase();
    lowered.contains("prompt is too long")
        || lowered.contains("context window exceeded")
        || lowered.contains("model_context_window_exceeded")
        || lowered.contains("context_length_exceeded")
        || (lowered.contains("maximum context length") && lowered.contains("token"))
}

/// Builds the history for a compaction call.
///
/// Two properties matter, and replaying the raw history guarantees neither:
///
/// 1. The history must end with a user message. Otherwise providers read the
///    request as an assistant prefill and refuse it outright: Anthropic answers
///    "This model does not support assistant message prefill. The conversation
///    must end with a user message."
/// 2. The history must fit the context window. Compaction runs precisely
///    because the conversation is too large, so replaying all of it asks the
///    summarizer to accept a prompt that is already over the limit and the
///    call fails with "prompt is too long".
///
/// Either failure falls back to the truncated summary, which is usually still
/// too large, so the conversation stays over its window and cannot recover.
///
/// Trimming keeps the most recent turns and only ever cuts at a user-message
/// boundary, so an assistant tool call is never separated from its results.
pub(super) fn compaction_history(
    history: Vec<ProjectedMessage>,
    context_window: Option<u64>,
) -> Vec<ProjectedMessage> {
    super::history::user_terminated(
        trim_to_context(history, context_window),
        "compaction:instruction",
        INSTRUCTIONS,
    )
}

fn trim_to_context(
    mut history: Vec<ProjectedMessage>,
    context_window: Option<u64>,
) -> Vec<ProjectedMessage> {
    let Some(budget) = context_window
        .filter(|window| *window > 0)
        .map(context_budget)
        .map(|budget| budget.saturating_sub(OUTPUT_TOKENS))
        .filter(|budget| *budget > 0)
    else {
        return history;
    };
    if estimate_projected_messages_tokens(&history) <= budget {
        return history;
    }
    // Walk back from the newest turn, keeping whole user-delimited turns.
    let mut kept = 0;
    let mut newest_turn = None;
    for (index, message) in history.iter().enumerate().rev() {
        if !is_turn_boundary(message) {
            continue;
        }
        newest_turn.get_or_insert(index);
        if estimate_projected_messages_tokens(&history[index..]) > budget {
            break;
        }
        kept = history.len() - index;
    }
    if kept == 0 {
        // Not even the newest turn fits. Keep it anyway rather than sending an
        // empty history: an empty summarize call returns a summary of nothing
        // that would then replace the whole conversation.
        let start = newest_turn.unwrap_or(0);
        return history.split_off(start);
    }
    history.split_off(history.len() - kept)
}

fn is_turn_boundary(message: &ProjectedMessage) -> bool {
    message.role == Role::User && matches!(message.content, ProjectedContent::Parts(_))
}

pub(super) fn estimated_tokens(
    prepared: &PreparedRun,
    projected_messages: &[ProjectedMessage],
    anchor: Option<ContextUsageAnchor>,
) -> u64 {
    anchor
        .filter(|anchor| anchor.message_count <= projected_messages.len())
        .map(|anchor| {
            anchor
                .context_input_tokens
                .saturating_add(estimate_projected_messages_tokens(
                    &projected_messages[anchor.message_count..],
                ))
        })
        .unwrap_or_else(|| estimate_context_tokens(&prepared.prompt, projected_messages))
}

pub(super) fn compaction_estimate(
    prepared: &PreparedRun,
    projected_messages: &[ProjectedMessage],
    anchor: Option<ContextUsageAnchor>,
) -> Option<u64> {
    let budget = input_budget(prepared)?;
    let estimated = estimated_tokens(prepared, projected_messages, anchor);
    (estimated > budget).then_some(estimated)
}

#[cfg(test)]
pub(super) fn should_compact(
    prepared: &PreparedRun,
    projected_messages: &[ProjectedMessage],
    anchor: Option<ContextUsageAnchor>,
) -> bool {
    compaction_estimate(prepared, projected_messages, anchor).is_some()
}

pub(super) fn validate_compacted(
    prepared: &PreparedRun,
    projected_messages: &[ProjectedMessage],
) -> std::result::Result<u64, String> {
    let estimated = estimate_context_tokens(&prepared.prompt, projected_messages);
    let Some(budget) = input_budget(prepared) else {
        return Ok(estimated);
    };
    if estimated <= budget {
        return Ok(estimated);
    }
    Err(format!(
        "context overflow after compaction: estimated input {estimated} tokens exceeds budget {budget} tokens"
    ))
}

pub(super) fn partition(
    messages: &[CanonicalMessage],
    current_ids: &HashSet<&str>,
) -> (
    Vec<CanonicalMessage>,
    Option<CanonicalMessage>,
    Vec<CanonicalMessage>,
) {
    partition_with_tail_turns(messages, current_ids, PREFERRED_TAIL_TURNS)
}

fn partition_with_tail_turns(
    messages: &[CanonicalMessage],
    current_ids: &HashSet<&str>,
    tail_turns: usize,
) -> (
    Vec<CanonicalMessage>,
    Option<CanonicalMessage>,
    Vec<CanonicalMessage>,
) {
    let latest_request_context = messages
        .iter()
        .rposition(|message| message.message_id.starts_with("request-context:"));
    let turn_starts = runtime_turn_starts(messages);
    let current_turn_start = messages
        .iter()
        .enumerate()
        .filter(|(_, message)| current_ids.contains(message.message_id.as_str()))
        .map(|(index, _)| index)
        .min()
        .unwrap_or(messages.len());
    let agentic_depth = messages.len().saturating_sub(current_turn_start);
    let retained_request_context = latest_request_context
        .and_then(|index| messages.get(index))
        .cloned();

    if agentic_depth <= 2 {
        let compactable = messages
            .iter()
            .enumerate()
            .filter(|(index, message)| {
                Some(*index) != latest_request_context
                    && !current_ids.contains(message.message_id.as_str())
                    && !is_conversation_summary(message)
            })
            .map(|(_, message)| message.clone())
            .collect();
        return (compactable, retained_request_context, Vec::new());
    }

    let turn_tail_start = turn_starts
        .get(turn_starts.len().saturating_sub(tail_turns))
        .copied()
        .unwrap_or(0);
    let message_tail_start = messages.len().saturating_sub(PREFERRED_TAIL_MESSAGES);
    let tail_start = align_tail_start(
        messages,
        turn_tail_start
            .max(current_turn_start)
            .max(message_tail_start),
    );
    let retained_tail = messages
        .iter()
        .enumerate()
        .filter(|(index, message)| {
            *index >= tail_start
                && latest_request_context != Some(*index)
                && !is_conversation_summary(message)
        })
        .map(|(_, message)| message.clone())
        .collect();
    let compactable = messages
        .iter()
        .enumerate()
        .filter(|(index, message)| {
            *index < tail_start
                && latest_request_context != Some(*index)
                && !is_conversation_summary(message)
        })
        .map(|(_, message)| message.clone())
        .collect();
    (compactable, retained_request_context, retained_tail)
}

pub(super) fn is_conversation_summary(message: &CanonicalMessage) -> bool {
    matches!(
        &message.content,
        MessageContent::Parts { parts }
            if parts.iter().any(|part| matches!(part,
                ContentPart::Text { text } if text.contains("<conversation_summary>")
            ))
    )
}

pub(crate) fn window_tail_after_summary(messages: &[CanonicalMessage]) -> u32 {
    let Some(summary_index) = messages.iter().position(is_conversation_summary) else {
        return 0;
    };
    messages
        .len()
        .saturating_sub(summary_index + 1)
        .min(u32::MAX as usize) as u32
}

pub(super) fn fallback_summary(messages: &[CanonicalMessage]) -> String {
    let serialized = serde_json::to_string(messages).unwrap_or_default();
    let start = serialized
        .char_indices()
        .rev()
        .nth(FALLBACK_CHARS.saturating_sub(1))
        .map_or(0, |(index, _)| index);
    format!(
        "Durable recent conversation state:\n{}",
        &serialized[start..]
    )
}

fn align_tail_start(messages: &[CanonicalMessage], mut start: usize) -> usize {
    start = start.min(messages.len());
    loop {
        if start == 0 {
            return 0;
        }
        if matches!(
            messages.get(start).map(|message| &message.content),
            Some(MessageContent::ToolResult(_))
        ) {
            start = tool_round_start(messages, start);
            continue;
        }
        if let MessageContent::Assistant { tool_calls, .. } = &messages[start - 1].content {
            if !tool_calls.is_empty() {
                start = tool_round_start(messages, start - 1);
                continue;
            }
        }
        return start;
    }
}

fn tool_round_start(messages: &[CanonicalMessage], mut index: usize) -> usize {
    while index > 0 {
        match &messages[index].content {
            MessageContent::ToolResult(_) => index -= 1,
            MessageContent::Assistant {
                tool_round_id: Some(round_id),
                ..
            } => {
                let round_id = round_id.clone();
                while index > 0 {
                    if let MessageContent::Assistant {
                        tool_round_id: Some(previous_round),
                        ..
                    } = &messages[index - 1].content
                    {
                        if previous_round == &round_id {
                            index -= 1;
                            continue;
                        }
                    }
                    break;
                }
                return index;
            }
            _ => return index,
        }
    }
    0
}

fn runtime_turn_starts(messages: &[CanonicalMessage]) -> Vec<usize> {
    messages
        .iter()
        .enumerate()
        .filter(|(_, message)| is_runtime_turn_start(message))
        .map(|(index, _)| index)
        .collect()
}

fn is_runtime_turn_start(message: &CanonicalMessage) -> bool {
    message.role == Role::User
        && message.origin == Origin::Runtime
        && message.message_id.starts_with("runtime:")
        && !is_conversation_summary(message)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::{
        project_messages, CheckpointId, ContentPart, ConversationId, ModelSpec, Origin, PromptSpec,
        Role, RunAction, RunId, RunKind, ToolCallContent, ToolResultContent, ToolRoundId,
    };

    fn prepared(context_window_tokens: u64) -> PreparedRun {
        let mut model = ModelSpec::new("model");
        model.context_window_tokens = Some(context_window_tokens);
        PreparedRun {
            run_id: RunId::new("run"),
            cursor_request_id: None,
            conversation_id: ConversationId::new("conversation"),
            kind: RunKind::Root,
            model,
            prompt: PromptSpec {
                instructions: String::new(),
                tools: Vec::new(),
            },
            initial_messages: Vec::new(),
            action: RunAction::Start,
            base_checkpoint_id: CheckpointId(1),
        }
    }

    #[test]
    fn automatic_compaction_uses_proportional_reserve_for_every_action() {
        let messages = vec![CanonicalMessage::text(
            "user",
            Role::User,
            Origin::Runtime,
            "x".repeat(40_000),
        )];
        let projected = project_messages(&messages).unwrap();
        let estimated = estimate_context_tokens(&prepared(1).prompt, &projected);
        // Smallest multiple-of-ten window whose 90% budget covers the estimate.
        let window = estimated.div_ceil(9) * 10;
        let mut prepared = prepared(window);
        assert!(context_budget(window) >= estimated);
        assert!(context_budget(window - 10) < estimated);

        assert!(!should_compact(&prepared, &projected, None));
        prepared.model.context_window_tokens = Some(window - 10);
        assert!(should_compact(&prepared, &projected, None));

        prepared.action = RunAction::Resume {
            pending_tool_round: None,
        };
        assert!(should_compact(&prepared, &projected, None));
    }

    #[test]
    fn the_reserve_leaves_room_for_the_estimate_to_run_behind() {
        // Reproduces a conversation that wedged itself against a 1M window.
        // The anchor said 947,797 tokens, so a fixed 10K reserve found room
        // and let the request through. Anthropic counted 1,017,628 and
        // refused it, and every retry repeated the same arithmetic.
        let messages = vec![CanonicalMessage::text(
            "user",
            Role::User,
            Origin::Runtime,
            "hello",
        )];
        let projected = project_messages(&messages).unwrap();
        let anchor = ContextUsageAnchor {
            context_input_tokens: 947_797,
            message_count: 1,
        };
        assert!(should_compact(
            &prepared(1_000_000),
            &projected,
            Some(anchor)
        ));
    }

    #[test]
    fn the_reserve_scales_with_the_window() {
        assert_eq!(context_budget(200_000), 180_000);
        assert_eq!(context_budget(1_000_000), 900_000);
        assert_eq!(context_budget(0), 0);
        assert_eq!(context_budget(1), 1);
    }

    #[test]
    fn provider_refusals_that_mean_the_prompt_did_not_fit_are_recognized() {
        assert!(is_context_overflow(
            "provider error: Anthropic 400 Bad Request: {\"type\":\"error\",\"error\":\
             {\"type\":\"invalid_request_error\",\"message\":\"prompt is too long: \
             1017628 tokens > 1000000 maximum\"}}"
        ));
        assert!(is_context_overflow("model_context_window_exceeded"));
        assert!(is_context_overflow("context_length_exceeded"));
        assert!(is_context_overflow(
            "This model's maximum context length is 128000 tokens"
        ));

        // Unrelated failures must not trigger a compaction, which would
        // destroy history to fix something compaction cannot fix.
        assert!(!is_context_overflow("401 Unauthorized: invalid api key"));
        assert!(!is_context_overflow("429 Too Many Requests"));
        assert!(!is_context_overflow(
            "This model does not support assistant message prefill"
        ));
    }

    fn user(id: &str, text: &str) -> ProjectedMessage {
        ProjectedMessage {
            message_id: id.into(),
            role: Role::User,
            content: ProjectedContent::Parts(vec![ContentPart::Text { text: text.into() }]),
        }
    }

    fn assistant(id: &str, text: &str) -> ProjectedMessage {
        ProjectedMessage {
            message_id: id.into(),
            role: Role::Assistant,
            content: ProjectedContent::Assistant {
                text: text.into(),
                thinking: String::new(),
                replay_state: None,
                calls: Vec::new(),
            },
        }
    }

    #[test]
    fn compaction_history_always_ends_with_a_user_message() {
        // Providers reject an assistant-terminated history as a prefill, which
        // made every automatic compaction fall back to the truncated summary.
        let history = vec![user("u1", "question"), assistant("a1", "answer")];
        let prepared = compaction_history(history, Some(200_000));
        assert_eq!(prepared.last().unwrap().role, Role::User);
        assert_eq!(
            prepared.last().unwrap().message_id,
            "compaction:instruction"
        );

        // An already user-terminated history is left alone.
        let history = vec![assistant("a1", "answer"), user("u2", "next")];
        let prepared = compaction_history(history.clone(), Some(200_000));
        assert_eq!(prepared, history);
    }

    #[test]
    fn compaction_history_is_trimmed_to_fit_the_context_window() {
        // Compaction runs because the conversation is too large, so the
        // summarize call must not replay a prompt that is over the window.
        let big = "x".repeat(400_000);
        let history = vec![
            user("u1", &big),
            assistant("a1", &big),
            user("u2", &big),
            assistant("a2", "recent answer"),
        ];
        let window = 200_000;
        let prepared = compaction_history(history, Some(window));

        let budget = context_budget(window) - OUTPUT_TOKENS;
        assert!(estimate_projected_messages_tokens(&prepared) <= budget);
        assert_eq!(prepared.last().unwrap().role, Role::User);
        // The newest turn survives the trim and is never split.
        assert!(prepared.iter().any(|message| message.message_id == "u2"));
        assert!(prepared.iter().any(|message| message.message_id == "a2"));
        assert!(!prepared.iter().any(|message| message.message_id == "u1"));
    }

    #[test]
    fn compaction_history_keeps_the_newest_turn_even_when_it_is_over_budget() {
        // A history whose newest turn alone exceeds the budget is still sent
        // rather than trimmed to nothing: a summary of nothing would replace
        // the whole conversation.
        let history = vec![
            user("u1", "old"),
            assistant("a1", "old answer"),
            user("u2", &"x".repeat(400_000)),
            assistant("a2", "answer"),
        ];
        let prepared = compaction_history(history, Some(50_000));
        assert_eq!(prepared[0].message_id, "u2");
        assert_eq!(prepared.last().unwrap().role, Role::User);
    }

    #[test]
    fn compaction_history_without_a_context_window_is_untouched_apart_from_termination() {
        let history = vec![user("u1", "question"), assistant("a1", "answer")];
        let prepared = compaction_history(history.clone(), None);
        assert_eq!(prepared[..2], history[..]);
        assert_eq!(prepared.last().unwrap().role, Role::User);
    }

    #[test]
    fn provider_usage_anchor_only_estimates_messages_added_after_last_request() {
        let messages = vec![
            CanonicalMessage::text("old", Role::User, Origin::Runtime, "x".repeat(400_000)),
            CanonicalMessage::text("new", Role::User, Origin::Runtime, "short follow-up"),
        ];
        let projected = project_messages(&messages).unwrap();
        let anchor = ContextUsageAnchor {
            context_input_tokens: 103_904,
            message_count: 1,
        };
        let expected = 103_904 + estimate_projected_messages_tokens(&projected[1..]);

        assert_eq!(
            estimated_tokens(&prepared(200_000), &projected, Some(anchor)),
            expected
        );
        assert!(!should_compact(
            &prepared(200_000),
            &projected,
            Some(anchor)
        ));
    }

    #[test]
    fn provider_usage_anchor_triggers_after_new_messages_cross_budget() {
        let messages = vec![
            CanonicalMessage::text("old", Role::User, Origin::Runtime, "old"),
            CanonicalMessage::text("new", Role::User, Origin::Runtime, "x".repeat(80_000)),
        ];
        let projected = project_messages(&messages).unwrap();

        assert!(should_compact(
            &prepared(200_000),
            &projected,
            Some(ContextUsageAnchor {
                context_input_tokens: 180_000,
                message_count: 1,
            })
        ));
    }

    #[test]
    fn missing_anchor_uses_full_fallback() {
        let messages = vec![CanonicalMessage::text(
            "user",
            Role::User,
            Origin::Runtime,
            "x".repeat(40_000),
        )];
        let projected = project_messages(&messages).unwrap();
        let prepared = prepared(200_000);

        assert_eq!(
            estimated_tokens(&prepared, &projected, None),
            estimate_context_tokens(&prepared.prompt, &projected)
        );
    }

    #[test]
    fn invalid_anchor_message_count_uses_full_fallback() {
        let messages = vec![CanonicalMessage::text(
            "user",
            Role::User,
            Origin::Runtime,
            "x".repeat(40_000),
        )];
        let projected = project_messages(&messages).unwrap();
        let expected = estimate_context_tokens(&prepared(200_000).prompt, &projected);

        assert_eq!(
            estimated_tokens(
                &prepared(200_000),
                &projected,
                Some(ContextUsageAnchor {
                    context_input_tokens: 1,
                    message_count: 2,
                })
            ),
            expected
        );
    }

    #[test]
    fn compacted_history_is_validated_against_the_same_budget() {
        let messages = vec![CanonicalMessage::text(
            "user",
            Role::User,
            Origin::Runtime,
            "x".repeat(40_000),
        )];
        let projected = project_messages(&messages).unwrap();
        let estimated = estimate_context_tokens(&prepared(1).prompt, &projected);
        let window = estimated.div_ceil(9) * 10;
        assert!(context_budget(window) >= estimated);
        assert!(context_budget(window - 10) < estimated);

        assert_eq!(
            validate_compacted(&prepared(window), &projected),
            Ok(estimated)
        );
        assert!(validate_compacted(&prepared(window - 10), &projected)
            .unwrap_err()
            .contains("context overflow after compaction"));
    }

    #[test]
    fn partition_preserves_current_turn_and_compacts_older_turns() {
        let messages = vec![
            CanonicalMessage::text("runtime:turn-1", Role::User, Origin::Runtime, "first"),
            CanonicalMessage::text("assistant:turn-1", Role::Assistant, Origin::Assistant, "a1"),
            CanonicalMessage::text("runtime:turn-2", Role::User, Origin::Runtime, "second"),
            CanonicalMessage::text("assistant:turn-2", Role::Assistant, Origin::Assistant, "a2"),
            CanonicalMessage::text("tool:turn-2", Role::Tool, Origin::Tool, "tool output"),
        ];
        let current_ids = HashSet::from(["runtime:turn-2"]);
        let (compactable, retained_context, retained_tail) = partition(&messages, &current_ids);
        assert!(retained_context.is_none());
        assert_eq!(compactable.len(), 2);
        assert_eq!(retained_tail.len(), 3);
        assert_eq!(retained_tail[0].message_id, "runtime:turn-2");
    }

    #[test]
    fn partition_keeps_request_context_outside_summary() {
        let messages = vec![
            CanonicalMessage::text("request-context:ctx", Role::User, Origin::Runtime, "rules"),
            CanonicalMessage::text("runtime:turn-1", Role::User, Origin::Runtime, "task"),
            CanonicalMessage::text(
                "assistant:turn-1",
                Role::Assistant,
                Origin::Assistant,
                "work",
            ),
        ];
        let current_ids = HashSet::from(["runtime:turn-1"]);
        let (compactable, retained_context, retained_tail) = partition(&messages, &current_ids);
        assert_eq!(retained_context.unwrap().message_id, "request-context:ctx");
        assert_eq!(compactable.len(), 1);
        assert_eq!(compactable[0].message_id, "assistant:turn-1");
        assert!(retained_tail.is_empty());
    }

    #[test]
    fn instructions_match_compaction_prompt_asset() {
        assert!(INSTRUCTIONS.contains("compacting conversation history"));
        assert!(INSTRUCTIONS.contains("8. Current Work:"));
    }

    #[test]
    fn partition_does_not_split_tool_rounds() {
        let mut messages = vec![
            CanonicalMessage::text("runtime:turn-1", Role::User, Origin::Runtime, "task"),
            CanonicalMessage {
                message_id: "assistant:round-1".into(),
                role: Role::Assistant,
                origin: Origin::Assistant,
                content: MessageContent::Assistant {
                    text: String::new(),
                    thinking: String::new(),
                    tool_round_id: Some(ToolRoundId::new("round-1")),
                    replay_state: None,
                    tool_calls: vec![ToolCallContent {
                        index: 0,
                        call_id: "call-1".into(),
                        name: "Read".into(),
                        arguments: serde_json::json!({}),
                    }],
                },
                runtime_event_id: None,
            },
            CanonicalMessage {
                message_id: "tool:round-1".into(),
                role: Role::Tool,
                origin: Origin::Tool,
                content: MessageContent::ToolResult(ToolResultContent {
                    call_id: "call-1".into(),
                    name: "Read".into(),
                    content: "file contents".into(),
                    is_error: false,
                    image: None,
                    provider_parts: Vec::new(),
                }),
                runtime_event_id: None,
            },
        ];
        for index in 3..34 {
            messages.push(CanonicalMessage::text(
                format!("assistant:filler-{index}"),
                Role::Assistant,
                Origin::Assistant,
                format!("filler-{index}"),
            ));
        }

        let current_ids = HashSet::from(["runtime:turn-1"]);
        let (compactable, _, retained_tail) = partition(&messages, &current_ids);

        project_messages(&compactable).expect("compactable history must project");
        assert!(!compactable.iter().any(|message| {
            matches!(
                message.content,
                MessageContent::Assistant {
                    tool_calls: ref calls,
                    ..
                } if !calls.is_empty()
            )
        }));
        assert!(retained_tail.iter().any(|message| {
            matches!(
                message.content,
                MessageContent::Assistant {
                    tool_calls: ref calls,
                    ..
                } if !calls.is_empty()
            )
        }));
        let mut rebuilt = compactable;
        rebuilt.extend(retained_tail);
        project_messages(&rebuilt).expect("rebuilt history must project");
    }
}
