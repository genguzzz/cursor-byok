use bytes::Bytes;
use prost::Message;

use crate::{
    cursor::interaction,
    cursor::proto::{agent::v1 as agent, aiserver::v1 as ai},
    cursor::{CursorCommand, CursorParent, CursorSessionRegistry},
    Error, Result,
};

pub struct DecodedAppend {
    pub request_id: String,
    pub seqno: i64,
    pub message: agent::AgentClientMessage,
}

/// Cursor and this server spell some subagent types differently.  Fold the
/// known aliases so an override written one way still matches a run named the
/// other.
fn canonical_subagent_type(value: &str) -> &str {
    match value {
        "generalPurpose" => "explore",
        "browserUse" => "browser-use",
        other => other,
    }
}

fn subagent_types_match(left: &str, right: &str) -> bool {
    canonical_subagent_type(left) == canonical_subagent_type(right)
}

impl DecodedAppend {
    fn run_request(&self) -> Option<&agent::AgentRunRequest> {
        match self.message.message.as_ref()? {
            agent::agent_client_message::Message::RunRequest(request) => Some(request),
            _ => None,
        }
    }

    fn run_request_mut(&mut self) -> Option<&mut agent::AgentRunRequest> {
        match self.message.message.as_mut()? {
            agent::agent_client_message::Message::RunRequest(request) => Some(request),
            _ => None,
        }
    }

    pub fn model_id(&self) -> Option<&str> {
        let request = self.run_request()?;
        // A child run carries the parent's `requested_model` while the real
        // choice lives in `subagent_model_overrides`.  Routing on the parent
        // model sends a local subagent to Cursor's cloud (and the reverse), so
        // the override wins when one applies.
        child_override_model(request)
            .map(|model| model.model_id.as_str())
            .filter(|model| !model.is_empty())
            .or_else(|| {
                request
                    .requested_model
                    .as_ref()
                    .map(|model| model.model_id.as_str())
                    .filter(|model| !model.is_empty())
            })
            .or_else(|| {
                request
                    .model_details
                    .as_ref()
                    .map(|model| model.model_id.as_str())
                    .filter(|model| !model.is_empty())
            })
    }

    /// Copy the resolved child model into `requested_model` so both the local
    /// forwarder and the official upstream see one coherent selection.
    ///
    /// Returns whether the message changed, i.e. whether the caller must
    /// re-encode the request body.
    pub fn apply_effective_child_model(&mut self) -> bool {
        let Some(request) = self.run_request_mut() else {
            return false;
        };
        let Some(model) = child_override_model(request).cloned() else {
            return false;
        };
        if request
            .requested_model
            .as_ref()
            .is_some_and(|current| current == &model)
        {
            return false;
        }
        request.requested_model = Some(model);
        true
    }

    pub fn conversation_id(&self) -> Option<&str> {
        self.run_request()?.conversation_id.as_deref()
    }

    pub fn is_background_task_completion(&self) -> bool {
        let Some(agent::agent_client_message::Message::RunRequest(request)) =
            self.message.message.as_ref()
        else {
            return false;
        };
        matches!(
            request
                .action
                .as_ref()
                .and_then(|action| action.action.as_ref()),
            Some(agent::conversation_action::Action::BackgroundTaskCompletionAction(_))
        )
    }

    fn is_runtime_cancellation(&self) -> bool {
        matches!(
            self.message.message.as_ref(),
            Some(agent::agent_client_message::Message::ConversationAction(action))
                if matches!(
                    action.action.as_ref(),
                    Some(agent::conversation_action::Action::CancelAction(_))
                )
        )
    }

