//! Debug runtime: an in-process capture hub plus an embedded web UI.
//!
//! <p>Ports the legacy Go `cursor-proxy-debugger` viewer, but the MITM hop is
//! not a separate proxy here: inbound Cursor traffic is already decrypted by the
//! main local-app proxy (see [`crate::local_app::proxy`]), which records hop 1
//! into the process-wide [`ExchangeStore`] while the debug runtime is running.
//! This module only owns the store, the REST/SSE endpoints the ported
//! `assets/app.js` frontend consumes, and the web UI listener.</p>
//!
//! <p>Upstream (hop 2) and provider (hop 3) traffic flows in through the global
//! hub from the existing forward/provider paths. All three hops feed the same
//! [`ExchangeStore`].</p>
use std::{convert::Infallible, sync::Arc};

use axum::{
    body::Body,
    extract::{Path, Query, State},
    http::{header, HeaderValue, StatusCode, Uri},
    response::{IntoResponse, Response},
    routing::get,
    Router,
};
use bytes::Bytes;
use include_dir::{include_dir, Dir};
use parking_lot::Mutex;
use serde_json::json;
use tokio::sync::oneshot;

use super::{
    capture,
    store::{project, ExchangeStore},
};

/// Default web UI listen address.
const DEFAULT_UI_ADDR: &str = "127.0.0.1:9091";
const DEFAULT_TARGET_HOST: &str = "*.cursor.sh";
const DEFAULT_MAX_STORE_BYTES: i64 = 200 << 20;
const DEFAULT_MAX_FRAMES: usize = 2000;
/// SSE heartbeat interval, small enough to survive idle-stream timeouts.
const EVENT_HEARTBEAT: std::time::Duration = std::time::Duration::from_secs(15);

static EMBEDDED_WEB: Dir<'_> = include_dir!("$CARGO_MANIFEST_DIR/src/debug/assets");

/// Runtime state surfaced by `/api/status`.
#[derive(Clone, Copy, Debug, serde::Serialize)]
pub enum DebugStatus {
    Stopped,
    Running,
}

/// A clonable handle to the debug runtime.
#[derive(Clone)]
pub struct DebugController {
    inner: Arc<DebugControllerInner>,
}

struct DebugControllerInner {
    store: Arc<ExchangeStore>,
    ui_addr: String,
    target_host: String,
    state: Mutex<DebugStatus>,
    ui_router: Router,
    ui_stop: Mutex<Option<oneshot::Sender<()>>>,
}

impl DebugController {
    /// Installs the process-wide capture store so the main proxy (hop 1) and
    /// the forward/provider paths (hop 2/3) record traffic.
    pub fn install_store(&self) {
        capture::set_store(Some(self.inner.store.clone()));
    }

    pub fn uninstall_store(&self) {
        if let Some(store) = capture::store() {
            if Arc::ptr_eq(&store, &self.inner.store) {
                capture::set_store(None);
            }
        }
    }

    pub fn store(&self) -> Arc<ExchangeStore> {
        self.inner.store.clone()
    }

    pub fn ui_addr(&self) -> String {
        self.inner.ui_addr.clone()
    }

    pub fn target_host(&self) -> String {
        self.inner.target_host.clone()
    }

    pub fn status(&self) -> DebugStatus {
        *self.inner.state.lock()
    }

    /// Installs the store and serves the web UI, if not already running.
    pub async fn start(&self) -> Result<(), String> {
        if matches!(*self.inner.state.lock(), DebugStatus::Running) {
            return Ok(());
        }
        let ui_listener = tokio::net::TcpListener::bind(&self.inner.ui_addr)
            .await
            .map_err(|error| format!("启动调试界面失败（{}）：{error}", self.inner.ui_addr))?;

        let (ui_stop, ui_done) = oneshot::channel();
        let ui_router = self.inner.ui_router.clone();

        self.install_store();
        *self.inner.ui_stop.lock() = Some(ui_stop);
        *self.inner.state.lock() = DebugStatus::Running;

        tokio::spawn(async move {
            let _ = axum::serve(ui_listener, ui_router)
                .with_graceful_shutdown(async move {
                    let _ = ui_done.await;
                })
                .await;
        });
        Ok(())
    }

