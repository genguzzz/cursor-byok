//! Keeps explicitly local CLI startup metadata off the Cursor upstream.
use std::{ffi::OsStr, sync::LazyLock};

use axum::{
    body::{to_bytes, Body},
    extract::{Extension, Request},
    http::{header, HeaderMap, HeaderValue, Response},
};

use crate::{api::cursor::proxy, Result};

pub const LOCAL_METADATA_HEADER: &str = "x-cursor-byok-local-metadata";
pub const FORCE_LOCAL_METADATA_ENV: &str = "CURSOR_FORCE_LOCAL_STARTUP_METADATA";

static FORCE_LOCAL_METADATA: LazyLock<bool> =
    LazyLock::new(|| local_metadata_enabled(std::env::var_os(FORCE_LOCAL_METADATA_ENV).as_deref()));

pub fn is_local_request(headers: &HeaderMap) -> bool {
    *FORCE_LOCAL_METADATA || has_local_metadata_header(headers)
}

fn has_local_metadata_header(headers: &HeaderMap) -> bool {
    headers
        .get(LOCAL_METADATA_HEADER)
        .is_some_and(|value| value == "1")
}

fn local_metadata_enabled(value: Option<&OsStr>) -> bool {
    value.and_then(OsStr::to_str).is_some_and(|value| {
        matches!(
            value.to_ascii_lowercase().as_str(),
            "1" | "true" | "yes" | "on"
        )
    })
}

pub async fn optional(
    Extension(proxy): Extension<proxy::CursorProxy>,
    request: Request<Body>,
) -> Result<Response<Body>> {
    if !is_local_request(request.headers()) {
        return proxy::forward(Extension(proxy), request).await;
    }
    consume(request).await?;
    Ok(empty_proto())
}

pub async fn consume(request: Request<Body>) -> Result<()> {
    to_bytes(request.into_body(), usize::MAX)
        .await
        .map_err(|error| crate::Error::Protocol(format!("cannot read request body: {error}")))?;
    Ok(())
}

fn empty_proto() -> Response<Body> {
    let mut response = Response::new(Body::empty());
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/proto"),
    );
    response
        .headers_mut()
        .insert(header::CONTENT_LENGTH, HeaderValue::from_static("0"));
    response
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn recognizes_only_the_explicit_local_metadata_header() {
        let mut headers = HeaderMap::new();
        assert!(!has_local_metadata_header(&headers));
        headers.insert(LOCAL_METADATA_HEADER, HeaderValue::from_static("0"));
        assert!(!has_local_metadata_header(&headers));
        headers.insert(LOCAL_METADATA_HEADER, HeaderValue::from_static("1"));
        assert!(has_local_metadata_header(&headers));
    }

    #[test]
    fn recognizes_explicit_force_local_values() {
        for value in ["1", "true", "TRUE", "yes", "on"] {
            assert!(local_metadata_enabled(Some(OsStr::new(value))), "{value}");
        }
        for value in ["", "0", "false", "no", "off", "unexpected"] {
            assert!(!local_metadata_enabled(Some(OsStr::new(value))), "{value}");
        }
        assert!(!local_metadata_enabled(None));
    }
}
