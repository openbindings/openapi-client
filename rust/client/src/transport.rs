use crate::{Cancellation, Code, Diagnostic, ExactJson, HostRefusal, Limits, PreparedRequest};
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
    /// Create a credential-bearing field. All names and values are omitted from Debug.
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
            .field("name_bytes", &self.name.len())
            .field("bytes", &self.value.len())
            .field("sensitive", &self.sensitive)
            .finish()
    }
}
/// Declared supplied-host restrictions. A declaration does not prove an arbitrary callback obeys it.
#[derive(Clone)]
pub struct HostCapabilities {
    /// Host promises no implicit redirect handling.
    pub no_redirects: bool,
    /// Host promises no automatic replay once request serialization has begun
    /// or may have begun. Recovery is allowed only when the host proves the
    /// original request was never serialized, for example on a stale pooled
    /// connection. Status responses, partial writes and ambiguous failures must
    /// not trigger another attempt. The core itself never retries.
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
    /// Validate using the strict replay policy. The declaration is not proof of host behavior.
    pub fn admit(&self, request: &PreparedRequest) -> Result<(), Diagnostic> {
        self.admit_with_replay(request, ReplayPolicy::RequireNoReplay)
            .map(|_| ())
    }
    /// Validate before callback entry, returning the replay declaration that would be recorded.
    /// Non-replay restrictions and request validity precede replay policy. A Replay
    /// refusal does not establish that a later adapter-specific preflight will succeed.
    pub fn admit_with_replay(
        &self,
        request: &PreparedRequest,
        replay: ReplayPolicy,
    ) -> Result<ReplayAssurance, Diagnostic> {
        if !self.no_redirects {
            return Err(HostRefusal::Redirects.into());
        }
        if !self.no_ambient_auth {
            return Err(HostRefusal::AmbientAuth.into());
        }
        if !self.exact_targets {
            return Err(HostRefusal::ExactTargets.into());
        }
        if !self.encoded_dot_segments && crate::uri::has_encoded_dot_segment(request.target()) {
            return Err(HostRefusal::EncodedDotSegment.into());
        }
        if request.headers().iter().any(|h| {
            self.forbidden_headers
                .iter()
                .any(|n| n.eq_ignore_ascii_case(h.name()))
        }) {
            return Err(HostRefusal::ForbiddenHeader.into());
        }
        crate::uri::origin(request.target())?;
        for h in request.headers() {
            h.validate()?;
        }
        if self.no_retries {
            Ok(ReplayAssurance::NoReplayDeclared)
        } else if replay == ReplayPolicy::AllowHostManaged {
            Ok(ReplayAssurance::HostManaged)
        } else {
            Err(HostRefusal::Replay.into())
        }
    }
}
impl fmt::Debug for HostCapabilities {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("HostCapabilities")
            .field("no_redirects", &self.no_redirects)
            .field("no_retries", &self.no_retries)
            .field("no_ambient_auth", &self.no_ambient_auth)
            .field("exact_targets", &self.exact_targets)
            .field("encoded_dot_segments", &self.encoded_dot_segments)
            .field("forbidden_header_count", &self.forbidden_headers.len())
            .finish()
    }
}
/// Caller authorization for host replay behavior; all other admission rules are unchanged.
/// ```compile_fail
/// use dynamic_openapi_client::ReplayPolicy;
/// fn exhaustive(p: ReplayPolicy) { match p {
///     ReplayPolicy::RequireNoReplay => (), ReplayPolicy::AllowHostManaged => (),
/// } }
/// ```
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
#[non_exhaustive]
pub enum ReplayPolicy {
    /// Require the host's no-replay declaration; used by the default admission and invocation APIs.
    #[default]
    RequireNoReplay,
    /// Accept host-managed repetition/retransmission, without an attempt bound or strict promise.
    AllowHostManaged,
}
/// The admitted declaration under which the core entered its callback, not observed attempts.
/// ```compile_fail
/// use dynamic_openapi_client::ReplayAssurance;
/// fn exhaustive(p: ReplayAssurance) { match p {
///     ReplayAssurance::NoReplayDeclared => (), ReplayAssurance::HostManaged => (),
/// } }
/// ```
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum ReplayAssurance {
    /// The supplied host declared no automatic replay after serialization may have begun.
    NoReplayDeclared,
    /// The caller accepted host-managed replay and the host made no strict declaration.
    HostManaged,
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
/// Source and interpretation of bytes delivered by the supplied transport.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Provenance {
    /// Host confirms these are unchanged representation bytes.
    Wire,
    /// Host has completely decoded the listed content codings; do not decode again.
    Decoded,
    /// Host cannot establish whether content decoding occurred.
    Unknown,
    /// Interpret bytes exactly as supplied by the selected host's exposed body stream.
    /// This asserts neither original wire equality nor removal of listed content
    /// codings. JSON parses the delivered bytes directly, retaining coding metadata.
    HostDelivered,
}
/// Completion of bounded response-body delivery.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[allow(missing_docs)]
pub enum BodyState {
    Complete,
    Truncated,
    Failed,
}
/// Meaning of delivered structured header metadata, independent of body provenance.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
#[non_exhaustive]
pub enum HeaderFidelity {
    /// No assertion about completeness, spelling, order or combination.
    #[default]
    Unknown,
    /// The host may normalize, combine, reorder or filter fields.
    HostProcessed,
    /// Host asserts received structured names, value bytes, duplicates and order
    /// are preserved without host omission. This is not raw HTTP framing or
    /// whitespace capture. Core bounds may separately omit supplied entries.
    ExactFields,
}
/// Whether the adapter finished enumerating the header entries exposed by its host.
/// This is independent of host filtering, field fidelity and body completion.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
#[non_exhaustive]
pub enum HeaderDelivery {
    /// The adapter made no enumeration-completeness assertion.
    #[default]
    Unspecified,
    /// Enumeration reached the end of the host-exposed entries.
    /// This does not assert that the host exposed every original wire field.
    Complete,
    /// Enumeration or conversion stopped before all exposed entries were delivered.
    /// The uninspected tail is unclassified; no omitted total is implied.
    Incomplete,
}

