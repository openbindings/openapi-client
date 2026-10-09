use crate::{ParameterLocation, SourceContext};
use std::fmt;

/// The configured resource whose admission check failed.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
#[allow(missing_docs)]
pub enum LimitKind {
    DocumentBytes,
    Depth,
    Nodes,
    TargetBytes,
    HeaderCount,
    InputCount,
    InputBytes,
    BodyBytes,
    NumberCharacters,
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
/// Safe corrective information; no input values are stored in reasons.
#[derive(Clone, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum DiagnosticReason {
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
    /// Multiple caller entries target the same declared parameter.
    DuplicateParameter,
    /// The caller must correct or supply the named election; Code distinguishes invalid/ambiguous forms.
    Selection(SelectionKind),
}
/// Explicit caller context. Names/keys are untrusted access data and omitted by Debug.
#[derive(Clone, PartialEq, Eq)]
#[non_exhaustive]
pub enum DiagnosticContext {
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
    /// Derived request target or final header collection.
    PreparedRequest,
    /// Aggregate caller data admission.
    CallerInput,
    /// Typed ordinary-value construction before a complete JSON source exists.
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
            Self::PreparedRequest => f.write_str("PreparedRequest"),
            Self::CallerInput => f.write_str("CallerInput"),
            Self::Construction => f.write_str("Construction"),
        }
    }
}
#[derive(Clone, Default, PartialEq, Eq)]
struct Details {
    reason: Option<DiagnosticReason>,
    context: Option<DiagnosticContext>,
    source: Option<SourceContext>,
}

/// Stable failure categories. Diagnostic text never includes input values.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum Code {
    /// Input bytes are not valid UTF-8; the range marks the decoding failure.
    InvalidUtf8,
    /// Input is not valid JSON under the supported grammar.
    InvalidJson,
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
    /// Delivered bytes require content decoding outside this convenience profile.
    UnsupportedCoding,
}

/// Half-open source byte range and JSON Pointer. A diagnostic's source_context()
/// identifies the exact authored or constructed source owning this range.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Location {
    /// Inclusive UTF-8 byte offset.
    pub start: usize,
    /// Exclusive UTF-8 byte offset.
    pub end: usize,
    /// Authored JSON Pointer. Treat document-controlled names as untrusted data.
    pub pointer: String,
}

/// Structured, safely displayed diagnostic. No arbitrary host error is displayed.
#[derive(Clone, PartialEq, Eq)]
pub struct Diagnostic {
    /// Stable category.
    pub code: Code,
    /// Authored position when a document/value supplies the failure.
    pub location: Option<Location>,
    /// Additional positions, such as the previous duplicate member.
    pub related: Vec<Location>,
    /// A setting that can repair the failure; never contains a setting value.
    pub setting: Option<String>,
    details: Option<Box<Details>>,
}
impl Diagnostic {
    /// Construct a category-only diagnostic; source/input details are optional.
    pub fn new(code: Code) -> Self {
        Self {
            code,
            location: None,
            related: vec![],
            setting: None,
            details: None,
        }
    }
    pub(crate) fn at(mut self, location: Location) -> Self {
        self.location = Some(location);
        self
    }
    pub(crate) fn setting(mut self, key: impl Into<String>) -> Self {
        self.setting = Some(key.into());
        self
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
    pub fn source_context(&self) -> Option<SourceContext> {
        self.details.as_ref()?.source
    }
    pub(crate) fn reasoned(mut self, reason: DiagnosticReason) -> Self {
        self.details.get_or_insert_with(Default::default).reason = Some(reason);
        self
    }
    pub(crate) fn contextual(mut self, context: DiagnosticContext) -> Self {
        self.details.get_or_insert_with(Default::default).context = Some(context);
        self
    }
    pub(crate) fn sourced(mut self, source: SourceContext) -> Self {
        self.details.get_or_insert_with(Default::default).source = Some(source);
        self
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
impl std::error::Error for Diagnostic {}

impl fmt::Debug for Diagnostic {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        fmt::Display::fmt(self, f)
    }
}
