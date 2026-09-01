//! Configures the local application proxy.
use std::{net::SocketAddr, sync::Arc, time::Instant};

use hudsucker::{
    certificate_authority::RcgenAuthority,
    hyper::{Request, Uri},
    rustls::crypto::aws_lc_rs,
    Body, HttpContext, HttpHandler, Proxy, RequestOrResponse,
};
use http_body_util::BodyExt as _;
use tokio::{net::TcpListener, sync::oneshot, task::JoinHandle};

use parking_lot::{Mutex, RwLock};

use crate::{
    api::cursor::proxy::UPSTREAM_URL_HEADER,
    cursor::services::tab::is_tab_path,
    debug::{
        capture::{self, CaptureStream},
        decode,
        model::{
            CaptureSource, Exchange, ExchangeState, ExchangeSummary, Payload, ServerKind, Side,
        },
    },
    store::TabMode,
    Error, Result,
};

use super::ca::LoadedCa;

#[derive(Default)]
pub struct ProxyRuntime {
    url: Option<String>,
    port: Option<u16>,
    stop: Option<oneshot::Sender<()>>,
    task: Option<JoinHandle<()>>,
}

impl ProxyRuntime {
    pub fn running(&self) -> bool {
        self.task.as_ref().is_some_and(|task| !task.is_finished())
    }
    pub fn url(&self) -> Option<String> {
        self.running().then(|| self.url.clone()).flatten()
    }

    pub async fn start(
        &mut self,
        backend: SocketAddr,
        ca: LoadedCa,
        requested_port: u16,
        tab_mode: Arc<RwLock<TabMode>>,
    ) -> Result<(String, u16)> {
        if let Some(url) = self.url() {
            return Ok((url, self.port.unwrap_or_default()));
        }
        let listener = bind_proxy_listener(requested_port).await?;
        let address = listener.local_addr()?;
        let (stop, done) = oneshot::channel();
        let authority = RcgenAuthority::new(ca.issuer, 1_000, aws_lc_rs::default_provider());
        let proxy = Proxy::builder()
            .with_listener(listener)
            .with_ca(authority)
            .with_rustls_connector(aws_lc_rs::default_provider())
            .with_http_handler(CursorRelay {
                backend,
                tab_mode,
                current: None,
            })
            .with_graceful_shutdown(async move {
                let _ = done.await;
            })
            .build()
            .map_err(|error| Error::Store(format!("build Cursor proxy: {error}")))?;
        self.stop = Some(stop);
        self.url = Some(format!("http://{address}"));
        self.port = Some(address.port());
        self.task = Some(tokio::spawn(async move {
            if let Err(error) = proxy.start().await {
                tracing::error!(%error, "Cursor proxy stopped unexpectedly");
            }
        }));
        Ok((self.url.clone().unwrap(), address.port()))
    }

    pub async fn stop(&mut self) {
        if let Some(stop) = self.stop.take() {
            let _ = stop.send(());
        }
        if let Some(task) = self.task.take() {
            let _ = tokio::time::timeout(std::time::Duration::from_secs(5), task).await;
        }
        self.url = None;
        self.port = None;
    }
}

async fn bind_proxy_listener(requested_port: u16) -> Result<TcpListener> {
    let requested = SocketAddr::from(([127, 0, 0, 1], requested_port));
    match TcpListener::bind(requested).await {
        Ok(listener) => Ok(listener),
        Err(error) if requested_port != 0 => {
            tracing::warn!(%requested, %error, "configured proxy port unavailable; selecting a random port");
            Ok(TcpListener::bind("127.0.0.1:0").await?)
        }
        Err(error) => Err(error.into()),
    }
}

/// Per-request capture context carried between the request and response halves
/// of one proxied exchange.
#[derive(Clone, Default)]
struct CaptureContext {
    id: String,
    path: String,
}

#[derive(Clone)]
struct CursorRelay {
    backend: SocketAddr,
    tab_mode: Arc<RwLock<TabMode>>,
    current: Option<CaptureContext>,
}

