//! Selects local handling or the configured official Cursor upstream.
use std::time::Instant;

use axum::{
    body::{to_bytes, Body, Bytes},
    extract::Extension,
    http::{header, Request, Response},
};

use crate::Result;

const CURSOR_UPSTREAM: &str = "https://api2.cursor.sh";
pub const UPSTREAM_URL_HEADER: &str = "x-server-upstream-url";

#[derive(Clone)]
pub struct CursorProxy {
    client: Option<reqwest::Client>,
    store: Option<crate::store::Store>,
    upstream: String,
}

pub struct BufferedResponse {
    pub status: axum::http::StatusCode,
    pub headers: axum::http::HeaderMap,
    pub body: Bytes,
}

impl BufferedResponse {
    pub fn into_response(self) -> Response<Body> {
        let body = self.body.clone();
        self.with_body(body)
    }

    pub fn with_body(mut self, body: Bytes) -> Response<Body> {
        self.headers.insert(
            header::CONTENT_LENGTH,
            body.len()
                .to_string()
                .parse()
                .expect("body length is always a valid header value"),
        );
        let mut response = Response::new(Body::from(body));
        *response.status_mut() = self.status;
        *response.headers_mut() = self.headers;
        response
    }
}

impl CursorProxy {
    pub fn cursor(store: crate::store::Store) -> Result<Self> {
        Ok(Self {
            client: None,
            store: Some(store),
            upstream: CURSOR_UPSTREAM.into(),
        })
    }

    async fn client(&self) -> Result<reqwest::Client> {
        match (&self.client, &self.store) {
            (Some(client), _) => Ok(client.clone()),
            (_, Some(store)) => Ok(crate::network::client_builder(store)
                .await?
                .redirect(reqwest::redirect::Policy::none())
                .build()?),
            _ => unreachable!("Cursor proxy always has a client or store"),
        }
    }
}

pub async fn forward(
    Extension(proxy): Extension<CursorProxy>,
    request: Request<Body>,
) -> Result<Response<Body>> {
    forward_request(&proxy, request, None).await
}

pub(crate) async fn forward_to_service(
    proxy: &CursorProxy,
    request: Request<Body>,
    service_url: &str,
) -> Result<Response<Body>> {
    forward_request(proxy, request, Some(service_url)).await
}

async fn forward_request(
    proxy: &CursorProxy,
    request: Request<Body>,
    service_url: Option<&str>,
) -> Result<Response<Body>> {
    let started = Instant::now();
    let (parts, body) = request.into_parts();
    let path = parts
        .uri
        .path_and_query()
        .map_or("/", |value| value.as_str())
        .to_owned();
    let url = match service_url {
        Some(service_url) => format!("{}{}", service_url.trim_end_matches('/'), path),
        None => upstream_url(&parts.headers, &proxy.upstream, &path)?,
    };
    let method = parts.method.clone();
    let mut headers = parts.headers;
    headers.remove(UPSTREAM_URL_HEADER);
    headers.remove(header::HOST);
    remove_hop_by_hop_headers(&mut headers);

    let capture_store = crate::debug::capture::store();
    let request_header = headers.clone();
    let host = host_of(&url);

    // When debug capture is active, buffer the request body so the hop 2 record
    // carries the full request bytes (matching the legacy Go whole-body
    // capture); otherwise keep the streaming path untouched.
    let (request_body_captured, request_body) = match &capture_store {
        Some(_) => {
            let bytes = to_bytes(body, usize::MAX).await.map_err(|error| {
                crate::Error::Protocol(format!("cannot read request body: {error}"))
            })?;
            let captured = truncate_capture(&bytes);
            (Some(captured), reqwest::Body::from(bytes))
        }
        None => (None, reqwest::Body::wrap_stream(body.into_data_stream())),
    };

    let client = proxy.client().await?;
    let upstream = client
        .request(method.clone(), url.clone())
        .headers(headers)
        .body(request_body)
        .send()
        .await;

    let upstream = match upstream {
        Ok(response) => response,
        Err(error) => {
            tracing::error!(
                method = %method,
                path,
                elapsed_ms = started.elapsed().as_millis(),
                %error,
                "Cursor upstream request failed"
            );
            if capture_store.is_some() {
                crate::debug::UpstreamHop {
                    method: method.as_str().to_owned(),
                    url: url.clone(),
                    host: host.clone(),
                    path: path.clone(),
                    status: 0,
                    request_id: String::new(),
                    request_header: request_header.clone(),
                    response_header: axum::http::HeaderMap::new(),
                    request_body: request_body_captured.clone().unwrap_or_default(),
                    response_body: Vec::new(),
                    error: error.to_string(),
                    started,
                }
                .emit();
            }
            return Err(error.into());
        }
    };

    let status = upstream.status();
    let mut response_headers = upstream.headers().clone();
    remove_hop_by_hop_headers(&mut response_headers);

    let response_body = if capture_store.is_some() {
        let response_header = response_headers.clone();
        let hop = HopContext {
            method: method.as_str().to_owned(),
            url,
            host,
            path: path.clone(),
            status: status.as_u16(),
            request_header,
            response_header,
            request_body: request_body_captured.unwrap_or_default(),
            started,
        };
        let tee = crate::debug::capture::BodyTee::<_, reqwest::Error>::new(
            upstream.bytes_stream(),
            move |captured, _size, _truncated, error| {
                crate::debug::UpstreamHop {
                    method: hop.method.clone(),
                    url: hop.url.clone(),
                    host: hop.host.clone(),
                    path: hop.path.clone(),
                    status: hop.status,
                    request_id: String::new(),
                    request_header: hop.request_header.clone(),
                    response_header: hop.response_header.clone(),
                    request_body: hop.request_body.clone(),
                    response_body: captured,
                    error: error.unwrap_or_default(),
                    started: hop.started,
                }
                .emit();
            },
        );
        Body::from_stream(tee)
    } else {
        Body::from_stream(upstream.bytes_stream())
    };

    let mut response = Response::new(response_body);
    *response.status_mut() = status;
    *response.headers_mut() = response_headers;

    tracing::info!(
        method = %method,
        path = %path,
        %status,
        elapsed_ms = started.elapsed().as_millis(),
        "forwarded Cursor backend request"
    );
    Ok(response)
}