    pub fn trace_metadata(&self) -> serde_json::Value {
        let Some(message) = self.message.message.as_ref() else {
            return serde_json::json!({
                "append_seqno": self.seqno,
                "message_type": "empty",
            });
        };
        let agent::agent_client_message::Message::RunRequest(request) = message else {
            return serde_json::json!({
                "append_seqno": self.seqno,
                "message_type": client_message_type(message),
            });
        };
        let (action_type, history_messages, history_images) = request
            .action
            .as_ref()
            .and_then(|action| action.action.as_ref())
            .map(|action| match action {
                agent::conversation_action::Action::UserMessageAction(action) => {
                    let history = action.conversation_history.as_ref();
                    (
                        "user_message",
                        history.map_or(0, |history| history.messages.len()),
                        history.map_or(0, history_image_count),
                    )
                }
                agent::conversation_action::Action::BackgroundTaskCompletionAction(_) => {
                    ("background_task_completion", 0, 0)
                }
                agent::conversation_action::Action::ExecutePlanAction(_) => ("execute_plan", 0, 0),
                agent::conversation_action::Action::SummarizeAction(_) => ("summarize", 0, 0),
                _ => ("other", 0, 0),
            })
            .unwrap_or(("none", 0, 0));
        let state = request.conversation_state.as_ref();
        serde_json::json!({
            "append_seqno": self.seqno,
            "message_type": "run_request",
            "conversation_id": request.conversation_id,
            "model_id": self.model_id(),
            "action_type": action_type,
            "conversation_history_messages": history_messages,
            "conversation_history_images": history_images,
            "root_message_count": state.map_or(0, |state| state.root_prompt_messages_json.len()),
            "turn_count": state.map_or(0, |state| state.turns.len()),
            "prefetched_blob_count": request.pre_fetched_blobs.len(),
        })
    }
}

/// The model a child run should actually use, if an override applies.
///
/// Only `Selection::Model` redirects: `inherit` deliberately keeps the parent
/// model, and `disabled` means the subagent will not run at all.
fn child_override_model(request: &agent::AgentRunRequest) -> Option<&agent::RequestedModel> {
    let subagent_type = request
        .subagent_type_name
        .as_deref()
        .map(str::trim)
        .filter(|name| !name.is_empty())?;
    request
        .subagent_model_overrides
        .iter()
        .find(|override_| subagent_types_match(&override_.subagent_type, subagent_type))
        .and_then(|override_| match override_.selection.as_ref()? {
            agent::subagent_model_override::Selection::Model(model) => Some(model),
            _ => None,
        })
        // `default` is the client's way of spelling inherit.
        .filter(|model| !model.model_id.is_empty() && model.model_id != "default")
}

fn client_message_type(message: &agent::agent_client_message::Message) -> &'static str {
    use agent::agent_client_message::Message;
    match message {
        Message::RunRequest(_) => "run_request",
        Message::ExecClientMessage(_) => "exec_client_message",
        Message::ExecClientControlMessage(_) => "exec_client_control_message",
        Message::KvClientMessage(_) => "kv_client_message",
        Message::ConversationAction(_) => "conversation_action",
        Message::InteractionResponse(_) => "interaction_response",
        Message::ClientHeartbeat(_) => "client_heartbeat",
        Message::PrewarmRequest(_) => "prewarm_request",
    }
}

fn history_image_count(history: &agent::ConversationHistory) -> usize {
    use agent::{
        conversation_history_message::Message,
        conversation_history_tool_result_content::Content as ToolContent,
        conversation_history_user_content::Content as UserContent,
    };
    history
        .messages
        .iter()
        .map(|message| match message.message.as_ref() {
            Some(Message::User(user)) => user
                .content
                .iter()
                .filter(|content| matches!(content.content, Some(UserContent::Image(_))))
                .count(),
            Some(Message::Tool(tool)) => tool
                .content
                .iter()
                .filter(|content| matches!(content.content, Some(ToolContent::Image(_))))
                .count(),
            _ => 0,
        })
        .sum()
}

pub fn decode(request: &ai::BidiAppendRequest) -> Result<DecodedAppend> {
    let request_id = request
        .request_id
        .as_ref()
        .map(|id| id.request_id.as_str())
        .filter(|id| !id.is_empty())
        .ok_or_else(|| Error::Protocol("BidiAppend request_id is required".into()))?;
    if !request.data_binary.is_empty() {
        return Err(Error::Protocol(
            "BidiAppend data_binary is not part of the captured protocol".into(),
        ));
    }
    if request.data.is_empty() {
        return Err(Error::Protocol(
            "BidiAppend contains no AgentClientMessage".into(),
        ));
    }
    let payload = hex::decode(&request.data)
        .map_err(|error| Error::Protocol(format!("invalid BidiAppend hex: {error}")))?;
    Ok(DecodedAppend {
        request_id: request_id.into(),
        seqno: request.append_seqno,
        message: agent::AgentClientMessage::decode(payload.as_slice())?,
    })
}

