//! Keeps explicitly local CLI startup metadata off the Cursor upstream.
use axum::{
    body::{to_bytes, Body},
    extract::{Extension, Request},
    http::{header, HeaderMap, HeaderValue, Response},
};

use crate::{api::cursor::proxy, Result};

pub const LOCAL_METADATA_HEADER: &str = "x-cursor-byok-local-metadata";

pub fn is_local_request(headers: &HeaderMap) -> bool {
    headers
        .get(LOCAL_METADATA_HEADER)
        .is_some_and(|value| value == "1")
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
        assert!(!is_local_request(&headers));
        headers.insert(LOCAL_METADATA_HEADER, HeaderValue::from_static("0"));
        assert!(!is_local_request(&headers));
        headers.insert(LOCAL_METADATA_HEADER, HeaderValue::from_static("1"));
        assert!(is_local_request(&headers));
    }
}
