//! In-memory capture buffer with byte-budget eviction and live SSE fan-out.
//!
//! Ports the legacy Go `exchangeStore`: newest-last ordering, incremental byte
//! accounting, oldest-first eviction under a byte budget, and a broadcast
//! channel that notifies the web UI of created/updated/evicted/cleared events.
use std::{
    collections::HashMap,
    sync::atomic::{AtomicU64, Ordering},
};

use parking_lot::RwLock;
use serde::Serialize;
use tokio::sync::broadcast;

use super::model::{Exchange, ExchangeState, ExchangeSummary, FrameView, Side, StoreStats};

const MAX_LIST_SUMMARIES: usize = 2000;
/// Publish an SSE update at most every N appended frames to avoid a storm.
const FRAME_PUBLISH_INTERVAL: usize = 64;
const DEFAULT_MAX_FRAMES: usize = 2000;

/// Broadcast event describing a mutation the web UI should react to.
#[derive(Clone, Debug, Serialize)]
pub struct StoreEvent {
    #[serde(rename = "type")]
    pub kind: &'static str,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub id: Option<String>,
}

pub struct ExchangeStore {
    inner: RwLock<Inner>,
    events: broadcast::Sender<StoreEvent>,
    max_store_bytes: i64,
    max_exchanges: usize,
    max_frames: usize,
    next_id: AtomicU64,
}

struct Inner {
    order: Vec<String>,
    exchanges: HashMap<String, Exchange>,
    used_bytes: i64,
}

impl ExchangeStore {
    pub fn new(max_store_bytes: i64, max_exchanges: usize, max_frames: usize) -> Self {
        let (events, _) = broadcast::channel(256);
        Self {
            inner: RwLock::new(Inner {
                order: Vec::new(),
                exchanges: HashMap::new(),
                used_bytes: 0,
            }),
            events,
            max_store_bytes: if max_store_bytes <= 0 {
                200 << 20
            } else {
                max_store_bytes
            },
            max_exchanges,
            max_frames: if max_frames == 0 {
                DEFAULT_MAX_FRAMES
            } else {
                max_frames
            },
            next_id: AtomicU64::new(1),
        }
    }

    pub fn next_id(&self) -> String {
        self.next_id.fetch_add(1, Ordering::Relaxed).to_string()
    }

    pub fn next_prefixed_id(&self, prefix: &str) -> String {
        format!("{}{}", prefix, self.next_id())
    }

    pub fn max_frames(&self) -> usize {
        self.max_frames
    }

    /// Returns the stored content type for one side of an exchange.
    pub fn content_type(&self, id: &str, response_side: bool) -> String {
        let inner = self.inner.read();
        let Some(exchange) = inner.exchanges.get(id) else {
            return String::new();
        };
        if response_side {
            exchange.response.content_type.clone()
        } else {
            exchange.request.content_type.clone()
        }
    }

    pub fn subscribe(&self) -> broadcast::Receiver<StoreEvent> {
        self.events.subscribe()
    }

    pub fn create(&self, mut exchange: Exchange) {
        exchange.recompute_stored_bytes();
        let id = exchange.summary.id.clone();
        let evicted = {
            let mut inner = self.inner.write();
            inner.used_bytes += exchange.summary.stored_bytes;
            inner.order.push(id.clone());
            inner.exchanges.insert(id.clone(), exchange);
            self.evict_locked(&mut inner)
        };
        self.publish("created", Some(id));
        self.publish_evicted(evicted);
    }

    /// Applies `apply` to an existing exchange, keeping byte accounting exact.
    pub fn update(&self, id: &str, apply: impl FnOnce(&mut Exchange)) {
        let evicted = {
            let mut inner = self.inner.write();
            let Some(exchange) = inner.exchanges.get_mut(id) else {
                return;
            };
            let old_bytes = exchange.summary.stored_bytes;
            apply(exchange);
            exchange.recompute_stored_bytes();
            let delta = exchange.summary.stored_bytes - old_bytes;
            inner.used_bytes = (inner.used_bytes + delta).max(0);
            self.evict_locked(&mut inner)
        };
        self.publish("updated", Some(id.to_owned()));
        self.publish_evicted(evicted);
    }

