//! Global capture hub and streaming body tee.
//!
//! The debug server installs one [`ExchangeStore`] into a process-wide slot.
//! The Cursor upstream forwarder (hop 2) and the provider adapters (hop 3)
//! emit [`UpstreamHop`] / [`ProviderHop`] through it; the local MITM proxy
//! (hop 1) uses [`ClientExchange`] to stream-capture its request and response
//! bodies. When no store is installed every path is a cheap no-op.
use std::{
    pin::Pin,
    sync::Arc,
    task::{Context, Poll},
    time::Instant,
};

use futures_util::Stream;
use parking_lot::RwLock;

use super::{
    decode,
    model::{Exchange, ExchangeSummary, Header, Payload, Side},
    store::ExchangeStore,
};

static STORE: RwLock<Option<Arc<ExchangeStore>>> = RwLock::new(None);

/// Installs (or, with `None`, clears) the process-wide capture store.
pub fn set_store(store: Option<Arc<ExchangeStore>>) {
    *STORE.write() = store;
}

/// Returns the active capture store, if the debugger is running.
pub fn store() -> Option<Arc<ExchangeStore>> {
    STORE.read().clone()
}

/// Cap on captured body bytes per side, matching the Go `MaxCaptureBytes`.
pub const MAX_CAPTURE_BYTES: usize = 16 << 20;

/// Sorted, masked header pairs for one hop.
pub fn sorted_headers(headers: &http::HeaderMap) -> Vec<Header> {
    let mut entries: Vec<(String, String)> = headers
        .iter()
        .map(|(name, value)| {
            let value = value.to_str().unwrap_or_default().to_owned();
            let value = if crate::model::is_sensitive_header(name.as_str()) && !value.is_empty() {
                "[已隐藏]".to_owned()
            } else {
                value
            };
            (name.to_string(), value)
        })
        .collect();
    entries.sort_by(|left, right| left.0.cmp(&right.0));
    entries
        .into_iter()
        .map(|(name, value)| Header { name, value })
        .collect()
}

/// One outbound BYOK→official-Cursor hop (the Go `UpstreamHop`).
#[derive(Debug)]
pub struct UpstreamHop {
    pub method: String,
    pub url: String,
    pub host: String,
    pub path: String,
    pub status: u16,
    pub request_id: String,
    pub request_header: http::HeaderMap,
    pub response_header: http::HeaderMap,
    pub request_body: Vec<u8>,
    pub response_body: Vec<u8>,
    pub error: String,
    pub started: Instant,
}

