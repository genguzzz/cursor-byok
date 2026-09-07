//! Converts runtime events into live Cursor server messages.

use std::{collections::BTreeMap, time::Duration};

use crate::{
    cursor::{protocol::proto::agent::v1 as pb, tools::codec},
    model::Usage,
    provider::ModelEvent,
    Result,
};

pub fn response_event(
    event: &ModelEvent,
    model_call_id: &str,
    dynamic_mcp: &BTreeMap<String, pb::McpToolDefinition>,
) -> Result<Option<pb::AgentServerMessage>> {
    use pb::interaction_update::Message;
    let message = match event {
        ModelEvent::TextDelta(text) => Message::TextDelta(pb::TextDeltaUpdate {
            text: text.clone(),
            is_server_notice: false,
        }),
        ModelEvent::ThinkingDelta { text, style } => {
            Message::ThinkingDelta(pb::ThinkingDeltaUpdate {
                text: text.clone(),
                thinking_style: Some(match style {
                    crate::provider::ThinkingStyle::Default => pb::ThinkingStyle::Default as i32,
                    crate::provider::ThinkingStyle::Gpt5 => pb::ThinkingStyle::Gpt5 as i32,
                }),
            })
        }
        ModelEvent::ToolCallStart { call_id, name, .. } => {
            Message::PartialToolCall(pb::PartialToolCallUpdate {
                call_id: call_id.clone(),
                tool_call: Some(match dynamic_mcp.get(name) {
                    Some(definition) => codec::dynamic_mcp_placeholder(definition, call_id),
                    None => codec::tool_placeholder(name, call_id)?,
                }),
                args_text_delta: String::new(),
                model_call_id: model_call_id.into(),
            })
        }
        ModelEvent::ToolCallArgumentsDelta { .. } => return Ok(None),
        ModelEvent::ToolCallEnd { .. }
        | ModelEvent::Start { .. }
        | ModelEvent::TextStart
        | ModelEvent::TextEnd
        | ModelEvent::ThinkingStart
        | ModelEvent::ThinkingEnd
        | ModelEvent::ProviderReplayState(_)
        | ModelEvent::Usage(_)
        | ModelEvent::Done(_) => return Ok(None),
    };
    Ok(Some(server_interaction(message)))
}

pub fn thinking_completed(elapsed: Duration) -> pb::AgentServerMessage {
    let milliseconds = elapsed.as_millis().clamp(1, i32::MAX as u128) as i32;
    server_interaction(pb::interaction_update::Message::ThinkingCompleted(
        pb::ThinkingCompletedUpdate {
            thinking_duration_ms: milliseconds,
        },
    ))
}

pub fn heartbeat() -> pb::AgentServerMessage {
    server_interaction(pb::interaction_update::Message::Heartbeat(
        pb::HeartbeatUpdate {},
    ))
}

pub fn turn_ended(usage: Option<Usage>) -> pb::AgentServerMessage {
    server_interaction(pb::interaction_update::Message::TurnEnded(
        pb::TurnEndedUpdate {
            input_tokens: usage.and_then(|usage| usage.input_tokens.map(|value| value as i64)),
            output_tokens: usage.and_then(|usage| usage.output_tokens.map(|value| value as i64)),
            cache_read_tokens: usage
                .and_then(|usage| usage.cache_read_tokens.map(|value| value as i64)),
            cache_write_tokens: usage
                .and_then(|usage| usage.cache_write_tokens.map(|value| value as i64)),
            reasoning_tokens: usage
                .and_then(|usage| usage.reasoning_tokens.map(|value| value as i64)),
        },
    ))
}

pub fn token_delta(tokens: u64) -> pb::AgentServerMessage {
    server_interaction(pb::interaction_update::Message::TokenDelta(
        pb::TokenDeltaUpdate {
            tokens: tokens.min(i32::MAX as u64) as i32,
        },
    ))
}

pub fn summary_started() -> pb::AgentServerMessage {
    server_interaction(pb::interaction_update::Message::SummaryStarted(
        pb::SummaryStartedUpdate {},
    ))
}

pub fn summary_delta(summary: String) -> pb::AgentServerMessage {
    server_interaction(pb::interaction_update::Message::Summary(
        pb::SummaryUpdate { summary },
    ))
}

pub fn summary_completed() -> pb::AgentServerMessage {
    server_interaction(pb::interaction_update::Message::SummaryCompleted(
        pb::SummaryCompletedUpdate { hook_message: None },
    ))
}

pub fn context_injection_queued(injection_id: String) -> pb::AgentServerMessage {
    server_interaction(pb::interaction_update::Message::ContextInjectionState(
        pb::ContextInjectionStateUpdate {
            injection_id,
            state: Some(pb::ContextInjectionState {
                state: Some(pb::context_injection_state::State::Queued(
                    pb::ContextInjectionQueued {},
                )),
            }),
        },
    ))
}

pub fn context_injection_rejected(injection_id: String, reason: String) -> pb::AgentServerMessage {
    server_interaction(pb::interaction_update::Message::ContextInjectionState(
        pb::ContextInjectionStateUpdate {
            injection_id,
            state: Some(pb::ContextInjectionState {
                state: Some(pb::context_injection_state::State::Rejected(
                    pb::ContextInjectionRejected { reason },
                )),
            }),
        },
    ))
}

pub fn context_injection_delivered(
    injection_id: String,
    delivery_batch_id: String,
    delivered_at_ms: i64,
) -> pb::AgentServerMessage {
    server_interaction(pb::interaction_update::Message::ContextInjectionState(
        pb::ContextInjectionStateUpdate {
            injection_id,
            state: Some(pb::ContextInjectionState {
                state: Some(pb::context_injection_state::State::Delivered(
                    pb::ContextInjectionDelivered {
                        step: 0,
                        delivery_batch_id,
                        delivered_at_ms,
                    },
                )),
            }),
        },
    ))
}

pub fn user_message_appended(user_message: pb::UserMessage) -> pb::AgentServerMessage {
    server_interaction(pb::interaction_update::Message::UserMessageAppended(
        pb::UserMessageAppendedUpdate {
            user_message: Some(user_message),
        },
    ))
}

pub fn server_interaction(message: pb::interaction_update::Message) -> pb::AgentServerMessage {
    pb::AgentServerMessage {
        ttft_breakdown: None,
        message: Some(pb::agent_server_message::Message::InteractionUpdate(
            pb::InteractionUpdate {
                message: Some(message),
            },
        )),
    }
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;

    use super::*;

    #[test]
    fn gpt5_reasoning_delta_keeps_its_presentation_style() {
        let message = response_event(
            &ModelEvent::ThinkingDelta {
                text: "正在分析请求。".into(),
                style: crate::provider::ThinkingStyle::Gpt5,
            },
            "",
            &BTreeMap::new(),
        )
        .unwrap()
        .unwrap();
        let pb::agent_server_message::Message::InteractionUpdate(update) = message.message.unwrap()
        else {
            panic!("expected interaction update");
        };
        let Some(pb::interaction_update::Message::ThinkingDelta(delta)) = update.message else {
            panic!("expected thinking delta");
        };

        assert_eq!(delta.text, "正在分析请求。");
        assert_eq!(delta.thinking_style, Some(pb::ThinkingStyle::Gpt5 as i32));
    }
}