    /// Stops the UI listener, clears captured memory, and uninstalls the store.
    pub async fn stop(&self) {
        if matches!(*self.inner.state.lock(), DebugStatus::Stopped) {
            return;
        }
        if let Some(stop) = self.inner.ui_stop.lock().take() {
            let _ = stop.send(());
        }
        self.inner.store.clear();
        self.uninstall_store();
        *self.inner.state.lock() = DebugStatus::Stopped;
    }
}

/// Configuration for a new debug runtime, mirroring the Go `Config` fields.
///
/// `proxy_addr` is retained for wire compatibility but unused: hop 1 is captured
/// by the main local-app proxy, not by a dedicated debug proxy.
#[derive(Clone, Debug)]
pub struct DebugServerConfig {
    pub proxy_addr: String,
    pub ui_addr: String,
    pub target_host: String,
    pub upstream_proxy: Option<String>,
    pub max_store_bytes: i64,
    pub max_exchanges: usize,
    pub max_frames: usize,
}

impl Default for DebugServerConfig {
    fn default() -> Self {
        Self {
            proxy_addr: String::new(),
            ui_addr: DEFAULT_UI_ADDR.into(),
            target_host: DEFAULT_TARGET_HOST.into(),
            upstream_proxy: None,
            max_store_bytes: DEFAULT_MAX_STORE_BYTES,
            max_exchanges: 0,
            max_frames: DEFAULT_MAX_FRAMES,
        }
    }
}

/// Builds a [`DebugController`] from a [`DebugServerConfig`].
#[derive(Clone, Debug)]
pub struct DebugServer {
    config: DebugServerConfig,
}

impl DebugServer {
    pub fn new(config: DebugServerConfig) -> Self {
        Self { config }
    }

    /// The main local-app proxy address, resolved lazily at build time by the
    /// caller via [`DebugServerConfig::proxy_addr`]; empty means "unknown".
    pub fn build(self) -> Result<DebugController, String> {
        let ui_addr = normalize_addr(&self.config.ui_addr, DEFAULT_UI_ADDR);
        validate_loopback(&ui_addr)?;

        let max_store_bytes = if self.config.max_store_bytes > 0 {
            self.config.max_store_bytes
        } else {
            DEFAULT_MAX_STORE_BYTES
        };
        let max_frames = if self.config.max_frames > 0 {
            self.config.max_frames
        } else {
            DEFAULT_MAX_FRAMES
        };

        let store = Arc::new(ExchangeStore::new(
            max_store_bytes,
            self.config.max_exchanges,
            max_frames,
        ));
        let ca_cert = read_ca_certificate().unwrap_or_default();
        let ui_router = build_ui_router(
            store.clone(),
            &self.config.proxy_addr,
            &ui_addr,
            &self.config.target_host,
            self.config.upstream_proxy.as_deref(),
            ca_cert,
        );

        Ok(DebugController {
            inner: Arc::new(DebugControllerInner {
                store,
                ui_addr,
                target_host: self.config.target_host.clone(),
                state: Mutex::new(DebugStatus::Stopped),
                ui_router,
                ui_stop: Mutex::new(None),
            }),
        })
    }
}

fn read_ca_certificate() -> crate::Result<String> {
    let path = crate::config::managed_data_dir()?.join("ca").join("ca.crt");
    std::fs::read_to_string(path).map_err(crate::Error::from)
}

/// Shared state for the UI endpoints.
#[derive(Clone)]
struct UiState {
    store: Arc<ExchangeStore>,
    proxy_addr: String,
    ui_addr: String,
    target_host: String,
    upstream_proxy: Option<String>,
    ca_cert: String,
}

