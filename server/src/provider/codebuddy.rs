//! CodeBuddy transport decoration.
//!
//! CodeBuddy is not a distinct wire protocol: its gateway accepts OpenAI Chat
//! Completions bodies.  What it *does* require is a large, exact set of
//! CLI-identifying headers plus a few body additions, and it rejects
//! `Accept-Encoding` while expecting a gzipped request body.  So instead of a
//! second `Provider` implementation we decorate the OpenAI Chat request.

use reqwest::header::{HeaderMap, HeaderName, HeaderValue};
use serde_json::Value;
use uuid::Uuid;

use crate::{Error, Result};

/// Versions are pinned to the CodeBuddy CLI build whose traffic this mirrors.
/// The gateway gates behaviour on them, so they are not cosmetic.
pub const CLI_VERSION: &str = "2.127.0";
const STAINLESS_VERSION: &str = "6.25.0";
const STAINLESS_RUNTIME: &str = "node";
const NODE_VERSION: &str = "v23.11.1";
const USER_AGENT: &str = "CLI/2.127.0 CodeBuddy/2.127.0";
const AGENT_INTENT: &str = "craft";
const AGENT_PURPOSE: &str = "conversation";
const DEFAULT_ENTERPRISE_ID: &str = "etahzsqej0n4";
const DEFAULT_DOMAIN: &str = "tencent.sso.copilot.tencent.com";

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
            conversation_id: non_empty(conversation_id).unwrap_or_else(|| Uuid::new_v4().to_string()),
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

/// The full CodeBuddy header set.
///
/// `custom_headers` are the user's own configured headers; anything they set
/// wins, so this only fills gaps.  Returns the merged map.
pub fn headers(
    custom_headers: &HeaderMap,
    api_key: &str,
    identity: &CallIdentity,
) -> Result<HeaderMap> {
    let mut headers = custom_headers.clone();
    let trace_id = random_hex(16);
    let span_id = random_hex(8);
    let parent_span_id = random_hex(8);

    let statics: [(&str, &str); 22] = [
        ("accept", "application/json"),
        ("content-type", "application/json"),
        ("x-requested-with", "XMLHttpRequest"),
        ("user-agent", USER_AGENT),
        ("x-ide-type", "CLI"),
        ("x-ide-name", "CLI"),
        ("x-ide-version", CLI_VERSION),
        ("x-product", "SaaS"),
        ("x-agent-intent", AGENT_INTENT),
        ("x-agent-purpose", AGENT_PURPOSE),
        ("x-private-data", "false"),
        ("x-enterprise-id", DEFAULT_ENTERPRISE_ID),
        ("x-tenant-id", DEFAULT_ENTERPRISE_ID),
        ("x-domain", DEFAULT_DOMAIN),
        ("x-stainless-arch", "arm64"),
        ("x-stainless-lang", "js"),
        ("x-stainless-os", "MacOS"),
        ("x-stainless-package-version", STAINLESS_VERSION),
        ("x-stainless-runtime", STAINLESS_RUNTIME),
        ("x-stainless-runtime-version", NODE_VERSION),
        // Retries are handled by our own retry layer, never by the SDK shim.
        ("x-stainless-retry-count", "0"),
        ("x-codebuddy-request", "1"),
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
        ("x-request-id", identity.message_id.as_str()),
        ("x-conversation-message-id", identity.message_id.as_str()),
        (
            "x-root-request-id",
            identity.conversation_request_id.as_str(),
        ),
        ("x-b3-traceid", trace_id.as_str()),
        ("x-b3-spanid", span_id.as_str()),
        ("x-b3-parentspanid", parent_span_id.as_str()),
        ("x-b3-sampled", "1"),
        ("x-trace-id", trace_id.as_str()),
    ];
    for (name, value) in dynamics {
        set_if_absent(&mut headers, name, value)?;
    }
    set_if_absent(
        &mut headers,
        "traceparent",
        &format!("00-{trace_id}-{span_id}-01"),
    )?;
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
    let decoded = URL_SAFE_NO_PAD
        .decode(payload.trim_end_matches('='))
        .ok()?;
    let claims: Value = serde_json::from_slice(&decoded).ok()?;
    claims
        .get("sub")
        .and_then(Value::as_str)
        .and_then(|sub| non_empty(sub))
}

/// Body additions applied before the user's own `extra_params`, so a user can
/// still override either field.
pub fn decorate_body(body: &mut Value, reasoning_effort: Option<&str>) -> Result<()> {
    let object = body
        .as_object_mut()
        .ok_or_else(|| Error::Provider("CodeBuddy request body is not an object".into()))?;
    object.insert("reasoning_summary".into(), Value::String("auto".into()));
    if let Some(verbosity) = verbosity(reasoning_effort) {
        object.insert("verbosity".into(), Value::String(verbosity.into()));
    }
    Ok(())
}

