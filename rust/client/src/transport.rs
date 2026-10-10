use crate::{Cancellation, Code, Diagnostic, ExactJson, Limits, Method, PreparedRequest};
use std::{fmt, future::Future, sync::Arc};

/// Ordered HTTP header field with explicit credential sensitivity.
#[derive(Clone, PartialEq, Eq)]
pub struct Header {
    name: String,
    value: Vec<u8>,
    sensitive: bool,
}
impl Header {
    /// Create a field. Validation occurs before dispatch and on host evidence.
    pub fn new(name: impl Into<String>, value: impl AsRef<[u8]>) -> Self {
        Self {
            name: name.into(),
            value: value.as_ref().to_vec(),
            sensitive: false,
        }
    }
    /// Create a credential-bearing field. All header values are omitted from Debug.
    pub fn sensitive(name: impl Into<String>, value: impl AsRef<[u8]>) -> Self {
        Self {
            name: name.into(),
            value: value.as_ref().to_vec(),
            sensitive: true,
        }
    }
    /// Original field-name spelling.
    pub fn name(&self) -> &str {
        &self.name
    }
    /// Exact field bytes; callers must protect credential-bearing values.
    pub fn value(&self) -> &[u8] {
        &self.value
    }
    /// Whether this field was explicitly marked sensitive.
    pub fn is_sensitive(&self) -> bool {
        self.sensitive
    }
    pub(crate) fn validate(&self) -> Result<(), Diagnostic> {
        if self.name.is_empty()
            || !self.name.bytes().all(crate::prepare::token)
            || self
                .value
                .iter()
                .any(|b| *b == 127 || *b < 32 && *b != b'\t')
            || self.value.first().is_some_and(|b| b" \t".contains(b))
            || self.value.last().is_some_and(|b| b" \t".contains(b))
        {
            return Err(Diagnostic::new(Code::InvalidHeader));
        }
        Ok(())
    }
}
impl fmt::Debug for Header {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Header")
            .field("name", &self.name)
            .field("bytes", &self.value.len())
            .field("sensitive", &self.sensitive)
            .finish()
    }
}
/// Declared supplied-host restrictions. A declaration does not prove an arbitrary callback obeys it.
#[derive(Clone, Debug)]
pub struct HostCapabilities {
    /// Host promises no implicit redirect handling.
    pub no_redirects: bool,
    /// Host promises no automatic retries.
    pub no_retries: bool,
    /// Host promises no ambient authentication or cookie jar.
    pub no_ambient_auth: bool,
    /// Host promises to preserve representable targets exactly.
    pub exact_targets: bool,
    /// Whether percent-encoded dot segments survive its URL stack.
    pub encoded_dot_segments: bool,
    /// Header names this host cannot faithfully supply.
    pub forbidden_headers: Vec<String>,
}
impl HostCapabilities {
    /// Declaration suitable for a fully controlled programmable transport.
    pub fn programmable() -> Self {
        Self {
            no_redirects: true,
            no_retries: true,
            no_ambient_auth: true,
            exact_targets: true,
            encoded_dot_segments: true,
            forbidden_headers: vec![],
        }
    }
    /// Validate the complete prepared request before calling the host.
    pub fn admit(&self, request: &PreparedRequest) -> Result<(), Diagnostic> {
        if !self.no_redirects
            || !self.no_retries
            || !self.no_ambient_auth
            || !self.exact_targets
            || !self.encoded_dot_segments && crate::uri::has_encoded_dot_segment(request.target())
            || request.headers().iter().any(|h| {
                self.forbidden_headers
                    .iter()
                    .any(|n| n.eq_ignore_ascii_case(h.name()))
            })
        {
            return Err(Diagnostic::new(Code::HostCapability));
        }
        crate::uri::origin(request.target())?;
        for h in request.headers() {
            h.validate()?;
        }
        Ok(())
    }
}
/// Evidence about crossing the host dispatch boundary, not server receipt.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[allow(missing_docs)]
pub enum DispatchEvidence {
    NotDispatched,
    Dispatched,
}
/// Host evidence about upload completion. A response is not proof of upload completion.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[allow(missing_docs)]
pub enum UploadState {
    NotStarted,
    Complete,
    Incomplete,
    Unknown,
}
/// Provenance of bytes delivered by the supplied transport.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Provenance {
    /// Host confirms these are unchanged representation bytes.
    Wire,
    /// Host has completely decoded the listed content codings; do not decode again.
    Decoded,
    /// Host cannot establish whether content decoding occurred.
    Unknown,
}
/// Completion of bounded response-body delivery.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[allow(missing_docs)]
pub enum BodyState {
    Complete,
    Truncated,
    Failed,
}
/// Finite body evidence supplied by the host, including a late failure or truncation.
#[derive(Clone)]
pub struct DeliveredBody {
    /// Bytes delivered so far. Core will retain at most its configured limit.
    pub bytes: Arc<[u8]>,
    /// Whether these bytes end at representation completion.
    pub state: BodyState,
    /// Explicit content-decoding provenance.
    pub provenance: Provenance,
}
impl fmt::Debug for DeliveredBody {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("DeliveredBody")
            .field("bytes", &self.bytes.len())
            .field("state", &self.state)
            .field("provenance", &self.provenance)
            .finish()
    }
}
/// Response metadata and finite body delivered by the host.
#[derive(Clone, Debug)]
pub struct ResponseInput {
    /// HTTP status. Zero is permitted only to describe an opaque redirect refusal.
    pub status: u16,
    /// Ordered field lines, including duplicates.
    pub headers: Vec<Header>,
    /// Body evidence.
    pub body: DeliveredBody,
    /// A browser returned an opaque redirect instead of inspectable response metadata.
    pub opaque_redirect: bool,
}
/// Host outcome; an arbitrary failure detail is retained privately and never formatted.
#[derive(Clone)]
pub struct TransportResult {
    /// Available response evidence, even on an HTTP error or later failure.
    pub response: Option<ResponseInput>,
    /// Upload state independent from response state.
    pub upload: UploadState,
    /// Caller-owned opaque error detail. Core exposes it only through explicit access.
    pub error_detail: Option<Arc<str>>,
}
impl fmt::Debug for TransportResult {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("TransportResult")
            .field("response", &self.response)
            .field("upload", &self.upload)
            .field("has_error", &self.error_detail.is_some())
            .finish()
    }
}
/// Owned finite data handed to the transport once.
#[derive(Clone)]
pub struct DispatchRequest {
    /// HTTP method.
    pub method: Method,
    /// Exact target, potentially containing credentials.
    pub target: String,
    /// Complete ordered header lines.
    pub headers: Vec<Header>,
    /// Absent or finite payload; sharing these bytes does not authorize retries.
    pub body: Option<Arc<[u8]>>,
    /// Required response-byte admission bound for the host.
    pub response_limit: usize,
}
impl fmt::Debug for DispatchRequest {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("DispatchRequest")
            .field("method", &self.method)
            .field("body_bytes", &self.body.as_ref().map(|b| b.len()))
            .finish()
    }
}
/// Preserved response evidence, whether successful, erroneous, partial or undecodable.
#[derive(Clone, Debug)]
pub struct Response {
    input: ResponseInput,
    limits: Limits,
    validation: Option<Diagnostic>,
    omitted_header_lines: usize,
}
impl Response {
    /// HTTP status as supplied, including a preserved invalid/opaque value on refusal.
    pub fn status(&self) -> u16 {
        self.input.status
    }
    /// All delivered header lines in order.
    pub fn headers(&self) -> &[Header] {
        &self.input.headers
    }
    /// Header lines omitted because the host exceeded declared metadata bounds. JSON decode refuses such evidence.
    pub fn omitted_header_lines(&self) -> usize {
        self.omitted_header_lines
    }
    /// Bounded bytes delivered by the host; provenance is reported separately.
    pub fn raw(&self) -> &[u8] {
        &self.input.body.bytes
    }
    /// Body completion evidence.
    pub fn body_state(&self) -> BodyState {
        self.input.body.state
    }
    /// Wire/decoded/unknown provenance; raw never silently means original wire bytes.
    pub fn provenance(&self) -> Provenance {
        self.input.body.provenance
    }
    /// Decode delivered JSON once. Failure leaves this response and raw bytes available.
    pub fn json(&self) -> Result<ExactJson, Diagnostic> {
        if let Some(e) = &self.validation {
            return Err(e.clone());
        }
        if self.body_state() != BodyState::Complete {
            return Err(Diagnostic::new(Code::IncompleteBody));
        }
        if self.raw().is_empty() {
            return Err(Diagnostic::new(Code::EmptyBody));
        }
        let types: Vec<_> = self
            .headers()
            .iter()
            .filter(|h| h.name().eq_ignore_ascii_case("content-type"))
            .collect();
        if types.len() != 1 {
            return Err(Diagnostic::new(Code::InvalidMedia));
        }
        let media = std::str::from_utf8(types[0].value())
            .map_err(|_| Diagnostic::new(Code::InvalidMedia))?;
        if !crate::prepare::media_type(media)?.1 {
            return Err(Diagnostic::new(Code::UnsupportedMedia));
        }
        if self.provenance() != Provenance::Decoded {
            for h in self
                .headers()
                .iter()
                .filter(|h| h.name().eq_ignore_ascii_case("content-encoding"))
            {
                let s = std::str::from_utf8(h.value())
                    .map_err(|_| Diagnostic::new(Code::UnsupportedCoding))?;
                if s.split(',')
                    .map(|v| v.trim_matches([' ', '\t']))
                    .filter(|v| !v.is_empty())
                    .any(|v| !v.eq_ignore_ascii_case("identity"))
                {
                    return Err(Diagnostic::new(Code::UnsupportedCoding));
                }
            }
        }
        let mut limits = self.limits.clone();
        limits.document_bytes = limits.body_bytes;
        ExactJson::parse(self.raw(), limits).map_err(|e| e.diagnostic)
    }
}
/// Honest combined outcome. Inspect the response independently from any failure.
#[derive(Clone)]
pub struct Outcome {
    /// Whether the supplied callback was entered with this request.
    pub dispatch: DispatchEvidence,
    /// Independent upload evidence.
    pub upload: UploadState,
    /// Response metadata/body evidence when available.
    pub response: Option<Response>,
    /// Safe SDK-controlled category; arbitrary callback text is not formatted.
    pub error: Option<Diagnostic>,
    /// Cancellation observed locally, even if the host returned a response.
    pub cancelled: bool,
    detail: Option<Arc<str>>,
}
impl fmt::Debug for Outcome {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Outcome")
            .field("dispatch", &self.dispatch)
            .field("upload", &self.upload)
            .field("response", &self.response)
            .field("error", &self.error)
            .field("cancelled", &self.cancelled)
            .finish()
    }
}
impl Outcome {
    /// Construct a transport adapter's pre-dispatch refusal. The adapter asserts
    /// that it has not started I/O for this request. Upload is NotStarted and no
    /// response exists. Optional detail remains explicit-access, untrusted text.
    pub fn not_dispatched(error: Diagnostic, cancelled: bool, detail: Option<Arc<str>>) -> Self {
        Self {
            dispatch: DispatchEvidence::NotDispatched,
            upload: UploadState::NotStarted,
            response: None,
            error: Some(error),
            cancelled,
            detail,
        }
    }
    /// Explicit access to untrusted caller-owned error detail; never safe to log by default.
    pub fn host_error_detail(&self) -> Option<&str> {
        self.detail.as_deref()
    }
}
impl PreparedRequest {
    /// Invoke once through a supplied future-producing callback, without a universal Send bound.
    /// Dropping this future does not establish remote cancellation or upload completion.
    pub async fn invoke<F, Fut>(
        &self,
        cancellation: Cancellation,
        host: &HostCapabilities,
        dispatch: F,
    ) -> Outcome
    where
        F: FnOnce(DispatchRequest, Cancellation) -> Fut,
        Fut: Future<Output = TransportResult>,
    {
        let preflight = cancellation.check().and_then(|_| host.admit(self));
        if let Err(error) = preflight {
            return Outcome {
                dispatch: DispatchEvidence::NotDispatched,
                upload: UploadState::NotStarted,
                response: None,
                error: Some(error),
                cancelled: cancellation.is_cancelled(),
                detail: None,
            };
        }
        let request = DispatchRequest {
            method: self.method,
            target: self.target.clone(),
            headers: self.headers.clone(),
            body: self.body.clone(),
            response_limit: self.limits.body_bytes,
        };
        let result = dispatch(request, cancellation.clone()).await;
        let mut error = result
            .error_detail
            .as_ref()
            .map(|_| Diagnostic::new(Code::TransportFailure));
        let response = result.response.map(|mut input| {
            let mut validation = None;
            let mut kept = Vec::new();
            let mut metadata_bytes = 0usize;
            let mut omitted_header_lines = 0;
            for h in input.headers {
                let size = h.name.len().saturating_add(h.value.len());
                if kept.len() >= self.limits.headers
                    || metadata_bytes.saturating_add(size) > self.limits.document_bytes
                {
                    omitted_header_lines += 1;
                    continue;
                }
                metadata_bytes += size;
                kept.push(Header {
                    name: h.name.into_boxed_str().into_string(),
                    value: h.value.into_boxed_slice().into_vec(),
                    sensitive: h.sensitive,
                });
            }
            input.headers = kept;
            if omitted_header_lines > 0 {
                validation = Some(Diagnostic::new(Code::InvalidHostEvidence));
            }
            if input.opaque_redirect {
                validation = Some(Diagnostic::new(Code::OpaqueRedirect));
            } else if !(100..=599).contains(&input.status)
                || input.headers.len() > self.limits.headers
                || input.headers.iter().any(|h| h.validate().is_err())
            {
                validation = Some(Diagnostic::new(Code::InvalidHostEvidence));
            }
            if input.body.bytes.len() > self.limits.body_bytes {
                input.body.bytes = Arc::from(&input.body.bytes[..self.limits.body_bytes]);
                input.body.state = BodyState::Truncated;
            }
            if error.is_none() {
                error = validation.clone();
            }
            Response {
                input,
                limits: self.limits.clone(),
                validation,
                omitted_header_lines,
            }
        });
        if response.is_none() && error.is_none() {
            error = Some(Diagnostic::new(Code::InvalidHostEvidence));
        }
        Outcome {
            dispatch: DispatchEvidence::Dispatched,
            upload: result.upload,
            response,
            error,
            cancelled: cancellation.is_cancelled(),
            detail: result.error_detail,
        }
    }
}