fn build_ui_router(
    store: Arc<ExchangeStore>,
    proxy_addr: &str,
    ui_addr: &str,
    target_host: &str,
    upstream_proxy: Option<&str>,
    ca_cert: String,
) -> Router {
    let state = UiState {
        store,
        proxy_addr: proxy_addr.to_owned(),
        ui_addr: ui_addr.to_owned(),
        target_host: target_host.to_owned(),
        upstream_proxy: upstream_proxy.map(str::to_owned),
        ca_cert,
    };
    Router::new()
        .route("/api/status", get(handle_status))
        .route(
            "/api/exchanges",
            get(handle_exchange_list).delete(handle_clear_exchanges),
        )
        .route("/api/exchanges/{id}", get(handle_exchange_detail))
        .route("/api/events", get(handle_events))
        .route("/api/ca.crt", get(handle_ca_certificate))
        .fallback(serve_static)
        .with_state(state)
        .layer(security_headers())
}

async fn handle_status(State(state): State<UiState>) -> impl IntoResponse {
    let stats = state.store.stats();
    json_response(json!({
        "proxyAddr": state.proxy_addr,
        "uiAddr": state.ui_addr,
        "targetHost": state.target_host,
        "targetHostPatterns": parse_target_host_patterns(&state.target_host),
        "upstreamProxy": state.upstream_proxy,
        "running": true,
        "store": stats,
        "maxStoreBytes": stats.max_store_bytes,
        "usedBytes": stats.used_bytes,
        "exchangeCount": stats.count,
    }))
}

async fn handle_exchange_list(
    State(state): State<UiState>,
    Query(params): Query<ExchangeListParams>,
) -> impl IntoResponse {
    let limit = params.limit.unwrap_or(super::store::max_list_summaries());
    json_response(json!(state.store.summaries(limit)))
}

#[derive(serde::Deserialize, Default)]
struct ExchangeListParams {
    #[serde(default)]
    limit: Option<usize>,
}

async fn handle_exchange_detail(
    State(state): State<UiState>,
    Path(id): Path<String>,
    Query(params): Query<ExchangeDetailParams>,
) -> Response {
    let Some(exchange) = state.store.get(id.trim()) else {
        return not_found(json!({"error": "请求记录不存在"}));
    };
    let include = params.include.unwrap_or_else(|| "full".to_owned());
    json_response(json!(project(exchange, &include)))
}

#[derive(serde::Deserialize, Default)]
struct ExchangeDetailParams {
    #[serde(default)]
    include: Option<String>,
}

async fn handle_clear_exchanges(State(state): State<UiState>) -> impl IntoResponse {
    state.store.clear();
    StatusCode::NO_CONTENT
}

async fn handle_ca_certificate(State(state): State<UiState>) -> impl IntoResponse {
    let mut response = Response::new(Body::from(state.ca_cert.into_bytes()));
    *response.status_mut() = StatusCode::OK;
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/x-x509-ca-cert"),
    );
    response.headers_mut().insert(
        header::CONTENT_DISPOSITION,
        HeaderValue::from_static("attachment; filename=\"cursor-local-proxy-ca.crt\""),
    );
    response
}

async fn handle_events(State(state): State<UiState>) -> impl IntoResponse {
    let mut receiver = state.store.subscribe();
    let stream = async_stream::stream! {
        yield Ok::<_, Infallible>(Bytes::from(sse_event("ready", "{}")));
        let mut heartbeat = tokio::time::interval(EVENT_HEARTBEAT);
        loop {
            tokio::select! {
                event = receiver.recv() => {
                    match event {
                        Ok(event) => {
                            let payload = serde_json::to_string(&event).unwrap_or_default();
                            yield Ok::<_, Infallible>(Bytes::from(sse_event("update", &payload)));
                        }
                        Err(tokio::sync::broadcast::error::RecvError::Lagged(_)) => continue,
                        Err(tokio::sync::broadcast::error::RecvError::Closed) => break,
                    }
                }
                _ = heartbeat.tick() => {
                    yield Ok::<_, Infallible>(Bytes::from(": heartbeat\n\n"));
                }
            }
        }
    };
    let mut response = Response::new(Body::from_stream(stream));
    *response.status_mut() = StatusCode::OK;
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("text/event-stream"),
    );
    response
        .headers_mut()
        .insert(header::CACHE_CONTROL, HeaderValue::from_static("no-cache"));
    response
        .headers_mut()
        .insert(header::CONNECTION, HeaderValue::from_static("keep-alive"));
    response
}

