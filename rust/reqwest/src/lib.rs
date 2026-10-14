//! Optional native HTTP transport with an explicit conservative fidelity profile.
#![doc = include_str!("../README.md")]
#![forbid(unsafe_code)]
#![warn(missing_docs)]

use dynamic_openapi_client::diagnostics::Code;
use dynamic_openapi_client::transport::{
    BodyState, HeaderDelivery, HeaderFidelity, HostCapabilities, PreparedRequest, Provenance,
    ResponseAdmission, ResponseInput, TransportResult, UploadState,
};
use dynamic_openapi_client::{
    Cancellation, Credential, Diagnostic, Header, Outcome, RequestBuilder,
};
use std::{fmt, sync::Arc, time::Duration};

/// Explicit TLS trust configuration using the backend's certificate type.
pub mod tls {
    /// Additional trust root. Hostname and certificate checks stay enabled.
    pub use reqwest::Certificate;
}
use tls::Certificate;

const FORBIDDEN: &[&str] = &[
    "host",
    "content-length",
    "transfer-encoding",
    "connection",
    "proxy-connection",
    "proxy-authorization",
    "keep-alive",
    "upgrade",
    "te",
    "trailer",
    "expect",
];

/// Reusable configuration, credentials and TLS state for native Tokio HTTP/1.1.
///
/// Clones share eligible HTTP/1.1 connections. The backend may recover a request
/// proven never serialized when an idle connection is unusable; it never
/// automatically repeats a request after serialization began or may have begun.
#[derive(Clone)]
pub struct Client {
    inner: reqwest::Client,
    credentials: Arc<[(String, Credential)]>,
    capabilities: Arc<HostCapabilities>,
}
impl fmt::Debug for Client {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Client")
            .field("configured_credentials", &self.credentials.len())
            .finish_non_exhaustive()
    }
}

/// Configure deadlines, explicit trust roots and origin-scoped core credentials.
/// The backend restrictions cannot be disabled through this builder.
pub struct ClientBuilder {
    inner: reqwest::ClientBuilder,
    credentials: Vec<(String, Credential)>,
}
impl fmt::Debug for ClientBuilder {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("ClientBuilder")
            .field("configured_credentials", &self.credentials.len())
            .finish_non_exhaustive()
    }
}
impl ClientBuilder {
    /// Add a credential for a declared security scheme. Core preparation checks
    /// its allowed origins. Duplicate entries cause core duplicate refusal.
    pub fn credential(mut self, scheme: impl Into<String>, credential: Credential) -> Self {
        self.credentials.push((scheme.into(), credential));
        self
    }

    /// Set the deadline from backend execution through response-body completion.
    /// The default is 30 seconds; zero requests an immediate deadline.
    pub fn timeout(mut self, timeout: Duration) -> Self {
        self.inner = self.inner.timeout(timeout);
        self
    }

    /// Set the connection deadline. The default is 10 seconds.
    pub fn connect_timeout(mut self, timeout: Duration) -> Self {
        self.inner = self.inner.connect_timeout(timeout);
        self
    }

    /// Trust an additional explicit certificate. Native trust and normal
    /// hostname/certificate verification remain enabled.
    pub fn add_root_certificate(mut self, certificate: Certificate) -> Self {
        self.inner = self.inner.tls_certs_merge([certificate]);
        self
    }

    /// Build the configured native client without making a request.
    pub fn build(self) -> Result<Client, Diagnostic> {
        Ok(Client {
            inner: self.inner.build().map_err(|_| {
                Diagnostic::from(dynamic_openapi_client::diagnostics::HostRefusal::Configuration)
            })?,
            credentials: self.credentials.into(),
            capabilities: Arc::new(capabilities()),
        })
    }
}

impl Client {
    /// Build a client using the documented deadlines and no configured credentials.
    pub fn new() -> Result<Self, Diagnostic> {
        Self::builder().build()
    }