/// Immutable response-retention bounds copied from the prepared request's Limits.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ResponseAdmission {
    body_bytes: usize,
    header_entries: usize,
    header_bytes: usize,
}
impl ResponseAdmission {
    /// Maximum retained response body bytes, from Limits::body_bytes.
    pub fn body_bytes(&self) -> usize {
        self.body_bytes
    }
    /// Maximum retained whole header entries, from Limits::headers.
    pub fn header_entries(&self) -> usize {
        self.header_entries
    }
    /// Maximum aggregate UTF-8 name bytes plus value bytes, excluding framing.
    /// This uses Limits::document_bytes under the current shared policy; it is
    /// not a separately configurable response-header limit.
    pub fn header_bytes(&self) -> usize {
        self.header_bytes
    }
}

/// Response metadata and finite body delivered by the host.
#[derive(Clone)]
#[non_exhaustive]
pub struct ResponseInput {
    /// HTTP status. Zero is permitted only to describe an opaque redirect refusal.
    pub status: u16,
    /// Delivered entries in host delivery order; interpreted through header_fidelity.
    pub headers: Vec<Header>,
    /// Bytes delivered so far. Core retains at most its configured limit.
    pub bytes: Arc<[u8]>,
    /// Whether these bytes end at representation completion.
    pub body_state: BodyState,
    /// Explicit body-source and interpretation evidence.
    pub provenance: Provenance,
    /// A browser returned an opaque redirect instead of inspectable response metadata.
    pub opaque_redirect: bool,
    /// Host declaration about delivered structured fields, independent of body bytes.
    pub header_fidelity: HeaderFidelity,
    /// Completion of adapter enumeration of host-exposed entries. Core omissions
    /// are counted separately and never change this supplied declaration.
    pub header_delivery: HeaderDelivery,
}
impl ResponseInput {
    /// Construct evidence with Unknown fidelity, Unspecified header delivery and
    /// no opaque redirect. These defaults preserve the supplied-host policy;
    /// they do not assert that metadata was completely enumerated.
    pub fn new(
        status: u16,
        headers: Vec<Header>,
        bytes: Arc<[u8]>,
        body_state: BodyState,
        provenance: Provenance,
    ) -> Self {
        Self {
            status,
            headers,
            bytes,
            body_state,
            provenance,
            opaque_redirect: false,
            header_fidelity: HeaderFidelity::Unknown,
            header_delivery: HeaderDelivery::Unspecified,
        }
    }
}
impl fmt::Debug for ResponseInput {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("ResponseInput")
            .field("status", &self.status)
            .field("headers", &self.headers)
            .field("bytes", &self.bytes.len())
            .field("body_state", &self.body_state)
            .field("provenance", &self.provenance)
            .field("opaque_redirect", &self.opaque_redirect)
            .field("header_fidelity", &self.header_fidelity)
            .field("header_delivery", &self.header_delivery)
            .finish()
    }
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
    /// Retained delivered entries in host delivery order; see header_fidelity().
    pub fn headers(&self) -> &[Header] {
        &self.input.headers
    }
    /// Supplied entries omitted by core metadata bounds; the historical name does
    /// not imply raw field lines. Zero does not rule out host filtering. JSON decode
    /// refuses incomplete retained metadata, including with ExactFields fidelity.
    pub fn omitted_header_lines(&self) -> usize {
        self.omitted_header_lines
    }
    /// Host-supplied structured-field declaration, unchanged by core omission.
    pub fn header_fidelity(&self) -> HeaderFidelity {
        self.input.header_fidelity
    }

    /// The adapter's original enumeration declaration, unchanged by core omission.
    /// Complete means all host-exposed entries were supplied, not all wire fields.
    /// Inspect omitted_header_lines separately for entries discarded by core.
    pub fn header_delivery(&self) -> HeaderDelivery {
        self.input.header_delivery
    }
    /// Bounded bytes delivered by the host; provenance is reported separately.
    pub fn raw(&self) -> &[u8] {
        &self.input.bytes
    }
    /// Body completion evidence.
    pub fn body_state(&self) -> BodyState {
        self.input.body_state
    }
    /// Declared source/interpretation; raw never silently means original wire bytes.
    pub fn provenance(&self) -> Provenance {
        self.input.provenance
    }
    /// Validate delivered JSON into a new, independent exact owner on each call.
    /// All admission checks finish here; dynamic access may materialize its index later.
    /// Failure leaves this response and raw bytes available.
    pub fn json(&self) -> Result<ExactJson, Diagnostic> {
        if let Some(e) = &self.validation {
            return Err(e.clone());
        }
        if self.header_delivery() == HeaderDelivery::Incomplete || self.omitted_header_lines > 0 {
            return Err(Diagnostic::new(Code::IncompleteHeaders));
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
        if matches!(self.provenance(), Provenance::Wire | Provenance::Unknown) {
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
        ExactJson::parse_response(self.raw(), limits).map_err(|e| e.diagnostic)
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
    replay_assurance: Option<ReplayAssurance>,
}
impl fmt::Debug for Outcome {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Outcome")
            .field("dispatch", &self.dispatch)
            .field("upload", &self.upload)
            .field("response", &self.response)
            .field("error", &self.error)
            .field("cancelled", &self.cancelled)
            .field("replay_assurance", &self.replay_assurance)
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
            replay_assurance: None,
        }
    }
    /// Replay declaration recorded at callback entry. None on every core or adapter
    /// refusal before entry. This is not server receipt or attempt-count evidence;
    /// later mutation of public outcome fields does not rewrite this historical record.
    /// ```compile_fail
    /// use dynamic_openapi_client::{Code, Diagnostic, Outcome, ReplayAssurance};
    /// let mut out = Outcome::not_dispatched(Diagnostic::new(Code::HostCapability), false, None);
    /// out.replay_assurance = Some(ReplayAssurance::NoReplayDeclared);
    /// ```
    pub fn replay_assurance(&self) -> Option<ReplayAssurance> {
        self.replay_assurance
    }
    /// Explicit access to untrusted caller-owned error detail; never safe to log by default.
    pub fn host_error_detail(&self) -> Option<&str> {
        self.detail.as_deref()
    }
}
impl PreparedRequest {
    /// Immutable response admission bounds derived from this request's Limits.
    ///
    /// ```compile_fail
    /// use dynamic_openapi_client::PreparedRequest;
    /// fn old_bound(request: PreparedRequest) -> usize { request.response_limit }
    /// ```
    /// ```compile_fail
    /// use dynamic_openapi_client::{PreparedRequest, Method};
    /// let request = PreparedRequest {
    ///     method: Method::Get, target: "https://example.test/".into(),
    ///     headers: vec![], body: None, response_limit: 64,
    /// };
    /// ```
    pub fn response_admission(&self) -> ResponseAdmission {
        ResponseAdmission {
            body_bytes: self.limits.body_bytes,
            header_entries: self.limits.headers,
            header_bytes: self.limits.document_bytes,
        }
    }

