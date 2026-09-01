//! Implements the Anthropic provider adapter.
use std::sync::OnceLock;

use async_stream::try_stream;
use base64::{engine::general_purpose::STANDARD, Engine};
use eventsource_stream::Eventsource;
use futures_util::StreamExt;
use serde_json::{json, Value};
use uuid::Uuid;

use crate::{
    config::ProviderConfig,
    model::{ContentPart, ModelInvocation, ProjectedContent, ProjectedMessage, Role, Usage},
    Error, Result,
};

use super::{
    map_sse_error, merge_extra_params, provider_event_error,
    recorder::recorded_headers,
    retry::{send_with_retry, Attempt, RetryPolicy},
    CallRecorder, FinishReason, ModelEvent, Provider, ProviderStream,
};

const DEFAULT_MAX_OUTPUT_TOKENS: u64 = 65_000;

/// 与 tclaude / claude-code CLI 对齐的 anthropic-beta 头（沿用旧 Go 版）。
const CLAUDE_CODE_BETA_HEADER: &str = "claude-code-20250219,context-1m-2025-08-07,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,effort-2025-11-24";

/// 与 claude-code SDK 对齐的 User-Agent（沿用旧 Go 版）。
const CLAUDE_CODE_USER_AGENT: &str = "claude-cli/2.1.154 (external, cli)";

/// 与 claude-code CLI 对齐的 billing 标记，作为 system 首个文本块注入（沿用旧 Go 版）。
const CLAUDE_CODE_BILLING_HEADER: &str =
    "x-anthropic-billing-header: cc_version=2.1.154; cc_entrypoint=cli; cch=37703;";

/// 进程级稳定设备标识，与 `metadata.user_id.device_id` 保持一致。
fn claude_code_device_id() -> &'static str {
    static DEVICE_ID: OnceLock<String> = OnceLock::new();
    DEVICE_ID.get_or_init(|| Uuid::new_v4().to_string())
}

/// 进程级稳定会话标识，与 `X-Claude-Code-Session-Id` 头及 `metadata.user_id.session_id` 保持一致。
fn claude_code_session_id() -> &'static str {
    static SESSION_ID: OnceLock<String> = OnceLock::new();
    SESSION_ID.get_or_init(|| Uuid::new_v4().to_string())
}

/// 将 Rust 的 `std::env::consts::ARCH` 映射为 claude-code SDK 的 `X-Stainless-Arch` 值。
fn claude_code_arch() -> &'static str {
    match std::env::consts::ARCH {
        "aarch64" => "arm64",
        "x86_64" => "amd64",
        arch => arch,
    }
}

/// 将 Rust 的 `std::env::consts::OS` 映射为 claude-code SDK 的 `X-Stainless-OS` 值。
fn claude_code_os() -> &'static str {
    match std::env::consts::OS {
        "macos" => "MacOS",
        "windows" => "Windows",
        "linux" => "Linux",
        os => os,
    }
}

pub struct AnthropicProvider {
    client: reqwest::Client,
    config: ProviderConfig,
    recorder: Option<CallRecorder>,
}

impl AnthropicProvider {
    pub fn new(client: reqwest::Client, config: ProviderConfig) -> Self {
        Self {
            client,
            config,
            recorder: None,
        }
    }

    pub fn with_recorder(mut self, recorder: Option<CallRecorder>) -> Self {
        self.recorder = recorder;
        self
    }
}

