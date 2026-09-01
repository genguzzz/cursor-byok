//! Applies provider retry and backoff behavior.
use std::time::Duration;

use tokio_util::sync::CancellationToken;

use crate::{Error, Result};

use super::CallRecorder;

#[derive(Clone, Copy, Debug)]
pub(crate) struct RetryPolicy {
    pub retries: u32,
    pub delay: Duration,
}

impl Default for RetryPolicy {
    fn default() -> Self {
        Self {
            retries: 5,
            delay: Duration::from_secs(5),
        }
    }
}

#[derive(Debug)]
pub(crate) enum Attempt {
    Response(reqwest::Response),
    Cancelled,
}

pub(crate) async fn send_with_retry<F>(
    label: &str,
    build: F,
    policy: RetryPolicy,
    cancellation: &CancellationToken,
    recorder: Option<&CallRecorder>,
    request_headers: serde_json::Value,
    request_body: &serde_json::Value,
) -> Result<Attempt>
where
    F: Fn() -> reqwest::RequestBuilder,
{
    for attempt in 0..=policy.retries {
        let response = tokio::select! {
            _ = cancellation.cancelled() => return Ok(Attempt::Cancelled),
            response = build().send() => response,
        }?;
        if let Some(recorder) = recorder {
            recorder
                .response_headers(response.status().as_u16())
                .await?;
        }
        if response.status().is_success() {
            return Ok(Attempt::Response(response));
        }
        let status = response.status();
        let bytes = response.bytes().await?;
        let error = Error::Provider(format!(
            "{label} {status}: {}",
            String::from_utf8_lossy(&bytes)
        ));
        if attempt == policy.retries {
            return Err(error);
        }
        tracing::warn!(
            provider = label,
            status = status.as_u16(),
            attempt = attempt + 1,
            retries = policy.retries,
            delay_ms = policy.delay.as_millis(),
            "provider returned a non-success status, retrying"
        );
        if let Some(recorder) = recorder {
            recorder
                .retry(&error, request_headers.clone(), request_body)
                .await?;
        }
        tokio::select! {
            _ = cancellation.cancelled() => return Ok(Attempt::Cancelled),
            _ = tokio::time::sleep(policy.delay) => {}
        }
    }
    unreachable!("the retry loop returns on the final attempt")
}

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