impl UpstreamHop {
    pub fn emit(self) {
        let Some(store) = store() else { return };
        let method = if self.method.is_empty() {
            "POST".to_owned()
        } else {
            self.method
        };
        let id = store.next_prefixed_id("u");
        let path = self.path.clone();
        let host = self.host.clone();
        let request_id = if self.request_id.is_empty() {
            header_text(&self.request_header, "x-request-id")
        } else {
            self.request_id
        };
        let req_codec = decode::request_content_codec(&path, &self.request_header);
        let resp_codec = decode::response_content_codec(&path, &self.response_header);
        let duration_ms = self.started.elapsed().as_millis() as i64;
        let error = self.error.clone();

        let exchange = Exchange {
            summary: ExchangeSummary {
                id: id.clone(),
                method: method.clone(),
                url: self.url.clone(),
                host: host.clone(),
                path: path.clone(),
                status: self.status,
                state: if error.is_empty() {
                    "completed"
                } else {
                    "error"
                },
                duration_ms,
                request_bytes: self.request_body.len() as i64,
                response_bytes: self.response_body.len() as i64,
                request_id,
                capture_source: super::CaptureSource::Upstream.as_str(),
                server: super::ServerKind::Official.as_str(),
                error,
                ..ExchangeSummary::new(
                    String::new(),
                    String::new(),
                    String::new(),
                    String::new(),
                    String::new(),
                    super::CaptureSource::Upstream,
                    super::ServerKind::Official,
                )
            },
            request: Payload {
                headers: sorted_headers(&self.request_header),
                content_type: header_text(&self.request_header, "content-type"),
                content_codec: req_codec.clone(),
                ..Default::default()
            },
            response: Payload {
                headers: sorted_headers(&self.response_header),
                content_type: header_text(&self.response_header, "content-type"),
                content_codec: resp_codec.clone(),
                ..Default::default()
            },
            started: self.started,
        };
        store.create(exchange);
        store.finish_side(
            &id,
            Side::Request,
            &path,
            decode::finish_payload(
                &path,
                false,
                &req_codec,
                &header_text(&self.request_header, "content-type"),
                &self.request_body,
                self.request_body.len() >= MAX_CAPTURE_BYTES,
                store.max_frames(),
            ),
            &self.request_body,
            self.request_body.len() >= MAX_CAPTURE_BYTES,
            None,
        );
        store.finish_side(
            &id,
            Side::Response,
            &path,
            decode::finish_payload(
                &path,
                true,
                &resp_codec,
                &header_text(&self.response_header, "content-type"),
                &self.response_body,
                self.response_body.len() >= MAX_CAPTURE_BYTES,
                store.max_frames(),
            ),
            &self.response_body,
            self.response_body.len() >= MAX_CAPTURE_BYTES,
            None,
        );
    }
}

/// One outbound localserver→model-provider hop (the Go `ProviderHop`).
#[derive(Debug)]
pub struct ProviderHop {
    pub method: String,
    pub url: String,
    pub host: String,
    pub path: String,
    pub status: u16,
    pub request_id: String,
    pub model_call_id: String,
    pub provider: String,
    pub request_header: http::HeaderMap,
    pub response_header: http::HeaderMap,
    pub request_body: Vec<u8>,
    pub response_body: Vec<u8>,
    pub error: String,
    pub started: Instant,
}

impl ProviderHop {
    pub fn emit(self) {
        let Some(store) = store() else { return };
        let method = if self.method.is_empty() {
            "POST".to_owned()
        } else {
            self.method
        };
        let id = store.next_prefixed_id("p");
        let path = self.path.clone();
        let request_id = if self.request_id.is_empty() {
            header_text(&self.request_header, "x-request-id")
        } else {
            self.request_id
        };
        let duration_ms = self.started.elapsed().as_millis() as i64;
        let error = self.error.clone();

        let exchange = Exchange {
            summary: ExchangeSummary {
                id: id.clone(),
                method: method.clone(),
                url: self.url.clone(),
                host: self.host.clone(),
                path: path.clone(),
                status: self.status,
                state: if error.is_empty() {
                    "completed"
                } else {
                    "error"
                },
                duration_ms,
                request_bytes: self.request_body.len() as i64,
                response_bytes: self.response_body.len() as i64,
                request_id,
                model_call_id: self.model_call_id,
                request_kind: "provider_request".to_owned(),
                response_kind: "provider_stream".to_owned(),
                capture_source: super::CaptureSource::Provider.as_str(),
                server: super::ServerKind::Provider.as_str(),
                error,
                ..ExchangeSummary::new(
                    String::new(),
                    String::new(),
                    String::new(),
                    String::new(),
                    String::new(),
                    super::CaptureSource::Provider,
                    super::ServerKind::Provider,
                )
            },
            request: Payload {
                headers: sorted_headers(&self.request_header),
                content_type: header_text(&self.request_header, "content-type"),
                ..Default::default()
            },
            response: Payload {
                headers: sorted_headers(&self.response_header),
                content_type: header_text(&self.response_header, "content-type"),
                ..Default::default()
            },
            started: self.started,
        };
        store.create(exchange);
        // Provider bodies are JSON text; decode through the text fallback path.
        store.finish_side(
            &id,
            Side::Request,
            &path,
            decode::finish_payload(
                &path,
                false,
                "",
                &header_text(&self.request_header, "content-type"),
                &self.request_body,
                false,
                store.max_frames(),
            ),
            &self.request_body,
            false,
            None,
        );
        store.finish_side(
            &id,
            Side::Response,
            &path,
            decode::finish_payload(
                &path,
                true,
                "",
                &header_text(&self.response_header, "content-type"),
                &self.response_body,
                false,
                store.max_frames(),
            ),
            &self.response_body,
            false,
            None,
        );
    }
}

