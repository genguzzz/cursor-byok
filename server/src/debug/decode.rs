//! Decodes Connect envelopes and Cursor protobuf messages into UI-facing JSON.
//!
//! Outbound provider traffic is already JSON, so this module targets inbound
//! Cursor traffic: it splits Connect frames, reflects agent.v1 messages through
//! the generated descriptor set (mirroring the legacy Go `protojson` rendering),
//! and falls back to UTF-8 / text rendering for non-protobuf bodies.
use std::{io::Read, sync::OnceLock};

use prost::Message as ProstMessage;
use prost_reflect::{DescriptorPool, DynamicMessage, MessageDescriptor, SerializeOptions};

use super::model::FrameView;

const MAX_CONNECT_FRAME_BYTES: usize = 64 << 20;
const COMPRESSED_FLAG: u8 = 0x01;
const END_STREAM_FLAG: u8 = 0x02;

/// Cursor wire endpoints the debugger decodes specially.
pub const BIDI_APPEND_PATH: &str = "/aiserver.v1.BidiService/BidiAppend";
pub const RUN_SSE_PATH: &str = "/agent.v1.AgentService/RunSSE";
pub const NOTIFY_CONVERSATION_CLONE_PATH: &str = "/agent.v1.AgentService/NotifyConversationClone";
pub const UPLOAD_CONVERSATION_BLOBS_PATH: &str = "/agent.v1.AgentService/UploadConversationBlobs";

/// agent.v1 message rendered from the RunSSE request unary body.
pub const RUN_SSE_REQUEST_TYPE: &str = "agent.v1.BidiRequestId";
/// agent.v1 message streamed back over RunSSE responses.
pub const RUN_SSE_RESPONSE_TYPE: &str = "agent.v1.AgentServerMessage";
/// agent.v1 message carried inside a BidiAppend request `data` field.
pub const AGENT_CLIENT_MESSAGE_TYPE: &str = "agent.v1.AgentClientMessage";

static DESCRIPTOR_POOL: OnceLock<DescriptorPool> = OnceLock::new();

fn descriptor_pool() -> &'static DescriptorPool {
    DESCRIPTOR_POOL.get_or_init(|| {
        let bytes = include_bytes!(concat!(env!("OUT_DIR"), "/agent_v1_descriptor.bin"));
        DescriptorPool::decode(bytes.as_ref()).expect("agent.v1 descriptor set is valid")
    })
}

fn message_descriptor(type_name: &str) -> Option<MessageDescriptor> {
    descriptor_pool().get_message_by_name(type_name)
}

/// Reflects a protobuf message into indented JSON with proto field names, the
/// same shape the legacy `protojson` renderer produced.
pub fn decode_message_json(type_name: &str, payload: &[u8]) -> Result<String, String> {
    let descriptor =
        message_descriptor(type_name).ok_or_else(|| format!("unknown message type {type_name}"))?;
    let message = DynamicMessage::decode(descriptor, payload)
        .map_err(|error| format!("protobuf decode failed: {error}"))?;
    let options = SerializeOptions::new()
        .use_proto_field_name(true)
        .skip_default_fields(true);
    let mut serializer = serde_json::Serializer::pretty(Vec::new());
    message
        .serialize_with_options(&mut serializer, &options)
        .map_err(|error| format!("json encode failed: {error}"))?;
    String::from_utf8(serializer.into_inner())
        .map_err(|error| format!("json utf-8 failed: {error}"))
}

/// Returns the active oneof field name, else the message name — the value the
/// UI shows in the "message" column.
pub fn message_kind(type_name: &str, payload: &[u8]) -> Option<String> {
    let descriptor = message_descriptor(type_name)?;
    let message = DynamicMessage::decode(descriptor.clone(), payload).ok()?;
    for oneof in descriptor.oneofs() {
        if let Some(field) = oneof.fields().find(|field| message.has_field(field)) {
            return Some(field.name().to_owned());
        }
    }
    Some(descriptor.name().to_owned())
}

