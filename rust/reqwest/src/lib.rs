//! Optional native HTTP transport with an explicit conservative fidelity profile.
#![doc = include_str!("../README.md")]
#![forbid(unsafe_code)]
#![warn(missing_docs)]

use dynamic_openapi_client::{
    BodyState, Cancellation, Code, Credential, DeliveredBody, Diagnostic, Header, HostCapabilities,
    Operation, Outcome, PreparedRequest, Provenance, RequestBuilder, ResponseInput,
    TransportResult, UploadState,
};
use std::{fmt, sync::Arc, time::Duration};

/// Explicit additional TLS trust root. Hostname and certificate checks stay enabled.
pub use reqwest::Certificate;

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
/// This profile opens a fresh connection per request to exclude hidden pooled
/// connection retries. Cloning this value does not enable connection pooling.
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
    pub fn build(self) -> Result<Client, reqwest::Error> {
        Ok(Client {
            inner: self.inner.build()?,
            credentials: self.credentials.into(),
            capabilities: Arc::new(capabilities()),
        })
    }
}

impl Client {
    /// Build a client using the documented deadlines and no configured credentials.
    pub fn new() -> Result<Self, reqwest::Error> {
        Self::builder().build()
    }

    /// Begin configuring a client. It cannot acquire ambient authentication,
    /// cookies or proxy configuration, follow redirects or retry a request.
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
            .pool_max_idle_per_host(0)
            .http1_max_headers(256)
            .timeout(Duration::from_secs(30))
            .connect_timeout(Duration::from_secs(10));
        ClientBuilder {
            inner,
            credentials: Vec::new(),
        }
    }

    /// Begin the existing core builder with this client's configured credentials.
    /// Per-call inputs and security/origin checks remain core preparation work.
    pub fn request<'a>(&'a self, operation: &Operation) -> RequestBuilder<'a> {
        let mut request = operation.request();
        for (scheme, credential) in self.credentials.iter() {
            request = request.credential(scheme.as_str(), credential.clone());
        }
        request
    }

    /// Execute a prepared request once and preserve available response evidence.
    ///
    /// Unsupported URL spelling or transport-controlled headers refuse before
    /// dispatch. After entering backend execution, upload completion is unknown.
    /// Cancellation stops the local wait without asserting remote cancellation.
    pub async fn execute(&self, prepared: &PreparedRequest, cancellation: Cancellation) -> Outcome {
        if let Err(error) = cancellation.check() {
            return Outcome::not_dispatched(error, true, None);
        }
        let capabilities = &self.capabilities;
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
                let mut request = request;
                if let Some(body) = dispatch.body {
                    *request.body_mut() = Some(bytes::Bytes::from_owner(body).into());
                }
                self.exchange(request, dispatch.response_limit, cancellation)
            })
            .await
    }

    async fn exchange(
        &self,
        request: reqwest::Request,
        limit: usize,
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
        let headers = response
            .headers()
            .iter()
            .map(|(name, value)| Header::new(name.as_str(), value.as_bytes()))
            .collect();
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
            response: Some(ResponseInput {
                status,
                headers,
                opaque_redirect: false,
                body: DeliveredBody {
                    bytes: bytes.into(),
                    state,
                    provenance: Provenance::Wire,
                },
            }),
            upload: UploadState::Unknown,
            error_detail: detail.map(Arc::from),
        }
    }
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
