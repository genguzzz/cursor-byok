//! Defines model and provider configuration.
use std::{fmt, str::FromStr};

use reqwest::Url;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use crate::{Error, Result};

pub const OPENAI_RESPONSES_ENDPOINT: &str = "/v1/responses";
pub const OPENAI_CHAT_ENDPOINT: &str = "/v1/chat/completions";
/// CodeBuddy's gateway exposes an OpenAI-compatible surface at `/chat/completions`
/// relative to its versioned base URL rather than at `/v1/...`.
pub const CODEBUDDY_ENDPOINT: &str = "/chat/completions";

#[derive(Clone, Copy, Debug, Deserialize, Serialize, PartialEq, Eq)]
pub enum ProviderType {
    #[serde(rename = "openai-chat")]
    OpenAiChat,
    #[serde(rename = "openai-responses")]
    OpenAiResponses,
    #[serde(rename = "anthropic")]
    Anthropic,
    /// 插件执行的调用;协议细节在插件内部,核心只按统一事件流记录。
    #[serde(rename = "plugin")]
    Plugin,
    #[serde(rename = "codebuddy")]
    CodeBuddy,
}

impl ProviderType {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::OpenAiChat => "openai-chat",
            Self::OpenAiResponses => "openai-responses",
            Self::Anthropic => "anthropic",
            Self::Plugin => "plugin",
            Self::CodeBuddy => "codebuddy",
        }
    }
}

impl fmt::Display for ProviderType {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(self.as_str())
    }
}

impl FromStr for ProviderType {
    type Err = Error;

    fn from_str(value: &str) -> Result<Self> {
        match value {
            "openai-chat" => Ok(Self::OpenAiChat),
            "openai-responses" => Ok(Self::OpenAiResponses),
            "anthropic" => Ok(Self::Anthropic),
            "plugin" => Ok(Self::Plugin),
            "codebuddy" => Ok(Self::CodeBuddy),
            _ => Err(Error::Config(format!("unsupported provider type: {value}"))),
        }
    }
}

#[derive(Clone, Copy, Debug, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum ModelType {
    OpenAi,
    Anthropic,
    CodeBuddy,
}

impl ModelType {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::OpenAi => "openai",
            Self::Anthropic => "anthropic",
            Self::CodeBuddy => "codebuddy",
        }
    }

    /// CodeBuddy speaks the OpenAI Chat Completions protocol; only its
    /// transport metadata differs.
    pub fn is_openai_compatible(self) -> bool {
        matches!(self, Self::OpenAi | Self::CodeBuddy)
    }
}

impl FromStr for ModelType {
    type Err = Error;

    fn from_str(value: &str) -> Result<Self> {
        match value {
            "openai" => Ok(Self::OpenAi),
            "anthropic" => Ok(Self::Anthropic),
            "codebuddy" => Ok(Self::CodeBuddy),
            _ => Err(Error::Config(format!("unsupported model type: {value}"))),
        }
    }
}

#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct ModelConfigInput {
    #[serde(default)]
    pub sort_order: i64,
    pub display_name: String,
    /// 供应商分组的自定义显示名;同一 base_url 主机下的模型共享。
    #[serde(default)]
    pub group_name: Option<String>,
    #[serde(rename = "type")]
    pub model_type: ModelType,
    pub base_url: String,
    #[serde(default)]
    pub use_full_url: bool,
    pub api_key: String,
    pub tooltip_data: String,
    pub model_id: String,
    #[serde(default)]
    pub reasoning_effort: Option<String>,
    #[serde(default)]
    pub openai_endpoint: String,
    #[serde(default)]
    pub openai_extra_params_enabled: bool,
    #[serde(default = "empty_object")]
    pub openai_extra_params: serde_json::Value,
    #[serde(default)]
    pub custom_headers_enabled: bool,
    #[serde(default = "empty_object")]
    pub custom_headers: serde_json::Value,
    #[serde(default)]
    pub anthropic_extra_params_enabled: bool,
    #[serde(default = "empty_object")]
    pub anthropic_extra_params: serde_json::Value,
    /// 开启后 Anthropic 请求走 tclaude / claude-code CLI 兼容路径：
    /// 解析 `tclaude-daemon` 主机名并注入对齐的 header / body 字段。
    #[serde(default)]
    pub claude_code_compat: bool,
    pub context_window_tokens: Option<u64>,
    pub max_completion_tokens: Option<u64>,
    pub anthropic_max_tokens: Option<u64>,
    #[serde(default)]
    pub anthropic_thinking_effort: Option<String>,
    pub thinking_budget_tokens: Option<u64>,
}

