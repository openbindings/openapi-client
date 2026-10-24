use crate::ParameterLocation;
use crate::source::SourceContext;
use std::fmt;

/// Structured YAML refusal, without retaining scalar values in error messages.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum YamlReason {
    /// Exactly one document is required in a YAML stream.
    MultipleDocuments,
    /// An explicit YAML directive selected another version.
    UnsupportedVersion,
    /// OpenAPI mapping keys must resolve to strings; keys are never stringified.
    NonStringKey,
    /// A tag has no supported JSON-compatible interpretation.
    UnsupportedTag,
    /// The scalar does not conform to its explicit standard tag.
    InvalidTaggedScalar,
    /// JSON cannot represent YAML infinity or NaN values.
    NonFiniteNumber,
    /// Alias expansion would revisit an active definition.
    AliasCycle,
    /// An alias refers to no admitted definition.
    UnknownAlias,
}

/// The configured resource whose admission check failed.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
#[allow(missing_docs)]
pub enum LimitKind {
    GraphResources,
    GraphOriginalBytes,
    GraphRetainedBytes,
    GraphNodes,
    GraphReferenceEdges,
    GraphIdentities,
    GraphLayouts,
    SchemaFactWork,
    SchemaFactDepth,
    GraphMetadataBytes,
    ReferenceDepth,
    DocumentBytes,
    NormalizedBytes,
    AliasSteps,
    /// Copied descriptor string bytes, bounded separately by Limits::document_bytes.
    DescriptorBytes,
    /// Descriptor entries and copied strings, bounded separately by Limits::nodes.
    DescriptorNodes,
    /// Aggregate descriptor declarations/reference steps, bounded by Limits::reference_steps.
    DescriptorWork,
    Depth,
    Nodes,
    TargetBytes,
    HeaderCount,
    InputCount,
    InputBytes,
    BodyBytes,
    FormFields,
    FormParts,
    FormMetadataBytes,
    FormWork,
    BoundaryAttempts,
    NumberCharacters,
    /// Redirects followed in one chain, bounded by Limits::redirects.
    Redirects,
}
/// Requested checked numeric representation.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum NumericTarget {
    /// Signed 64-bit integer.
    I64,
    /// Unsigned 64-bit integer.
    U64,
    /// Exactly represented binary64, restricted to integer lexemes fitting i64.
    F64Exact,
}
/// Why a checked numerical convenience refused an exact token.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum NumericReason {
    /// A decimal point or exponent requires mathematics outside this lexical convenience.
    UnsupportedLexicalForm,
    /// The integer does not fit i64; this also bounds F64Exact by intentional policy.
    SignedRange,
    /// The integer is negative or does not fit u64.
    UnsignedRange,
    /// The admitted i64 integer would round in binary64.
    PrecisionLoss,
}
/// Why checked Serde construction refused an application value.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum SerializationReason {
    /// JSON cannot represent NaN or infinity.
    NonFiniteNumber,
    /// An object key did not serialize as a string.
    NonStringKey,
    /// A private raw-token representation requires the exact parsing path.
    UnsupportedRepresentation,
    /// A serializer emitted an invalid compound or private-number protocol.
    InvalidRepresentation,
    /// A Serialize implementation (including derive adapters) or string formatter
    /// returned an error; its text is not retained.
    Custom,
}
/// Caller election field, without the elected value.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SelectionKind {
    /// Index in the effective server list.
    Server,
    /// A server-variable override.
    ServerVariable,
    /// Concrete request media.
    Media,
    /// Index in the effective security alternatives.
    Security,
}
/// Why the document's edition cannot be interpreted.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum VersionReason {
    /// Neither discriminator is present.
    Missing,
    /// Both Swagger and OpenAPI discriminators are present.
    Conflicting,
    /// The discriminator is not a valid supported version spelling.
    Malformed,
    /// The declared major or minor family is not supported.
    UnsupportedFamily,
}
/// Controlled reason a supplied host cannot admit the request.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum HostRefusal {
    /// The host may follow redirects implicitly.
    Redirects,
    /// The host may supply ambient authentication or cookies.
    AmbientAuth,
    /// The host cannot declare exact target preservation.
    ExactTargets,
    /// The URL stack cannot preserve this encoded dot segment.
    EncodedDotSegment,
    /// The host cannot faithfully supply a request header.
    ForbiddenHeader,
    /// Strict admission requires a no-replay declaration.
    Replay,
    /// The selected host's required execution APIs are unavailable.
    Unavailable,
    /// Native client construction failed; check explicit trust roots and host TLS availability.
    Configuration,
    /// The host cannot represent or execute this request method.
    Method,
    /// The host prohibits the supplied body for this request method.
    RequestBody,
}
impl From<HostRefusal> for Diagnostic {
    fn from(refusal: HostRefusal) -> Self {
        Self::new(Code::HostCapability).reasoned(DiagnosticReason::Host(refusal))
    }
}
/// Controlled reason the redirect step cannot derive the next request.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum RedirectRefusal {
    /// The Location field is repeated, is not a URI reference, or does not
    /// resolve to an absolute http or https URI with a host and no userinfo.
    Location,
    /// The redirect leaves https for http.
    Downgrade,
    /// Header delivery is not known complete, so a Location field could be missing.
    HeaderDelivery,
}
/// Corrective information with privacy-safe default formatting.
/// Retained source evidence is available only through explicit accessors.
#[derive(Clone, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum DiagnosticReason {
    /// Different declarations claim one identity. The diagnostic's primary site
    /// remains the later declaration; this position owns the earlier source.
    IdentityCollision {
        /// Earlier claimant, in authored source when available.
        previous: crate::source::SourcePosition,
    },
    /// A controlled host capability or replay policy refusal.
    Host(HostRefusal),
    /// A controlled refusal of the redirect step.
    Redirect(RedirectRefusal),
    /// Selected schema interpretation or bounded fact refusal.
    Schema(crate::schema::SchemaReason),
    /// Why a finite named form could not be represented.
    Form(crate::form::FormReason),
    /// The declared style/type/explode combination has no supported defined serialization.
    UnsupportedStyleCombination,
    /// A nested or null value requires a separately selected serialization convention.
    UnsupportedStyleValue,
    /// A data delimiter needs an explicit interoperability convention.
    AmbiguousStyleDelimiter,
    /// YAML syntax-to-JSON profile refusal.
    Yaml(crate::diagnostics::YamlReason),
    /// Edition discrimination failed without rewriting the authored source.
    Version(VersionReason),
    /// Checked Serde construction refused the emitted representation.
    Serialization(SerializationReason),
    /// A resource exceeds its configured maximum. Actual can be a lower bound when admission stopped early.
    Limit {
        /// Resource dimension being limited.
        kind: LimitKind,
        /// Configured maximum.
        maximum: usize,
        /// Observed amount, or a lower bound when admission stopped early.
        actual: usize,
    },
    /// A checked conversion refused an exact token under the documented policy.
    Numeric {
        /// Requested output representation.
        target: NumericTarget,
        /// Intentional numerical policy that refused the input.
        reason: NumericReason,
    },
    /// No declared parameter matches this caller entry.
    UnknownParameter,
    /// A security requirement names a scheme absent from the edition's definitions.
    /// The source location identifies the requirement entry, including its name.
    UndefinedSecurityScheme,
    /// A direct `$ref` appears where the OpenAPI object does not admit a reference.
    /// The original value remains inspectable; preparation cannot ignore it.
    UnsupportedReferencePosition,
    /// Multiple caller entries target the same declared parameter.
    DuplicateParameter,
    /// Two explicit builder events assigned the same field or key.
    DuplicateAssignment {
        /// Zero-based first assignment event.
        first_assignment: usize,
        /// Zero-based repeated assignment event.
        repeated_assignment: usize,
    },
    /// The caller must correct or supply the named election; Code distinguishes invalid/ambiguous forms.
    Selection(SelectionKind),
}
/// Explicit caller context. Names/keys are untrusted access data and omitted by Debug.
#[derive(Clone, PartialEq, Eq)]
#[non_exhaustive]
pub enum DiagnosticContext {
    /// Logical form field and optional repeated item. Names are explicit-access data.
    FormField {
        /// Zero-based logical field index.
        index: usize,
        /// Logical field name; omitted by safe Debug.
        name: String,
        /// Zero-based repeated value index, when applicable.
        item: Option<usize>,
    },
    /// Caller parameter vector entry, independent of its exact value's source owner.
    Parameter {
        /// Index in the caller parameter vector.
        index: usize,
        /// Parameter namespace.
        location: ParameterLocation,
        /// Explicit-access untrusted parameter name; omitted by Debug.
        name: String,
    },
    /// Caller election and optional index/key. No elected value is retained.
    Selection {
        /// Election field.
        kind: SelectionKind,
        /// Elected server/security index, when supplied.
        index: Option<usize>,
        /// Explicit-access untrusted variable key, when applicable.
        key: Option<String>,
    },
    /// Explicit finite body assignment.
    Body,
    /// Caller credential scheme; the name is explicit-access untrusted data.
    Credential {
        /// Declared scheme name, omitted from default formatting.
        scheme: String,
    },
    /// Derived request target or final header collection.
    PreparedRequest,
    /// Aggregate caller data admission.
    CallerInput,
    /// Ordinary-value or checked Serde construction before a complete JSON source exists.
    Construction,
}
impl fmt::Debug for DiagnosticContext {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Parameter {
                index, location, ..
            } => f
                .debug_struct("Parameter")
                .field("index", index)
                .field("location", location)
                .finish_non_exhaustive(),
            Self::Selection { kind, index, .. } => f
                .debug_struct("Selection")
                .field("kind", kind)
                .field("index", index)
                .finish_non_exhaustive(),
            Self::Body => f.write_str("Body"),
            Self::FormField { index, item, .. } => f
                .debug_struct("FormField")
                .field("index", index)
                .field("item", item)
                .finish_non_exhaustive(),
            Self::Credential { .. } => f.write_str("Credential { .. }"),
            Self::PreparedRequest => f.write_str("PreparedRequest"),
            Self::CallerInput => f.write_str("CallerInput"),
            Self::Construction => f.write_str("Construction"),
        }
    }
}
#[derive(Clone, Default, PartialEq, Eq)]
struct Details {
    http_status: Option<u16>,
    bytes: std::sync::Arc<[u8]>,
    projection: Option<Projection>,