    /// Begin configuring a client. It cannot acquire ambient authentication,
    /// cookies or proxy configuration, follow redirects or replay a started request.
    /// Proven-never-serialized recovery is permitted within the request deadline.
    pub fn builder() -> ClientBuilder {
        let inner = reqwest::Client::builder()
            .tls_backend_rustls()
            .redirect(reqwest::redirect::Policy::none())
            .retry(reqwest::retry::never())
            .referer(false)
            .no_proxy()
            .no_gzip()
            .no_brotli()
            .no_deflate()
            .no_zstd()
            .http1_only()
            .pool_max_idle_per_host(8)
            .pool_idle_timeout(Duration::from_secs(90))
            .http1_max_headers(256)
            .timeout(Duration::from_secs(30))
            .connect_timeout(Duration::from_secs(10));
        ClientBuilder {
            inner,
            credentials: Vec::new(),
        }
    }

    /// Consume a checked builder, applying configured scoped credentials once before
    /// preparation. Per-call/default duplicates refuse rather than overriding.
    /// Preparation and I/O share the builder's cancellation token. Preparation
    /// failure returns a non-dispatched Outcome with the actual corrective diagnostic.
    pub async fn send(&self, mut request: RequestBuilder) -> Outcome {
        for (scheme, credential) in self.credentials.iter() {
            request = request.credential(scheme.as_str(), credential.clone());
        }
        let (prepared, cancellation) = dynamic_openapi_client::advanced::prepare(request);
        match prepared {
            Ok(prepared) => advanced::execute(self, &prepared, cancellation).await,
            Err(error) => Outcome::not_dispatched(error, cancellation.is_cancelled(), None),
        }
    }

    async fn exchange(
        &self,
        request: reqwest::Request,
        admission: ResponseAdmission,
        cancellation: Cancellation,
    ) -> TransportResult {
        if cancellation.is_cancelled() {
            return failed(
                None,
                UploadState::NotStarted,
                "local cancellation before backend execution",
            );
        }
        let cancelled = cancellation.cancelled();
        tokio::pin!(cancelled);
        let mut response = tokio::select! {
            biased;
            () = &mut cancelled => return failed(None, UploadState::Unknown, "local cancellation during request"),
            result = self.inner.execute(request) => match result {
                Ok(response) => response,
                Err(error) => return failed(None, UploadState::Unknown, request_error(&error)),
            },
        };
        let status = response.status().as_u16();
        let (headers, header_delivery) = collect_headers(
            response
                .headers()
                .iter()
                .map(|(name, value)| (name.as_str(), value.as_bytes())),
            admission.header_entries(),
            admission.header_bytes(),
        );
        let limit = admission.body_bytes();
        let mut bytes = Vec::new();
        let (state, detail) = loop {
            let chunk = tokio::select! {
                biased;
                () = &mut cancelled => break (BodyState::Failed, Some("local cancellation during response body")),
                result = response.chunk() => result,
            };
            match chunk {
                Ok(None) => break (BodyState::Complete, None),
                Ok(Some(chunk)) => {
                    let available = limit - bytes.len();
                    if chunk.len() > available {
                        bytes.extend_from_slice(&chunk[..available]);
                        break (BodyState::Truncated, None);
                    }
                    bytes.extend_from_slice(&chunk);
                }
                Err(error) => {
                    break (
                        BodyState::Failed,
                        Some(if error.is_timeout() {
                            "response body deadline exceeded"
                        } else {
                            "response body delivery failed"
                        }),
                    );
                }
            }
        };
        TransportResult {
            response: Some({
                let mut response =
                    ResponseInput::new(status, headers, bytes.into(), state, Provenance::Wire);
                response.header_fidelity = HeaderFidelity::HostProcessed;
                response.header_delivery = header_delivery;
                response
            }),
            upload: UploadState::Unknown,
            error_detail: detail.map(Arc::from),
        }
    }
}

/// Deliberate execution of an already prepared request.
/// No client credential defaults are applied after preparation.
pub mod advanced {
    use super::*;