    /// Invoke once through a supplied future-producing callback, without a universal Send bound.
    /// Dropping this future does not establish remote cancellation or upload completion.
    /// The callback receives a clone sharing the body and retaining the operation
    /// and source graph; saving it extends their lifetime beyond this invocation.
    pub async fn invoke<F, Fut>(
        &self,
        cancellation: Cancellation,
        host: &HostCapabilities,
        dispatch: F,
    ) -> Outcome
    where
        F: FnOnce(PreparedRequest, Cancellation) -> Fut,
        Fut: Future<Output = TransportResult>,
    {
        self.invoke_with_replay(cancellation, host, ReplayPolicy::RequireNoReplay, dispatch)
            .await
    }
    /// Invoke the callback once under the caller's explicit replay policy.
    /// Host-managed permission does not change bytes or other admission rules.
    /// The callback may itself use a host that retransmits; its entry is not an attempt count.
    /// As with invoke, retaining the callback request also retains its source graph.
    pub async fn invoke_with_replay<F, Fut>(
        &self,
        cancellation: Cancellation,
        host: &HostCapabilities,
        replay: ReplayPolicy,
        dispatch: F,
    ) -> Outcome
    where
        F: FnOnce(PreparedRequest, Cancellation) -> Fut,
        Fut: Future<Output = TransportResult>,
    {
        let preflight = cancellation
            .check()
            .and_then(|_| host.admit_with_replay(self, replay));
        let replay_assurance = match preflight {
            Ok(assurance) => assurance,
            Err(error) => {
                return Outcome {
                    dispatch: DispatchEvidence::NotDispatched,
                    upload: UploadState::NotStarted,
                    response: None,
                    error: Some(error),
                    cancelled: cancellation.is_cancelled(),
                    detail: None,
                    replay_assurance: None,
                };
            }
        };
        let result = dispatch(self.clone(), cancellation.clone()).await;
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
            if input.opaque_redirect {
                validation = Some(Diagnostic::new(Code::OpaqueRedirect));
            } else if !(100..=599).contains(&input.status)
                || input.headers.len() > self.limits.headers
                || input.headers.iter().any(|h| h.validate().is_err())
                || (input.header_fidelity == HeaderFidelity::ExactFields
                    && input.header_delivery == HeaderDelivery::Incomplete)
            {
                validation = Some(Diagnostic::new(Code::InvalidHostEvidence));
            }
            if input.bytes.len() > self.limits.body_bytes {
                input.bytes = Arc::from(&input.bytes[..self.limits.body_bytes]);
                input.body_state = BodyState::Truncated;
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
            replay_assurance: Some(replay_assurance),
        }
    }
}