/// Streaming Connect frame splitter that decodes each envelope as it completes.
pub struct FrameDecoder {
    buffer: Vec<u8>,
    message_type: String,
    codec: String,
    max_frames: usize,
    frame_count: usize,
}

impl FrameDecoder {
    pub fn new(
        message_type: impl Into<String>,
        codec: impl Into<String>,
        max_frames: usize,
    ) -> Self {
        Self {
            buffer: Vec::new(),
            message_type: message_type.into(),
            codec: codec.into().trim().to_owned(),
            max_frames,
            frame_count: 0,
        }
    }

    /// Feeds bytes and yields any frames that became complete.
    pub fn push(&mut self, payload: &[u8]) -> Vec<FrameView> {
        let mut frames = Vec::new();
        if payload.is_empty() || self.frame_count >= self.max_frames {
            return frames;
        }
        self.buffer.extend_from_slice(payload);
        while self.buffer.len() >= 5 && self.frame_count < self.max_frames {
            let flags = self.buffer[0];
            let length = u32::from_be_bytes([
                self.buffer[1],
                self.buffer[2],
                self.buffer[3],
                self.buffer[4],
            ]) as usize;
            if length > MAX_CONNECT_FRAME_BYTES {
                frames.push(self.emit(FrameView {
                    flags,
                    length,
                    error: "Connect frame length out of range".into(),
                    ..Default::default()
                }));
                self.buffer.clear();
                return frames;
            }
            if self.buffer.len() < 5 + length {
                break;
            }
            let frame_payload = self.buffer[5..5 + length].to_vec();
            self.buffer.drain(..5 + length);
            let decoded = self.decode(flags, &frame_payload);
            frames.push(self.emit(decoded));
        }
        frames
    }

    /// Flushes a trailing partial frame, if any, as an error frame.
    pub fn finish(&mut self) -> Option<FrameView> {
        if !self.buffer.is_empty() && self.frame_count < self.max_frames {
            let frame = FrameView {
                length: self.buffer.len(),
                raw_hex: clipped_hex(&self.buffer, 4096),
                error: "incomplete Connect frame at end of stream".into(),
                ..Default::default()
            };
            self.buffer.clear();
            return Some(self.emit(frame));
        }
        None
    }

    fn emit(&mut self, mut frame: FrameView) -> FrameView {
        frame.index = self.frame_count;
        self.frame_count += 1;
        frame
    }

    fn decode(&self, flags: u8, payload: &[u8]) -> FrameView {
        let mut frame = FrameView {
            flags,
            length: payload.len(),
            compressed: flags & COMPRESSED_FLAG != 0,
            end_stream: flags & END_STREAM_FLAG != 0,
            ..Default::default()
        };
        let decoded = if frame.compressed {
            match decompress(payload, &self.codec) {
                Ok(bytes) => bytes,
                Err(error) => {
                    frame.error = error;
                    frame.raw_hex = clipped_hex(payload, 4096);
                    return frame;
                }
            }
        } else {
            payload.to_vec()
        };
        if frame.end_stream {
            frame.kind = "end_stream".into();
            frame.message_type = "connect.error.v1.EndStreamResponse".into();
            frame.json = pretty_json(&decoded);
            return frame;
        }
        match decode_message_json(&self.message_type, &decoded) {
            Ok(json) => {
                frame.message_type = self.message_type.clone();
                frame.kind = message_kind(&self.message_type, &decoded).unwrap_or_default();
                if self.message_type == RUN_SSE_REQUEST_TYPE {
                    frame.request_id = request_id_of(&decoded);
                }
                frame.json = json;
            }
            Err(error) => {
                frame.error = error;
                frame.raw_hex = clipped_hex(payload, 4096);
            }
        }
        frame
    }
}