/// Re-encode a mutated append into the same wire shape it arrived in.
///
/// The `AgentClientMessage` lives hex-encoded inside `BidiAppendRequest.data`,
/// so a rewrite has to re-hex the inner message and then re-frame the outer
/// request exactly as the client framed it — a Connect envelope stays an
/// envelope, a bare proto body stays bare.
pub fn encode(
    original: &ai::BidiAppendRequest,
    decoded: &DecodedAppend,
    framed: bool,
) -> Result<Bytes> {
    let rewritten = ai::BidiAppendRequest {
        data: hex::encode(decoded.message.encode_to_vec()),
        ..original.clone()
    };
    if framed {
        return crate::cursor::connect::encode_message(&rewritten);
    }
    Ok(Bytes::from(rewritten.encode_to_vec()))
}

/// Whether a request body carries a Connect length-prefixed envelope.
pub fn is_framed(body: &[u8]) -> bool {
    if body.len() < 5 {
        return false;
    }
    let flags = body[0];
    let length = u32::from_be_bytes([body[1], body[2], body[3], body[4]]) as usize;
    flags & crate::cursor::connect::END_STREAM_FLAG == 0 && length == body.len() - 5
}

pub async fn append(
    registry: &CursorSessionRegistry,
    request: DecodedAppend,
    parent: Option<CursorParent>,
) -> Result<ai::BidiAppendResponse> {
    if let Some(conversation_id) = request.conversation_id() {
        if request.is_background_task_completion()
            && registry.conversation_cancelled(conversation_id)
        {
            tracing::info!(
                request_id = %request.request_id,
                %conversation_id,
                "dropping background task completion for cancelled conversation"
            );
            return Ok(ai::BidiAppendResponse {});
        }
        if !request.is_background_task_completion() {
            registry.clear_conversation_cancelled(conversation_id);
        }
    }
    let handle = registry.get_or_create(&request.request_id).await?;
    if let Some(conversation_id) = request.conversation_id() {
        handle.set_conversation_id(conversation_id)?;
    }
    if request.is_runtime_cancellation() {
        handle.mark_conversation_cancelled();
        handle.cancel();
    }
    if let Some(parent) = parent {
        handle.set_parent(parent)?;
    }
    if matches!(
        request.message.message.as_ref(),
        Some(agent::agent_client_message::Message::ClientHeartbeat(_))
    ) {
        handle.emit(&interaction::heartbeat())?;
    }
    handle
        .command(CursorCommand::Append {
            seqno: request.seqno,
            message: Box::new(request.message),
        })
        .await?;
    Ok(ai::BidiAppendResponse {})
}

#[cfg(test)]
mod tests {
    use super::*;

    fn encoded(run: agent::AgentRunRequest) -> ai::BidiAppendRequest {
        let message = agent::AgentClientMessage {
            message: Some(agent::agent_client_message::Message::RunRequest(run)),
        };
        ai::BidiAppendRequest {
            data: hex::encode(message.encode_to_vec()),
            request_id: Some(ai::BidiRequestId {
                request_id: "request".into(),
            }),
            append_seqno: 1,
            data_binary: Vec::new(),
        }
    }

    #[test]
    fn route_model_uses_requested_model_id() {
        let decoded = decode(&encoded(agent::AgentRunRequest {
            requested_model: Some(agent::RequestedModel {
                model_id: "33ceed20".into(),
                ..Default::default()
            }),
            ..Default::default()
        }))
        .unwrap();
        assert_eq!(decoded.model_id(), Some("33ceed20"));
    }

    #[test]
    fn route_model_uses_legacy_model_details_when_needed() {
        let decoded = decode(&encoded(agent::AgentRunRequest {
            model_details: Some(agent::ModelDetails {
                model_id: "grok-4.6".into(),
                ..Default::default()
            }),
            ..Default::default()
        }))
        .unwrap();
        assert_eq!(decoded.model_id(), Some("grok-4.6"));
    }

    #[test]
    fn detects_background_task_completion_actions() {
        let decoded = decode(&encoded(agent::AgentRunRequest {
            action: Some(agent::ConversationAction {
                action: Some(
                    agent::conversation_action::Action::BackgroundTaskCompletionAction(
                        agent::BackgroundTaskCompletionAction::default(),
                    ),
                ),
                ..Default::default()
            }),
            ..Default::default()
        }))
        .unwrap();
        assert!(decoded.is_background_task_completion());
    }