    reason: Option<DiagnosticReason>,
    context: Option<DiagnosticContext>,
    source: Option<SourceContext>,
    primary_source: Option<std::sync::Arc<str>>,
    context_omitted_for_limit: bool,
    need_fact: Option<crate::graph::NeedFact>,
    resource_need: Option<crate::graph::ResourceNeed>,
}
#[derive(Clone)]
struct Projection(std::sync::Arc<serde_json::Error>);
impl PartialEq for Projection {
    fn eq(&self, other: &Self) -> bool {
        self.0.classify() == other.0.classify()
            && self.0.line() == other.0.line()
            && self.0.column() == other.0.column()
            && self.0.to_string() == other.0.to_string()
    }
}
impl Eq for Projection {}

/// Stable failure categories. Diagnostic text never includes input values.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum Code {
    /// Selected schema semantics require a dialect capability outside this profile.
    UnsupportedSchema,
    /// Referenced resource bytes have not been supplied to this immutable snapshot.
    ResourceUnavailable,
    /// A non-root reference into an untyped resource needs an explicit layout.
    ResourceLayoutRequired,
    /// Two different sources claim the same resource identity.
    IdentityCollision,
    /// A source node is requested under incompatible object interpretations.
    ConflictingObjectContext,
    /// A protocol reference crosses an unsupported feature-family boundary.
    ReferencedEditionMismatch,
    /// Unknown mounted paths prevent a complete or unique operation-ID lookup.
    IncompleteDiscovery,
    /// A handle/need belongs to a different immutable snapshot.
    SnapshotMismatch,
    /// An authored or supplied resource URI violates the strict admission policy.
    InvalidResourceUri,
    /// A relative resource reference has no actual supplied or authored base.
    MissingResourceBase,
    /// Checked Serde construction refused; inspect its Serialization reason.
    Serialization,
    /// Input bytes are not valid UTF-8; the range marks the decoding failure.
    InvalidUtf8,
    /// Input is not valid JSON under the supported grammar.
    InvalidJson,
    /// Input is not syntactically valid YAML.
    InvalidYaml,
    /// YAML uses a representation outside the admitted JSON-compatible profile.
    UnsupportedYaml,
    /// A JSON string cannot be represented under the Unicode-scalar policy.
    UnsupportedString,
    /// Two object names decode to the same key; related contains the previous position.
    DuplicateMember,
    /// A configured resource budget refused the requested work.
    Limit,
    /// A checked numeric convenience refused; inspect reason() for its policy reason.
    NumericConversion,
    /// The supplied JSON Pointer or its token escaping is malformed.
    InvalidPointer,
    /// A well-formed local reference/pointer has no target.
    MissingReference,
    /// A protocol reference declaration is malformed.
    InvalidReference,
    /// A local protocol target has the wrong declaration kind.
    WrongReferenceKind,
    /// The supported protocol reference walk repeats a target.
    ReferenceCycle,
    /// Preparation requires another resource, outside this local-only profile.
    ExternalReference,
    /// Path Item reference fields conflict under the explicit ambiguity policy.
    AmbiguousReference,
    /// The authored version remains inspectable but this profile cannot prepare it.
    UnsupportedVersion,
    /// A needed protocol declaration has invalid structure or required fields.
    InvalidDeclaration,
    /// An operation ID identifies multiple operations.
    AmbiguousOperation,
    /// No operation matches the requested ID or path/method.
    MissingOperation,
    /// A relative server needs an explicit caller document base.
    MissingBase,
    /// The resolved server/target is not an admitted HTTP(S) destination.
    InvalidDestination,
    /// Multiple usable choices require an explicit caller election.
    AmbiguousSelection,
    /// A caller election/input does not match the supported declarations.
    InvalidSelection,
    /// A needed parameter location/style/encoding is outside this profile.
    UnsupportedParameter,
    /// A required parameter value or template substitution is absent.
    MissingInput,
    /// A supplied value shape cannot be serialized by the selected parameter profile.
    UnsupportedValue,
    /// A final header name or value violates the owned header policy.
    InvalidHeader,
    /// A query name/value cannot be represented under the admitted query policy.
    InvalidQuery,
    /// A required request body is absent.
    MissingBody,
    /// A media declaration/election or delivered Content-Type is malformed.
    InvalidMedia,
    /// The selected convenience codec does not support this media.
    UnsupportedMedia,
    /// A chosen security requirement lacks caller-supplied credential material.
    MissingCredential,
    /// Credential admission does not include the resolved destination origin.
    CredentialOrigin,
    /// A chosen security scheme is outside supplied Basic/Bearer/API-key support.
    UnsupportedSecurity,
    /// Applied credential fields collide with other supplied request fields.
    CredentialCollision,
    /// Cooperative local cancellation was observed; no remote result is implied.
    Cancelled,
    /// The declared host cannot preserve the prepared request.
    HostCapability,
    /// The host returned a redirect it cannot expose as an ordinary finite response.
    OpaqueRedirect,
    /// The callback reported a failure; response evidence may still exist.
    TransportFailure,
    /// Returned metadata violates the finite host contract.
    InvalidHostEvidence,
    /// JSON decoding was requested for an empty delivered body.
    EmptyBody,
    /// JSON decoding was requested for incomplete/truncated body evidence.
    IncompleteBody,
    /// JSON decoding was requested after known response-header delivery omission.
    IncompleteHeaders,
    /// Delivered bytes require content decoding outside this convenience profile.
    UnsupportedCoding,
    /// Received status is outside the selected 2xx acceptance range.
    HttpStatus,
    /// Admitted JSON cannot project into the requested application type.
    Deserialization,
}

