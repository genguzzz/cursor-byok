//! CodeBuddy transport decoration.
//!
//! CodeBuddy exposes an OpenAI-compatible Chat Completions endpoint. Its IDE
//! request uses a dedicated header set, a small set of model-specific body
//! fields, and a gzip-compressed request body.

use std::time::{SystemTime, UNIX_EPOCH};

use reqwest::header::{HeaderMap, HeaderName, HeaderValue};
use serde_json::Value;
use uuid::Uuid;

use crate::{Error, Result};

/// Version and headers mirror the CodeBuddy IDE request captured from Proxyman.
pub const IDE_VERSION: &str = "4.12.0";
const USER_AGENT: &str = "CodeBuddyIDE/4.12.0 CodeBuddy/4.12.0";
const AGENT_INTENT: &str = "craft";
const ENVIRONMENT: &str = "production";
const PRODUCT: &str = "SaaS";
const IDE_TYPE: &str = "CodeBuddyIDE";
const DEFAULT_ENTERPRISE_ID: &str = "etahzsqej0n4";
const DEFAULT_DOMAIN: &str = "tencent.sso.codebuddy.cn";

/// Identity for one outbound call.  Supplied by the caller so a retry reuses
/// the conversation identity while getting fresh trace ids.
#[derive(Clone, Debug)]
pub struct CallIdentity {
    pub conversation_id: String,
    pub conversation_request_id: String,
    pub message_id: String,
}

impl CallIdentity {
    /// Derive identity from the run's own ids, mirroring the CLI's habit of
    /// sending dash-free hex.
    pub fn new(conversation_id: &str, request_id: &str, model_call_id: &str) -> Self {
        Self {
            conversation_id: non_empty(conversation_id)
                .unwrap_or_else(|| Uuid::new_v4().to_string()),
            conversation_request_id: compact_id(request_id),
            message_id: compact_id(model_call_id),
        }
    }
}

fn non_empty(value: &str) -> Option<String> {
    let value = value.trim();
    (!value.is_empty()).then(|| value.to_string())
}

/// Strip dashes from an id, or mint 16 random bytes of hex when absent.
fn compact_id(value: &str) -> String {
    match non_empty(value) {
        Some(value) => value.replace('-', ""),
        None => random_hex(16),
    }
}

/// A trace id is 16 bytes, a span id 8.  `Uuid::new_v4` gives us 16 random
/// bytes without pulling in a separate RNG dependency.
fn random_hex(bytes: usize) -> String {
    let mut out = String::with_capacity(bytes * 2);
    while out.len() < bytes * 2 {
        out.push_str(&hex::encode(Uuid::new_v4().as_bytes()));
    }
    out.truncate(bytes * 2);
    out
}

/// The full CodeBuddy IDE header set observed on `/v2/chat/completions`.
///
/// User-configured headers win over these defaults. Request and trace IDs are
/// generated for every outbound call, while the conversation ID is reused from
/// the Cursor conversation.
pub fn headers(
    custom_headers: &HeaderMap,
    api_key: &str,
    identity: &CallIdentity,
    model_id: &str,
) -> Result<HeaderMap> {
    let mut headers = custom_headers.clone();
    let trace_id = random_hex(16);
    let span_id = random_hex(8);
    let parent_span_id = random_hex(8);
    let request_trace_id = Uuid::new_v4().to_string();
    let monitor_prompt_prepare_time = unix_millis().to_string();
    let monitor_http_send_time = unix_millis().to_string();

    let statics: [(&str, &str); 17] = [
        ("accept", "*/*"),
        ("content-type", "application/json"),
        ("accept-language", "*"),
        ("sec-fetch-mode", "cors"),
        ("accept-encoding", "br, gzip, deflate"),
        ("x-requested-with", "XMLHttpRequest"),
        ("user-agent", USER_AGENT),
        ("x-ide-type", IDE_TYPE),
        ("x-ide-name", IDE_TYPE),
        ("x-ide-version", IDE_VERSION),
        ("x-product-version", IDE_VERSION),
        ("x-env-id", ENVIRONMENT),
        ("x-product", PRODUCT),
        ("x-agent-intent", AGENT_INTENT),
        ("x-enterprise-id", DEFAULT_ENTERPRISE_ID),
        ("x-tenant-id", DEFAULT_ENTERPRISE_ID),
        ("x-domain", DEFAULT_DOMAIN),
    ];
    for (name, value) in statics {
        set_if_absent(&mut headers, name, value)?;
    }

    let dynamics = [
        ("x-conversation-id", identity.conversation_id.as_str()),
        (
            "x-conversation-request-id",
            identity.conversation_request_id.as_str(),
        ),
        ("x-conversation-message-id", identity.message_id.as_str()),
        ("x-request-id", identity.conversation_request_id.as_str()),
        ("x-request-trace-id", request_trace_id.as_str()),
        ("x-b3-traceid", trace_id.as_str()),
        ("x-b3-spanid", span_id.as_str()),
        ("x-b3-parentspanid", parent_span_id.as_str()),
        ("x-b3-sampled", "1"),
        ("x-trace-id", trace_id.as_str()),
        ("x-model-id", model_id),
        (
            "monitor_promptPrepareStartTime",
            monitor_prompt_prepare_time.as_str(),
        ),
        ("monitor_httpSendTime", monitor_http_send_time.as_str()),
    ];
    for (name, value) in dynamics {
        set_if_absent(&mut headers, name, value)?;
    }
    set_if_absent(
        &mut headers,
        "b3",
        &format!("{trace_id}-{span_id}-1-{parent_span_id}"),
    )?;

    if let Some(user_id) = user_id_from_api_key(api_key) {
        set_if_absent(&mut headers, "x-user-id", &user_id)?;
    }
    Ok(headers)
}

