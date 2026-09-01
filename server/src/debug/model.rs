//! Defines the debug traffic viewer wire types shared with the embedded web UI.
//!
//! Field names and shapes mirror the legacy Go debugger so the ported frontend
//! (`assets/app.js`) runs unchanged: the desktop menu bar debug switch starts an
//! in-process capture hub and this JSON is what the browser renders.
use std::time::Instant;

use serde::Serialize;

/// Traffic origin marker used by the UI "Server" column and filters.
///
/// `Local` and `Official` split inbound Cursor traffic by whether the BYOK
/// server answered the run locally or forwarded it to the official Cursor
/// backend; `Provider` marks the outbound model-gateway hop.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ServerKind {
    Local,
    Official,
    Provider,
}

impl ServerKind {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Local => "local",
            Self::Official => "official",
            Self::Provider => "provider",
        }
    }
}

/// Which hop of the conversation a capture belongs to, surfaced as the
/// `captureSource` field the UI's `resolveServer` fallback reads.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CaptureSource {
    Client,
    Upstream,
    Provider,
}

impl CaptureSource {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Client => "client",
            Self::Upstream => "upstream",
            Self::Provider => "provider",
        }
    }
}

/// Which side of an exchange a payload belongs to.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Side {
    Request,
    Response,
}

/// Lifecycle of a captured exchange, surfaced as the row status dot.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ExchangeState {
    Pending,
    Streaming,
    Completed,
    Error,
}

impl ExchangeState {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Pending => "pending",
            Self::Streaming => "streaming",
            Self::Completed => "completed",
            Self::Error => "error",
        }
    }
}

/// One stable, sorted HTTP header pair. Sensitive values are masked upstream.
#[derive(Clone, Debug, Serialize)]
pub struct Header {
    pub name: String,
    pub value: String,
}

/// One decoded Connect streaming envelope, or a single unary message rendered
/// as a synthetic frame.
#[derive(Clone, Debug, Default, Serialize)]
pub struct FrameView {
    pub index: usize,
    pub flags: u8,
    pub length: usize,
    #[serde(skip_serializing_if = "is_false")]
    pub compressed: bool,
    #[serde(rename = "endStream", skip_serializing_if = "is_false")]
    pub end_stream: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub kind: String,
    #[serde(rename = "messageType", skip_serializing_if = "String::is_empty")]
    pub message_type: String,
    #[serde(rename = "requestId", skip_serializing_if = "String::is_empty")]
    pub request_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub json: String,
    #[serde(rename = "rawHex", skip_serializing_if = "String::is_empty")]
    pub raw_hex: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error: String,
}

impl FrameView {
    pub fn estimated_bytes(&self) -> i64 {
        (64 + self.kind.len()
            + self.message_type.len()
            + self.request_id.len()
            + self.json.len()
            + self.raw_hex.len()
            + self.error.len()) as i64
    }
}

/// Headers plus decoded and raw bodies for one side of an exchange.
#[derive(Clone, Debug, Default, Serialize)]
pub struct Payload {
    pub headers: Vec<Header>,
    #[serde(rename = "contentType", skip_serializing_if = "String::is_empty")]
    pub content_type: String,
    #[serde(rename = "contentCodec", skip_serializing_if = "String::is_empty")]
    pub content_codec: String,
    pub size: i64,
    #[serde(rename = "rawHex", skip_serializing_if = "String::is_empty")]
    pub raw_hex: String,
    #[serde(rename = "rawTruncated", skip_serializing_if = "is_false")]
    pub raw_truncated: bool,
    #[serde(rename = "decodedJson", skip_serializing_if = "String::is_empty")]
    pub decoded_json: String,
    #[serde(rename = "decodeError", skip_serializing_if = "String::is_empty")]
    pub decode_error: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub frames: Vec<FrameView>,
}

impl Payload {
    fn estimated_bytes(&self) -> i64 {
        let mut total = (64
            + self.content_type.len()
            + self.content_codec.len()
            + self.decoded_json.len()
            + self.decode_error.len()
            + self.raw_hex.len()) as i64;
        for header in &self.headers {
            total += (header.name.len() + header.value.len() + 8) as i64;
        }
        for frame in &self.frames {
            total += frame.estimated_bytes();
        }
        total
    }
}