impl Code {
    fn message(self) -> &'static str {
        match self {
            Self::UnsupportedSchema => "the selected schema semantics are unsupported",
            Self::ResourceUnavailable => "referenced resource bytes are not available",
            Self::ResourceLayoutRequired => "the referenced resource needs an explicit layout",
            Self::IdentityCollision => "different sources claim the same resource identity",
            Self::ConflictingObjectContext => "the source has conflicting object interpretations",
            Self::ReferencedEditionMismatch => {
                "the protocol target uses a different feature family"
            }
            Self::IncompleteDiscovery => "operation discovery is incomplete",
            Self::SnapshotMismatch => "the handle belongs to another document snapshot",
            Self::InvalidResourceUri => "the resource URI is not admitted",
            Self::MissingResourceBase => "a relative resource reference requires a real base",
            Self::Serialization => "application data could not be serialized as supported JSON",
            Self::InvalidUtf8 => "input is not valid UTF-8",
            Self::InvalidYaml => "input is not valid YAML",
            Self::UnsupportedYaml => "YAML cannot be admitted under the documented profile",
            Self::InvalidJson => "input is not valid JSON",
            Self::UnsupportedString => "the string is outside the supported Unicode profile",
            Self::DuplicateMember => "an object contains duplicate decoded member names",
            Self::Limit => "a configured resource limit was exceeded",
            Self::NumericConversion => {
                "the number cannot be converted under the requested exact policy"
            }
            Self::InvalidPointer => "the JSON pointer is malformed",
            Self::MissingReference => "the referenced value does not exist",
            Self::InvalidReference => "the protocol reference is malformed",
            Self::WrongReferenceKind => "the reference targets the wrong declaration kind",
            Self::ReferenceCycle => "the reference chain contains a cycle",
            Self::ExternalReference => "this profile requires a local reference",
            Self::AmbiguousReference => "the reference and authored fields conflict",
            Self::UnsupportedVersion => {
                "the document edition is missing, conflicting, malformed or unsupported"
            }
            Self::InvalidDeclaration => "a required declaration is invalid",
            Self::AmbiguousOperation => "the operation ID identifies more than one operation",
            Self::MissingOperation => "the requested operation does not exist",
            Self::MissingBase => "a relative server URL requires a document base URL",
            Self::InvalidDestination => "the request target is not an admitted HTTP(S) destination",
            Self::AmbiguousSelection => "more than one choice is available; select one explicitly",
            Self::InvalidSelection => {
                "the selected choice does not match the supported declarations"
            }
            Self::UnsupportedParameter => "the parameter style or location is unsupported",
            Self::MissingInput => "a required parameter or template value is missing",
            Self::UnsupportedValue => {
                "the supplied value cannot be encoded by this parameter profile"
            }
            Self::InvalidHeader => "an HTTP header name or value is invalid",
            Self::InvalidQuery => "the query input cannot be represented by this profile",
            Self::MissingBody => "the operation requires a request body",
            Self::InvalidMedia => "the media declaration or Content-Type is invalid",
            Self::UnsupportedMedia => "the selected body codec does not support this media type",
            Self::MissingCredential => {
                "the selected security requirement needs a supplied credential"
            }
            Self::CredentialOrigin => {
                "the credential is not authorized for this destination origin"
            }
            Self::UnsupportedSecurity => "the selected security scheme is unsupported",
            Self::CredentialCollision => "a credential would overwrite another supplied field",
            Self::Cancelled => "the local operation was cancelled",
            Self::HostCapability => "the HTTP host cannot faithfully send this request",
            Self::OpaqueRedirect => "the HTTP host returned an uninspectable redirect",
            Self::TransportFailure => {
                "the HTTP transport failed; inspect any retained response evidence"
            }
            Self::InvalidHostEvidence => {
                "the HTTP host returned invalid or contradictory response metadata"
            }
            Self::EmptyBody => "JSON decoding requires a nonempty response body",
            Self::IncompleteBody => "JSON decoding requires a complete response body",
            Self::IncompleteHeaders => "JSON decoding requires complete response-header delivery",
            Self::HttpStatus => "HTTP status is outside the accepted 2xx range",
            Self::Deserialization => "JSON does not match the requested Rust type",
            Self::UnsupportedCoding => "the response needs unsupported content decoding",
        }
    }
}