    /// Execute a prepared request and preserve available response evidence.
    /// An unusable idle connection may be replaced only while the backend can
    /// recover the original request before serialization has begun.
    ///
    /// Unsupported URL spelling or transport-controlled headers refuse before
    /// dispatch. After entering backend execution, upload completion is unknown.
    /// Cancellation stops the local wait without asserting remote cancellation.
    pub async fn execute(
        client: &Client,
        prepared: &PreparedRequest,
        cancellation: Cancellation,
    ) -> Outcome {
        if let Err(error) = cancellation.check() {
            return Outcome::not_dispatched(error, true, None);
        }
        let capabilities = &client.capabilities;
        if let Err(error) = capabilities.admit(prepared) {
            return Outcome::not_dispatched(
                error,
                cancellation.is_cancelled(),
                Some(Arc::from("native request profile refused")),
            );
        }
        let request = match backend_request(prepared) {
            Ok(request) => request,
            Err(detail) => {
                return Outcome::not_dispatched(
                    Diagnostic::new(Code::HostCapability),
                    cancellation.is_cancelled(),
                    Some(Arc::from(detail)),
                );
            }
        };
        prepared
            .invoke(cancellation, capabilities, |dispatch, cancellation| {
                let admission = dispatch.response_admission();
                let mut request = request;
                if let Some(body) = dispatch.body() {
                    *request.body_mut() = Some(bytes::Bytes::from_owner(body.clone()).into());
                }
                client.exchange(request, admission, cancellation)
            })
            .await
    }
}

// Inspect borrowed lengths before Header::new allocates either name or value.
// One next() beyond the entry limit distinguishes EOF from a known omission.
// Stop at the first inadmissible field: its syntax and the remaining tail are
// unclassified, and no total omitted count can be inferred from them.
fn collect_headers<'a>(
    entries: impl IntoIterator<Item = (&'a str, &'a [u8])>,
    entry_limit: usize,
    byte_limit: usize,
) -> (Vec<Header>, HeaderDelivery) {
    let mut headers = Vec::new();
    let mut bytes = 0usize;
    for (name, value) in entries {
        let total = bytes
            .checked_add(name.len())
            .and_then(|n| n.checked_add(value.len()));
        let Some(total) = total.filter(|n| *n <= byte_limit && headers.len() < entry_limit) else {
            return (headers, HeaderDelivery::Incomplete);
        };
        if headers.len() == headers.capacity() {
            let capacity = headers.capacity().saturating_mul(2).max(1).min(entry_limit);
            headers.reserve_exact(capacity - headers.len());
        }
        headers.push(Header::new(name, value));
        bytes = total;
    }
    (headers, HeaderDelivery::Complete)
}

fn capabilities() -> HostCapabilities {
    HostCapabilities {
        encoded_dot_segments: false,
        forbidden_headers: FORBIDDEN.iter().map(|name| (*name).to_owned()).collect(),
        ..HostCapabilities::programmable()
    }
}

fn backend_request(prepared: &PreparedRequest) -> Result<reqwest::Request, &'static str> {
    let url = reqwest::Url::parse(prepared.target()).map_err(|_| "target is not representable")?;
    if url.as_str() != prepared.target()
        || !matches!(url.scheme(), "http" | "https")
        || !url.username().is_empty()
        || url.password().is_some()
        || url.fragment().is_some()
    {
        return Err("target would be normalized by the native HTTP stack");
    }
    let _: http::Uri = prepared
        .target()
        .parse()
        .map_err(|_| "target is not an HTTP URI")?;
    let method = reqwest::Method::from_bytes(prepared.method().as_str().as_bytes())
        .map_err(|_| "method is not representable")?;
    let mut request = reqwest::Request::new(method, url);
    for header in prepared.headers() {
        let name = reqwest::header::HeaderName::from_bytes(header.name().as_bytes())
            .map_err(|_| "header name is not representable")?;
        let mut value = reqwest::header::HeaderValue::from_bytes(header.value())
            .map_err(|_| "header value is not representable")?;
        value.set_sensitive(header.is_sensitive());
        request.headers_mut().append(name, value);
    }
    Ok(request)
}