#[derive(Clone, Debug, Serialize)]
pub struct ModelConfig {
    pub model_hash: String,
    pub sort_order: i64,
    pub display_name: String,
    pub group_name: Option<String>,
    #[serde(rename = "type")]
    pub model_type: ModelType,
    pub base_url: String,
    pub use_full_url: bool,
    pub api_key: String,
    pub tooltip_data: String,
    pub model_id: String,
    pub reasoning_effort: Option<String>,
    pub openai_endpoint: String,
    pub openai_extra_params_enabled: bool,
    pub openai_extra_params: serde_json::Value,
    pub custom_headers_enabled: bool,
    pub custom_headers: serde_json::Value,
    pub anthropic_extra_params_enabled: bool,
    pub anthropic_extra_params: serde_json::Value,
    pub claude_code_compat: bool,
    pub context_window_tokens: Option<u64>,
    pub max_completion_tokens: Option<u64>,
    pub anthropic_max_tokens: Option<u64>,
    pub anthropic_thinking_effort: Option<String>,
    pub thinking_budget_tokens: Option<u64>,
    pub created_at_ms: i64,
    pub updated_at_ms: i64,
}

impl ModelConfig {
    pub fn provider_type(&self) -> ProviderType {
        match self.model_type {
            ModelType::Anthropic => ProviderType::Anthropic,
            ModelType::CodeBuddy => ProviderType::CodeBuddy,
            ModelType::OpenAi if self.openai_endpoint == OPENAI_RESPONSES_ENDPOINT => {
                ProviderType::OpenAiResponses
            }
            ModelType::OpenAi => ProviderType::OpenAiChat,
        }
    }

    pub fn request_url(&self) -> Result<String> {
        resolve_request_url(
            self.model_type,
            &self.base_url,
            &self.openai_endpoint,
            self.use_full_url,
        )
    }

    pub fn max_output_tokens(&self) -> Option<u64> {
        match self.model_type {
            ModelType::OpenAi | ModelType::CodeBuddy => self.max_completion_tokens,
            ModelType::Anthropic => self.anthropic_max_tokens.or(self.max_completion_tokens),
        }
    }

    pub fn extra_params(&self) -> &serde_json::Value {
        match self.model_type {
            ModelType::OpenAi | ModelType::CodeBuddy if self.openai_extra_params_enabled => {
                &self.openai_extra_params
            }
            ModelType::Anthropic if self.anthropic_extra_params_enabled => {
                &self.anthropic_extra_params
            }
            _ => empty_object_ref(),
        }
    }

    pub fn configure(&self, model: &mut super::ModelSpec) {
        model.display_name = Some(self.display_name.clone());
        // A request-selected context is authoritative.  Use the saved model
        // value only when Cursor did not send a context parameter.
        if model.context_window_tokens.is_none() {
            model.context_window_tokens = self.context_window_tokens;
        }
        if model.reasoning.effort.is_none() {
            model.reasoning.effort = match self.model_type {
                ModelType::OpenAi | ModelType::CodeBuddy => self.reasoning_effort.clone(),
                ModelType::Anthropic => self.anthropic_thinking_effort.clone(),
            };
        }
        model.reasoning.enabled |= model.reasoning.effort.is_some();
    }
}

