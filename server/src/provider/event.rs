//! Defines normalized provider streaming events.
use crate::model::{ProviderReplayState, Usage};

/// Presentation hint for a provider-visible reasoning summary.
///
/// This never represents a model's private chain of thought. It only labels
/// text that the provider has explicitly emitted as displayable reasoning.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum ThinkingStyle {
    #[default]
    Default,
    Gpt5,
}

impl ThinkingStyle {
    pub fn for_model(model_id: &str) -> Self {
        if model_id
            .trim_start()
            .to_ascii_lowercase()
            .starts_with("gpt-5")
        {
            Self::Gpt5
        } else {
            Self::Default
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum FinishReason {
    Stop,
    Length,
    ToolUse,
}

#[derive(Clone, Debug, PartialEq)]
pub enum ModelEvent {
    Start {
        model_call_id: String,
    },
    TextStart,
    TextDelta(String),
    TextEnd,
    ThinkingStart,
    ThinkingDelta {
        text: String,
        style: ThinkingStyle,
    },
    ThinkingEnd,
    ToolCallStart {
        index: usize,
        call_id: String,
        name: String,
    },
    ToolCallArgumentsDelta {
        index: usize,
        delta: String,
    },
    ToolCallEnd {
        index: usize,
    },
    ProviderReplayState(ProviderReplayState),
    Usage(Usage),
    Done(FinishReason),
}

/// Returns whether an event represents the first valid upstream response.
/// Transport markers, replay metadata, usage, completion, and provider heartbeats
/// are intentionally excluded; empty text/reasoning/tool deltas are valid events.
pub fn is_valid_response_event(event: &ModelEvent) -> bool {
    matches!(
        event,
        ModelEvent::TextDelta(_)
            | ModelEvent::ThinkingStart
            | ModelEvent::ThinkingDelta { .. }
            | ModelEvent::ThinkingEnd
            | ModelEvent::ToolCallStart { .. }
            | ModelEvent::ToolCallArgumentsDelta { .. }
            | ModelEvent::ToolCallEnd { .. }
    )
}
