//! Bridges the modern Cursor CLI bidirectional `AgentService/Run` transport.

use std::io::Read;

use axum::{
    body::{Body, Bytes},
    extract::{Extension, State},
    http::{Request, Response},
};
use bytes::{Buf, BytesMut};
use prost::Message;
use tokio_stream::StreamExt;

use crate::{
    api::cursor::{
        bidi::{self, DecodedAppend},
        proxy::{self, CursorProxy},
        run_sse,
    },
    cursor::{
        protocol::{connect, proto::agent::v1 as agent},
        transport::{TransportParent, TransportRegistry},
    },
    Error, Result,
};

pub async fn run(
    State(registry): State<TransportRegistry>,
    Extension(proxy): Extension<CursorProxy>,
    request: Request<Body>,
) -> Result<Response<Body>> {
    let (parts, body) = request.into_parts();
    let parent = parent_headers(&parts.headers)?;
    let mut incoming = body.into_data_stream();
    let mut consumed = BytesMut::new();
    let mut decoder = FrameDecoder::default();
    let first = loop {
        let Some(chunk) = incoming.next().await else {
            return Err(Error::Protocol("Cursor Agent Run stream ended before its first message".into()));
        };
        let chunk = chunk.map_err(|error| Error::Protocol(format!("cannot read Cursor Agent Run stream: {error}")))?;
        consumed.extend_from_slice(&chunk);
        decoder.push(chunk);
        if let Some(payload) = decoder.next()? {
            break payload;
        }
    };
    let first_message = agent::AgentClientMessage::decode(first.as_ref())?;
    let local = is_local(&registry, &first_message).await?;
    tracing::info!(
        local,
        content_type = ?parts.headers.get(axum::http::header::CONTENT_TYPE),
        connect_protocol = ?parts.headers.get("connect-protocol-version"),
        model_id = direct_model_id(&first_message).unwrap_or(""),
        "received Cursor Agent Run stream"
    );
    if !local {
        return proxy::forward(
            Extension(proxy),
            Request::from_parts(parts, replay_body(consumed.freeze(), incoming)),
        )
        .await;
    }

    let request_id = format!("cli-{}", uuid::Uuid::new_v4());
    append(&registry, &request_id, 0, first_message, parent).await?;
    let initial = decoder.take_buffered();
    let receiver_registry = registry.clone();
    let receiver_request_id = request_id.clone();
    tokio::spawn(async move {
        if let Err(error) = receive_messages(
            receiver_registry.clone(),
            receiver_request_id.clone(),
            1,
            initial,
            incoming,
        )
        .await
        {
            tracing::warn!(request_id = receiver_request_id, %error, "Cursor Agent Run client stream failed");
            if let Some(handle) = receiver_registry.local(&receiver_request_id).await {
                handle.disconnect().await;
            }
        }
    });
    run_sse::stream_connect(&registry, &request_id).await
}

fn direct_model_id(message: &agent::AgentClientMessage) -> Option<&str> {
    let agent::agent_client_message::Message::RunRequest(request) = message.message.as_ref()? else {
        return None;
    };
    request
        .requested_model
        .as_ref()
        .map(|model| model.model_id.as_str())
        .filter(|model| !model.is_empty())
        .or_else(|| request.model_details.as_ref().map(|model| model.model_id.as_str()))
        .filter(|model| !model.is_empty())
}

async fn is_local(registry: &TransportRegistry, message: &agent::AgentClientMessage) -> Result<bool> {
    let Some(model_id) = direct_model_id(message) else {
        return Ok(false);
    };
    Ok(model_id.starts_with(crate::plugin::ADAPTER_ID_PREFIX)
        || registry.store().model(model_id).await?.is_some())
}

async fn append(
    registry: &TransportRegistry,
    request_id: &str,
    seqno: i64,
    message: agent::AgentClientMessage,
    parent: Option<TransportParent>,
) -> Result<()> {
    bidi::append(
        registry,
        DecodedAppend { request_id: request_id.into(), seqno, message },
        parent,
    )
    .await?;
    Ok(())
}