fn sse_event(event: &str, data: &str) -> String {
    format!("event: {event}\ndata: {data}\n\n")
}

fn json_response(value: serde_json::Value) -> Response {
    let mut response = Response::new(json_body(value));
    *response.status_mut() = StatusCode::OK;
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/json; charset=utf-8"),
    );
    response
}

fn not_found(value: serde_json::Value) -> Response {
    let mut response = Response::new(json_body(value));
    *response.status_mut() = StatusCode::NOT_FOUND;
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/json; charset=utf-8"),
    );
    response
}

fn json_body(value: serde_json::Value) -> Body {
    Body::from(serde_json::to_vec(&value).unwrap_or_default())
}

fn security_headers() -> tower_http::set_header::SetResponseHeaderLayer<HeaderValue> {
    tower_http::set_header::SetResponseHeaderLayer::overriding(
        header::CONTENT_SECURITY_POLICY,
        HeaderValue::from_static(
            "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'",
        ),
    )
}

/// Serves the embedded web assets directly from the compile-time directory.
async fn serve_static(uri: Uri) -> impl IntoResponse {
    let path = uri.path().trim_start_matches('/');
    let path = if path.is_empty() { "index.html" } else { path };
    let (contents, content_type) = match EMBEDDED_WEB.get_file(path) {
        Some(file) => (file.contents(), content_type_for(path)),
        None => (
            EMBEDDED_WEB
                .get_file("index.html")
                .map(|file| file.contents())
                .unwrap_or_default(),
            "text/html; charset=utf-8",
        ),
    };
    let mut response = Response::new(Body::from(contents.to_vec()));
    *response.status_mut() = StatusCode::OK;
    response
        .headers_mut()
        .insert(header::CONTENT_TYPE, HeaderValue::from_static(content_type));
    response
}

fn content_type_for(path: &str) -> &'static str {
    match path.rsplit('.').next().unwrap_or_default() {
        "html" => "text/html; charset=utf-8",
        "js" => "application/javascript; charset=utf-8",
        "css" => "text/css; charset=utf-8",
        "json" => "application/json; charset=utf-8",
        _ => "application/octet-stream",
    }
}

fn normalize_addr(value: &str, fallback: &str) -> String {
    let value = value.trim();
    if value.is_empty() {
        fallback.to_owned()
    } else {
        value.to_owned()
    }
}

fn validate_loopback(addr: &str) -> Result<(), String> {
    let host = addr.rsplit_once(':').map(|(host, _)| host).unwrap_or(addr);
    if host.eq_ignore_ascii_case("localhost") {
        return Ok(());
    }
    let parsed: std::net::IpAddr = host
        .parse()
        .map_err(|_| format!("调试界面监听地址无效：{addr}"))?;
    if parsed.is_loopback() {
        Ok(())
    } else {
        Err("调试界面只能监听本机回环地址".into())
    }
}

/// Splits a comma-separated target host list into normalized patterns.
fn parse_target_host_patterns(raw: &str) -> Vec<String> {
    let mut out: Vec<String> = raw
        .split(',')
        .map(str::trim)
        .filter(|part| !part.is_empty())
        .map(|part| part.to_ascii_lowercase())
        .collect();
    if out.is_empty() {
        out.push("*.cursor.sh".into());
    }
    out
}