/// Compact request-list representation returned by `GET /api/exchanges`.
#[derive(Clone, Debug, Serialize)]
pub struct ExchangeSummary {
    pub id: String,
    #[serde(rename = "startedAt")]
    pub started_at: String,
    pub method: String,
    pub url: String,
    pub host: String,
    pub path: String,
    pub status: u16,
    pub state: &'static str,
    #[serde(rename = "durationMs")]
    pub duration_ms: i64,
    #[serde(rename = "requestBytes")]
    pub request_bytes: i64,
    #[serde(rename = "responseBytes")]
    pub response_bytes: i64,
    #[serde(rename = "requestId", skip_serializing_if = "String::is_empty")]
    pub request_id: String,
    #[serde(rename = "modelCallId", skip_serializing_if = "String::is_empty")]
    pub model_call_id: String,
    #[serde(rename = "requestKind", skip_serializing_if = "String::is_empty")]
    pub request_kind: String,
    #[serde(rename = "responseKind", skip_serializing_if = "String::is_empty")]
    pub response_kind: String,
    #[serde(rename = "captureSource")]
    pub capture_source: &'static str,
    pub server: &'static str,
    #[serde(rename = "frameCount")]
    pub frame_count: usize,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub error: String,
    #[serde(rename = "storedBytes")]
    pub stored_bytes: i64,
}

impl ExchangeSummary {
    pub fn new(
        id: String,
        method: String,
        url: String,
        host: String,
        path: String,
        capture_source: CaptureSource,
        server: ServerKind,
    ) -> Self {
        Self {
            id,
            started_at: timestamp(),
            method,
            url,
            host,
            path,
            status: 0,
            state: ExchangeState::Pending.as_str(),
            duration_ms: 0,
            request_bytes: 0,
            response_bytes: 0,
            request_id: String::new(),
            model_call_id: String::new(),
            request_kind: String::new(),
            response_kind: String::new(),
            capture_source: capture_source.as_str(),
            server: server.as_str(),
            frame_count: 0,
            error: String::new(),
            stored_bytes: 0,
        }
    }
}

/// A full captured exchange: summary plus request and response payloads.
#[derive(Clone, Debug, Serialize)]
pub struct Exchange {
    #[serde(flatten)]
    pub summary: ExchangeSummary,
    pub request: Payload,
    pub response: Payload,
    /// Wall-clock start used to compute `durationMs`; never serialized.
    #[serde(skip)]
    pub started: Instant,
}

impl Exchange {
    pub fn recompute_stored_bytes(&mut self) {
        let summary = &self.summary;
        let base = (128
            + summary.id.len()
            + summary.url.len()
            + summary.host.len()
            + summary.path.len()
            + summary.request_id.len()
            + summary.request_kind.len()
            + summary.response_kind.len()
            + summary.error.len()) as i64;
        self.summary.stored_bytes =
            base + self.request.estimated_bytes() + self.response.estimated_bytes();
    }

    pub fn elapsed_ms(&self) -> i64 {
        self.started.elapsed().as_millis() as i64
    }

    pub fn state(&self) -> ExchangeState {
        // Reconstruct the enum from the stored string so callers can branch on it.
        match self.summary.state {
            "pending" => ExchangeState::Pending,
            "streaming" => ExchangeState::Streaming,
            "error" => ExchangeState::Error,
            _ => ExchangeState::Completed,
        }
    }

    pub fn set_state(&mut self, state: ExchangeState) {
        self.summary.state = state.as_str();
    }

    pub fn payload_mut(&mut self, side: Side) -> &mut Payload {
        match side {
            Side::Request => &mut self.request,
            Side::Response => &mut self.response,
        }
    }
}

/// Current capture buffer usage, embedded in `/api/status` and query responses.
#[derive(Clone, Copy, Debug, Serialize)]
pub struct StoreStats {
    pub count: usize,
    #[serde(rename = "usedBytes")]
    pub used_bytes: i64,
    #[serde(rename = "maxStoreBytes")]
    pub max_store_bytes: i64,
    #[serde(rename = "maxExchanges")]
    pub max_exchanges: usize,
}

/// Current RFC 3339 UTC timestamp used for `startedAt`, matching the Go
/// `time.Time` JSON encoding the frontend parses with `new Date(...)`.
pub fn timestamp() -> String {
    use chrono::Utc;
    Utc::now().to_rfc3339_opts(chrono::SecondsFormat::Millis, true)
}

fn is_false(value: &bool) -> bool {
    !*value
}