fn unix_millis() -> u128 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |duration| duration.as_millis())
}

fn set_if_absent(headers: &mut HeaderMap, name: &str, value: &str) -> Result<()> {
    let name = HeaderName::from_bytes(name.as_bytes())
        .map_err(|error| Error::Config(format!("invalid CodeBuddy header name: {error}")))?;
    if headers.contains_key(&name) {
        return Ok(());
    }
    let value = HeaderValue::from_str(value)
        .map_err(|error| Error::Config(format!("invalid CodeBuddy header value: {error}")))?;
    headers.insert(name, value);
    Ok(())
}

/// The gateway expects `X-User-Id` to match the `sub` claim of the bearer JWT.
fn user_id_from_api_key(api_key: &str) -> Option<String> {
    use base64::{engine::general_purpose::URL_SAFE_NO_PAD, Engine};

    let token = api_key
        .trim()
        .strip_prefix("Bearer ")
        .unwrap_or(api_key.trim());
    let payload = token.split('.').nth(1)?;
    // JWT payloads are unpadded base64url, but be forgiving about padding.
    let decoded = URL_SAFE_NO_PAD.decode(payload.trim_end_matches('=')).ok()?;
    let claims: Value = serde_json::from_slice(&decoded).ok()?;
    claims
        .get("sub")
        .and_then(Value::as_str)
        .and_then(non_empty)
}

/// Apply the CodeBuddy body fields observed in the IDE request.
///
/// DeepSeek Flash does not receive `reasoning_effort`; GPT reasoning models do.
/// The generic OpenAI `max_completion_tokens` and `stream_options` fields are
/// handled by the caller and are not emitted on CodeBuddy requests.
pub fn decorate_body(
    body: &mut Value,
    model_id: &str,
    reasoning_effort: Option<&str>,
) -> Result<()> {
    let object = body
        .as_object_mut()
        .ok_or_else(|| Error::Provider("CodeBuddy request body is not an object".into()))?;
    object.insert(
        "temperature".into(),
        Value::from(if model_id.to_ascii_lowercase().contains("deepseek") {
            1.0
        } else {
            0.6
        }),
    );
    object.insert("reasoning_summary".into(), Value::String("auto".into()));
    object.remove("verbosity");
    if model_id.to_ascii_lowercase().contains("deepseek") {
        object.remove("reasoning_effort");
    } else if let Some(effort) = reasoning_effort {
        object.insert("reasoning_effort".into(), Value::String(effort.into()));
    }
    Ok(())
}

/// Gzip the request body as CodeBuddy does before sending Chat Completions.
pub fn gzip(body: &[u8]) -> Result<Vec<u8>> {
    use std::io::Write;

    use flate2::{write::GzEncoder, Compression};

    let mut encoder = GzEncoder::new(Vec::with_capacity(body.len() / 2), Compression::default());
    encoder
        .write_all(body)
        .map_err(|error| Error::Provider(format!("CodeBuddy gzip failed: {error}")))?;
    encoder
        .finish()
        .map_err(|error| Error::Provider(format!("CodeBuddy gzip failed: {error}")))
}

#[cfg(test)]
mod tests {
    use super::*;
    use base64::{engine::general_purpose::URL_SAFE_NO_PAD, Engine};

    fn identity() -> CallIdentity {
        CallIdentity::new(
            "conversation-1",
            "11111111-2222-3333-4444-555555555555",
            "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
        )
    }