    fn child_run(
        subagent_type: &str,
        parent_model: &str,
        override_type: &str,
        selection: Option<agent::subagent_model_override::Selection>,
    ) -> agent::AgentRunRequest {
        agent::AgentRunRequest {
            requested_model: Some(agent::RequestedModel {
                model_id: parent_model.into(),
                ..Default::default()
            }),
            subagent_type_name: Some(subagent_type.into()),
            subagent_model_overrides: vec![agent::SubagentModelOverride {
                subagent_type: override_type.into(),
                selection,
            }],
            ..Default::default()
        }
    }

    fn model_selection(model_id: &str) -> Option<agent::subagent_model_override::Selection> {
        Some(agent::subagent_model_override::Selection::Model(
            agent::RequestedModel {
                model_id: model_id.into(),
                ..Default::default()
            },
        ))
    }

    #[test]
    fn child_run_routes_on_its_override_not_the_parent_model() {
        // An official parent spawning a local explore must route the child
        // locally, and the reverse must route upstream.
        let decoded = decode(&encoded(child_run(
            "explore",
            "cursor-grok-4.6",
            "explore",
            model_selection("0123456789abcdef"),
        )))
        .unwrap();
        assert_eq!(decoded.model_id(), Some("0123456789abcdef"));

        let decoded = decode(&encoded(child_run(
            "explore",
            "0123456789abcdef",
            "explore",
            model_selection("cursor-grok-4.6"),
        )))
        .unwrap();
        assert_eq!(decoded.model_id(), Some("cursor-grok-4.6"));
    }

    #[test]
    fn subagent_type_aliases_are_folded() {
        let decoded = decode(&encoded(child_run(
            "explore",
            "parent",
            "generalPurpose",
            model_selection("child"),
        )))
        .unwrap();
        assert_eq!(decoded.model_id(), Some("child"));

        let decoded = decode(&encoded(child_run(
            "browser-use",
            "parent",
            "browserUse",
            model_selection("child"),
        )))
        .unwrap();
        assert_eq!(decoded.model_id(), Some("child"));
    }

    #[test]
    fn inherit_and_disabled_keep_the_parent_model() {
        for selection in [
            Some(agent::subagent_model_override::Selection::Inherit(true)),
            Some(agent::subagent_model_override::Selection::Disabled(true)),
            model_selection("default"),
            None,
        ] {
            let decoded =
                decode(&encoded(child_run("explore", "parent", "explore", selection))).unwrap();
            assert_eq!(decoded.model_id(), Some("parent"));
        }
    }

    #[test]
    fn an_override_for_another_subagent_type_is_ignored() {
        let decoded = decode(&encoded(child_run(
            "explore",
            "parent",
            "shell",
            model_selection("other-child"),
        )))
        .unwrap();
        assert_eq!(decoded.model_id(), Some("parent"));
    }

    #[test]
    fn a_root_run_never_consults_the_overrides() {
        let mut run = child_run("explore", "parent", "explore", model_selection("child"));
        run.subagent_type_name = None;
        let mut decoded = decode(&encoded(run)).unwrap();
        assert_eq!(decoded.model_id(), Some("parent"));
        assert!(!decoded.apply_effective_child_model());
    }

    #[test]
    fn applying_the_child_model_rewrites_requested_model_once() {
        let request = encoded(child_run(
            "explore",
            "parent",
            "explore",
            model_selection("child"),
        ));
        let mut decoded = decode(&request).unwrap();

        assert!(decoded.apply_effective_child_model());
        assert_eq!(
            decoded
                .run_request()
                .unwrap()
                .requested_model
                .as_ref()
                .unwrap()
                .model_id,
            "child"
        );
        // Idempotent: a second pass has nothing left to change.
        assert!(!decoded.apply_effective_child_model());
    }

    #[test]
    fn rewritten_bodies_round_trip_in_both_wire_shapes() {
        let request = encoded(child_run(
            "explore",
            "parent",
            "explore",
            model_selection("child"),
        ));
        let mut decoded = decode(&request).unwrap();
        assert!(decoded.apply_effective_child_model());

        for framed in [false, true] {
            let body = encode(&request, &decoded, framed).unwrap();
            assert_eq!(is_framed(&body), framed);
            let reparsed: ai::BidiAppendRequest = crate::cursor::connect::decode_unary(&body)
                .expect("rewritten body decodes");
            let reparsed = decode(&reparsed).unwrap();
            assert_eq!(reparsed.model_id(), Some("child"));
            assert_eq!(reparsed.request_id, decoded.request_id);
            assert_eq!(reparsed.seqno, decoded.seqno);
        }
    }
}