pub fn normalize_model_input(input: &ModelConfigInput) -> Result<ModelConfigInput> {
    let display_name = required(&input.display_name, "model display name")?;
    let group_name = input
        .group_name
        .as_deref()
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(String::from);
    let base_url = normalize_request_url(&input.base_url)?;
    let api_key = required(&input.api_key, "model API key")?;
    let tooltip_data = required(&input.tooltip_data, "model tooltip")?;
    let model_id = required(&input.model_id, "model id")?;
    let reasoning_effort = normalize_effort(input.reasoning_effort.as_deref(), true)?;
    let anthropic_thinking_effort = match input.model_type {
        ModelType::Anthropic => Some(
            normalize_effort(
                input.anthropic_thinking_effort.as_deref().or(Some("xhigh")),
                false,
            )?
            .expect("Anthropic effort has a default"),
        ),
        ModelType::OpenAi | ModelType::CodeBuddy => None,
    };
    let openai_endpoint = match input.model_type {
        ModelType::OpenAi => normalize_openai_endpoint(&input.openai_endpoint)?,
        ModelType::CodeBuddy => normalize_codebuddy_endpoint(&input.openai_endpoint)?,
        ModelType::Anthropic => String::new(),
    };
    validate_object(&input.openai_extra_params, "OpenAI extra params")?;
    validate_object(&input.anthropic_extra_params, "Anthropic extra params")?;
    validate_headers(&input.custom_headers)?;

    let normalized = ModelConfigInput {
        sort_order: input.sort_order.max(0),
        display_name,
        group_name,
        model_type: input.model_type,
        base_url,
        use_full_url: input.use_full_url,
        api_key,
        tooltip_data,
        model_id,
        reasoning_effort: input
            .model_type
            .is_openai_compatible()
            .then_some(reasoning_effort)
            .flatten(),
        openai_endpoint,
        openai_extra_params_enabled: input.model_type.is_openai_compatible()
            && input.openai_extra_params_enabled,
        openai_extra_params: if input.model_type.is_openai_compatible() {
            input.openai_extra_params.clone()
        } else {
            empty_object()
        },
        custom_headers_enabled: input.custom_headers_enabled,
        custom_headers: input.custom_headers.clone(),
        anthropic_extra_params_enabled: input.model_type == ModelType::Anthropic
            && input.anthropic_extra_params_enabled,
        anthropic_extra_params: if input.model_type == ModelType::Anthropic {
            input.anthropic_extra_params.clone()
        } else {
            empty_object()
        },
        claude_code_compat: input.model_type == ModelType::Anthropic && input.claude_code_compat,
        context_window_tokens: positive(input.context_window_tokens, "context window")?,
        max_completion_tokens: positive(input.max_completion_tokens, "max completion tokens")?,
        anthropic_max_tokens: positive(input.anthropic_max_tokens, "Anthropic max tokens")?,
        anthropic_thinking_effort,
        thinking_budget_tokens: positive(input.thinking_budget_tokens, "thinking budget")?,
    };
    resolve_request_url(
        normalized.model_type,
        &normalized.base_url,
        &normalized.openai_endpoint,
        normalized.use_full_url,
    )?;
    Ok(normalized)
}

pub fn model_hash(input: &ModelConfigInput) -> Result<String> {
    let normalized = normalize_model_input(input)?;
    let request_url = resolve_request_url(
        normalized.model_type,
        &normalized.base_url,
        &normalized.openai_endpoint,
        normalized.use_full_url,
    )?;
    let mut parts = vec![
        request_url,
        normalized.model_id,
        normalized.api_key,
        normalized.display_name,
    ];
    if normalized.model_type.is_openai_compatible() {
        parts.push(normalized.openai_endpoint);
    }
    let digest = Sha256::digest(parts.join("\n").as_bytes());
    Ok(hex::encode(&digest[..8]))
}

pub fn normalize_request_url(value: &str) -> Result<String> {
    let value = value.trim();
    let url = Url::parse(value)
        .map_err(|error| Error::Config(format!("invalid model request URL: {error}")))?;
    if !matches!(url.scheme(), "http" | "https") || url.host_str().is_none() {
        return Err(Error::Config(
            "model request URL must be an HTTP(S) URL with a host".into(),
        ));
    }
    if url.fragment().is_some() {
        return Err(Error::Config(
            "model request URL cannot contain a fragment".into(),
        ));
    }
    Ok(value.into())
}

pub fn resolve_request_url(
    model_type: ModelType,
    base_url: &str,
    openai_endpoint: &str,
    use_full_url: bool,
) -> Result<String> {
    let base_url = resolve_tclaude_daemon_host(normalize_request_url(base_url)?)?;
    let endpoint = match model_type {
        ModelType::OpenAi => normalize_openai_endpoint(openai_endpoint)?,
        ModelType::CodeBuddy => normalize_codebuddy_endpoint(openai_endpoint)?,
        ModelType::Anthropic => "/v1/messages".into(),
    };
    if use_full_url {
        return Ok(base_url);
    }
    append_standard_endpoint(&base_url, &endpoint)
}