/// Metadata captured once and moved into the response tee callback.
struct HopContext {
    method: String,
    url: String,
    host: String,
    path: String,
    status: u16,
    request_header: axum::http::HeaderMap,
    response_header: axum::http::HeaderMap,
    request_body: Vec<u8>,
    started: Instant,
}

fn host_of(url: &str) -> String {
    url::Url::parse(url)
        .ok()
        .and_then(|parsed| parsed.host_str().map(str::to_owned))
        .unwrap_or_default()
}

fn truncate_capture(bytes: &[u8]) -> Vec<u8> {
    let limit = crate::debug::capture::MAX_CAPTURE_BYTES;
    if bytes.len() <= limit {
        bytes.to_vec()
    } else {
        bytes[..limit].to_vec()
    }
}

pub async fn forward_buffered(
    proxy: &CursorProxy,
    request: Request<Body>,
) -> Result<BufferedResponse> {
    let (parts, body) = request.into_parts();
    let path = parts
        .uri
        .path_and_query()
        .map_or("/", |value| value.as_str());
    let url = upstream_url(&parts.headers, &proxy.upstream, path)?;
    let mut headers = parts.headers;
    headers.remove(UPSTREAM_URL_HEADER);
    headers.remove(header::HOST);
    remove_hop_by_hop_headers(&mut headers);
    headers.insert(
        "connect-accept-encoding",
        axum::http::HeaderValue::from_static("identity"),
    );
    headers.insert(
        header::ACCEPT_ENCODING,
        axum::http::HeaderValue::from_static("identity"),
    );
    let body = to_bytes(body, usize::MAX)
        .await
        .map_err(|error| crate::Error::Protocol(format!("cannot read request body: {error}")))?;
    let upstream = proxy
        .client()
        .await?
        .request(parts.method, url)
        .headers(headers)
        .body(body)
        .send()
        .await?;
    let status = upstream.status();
    let mut headers = upstream.headers().clone();
    remove_hop_by_hop_headers(&mut headers);
    let body = upstream.bytes().await?;
    Ok(BufferedResponse {
        status,
        headers,
        body,
    })
}

fn upstream_url(headers: &axum::http::HeaderMap, fallback: &str, path: &str) -> Result<String> {
    let Some(value) = headers.get(UPSTREAM_URL_HEADER) else {
        return Ok(format!("{fallback}{path}"));
    };
    let value = value
        .to_str()
        .map_err(|error| crate::Error::Protocol(format!("invalid upstream URL header: {error}")))?;
    let url = reqwest::Url::parse(value)
        .map_err(|error| crate::Error::Protocol(format!("invalid upstream URL: {error}")))?;
    let host = url.host_str().unwrap_or_default();
    if url.scheme() != "https" || !crate::local_app::proxy_host_allowed(host) {
        return Err(crate::Error::Protocol(
            "upstream URL must target a Cursor HTTPS host".into(),
        ));
    }
    Ok(url.into())
}

fn remove_hop_by_hop_headers(headers: &mut axum::http::HeaderMap) {
    let connection_headers = headers
        .get(header::CONNECTION)
        .and_then(|value| value.to_str().ok())
        .map(|value| {
            value
                .split(',')
                .map(str::trim)
                .filter(|name| !name.is_empty())
                .map(str::to_owned)
                .collect::<Vec<_>>()
        })
        .unwrap_or_default();
    for name in connection_headers {
        headers.remove(name);
    }
    for name in [
        header::CONNECTION,
        header::PROXY_AUTHENTICATE,
        header::PROXY_AUTHORIZATION,
        header::TE,
        header::TRAILER,
        header::TRANSFER_ENCODING,
        header::UPGRADE,
    ] {
        headers.remove(name);
    }
    headers.remove("keep-alive");
}