impl Provider for AnthropicProvider {
    fn stream(
        &self,
        invocation: ModelInvocation,
        cancellation: tokio_util::sync::CancellationToken,
    ) -> ProviderStream {
        let client = self.client.clone();
        let config = self.config.clone();
        let recorder = self.recorder.clone();
        Box::pin(try_stream! {
            let ModelInvocation { call_id, request, run_id, .. } = invocation;
            let mut messages = anthropic_messages(&request.history)?;
            mark_cache_breakpoint(&mut messages);
            let system = if config.claude_code_compat {
                // tclaude / claude-code 路径：system 首个文本块注入 billing 标记（沿用旧 Go 版）。
                let mut blocks = vec![json!({"type": "text", "text": CLAUDE_CODE_BILLING_HEADER})];
                if !request.prompt.instructions.is_empty() {
                    blocks.push(json!({
                        "type": "text",
                        "text": request.prompt.instructions,
                        "cache_control": {"type": "ephemeral"}
                    }));
                }
                json!(blocks)
            } else if request.prompt.instructions.is_empty() {
                Value::String(String::new())
            } else {
                json!([{
                    "type": "text",
                    "text": request.prompt.instructions,
                    "cache_control": {"type": "ephemeral"}
                }])
            };
            let max_tokens = request.model.max_output_tokens.or(config.max_output_tokens)
                .unwrap_or(DEFAULT_MAX_OUTPUT_TOKENS);
            let mut body = json!({
                "model": request.model.model_id, "system": system, "messages": messages,
                "max_tokens": max_tokens, "stream": true
            });
            if !request.prompt.tools.is_empty() {
                let tool_count = request.prompt.tools.len();
                body["tools"] = json!(request.prompt.tools.iter().enumerate().map(|(index, tool)| {
                    let mut value = json!({
                        "name": tool.name, "description": tool.description, "input_schema": tool.parameters
                    });
                    if index + 1 == tool_count {
                        value["cache_control"] = json!({"type": "ephemeral"});
                    }
                    value
                }).collect::<Vec<_>>());
            }
            apply_model(&mut body, &request.model)?;
            merge_extra_params(&mut body, &request.model.extra_params)?;
            if config.claude_code_compat {
                apply_claude_code_body(&mut body, claude_code_device_id(), claude_code_session_id());
            }
            let request_headers = recorded_headers(
                &config,
                &[("content-type", "application/json"), ("anthropic-version", "2023-06-01")],
            );
            if let Some(recorder) = &recorder {
                recorder.request(request_headers.clone(), &body).await?;
            }
            let attempt = send_with_retry(
                "Anthropic",
                || {
                    let mut request = client.post(&config.request_url)
                        .header("anthropic-version", "2023-06-01");
                    if config.claude_code_compat {
                        // tclaude daemon 仅使用 Authorization: Bearer 鉴权。
                        request = request
                            .header("Authorization", format!("Bearer {}", bearer_token(&config.api_key)))
                            .header("anthropic-beta", CLAUDE_CODE_BETA_HEADER)
                            .header("x-app", "cli")
                            .header("anthropic-dangerous-direct-browser-access", "true")
                            .header("accept", "application/json")
                            .header("accept-encoding", "gzip, deflate, br, zstd")
                            .header("connection", "keep-alive")
                            .header("user-agent", CLAUDE_CODE_USER_AGENT)
                            .header("x-claude-code-session-id", claude_code_session_id())
                            .header("x-stainless-arch", claude_code_arch())
                            .header("x-stainless-lang", "js")
                            .header("x-stainless-os", claude_code_os())
                            .header("x-stainless-package-version", "0.94.0")
                            .header("x-stainless-runtime", "node")
                            .header("x-stainless-runtime-version", "v24.3.0")
                            .header("x-stainless-retry-count", "0")
                            .header("x-stainless-timeout", "600");
                    } else {
                        request = request.header("x-api-key", &config.api_key);
                    }
                    request.headers(config.custom_headers.clone()).json(&body)
                },
                RetryPolicy { retries: config.retry_count, ..RetryPolicy::default() },
                &cancellation,
                recorder.as_ref(),
                request_headers,
                &body,
            ).await?;
            let Attempt::Response(response) = attempt else { return };
            yield ModelEvent::Start { model_call_id: call_id.clone() };
            let chunk_recorder = recorder.clone();
            let hop = super::retry::ProviderHopMeta {
                provider: "Anthropic".to_owned(),
                model_call_id: call_id.clone(),
                request_id: run_id.clone(),
                request_header: config.custom_headers.clone(),
                request_body: serde_json::to_vec(&body).unwrap_or_default(),
                started: std::time::Instant::now(),
            };
            let stream = super::retry::tee_provider_response(response, hop);
            let chunks = stream
                .map(|chunk| chunk.map_err(Error::from))
                .then(move |chunk| {
                    let recorder = chunk_recorder.clone();
                    async move {
                        let chunk = chunk?;
                        if let Some(recorder) = recorder { recorder.response_chunk(&chunk).await?; }
                        Ok::<_, Error>(chunk)
                    }
                });
            let source = chunks.eventsource();
            futures_util::pin_mut!(source);
            let mut block_types = std::collections::BTreeMap::<usize, String>::new();
            let mut thinking_text = std::collections::HashMap::<usize, String>::new();
            let mut thinking_signatures = std::collections::HashMap::<usize, String>::new();
            let mut thinking_blocks = Vec::new();
            let mut finish = None;
            let mut saw_tool = false;
            let mut terminal = false;
            let mut final_usage = None::<Usage>;
            while let Some(event) = tokio::select! {
                _ = cancellation.cancelled() => { return; }
                event = source.next() => event,
            } {
                let event = event.map_err(|error| map_sse_error("Anthropic", error))?;
                let value: Value = serde_json::from_str(&event.data)?;
                if let Some(error) = provider_event_error("Anthropic", &value) {
                    Err(error)?;
                }
                let data_kind = value.get("type").and_then(Value::as_str);
                let kind = match event.event.as_str() {
                    "" | "message" => data_kind.unwrap_or(event.event.as_str()),
                    kind => kind,
                };
                match kind {
                    "message_start" => if let Some(usage) = value.pointer("/message/usage") {
                        merge_usage(final_usage.get_or_insert_default(), anthropic_usage(usage));
                    },
                    "content_block_start" => {
                        let index = required_u64(&value, "index")? as usize;
                        let block = value.get("content_block").unwrap_or(&Value::Null);
                        let kind = required_string(block, "type")?;
                        block_types.insert(index, kind.into());
                        match kind {
                            "text" => yield ModelEvent::TextStart,
                            "thinking" => {
                                thinking_text.insert(index, String::new());
                                thinking_signatures.insert(index, String::new());
                                yield ModelEvent::ThinkingStart;
                            }
                            "redacted_thinking" => thinking_blocks.push(block.clone()),
                            "tool_use" => {
                                saw_tool = true;
                                yield ModelEvent::ToolCallStart {
                                    index,
                                    call_id: required_string(block, "id")?.into(),
                                    name: required_string(block, "name")?.into(),
                                };
                            }
                            _ => {}
                        }
                    }
                    "content_block_delta" => {
                        let index = required_u64(&value, "index")? as usize;
                        let delta = value.get("delta").unwrap_or(&Value::Null);
                        let delta_kind = required_string(delta, "type")?;
                        if let std::collections::btree_map::Entry::Vacant(entry) = block_types.entry(index) {
                            match delta_kind {
                                "text_delta" => {
                                    entry.insert("text".into());
                                    yield ModelEvent::TextStart;
                                }
                                "thinking_delta" | "signature_delta" => {
                                    entry.insert("thinking".into());
                                    thinking_text.insert(index, String::new());
                                    thinking_signatures.insert(index, String::new());
                                    yield ModelEvent::ThinkingStart;
                                }
                                _ => {}
                            }
                        }
                        match delta_kind {
                            "text_delta" => if let Some(text) = delta.get("text").and_then(Value::as_str) { yield ModelEvent::TextDelta(text.into()); },
                            "thinking_delta" => if let Some(text) = delta.get("thinking").and_then(Value::as_str) {
                                thinking_text.entry(index).or_default().push_str(text);
                                yield ModelEvent::ThinkingDelta(text.into());
                            },
                            "signature_delta" => if let Some(signature) = delta.get("signature").and_then(Value::as_str) {
                                thinking_signatures.entry(index).or_default().push_str(signature);
                            },
                            "input_json_delta" => if let Some(text) = delta.get("partial_json").and_then(Value::as_str) { yield ModelEvent::ToolCallArgumentsDelta { index, delta: text.into() }; },
                            _ => {}
                        }
                    }
                    "content_block_stop" => {
                        let index = required_u64(&value, "index")? as usize;
                        if let Some(kind) = block_types.remove(&index) {
                            for event in close_anthropic_block(index, &kind, &mut thinking_text, &mut thinking_signatures, &mut thinking_blocks) {
                                yield event;
                            }
                        }
                    }
                    "message_delta" => {
                        if let Some(usage) = value.get("usage") {
                            merge_usage(final_usage.get_or_insert_default(), anthropic_usage(usage));
                        }
                        finish = match value.pointer("/delta/stop_reason").and_then(Value::as_str) {
                            Some("tool_use") => Some(FinishReason::ToolUse),
                            Some("max_tokens" | "model_context_window_exceeded") => Some(FinishReason::Length),
                            Some("end_turn" | "stop_sequence" | "pause_turn" | "refusal") => Some(FinishReason::Stop),
                            None => finish,
                            Some(_) => Some(if saw_tool { FinishReason::ToolUse } else { FinishReason::Stop }),
                        };
                    }
                    "message_stop" => {
                        for (index, kind) in std::mem::take(&mut block_types) {
                            for event in close_anthropic_block(index, &kind, &mut thinking_text, &mut thinking_signatures, &mut thinking_blocks) {
                                yield event;
                            }
                        }
                        terminal = true;
                        if !thinking_blocks.is_empty() {
                            yield ModelEvent::ProviderReplayState(
                                crate::model::ProviderReplayState {
                                    provider_kind: "anthropic".into(),
                                    value: json!({"blocks": std::mem::take(&mut thinking_blocks)}),
                                },
                            );
                        }
                        if let Some(usage) = final_usage {
                            yield ModelEvent::Usage(usage);
                        }
                        let finish = match finish {
                            Some(FinishReason::Length) => FinishReason::Length,
                            _ if saw_tool => FinishReason::ToolUse,
                            Some(finish) => finish,
                            None => FinishReason::Stop,
                        };
                        yield ModelEvent::Done(finish);
                    }
                    _ => {}
                }
            }
            if !terminal && finish.is_some() {
                for (index, kind) in std::mem::take(&mut block_types) {
                    for event in close_anthropic_block(index, &kind, &mut thinking_text, &mut thinking_signatures, &mut thinking_blocks) {
                        yield event;
                    }
                }
                if !thinking_blocks.is_empty() {
                    yield ModelEvent::ProviderReplayState(crate::model::ProviderReplayState {
                        provider_kind: "anthropic".into(),
                        value: json!({"blocks": std::mem::take(&mut thinking_blocks)}),
                    });
                }
                if let Some(usage) = final_usage { yield ModelEvent::Usage(usage); }
                terminal = true;
                let finish = match finish {
                    Some(FinishReason::Length) => FinishReason::Length,
                    _ if saw_tool => FinishReason::ToolUse,
                    Some(finish) => finish,
                    None => FinishReason::Stop,
                };
                yield ModelEvent::Done(finish);
            }
            if !terminal {
                Err(Error::Provider("Anthropic stream ended without message_stop".into()))?;
            }
        })
    }
}