/// Decodes an entire Connect body offline into frames (used when the streaming
/// path did not populate frames, e.g. buffered upstream responses).
pub fn decode_frames_offline(
    message_type: &str,
    codec: &str,
    max_frames: usize,
    payload: &[u8],
) -> Vec<FrameView> {
    if payload.is_empty() || max_frames == 0 {
        return Vec::new();
    }
    let mut decoder = FrameDecoder::new(message_type, codec, max_frames);
    let mut frames = decoder.push(payload);
    if let Some(trailer) = decoder.finish() {
        frames.push(trailer);
    }
    frames
}

fn request_id_of(payload: &[u8]) -> String {
    message_descriptor(RUN_SSE_REQUEST_TYPE)
        .and_then(|descriptor| DynamicMessage::decode(descriptor, payload).ok())
        .and_then(|message| {
            message
                .get_field_by_name("request_id")
                .map(|value| value.as_str().unwrap_or_default().trim().to_owned())
        })
        .unwrap_or_default()
}

fn decompress(payload: &[u8], codec: &str) -> Result<Vec<u8>, String> {
    if !codec.is_empty() && !codec.eq_ignore_ascii_case("gzip") {
        return Err(format!("unsupported compression codec {codec:?}"));
    }
    let decoder = flate2::read::GzDecoder::new(payload);
    let mut output = Vec::new();
    decoder
        .take((MAX_CONNECT_FRAME_BYTES + 1) as u64)
        .read_to_end(&mut output)
        .map_err(|error| format!("gzip decompress failed: {error}"))?;
    if output.len() > MAX_CONNECT_FRAME_BYTES {
        return Err(format!(
            "gzip output exceeds {MAX_CONNECT_FRAME_BYTES} byte limit"
        ));
    }
    Ok(output)
}

/// Turns non-protobuf bodies (JSON errors, text/HTML) into UI-visible content.
/// Returns `(decoded_json, kind)` when a text rendering is available.
pub fn fallback_display_body(content_type: &str, payload: &[u8]) -> Option<(String, String)> {
    if payload.is_empty() {
        return None;
    }
    let content_type_lower = content_type.trim().to_ascii_lowercase();
    if content_type_lower.contains("json") {
        let trimmed = payload.trim_ascii();
        if serde_json::from_slice::<serde_json::Value>(trimmed).is_ok() {
            return Some((pretty_json(trimmed), "json".into()));
        }
        let wrapped = serde_json::json!({
            "content_type": content_type,
            "text": String::from_utf8_lossy(payload),
        });
        return Some((pretty_value(&wrapped), "json_text".into()));
    }
    if content_type_lower.contains("text/")
        || content_type_lower.contains("xml")
        || content_type_lower.contains("javascript")
        || looks_like_utf8_text(payload)
    {
        let wrapped = serde_json::json!({
            "content_type": content_type,
            "text": String::from_utf8_lossy(payload),
        });
        let kind = if content_type_lower.contains("html") {
            "html"
        } else {
            "text"
        };
        return Some((pretty_value(&wrapped), kind.into()));
    }
    None
}

fn looks_like_utf8_text(payload: &[u8]) -> bool {
    let sample = &payload[..payload.len().min(4096)];
    if std::str::from_utf8(sample).is_err() {
        return false;
    }
    let printable = sample
        .iter()
        .filter(|&&byte| {
            byte == b'\n'
                || byte == b'\r'
                || byte == b'\t'
                || (32..127).contains(&byte)
                || byte >= 0x80
        })
        .count();
    printable * 10 >= sample.len() * 8
}

fn pretty_json(payload: &[u8]) -> String {
    match serde_json::from_slice::<serde_json::Value>(payload) {
        Ok(value) => pretty_value(&value),
        Err(_) => String::from_utf8_lossy(payload).into_owned(),
    }
}

fn pretty_value(value: &serde_json::Value) -> String {
    serde_json::to_string_pretty(value).unwrap_or_default()
}