impl CursorRelay {
    /// Captures the inbound request as hop 1 when the debug store is active.
    /// Returns the (possibly tee'd) body and records the exchange id.
    fn capture_request(
        &mut self,
        parts: &mut hudsucker::hyper::http::request::Parts,
        body: Body,
        locally_routed: bool,
        cursor_host: bool,
    ) -> Body {
        let Some(store) = capture::store() else {
            return body;
        };
        if !cursor_host {
            return body;
        }

        let id = store.next_prefixed_id("c");
        let path = parts.uri.path().to_owned();
        let method = parts.method.as_str().to_owned();
        let url = parts.uri.to_string();
        let host = parts
            .uri
            .authority()
            .map(|authority| authority.to_string())
            .or_else(|| {
                parts
                    .headers
                    .get("host")
                    .and_then(|value| value.to_str().ok())
                    .map(str::to_owned)
            })
            .unwrap_or_default();
        let codec = decode::request_content_codec(&path, &parts.headers);
        let server = if locally_routed {
            ServerKind::Local
        } else {
            ServerKind::Official
        };
        let started = Instant::now();
        let content_type = header_text(&parts.headers, "content-type");

        let exchange = Exchange {
            summary: ExchangeSummary {
                id: id.clone(),
                method: method.clone(),
                url: url.clone(),
                host: host.clone(),
                path: path.clone(),
                capture_source: CaptureSource::Client.as_str(),
                server: server.as_str(),
                ..ExchangeSummary::new(
                    id.clone(),
                    method,
                    url,
                    host,
                    path.clone(),
                    CaptureSource::Client,
                    server,
                )
            },
            request: Payload {
                headers: capture::sorted_headers(&parts.headers),
                content_type: content_type.clone(),
                content_codec: codec.clone(),
                ..Default::default()
            },
            response: Payload::default(),
            started,
        };
        store.create(exchange);
        self.current = Some(CaptureContext {
            id: id.clone(),
            path: path.clone(),
        });

        let decoder = (path == decode::RUN_SSE_PATH).then(|| {
            Arc::new(Mutex::new(decode::FrameDecoder::new(
                decode::RUN_SSE_REQUEST_TYPE,
                codec.clone(),
                store.max_frames(),
            )))
        });
        let captured = CaptureStream::new(
            body.into_data_stream(),
            {
                let decoder = decoder.clone();
                let store = store.clone();
                let id = id.clone();
                move |chunk: &[u8]| {
                    if let Some(decoder) = &decoder {
                        for frame in decoder.lock().push(chunk) {
                            store.append_frame(&id, Side::Request, frame, store.max_frames());
                        }
                    }
                }
            },
            move |captured: &[u8], _size: usize, truncated: bool, error: Option<String>| {
                let store = store.clone();
                let id = id.clone();
                let path = path.clone();
                let codec = codec.clone();
                let content_type = content_type.clone();
                if let Some(decoder) = decoder.clone() {
                    if let Some(trailer) = decoder.lock().finish() {
                        store.append_frame(&id, Side::Request, trailer, store.max_frames());
                    }
                }
                store.finish_side(
                    &id,
                    Side::Request,
                    &path,
                    decode::finish_payload(
                        &path,
                        false,
                        &codec,
                        &content_type,
                        captured,
                        truncated,
                        store.max_frames(),
                    ),
                    captured,
                    truncated,
                    error,
                );
            },
        );
        Body::from_stream(captured)
    }
}

impl HttpHandler for CursorRelay {
    async fn handle_request(
        &mut self,
        _ctx: &HttpContext,
        request: Request<Body>,
    ) -> RequestOrResponse {
        let (mut parts, body) = request.into_parts();
        let original = parts.uri.clone();
        let host = parts
            .uri
            .host()
            .map(str::to_owned)
            .unwrap_or_default();
        let cursor_host = is_cursor_host(&host);
        let locally_routed = should_route_locally(original.path(), *self.tab_mode.read());

        let body = self.capture_request(&mut parts, body, locally_routed, cursor_host);

        let mut request = Request::from_parts(parts, body);
        if cursor_host && locally_routed {
            if let Ok(value) = original.to_string().parse() {
                request.headers_mut().insert(UPSTREAM_URL_HEADER, value);
            }
            let path = original
                .path_and_query()
                .map(|value| value.as_str())
                .unwrap_or("/");
            if let Ok(uri) = format!("http://{}{}", self.backend, path).parse::<Uri>() {
                *request.uri_mut() = uri;
            }
        }
        request.into()
    }

