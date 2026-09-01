//! Decides when to compact context and builds a stable fallback summary.

use std::collections::HashSet;

use crate::model::{
    CanonicalMessage, ContentPart, LlmCallUsageAnchor, MessageContent, Origin, PreparedRun,
    ProjectedMessage, Role,
};

const FALLBACK_CHARS: usize = 12_000;

pub(super) const OUTPUT_TOKENS: u64 = 4_096;
pub(super) const AUTO_RESERVE_TOKENS: u64 = 10_000;
pub(super) const PREFERRED_TAIL_TURNS: usize = 4;
pub(super) const PREFERRED_TAIL_MESSAGES: usize = 32;
pub(super) const INSTRUCTIONS: &str = include_str!("../../prompt/cursor/compaction/prompt.md");

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) struct ContextUsageAnchor {
    input_tokens: u64,
    message_count: usize,
    tool_count: usize,
}

impl ContextUsageAnchor {
    pub(super) fn from_llm_call(anchor: LlmCallUsageAnchor) -> Option<Self> {
        Some(Self {
            input_tokens: anchor.usage.context_input_tokens(anchor.request_type)?,
            message_count: anchor.message_count,
            tool_count: anchor.tool_count,
        })
    }
}

pub(super) fn should_compact(
    prepared: &PreparedRun,
    messages: &[CanonicalMessage],
    projected_messages: &[ProjectedMessage],
    anchor: Option<ContextUsageAnchor>,
) -> bool {
    let Some(context_window) = prepared.model.context_window_tokens else {
        return false;
    };
    if context_window == 0 || messages.len() <= prepared.initial_messages.len() {
        return false;
    }
    let trigger_limit = context_window.saturating_sub(AUTO_RESERVE_TOKENS);
    if trigger_limit == 0 {
        return false;
    }
    let estimated_input = anchor
        .filter(|anchor| {
            anchor.message_count <= projected_messages.len()
                && anchor.tool_count == prepared.prompt.tools.len()
        })
        .map(|anchor| {
            anchor
                .input_tokens
                .saturating_add(estimate_serialized_tokens(
                    &serde_json::to_string(&projected_messages[anchor.message_count..])
                        .unwrap_or_default(),
                ))
        })
        .unwrap_or_else(|| {
            estimate_serialized_tokens(
                &serde_json::to_string(&(&prepared.prompt, messages)).unwrap_or_default(),
            )
        });
    estimated_input > trigger_limit
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
                && latest_request_context.map_or(true, |ctx_idx| *index != ctx_idx)
                && !is_conversation_summary(message)
        })
        .map(|(_, message)| message.clone())
        .collect();
    let compactable = messages
        .iter()
        .enumerate()
        .filter(|(index, message)| {
            *index < tail_start
                && latest_request_context.map_or(true, |ctx_idx| *index != ctx_idx)
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

fn estimate_serialized_tokens(serialized: &str) -> u64 {
    serialized
        .chars()
        .fold(0_u64, |units, character| {
            units.saturating_add(if character.is_ascii() { 273 } else { 550 })
        })
        .div_ceil(1_000)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::{
        project_messages, CheckpointId, ConversationId, ModelSpec, PromptSpec, RunAction, RunId,
        RunKind, ToolCallContent, ToolResultContent, ToolRoundId,
    };

    #[test]
    fn automatic_compaction_runs_for_start_and_resume_actions_after_the_limit() {
        let mut model = ModelSpec::new("model");
        model.context_window_tokens = Some(200_000);
        let mut prepared = PreparedRun {
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
        };
        let messages = vec![CanonicalMessage::text(
            "user",
            Role::User,
            Origin::Runtime,
            "hello",
        )];
        let projected = project_messages(&messages).unwrap();
        let tail_tokens = estimate_serialized_tokens(&serde_json::to_string(&projected).unwrap());
        let reserve = AUTO_RESERVE_TOKENS;
        let anchor = |estimated_input| {
            Some(ContextUsageAnchor {
                input_tokens: estimated_input - tail_tokens,
                message_count: 0,
                tool_count: 0,
            })
        };

        assert!(!should_compact(
            &prepared,
            &messages,
            &projected,
            anchor(200_000 - reserve)
        ));
        assert!(should_compact(
            &prepared,
            &messages,
            &projected,
            anchor(200_000 - reserve + 1)
        ));

        prepared.action = RunAction::Resume {
            pending_tool_round: None,
        };
        assert!(should_compact(
            &prepared,
            &messages,
            &projected,
            anchor(200_000 - reserve + 1)
        ));
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
            CanonicalMessage::text(
                "request-context:ctx",
                Role::User,
                Origin::Runtime,
                "rules",
            ),
            CanonicalMessage::text("runtime:turn-1", Role::User, Origin::Runtime, "task"),
            CanonicalMessage::text("assistant:turn-1", Role::Assistant, Origin::Assistant, "work"),
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
        assert!(
            !compactable.iter().any(|message| {
                matches!(
                    message.content,
                    MessageContent::Assistant {
                        tool_calls: ref calls,
                        ..
                    } if !calls.is_empty()
                )
            })
        );
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