/// CodeBuddy exposes `verbosity` rather than a reasoning-effort ladder.
/// An unmapped or absent effort leaves the field out entirely rather than
/// guessing a default.
fn verbosity(effort: Option<&str>) -> Option<&'static str> {
    match effort?.trim().to_ascii_lowercase().as_str() {
        "low" => Some("low"),
        "medium" => Some("medium"),
        "high" | "xhigh" | "max" => Some("high"),
        _ => None,
    }
}

/// Replace user-role inline images with their paths.
///
/// The gateway silently drops `image_url` parts on user-role messages, so a
/// pasted screenshot would vanish with no diagnostic.  Emitting the paths plus
/// a nudge to call `Read` keeps the image reachable, since tool-result images
/// *are* accepted.
pub fn strip_user_inline_images(messages: &mut Vec<Value>) {
    for message in messages.iter_mut() {
        let Some(object) = message.as_object_mut() else {
            continue;
        };
        if object.get("role").and_then(Value::as_str) != Some("user") {
            continue;
        }
        let Some(parts) = object.get("content").and_then(Value::as_array) else {
            continue;
        };
        let mut text = String::new();
        let mut paths = Vec::new();
        for part in parts {
            match part.get("type").and_then(Value::as_str) {
                Some("text") => {
                    if let Some(value) = part.get("text").and_then(Value::as_str) {
                        text.push_str(value);
                    }
                }
                Some("image_url") => paths.push(image_path(part)),
                _ => {}
            }
        }
        if paths.is_empty() {
            continue;
        }
        let mut rewritten = text;
        rewritten.push_str("\n<selected_images>");
        for path in &paths {
            rewritten.push_str(&format!("\n  <image path=\"{path}\" />"));
        }
        rewritten.push_str(
            "\n</selected_images>\nThe image bytes were not forwarded. Use the Read tool on each \
             path above to view an image.",
        );
        object.insert("content".into(), Value::String(rewritten));
    }
}

fn image_path(part: &Value) -> String {
    part.pointer("/image_url/path")
        .and_then(Value::as_str)
        .map(str::to_string)
        .unwrap_or_else(|| "(unknown path)".into())
}