/// Half-open source byte range and JSON Pointer. A diagnostic's source_context()
/// identifies the exact authored, constructed or normalized source owning this range.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Location {
    /// Inclusive UTF-8 byte offset.
    pub start: usize,
    /// Exclusive UTF-8 byte offset.
    pub end: usize,
    /// Semantic JSON Pointer. Treat document-controlled names as untrusted data.
    pub pointer: String,
}

/// Structured, safely displayed diagnostic. No arbitrary host error is displayed.
/// Source metadata is immutable to callers; add application context with a wrapper error.
/// Equality compares all diagnostic data, independently of its source-chain snapshot.
/// ```compile_fail
/// use dynamic_openapi_client::{diagnostics::Code, Diagnostic};
/// let mut error = Diagnostic::new(Code::InvalidJson);
/// error.location = None;
/// ```
#[derive(Clone)]
pub struct Diagnostic {
    /// Stable category.
    pub(crate) code: Code,
    /// Authored position when a document/value supplies the failure.
    pub(crate) location: Option<Location>,
    /// Additional positions, such as the previous duplicate member.
    pub(crate) related: Vec<Location>,
    /// A setting that can repair the failure; never contains a setting value.
    pub(crate) setting: Option<String>,
    details: Option<Box<Details>>,
    // Compatibility with removed parse/request wrappers; not diagnostic data.
    cause: Option<std::sync::Arc<Diagnostic>>,
}
impl PartialEq for Diagnostic {
    fn eq(&self, other: &Self) -> bool {
        self.code == other.code
            && self.location == other.location
            && self.related == other.related
            && self.setting == other.setting
            && self.details == other.details
    }
}
impl Eq for Diagnostic {}
impl Diagnostic {
    /// Construct a category-only diagnostic; source/input details are optional.
    pub fn new(code: Code) -> Self {
        Self {
            code,
            location: None,
            related: vec![],
            setting: None,
            details: None,
            cause: None,
        }
    }
    /// Admitted original bytes, including invalid UTF-8. Empty when byte admission refused.
    /// The source remains immutable and paired with its diagnostic.
    ///
    /// ```compile_fail
    /// use dynamic_openapi_client::{exact::Value, Limits};
    /// let error = Value::parse("{", Limits::default()).unwrap_err();
    /// error.source_bytes()[0] = b'x';
    /// ```
    pub fn source_bytes(&self) -> &[u8] {
        self.details.as_ref().map_or(&[], |details| &details.bytes)
    }
    /// One-based projection error line; zero when unavailable.
    pub fn line(&self) -> usize {
        self.details
            .as_ref()
            .and_then(|d| d.projection.as_ref())
            .map_or(0, |p| p.0.line())
    }
    /// One-based projection error column; zero when unavailable.
    pub fn column(&self) -> usize {
        self.details
            .as_ref()
            .and_then(|d| d.projection.as_ref())
            .map_or(0, |p| p.0.column())
    }
    /// Explicit untrusted projection detail. Never included in safe formatting.
    pub fn detail(&self) -> Option<&serde_json::Error> {
        self.details
            .as_ref()?
            .projection
            .as_ref()
            .map(|p| p.0.as_ref())
    }
    pub(crate) fn with_source_bytes(mut self, bytes: std::sync::Arc<[u8]>) -> Self {
        if !bytes.is_empty() {
            self.details.get_or_insert_with(Default::default).bytes = bytes;
        }
        self.refresh_cause()
    }
    // Only boundaries formerly returning ParseFailure expose a diagnostic cause.
    pub(crate) fn parse_failure(self, bytes: std::sync::Arc<[u8]>) -> Self {
        self.with_source_bytes(bytes).caused()
    }
    // Preserve the safe structured cause formerly supplied by the parse/request
    // wrappers. The cause is another Diagnostic, never the untrusted Serde error.
    // Refresh a single terminal snapshot rather than building wrapper chains.
    pub(crate) fn caused(mut self) -> Self {
        let mut cause = self.clone();
        cause.cause = None;
        self.cause = Some(std::sync::Arc::new(cause));
        self
    }
    fn refresh_cause(self) -> Self {
        if self.cause.is_some() {
            self.caused()
        } else {
            self
        }
    }
    pub(crate) fn http_status(status: u16) -> Self {
        let mut diagnostic = Self::new(Code::HttpStatus);
        diagnostic
            .details
            .get_or_insert_with(Default::default)
            .http_status = Some(status);
        diagnostic
    }
    pub(crate) fn projection(error: serde_json::Error) -> Self {
        let mut diagnostic = Self::new(Code::Deserialization);
        diagnostic
            .details
            .get_or_insert_with(Default::default)
            .projection = Some(Projection(std::sync::Arc::new(error)));
        diagnostic
    }
    /// Stable failure category.
    pub fn code(&self) -> Code {
        self.code
    }
    /// Exact source range when available; source_context() identifies its source.
    /// source_position() separately reports whether those bytes are retained.
    pub fn location(&self) -> Option<&Location> {
        self.location.as_ref()
    }
    /// Format a human-facing diagnostic including the JSON pointer and caller
    /// name when available. These names come from untrusted documents or inputs
    /// and can be sensitive; default Display/Debug deliberately omit them.
    /// Each displayed name/pointer is escaped and limited to 256 characters.
    /// The full values remain available through location() and context().
    pub fn display_with_location(&self) -> impl fmt::Display + '_ {
        LocatedDisplay(self)
    }
    /// Related ranges within the diagnostic's source owner.
    pub fn related(&self) -> &[Location] {
        &self.related
    }
    /// Corrective setting name, never a setting value.
    pub fn setting(&self) -> Option<&str> {
        self.setting.as_deref()
    }
    pub(crate) fn at(mut self, location: Location) -> Self {
        if let Some(details) = &mut self.details {
            details.primary_source = None;
        }
        self.location = Some(location);
        self.refresh_cause()
    }
    pub(crate) fn with_setting(mut self, key: impl Into<String>) -> Self {
        self.setting = Some(key.into());
        self.refresh_cause()
    }
    /// Typed corrective reason, without parsing Display text.
    pub fn reason(&self) -> Option<&DiagnosticReason> {
        self.details.as_ref()?.reason.as_ref()
    }
    /// Caller or derived-output context. Explicit names/keys are untrusted data.
    pub fn context(&self) -> Option<&DiagnosticContext> {
        self.details.as_ref()?.context.as_ref()
    }
    /// Process-local source identity and origin owning location and related ranges.
    /// None means no exact source owner was associated; do not assume the OpenAPI document.
    /// This identity alone does not retain bytes; source_position() reports that separately.
    pub fn source_context(&self) -> Option<SourceContext> {
        self.details.as_ref()?.source
    }
    /// Retain an independently owning position for a value-located diagnostic.
    /// The returned position shares the complete original source text and copies
    /// its pointer; it does not retain the value index or document graph.
    /// It remains usable after this diagnostic and its document/input are dropped.
    /// Parser or unlocated diagnostics may return None; Diagnostic::source_bytes
    /// remains the accessor for admitted parse-failure bytes, including invalid UTF-8.
    pub fn source_position(&self) -> Option<crate::source::SourcePosition> {
        let details = self.details.as_ref()?;
        let source = details.primary_source.as_ref()?;
        Some(crate::source::SourcePosition {
            context: details.source?,
            location: self.location.as_ref()?.clone(),
            source: source.clone(),
        })
    }
    /// Typed corrective resource need, consistently exposed by direct/fluent preparation
    /// and effective inspection. The need owns its originating immutable snapshot.
    pub fn resource_need(&self) -> Option<&crate::graph::ResourceNeed> {
        self.details.as_ref()?.resource_need.as_ref()
    }
    pub(crate) fn need_data(&self) -> Option<&crate::graph::NeedFact> {
        self.details.as_ref()?.need_fact.as_ref()
    }
    pub(crate) fn need_fact(mut self, fact: crate::graph::NeedFact) -> Self {
        self.details.get_or_insert_with(Default::default).need_fact = Some(fact);
        self.refresh_cause()
    }
    pub(crate) fn bind_graph(mut self, store: &std::sync::Arc<crate::document::Store>) -> Self {
        if let Some(details) = &mut self.details
            && let Some(fact) = &details.need_fact
        {
            details.resource_need = Some(crate::graph::ResourceNeed {
                store: store.clone(),
                fact: fact.clone(),
            });
        }
        self.refresh_cause()
    }
    /// Whether corrective context was omitted because its complete name exceeded the bound.
    pub fn context_omitted_for_limit(&self) -> bool {
        self.details
            .as_ref()
            .is_some_and(|d| d.context_omitted_for_limit)
    }
    pub(crate) fn omit_context(mut self) -> Self {
        self.details
            .get_or_insert_with(Default::default)
            .context_omitted_for_limit = true;
        self.refresh_cause()
    }
    pub(crate) fn reasoned(mut self, reason: DiagnosticReason) -> Self {
        self.details.get_or_insert_with(Default::default).reason = Some(reason);
        self.refresh_cause()
    }
    pub(crate) fn identity_collision(previous: &crate::exact::Value) -> Self {
        Self::new(Code::IdentityCollision).reasoned(DiagnosticReason::IdentityCollision {
            previous: previous.diagnostic_position(),
        })
    }
    pub(crate) fn contextual(mut self, context: DiagnosticContext) -> Self {
        self.details.get_or_insert_with(Default::default).context = Some(context);
        self.refresh_cause()
    }
    pub(crate) fn sourced(mut self, source: SourceContext) -> Self {
        let details = self.details.get_or_insert_with(Default::default);
        details.primary_source = None;
        details.source = Some(source);
        self.refresh_cause()
    }
    pub(crate) fn positioned(self, position: crate::source::SourcePosition) -> Self {
        debug_assert!(
            position
                .source
                .get(position.location.start..position.location.end)
                .is_some()
        );
        let mut diagnostic = self.at(position.location).sourced(position.context);
        diagnostic
            .details
            .as_mut()
            .expect("source details")
            .primary_source = Some(position.source);
        diagnostic.refresh_cause()
    }
    pub(crate) fn limited(kind: LimitKind, maximum: usize, actual: usize) -> Self {
        Self::new(Code::Limit).reasoned(DiagnosticReason::Limit {
            kind,
            maximum,
            actual,
        })
    }
    pub(crate) fn election(
        mut self,
        kind: SelectionKind,
        index: Option<usize>,
        key: Option<String>,
    ) -> Self {
        if self.reason().is_none()
            && matches!(
                self.code,
                Code::InvalidSelection | Code::AmbiguousSelection | Code::InvalidMedia
            )
        {
            self = self.reasoned(DiagnosticReason::Selection(kind));
        }
        if self.context().is_none() {
            self = self.contextual(DiagnosticContext::Selection { kind, index, key });
        }
        self
    }
}
impl fmt::Display for Diagnostic {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if matches!(
            self.reason(),
            Some(DiagnosticReason::Host(HostRefusal::Configuration))
        ) {
            f.write_str("native client construction failed; check additional TLS trust certificates and host TLS availability: ")?;
        } else {
            write!(f, "{}: ", self.code.message())?;
        }
        if let Some(status) = self.details.as_ref().and_then(|d| d.http_status) {
            write!(f, "{status} ")?;
        }
        self.fmt_structured(f)
    }
}
impl Diagnostic {
    // Preserve the serializer's associated Error rendering: user Serialize code
    // can embed that formatting into otherwise successful JSON output.
    pub(crate) fn fmt_structured(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{:?}", self.code)?;
        if let Some(reason) = self.reason() {
            write!(f, ": {reason:?}")?;
        }
        if let Some(context) = self.context() {
            write!(f, " ({context:?})")?;
        }
        if let Some(loc) = &self.location {
            if let Some(source) = self.source_context() {
                write!(f, " in {:?} source {}", source.origin, source.id)?;
            }
            write!(f, " at bytes {}..{}", loc.start, loc.end)?;
        }
        Ok(())
    }
}
impl std::error::Error for Diagnostic {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        self.cause.as_deref().map(|cause| cause as _)
    }
}