    /// Appends one decoded Connect frame with incremental accounting, throttling
    /// SSE updates and refreshing the summary fields the request list shows.
    pub fn append_frame(&self, id: &str, side: Side, mut frame: FrameView, max_frames: usize) {
        let (should_publish, evicted) = {
            let mut inner = self.inner.write();
            let Some(exchange) = inner.exchanges.get_mut(id) else {
                return;
            };
            let frames_len = exchange.payload_mut(side).frames.len();
            if max_frames > 0 && frames_len >= max_frames {
                return;
            }
            // Successful frames keep only decoded JSON; raw hex is retained only to
            // help debug a decode failure.
            if frame.error.is_empty() {
                frame.raw_hex.clear();
            }
            let delta = frame.estimated_bytes();
            let kind = frame.kind.clone();
            let request_id = frame.request_id.clone();
            let error = frame.error.clone();
            let payload = exchange.payload_mut(side);
            payload.frames.push(frame);
            let frames_len = payload.frames.len();
            if !error.is_empty() {
                payload.decode_error = error;
            }
            exchange.summary.stored_bytes += delta;
            match side {
                Side::Response => {
                    exchange.summary.frame_count = frames_len;
                    if !kind.is_empty() && kind != "end_stream" {
                        exchange.summary.response_kind = kind;
                    }
                }
                Side::Request => {
                    if exchange.summary.frame_count == 0 {
                        exchange.summary.frame_count = frames_len;
                    }
                    if !kind.is_empty() {
                        exchange.summary.request_kind = kind;
                    }
                    if !request_id.is_empty() {
                        exchange.summary.request_id = request_id;
                    }
                }
            }
            inner.used_bytes += delta;
            let should_publish = frames_len % FRAME_PUBLISH_INTERVAL == 0;
            (should_publish, self.evict_locked(&mut inner))
        };
        if should_publish {
            self.publish("updated", Some(id.to_owned()));
        }
        self.publish_evicted(evicted);
    }

    pub fn summaries(&self, limit: usize) -> Vec<ExchangeSummary> {
        let inner = self.inner.read();
        let limit = if limit == 0 || limit > MAX_LIST_SUMMARIES {
            MAX_LIST_SUMMARIES
        } else {
            limit
        };
        let mut result = Vec::with_capacity(limit.min(inner.order.len()));
        // Newest first.
        for id in inner.order.iter().rev() {
            if result.len() >= limit {
                break;
            }
            if let Some(exchange) = inner.exchanges.get(id) {
                result.push(exchange.summary.clone());
            }
        }
        result
    }

    /// Applies the decoded outcome of one side of an exchange to the store.
    ///
    /// Writes raw hex, size, decoded JSON, kind, request id, and backfills
    /// offline/synthetic frames when the streaming path did not populate them.
    pub fn finish_side(
        &self,
        id: &str,
        side: Side,
        path: &str,
        decoded: super::decode::DecodedPayload,
        captured: &[u8],
        truncated: bool,
        read_error: Option<String>,
    ) {
        let captured = captured.to_vec();
        let captured_len = captured.len();
        self.update(id, move |exchange| {
            // Snapshot summary fields before taking the payload borrow.
            let current_kind = match side {
                Side::Response => exchange.summary.response_kind.clone(),
                Side::Request => exchange.summary.request_kind.clone(),
            };
            let request_id = exchange.summary.request_id.clone();

            let mut next_kind;
            let next_request_id;
            {
                let payload = exchange.payload_mut(side);
                payload.size = captured_len as i64;
                payload.raw_hex = hex::encode(&captured);
                payload.raw_truncated = truncated;
                if !decoded.decoded_json.is_empty() {
                    payload.decoded_json = decoded.decoded_json;
                }
                payload.decode_error = decoded.decode_error;
                next_kind = if decoded.kind.is_empty() {
                    current_kind.clone()
                } else {
                    decoded.kind
                };
                next_request_id = if decoded.request_id.is_empty() {
                    request_id
                } else {
                    decoded.request_id
                };
                if payload.frames.is_empty() && !decoded.offline_frames.is_empty() {
                    payload.frames = decoded.offline_frames;
                    if current_kind.is_empty() {
                        next_kind = super::decode::first_non_empty_frame_kind(&payload.frames);
                    }
                }
                if payload.frames.is_empty() && !payload.decoded_json.is_empty() {
                    let frame = super::decode::synthetic_unary_frame(
                        path,
                        &next_kind,
                        &next_request_id,
                        &payload.decoded_json,
                        captured_len,
                    );
                    payload.frames = vec![frame];
                }
            }

            let frame_count = match side {
                Side::Response => exchange.response.frames.len(),
                Side::Request => exchange.request.frames.len(),
            };
            if exchange.summary.frame_count == 0 {
                exchange.summary.frame_count = frame_count;
            }
            match side {
                Side::Request => exchange.summary.request_bytes = captured_len as i64,
                Side::Response => exchange.summary.response_bytes = captured_len as i64,
            }
            match side {
                Side::Response => exchange.summary.response_kind = next_kind,
                Side::Request => exchange.summary.request_kind = next_kind,
            }
            exchange.summary.request_id = next_request_id;
            if side == Side::Response {
                exchange.summary.duration_ms = exchange.elapsed_ms();
                exchange.set_state(ExchangeState::Completed);
            }
            if let Some(error) = read_error {
                exchange.summary.error = error;
                exchange.set_state(ExchangeState::Error);
            }
        });
    }