fn failed(
    response: Option<ResponseInput>,
    upload: UploadState,
    detail: &'static str,
) -> TransportResult {
    TransportResult {
        response,
        upload,
        error_detail: Some(Arc::from(detail)),
    }
}

fn request_error(error: &reqwest::Error) -> &'static str {
    if error.is_timeout() {
        "request deadline exceeded"
    } else if error.is_connect() {
        "connection or TLS establishment failed"
    } else {
        "request dispatch failed"
    }
}

#[cfg(test)]
mod metadata_tests {
    use super::*;
    use std::cell::Cell;

    #[test]
    fn empty_and_exact_count_observe_eof_without_inventing_omissions() {
        for limit in [0, 1] {
            let (headers, delivery) = collect_headers([], limit, 0);
            assert!(headers.is_empty());
            assert_eq!(delivery, HeaderDelivery::Complete);
        }
        let (headers, delivery) = collect_headers([("x", b"v".as_slice())], 1, 2);
        assert_eq!(headers.len(), 1);
        assert_eq!(delivery, HeaderDelivery::Complete);
        let visits = Cell::new(0);
        let entries = [
            ("x", b"v".as_slice()),
            ("next", b"v"),
            ("uninspected", b"v"),
        ];
        let (headers, delivery) = collect_headers(
            entries
                .into_iter()
                .inspect(|_| visits.set(visits.get() + 1)),
            1,
            100,
        );
        assert_eq!(headers.len(), 1);
        assert_eq!(delivery, HeaderDelivery::Incomplete);
        assert_eq!(visits.get(), 2);
    }

    #[test]
    fn byte_boundary_counts_both_name_and_value_and_stops_at_first_overflow() {
        let entries = [("x", b"ab".as_slice()), ("y", b"c"), ("z", b"d")];
        for (bound, retained, delivery) in [
            (0, 0, HeaderDelivery::Incomplete),
            (2, 0, HeaderDelivery::Incomplete),
            (3, 1, HeaderDelivery::Incomplete),
            (4, 1, HeaderDelivery::Incomplete),
            (5, 2, HeaderDelivery::Incomplete),
            (7, 3, HeaderDelivery::Complete),
        ] {
            let visits = Cell::new(0);
            let (headers, actual) = collect_headers(
                entries
                    .into_iter()
                    .inspect(|_| visits.set(visits.get() + 1)),
                3,
                bound,
            );
            assert_eq!(headers.len(), retained);
            assert_eq!(actual, delivery);
            assert_eq!(visits.get(), (retained + 1).min(3));
        }
        let (headers, delivery) = collect_headers([("x", b"ab".as_slice())], 1, 3);
        assert_eq!(headers[0].value(), b"ab");
        assert_eq!(delivery, HeaderDelivery::Complete);
    }

    #[test]
    fn oversized_borrowed_metadata_is_not_copied_and_growth_is_capped() {
        let large = vec![b'a'; 1024 * 1024]; // Already owned by the host fixture.
        for (count, bytes) in [(0, usize::MAX), (1, 4)] {
            let (headers, delivery) = collect_headers([("x", large.as_slice())], count, bytes);
            assert!(headers.is_empty());
            assert_eq!(headers.capacity(), 0);
            assert_eq!(delivery, HeaderDelivery::Incomplete);
        }
        let (headers, delivery) = collect_headers([("x", b"\xff\x80".as_slice()); 5], 5, 15);
        assert_eq!(headers.len(), 5);
        assert!(headers.capacity() <= 5);
        assert_eq!(headers[0].value(), b"\xff\x80");
        assert_eq!(delivery, HeaderDelivery::Complete);
    }
}
