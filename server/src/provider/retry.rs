//! Provider-hop debug capture helpers kept after retry moved into `attempt`.

/// Metadata needed to tee one provider hop (hop 3) response when debug is on.
#[derive(Clone)]
pub(crate) struct ProviderHopMeta {
    pub provider: String,
    pub model_call_id: String,
    pub request_id: String,
    pub request_header: http::HeaderMap,
    pub request_body: Vec<u8>,
    pub started: std::time::Instant,
}

/// Tees a provider response stream into the debug capture store, emitting one
/// [`crate::debug::ProviderHop`] when the stream completes. When debug capture
/// is off this returns the original stream untouched (zero overhead), matching
/// the legacy Go guard that skips body copying unless a sink is registered.
pub(crate) fn tee_provider_response(
    response: reqwest::Response,
    hop: ProviderHopMeta,
) -> futures_util::future::Either<
    crate::debug::capture::BodyTee<
        impl futures_util::Stream<Item = Result<bytes::Bytes, reqwest::Error>>,
        reqwest::Error,
    >,
    impl futures_util::Stream<Item = Result<bytes::Bytes, reqwest::Error>>,
> {
    if crate::debug::capture::store().is_none() {
        return futures_util::future::Either::Right(response.bytes_stream());
    }
    let status = response.status().as_u16();
    let url = response.url().to_string();
    let response_header = crate::debug::capture::response_headers_to_header_map(response.headers());
    let (host, path) = crate::debug::capture::split_url(&url);
    let ProviderHopMeta {
        provider,
        model_call_id,
        request_id,
        request_header,
        request_body,
        started,
    } = hop;
    futures_util::future::Either::Left(crate::debug::capture::BodyTee::<_, reqwest::Error>::new(
        response.bytes_stream(),
        move |captured, _size, _truncated, error| {
            crate::debug::ProviderHop {
                method: "POST".to_owned(),
                url: url.clone(),
                host: host.clone(),
                path: path.clone(),
                status,
                request_id: request_id.clone(),
                model_call_id: model_call_id.clone(),
                provider: provider.clone(),
                request_header: request_header.clone(),
                response_header: response_header.clone(),
                request_body: request_body.clone(),
                response_body: captured,
                error: error.unwrap_or_default(),
                started,
            }
            .emit();
        },
    ))
}