    pub fn get(&self, id: &str) -> Option<Exchange> {
        self.inner.read().exchanges.get(id).cloned()
    }

    pub fn stats(&self) -> StoreStats {
        let inner = self.inner.read();
        StoreStats {
            count: inner.order.len(),
            used_bytes: inner.used_bytes,
            max_store_bytes: self.max_store_bytes,
            max_exchanges: self.max_exchanges,
        }
    }

    pub fn clear(&self) {
        {
            let mut inner = self.inner.write();
            inner.order.clear();
            inner.exchanges.clear();
            inner.used_bytes = 0;
        }
        self.publish("cleared", None);
    }

    /// Drops oldest exchanges until the byte and count budgets are satisfied.
    fn evict_locked(&self, inner: &mut Inner) -> Vec<String> {
        let mut evicted = Vec::new();
        while !inner.order.is_empty() {
            let over_bytes = inner.used_bytes > self.max_store_bytes;
            let over_count = self.max_exchanges > 0 && inner.order.len() > self.max_exchanges;
            if !over_bytes && !over_count {
                break;
            }
            let oldest = inner.order.remove(0);
            if let Some(exchange) = inner.exchanges.remove(&oldest) {
                inner.used_bytes = (inner.used_bytes - exchange.summary.stored_bytes).max(0);
                evicted.push(oldest);
            }
        }
        evicted
    }

    fn publish(&self, kind: &'static str, id: Option<String>) {
        // Ignore send errors: no subscribers simply means the web UI is closed.
        let _ = self.events.send(StoreEvent { kind, id });
    }

    fn publish_evicted(&self, evicted: Vec<String>) {
        for id in evicted {
            self.publish("evicted", Some(id));
        }
    }
}

/// Projects an exchange for the requested detail level (`include` query param).
pub fn project(mut exchange: Exchange, include: &str) -> Exchange {
    match include.trim().to_ascii_lowercase().as_str() {
        "" | "summary" => Exchange {
            request: Default::default(),
            response: Default::default(),
            summary: exchange.summary,
            started: exchange.started,
        },
        "full" | "all" => exchange,
        "decoded" => {
            strip_raw(&mut exchange);
            exchange
        }
        "raw" => {
            exchange.request.decoded_json.clear();
            exchange.response.decoded_json.clear();
            exchange.request.frames.clear();
            exchange.response.frames.clear();
            exchange
        }
        "frames" => {
            exchange.request.raw_hex.clear();
            exchange.response.raw_hex.clear();
            exchange.request.decoded_json.clear();
            exchange.response.decoded_json.clear();
            exchange
        }
        _ => Exchange {
            request: Default::default(),
            response: Default::default(),
            summary: exchange.summary,
            started: exchange.started,
        },
    }
}

fn strip_raw(exchange: &mut Exchange) {
    exchange.request.raw_hex.clear();
    exchange.response.raw_hex.clear();
    for frame in &mut exchange.request.frames {
        frame.raw_hex.clear();
    }
    for frame in &mut exchange.response.frames {
        frame.raw_hex.clear();
    }
}

pub const fn max_list_summaries() -> usize {
    MAX_LIST_SUMMARIES
}
