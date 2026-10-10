use crate::{Location, SourceContext};
use std::{collections::HashMap, fmt, ops::Range, sync::Arc};

/// Serialization detected from admitted document bytes. Callers need not branch on it.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum SourceFormat {
    /// The supplied bytes are JSON.
    Json,
    /// The supplied bytes are YAML under the documented loading profile.
    Yaml,
}

/// A location paired with the exact immutable source that owns its byte offsets.
/// Default formatting omits source text and document-controlled pointers.
#[derive(Clone)]
pub struct SourcePosition {
    pub(crate) context: SourceContext,
    pub(crate) location: Location,
    pub(crate) source: Arc<str>,
}
impl SourcePosition {
    /// Process-local identity and origin of the selected source buffer.
    pub fn context(&self) -> SourceContext {
        self.context
    }
    /// Half-open UTF-8 byte range and semantic JSON Pointer at the use site.
    /// An alias-expanded descendant need not exist literally at this pointer in YAML.
    pub fn location(&self) -> &Location {
        &self.location
    }
    /// Entire original or generated source buffer, as identified by context().
    pub fn source(&self) -> &str {
        &self.source
    }
    /// Exact source text covered by this position, including YAML scalar notation.
    pub fn raw(&self) -> &str {
        &self.source[self.location.start..self.location.end]
    }
}
impl fmt::Debug for SourcePosition {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("SourcePosition")
            .field("context", &self.context)
            .field("range", &(self.location.start..self.location.end))
            .finish_non_exhaustive()
    }
}

/// Provenance of a dynamic value, keeping generated JSON and authored text distinct.
#[derive(Clone, Debug)]
pub struct SourceTrace {
    /// Indexed JSON representation owning Value::raw() and Value::location().
    pub representation: SourcePosition,
    /// Original authored value/definition; absent for caller-constructed values.
    /// JSON shares its existing source owner. YAML retains the original YAML source.
    pub authored: Option<SourcePosition>,
    /// YAML alias use sites, outermost first. Empty for values not reached through an alias.
    pub alias_uses: Vec<SourcePosition>,
}

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

pub(crate) struct AuthoredMap {
    pub source: Arc<str>,
    pub context: SourceContext,
    pub entries: Vec<AuthoredEntry>,
}
#[derive(Clone)]
pub(crate) struct AuthoredEntry {
    pub span: Range<usize>,
    pub aliases: Arc<[Range<usize>]>,
}
pub(crate) type PendingMap = HashMap<usize, AuthoredEntry>;