    fn codebuddy_model() -> &'static str {
        "deepseek-v4.1-flash-ioa"
    }

    #[test]
    fn every_codebuddy_ide_header_is_present() {
        let headers = headers(&HeaderMap::new(), "", &identity(), codebuddy_model()).unwrap();
        for name in [
            "accept",
            "content-type",
            "accept-language",
            "sec-fetch-mode",
            "accept-encoding",
            "x-requested-with",
            "user-agent",
            "x-ide-type",
            "x-ide-name",
            "x-ide-version",
            "x-product-version",
            "x-env-id",
            "x-product",
            "x-agent-intent",
            "x-enterprise-id",
            "x-tenant-id",
            "x-domain",
            "x-conversation-id",
            "x-conversation-request-id",
            "x-conversation-message-id",
            "x-request-id",
            "x-request-trace-id",
            "x-model-id",
            "b3",
            "x-b3-traceid",
            "x-b3-spanid",
            "x-b3-parentspanid",
            "x-b3-sampled",
            "x-trace-id",
        ] {
            assert!(headers.contains_key(name), "missing header {name}");
        }
        assert_eq!(headers["user-agent"], USER_AGENT);
        assert_eq!(headers["x-ide-version"], IDE_VERSION);
        assert_eq!(headers["x-domain"], DEFAULT_DOMAIN);
        assert_eq!(headers["x-model-id"], codebuddy_model());
    }

    #[test]
    fn identity_headers_are_dash_free_and_correlated() {
        let identity = identity();
        let headers = headers(&HeaderMap::new(), "", &identity, codebuddy_model()).unwrap();
        assert_eq!(headers["x-conversation-id"], "conversation-1");
        assert_eq!(
            headers["x-conversation-request-id"],
            "111111112222333344445555555555 55".replace(' ', "")
        );
        assert_eq!(
            headers["x-request-id"],
            headers["x-conversation-request-id"]
        );
        assert_ne!(
            headers["x-request-id"],
            headers["x-conversation-message-id"]
        );
    }

    #[test]
    fn trace_headers_agree_across_formats() {
        let headers = headers(&HeaderMap::new(), "", &identity(), codebuddy_model()).unwrap();
        let trace = headers["x-b3-traceid"].to_str().unwrap().to_string();
        let span = headers["x-b3-spanid"].to_str().unwrap().to_string();
        let parent = headers["x-b3-parentspanid"].to_str().unwrap().to_string();
        assert_eq!(trace.len(), 32);
        assert_eq!(span.len(), 16);
        assert_eq!(headers["x-trace-id"], trace.as_str());
        assert_eq!(headers["b3"], format!("{trace}-{span}-1-{parent}").as_str());
        assert_eq!(headers["x-request-trace-id"].len(), 36);
        assert!(!headers.contains_key("traceparent"));
    }

    #[test]
    fn user_headers_are_never_overwritten() {
        let mut custom = HeaderMap::new();
        custom.insert("user-agent", HeaderValue::from_static("mine/1.0"));
        custom.insert("x-domain", HeaderValue::from_static("my.domain"));
        custom.insert("x-conversation-id", HeaderValue::from_static("mine"));

        let headers = headers(&custom, "", &identity(), codebuddy_model()).unwrap();

        assert_eq!(headers["user-agent"], "mine/1.0");
        assert_eq!(headers["x-domain"], "my.domain");
        assert_eq!(headers["x-conversation-id"], "mine");
    }

    #[test]
    fn user_id_comes_from_the_jwt_sub_claim() {
        let payload = URL_SAFE_NO_PAD.encode(br#"{"sub":"user-4242","exp":1}"#);
        let token = format!("header.{payload}.signature");

        let plain = headers(&HeaderMap::new(), &token, &identity(), codebuddy_model()).unwrap();
        assert_eq!(plain["x-user-id"], "user-4242");

        let bearer = headers(
            &HeaderMap::new(),
            &format!("Bearer {token}"),
            &identity(),
            codebuddy_model(),
        )
        .unwrap();
        assert_eq!(bearer["x-user-id"], "user-4242");
    }

    #[test]
    fn a_non_jwt_key_simply_omits_the_user_header() {
        let sent = headers(
            &HeaderMap::new(),
            "sk-plain-key",
            &identity(),
            codebuddy_model(),
        )
        .unwrap();
        assert!(!sent.contains_key("x-user-id"));
    }

    #[test]
    fn deepseek_body_matches_codebuddy_image_request_shape() {
        let mut body = serde_json::json!({
            "model": codebuddy_model(),
            "reasoning_effort": "high",
        });
        decorate_body(&mut body, codebuddy_model(), Some("high")).unwrap();
        assert_eq!(body["temperature"], 1.0);
        assert_eq!(body["reasoning_summary"], "auto");
        assert!(body.get("reasoning_effort").is_none());
        assert!(body.get("verbosity").is_none());

        let mut gpt_body = serde_json::json!({"model": "gpt-5.6-luna"});
        decorate_body(&mut gpt_body, "gpt-5.6-luna", Some("high")).unwrap();
        assert_eq!(gpt_body["reasoning_effort"], "high");
    }

    #[test]
    fn user_extra_params_override_the_decoration() {
        // The router merges extra_params after decorate_body, so a user value
        // must survive.
        let mut body = serde_json::json!({"model": "gpt-5.6-luna"});
        decorate_body(&mut body, "gpt-5.6-luna", Some("low")).unwrap();
        assert_eq!(body["reasoning_effort"], "low");

        crate::provider::merge_extra_params(
            &mut body,
            &serde_json::json!({"reasoning_effort": "high", "reasoning_summary": "off"}),
        )
        .unwrap();

        assert_eq!(body["reasoning_effort"], "high");
        assert_eq!(body["reasoning_summary"], "off");
    }

    #[test]
    fn gzip_round_trips() {
        use std::io::Read;

        let body = br#"{"model":"deepseek-v4-pro"}"#;
        let compressed = gzip(body).unwrap();
        let mut decoded = Vec::new();
        flate2::read::GzDecoder::new(compressed.as_slice())
            .read_to_end(&mut decoded)
            .unwrap();
        assert_eq!(decoded, body);
    }
}