fn header_text(headers: &http::HeaderMap, name: &str) -> String {
    headers
        .get(name)
        .and_then(|value| value.to_str().ok())
        .map(str::trim)
        .unwrap_or_default()
        .to_owned()
}

/// A bytes stream that tees its data into a capture buffer, reporting the
/// total byte count, truncation, and terminal error exactly once when done.
///
/// <p>Callbacks are stored behind `Arc` so the whole stream stays `Send + Sync`,
/// which [`hudsucker::Body::from_stream`] requires for a proxied body. Each
/// `on_chunk` is invoked once per incoming chunk (used by the MITM hop to
/// stream-decode Connect frames), and `on_done` fires exactly once on EOF or
/// error with the captured bytes, total byte count, truncation flag, and an
/// optional terminal read error.</p>
pub struct CaptureStream<S> {
    inner: S,
    captured: Vec<u8>,
    total: usize,
    truncated: bool,
    done: bool,
    on_chunk: Option<Arc<dyn Fn(&[u8]) + Send + Sync>>,
    on_done: Option<Arc<dyn Fn(&[u8], usize, bool, Option<String>) + Send + Sync>>,
}

impl<S> CaptureStream<S> {
    pub fn new<C, D>(inner: S, on_chunk: C, on_done: D) -> Self
    where
        C: Fn(&[u8]) + Send + Sync + 'static,
        D: Fn(&[u8], usize, bool, Option<String>) + Send + Sync + 'static,
    {
        Self {
            inner,
            captured: Vec::new(),
            total: 0,
            truncated: false,
            done: false,
            on_chunk: Some(Arc::new(on_chunk)),
            on_done: Some(Arc::new(on_done)),
        }
    }
}

impl<S> Stream for CaptureStream<S>
where
    S: Stream<Item = std::result::Result<bytes::Bytes, hudsucker::Error>> + Unpin,
{
    type Item = std::result::Result<bytes::Bytes, hudsucker::Error>;

    fn poll_next(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        match Pin::new(&mut self.inner).poll_next(cx) {
            Poll::Ready(Some(Ok(chunk))) => {
                self.total += chunk.len();
                let remaining = MAX_CAPTURE_BYTES.saturating_sub(self.captured.len());
                if remaining > 0 {
                    let take = remaining.min(chunk.len());
                    self.captured.extend_from_slice(&chunk[..take]);
                }
                if self.captured.len() >= MAX_CAPTURE_BYTES && self.total > self.captured.len() {
                    self.truncated = true;
                }
                if let Some(on_chunk) = &self.on_chunk {
                    on_chunk(&chunk);
                }
                Poll::Ready(Some(Ok(chunk)))
            }
            Poll::Ready(Some(Err(error))) => {
                self.finish(Some(error.to_string()));
                Poll::Ready(Some(Err(error)))
            }
            Poll::Ready(None) => {
                self.finish(None);
                Poll::Ready(None)
            }
            Poll::Pending => Poll::Pending,
        }
    }
}

impl<S> CaptureStream<S> {
    fn finish(&mut self, error: Option<String>) {
        if self.done {
            return;
        }
        self.done = true;
        if let Some(on_done) = self.on_done.take() {
            on_done(&self.captured, self.total, self.truncated, error);
        }
    }
}