fn clipped_hex(payload: &[u8], max: usize) -> String {
    if payload.len() > max {
        format!("{}...", hex::encode(&payload[..max]))
    } else {
        hex::encode(payload)
    }
}

/// Returns the `message_type` used to decode Connect frames for `path`, or
/// `None` when `path` is not the RunSSE stream endpoint.
pub fn run_sse_message_type(path: &str, response_side: bool) -> Option<&'static str> {
    if path != RUN_SSE_PATH {
        return None;
    }
    Some(if response_side {
        RUN_SSE_RESPONSE_TYPE
    } else {
        RUN_SSE_REQUEST_TYPE
    })
}

/// Picks the content codec for the request side, mirroring the Go
/// `requestContentCodec`: Connect streams use `Connect-Content-Encoding`.
pub fn request_content_codec(path: &str, headers: &http::HeaderMap) -> String {
    if path == RUN_SSE_PATH {
        header_text(headers, "connect-content-encoding")
    } else {
        header_text(headers, "content-encoding")
    }
}

/// Picks the content codec for the response side, mirroring the Go
/// `responseContentCodec`.
pub fn response_content_codec(path: &str, headers: &http::HeaderMap) -> String {
    if path == RUN_SSE_PATH {
        return header_text(headers, "connect-content-encoding");
    }
    let connect = header_text(headers, "connect-content-encoding");
    if !connect.is_empty() {
        return connect;
    }
    header_text(headers, "content-encoding")
}

fn header_text(headers: &http::HeaderMap, name: &str) -> String {
    headers
        .get(name)
        .and_then(|value| value.to_str().ok())
        .map(str::trim)
        .unwrap_or_default()
        .to_owned()
}

/// First non-empty frame kind scanning from the newest frame, skipping
/// `end_stream` — used to backfill the summary request/response kind.
pub fn first_non_empty_frame_kind(frames: &[FrameView]) -> String {
    frames
        .iter()
        .rev()
        .map(|frame| frame.kind.trim())
        .find(|kind| !kind.is_empty() && *kind != "end_stream")
        .unwrap_or_default()
        .to_owned()
}

/// Builds a synthetic frame for a unary (non-Connect) body so the UI's default
/// frames pane is not empty, mirroring the Go `syntheticUnaryFrame`.
pub fn synthetic_unary_frame(
    path: &str,
    kind: &str,
    request_id: &str,
    decoded_json: &str,
    length: usize,
) -> FrameView {
    let message_type = match path {
        BIDI_APPEND_PATH => "aiserver.v1.BidiAppendRequest+agent.v1.AgentClientMessage",
        NOTIFY_CONVERSATION_CLONE_PATH => "agent.v1.NotifyConversationCloneRequest",
        UPLOAD_CONVERSATION_BLOBS_PATH => "agent.v1.UploadConversationBlobsRequest",
        other if !other.is_empty() => other.trim_start_matches('/'),
        _ => "",
    };
    let kind = if kind.is_empty() {
        "unary_message"
    } else {
        kind
    };
    FrameView {
        index: 0,
        length,
        kind: kind.to_owned(),
        message_type: format!("{message_type}#unary"),
        request_id: request_id.to_owned(),
        json: decoded_json.to_owned(),
        ..Default::default()
    }
}

/// Unwraps a single Connect unary envelope (5-byte header + body), mirroring the
/// Go `maybeUnwrapConnectUnary`. Returns the original payload when it is not
/// exactly one envelope.
pub fn maybe_unwrap_connect_unary(payload: &[u8], codec: &str) -> Vec<u8> {
    if payload.len() < 5 {
        return payload.to_vec();
    }
    let flags = payload[0];
    let length = u32::from_be_bytes([payload[1], payload[2], payload[3], payload[4]]) as usize;
    if length > MAX_CONNECT_FRAME_BYTES || 5 + length != payload.len() {
        return payload.to_vec();
    }
    let body = &payload[5..5 + length];
    if flags & COMPRESSED_FLAG == 0 {
        return body.to_vec();
    }
    let codec = codec.trim();
    let codec = if codec.is_empty() || codec.eq_ignore_ascii_case("identity") {
        "gzip"
    } else {
        codec
    };
    decompress(body, codec).unwrap_or_else(|_| body.to_vec())
}