fn close_anthropic_block(
    index: usize,
    kind: &str,
    thinking_text: &mut std::collections::HashMap<usize, String>,
    thinking_signatures: &mut std::collections::HashMap<usize, String>,
    thinking_blocks: &mut Vec<Value>,
) -> Vec<ModelEvent> {
    match kind {
        "text" => vec![ModelEvent::TextEnd],
        "thinking" => {
            let thinking = thinking_text.remove(&index).unwrap_or_default();
            let signature = thinking_signatures.remove(&index).unwrap_or_default();
            if !signature.is_empty() {
                thinking_blocks.push(json!({
                    "type": "thinking",
                    "thinking": thinking,
                    "signature": signature,
                }));
            }
            vec![ModelEvent::ThinkingEnd]
        }
        "tool_use" => vec![ModelEvent::ToolCallEnd { index }],
        _ => Vec::new(),
    }
}

fn apply_model(body: &mut Value, model: &crate::model::ModelSpec) -> Result<()> {
    let object = body
        .as_object_mut()
        .ok_or_else(|| Error::Provider("Anthropic request body is not an object".into()))?;
    if model.reasoning.enabled {
        object.insert(
            "thinking".into(),
            json!({"type":"adaptive", "display":"summarized"}),
        );
    }
    if let Some(effort) = &model.reasoning.effort {
        object.insert("output_config".into(), json!({"effort":effort}));
    }
    Ok(())
}