/// Gzip the request body.  Paired with suppressing `Accept-Encoding`, this
/// matches what the CLI puts on the wire.
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

    #[test]
    fn every_cli_header_is_present() {
        let headers = headers(&HeaderMap::new(), "", &identity()).unwrap();
        for name in [
            "accept",
            "content-type",
            "x-requested-with",
            "user-agent",
            "x-ide-type",
            "x-ide-name",
            "x-ide-version",
            "x-product",
            "x-agent-intent",
            "x-agent-purpose",
            "x-private-data",
            "x-enterprise-id",
            "x-tenant-id",
            "x-domain",
            "x-stainless-arch",
            "x-stainless-lang",
            "x-stainless-os",
            "x-stainless-package-version",
            "x-stainless-runtime",
            "x-stainless-runtime-version",
            "x-stainless-retry-count",
            "x-codebuddy-request",
            "x-conversation-id",
            "x-conversation-request-id",
            "x-request-id",
            "x-conversation-message-id",
            "x-root-request-id",
            "traceparent",
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
        assert_eq!(headers["x-ide-version"], CLI_VERSION);
        // Accept-Encoding must never be sent; the gateway rejects it.
        assert!(!headers.contains_key("accept-encoding"));
    }

    #[test]
    fn identity_headers_are_dash_free_and_correlated() {
        let identity = identity();
        let headers = headers(&HeaderMap::new(), "", &identity).unwrap();
        assert_eq!(headers["x-conversation-id"], "conversation-1");
        assert_eq!(
            headers["x-conversation-request-id"],
            "111111112222333344445555555555 55".replace(' ', "")
        );
        assert_eq!(headers["x-request-id"], headers["x-conversation-message-id"]);
        assert_eq!(
            headers["x-root-request-id"],
            headers["x-conversation-request-id"]
        );
    }

    #[test]
    fn trace_headers_agree_across_formats() {
        let headers = headers(&HeaderMap::new(), "", &identity()).unwrap();
        let trace = headers["x-b3-traceid"].to_str().unwrap().to_string();
        let span = headers["x-b3-spanid"].to_str().unwrap().to_string();
        let parent = headers["x-b3-parentspanid"].to_str().unwrap().to_string();
        assert_eq!(trace.len(), 32);
        assert_eq!(span.len(), 16);
        assert_eq!(headers["x-trace-id"], trace.as_str());
        assert_eq!(
            headers["traceparent"],
            format!("00-{trace}-{span}-01").as_str()
        );
        assert_eq!(
            headers["b3"],
            format!("{trace}-{span}-1-{parent}").as_str()
        );
    }

    #[test]
    fn user_headers_are_never_overwritten() {
        let mut custom = HeaderMap::new();
        custom.insert("user-agent", HeaderValue::from_static("mine/1.0"));
        custom.insert("x-domain", HeaderValue::from_static("my.domain"));
        custom.insert("x-conversation-id", HeaderValue::from_static("mine"));

        let headers = headers(&custom, "", &identity()).unwrap();

        assert_eq!(headers["user-agent"], "mine/1.0");
        assert_eq!(headers["x-domain"], "my.domain");
        assert_eq!(headers["x-conversation-id"], "mine");
    }

    #[test]
    fn user_id_comes_from_the_jwt_sub_claim() {
        let payload = URL_SAFE_NO_PAD.encode(br#"{"sub":"user-4242","exp":1}"#);
        let token = format!("header.{payload}.signature");

        let plain = headers(&HeaderMap::new(), &token, &identity()).unwrap();
        assert_eq!(plain["x-user-id"], "user-4242");

        let bearer = headers(&HeaderMap::new(), &format!("Bearer {token}"), &identity()).unwrap();
        assert_eq!(bearer["x-user-id"], "user-4242");
    }

    #[test]
    fn a_non_jwt_key_simply_omits_the_user_header() {
        let sent = headers(&HeaderMap::new(), "sk-plain-key", &identity()).unwrap();
        assert!(!sent.contains_key("x-user-id"));
    }

    #[test]
    fn verbosity_maps_the_effort_ladder() {
        assert_eq!(verbosity(Some("low")), Some("low"));
        assert_eq!(verbosity(Some("medium")), Some("medium"));
        assert_eq!(verbosity(Some("high")), Some("high"));
        assert_eq!(verbosity(Some("xhigh")), Some("high"));
        assert_eq!(verbosity(Some("MAX")), Some("high"));
        assert_eq!(verbosity(Some("disabled")), None);
        assert_eq!(verbosity(None), None);
    }

    #[test]
    fn body_gains_reasoning_summary_but_never_a_temperature() {
        let mut body = serde_json::json!({"model": "deepseek-v4-pro"});
        decorate_body(&mut body, Some("xhigh")).unwrap();
        assert_eq!(body["reasoning_summary"], "auto");
        assert_eq!(body["verbosity"], "high");
        assert!(body.get("temperature").is_none());

        let mut body = serde_json::json!({"model": "deepseek-v4-pro"});
        decorate_body(&mut body, None).unwrap();
        assert_eq!(body["reasoning_summary"], "auto");
        assert!(body.get("verbosity").is_none());
    }

    #[test]
    fn user_inline_images_become_paths_with_a_read_hint() {
        let mut messages = vec![serde_json::json!({
            "role": "user",
            "content": [
                {"type": "text", "text": "look at this"},
                {"type": "image_url", "image_url": {"url": "data:image/png;base64,AAA", "path": "/tmp/a.png"}},
            ],
        })];

        strip_user_inline_images(&mut messages);

        let content = messages[0]["content"].as_str().unwrap();
        assert!(content.contains("look at this"));
        assert!(content.contains("<image path=\"/tmp/a.png\" />"));
        assert!(content.contains("Read tool"));
        assert!(!content.contains("base64"));
    }

    #[test]
    fn tool_result_images_are_left_alone() {
        let original = serde_json::json!({
            "role": "tool",
            "tool_call_id": "call-1",
            "content": [
                {"type": "image_url", "image_url": {"url": "data:image/png;base64,AAA"}},
            ],
        });
        let mut messages = vec![original.clone()];

        strip_user_inline_images(&mut messages);

        assert_eq!(messages[0], original);
    }

    #[test]
    fn user_extra_params_override_the_decoration() {
        // The router merges extra_params after decorate_body, so a user value
        // must survive.
        let mut body = serde_json::json!({"model": "deepseek-v4-pro"});
        decorate_body(&mut body, Some("low")).unwrap();
        assert_eq!(body["verbosity"], "low");

        crate::provider::merge_extra_params(
            &mut body,
            &serde_json::json!({"verbosity": "high", "reasoning_summary": "off"}),
        )
        .unwrap();

        assert_eq!(body["verbosity"], "high");
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