/// Determines whether a path decodes as a unary request body (known protobuf
/// endpoints), controlling whether decode failures are surfaced or suppressed.
pub fn decodes_unary_request(path: &str) -> bool {
    path == BIDI_APPEND_PATH
}

/// Determines whether a path decodes as a unary response body.
pub fn decodes_unary_response(path: &str) -> bool {
    path == NOTIFY_CONVERSATION_CLONE_PATH || path == UPLOAD_CONVERSATION_BLOBS_PATH
}

/// Decodes a unary request body into `(json, kind, request_id)`.
pub fn decode_unary_request(
    path: &str,
    payload: &[u8],
) -> Result<(String, String, String), String> {
    if path != BIDI_APPEND_PATH {
        return Ok((String::new(), String::new(), String::new()));
    }
    // BidiAppendRequest: { data: string (hex), request_id: BidiRequestId }.
    let request = crate::cursor::protocol::proto::aiserver::v1::BidiAppendRequest::decode(payload)
        .map_err(|error| error.to_string())?;
    let request_id = request
        .request_id
        .map(|id| id.request_id)
        .unwrap_or_default()
        .trim()
        .to_owned();
    let outer = decode_message_json("aiserver.v1.BidiAppendRequest", payload).unwrap_or_default();
    let mut kind = "bidi_append".to_owned();
    let mut inner_json = String::new();
    if !request.data.is_empty() {
        if let Ok(data) = hex::decode(&request.data) {
            if let Ok(json) = decode_message_json(AGENT_CLIENT_MESSAGE_TYPE, &data) {
                kind = message_kind(AGENT_CLIENT_MESSAGE_TYPE, &data).unwrap_or(kind);
                inner_json = json;
            }
        }
    }
    let combined = if inner_json.is_empty() {
        outer
    } else {
        let outer_value: serde_json::Value =
            serde_json::from_str(&outer).unwrap_or(serde_json::Value::Null);
        let inner_value: serde_json::Value =
            serde_json::from_str(&inner_json).unwrap_or(serde_json::Value::Null);
        serde_json::to_string_pretty(&serde_json::json!({
            "bidi_append_request": outer_value,
            "agent_client_kind": kind,
            "agent_client_message": inner_value,
        }))
        .unwrap_or_default()
    };
    Ok((combined, kind, request_id))
}

/// Decodes a unary response body into `(json, kind)`.
pub fn decode_unary_response(path: &str, payload: &[u8]) -> Result<(String, String), String> {
    let (type_name, kind) = match path {
        NOTIFY_CONVERSATION_CLONE_PATH => (
            "agent.v1.NotifyConversationCloneResponse",
            "notify_conversation_clone_response",
        ),
        UPLOAD_CONVERSATION_BLOBS_PATH => (
            "agent.v1.UploadConversationBlobsResponse",
            "upload_conversation_blobs_response",
        ),
        _ => return Ok((String::new(), String::new())),
    };
    Ok((decode_message_json(type_name, payload)?, kind.to_owned()))
}

/// The decoded outcome of one side of an exchange, produced by [`finish_payload`].
pub struct DecodedPayload {
    /// Body after decompression (or the original when no codec applied).
    pub decoded_body: Vec<u8>,
    pub decoded_json: String,
    pub kind: String,
    pub request_id: String,
    pub decode_error: String,
    pub offline_frames: Vec<FrameView>,
}