/// 剥离 api_key 的 `Bearer ` 前缀，返回纯 token（与旧 Go 版 anthropicCompatibleAuthToken 对齐）。
fn bearer_token(api_key: &str) -> &str {
    let token = api_key.trim();
    if token.len() >= 7 && token[..7].eq_ignore_ascii_case("Bearer ") {
        token[7..].trim()
    } else {
        token
    }
}

/// 设置与 tclaude daemon 期望对齐的请求体字段（metadata.user_id 与 context_management）。
///
/// <p>`metadata.user_id` 必须是一个 JSON 编码的字符串（而非嵌套对象），与 claude-code CLI
/// 的序列化方式一致；`session_id` 与 `X-Claude-Code-Session-Id` 头保持一致。</p>
fn apply_claude_code_body(body: &mut Value, device_id: &str, session_id: &str) {
    let Some(object) = body.as_object_mut() else {
        return;
    };
    if !object.contains_key("metadata") {
        let user_id = serde_json::to_string(&json!({
            "device_id": device_id,
            "account_uuid": "",
            "session_id": session_id,
        }))
        .unwrap_or_else(|_| {
            format!(r#"{{"device_id":"unknown","account_uuid":"","session_id":"{session_id}"}}"#)
        });
        object.insert("metadata".into(), json!({"user_id": user_id}));
    }
    if !object.contains_key("context_management") {
        object.insert(
            "context_management".into(),
            json!({"edits": [{"type": "clear_thinking_20251015", "keep": "all"}]}),
        );
    }
}

fn merge_usage(total: &mut Usage, update: Usage) {
    merge_usage_field(&mut total.input_tokens, update.input_tokens);
    merge_usage_field(&mut total.output_tokens, update.output_tokens);
    merge_usage_field(&mut total.cache_read_tokens, update.cache_read_tokens);
    merge_usage_field(&mut total.cache_write_tokens, update.cache_write_tokens);
    merge_usage_field(&mut total.reasoning_tokens, update.reasoning_tokens);
}

fn merge_usage_field(total: &mut Option<u64>, update: Option<u64>) {
    if let Some(update) = update {
        *total = Some(total.map_or(update, |current| current.max(update)));
    }
}

fn anthropic_messages(messages: &[ProjectedMessage]) -> Result<Vec<Value>> {
    let mut output = Vec::new();
    for message in messages {
        match &message.content {
            ProjectedContent::Parts(parts) => {
                let content = anthropic_parts(&message.role, parts)?;
                if !content.is_empty() {
                    push_anthropic(&mut output, role_name(&message.role), content);
                }
            }
            ProjectedContent::ToolResult(result) => {
                let content = if result.provider_parts.is_empty() {
                    Value::String(result.content.clone())
                } else {
                    Value::Array(anthropic_parts(&Role::User, &result.provider_parts)?)
                };
                push_anthropic(
                    &mut output,
                    "user",
                    vec![json!({
                        "type": "tool_result",
                        "tool_use_id": result.call_id,
                        "content": content,
                    })],
                );
            }
            ProjectedContent::Assistant {
                text,
                replay_state,
                calls,
                ..
            } => {
                let mut content = Vec::new();
                if let Some(blocks) = replay_state
                    .as_ref()
                    .filter(|state| state.provider_kind == "anthropic")
                    .and_then(|state| state.value.get("blocks"))
                    .and_then(Value::as_array)
                {
                    content.extend(blocks.iter().cloned());
                }
                if !text.is_empty() {
                    content.push(json!({"type": "text", "text": text}));
                }
                content.extend(calls.iter().map(|call| {
                    json!({
                        "type": "tool_use",
                        "id": call.call_id,
                        "name": call.name,
                        "input": call.arguments,
                    })
                }));
                if !content.is_empty() {
                    push_anthropic(&mut output, "assistant", content);
                }
            }
        }
    }
    Ok(output)
}

fn mark_cache_breakpoint(messages: &mut [Value]) {
    let Some(message) = messages
        .iter_mut()
        .rev()
        .find(|message| message.get("role").and_then(Value::as_str) == Some("user"))
    else {
        return;
    };
    let Some(content) = message.get_mut("content").and_then(Value::as_array_mut) else {
        return;
    };
    for block in content.iter_mut().rev() {
        let kind = block.get("type").and_then(Value::as_str);
        if !matches!(kind, Some("text" | "image" | "tool_result")) {
            continue;
        }
        if let Some(block) = block.as_object_mut() {
            block.insert("cache_control".into(), json!({"type": "ephemeral"}));
            return;
        }
    }
}

fn anthropic_parts(role: &Role, parts: &[ContentPart]) -> Result<Vec<Value>> {
    parts
        .iter()
        .filter_map(|part| match part {
            ContentPart::Text { text } if text.is_empty() => None,
            ContentPart::Text { text } => Some(Ok(json!({"type":"text", "text":text}))),
            ContentPart::Image { mime_type, data } if *role == Role::User => Some(Ok(json!({
                "type":"image",
                "source":{
                    "type":"base64",
                    "media_type":mime_type,
                    "data":STANDARD.encode(data),
                },
            }))),
            ContentPart::Image { .. } => Some(Err(Error::Protocol(
                "Anthropic only accepts images in user messages".into(),
            ))),
        })
        .collect()
}

fn push_anthropic(output: &mut Vec<Value>, role: &str, mut content: Vec<Value>) {
    if let Some(last) = output
        .last_mut()
        .filter(|last| last.get("role").and_then(Value::as_str) == Some(role))
    {
        if let Some(existing) = last.get_mut("content").and_then(Value::as_array_mut) {
            existing.append(&mut content);
            return;
        }
    }
    output.push(json!({"role":role, "content":content}));
}

fn role_name(role: &Role) -> &'static str {
    match role {
        Role::System => "user",
        Role::User => "user",
        Role::Assistant => "assistant",
        Role::Tool => "user",
    }
}

fn required_string<'a>(value: &'a Value, name: &str) -> Result<&'a str> {
    value
        .get(name)
        .and_then(Value::as_str)
        .ok_or_else(|| Error::Provider(format!("Anthropic event is missing {name}")))
}

fn required_u64(value: &Value, name: &str) -> Result<u64> {
    value
        .get(name)
        .and_then(Value::as_u64)
        .ok_or_else(|| Error::Provider(format!("Anthropic event is missing {name}")))
}

fn anthropic_usage(value: &Value) -> Usage {
    Usage {
        input_tokens: value.get("input_tokens").and_then(Value::as_u64),
        output_tokens: value.get("output_tokens").and_then(Value::as_u64),
        total_tokens: value.get("total_tokens").and_then(Value::as_u64),
        cache_read_tokens: value.get("cache_read_input_tokens").and_then(Value::as_u64),
        cache_write_tokens: value
            .get("cache_creation_input_tokens")
            .and_then(Value::as_u64),
        reasoning_tokens: None,
    }
}