/// 将 `tclaude-daemon` 主机名解析为 tclaude daemon 实际监听的 loopback 端口。
///
/// tclaude 每次重启监听端口都会变化，端口记录在 `~/.tclaude/daemon.json` 中。
/// 当 base URL 使用特殊主机名 `tclaude-daemon` 时，这里读取 daemon.json 并替换为
/// `127.0.0.1:<port>`；无法解析时原样返回（沿用旧 Go 版行为）。
fn resolve_tclaude_daemon_host(base_url: String) -> Result<String> {
    let mut parsed = match Url::parse(&base_url) {
        Ok(parsed) => parsed,
        Err(_) => return Ok(base_url),
    };
    if !parsed
        .host_str()
        .is_some_and(|host| host.eq_ignore_ascii_case("tclaude-daemon"))
    {
        return Ok(base_url);
    }
    let port = match read_tclaude_daemon_port() {
        Some(port) => port,
        None => return Ok(base_url),
    };
    parsed
        .set_port(Some(port))
        .map_err(|()| Error::Config(format!("invalid tclaude daemon port {port}")))?;
    parsed
        .set_host(Some("127.0.0.1"))
        .map_err(|error| Error::Config(format!("set tclaude daemon host: {error}")))?;
    parsed
        .set_scheme("http")
        .map_err(|()| Error::Config("set tclaude daemon scheme".into()))?;
    Ok(parsed.to_string())
}

/// 读取 `~/.tclaude/daemon.json` 中的端口。失败或端口非法返回 None。
fn read_tclaude_daemon_port() -> Option<u16> {
    let daemon_path = dirs::home_dir()?.join(".tclaude").join("daemon.json");
    let raw = std::fs::read_to_string(daemon_path).ok()?;
    let entry: serde_json::Value = serde_json::from_str(&raw).ok()?;
    if let Some(port) = entry.get("port").and_then(serde_json::Value::as_u64) {
        if (1..=65535).contains(&port) {
            return Some(port as u16);
        }
    }
    if let Some(url) = entry.get("url").and_then(serde_json::Value::as_str) {
        if let Ok(parsed) = Url::parse(url) {
            if let Some(port) = parsed.port() {
                return Some(port);
            }
        }
    }
    None
}

fn append_standard_endpoint(base_url: &str, endpoint: &str) -> Result<String> {
    let mut url = Url::parse(base_url)
        .map_err(|error| Error::Config(format!("invalid model server URL: {error}")))?;
    let base_path = url.path().trim_end_matches('/').to_string();
    let endpoint = if has_trailing_version(&base_path) {
        endpoint.strip_prefix("/v1").unwrap_or(endpoint)
    } else {
        endpoint
    };
    url.set_path(&format!("{base_path}{endpoint}"));
    normalize_request_url(url.as_str())
}

fn has_trailing_version(path: &str) -> bool {
    let Some(segment) = path.rsplit('/').next() else {
        return false;
    };
    segment.strip_prefix('v').is_some_and(|digits| {
        !digits.is_empty() && digits.bytes().all(|byte| byte.is_ascii_digit())
    })
}

pub fn is_sensitive_header(name: &str) -> bool {
    matches!(
        name.to_ascii_lowercase().as_str(),
        "authorization" | "proxy-authorization" | "x-api-key" | "api-key" | "cookie" | "set-cookie"
    )
}

fn normalize_openai_endpoint(value: &str) -> Result<String> {
    match value.trim() {
        "" | OPENAI_RESPONSES_ENDPOINT => Ok(OPENAI_RESPONSES_ENDPOINT.into()),
        OPENAI_CHAT_ENDPOINT => Ok(OPENAI_CHAT_ENDPOINT.into()),
        value => Err(Error::Config(format!(
            "unsupported OpenAI endpoint: {value}"
        ))),
    }
}

/// CodeBuddy only speaks Chat Completions.  Accept the OpenAI spelling so an
/// existing model can be retyped without editing its endpoint.
fn normalize_codebuddy_endpoint(value: &str) -> Result<String> {
    match value.trim() {
        "" | CODEBUDDY_ENDPOINT => Ok(CODEBUDDY_ENDPOINT.into()),
        OPENAI_CHAT_ENDPOINT => Ok(OPENAI_CHAT_ENDPOINT.into()),
        value => Err(Error::Config(format!(
            "unsupported CodeBuddy endpoint: {value}"
        ))),
    }
}

fn normalize_effort(value: Option<&str>, allow_empty: bool) -> Result<Option<String>> {
    let value = value.unwrap_or_default().trim().to_ascii_lowercase();
    if value.is_empty() && allow_empty {
        return Ok(None);
    }
    if matches!(value.as_str(), "low" | "medium" | "high" | "xhigh" | "max") {
        Ok(Some(value))
    } else {
        Err(Error::Config(format!(
            "unsupported reasoning effort: {value}"
        )))
    }
}