/// A generic bytes-stream tee that buffers captured bytes (up to
/// [`MAX_CAPTURE_BYTES`]) while forwarding every chunk untouched, then invokes
/// `on_done` exactly once with the captured buffer, total byte count,
/// truncation flag, and an optional terminal error.
///
/// <p>Used by the hop 2 (upstream forward) and hop 3 (provider) paths whose
/// streams carry `axum::Error` / `reqwest::Error` rather than
/// `hudsucker::Error`. Forwarding is never clamped: only the capture buffer is,
/// matching the legacy Go fix that stopped debug capture from truncating the
/// live stream.</p>
pub struct BodyTee<S, E> {
    inner: S,
    captured: Vec<u8>,
    total: usize,
    truncated: bool,
    done: bool,
    on_done: Option<Arc<dyn Fn(Vec<u8>, usize, bool, Option<String>) + Send + Sync>>,
    marker: std::marker::PhantomData<fn() -> E>,
}

// `S` is `Unpin` (bounded on the `Stream` impl) and the rest of the fields are
// `Unpin`, so the whole type is `Unpin` regardless of `E`.
impl<S, E> Unpin for BodyTee<S, E> {}

impl<S, E> BodyTee<S, E> {
    pub fn new<D>(inner: S, on_done: D) -> Self
    where
        D: Fn(Vec<u8>, usize, bool, Option<String>) + Send + Sync + 'static,
    {
        Self {
            inner,
            captured: Vec::new(),
            total: 0,
            truncated: false,
            done: false,
            on_done: Some(Arc::new(on_done)),
            marker: std::marker::PhantomData,
        }
    }
}
impl<S, E> Stream for BodyTee<S, E>
where
    S: Stream<Item = std::result::Result<bytes::Bytes, E>> + Unpin,
    E: std::fmt::Display,
{
    type Item = std::result::Result<bytes::Bytes, E>;

    fn poll_next(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        match Pin::new(&mut self.inner).poll_next(cx) {
            Poll::Ready(Some(Ok(chunk))) => {
                self.total += chunk.len();
                let remaining = MAX_CAPTURE_BYTES.saturating_sub(self.captured.len());
                if remaining > 0 {
                    let take = remaining.min(chunk.len());
                    self.captured.extend_from_slice(&chunk[..take]);
                }
                if self.captured.len() >= MAX_CAPTURE_BYTES && self.total > self.captured.len() {
                    self.truncated = true;
                }
                Poll::Ready(Some(Ok(chunk)))
            }
            Poll::Ready(Some(Err(error))) => {
                self.finish(Some(error.to_string()));
                Poll::Ready(Some(Err(error)))
            }
            Poll::Ready(None) => {
                self.finish(None);
                Poll::Ready(None)
            }
            Poll::Pending => Poll::Pending,
        }
    }
}

impl<S, E> BodyTee<S, E> {
    fn finish(&mut self, error: Option<String>) {
        if self.done {
            return;
        }
        self.done = true;
        if let Some(on_done) = self.on_done.take() {
            on_done(
                std::mem::take(&mut self.captured),
                self.total,
                self.truncated,
                error,
            );
        }
    }
}

/// Splits a provider request URL into `(host, path)`.
pub fn split_url(url: &str) -> (String, String) {
    match url::Url::parse(url) {
        Ok(parsed) => (
            parsed.host_str().map(str::to_owned).unwrap_or_default(),
            parsed.path().to_owned(),
        ),
        Err(_) => (String::new(), String::new()),
    }
}

/// Converts a reqwest response header map into an `http::HeaderMap`.
pub fn response_headers_to_header_map(headers: &reqwest::header::HeaderMap) -> http::HeaderMap {
    let mut map = http::HeaderMap::new();
    for (name, value) in headers {
        if let Ok(value) = value.to_str() {
            if let Ok(value) = http::header::HeaderValue::from_str(value) {
                map.insert(name.clone(), value);
            }
        }
    }
    map
}