    async fn handle_response(
        &mut self,
        _ctx: &HttpContext,
        response: hudsucker::hyper::Response<Body>,
    ) -> hudsucker::hyper::Response<Body> {
        let Some(context) = self.current.take() else {
            return response;
        };
        let Some(store) = capture::store() else {
            return response;
        };
        let (parts, body) = response.into_parts();
        let id = context.id.clone();
        let path = context.path.clone();
        let codec = decode::response_content_codec(&path, &parts.headers);
        store.update(&id, |exchange| {
            exchange.summary.status = parts.status.as_u16();
            exchange.set_state(ExchangeState::Streaming);
            exchange.summary.duration_ms = exchange.elapsed_ms();
            exchange.response.headers = capture::sorted_headers(&parts.headers);
            exchange.response.content_type = header_text(&parts.headers, "content-type");
            exchange.response.content_codec = codec.clone();
        });

        let decoder = (path == decode::RUN_SSE_PATH).then(|| {
            Arc::new(Mutex::new(decode::FrameDecoder::new(
                decode::RUN_SSE_RESPONSE_TYPE,
                codec.clone(),
                store.max_frames(),
            )))
        });
        let content_type = header_text(&parts.headers, "content-type");
        let captured = CaptureStream::new(
            body.into_data_stream(),
            {
                let decoder = decoder.clone();
                let store = store.clone();
                let id = id.clone();
                move |chunk: &[u8]| {
                    if let Some(decoder) = &decoder {
                        for frame in decoder.lock().push(chunk) {
                            store.append_frame(&id, Side::Response, frame, store.max_frames());
                        }
                    }
                }
            },
            move |captured: &[u8], _size: usize, truncated: bool, error: Option<String>| {
                let store = store.clone();
                let id = id.clone();
                let path = path.clone();
                let codec = codec.clone();
                let content_type = content_type.clone();
                if let Some(decoder) = decoder.clone() {
                    if let Some(trailer) = decoder.lock().finish() {
                        store.append_frame(&id, Side::Response, trailer, store.max_frames());
                    }
                }
                store.finish_side(
                    &id,
                    Side::Response,
                    &path,
                    decode::finish_payload(
                        &path,
                        true,
                        &codec,
                        &content_type,
                        captured,
                        truncated,
                        store.max_frames(),
                    ),
                    captured,
                    truncated,
                    error,
                );
            },
        );
        hudsucker::hyper::Response::from_parts(parts, Body::from_stream(captured))
    }

    async fn should_intercept_connect(
        &mut self,
        _ctx: &HttpContext,
        request: &Request<Body>,
    ) -> bool {
        request
            .uri()
            .authority()
            .is_some_and(|authority| is_cursor_host(authority.host()))
    }

    async fn should_intercept_tls(
        &mut self,
        _ctx: &HttpContext,
        hello: hudsucker::rustls::server::ClientHello<'_>,
    ) -> bool {
        hello.server_name().is_some_and(is_cursor_host)
    }
}

pub fn is_cursor_host(host: &str) -> bool {
    let host = host.trim_end_matches('.').to_ascii_lowercase();
    matches!(host.as_str(), "api2.cursor.sh" | "api3.cursor.sh") || host.ends_with(".cursor.sh")
}

fn is_local_path(path: &str) -> bool {
    matches!(
        path,
        "/agent.v1.AgentService/RunSSE"
            | "/aiserver.v1.BidiService/BidiAppend"
            | "/aiserver.v1.AiService/AvailableModels"
            | "/agent.v1.AgentService/GetUsableModels"
            | "/aiserver.v1.AiService/GetUsableModels"
            | "/aiserver.v1.AuthService/GetEmail"
            | "/aiserver.v1.DashboardService/GetMe"
            | "/aiserver.v1.DashboardService/GetTeams"
            | "/aiserver.v1.DashboardService/GetUserProfile"
            | "/aiserver.v1.DashboardService/GetCurrentPeriodUsage"
            | "/aiserver.v1.DashboardService/GetUsageLimitStatusAndActiveGrants"
            | "/aiserver.v1.AiService/KnowledgeBaseAdd"
            | "/aiserver.v1.AiService/KnowledgeBaseList"
            | "/aiserver.v1.AiService/KnowledgeBaseUpdate"
            | "/aiserver.v1.AiService/KnowledgeBaseRemove"
            | "/aiserver.v1.AnalyticsService/BootstrapStatsig"
            | "/auth/full_stripe_profile"
    )
}

fn should_route_locally(path: &str, tab_mode: TabMode) -> bool {
    is_local_path(path) || (is_tab_path(path) && tab_mode != TabMode::Direct)
}

fn header_text(headers: &hudsucker::hyper::HeaderMap, name: &str) -> String {
    headers
        .get(name)
        .and_then(|value| value.to_str().ok())
        .map(str::trim)
        .unwrap_or_default()
        .to_owned()
}