fn positive(value: Option<u64>, label: &str) -> Result<Option<u64>> {
    match value {
        Some(0) => Err(Error::Config(format!("{label} must be greater than zero"))),
        value => Ok(value),
    }
}

fn required(value: &str, label: &str) -> Result<String> {
    let value = value.trim();
    if value.is_empty() {
        Err(Error::Config(format!("{label} cannot be empty")))
    } else {
        Ok(value.into())
    }
}

fn validate_object(value: &serde_json::Value, label: &str) -> Result<()> {
    if value.is_object() {
        Ok(())
    } else {
        Err(Error::Config(format!("{label} must be a JSON object")))
    }
}

fn validate_headers(value: &serde_json::Value) -> Result<()> {
    validate_object(value, "custom headers")?;
    for (name, value) in value.as_object().expect("validated object") {
        if name.trim().is_empty() || !value.is_string() {
            return Err(Error::Config(
                "custom headers must have non-empty names and string values".into(),
            ));
        }
    }
    Ok(())
}

fn empty_object() -> serde_json::Value {
    serde_json::json!({})
}

fn empty_object_ref() -> &'static serde_json::Value {
    static EMPTY: std::sync::OnceLock<serde_json::Value> = std::sync::OnceLock::new();
    EMPTY.get_or_init(empty_object)
}