async fn receive_messages<S>(
    registry: TransportRegistry,
    request_id: String,
    mut seqno: i64,
    initial: Bytes,
    mut incoming: S,
) -> Result<()>
where
    S: tokio_stream::Stream<Item = std::result::Result<Bytes, axum::Error>> + Unpin,
{
    let mut decoder = FrameDecoder::default();
    decoder.push(initial);
    loop {
        while let Some(payload) = decoder.next()? {
            let message = agent::AgentClientMessage::decode(payload.as_ref())?;
            append(&registry, &request_id, seqno, message, None).await?;
            seqno += 1;
        }
        let Some(chunk) = incoming.next().await else {
            return decoder.finish();
        };
        decoder.push(chunk.map_err(|error| Error::Protocol(format!("cannot read Cursor Agent Run stream: {error}")))?);
    }
}

fn replay_body<S>(initial: Bytes, mut incoming: S) -> Body
where
    S: tokio_stream::Stream<Item = std::result::Result<Bytes, axum::Error>> + Send + Unpin + 'static,
{
    Body::from_stream(async_stream::stream! {
        yield Ok::<Bytes, axum::Error>(initial);
        while let Some(chunk) = incoming.next().await {
            yield chunk;
        }
    })
}

#[derive(Default)]
struct FrameDecoder {
    buffered: BytesMut,
}

impl FrameDecoder {
    fn push(&mut self, bytes: Bytes) {
        self.buffered.extend_from_slice(&bytes);
    }

    fn next(&mut self) -> Result<Option<Bytes>> {
        if self.buffered.len() < 5 {
            return Ok(None);
        }
        let flags = self.buffered[0];
        let length = u32::from_be_bytes(self.buffered[1..5].try_into().expect("frame prefix")) as usize;
        if self.buffered.len() < 5 + length {
            return Ok(None);
        }
        self.buffered.advance(5);
        let payload = self.buffered.split_to(length).freeze();
        if flags & connect::END_STREAM_FLAG != 0 {
            return Ok(None);
        }
        if flags & !0x01 != 0 {
            return Err(Error::Protocol(format!("unsupported Cursor Agent Run frame flags: {flags}")));
        }
        if flags & 0x01 == 0 {
            return Ok(Some(payload));
        }
        let mut output = Vec::new();
        flate2::read::GzDecoder::new(payload.as_ref())
            .read_to_end(&mut output)
            .map_err(|error| Error::Protocol(format!("cannot decompress Cursor Agent Run frame: {error}")))?;
        Ok(Some(Bytes::from(output)))
    }

    fn take_buffered(self) -> Bytes {
        self.buffered.freeze()
    }

    fn finish(&self) -> Result<()> {
        if self.buffered.is_empty() {
            Ok(())
        } else {
            Err(Error::Protocol("truncated Cursor Agent Run Connect frame".into()))
        }
    }
}

fn parent_headers(headers: &axum::http::HeaderMap) -> Result<Option<TransportParent>> {
    let request_id = header_text(headers, "x-parent-request-id")?;
    let tool_call_id = header_text(headers, "x-parent-agent-tool-call-id")?;
    match (request_id, tool_call_id) {
        (None, None) => Ok(None),
        (Some(request_id), Some(tool_call_id)) => Ok(Some(TransportParent {
            request_id: request_id.into(),
            tool_call_id: tool_call_id.into(),
        })),
        _ => Err(Error::Protocol("Cursor subagent request must include both parent headers".into())),
    }
}

fn header_text<'a>(headers: &'a axum::http::HeaderMap, name: &str) -> Result<Option<&'a str>> {
    headers
        .get(name)
        .map(|value| value.to_str())
        .transpose()
        .map_err(|error| Error::Protocol(format!("invalid {name} header: {error}")))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn frame_decoder_accepts_a_connect_message_split_across_chunks() {
        let message = agent::AgentClientMessage::default();
        let frame = connect::encode_message(&message).expect("encode test message");
        let mut decoder = FrameDecoder::default();
        decoder.push(frame.slice(..3));
        assert!(decoder.next().expect("partial frame").is_none());
        decoder.push(frame.slice(3..));
        let payload = decoder.next().expect("complete frame").expect("message payload");
        assert_eq!(payload, Bytes::new());
        decoder.finish().expect("fully consumed frame");
    }
}