impl fmt::Debug for Diagnostic {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        fmt::Display::fmt(self, f)
    }
}

struct LocatedDisplay<'a>(&'a Diagnostic);
impl fmt::Display for LocatedDisplay<'_> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        fmt::Display::fmt(self.0, f)?;
        if let Some(location) = self.0.location() {
            f.write_str("; JSON pointer ")?;
            display_name(f, &location.pointer)?;
        }
        match self.0.context() {
            Some(DiagnosticContext::FormField { name, index, item }) => {
                write!(f, "; form field {index} ")?;
                display_name(f, name)?;
                if let Some(item) = item {
                    write!(f, "; item {item}")?;
                }
            }
            Some(DiagnosticContext::Parameter { name, .. }) => {
                f.write_str("; parameter ")?;
                display_name(f, name)?;
            }
            Some(DiagnosticContext::Selection { key: Some(key), .. }) => {
                f.write_str("; variable ")?;
                display_name(f, key)?;
            }
            Some(DiagnosticContext::Credential { scheme }) => {
                f.write_str("; security scheme ")?;
                display_name(f, scheme)?;
            }
            _ => {}
        }
        Ok(())
    }
}
fn display_name(f: &mut fmt::Formatter<'_>, name: &str) -> fmt::Result {
    f.write_str("\"")?;
    let mut chars = name.chars();
    for c in chars.by_ref().take(256) {
        for escaped in c.escape_debug() {
            write!(f, "{escaped}")?;
        }
    }
    if chars.next().is_some() {
        f.write_str("…")?;
    }
    f.write_str("\"")
}