/// Runs the full decode pipeline for a captured side: decompress, unary protobuf
/// decode, Connect envelope unwrap, offline stream framing, and text fallback.
/// Mirrors the Go `finishRequestBody` / `finishResponseBody`.
pub fn finish_payload(
    path: &str,
    response_side: bool,
    codec: &str,
    content_type: &str,
    captured: &[u8],
    truncated: bool,
    max_frames: usize,
) -> DecodedPayload {
    let decodes_unary = if response_side {
        decodes_unary_response(path)
    } else {
        decodes_unary_request(path)
    };
    let mut decoded_body = captured.to_vec();
    let mut content_decode_err: Option<String> = None;
    if truncated && decodes_unary {
        content_decode_err = Some(if response_side {
            "响应正文超过抓取上限，无法完整解码".into()
        } else {
            "请求正文超过抓取上限，无法完整解码".into()
        });
    } else if !captured.is_empty() {
        match decompress(captured, codec) {
            Ok(decompressed) => decoded_body = decompressed,
            Err(error) if decodes_unary => content_decode_err = Some(error),
            Err(_) => {}
        }
    }
    let (mut decoded_json, mut kind, mut request_id, mut decode_err) = match content_decode_err {
        Some(error) => (String::new(), String::new(), String::new(), Some(error)),
        None => {
            if response_side {
                match decode_unary_response(path, &decoded_body) {
                    Ok((json, kind)) => (json, kind, String::new(), None),
                    Err(error) => (String::new(), String::new(), String::new(), Some(error)),
                }
            } else {
                match decode_unary_request(path, &decoded_body) {
                    Ok((json, kind, id)) => (json, kind, id, None),
                    Err(error) => (String::new(), String::new(), String::new(), Some(error)),
                }
            }
        }
    };
    // Some clients carry a unary protobuf body inside a Connect unary envelope.
    if decodes_unary
        && !decoded_body.is_empty()
        && (decode_err.is_some() || decoded_json.is_empty())
    {
        let unwrapped = maybe_unwrap_connect_unary(&decoded_body, codec);
        if unwrapped.len() > 0 && unwrapped.len() != decoded_body.len() {
            let alt = if response_side {
                decode_unary_response(path, &unwrapped)
                    .map(|(json, kind)| (json, kind, String::new()))
            } else {
                decode_unary_request(path, &unwrapped)
            };
            if let Ok((alt_json, alt_kind, alt_id)) = alt {
                if !alt_json.is_empty() {
                    decoded_json = alt_json;
                    kind = alt_kind;
                    request_id = alt_id;
                    decode_err = None;
                    decoded_body = unwrapped;
                }
            }
        }
    }
    let proto_err = decode_err.clone();
    // Offline Connect framing for buffered RunSSE bodies (upstream whole-body path).
    let mut offline_frames = Vec::new();
    if let Some(message_type) = run_sse_message_type(path, response_side) {
        if !captured.is_empty() {
            offline_frames = decode_frames_offline(message_type, codec, max_frames, captured);
        }
    }
    // Text/JSON fallback only for unary HTTP (never Connect RunSSE streams).
    let is_run_sse = run_sse_message_type(path, response_side).is_some();
    if decoded_json.is_empty() && offline_frames.is_empty() && !is_run_sse {
        if let Some((fb_json, fb_kind)) = fallback_display_body(content_type, &decoded_body) {
            decoded_json = fb_json;
            kind = fb_kind;
            decode_err = None;
        }
    }
    // Soft warning when a protobuf failure was recovered by the text fallback.
    let decode_error = if let Some(error) = decode_err {
        error
    } else if let Some(proto_error) = proto_err {
        format!("protobuf 解码失败，已回退为文本/JSON 展示: {proto_error}")
    } else {
        String::new()
    };
    DecodedPayload {
        decoded_body,
        decoded_json,
        kind,
        request_id,
        decode_error,
        offline_frames,
    }
}