#[cfg(test)]
mod host_delivered_tests {
    use super::*;

    #[test]
    fn saved_callback_request_shares_body_and_retains_graph_until_released() {
        use std::task::{Context, Poll, Waker};
        let source = format!(
            r#"{{"openapi":"3.1.2","x-padding":"{}","servers":[{{"url":"https://api.example/SECRET-TARGET"}}],"paths":{{"/send":{{"post":{{"operationId":"send","requestBody":{{"content":{{"application/octet-stream":{{}}}}}}}}}}}}}}"#,
            "x".repeat(1024 * 1024),
        );
        let document = crate::Document::parse(source, None, Limits::default()).unwrap();
        let graph = Arc::downgrade(&document.0);
        let retained = document.retained_bytes();
        let payload: Arc<[u8]> = b"SECRET-BODY".as_slice().into();
        let request = document
            .operation_id("send")
            .unwrap()
            .request()
            .body(crate::Body::Raw(payload.clone()))
            .header(Header::new("SECRET-NAME", "SECRET-VALUE"))
            .prepare()
            .unwrap();
        let compiled = Arc::downgrade(&request.compiled);
        let mut saved = None;
        let host = HostCapabilities::programmable();
        let future = request.invoke(Cancellation::default(), &host, |received, _| {
            assert!(Arc::ptr_eq(
                request.body().unwrap(),
                received.body().unwrap()
            ));
            assert!(Arc::ptr_eq(&payload, received.body().unwrap()));
            assert!(!format!("{received:?} {received:#?}").contains("SECRET"));
            saved = Some(received);
            std::future::ready(TransportResult {
                response: None,
                upload: UploadState::Unknown,
                error_detail: Some("synthetic failure".into()),
            })
        });
        assert!(matches!(
            Box::pin(future)
                .as_mut()
                .poll(&mut Context::from_waker(Waker::noop())),
            Poll::Ready(_)
        ));
        drop(document);
        drop(request);
        assert!(graph.upgrade().is_some());
        assert!(compiled.upgrade().is_some());
        assert_eq!(
            saved.as_ref().unwrap().operation().id().as_deref(),
            Some("send")
        );
        println!("saved prepared request retains {retained} accounted document bytes");
        drop(saved);
        assert!(graph.upgrade().is_none());
        assert!(compiled.upgrade().is_none());
        assert_eq!(Arc::strong_count(&payload), 1);
        assert_eq!(payload.as_ref(), b"SECRET-BODY");
    }

    // Frozen BDS-35 is a private response boundary: depth=1 cannot admit the
    // operation-bearing source needed to construct a public PreparedRequest.
    #[test]
    fn host_delivered_preserves_literal_depth_guard() {
        let mut input = ResponseInput::new(
            200,
            vec![Header::new("content-type", "application/json")],
            b"[[0]]".as_slice().into(),
            BodyState::Complete,
            Provenance::HostDelivered,
        );
        input.header_delivery = HeaderDelivery::Complete;
        input.header_fidelity = HeaderFidelity::HostProcessed;
        let response = Response {
            input,
            limits: Limits {
                body_bytes: 64,
                headers: 16,
                document_bytes: 1024,
                depth: 1,
                nodes: 128,
                ..Limits::default()
            },
            validation: None,
            omitted_header_lines: 0,
        };
        let error = response.json().unwrap_err();
        assert_eq!(error.code(), Code::Limit);
        assert!(matches!(
            error.reason(),
            Some(crate::DiagnosticReason::Limit {
                kind: crate::LimitKind::Depth,
                ..
            })
        ));
    }
}