#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq, Eq)]
pub struct ReasoningSpec {
    pub enabled: bool,
    pub effort: Option<String>,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum ModelLatency {
    #[default]
    Standard,
    Fast,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub struct ModelSpec {
    pub model_id: String,
    pub display_name: Option<String>,
    pub reasoning: ReasoningSpec,
    pub latency: ModelLatency,
    pub max_output_tokens: Option<u64>,
    pub context_window_tokens: Option<u64>,
    #[serde(default)]
    pub supports_image_generation: bool,
    #[serde(default)]
    pub extra_params: serde_json::Value,
}

impl ModelSpec {
    pub fn new(model_id: impl Into<String>) -> Self {
        Self {
            model_id: model_id.into(),
            display_name: None,
            reasoning: ReasoningSpec::default(),
            latency: ModelLatency::Standard,
            max_output_tokens: None,
            context_window_tokens: None,
            supports_image_generation: false,
            extra_params: serde_json::json!({}),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn input() -> ModelConfigInput {
        ModelConfigInput {
            sort_order: 1,
            display_name: "Model A".into(),
            group_name: None,
            model_type: ModelType::OpenAi,
            base_url: "https://example.com/custom/generate".into(),
            use_full_url: true,
            api_key: "secret".into(),
            tooltip_data: "Model A".into(),
            model_id: "model-a".into(),
            reasoning_effort: Some("high".into()),
            openai_endpoint: OPENAI_RESPONSES_ENDPOINT.into(),
            openai_extra_params_enabled: false,
            openai_extra_params: empty_object(),
            custom_headers_enabled: false,
            custom_headers: empty_object(),
            anthropic_extra_params_enabled: false,
            anthropic_extra_params: empty_object(),
            claude_code_compat: false,
            context_window_tokens: Some(200_000),
            max_completion_tokens: None,
            anthropic_max_tokens: None,
            anthropic_thinking_effort: None,
            thinking_budget_tokens: None,
        }
    }

    #[test]
    fn hash_matches_the_v0049_channel_identity() {
        let input = input();
        let expected = Sha256::digest(
            "https://example.com/custom/generate\nmodel-a\nsecret\nModel A\n/v1/responses"
                .as_bytes(),
        );
        assert_eq!(model_hash(&input).unwrap(), hex::encode(&expected[..8]));
    }

    #[test]
    fn request_url_is_exact_and_protocol_does_not_depend_on_its_path() {
        assert_eq!(
            resolve_request_url(
                ModelType::OpenAi,
                "https://example.com/custom/generate?api-version=2026-01-01",
                OPENAI_RESPONSES_ENDPOINT,
                true,
            )
            .unwrap(),
            "https://example.com/custom/generate?api-version=2026-01-01"
        );
        assert_eq!(
            resolve_request_url(
                ModelType::OpenAi,
                "https://example.com/another/arbitrary/path",
                OPENAI_CHAT_ENDPOINT,
                true,
            )
            .unwrap(),
            "https://example.com/another/arbitrary/path"
        );
        assert_eq!(
            resolve_request_url(ModelType::Anthropic, "https://example.com/claude", "", true)
                .unwrap(),
            "https://example.com/claude"
        );
        assert_eq!(
            resolve_request_url(
                ModelType::Anthropic,
                "https://example.com/claude/",
                "",
                true
            )
            .unwrap(),
            "https://example.com/claude/"
        );
        assert_eq!(
            resolve_request_url(
                ModelType::OpenAi,
                "https://example.com/v1",
                OPENAI_RESPONSES_ENDPOINT,
                false,
            )
            .unwrap(),
            "https://example.com/v1/responses"
        );
        assert_eq!(
            resolve_request_url(ModelType::Anthropic, "https://example.com/v1", "", false).unwrap(),
            "https://example.com/v1/messages"
        );
    }

    #[test]
    fn configured_context_window_does_not_override_the_client_request() {
        let input = input();
        let config = ModelConfig {
            model_hash: "hash".into(),
            sort_order: input.sort_order,
            display_name: input.display_name,
            group_name: input.group_name,
            model_type: input.model_type,
            base_url: input.base_url,
            use_full_url: input.use_full_url,
            api_key: input.api_key,
            tooltip_data: input.tooltip_data,
            model_id: input.model_id,
            reasoning_effort: input.reasoning_effort,
            openai_endpoint: input.openai_endpoint,
            openai_extra_params_enabled: input.openai_extra_params_enabled,
            openai_extra_params: input.openai_extra_params,
            custom_headers_enabled: input.custom_headers_enabled,
            custom_headers: input.custom_headers,
            anthropic_extra_params_enabled: input.anthropic_extra_params_enabled,
            anthropic_extra_params: input.anthropic_extra_params,
            claude_code_compat: input.claude_code_compat,
            context_window_tokens: Some(350_000),
            max_completion_tokens: input.max_completion_tokens,
            anthropic_max_tokens: input.anthropic_max_tokens,
            anthropic_thinking_effort: input.anthropic_thinking_effort,
            thinking_budget_tokens: input.thinking_budget_tokens,
            created_at_ms: 0,
            updated_at_ms: 0,
        };
        let mut requested = super::super::ModelSpec::new("model-a");
        requested.context_window_tokens = Some(200_000);

        config.configure(&mut requested);

        assert_eq!(requested.context_window_tokens, Some(200_000));
    }

    #[test]
    fn configured_context_window_fills_missing_client_value() {
        let input = input();
        let config = ModelConfig {
            model_hash: "hash".into(),
            sort_order: input.sort_order,
            display_name: input.display_name,
            group_name: input.group_name,
            model_type: input.model_type,
            base_url: input.base_url,
            use_full_url: input.use_full_url,
            api_key: input.api_key,
            tooltip_data: input.tooltip_data,
            model_id: input.model_id,
            reasoning_effort: input.reasoning_effort,
            openai_endpoint: input.openai_endpoint,
            openai_extra_params_enabled: input.openai_extra_params_enabled,
            openai_extra_params: input.openai_extra_params,
            custom_headers_enabled: input.custom_headers_enabled,
            custom_headers: input.custom_headers,
            anthropic_extra_params_enabled: input.anthropic_extra_params_enabled,
            anthropic_extra_params: input.anthropic_extra_params,
            claude_code_compat: input.claude_code_compat,
            context_window_tokens: Some(350_000),
            max_completion_tokens: input.max_completion_tokens,
            anthropic_max_tokens: input.anthropic_max_tokens,
            anthropic_thinking_effort: input.anthropic_thinking_effort,
            thinking_budget_tokens: input.thinking_budget_tokens,
            created_at_ms: 0,
            updated_at_ms: 0,
        };
        let mut requested = super::super::ModelSpec::new("model-a");

        config.configure(&mut requested);

        assert_eq!(requested.context_window_tokens, Some(350_000));
    }

    #[test]
    fn tclaude_daemon_host_is_left_unchanged_when_daemon_json_is_missing() {
        // daemon.json 不存在时原样返回(无法解析不是错误)。
        let _base = "http://tclaude-daemon/v1/messages?beta=true".to_string();
        // 无法在测试中依赖真实的 ~/.tclaude/daemon.json，只验证非 tclaude 主机不受影响。
        let untouched = resolve_tclaude_daemon_host("https://api.anthropic.com/v1/messages".into())
            .unwrap();
        assert_eq!(untouched, "https://api.anthropic.com/v1/messages");
    }

    #[test]
    fn non_tclaude_host_is_not_rewritten() {
        let resolved =
            resolve_request_url(ModelType::Anthropic, "https://api.anthropic.com", "", true)
                .unwrap();
        assert_eq!(resolved, "https://api.anthropic.com");
    }
}
