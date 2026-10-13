use crate::{
    Code, Diagnostic, Edition, Header, Limits, Method, Operation, ReferenceKind, Value, ValueKind,
};
use crate::{DiagnosticContext, DiagnosticReason, LimitKind, SelectionKind, uri};
use base64::Engine;
use std::{
    collections::{BTreeMap, HashMap, HashSet, hash_map::Entry},
    sync::{Arc, OnceLock, atomic::Ordering},
};

/// Parameter namespace, independent from its name.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
#[allow(missing_docs)]
pub enum ParameterLocation {
    Path,
    Query,
    Header,
    Cookie,
}
impl ParameterLocation {
    /// Parse the edition's location spelling.
    pub fn parse(s: &str) -> Option<Self> {
        Some(match s {
            "path" => Self::Path,
            "query" => Self::Query,
            "header" => Self::Header,
            "cookie" => Self::Cookie,
            _ => return None,
        })
    }
}
/// An exact dynamic parameter value.
#[derive(Clone, Debug)]
pub struct ParameterInput {
    /// Parameter namespace.
    pub location: ParameterLocation,
    /// Authored name; header lookup is case-insensitive.
    pub name: String,
    /// Owned scalar/array/flat-object value.
    pub value: Value,
}
/// Finite request body with absence distinct from JSON null and empty raw bytes.
#[derive(Clone, Default)]
#[non_exhaustive]
pub enum Body {
    /// No request body.
    #[default]
    Absent,
    /// Exact JSON token/subtree.
    Json(Value),
    /// Explicit finite bytes for a concretely elected media type.
    Raw(Arc<[u8]>),
    /// Reusable admitted logical fields, encoded by the selected operation.
    Form(crate::Form),
}
// One internal body view is authoritative for owned and borrowed routes.
#[derive(Clone, Copy)]
pub(crate) enum AdmittedBody<'a> {
    Absent,
    Json(&'a Value),
    Raw(&'a Arc<[u8]>),
    Form(&'a [crate::form::ReadyField<'a>]),
}
impl<'a> From<&'a Body> for AdmittedBody<'a> {
    fn from(body: &'a Body) -> Self {
        match body {
            Body::Absent => Self::Absent,
            Body::Json(v) => Self::Json(v),
            Body::Raw(v) => Self::Raw(v),
            Body::Form(v) => Self::Form(v.fields()),
        }
    }
}
/// Explicit per-call elections, with the default policy documented per field.
#[derive(Clone, Default)]
pub struct Selection {
    /// Index in the effective authored server array. None selects a unique usable server.
    pub server: Option<usize>,
    /// Server-variable overrides; values are checked after substitution.
    pub variables: BTreeMap<String, String>,
    /// Exact concrete request media spelling (surrounding whitespace is trimmed).
    /// None selects a unique concrete declaration compatible with the supplied body.
    pub media: Option<String>,
    /// Index in the effective security alternatives, including an allowed empty alternative.
    /// Multiple authored alternatives require an election, regardless of supplied credentials.
    pub security: Option<usize>,
}
/// Supplied credential material. Debug is deliberately opaque.
#[derive(Clone)]
#[allow(missing_docs)]
pub enum CredentialValue {
    Basic { username: String, password: String },
    Bearer(String),
    ApiKey(String),
}
impl std::fmt::Debug for CredentialValue {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("CredentialValue([redacted])")
    }
}
/// A caller-supplied credential scoped to explicitly admitted HTTP(S) origins.
#[derive(Clone)]
pub struct Credential {
    /// Material suitable for the elected Security Scheme Object.
    pub value: CredentialValue,
    /// Origin URLs. Default ports and host/scheme case are compared canonically.
    pub origins: Vec<String>,
}
impl std::fmt::Debug for Credential {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("Credential([redacted])")
    }
}
/// Per-call data. No schema defaults are applied and no credentials are acquired.
#[derive(Clone, Default)]
pub struct Input {
    /// Explicit elections.
    pub selection: Selection,
    /// Parameter values distinguished by name and location.
    pub parameters: Vec<ParameterInput>,
    /// Finite body.
    pub body: Body,
    /// Caller-supplied additional header fields, checked after all application.
    pub headers: Vec<Header>,
    /// Credentials keyed by declared security-scheme name.
    pub credentials: BTreeMap<String, Credential>,
}
/// One effective parameter with independently retained source/reference data.
#[derive(Clone)]
pub struct Parameter {
    resolved: crate::ResolvedReference,
    node: Value,
    name: String,
    location: ParameterLocation,
    required: bool,
    explode: bool,
    style: crate::style::Style,
    reserved: bool,
    unsupported: bool,
    content: Option<crate::style::ContentCodec>,
    collection: Option<String>,
    schema_context: Option<SchemaDeclaration>,
}
/// Effective parameter declarations retaining their exact source owners.
/// Structured refusal leaves Operation::inspect available for raw inspection.
#[derive(Clone, Debug)]
pub struct Parameters {
    entries: Arc<[Parameter]>,
}
impl Parameters {
    /// Effective order, with operation declarations overriding path declarations.
    pub fn iter(&self) -> impl ExactSizeIterator<Item = Parameter> + '_ {
        self.entries.iter().cloned()
    }
}
impl std::fmt::Debug for Parameter {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Parameter")
            .field("location", &self.location)
            .field("required", &self.required)
            .finish_non_exhaustive()
    }
}
impl Parameter {
    /// Exact declared name; untrusted explicit-access data.
    pub fn name(&self) -> &str {
        &self.name
    }
    /// Parameter namespace.
    pub fn location(&self) -> ParameterLocation {
        self.location
    }
    /// Effective required flag after declaration checks.
    pub fn required(&self) -> bool {
        self.required
    }
    /// Authored declaration/reference at its use site.
    pub fn authored(&self) -> Value {
        self.resolved.authored.clone()
    }
    /// Reference use-site siblings, chain and final target, retaining their owners.
    pub fn resolved(&self) -> crate::ResolvedReference {
        self.resolved.clone()
    }
    /// Raw Schema Object. None does not mean a content declaration is absent.
    pub fn schema(&self) -> Option<Value> {
        self.schema_context.as_ref().map(|s| s.value.clone())
    }
    /// Opaque schema with retained edition and dialect context.
    pub fn schema_description(&self) -> Option<crate::SchemaDescription> {
        self.schema_context
            .as_ref()
            .map(SchemaDeclaration::describe)
    }
}
/// A schema value and its interpretation context; no evaluation is performed.
#[derive(Clone, Debug)]
pub(crate) struct SchemaDeclaration {
    pub value: Value,
    pub profile: crate::SchemaProfile,
}
impl SchemaDeclaration {
    pub(crate) fn into_description(self) -> crate::SchemaDescription {
        crate::SchemaDescription {
            value: self.value,
            profile: self.profile,
        }
    }
    pub(crate) fn describe(&self) -> crate::SchemaDescription {
        crate::SchemaDescription {
            value: self.value.clone(),
            profile: self.profile.clone(),
        }
    }
}
/// An effective representation, retaining its original declaration.
#[derive(Clone, Debug)]
pub(crate) struct MediaDeclaration {
    /// Effective media type or range, including protocol-defined form choices.
    pub media_type: String,
    /// Original Media Type Object, Swagger consumes/produces string, or form
    /// parameter establishing an implicit Swagger form representation.
    pub authored: Value,
    /// Opaque payload schema.
    pub schema: Option<SchemaDeclaration>,
    /// Opaque encoding declarations.
    pub encoding: Option<Value>,
    /// Opaque sequential-item schema when declared.
    pub item_schema: Option<SchemaDeclaration>,
    /// Authoritative form shape for this representation only.
    pub fields: FieldDeclarations,
    /// Positional encoding declarations, when authored in OAS 3.2.
    pub prefix_encoding: Option<Value>,
    /// Repeated positional encoding declaration, when authored in OAS 3.2.
    pub item_encoding: Option<Value>,
}
/// A logical request body independent of the edition's declaration syntax.
#[derive(Clone, Debug)]
pub(crate) struct BodyDeclaration {
    /// Original declaration and local-reference provenance.
    pub declaration: crate::ResolvedReference,
    /// Whether omitting the logical body is permitted.
    pub required: bool,
    /// Effective request representations. Malformed media keys are excluded;
    /// their original values remain accessible through `declaration`.
    pub media: Vec<MediaDeclaration>,
    /// No media constraint was declared; an explicit concrete type or the JSON codec determines it.
    /// Swagger form parameters still constrain the body to form representations.
    pub media_unrestricted: bool,
}
/// A logical form field; automatic form serialization is qualified separately.
#[derive(Clone, Debug)]
pub(crate) struct FieldDeclaration {
    /// Logical field name.
    pub name: String,
    /// Protocol obligation or a schema-validation hint; these are not interchangeable.
    pub requirement: FieldRequirement,
    /// Opaque field schema and vocabulary.
    pub schema: SchemaDeclaration,
    /// Authored declaration, retaining format/encoding details.
    pub authored: Value,
    /// Original field-specific Encoding Object, if present.
    pub encoding: Option<Value>,
}
/// Meaning of a field's requiredness declaration.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum FieldRequirement {
    /// No unconditional requirement is declared.
    Optional,
    /// A Swagger form parameter must be supplied.
    ProtocolRequired,
    /// A supported static schema required hint; preparation does not enforce it.
    SchemaRequired,
    /// Supported static inspection could not establish a complete required hint.
    UnknownHint,
}
/// Why field inspection cannot provide a complete shape.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum FieldShapeReason {
    /// No schema was supplied for this representation.
    MissingSchema,
    /// A schema reference needs schema-aware resolution.
    SchemaReference,
    /// Composition or conditional schemas may contribute fields.
    Composition,
    /// The schema vocabulary or shape cannot be mechanically inspected.
    SchemaShape,
    /// A positional or nested multipart representation needs another body model.
    PositionalEncoding,
}
/// A typed unresolved fact with its actual source owner.
#[derive(Clone, Debug)]
pub struct FieldShapeIssue {
    /// Unresolved fact classification.
    pub reason: FieldShapeReason,
    /// Authored schema or media declaration requiring interpretation.
    pub source: Value,
}
/// Completeness of logical form-field inspection.
#[derive(Clone, Debug)]
pub(crate) enum FieldDeclarations {
    /// The body has no form representations.
    NotApplicable,
    /// Explicit logical fields are available.
    Declared(Vec<FieldDeclaration>),
}
/// An effective response declaration and its representations.
#[derive(Clone, Debug)]
pub struct ResponseDescription {
    /// Exact, range or default status selector.
    pub status: String,
    /// Original declaration and local-reference provenance.
    pub declaration: crate::ResolvedReference,
    /// Effective response representations. Malformed media keys are excluded;
    /// their original values remain accessible through `declaration`.
    pub media: Vec<crate::MediaDescription>,
}
/// One effective target choice. Swagger components become a URL template without source rewriting.
#[derive(Clone, Debug)]
pub struct ServerDescription {
    /// Effective URL or URL template before variable substitution.
    pub url: String,
    /// Original variable declarations, when applicable.
    pub variables: Option<Value>,
    /// Original values contributing to this target; an empty list denotes the protocol default.
    pub authored: Vec<Value>,
}
/// A normalized OAuth flow, with original source retained by the owning requirement.
#[derive(Clone, Debug)]
pub struct OAuthFlowDescription {
    /// Common flow name: implicit, password, clientCredentials, authorizationCode
    /// or deviceAuthorization.
    pub flow: String,
    /// Authorization endpoint when applicable.
    pub authorization_url: Option<String>,
    /// Device authorization endpoint for the OpenAPI 3.2 flow.
    pub device_authorization_url: Option<String>,
    /// Token endpoint when applicable.
    pub token_url: Option<String>,
    /// Refresh endpoint when declared.
    pub refresh_url: Option<String>,
    /// Declared scope names and descriptions.
    pub scopes: BTreeMap<String, String>,
}
/// Edition-independent security mechanism classification.
#[derive(Clone, Debug)]
#[non_exhaustive]
pub enum SecurityScheme {
    /// HTTP Basic credentials.
    Basic,
    /// HTTP bearer token; format is descriptive only.
    Bearer {
        /// Optional format hint.
        format: Option<String>,
    },
    /// API key sent under a declared name/location.
    ApiKey {
        /// Wire name.
        name: String,
        /// Wire location.
        location: ParameterLocation,
    },
    /// Caller-supplied OAuth bearer token; flows describe acquisition separately.
    OAuth2 {
        /// Normalized acquisition flows.
        flows: Vec<OAuthFlowDescription>,
    },
    /// OpenID Connect discovery description; caller supplies bearer token.
    OpenIdConnect {
        /// Discovery URL.
        url: String,
    },
    /// Mutual TLS host identity; finite credential input does not implement it.
    MutualTls,
    /// Retained mechanism outside this implementation's recognized profile.
    Unsupported {
        /// Original mechanism identifier.
        type_name: String,
    },
}
/// One declared scheme in a conjunctive security requirement.
#[derive(Clone, Debug)]
pub struct SecurityRequirement {
    /// Declared scheme identity.
    pub name: String,
    /// Required OAuth scopes or later-edition roles; verifying credentials remains caller-owned.
    pub scopes: Vec<String>,
    /// Original scheme declaration and reference provenance.
    pub declaration: crate::ResolvedReference,
    /// Normalized security mechanism; credentials remain separate per-call inputs.
    pub scheme: SecurityScheme,
}
/// One effective security alternative; an empty requirement list permits anonymous calls.
#[derive(Clone, Debug)]
pub struct SecurityAlternative {
    /// Original requirement object.
    pub authored: Value,
    /// All these schemes apply together.
    pub requirements: Vec<SecurityRequirement>,
}
/// Common effective declarations for an explorer or dynamic caller.
#[derive(Clone, Debug)]
pub struct EffectiveOperationDescription {
    /// Authored path template.
    pub path: String,
    /// Exact HTTP method.
    pub method: Method,
    /// Optional operation ID.
    pub operation_id: Option<String>,
    /// Effective non-body parameter declarations.
    pub parameters: Parameters,
    /// Effective target choices, in election order.
    pub servers: Vec<ServerDescription>,
    /// Effective logical body; ignored edition-specific body declarations are absent here.
    pub request_body: Option<crate::RequestBodyDescription>,
    /// Effective status and representation declarations.
    pub responses: Vec<ResponseDescription>,
    /// Effective security alternatives.
    pub security: Vec<SecurityAlternative>,
}

pub(crate) struct Compiled {
    parameters: Arc<[Parameter]>,
    usage: DescriptorUsage,
    request: OnceLock<Result<RequestDeclarations, Diagnostic>>,
}
struct RequestDeclarations {
    body: Option<BodyDeclaration>,
    responses: Vec<(String, Value)>,
}
/// Immutable request plan. Preparing or cloning it does not authorize a retry.
#[derive(Clone)]
pub struct PreparedRequest {
    pub(crate) operation: Operation,
    pub(crate) compiled: Arc<Compiled>,
    pub(crate) method: Method,
    pub(crate) target: String,
    pub(crate) headers: Vec<Header>,
    pub(crate) body: Option<Arc<[u8]>>,
    pub(crate) body_kind: &'static str,
    pub(crate) limits: Limits,
}
impl std::fmt::Debug for PreparedRequest {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("PreparedRequest")
            .field("method", &self.method)
            .field("body_bytes", &self.body.as_ref().map(|b| b.len()))
            .finish()
    }
}

fn required_string(node: &Value, key: &str) -> Result<String, Diagnostic> {
    node.get(key)
        .and_then(|v| v.as_str().map(str::to_owned))
        .ok_or_else(|| node.error(Code::InvalidDeclaration))
}
fn bool_field(node: &Value, key: &str, default: bool) -> Result<bool, Diagnostic> {
    match node.get(key) {
        None => Ok(default),
        Some(v) => v.as_bool().ok_or_else(|| v.error(Code::InvalidDeclaration)),
    }
}
pub(crate) fn same_name(a: &str, b: &str, location: ParameterLocation) -> bool {
    if location == ParameterLocation::Header {
        a.eq_ignore_ascii_case(b)
    } else {
        a == b
    }
}

/// Admission for newly allocated per-operation descriptions. Original Value owners
/// are shared and are not charged as copied source bytes. Limits are per construction,
/// independent from both the admitted source owner and other compiled cache entries.
pub(crate) struct DescriptorWork<'a> {
    limits: &'a Limits,
    usage: DescriptorUsage,
    cancellation: Option<&'a crate::Cancellation>,
}
#[derive(Clone, Copy, Default)]
struct DescriptorUsage {
    bytes: usize,
    nodes: usize,
    references: usize,
}
impl<'a> DescriptorWork<'a> {
    pub(crate) fn new(limits: &'a Limits) -> Self {
        Self {
            limits,
            usage: DescriptorUsage::default(),
            cancellation: None,
        }
    }
    pub(crate) fn with_cancellation(
        limits: &'a Limits,
        cancellation: &'a crate::Cancellation,
    ) -> Self {
        Self {
            cancellation: Some(cancellation),
            ..Self::new(limits)
        }
    }
    fn checkpoint(&self) -> Result<(), Diagnostic> {
        #[cfg(test)]
        parameter_work_tests::CHECKPOINTS.with(|count| count.set(count.get() + 1));
        self.cancellation.map_or(Ok(()), crate::schema::checkpoint)
    }
    fn admit(
        used: &mut usize,
        amount: usize,
        maximum: usize,
        kind: LimitKind,
        source: &Value,
    ) -> Result<(), Diagnostic> {
        let actual = used.saturating_add(amount);
        if actual > maximum {
            return Err(source.error(Code::Limit).reasoned(DiagnosticReason::Limit {
                kind,
                maximum,
                actual,
            }));
        }
        *used = actual;
        Ok(())
    }
    pub(crate) fn entry(&mut self, source: &Value) -> Result<(), Diagnostic> {
        self.checkpoint()?;
        Self::admit(
            &mut self.usage.nodes,
            1,
            self.limits.nodes,
            LimitKind::DescriptorNodes,
            source,
        )
    }
    pub(crate) fn declaration(&mut self, source: &Value) -> Result<(), Diagnostic> {
        self.step(source)?;
        self.entry(source)
    }
    fn step(&mut self, source: &Value) -> Result<(), Diagnostic> {
        self.checkpoint()?;
        Self::admit(
            &mut self.usage.references,
            1,
            self.limits.descriptor_work,
            LimitKind::DescriptorWork,
            source,
        )
    }
    pub(crate) fn text(&mut self, source: &Value, text: &str) -> Result<String, Diagnostic> {
        self.entry(source)?;
        Self::admit(
            &mut self.usage.bytes,
            text.len(),
            self.limits.document_bytes,
            LimitKind::DescriptorBytes,
            source,
        )?;
        let mut copy = String::with_capacity(text.len());
        // Respect UTF-8 boundaries while observing cancellation during large copies.
        let mut start = 0;
        while start < text.len() {
            self.checkpoint()?;
            let mut end = (start + 8192).min(text.len());
            while !text.is_char_boundary(end) {
                end -= 1;
            }
            copy.push_str(&text[start..end]);
            start = end;
        }
        Ok(copy)
    }
    fn required(&mut self, source: &Value, key: &str) -> Result<String, Diagnostic> {
        let value = source
            .get(key)
            .ok_or_else(|| source.error(Code::InvalidDeclaration))?;
        self.text(
            &value,
            value
                .as_str()
                .ok_or_else(|| value.error(Code::InvalidDeclaration))?,
        )
    }
    pub(crate) fn optional(
        &mut self,
        source: &Value,
        key: &str,
    ) -> Result<Option<String>, Diagnostic> {
        source
            .get(key)
            .map(|value| {
                self.text(
                    &value,
                    value
                        .as_str()
                        .ok_or_else(|| value.error(Code::InvalidDeclaration))?,
                )
            })
            .transpose()
    }
    pub(crate) fn resolve(
        &mut self,
        graph: &crate::graph::Graph,
        source: &Value,
        kind: ReferenceKind,
    ) -> Result<crate::ResolvedReference, Diagnostic> {
        self.declaration(source)?;
        let work_left = self.limits.descriptor_work - self.usage.references;
        let nodes_left = self.limits.nodes - self.usage.nodes;
        let remaining = work_left.min(nodes_left).min(self.limits.reference_steps);
        let resolved = graph.resolve(source, kind, remaining).map_err(|error| {
            if error.code() == Code::Limit && remaining < self.limits.reference_steps {
                let (kind, maximum) = if work_left <= nodes_left {
                    (LimitKind::DescriptorWork, self.limits.descriptor_work)
                } else {
                    (LimitKind::DescriptorNodes, self.limits.nodes)
                };
                error.reasoned(DiagnosticReason::Limit {
                    kind,
                    maximum,
                    actual: maximum.saturating_add(1),
                })
            } else {
                error
            }
        })?;
        let hops = resolved.chain.len().saturating_sub(1);
        self.usage.references += hops;
        self.usage.nodes += hops;
        Ok(resolved)
    }
}

impl Operation {
    pub(crate) fn schema_description(
        &self,
        value: Value,
        work: &mut DescriptorWork<'_>,
    ) -> Result<SchemaDeclaration, Diagnostic> {
        work.entry(&value)?;
        let profile = if let Some(scope) = self.store.graph.schemas.scope(crate::graph::key(&value))
        {
            match scope.profile.as_ref() {
                crate::SchemaProfile::JsonSchema { dialect_uri } => {
                    let dialect_source = self
                        .store
                        .graph
                        .value(scope.root)
                        .get("$schema")
                        .or_else(|| {
                            self.store
                                .graph
                                .resource_root(&value)
                                .ok()?
                                .get("jsonSchemaDialect")
                        })
                        .unwrap_or_else(|| value.clone());
                    crate::SchemaProfile::JsonSchema {
                        dialect_uri: work.text(&dialect_source, dialect_uri)?,
                    }
                }
                profile => profile.clone(),
            }
        } else if self.store.edition == Ok(Edition::Swagger20) {
            // Swagger's non-body parameter constraints are protocol positions.
            crate::SchemaProfile::Swagger20
        } else {
            return Err(value.error(Code::UnsupportedSchema));
        };
        Ok(SchemaDeclaration { value, profile })
    }
    fn effective_parameter_references(
        &self,
        work: &mut DescriptorWork<'_>,
    ) -> Result<Vec<crate::ResolvedReference>, Diagnostic> {
        self.store.edition.clone()?;
        let mut parameters: Vec<crate::ResolvedReference> = vec![];
        // One entry per identity records its stable output position and the list
        // that last declared it. This also refuses duplicates within either list.
        let mut identities = HashMap::new();
        for (level, list) in [
            self.path_field("parameters"),
            self.value().get("parameters"),
        ]
        .into_iter()
        .enumerate()
        {
            let Some(list) = list else { continue };
            for authored in list
                .elements()
                .ok_or_else(|| list.error(Code::InvalidDeclaration))?
            {
                let resolved =
                    work.resolve(&self.store.graph, &authored, ReferenceKind::Parameter)?;
                let p = &resolved.target;
                let location = work.required(p, "in")?;
                let mut name = work.required(p, "name")?;
                if location == "header" {
                    name.make_ascii_lowercase();
                }
                // Charge the identity lookup before hashing or allocating its entry.
                // Copied key bytes and map/vector entries have separate admissions.
                work.step(p)?;
                match identities.entry((location, name)) {
                    Entry::Occupied(mut entry) => {
                        let (index, previous_level) = *entry.get();
                        if previous_level == level {
                            return Err(p.error(Code::InvalidDeclaration));
                        }
                        entry.insert((index, level));
                        parameters[index] = resolved;
                    }
                    Entry::Vacant(entry) => {
                        work.entry(p)?;
                        entry.insert((parameters.len(), level));
                        work.entry(p)?;
                        parameters.push(resolved);
                    }
                }
            }
        }
        Ok(parameters)
    }
    fn media_descriptions(
        &self,
        node: &Value,
        legacy_key: Option<&str>,
        work: &mut DescriptorWork<'_>,
    ) -> Result<Vec<MediaDeclaration>, Diagnostic> {
        if let Some(key) = legacy_key {
            let declared = self
                .value()
                .get(key)
                .or_else(|| self.store.json.root().get(key));
            return declared
                .map(|v| {
                    v.elements()
                        .ok_or_else(|| v.error(Code::InvalidDeclaration))?
                        .map(|media| {
                            work.entry(&media)?;
                            let media_type = work.text(
                                &media,
                                media
                                    .as_str()
                                    .ok_or_else(|| media.error(Code::InvalidDeclaration))?,
                            )?;
                            Ok(MediaDeclaration {
                                media_type,
                                authored: media,
                                schema: node
                                    .get("schema")
                                    .map(|v| self.schema_description(v, work))
                                    .transpose()?,
                                encoding: None,
                                item_schema: None,
                                fields: FieldDeclarations::NotApplicable,
                                prefix_encoding: None,
                                item_encoding: None,
                            })
                        })
                        .collect()
                })
                .unwrap_or_else(|| Ok(vec![]));
        }
        let Some(content) = node.get("content") else {
            return Ok(vec![]);
        };
        content
            .members()
            .ok_or_else(|| content.error(Code::InvalidDeclaration))?
            // Only syntactically valid media keys govern a representation. Exclude
            // other entries before inspecting their value; raw declarations retain them.
            .filter(|(key, _)| parse_media(key, true).is_ok())
            .map(|(key, value)| {
                let resolved = self.resolve_media(&value, work)?;
                let fields = FieldDeclarations::NotApplicable;
                Ok(MediaDeclaration {
                    fields,
                    prefix_encoding: (self.store.edition == Ok(Edition::OpenApi32))
                        .then(|| resolved.get("prefixEncoding"))
                        .flatten(),
                    item_encoding: (self.store.edition == Ok(Edition::OpenApi32))
                        .then(|| resolved.get("itemEncoding"))
                        .flatten(),
                    media_type: work.text(&value, key)?,
                    authored: value,
                    schema: resolved
                        .get("schema")
                        .map(|v| self.schema_description(v, work))
                        .transpose()?,
                    encoding: resolved.get("encoding"),
                    item_schema: (self.store.edition == Ok(Edition::OpenApi32))
                        .then(|| resolved.get("itemSchema"))
                        .flatten()
                        .map(|v| self.schema_description(v, work))
                        .transpose()?,
                })
            })
            .collect()
    }
    // The protocol target is shared by body descriptions and content parameters.
    // Resolving this object does not inspect or interpret its payload schema.
    fn resolve_media(
        &self,
        value: &Value,
        work: &mut DescriptorWork<'_>,
    ) -> Result<Value, Diagnostic> {
        work.entry(value)?;
        if value.kind() != ValueKind::Object {
            return Err(value.error(Code::InvalidDeclaration));
        }
        if value.get("$ref").is_some() {
            if self.store.edition != Ok(Edition::OpenApi32) {
                return Err(value.error(Code::InvalidDeclaration));
            }
            Ok(work
                .resolve(&self.store.graph, value, ReferenceKind::MediaType)?
                .target)
        } else {
            Ok(value.clone())
        }
    }
    pub(crate) fn body_description(
        &self,
        work: &mut DescriptorWork<'_>,
    ) -> Result<Option<BodyDeclaration>, Diagnostic> {
        let edition = self.store.edition.clone()?;
        if edition == Edition::Swagger20 {
            let mut parameters = self.effective_parameter_references(work)?;
            let mut body_count = 0;
            let mut has_forms = false;
            let mut index = None;
            for (i, parameter) in parameters.iter().enumerate() {
                work.step(&parameter.target)?;
                let location = parameter.target.get("in");
                let location = location.as_ref().and_then(Value::as_str);
                body_count += usize::from(location == Some("body"));
                has_forms |= location == Some("formData");
                if index.is_none() && matches!(location, Some("body" | "formData")) {
                    index = Some(i);
                }
            }
            if body_count > 1 || body_count != 0 && has_forms {
                return Err(self.value().error(Code::InvalidDeclaration));
            }
            let Some(index) = index else {
                return Ok(None);
            };
            if body_count != 0 && parameters[index].target.get("schema").is_none() {
                return Err(parameters[index].target.error(Code::InvalidDeclaration));
            }
            let fields = if !has_forms {
                FieldDeclarations::NotApplicable
            } else {
                let mut fields = vec![];
                for field in &parameters {
                    work.step(&field.target)?;
                    if !field
                        .target
                        .get("in")
                        .is_some_and(|v| v.as_str() == Some("formData"))
                    {
                        continue;
                    }
                    work.entry(&field.target)?;
                    fields.push(FieldDeclaration {
                        name: work.required(&field.target, "name")?,
                        requirement: if bool_field(&field.target, "required", false)? {
                            FieldRequirement::ProtocolRequired
                        } else {
                            FieldRequirement::Optional
                        },
                        encoding: None,
                        schema: self.schema_description(field.target.clone(), work)?,
                        authored: field.authored.clone(),
                    });
                }
                FieldDeclarations::Declared(fields)
            };
            // Move the resolved declaration so its provenance chain is not copied.
            let declaration = parameters.swap_remove(index);
            let required = match &fields {
                FieldDeclarations::Declared(fields) => {
                    let mut required = false;
                    for field in fields {
                        work.step(&field.authored)?;
                        required |= field.requirement == FieldRequirement::ProtocolRequired;
                    }
                    required
                }
                _ => bool_field(&declaration.target, "required", false)?,
            };
            let mut media = self.media_descriptions(&declaration.target, Some("consumes"), work)?;
            let media_unrestricted = self
                .value()
                .get("consumes")
                .or_else(|| self.store.json.root().get("consumes"))
                .is_none_or(|v| v.elements().is_some_and(|mut v| v.next().is_none()));
            if has_forms && media_unrestricted {
                // formData itself supplies a protocol constraint even without
                // consumes. Keep both legal choices visible; do not guess an
                // application preference or send a JSON body for form fields.
                for media_type in ["application/x-www-form-urlencoded", "multipart/form-data"] {
                    work.entry(&declaration.target)?;
                    media.push(MediaDeclaration {
                        media_type: work.text(&declaration.target, media_type)?,
                        authored: declaration.target.clone(),
                        schema: None,
                        encoding: None,
                        item_schema: None,
                        fields: FieldDeclarations::NotApplicable,
                        prefix_encoding: None,
                        item_encoding: None,
                    });
                }
            }
            for representation in &mut media {
                work.step(&representation.authored)?;
                if parse_media(&representation.media_type, true).is_ok_and(|m| {
                    m.essence.starts_with("multipart/")
                        || m.essence == "application/x-www-form-urlencoded"
                }) && let FieldDeclarations::Declared(fields) = &fields
                {
                    let mut copied = Vec::new();
                    for field in fields {
                        work.declaration(&field.authored)?;
                        let name = work.text(&field.authored, &field.name)?;
                        copied.push(FieldDeclaration {
                            name,
                            requirement: field.requirement,
                            schema: field.schema.clone(),
                            encoding: field.encoding.clone(),
                            authored: field.authored.clone(),
                        });
                    }
                    representation.fields = FieldDeclarations::Declared(copied);
                }
            }
            work.entry(&declaration.target)?;
            return Ok(Some(BodyDeclaration {
                declaration,
                required,
                media,
                media_unrestricted: media_unrestricted && !has_forms,
            }));
        }
        // 3.0 requires consumers to ignore requestBody where HTTP semantics are undefined.
        if edition == Edition::OpenApi30
            && !matches!(
                self.method(),
                Method::Post | Method::Put | Method::Patch | Method::Options
            )
        {
            return Ok(None);
        }
        let Some(body) = self.value().get("requestBody") else {
            return Ok(None);
        };
        let declaration = work.resolve(&self.store.graph, &body, ReferenceKind::RequestBody)?;
        let media = self.media_descriptions(&declaration.target, None, work)?;
        work.entry(&declaration.target)?;
        Ok(Some(BodyDeclaration {
            required: bool_field(&declaration.target, "required", false)?,
            declaration,
            media,
            media_unrestricted: false,
        }))
    }
    fn server_descriptions(
        &self,
        work: &mut DescriptorWork<'_>,
    ) -> Result<Vec<ServerDescription>, Diagnostic> {
        if self.store.edition.clone()? != Edition::Swagger20 {
            return match self
                .value()
                .get("servers")
                .or_else(|| self.path_field("servers"))
                .or_else(|| self.store.json.root().get("servers"))
            {
                None => {
                    work.entry(&self.value())?;
                    Ok(vec![ServerDescription {
                        url: work.text(&self.value(), "/")?,
                        variables: None,
                        authored: vec![],
                    }])
                }
                Some(list) => {
                    let result = list
                        .elements()
                        .ok_or_else(|| list.error(Code::InvalidDeclaration))?
                        .map(|v| {
                            work.entry(&v)?;
                            work.entry(&v)?; // Retained authored contributor.
                            Ok(ServerDescription {
                                url: work.required(&v, "url")?,
                                variables: v.get("variables"),
                                authored: vec![v],
                            })
                        })
                        .collect::<Result<Vec<_>, Diagnostic>>()?;
                    Ok(if result.is_empty() {
                        work.entry(&self.value())?;
                        vec![ServerDescription {
                            url: work.text(&self.value(), "/")?,
                            variables: None,
                            authored: vec![],
                        }]
                    } else {
                        result
                    })
                }
            };
        }
        let root = self.store.json.root();
        let host_node = root.get("host");
        let path_node = root.get("basePath");
        let base = self
            .store
            .base
            .as_deref()
            .map(fluent_uri::UriRef::parse)
            .transpose()
            .map_err(|_| root.error(Code::InvalidDestination))?;
        let host = match work.optional(&root, "host")? {
            Some(host) => host,
            None => work.text(
                &root,
                base.as_ref()
                    .and_then(|b| b.authority())
                    .ok_or_else(|| root.error(Code::MissingBase))?
                    .as_str(),
            )?,
        };
        DescriptorWork::admit(
            &mut work.usage.bytes,
            host.len().saturating_add(2),
            work.limits.document_bytes,
            LimitKind::DescriptorBytes,
            host_node.as_ref().unwrap_or(&root),
        )?;
        let authority = fluent_uri::UriRef::parse(format!("//{host}"))
            .map_err(|_| root.error(Code::InvalidDestination))?;
        if authority.authority().is_none()
            || !authority.path().is_empty()
            || authority.query().is_some()
            || authority.fragment().is_some()
        {
            return Err(host_node
                .as_ref()
                .unwrap_or(&root)
                .error(Code::InvalidDeclaration));
        }
        let path = work.optional(&root, "basePath")?.unwrap_or_default();
        if !path.is_empty() && !path.starts_with('/') || path.contains(['?', '#']) {
            return Err(path_node
                .as_ref()
                .unwrap_or(&root)
                .error(Code::InvalidDeclaration));
        }
        let schemes = self.value().get("schemes").or_else(|| root.get("schemes"));
        let mut scheme_values = match schemes.as_ref() {
            Some(v) => v
                .elements()
                .ok_or_else(|| v.error(Code::InvalidDeclaration))?
                .map(|v| {
                    work.entry(&v)?;
                    work.text(
                        &v,
                        v.as_str()
                            .ok_or_else(|| v.error(Code::InvalidDeclaration))?,
                    )
                    .map(|s| (s, Some(v.clone())))
                })
                .collect::<Result<Vec<_>, _>>()?,
            None => vec![],
        };
        if scheme_values.is_empty() {
            work.entry(&root)?;
            scheme_values.push((
                work.text(
                    &root,
                    base.as_ref()
                        .and_then(|b| b.scheme())
                        .ok_or_else(|| root.error(Code::MissingBase))?
                        .as_str(),
                )?,
                None,
            ));
        }
        scheme_values
            .into_iter()
            .map(|(scheme, source)| {
                if !matches!(scheme.as_str(), "http" | "https" | "ws" | "wss") {
                    return Err(source
                        .as_ref()
                        .unwrap_or(&root)
                        .error(Code::InvalidDeclaration));
                }
                work.entry(&root)?;
                DescriptorWork::admit(
                    &mut work.usage.bytes,
                    scheme
                        .len()
                        .saturating_add(3)
                        .saturating_add(host.len())
                        .saturating_add(path.len()),
                    work.limits.document_bytes,
                    LimitKind::DescriptorBytes,
                    source.as_ref().unwrap_or(&root),
                )?;
                let mut authored = vec![];
                for contributor in [host_node.clone(), path_node.clone(), source]
                    .into_iter()
                    .flatten()
                {
                    work.entry(&contributor)?;
                    authored.push(contributor);
                }
                Ok(ServerDescription {
                    url: format!("{scheme}://{host}{path}"),
                    variables: None,
                    authored,
                })
            })
            .collect()
    }
    fn choose_target(&self, selection: &Selection) -> Result<String, Diagnostic> {
        let root = self.store.json.root();
        if self.store.edition.clone()? != Edition::Swagger20 {
            let servers = self.inspect().servers;
            let base = match &servers {
                Some(value) => self.store.graph.retrieval_base(value),
                None => self.store.base.as_deref(),
            };
            return choose_server(servers.as_ref(), base, selection, &root, &self.store.limits);
        }
        if !selection.variables.is_empty() {
            return Err(root.error(Code::InvalidSelection));
        }
        let servers = self.server_descriptions(&mut DescriptorWork::new(&self.store.limits))?;
        let expand = |s: &ServerDescription| uri::server_target(&s.url, self.store.base.as_deref());
        if let Some(i) = selection.server {
            return expand(
                servers
                    .get(i)
                    .ok_or_else(|| root.error(Code::InvalidSelection))?,
            );
        }
        let usable: Vec<_> = servers.iter().map(expand).collect();
        let good: Vec<_> = usable.iter().filter_map(|r| r.as_ref().ok()).collect();
        match good.as_slice() {
            [one] => Ok((*one).clone()),
            [] => Err(usable
                .into_iter()
                .find_map(Result::err)
                .unwrap_or_else(|| root.error(Code::InvalidSelection))),
            _ => Err(root.error(Code::AmbiguousSelection)),
        }
    }
    /// Inspect effective edition-independent inputs, target choices, representations and security.
    /// This performs no request preparation, credential acquisition or schema evaluation.
    /// Newly copied strings, entries and declaration/reference work have per-construction
    /// budgets controlled by document_bytes, nodes and descriptor_work respectively.
    /// Each reference chain is independently bounded by reference_steps.
    /// Retained source owners and other compiled cache entries are separate resource domains.
    pub fn describe(&self) -> Result<EffectiveOperationDescription, Diagnostic> {
        self.describe_with_cancellation(&crate::Cancellation::default())
    }
    /// Inspect effective declarations with bounded selected schema work and cancellation.
    pub fn describe_with_cancellation(
        &self,
        cancellation: &crate::Cancellation,
    ) -> Result<EffectiveOperationDescription, Diagnostic> {
        self.describe_inner(cancellation)
            .map_err(|e| e.bind_graph(&self.store))
    }
    fn describe_inner(
        &self,
        cancellation: &crate::Cancellation,
    ) -> Result<EffectiveOperationDescription, Diagnostic> {
        let mut work = DescriptorWork::with_cancellation(&self.store.limits, cancellation);
        let mut facts = crate::schema_facts::FactWork::new(&self.store.limits, cancellation);
        let parameters = self.parameter_declarations(&mut work)?;
        let request_body = self
            .body_description(&mut work)?
            .map(|body| self.describe_body(body, &mut work, &mut facts))
            .transpose()?;
        let mut responses = vec![];
        for (status, declaration) in self.response_declarations(&mut work)? {
            let media = self.media_descriptions(
                &declaration.target,
                (self.store.edition == Ok(Edition::Swagger20)).then_some("produces"),
                &mut work,
            )?;
            work.entry(&declaration.target)?;
            responses.push(ResponseDescription {
                status,
                declaration,
                media: media
                    .into_iter()
                    .map(|media| self.describe_media(media, &mut work, &mut facts))
                    .collect::<Result<_, _>>()?,
            });
        }
        let node = self.value();
        Ok(EffectiveOperationDescription {
            path: work.text(&node, self.path())?,
            method: self.method(),
            operation_id: node
                .get("operationId")
                .filter(|v| v.as_str().is_some())
                .map(|v| work.text(&v, v.as_str().expect("string operation ID")))
                .transpose()?,
            parameters: Parameters {
                entries: parameters.into(),
            },
            servers: self.server_descriptions(&mut work)?,
            request_body,
            responses,
            security: self.security_descriptions(&mut work)?,
        })
    }
    fn response_declarations(
        &self,
        work: &mut DescriptorWork<'_>,
    ) -> Result<Vec<(String, crate::ResolvedReference)>, Diagnostic> {
        let mut result = vec![];
        if let Some(responses) = self.value().get("responses") {
            for (key, value) in responses
                .members()
                .ok_or_else(|| responses.error(Code::InvalidDeclaration))?
            {
                work.step(&value)?;
                if key.starts_with("x-") {
                    continue;
                }
                let valid = key == "default"
                    || key.len() == 3
                        && key.as_bytes()[0] >= b'1'
                        && key.as_bytes()[0] <= b'5'
                        && (self.store.edition.clone()? != Edition::Swagger20 && &key[1..] == "XX"
                            || key.bytes().all(|b| b.is_ascii_digit()));
                if !valid {
                    return Err(value.error(Code::InvalidDeclaration));
                }
                let declaration =
                    work.resolve(&self.store.graph, &value, ReferenceKind::Response)?;
                let key = work.text(&value, key)?;
                work.entry(&value)?;
                result.push((key, declaration));
            }
            if result.is_empty() {
                return Err(responses.error(Code::InvalidDeclaration));
            }
        }
        Ok(result)
    }
    fn security_descriptions(
        &self,
        work: &mut DescriptorWork<'_>,
    ) -> Result<Vec<SecurityAlternative>, Diagnostic> {
        let Some(security) = self
            .value()
            .get("security")
            .or_else(|| self.store.json.root().get("security"))
        else {
            return Ok(vec![]);
        };
        let root = self.store.json.root();
        let edition = self.store.edition.clone()?;
        let mut alternatives = vec![];
        for authored in security
            .elements()
            .ok_or_else(|| security.error(Code::InvalidDeclaration))?
        {
            work.entry(&authored)?;
            let mut requirements = vec![];
            for (name, scopes_source) in authored
                .members()
                .ok_or_else(|| authored.error(Code::InvalidDeclaration))?
            {
                let declared = security_source(&self.store.graph, &root, name, &scopes_source)?;
                let declaration =
                    work.resolve(&self.store.graph, &declared, ReferenceKind::SecurityScheme)?;
                let scheme = describe_security(&declaration.target, edition, work)?;
                let mut scopes = vec![];
                for scope in scopes_source
                    .elements()
                    .ok_or_else(|| scopes_source.error(Code::InvalidDeclaration))?
                {
                    scopes.push(
                        work.text(
                            &scope,
                            scope
                                .as_str()
                                .ok_or_else(|| scope.error(Code::InvalidDeclaration))?,
                        )?,
                    );
                }
                if matches!(edition, Edition::Swagger20 | Edition::OpenApi30)
                    && !matches!(
                        scheme,
                        SecurityScheme::OAuth2 { .. } | SecurityScheme::OpenIdConnect { .. }
                    )
                    && !scopes.is_empty()
                {
                    return Err(scopes_source.error(Code::InvalidDeclaration));
                }
                work.entry(&scopes_source)?;
                requirements.push(SecurityRequirement {
                    name: work.text(&authored, name)?,
                    scopes,
                    declaration,
                    scheme,
                });
            }
            alternatives.push(SecurityAlternative {
                authored,
                requirements,
            });
        }
        Ok(alternatives)
    }
}

fn security_source(
    graph: &crate::graph::Graph,
    root: &Value,
    name: &str,
    requirement: &Value,
) -> Result<Value, Diagnostic> {
    let schemes = if Edition::detect(root)? == Edition::Swagger20 {
        root.get("securityDefinitions")
    } else {
        root.get("components")
            .and_then(|v| v.get("securitySchemes"))
    };
    if let Some(scheme) = schemes.and_then(|v| v.get(name)) {
        return Ok(scheme);
    }
    if Edition::detect(root)? == Edition::OpenApi32 {
        return graph
            .target(
                requirement,
                name,
                ReferenceKind::SecurityScheme,
                Edition::OpenApi32,
            )
            .map(|(value, _)| value);
    }
    Err(requirement
        .error(Code::InvalidDeclaration)
        .reasoned(DiagnosticReason::UndefinedSecurityScheme))
}
fn string_value(source: &Value, key: &str) -> Result<Value, Diagnostic> {
    source
        .get(key)
        .filter(|value| value.as_str().is_some())
        .ok_or_else(|| source.error(Code::InvalidDeclaration))
}
fn optional_string_value(source: &Value, key: &str) -> Result<Option<Value>, Diagnostic> {
    source
        .get(key)
        .map(|value| {
            if value.as_str().is_some() {
                Ok(value)
            } else {
                Err(value.error(Code::InvalidDeclaration))
            }
        })
        .transpose()
}
#[derive(Clone, Copy)]
enum SecurityMechanism {
    Basic,
    Bearer,
    ApiKey,
    OAuth2,
    OpenIdConnect,
    MutualTls,
    Unsupported,
}
fn visit_oauth_flows(
    mut visit: impl FnMut(&'static str, &Value) -> Result<(), Diagnostic>,
    scheme: &Value,
    edition: Edition,
) -> Result<(), Diagnostic> {
    let mut flow = |name: &str, value: &Value| {
        let name = match (edition, name) {
            (_, "implicit") => "implicit",
            (_, "password") => "password",
            (Edition::Swagger20, "application") => "clientCredentials",
            (Edition::Swagger20, "accessCode") => "authorizationCode",
            (Edition::OpenApi30 | Edition::OpenApi31 | Edition::OpenApi32, "clientCredentials") => {
                "clientCredentials"
            }
            (Edition::OpenApi30 | Edition::OpenApi31 | Edition::OpenApi32, "authorizationCode") => {
                "authorizationCode"
            }
            (Edition::OpenApi32, "deviceAuthorization") => "deviceAuthorization",
            _ => return Err(value.error(Code::InvalidDeclaration)),
        };
        if matches!(name, "implicit" | "authorizationCode") {
            string_value(value, "authorizationUrl")?;
        }
        if name == "deviceAuthorization" {
            string_value(value, "deviceAuthorizationUrl")?;
        }
        if matches!(
            name,
            "password" | "clientCredentials" | "authorizationCode" | "deviceAuthorization"
        ) {
            string_value(value, "tokenUrl")?;
        }
        // Even endpoints unused by this flow must retain their declared string shape.
        optional_string_value(value, "authorizationUrl")?;
        optional_string_value(value, "tokenUrl")?;
        optional_string_value(value, "refreshUrl")?;
        if edition == Edition::OpenApi32 {
            optional_string_value(value, "deviceAuthorizationUrl")?;
        }
        let scopes = value
            .get("scopes")
            .ok_or_else(|| value.error(Code::InvalidDeclaration))?;
        for (_, description) in scopes
            .members()
            .ok_or_else(|| scopes.error(Code::InvalidDeclaration))?
        {
            if description.as_str().is_none() {
                return Err(description.error(Code::InvalidDeclaration));
            }
        }
        visit(name, value)
    };
    if edition == Edition::Swagger20 {
        flow(string_value(scheme, "flow")?.as_str().unwrap(), scheme)
    } else {
        let flows = scheme
            .get("flows")
            .ok_or_else(|| scheme.error(Code::InvalidDeclaration))?;
        for (name, value) in flows
            .members()
            .ok_or_else(|| flows.error(Code::InvalidDeclaration))?
        {
            if !name.starts_with("x-") {
                flow(name, &value)?;
            }
        }
        Ok(())
    }
}
/// Validate without allocating descriptions, scope maps or endpoint strings.
fn security_mechanism(scheme: &Value, edition: Edition) -> Result<SecurityMechanism, Diagnostic> {
    Ok(match string_value(scheme, "type")?.as_str().unwrap() {
        "basic" if edition == Edition::Swagger20 => SecurityMechanism::Basic,
        "http" if edition != Edition::Swagger20 => {
            let mechanism = string_value(scheme, "scheme")?;
            if mechanism.as_str().unwrap().eq_ignore_ascii_case("basic") {
                SecurityMechanism::Basic
            } else if mechanism.as_str().unwrap().eq_ignore_ascii_case("bearer") {
                optional_string_value(scheme, "bearerFormat")?;
                SecurityMechanism::Bearer
            } else {
                SecurityMechanism::Unsupported
            }
        }
        "apiKey" => {
            string_value(scheme, "name")?;
            let location = string_value(scheme, "in")?;
            ParameterLocation::parse(location.as_str().unwrap())
                .filter(|l| {
                    *l != ParameterLocation::Path
                        && (edition != Edition::Swagger20 || *l != ParameterLocation::Cookie)
                })
                .ok_or_else(|| scheme.error(Code::InvalidDeclaration))?;
            SecurityMechanism::ApiKey
        }
        "oauth2" => {
            visit_oauth_flows(|_, _| Ok(()), scheme, edition)?;
            SecurityMechanism::OAuth2
        }
        "openIdConnect" if edition != Edition::Swagger20 => {
            string_value(scheme, "openIdConnectUrl")?;
            SecurityMechanism::OpenIdConnect
        }
        "mutualTLS" if matches!(edition, Edition::OpenApi31 | Edition::OpenApi32) => {
            SecurityMechanism::MutualTls
        }
        _ => SecurityMechanism::Unsupported,
    })
}
fn describe_security(
    scheme: &Value,
    edition: Edition,
    work: &mut DescriptorWork<'_>,
) -> Result<SecurityScheme, Diagnostic> {
    work.entry(scheme)?;
    Ok(match security_mechanism(scheme, edition)? {
        SecurityMechanism::Basic => SecurityScheme::Basic,
        SecurityMechanism::Bearer => SecurityScheme::Bearer {
            format: work.optional(scheme, "bearerFormat")?,
        },
        SecurityMechanism::ApiKey => SecurityScheme::ApiKey {
            name: work.required(scheme, "name")?,
            location: ParameterLocation::parse(string_value(scheme, "in")?.as_str().unwrap())
                .expect("validated mechanism"),
        },
        SecurityMechanism::OAuth2 => {
            let mut flows = vec![];
            visit_oauth_flows(
                |name, value| {
                    work.entry(value)?;
                    let mut scopes = BTreeMap::new();
                    let source = value.get("scopes").expect("validated scopes");
                    for (name, description) in source.members().expect("validated scope map") {
                        work.entry(&description)?;
                        let name = work.text(&source, name)?;
                        let description = work.text(
                            &description,
                            description.as_str().expect("validated scope description"),
                        )?;
                        scopes.insert(name, description);
                    }
                    flows.push(OAuthFlowDescription {
                        flow: work.text(value, name)?,
                        authorization_url: work.optional(value, "authorizationUrl")?,
                        device_authorization_url: if edition == Edition::OpenApi32 {
                            work.optional(value, "deviceAuthorizationUrl")?
                        } else {
                            None
                        },
                        token_url: work.optional(value, "tokenUrl")?,
                        refresh_url: work.optional(value, "refreshUrl")?,
                        scopes,
                    });
                    Ok(())
                },
                scheme,
                edition,
            )?;
            SecurityScheme::OAuth2 { flows }
        }
        SecurityMechanism::OpenIdConnect => SecurityScheme::OpenIdConnect {
            url: work.required(scheme, "openIdConnectUrl")?,
        },
        SecurityMechanism::MutualTls => SecurityScheme::MutualTls,
        SecurityMechanism::Unsupported => {
            let ty = string_value(scheme, "type")?;
            let type_name = if ty.as_str() == Some("http") && edition != Edition::Swagger20 {
                let mut mechanism = work.required(scheme, "scheme")?;
                mechanism.make_ascii_lowercase();
                let extra = 5;
                DescriptorWork::admit(
                    &mut work.usage.bytes,
                    extra,
                    work.limits.document_bytes,
                    LimitKind::DescriptorBytes,
                    scheme,
                )?;
                mechanism.insert_str(0, "http:");
                mechanism
            } else {
                work.text(&ty, ty.as_str().unwrap())?
            };
            SecurityScheme::Unsupported { type_name }
        }
    })
}
fn serialize_legacy(
    v: &Value,
    loc: ParameterLocation,
    collection: Option<&str>,
    name: &str,
) -> Result<Serialized, Diagnostic> {
    if v.kind() == ValueKind::Object {
        return Err(v.error(Code::UnsupportedValue));
    }
    if v.kind() != ValueKind::Array {
        return serialize(v, loc, false, name);
    }
    let collection = collection.ok_or_else(|| v.error(Code::UnsupportedValue))?;
    let mut serialized = serialize(v, loc, collection == "multi", name)?;
    if collection != "csv" && collection != "multi" {
        let separator = match (collection, loc == ParameterLocation::Header) {
            ("ssv", true) => " ",
            ("ssv", false) => "%20",
            ("tsv", true) => "\t",
            ("tsv", false) => "%09",
            ("pipes", true) => "|",
            ("pipes", false) => "%7C",
            _ => unreachable!(),
        };
        let values = v
            .elements()
            .unwrap()
            .map(|v| {
                scalar(&v).map(|s| {
                    if loc == ParameterLocation::Header {
                        s
                    } else {
                        uri::encode(&s)
                    }
                })
            })
            .collect::<Result<Vec<_>, _>>()?;
        serialized.value = values.join(separator);
        serialized.pairs = vec![(
            if loc == ParameterLocation::Header {
                name.into()
            } else {
                uri::encode(name)
            },
            serialized.value.clone(),
        )];
    }
    Ok(serialized)
}
impl Operation {
    /// Inspect effective parameters without compiling body/media/security requirements.
    /// Unsupported editions and invalid parameter declarations refuse; raw inspection remains.
    pub fn parameters(&self) -> Result<Parameters, Diagnostic> {
        self.compiled(&crate::Cancellation::default())
            .map(|compiled| Parameters {
                entries: compiled.parameters.clone(),
            })
            .map_err(|e| e.bind_graph(&self.store))
    }
    fn parameter_declarations(
        &self,
        work: &mut DescriptorWork<'_>,
    ) -> Result<Vec<Parameter>, Diagnostic> {
        let node = self.value();
        let edition = self.store.edition.clone()?;
        if matches!(self.method(), Method::Query | Method::Extension(_))
            && edition != Edition::OpenApi32
            || self.method() == Method::Trace && edition == Edition::Swagger20
        {
            return Err(node.error(Code::InvalidDeclaration));
        }
        if let Some(e) = &self.record().path_error {
            return Err(e.clone());
        }
        if node.kind() != ValueKind::Object {
            return Err(node.error(Code::InvalidDeclaration));
        }
        // Operation Objects are not Reference Object positions. Treating a
        // source-composed operation as empty would silently omit its contract.
        // Unknown inspection fields elsewhere remain inert.
        if let Some(reference) = node.get("$ref") {
            return Err(reference
                .error(Code::InvalidDeclaration)
                .reasoned(DiagnosticReason::UnsupportedReferencePosition));
        }
        let mut parameters: Vec<Parameter> = vec![];
        for resolved in self.effective_parameter_references(work)? {
            let p = resolved.target.clone();
            work.declaration(&p)?;
            if edition == Edition::Swagger20
                && p.get("in")
                    .is_some_and(|v| matches!(v.as_str(), Some("body" | "formData")))
            {
                continue;
            }
            let name = work.required(&p, "name")?;
            let location = ParameterLocation::parse(&work.required(&p, "in")?)
                .ok_or_else(|| p.error(Code::UnsupportedParameter))?;
            if edition != Edition::Swagger20
                && location == ParameterLocation::Header
                && ["accept", "content-type", "authorization"]
                    .iter()
                    .any(|n| name.eq_ignore_ascii_case(n))
            {
                continue;
            }
            let required = bool_field(&p, "required", false)?;
            if location == ParameterLocation::Path {
                if !required {
                    return Err(p.error(Code::InvalidDeclaration));
                }
                let mut found = false;
                for (index, _) in self.path().match_indices('{') {
                    work.step(&p)?;
                    if self.path()[index + 1..]
                        .strip_prefix(&name)
                        .is_some_and(|tail| tail.starts_with('}'))
                    {
                        found = true;
                        break;
                    }
                }
                if !found {
                    return Err(p.error(Code::InvalidDeclaration));
                }
            }
            let (
                explode,
                style_kind,
                reserved,
                unsupported,
                collection,
                schema_context,
                content_codec,
            ) = if edition == Edition::Swagger20 {
                if location == ParameterLocation::Cookie {
                    return Err(p.error(Code::InvalidDeclaration));
                }
                let ty = work.required(&p, "type")?;
                if !matches!(
                    ty.as_str(),
                    "string" | "number" | "integer" | "boolean" | "array"
                ) {
                    return Err(p.error(Code::InvalidDeclaration));
                }
                let collection = if ty == "array" {
                    if p.get("items").is_none() {
                        return Err(p.error(Code::InvalidDeclaration));
                    }
                    let format = work
                        .optional(&p, "collectionFormat")?
                        .map_or_else(|| work.text(&p, "csv"), Ok)?;
                    if !matches!(format.as_str(), "csv" | "ssv" | "tsv" | "pipes" | "multi")
                        || format == "multi" && location != ParameterLocation::Query
                    {
                        return Err(p.error(Code::InvalidDeclaration));
                    }
                    Some(format)
                } else {
                    None
                };
                (
                    false,
                    crate::style::Style::Form,
                    false,
                    false,
                    collection,
                    Some(self.schema_description(p.clone(), work)?),
                    None,
                )
            } else if p.get("schema").is_none() && p.get("content").is_some() {
                let content = p.get("content").unwrap();
                if content.members().is_none_or(|members| members.len() != 1) {
                    return Err(content.error(Code::InvalidDeclaration));
                }
                let (media_type, authored) = content.members().unwrap().next().unwrap();
                let media_type = work.text(&authored, media_type)?;
                let parsed = parse_media(&media_type, false)
                    .map_err(|_| authored.error(Code::InvalidDeclaration))?;
                self.resolve_media(&authored, work)?;
                let charset_supported = parsed
                    .parameters
                    .get("charset")
                    .is_none_or(|s| s.eq_ignore_ascii_case("utf-8"));
                let codec = if !charset_supported {
                    None
                } else if parsed.json {
                    Some(crate::style::ContentCodec::Json)
                } else if parsed.essence == "text/plain" {
                    Some(crate::style::ContentCodec::Text)
                } else {
                    None
                };
                (
                    false,
                    crate::style::Style::Simple,
                    false,
                    codec.is_none()
                        || location == ParameterLocation::Cookie
                        || location == ParameterLocation::Header
                            && name.eq_ignore_ascii_case("cookie"),
                    None,
                    None, // schema() describes the parameter schema, never content schemas.
                    codec,
                )
            } else {
                let style = work.optional(&p, "style")?.map_or_else(
                    || {
                        work.text(&p, {
                            if matches!(
                                location,
                                ParameterLocation::Path | ParameterLocation::Header
                            ) {
                                "simple"
                            } else {
                                "form"
                            }
                        })
                    },
                    Ok,
                )?;
                if p.get("schema").is_none() == p.get("content").is_none() {
                    return Err(p.error(Code::InvalidDeclaration));
                }
                let style_kind = match (style.as_str(), location) {
                    ("simple", ParameterLocation::Path | ParameterLocation::Header) => {
                        Some(crate::style::Style::Simple)
                    }
                    ("form", ParameterLocation::Query | ParameterLocation::Cookie) => {
                        Some(crate::style::Style::Form)
                    }
                    ("deepObject", ParameterLocation::Query) => {
                        Some(crate::style::Style::DeepObject)
                    }
                    ("label", ParameterLocation::Path) => Some(crate::style::Style::Label),
                    ("matrix", ParameterLocation::Path) => Some(crate::style::Style::Matrix),
                    ("spaceDelimited", ParameterLocation::Query) => {
                        Some(crate::style::Style::SpaceDelimited)
                    }
                    ("pipeDelimited", ParameterLocation::Query) => {
                        Some(crate::style::Style::PipeDelimited)
                    }
                    _ => None,
                };
                let deep = style_kind == Some(crate::style::Style::DeepObject);
                let unsupported = style_kind.is_none()
                    || location == ParameterLocation::Header && name.eq_ignore_ascii_case("cookie");
                let explode = bool_field(&p, "explode", style == "form")?;
                if edition == Edition::OpenApi32
                    && location == ParameterLocation::Cookie
                    && p.get("schema").is_some()
                    && matches!(style.as_str(), "form" | "cookie")
                    && let Some(authored) = p.get("explode")
                    && authored.as_bool() == Some(false)
                {
                    return Err(authored
                        .error(Code::InvalidDeclaration)
                        .reasoned(DiagnosticReason::UnsupportedStyleCombination));
                }
                (
                    if deep && edition == Edition::OpenApi32 {
                        true
                    } else {
                        explode
                    },
                    style_kind.unwrap_or(crate::style::Style::Form),
                    bool_field(&p, "allowReserved", false)?
                        && (location == ParameterLocation::Query
                            || edition == Edition::OpenApi32
                                && matches!(
                                    location,
                                    ParameterLocation::Path | ParameterLocation::Cookie
                                )),
                    unsupported,
                    None,
                    p.get("schema")
                        .map(|v| self.schema_description(v, work))
                        .transpose()?,
                    None,
                )
            };
            work.entry(&p)?;
            let param = Parameter {
                resolved,
                node: p.clone(),
                name: work.text(&p, &name)?,
                location,
                required,
                explode,
                style: style_kind,
                reserved,
                unsupported,
                content: content_codec,
                collection,
                schema_context,
            };
            // The effective reference list has already merged all identities.
            parameters.push(param);
        }
        Ok(parameters)
    }
    fn compile(&self, cancellation: &crate::Cancellation) -> Result<Arc<Compiled>, Diagnostic> {
        let mut work = DescriptorWork::with_cancellation(&self.store.limits, cancellation);
        let parameters = self.parameter_declarations(&mut work)?;
        work.checkpoint()?;
        Ok(Arc::new(Compiled {
            parameters: parameters.into(),
            usage: work.usage,
            request: OnceLock::new(),
        }))
    }
    fn compiled(&self, cancellation: &crate::Cancellation) -> Result<Arc<Compiled>, Diagnostic> {
        cancellation.check()?;
        let record = self.record();
        if let Some(c) = record.compiled.get() {
            return c.clone();
        }
        let can_cache = *record.cache_slot.get_or_init(|| {
            self.store
                .cached
                .try_update(Ordering::Relaxed, Ordering::Relaxed, |n| {
                    (n < self.store.limits.cached_operations).then_some(n + 1)
                })
                .is_ok()
        });
        // Compile outside OnceLock initialization: another caller's work must
        // neither delay this caller's cancellation nor cache its Cancelled error.
        let result = self.compile(cancellation);
        cancellation.check()?;
        if can_cache && !matches!(&result, Err(e) if e.code() == Code::Cancelled) {
            let _ = record.compiled.set(result);
            record.compiled.get().expect("published parameters").clone()
        } else {
            result
        }
    }
    fn request_declarations<'a>(
        &self,
        compiled: &'a Compiled,
        cancellation: &crate::Cancellation,
    ) -> Result<&'a RequestDeclarations, Diagnostic> {
        cancellation.check()?;
        if compiled.request.get().is_none() {
            // Continue the same aggregate admission even when parameters were
            // previously compiled by inspection alone.
            let mut work = DescriptorWork::with_cancellation(&self.store.limits, cancellation);
            work.usage = compiled.usage;
            let result: Result<_, Diagnostic> = (|| {
                let body = self.body_description(&mut work)?;
                let mut responses = vec![];
                for (key, value) in self.response_declarations(&mut work)? {
                    work.entry(&value.target)?;
                    responses.push((key, value.target));
                }
                work.checkpoint()?;
                Ok(RequestDeclarations { body, responses })
            })();
            cancellation.check()?;
            if let Err(e) = &result
                && e.code() == Code::Cancelled
            {
                return Err(e.clone());
            }
            let _ = compiled.request.set(result);
        }
        compiled
            .request
            .get()
            .expect("published request declarations")
            .as_ref()
            .map_err(Clone::clone)
    }
    /// Prepare supported parameters, body and scoped credentials into a finite immutable request.
    /// Inspection remains usable when preparation returns a located refusal.
    pub fn prepare(&self, input: &Input) -> Result<PreparedRequest, Diagnostic> {
        self.prepare_with_cancellation(input, &crate::Cancellation::default())
    }
    /// Prepare with cancellation checkpoints in owned work; no remote effects occur.
    pub fn prepare_with_cancellation(
        &self,
        input: &Input,
        cancellation: &crate::Cancellation,
    ) -> Result<PreparedRequest, Diagnostic> {
        self.prepare_admitted(input, AdmittedBody::from(&input.body), cancellation)
    }
    pub(crate) fn prepare_admitted(
        &self,
        input: &Input,
        body: AdmittedBody<'_>,
        cancellation: &crate::Cancellation,
    ) -> Result<PreparedRequest, Diagnostic> {
        self.prepare_inner(input, body, cancellation)
            .map_err(|mut e| {
                if e.location.is_none() && e.context().is_none() {
                    e = e.contextual(DiagnosticContext::PreparedRequest);
                }
                e.bind_graph(&self.store)
            })
    }
    fn prepare_inner(
        &self,
        input: &Input,
        body: AdmittedBody<'_>,
        cancellation: &crate::Cancellation,
    ) -> Result<PreparedRequest, Diagnostic> {
        cancellation.check()?;
        let compiled = self.compiled(cancellation)?;
        let declarations = self.request_declarations(&compiled, cancellation)?;
        admit_input(input, &self.store.limits)?;
        if let AdmittedBody::Form(fields) = body {
            crate::form::admit_ready(fields, &self.store.limits, cancellation)?;
        }
        let root = self.store.json.root();
        let desc = self.inspect();
        let limits = self.store.limits.clone();
        let mut target = self
            .choose_target(&input.selection)
            .map_err(|e| e.election(SelectionKind::Server, input.selection.server, None))?;
        let mut path = self.path().to_owned();
        if path.contains('?') || path.contains('#') || !path.starts_with('/') {
            return Err(self.value().error(Code::InvalidDeclaration));
        }
        if path.len() > limits.target_bytes {
            return Err(Diagnostic::limited(
                LimitKind::TargetBytes,
                limits.target_bytes,
                path.len(),
            )
            .contextual(DiagnosticContext::PreparedRequest));
        }
        let mut headers = input.headers.clone();
        let mut query: Vec<(String, String)> = vec![];
        let mut cookies: Vec<(String, String)> = vec![];
        let mut used = HashSet::new();
        let mut parameter_target_bytes = 0usize;
        let mut parameter_header_bytes = 0usize;
        for p in compiled.parameters.iter() {
            cancellation.check()?;
            let values: Vec<_> = input
                .parameters
                .iter()
                .enumerate()
                .filter(|(_, v)| {
                    v.location == p.location && same_name(&v.name, &p.name, p.location)
                })
                .collect();
            if values.len() > 1 {
                let (index, value) = values[1];
                return Err(p
                    .node
                    .error(Code::InvalidSelection)
                    .reasoned(DiagnosticReason::DuplicateParameter)
                    .contextual(parameter_context(index, value)));
            }
            let Some((index, value)) = values.first().copied() else {
                if p.required {
                    return Err(p.node.error(Code::MissingInput));
                }
                continue;
            };
            used.insert(index);
            if p.unsupported {
                return Err(p
                    .node
                    .error(Code::UnsupportedParameter)
                    .reasoned(DiagnosticReason::UnsupportedStyleCombination));
            }
            let scalar = if self.store.edition == Ok(Edition::Swagger20) {
                serialize_legacy(&value.value, p.location, p.collection.as_deref(), &p.name)
            } else {
                let header = matches!(
                    p.location,
                    ParameterLocation::Header | ParameterLocation::Cookie
                );
                serialize_style(
                    &value.value,
                    p,
                    &limits,
                    cancellation,
                    if header {
                        &mut parameter_header_bytes
                    } else {
                        &mut parameter_target_bytes
                    },
                )
            }
            .map_err(|e| e.contextual(parameter_context(index, value)))?;
            match p.location {
                ParameterLocation::Path => {
                    let marker = format!("{{{}}}", p.name);
                    let count = path.matches(&marker).count();
                    let extra = scalar.value.len().saturating_sub(marker.len());
                    if count
                        .checked_mul(extra)
                        .and_then(|n| n.checked_add(path.len()))
                        .is_none_or(|n| n > limits.target_bytes)
                    {
                        return Err(Diagnostic::limited(
                            LimitKind::TargetBytes,
                            limits.target_bytes,
                            path.len().saturating_add(count.saturating_mul(extra)),
                        )
                        .contextual(parameter_context(index, value)));
                    }
                    // String::replace can grow geometrically when a large
                    // replacement is followed by a small suffix. Admit the whole
                    // result above, then allocate its exact completed length.
                    if count != 0 {
                        let size = path.len() - count * marker.len() + count * scalar.value.len();
                        path =
                            replace_path_marker(&path, &marker, &scalar.value, size, cancellation)
                                .map_err(|e| e.contextual(parameter_context(index, value)))?;
                    }
                }
                ParameterLocation::Header => {
                    headers.push(Header::new(&p.name, scalar.value.as_bytes()))
                }
                ParameterLocation::Query => query.extend(scalar.pairs),
                ParameterLocation::Cookie => cookies.extend(scalar.pairs),
            }
        }
        if used.len() != input.parameters.len() {
            let (index, value) = input
                .parameters
                .iter()
                .enumerate()
                .find(|(i, _)| !used.contains(i))
                .expect("unused input");
            return Err(Diagnostic::new(Code::InvalidSelection)
                .reasoned(DiagnosticReason::UnknownParameter)
                .contextual(parameter_context(index, value)));
        }
        if path.contains(['{', '}']) {
            return Err(self.value().error(Code::MissingInput));
        }
        // Preparation safety policy: literal dot segments can change the target in
        // HTTP URL stacks. Check the completed path because separate substitutions
        // and authored text can compose a dot segment. Empty segments remain valid.
        // Server reference resolution and encoded-dot host admission are independent.
        if path.split('/').any(|segment| matches!(segment, "." | "..")) {
            return Err(Diagnostic::new(Code::InvalidDestination)
                .contextual(DiagnosticContext::PreparedRequest));
        }
        target.truncate(target.len() - usize::from(target.ends_with('/')));
        let target_size = target.len().saturating_add(path.len());
        if target_size > limits.target_bytes {
            return Err(Diagnostic::limited(
                LimitKind::TargetBytes,
                limits.target_bytes,
                target_size,
            )
            .contextual(DiagnosticContext::PreparedRequest));
        }
        target.reserve_exact(path.len());
        append_path_piece(&mut target, &path, cancellation)?;
        drop(path);
        let (body, body_kind, media) = prepare_body(
            &self.store.graph,
            declarations.body.as_ref(),
            input,
            body,
            self.store.edition.clone()?,
            &limits,
            cancellation,
        )
        .map_err(|e| e.election(SelectionKind::Media, None, None))?;
        if let Some(media) = media {
            if headers
                .iter()
                .any(|h| h.name().eq_ignore_ascii_case("content-type"))
            {
                return Err(Diagnostic::new(Code::InvalidHeader));
            }
            headers.push(Header::new("Content-Type", media.as_bytes()));
        }
        apply_security(
            &self.store.graph,
            &root,
            desc.security.as_ref(),
            &target,
            input,
            SecurityFields {
                headers: &mut headers,
                query: &mut query,
                cookies: &mut cookies,
            },
            &limits,
        )
        .map_err(|e| e.election(SelectionKind::Security, input.selection.security, None))?;
        if !cookies.is_empty() {
            if headers
                .iter()
                .any(|h| h.name().eq_ignore_ascii_case("cookie"))
            {
                return Err(Diagnostic::new(Code::CredentialCollision));
            }
            let size = pair_bytes(&cookies, 2, cancellation)?;
            if size > limits.document_bytes {
                return Err(Diagnostic::limited(
                    LimitKind::InputBytes,
                    limits.document_bytes,
                    size,
                )
                .contextual(DiagnosticContext::PreparedRequest));
            }
            let mut value = String::with_capacity(size);
            append_pairs(&mut value, &cookies, "; ", cancellation)?;
            headers.push(Header::sensitive("Cookie", value.as_bytes()));
        }
        if !query.is_empty() {
            let size = pair_bytes(&query, 1, cancellation)?;
            let target_size = target.len().saturating_add(1).saturating_add(size);
            if target_size > limits.target_bytes {
                return Err(Diagnostic::limited(
                    LimitKind::TargetBytes,
                    limits.target_bytes,
                    target_size,
                )
                .contextual(DiagnosticContext::PreparedRequest));
            }
            target.reserve_exact(size.saturating_add(1));
            target.push('?');
            append_pairs(&mut target, &query, "&", cancellation)?;
        }
        if target.len() > limits.target_bytes {
            return Err(Diagnostic::limited(
                LimitKind::TargetBytes,
                limits.target_bytes,
                target.len(),
            )
            .contextual(DiagnosticContext::PreparedRequest));
        }
        if headers.len() > limits.headers {
            return Err(
                Diagnostic::limited(LimitKind::HeaderCount, limits.headers, headers.len())
                    .contextual(DiagnosticContext::PreparedRequest),
            );
        }
        cancellation.check()?;
        uri::origin(&target)?;
        for h in &headers {
            h.validate()?;
        }
        Ok(PreparedRequest {
            operation: self.clone(),
            compiled,
            method: self.method(),
            target,
            headers,
            body,
            body_kind,
            limits,
        })
    }
}

fn pair_bytes(
    pairs: &[(String, String)],
    separator: usize,
    cancellation: &crate::Cancellation,
) -> Result<usize, Diagnostic> {
    let mut size = 0usize;
    for (key, value) in pairs {
        cancellation.check()?;
        size = size
            .saturating_add(key.len())
            .saturating_add(value.len())
            .saturating_add(1)
            .saturating_add(separator);
    }
    Ok(size.saturating_sub(separator))
}
fn append_pairs(
    output: &mut String,
    pairs: &[(String, String)],
    separator: &str,
    cancellation: &crate::Cancellation,
) -> Result<(), Diagnostic> {
    for (index, (name, value)) in pairs.iter().enumerate() {
        cancellation.check()?;
        if index != 0 {
            output.push_str(separator);
        }
        output.push_str(name);
        output.push('=');
        output.push_str(value);
    }
    Ok(())
}

fn parameter_context(index: usize, value: &ParameterInput) -> DiagnosticContext {
    DiagnosticContext::Parameter {
        index,
        location: value.location,
        name: value.name.clone(),
    }
}
fn admit_input(input: &Input, limits: &Limits) -> Result<(), Diagnostic> {
    if input.headers.len() > limits.headers {
        return Err(Diagnostic::limited(
            LimitKind::HeaderCount,
            limits.headers,
            input.headers.len(),
        )
        .contextual(DiagnosticContext::CallerInput));
    }
    for actual in [
        input.parameters.len(),
        input.credentials.len(),
        input.selection.variables.len(),
    ] {
        if actual > limits.reference_steps {
            return Err(
                Diagnostic::limited(LimitKind::InputCount, limits.reference_steps, actual)
                    .contextual(DiagnosticContext::CallerInput),
            );
        }
    }
    let mut bytes = 0usize;
    let mut add = |n: usize| -> Result<(), Diagnostic> {
        bytes = bytes.checked_add(n).ok_or_else(|| {
            Diagnostic::limited(LimitKind::InputBytes, limits.document_bytes, usize::MAX)
                .contextual(DiagnosticContext::CallerInput)
        })?;
        if bytes > limits.document_bytes {
            Err(
                Diagnostic::limited(LimitKind::InputBytes, limits.document_bytes, bytes)
                    .contextual(DiagnosticContext::CallerInput),
            )
        } else {
            Ok(())
        }
    };
    for p in &input.parameters {
        add(p.name.len())?;
        add(p.value.raw().len())?;
    }
    for h in &input.headers {
        add(h.name().len())?;
        add(h.value().len())?;
    }
    for (k, v) in &input.selection.variables {
        add(k.len())?;
        add(v.len())?;
    }
    if let Some(s) = &input.selection.media {
        add(s.len())?;
    }
    for (k, c) in &input.credentials {
        add(k.len())?;
        match &c.value {
            CredentialValue::Basic { username, password } => {
                add(username.len())?;
                add(password.len())?;
            }
            CredentialValue::Bearer(s) | CredentialValue::ApiKey(s) => add(s.len())?,
        }
        for s in &c.origins {
            add(s.len())?;
        }
    }
    Ok(())
}

fn choose_server(
    list: Option<&Value>,
    base: Option<&str>,
    selection: &Selection,
    root: &Value,
    limits: &Limits,
) -> Result<String, Diagnostic> {
    let servers = match list {
        None => vec![],
        Some(v) => v
            .elements()
            .ok_or_else(|| v.error(Code::InvalidDeclaration))?
            .collect::<Vec<_>>(),
    };
    if servers.is_empty() {
        if selection.server.is_some_and(|i| i != 0) || !selection.variables.is_empty() {
            return Err(root.error(Code::InvalidSelection));
        }
        return uri::server_target("/", base);
    }
    let expand = |server: &Value| -> Result<String, Diagnostic> {
        let mut s = required_string(server, "url")?;
        let mut touched = HashSet::new();
        let vars = server.get("variables");
        while let Some(begin) = s.find('{') {
            let end = s[begin + 1..]
                .find('}')
                .map(|i| i + begin + 1)
                .ok_or_else(|| server.error(Code::InvalidDeclaration))?;
            let name = s[begin + 1..end].to_owned();
            let v = vars
                .as_ref()
                .and_then(|v| v.get(&name))
                .ok_or_else(|| server.error(Code::InvalidDeclaration))?;
            let default = required_string(&v, "default")?;
            let value = selection.variables.get(&name).unwrap_or(&default);
            if value.contains(['{', '}']) {
                return Err(v
                    .error(Code::InvalidDestination)
                    .with_setting(format!("server.variables.{name}"))
                    .election(SelectionKind::ServerVariable, None, Some(name.clone())));
            }
            if let Some(en) = v.get("enum") {
                let values = en
                    .elements()
                    .ok_or_else(|| en.error(Code::InvalidDeclaration))?
                    .map(|v| {
                        v.as_str()
                            .map(str::to_owned)
                            .ok_or_else(|| v.error(Code::InvalidDeclaration))
                    })
                    .collect::<Result<Vec<_>, _>>()?;
                if values.is_empty() || !values.contains(&default) {
                    return Err(v.error(Code::InvalidDeclaration));
                }
                if !values.contains(value) {
                    return Err(v
                        .error(Code::InvalidSelection)
                        .with_setting(format!("server.variables.{name}"))
                        .election(SelectionKind::ServerVariable, None, Some(name.clone())));
                }
            }
            if s.len()
                .saturating_sub(end + 1 - begin)
                .checked_add(value.len())
                .is_none_or(|n| n > limits.target_bytes)
            {
                return Err(Diagnostic::limited(
                    LimitKind::TargetBytes,
                    limits.target_bytes,
                    s.len()
                        .saturating_sub(end + 1 - begin)
                        .saturating_add(value.len()),
                )
                .contextual(DiagnosticContext::Selection {
                    kind: SelectionKind::ServerVariable,
                    index: None,
                    key: Some(name.clone()),
                }));
            }
            s.replace_range(begin..=end, value);
            touched.insert(name);
        }
        if let Some(name) = selection.variables.keys().find(|n| !touched.contains(*n)) {
            return Err(server.error(Code::InvalidSelection).election(
                SelectionKind::ServerVariable,
                None,
                Some(name.clone()),
            ));
        }
        if s.contains('}') {
            return Err(server.error(Code::InvalidSelection));
        }
        uri::server_target(&s, base).map_err(|mut e| {
            e = server.locate(e);
            if touched.len() == 1 {
                let name = touched.iter().next().unwrap();
                let variable = vars.as_ref().and_then(|v| v.get(name));
                let candidates = variable
                    .and_then(|v| v.get("enum"))
                    .and_then(|v| {
                        v.elements().map(|e| {
                            e.filter_map(|v| v.as_str().map(str::to_owned))
                                .collect::<Vec<_>>()
                        })
                    })
                    .unwrap_or_else(|| {
                        [
                            "443",
                            "api.example",
                            "https",
                            "x",
                            "https://api.example",
                            "::1",
                        ]
                        .into_iter()
                        .map(str::to_owned)
                        .collect()
                    });
                let authored = server.get("url").unwrap();
                let authored = authored.as_str().unwrap();
                if candidates.iter().any(|candidate| {
                    uri::server_target(&authored.replace(&format!("{{{name}}}"), candidate), base)
                        .is_ok()
                }) {
                    e.setting = Some(format!("server.variables.{name}"));
                }
            }
            e
        })
    };
    if let Some(i) = selection.server {
        return expand(
            servers
                .get(i)
                .ok_or_else(|| root.error(Code::InvalidSelection))?,
        );
    }
    let mut usable = vec![];
    let mut errors = vec![];
    for s in &servers {
        match expand(s) {
            Ok(s) => {
                usable.push(s);
                if usable.len() > 1 {
                    return Err(list.unwrap_or(root).error(Code::AmbiguousSelection));
                }
            }
            Err(e) => errors.push(e),
        }
    }
    match usable.len() {
        1 => Ok(usable.remove(0)),
        0 => Err(errors
            .into_iter()
            .next()
            .unwrap_or_else(|| root.error(Code::InvalidSelection))),
        _ => Err(list.unwrap_or(root).error(Code::AmbiguousSelection)),
    }
}

// A marker is matched only in the existing path. Replacement bytes are never
// reinterpreted during this substitution. The caller admits the complete size
// before allocation; byte offsets from match_indices are valid UTF-8 boundaries.
fn replace_path_marker(
    path: &str,
    marker: &str,
    replacement: &str,
    size: usize,
    cancellation: &crate::Cancellation,
) -> Result<String, Diagnostic> {
    cancellation.check()?;
    let mut result = String::with_capacity(size);
    let mut cursor = 0;
    for (offset, _) in path.match_indices(marker) {
        append_path_piece(&mut result, &path[cursor..offset], cancellation)?;
        append_path_piece(&mut result, replacement, cancellation)?;
        cursor = offset + marker.len();
    }
    append_path_piece(&mut result, &path[cursor..], cancellation)?;
    debug_assert_eq!(result.len(), size);
    Ok(result)
}
fn append_path_piece(
    out: &mut String,
    text: &str,
    cancellation: &crate::Cancellation,
) -> Result<(), Diagnostic> {
    let mut cursor = 0;
    while cursor < text.len() {
        cancellation.check()?;
        let mut end = cursor.saturating_add(4096).min(text.len());
        while !text.is_char_boundary(end) {
            end -= 1;
        }
        out.push_str(&text[cursor..end]);
        cursor = end;
    }
    cancellation.check()
}
struct Serialized {
    value: String,
    pairs: Vec<(String, String)>,
}
fn scalar(v: &Value) -> Result<String, Diagnostic> {
    match v.kind() {
        ValueKind::String => Ok(v.as_str().unwrap().to_owned()),
        ValueKind::Number | ValueKind::Boolean => Ok(v.raw().to_owned()),
        _ => Err(v.error(Code::UnsupportedValue)),
    }
}
fn serialize(
    v: &Value,
    loc: ParameterLocation,
    explode: bool,
    name: &str,
) -> Result<Serialized, Diagnostic> {
    let encode = |s: &str| {
        if loc == ParameterLocation::Header {
            s.to_owned()
        } else {
            uri::encode(s)
        }
    };
    let simple = matches!(loc, ParameterLocation::Path | ParameterLocation::Header);
    let n = encode(name);
    let (value, pairs) = match v.kind() {
        ValueKind::Null => return Err(v.error(Code::UnsupportedValue)),
        ValueKind::Array => {
            let values = v
                .elements()
                .unwrap()
                .map(|v| scalar(&v).map(|s| encode(&s)))
                .collect::<Result<Vec<_>, _>>()?;
            if values.is_empty() {
                return Err(v.error(Code::UnsupportedValue));
            }
            let joined = values.join(",");
            let pairs = if explode && !simple {
                values.into_iter().map(|v| (n.clone(), v)).collect()
            } else {
                vec![(n, joined.clone())]
            };
            (joined, pairs)
        }
        ValueKind::Object => {
            let values = v
                .members()
                .unwrap()
                .map(|(k, v)| scalar(&v).map(|s| (encode(k), encode(&s))))
                .collect::<Result<Vec<_>, _>>()?;
            if values.is_empty() {
                return Err(v.error(Code::UnsupportedValue));
            }
            let joined = values
                .iter()
                .map(|(k, v)| {
                    if explode {
                        format!("{k}={v}")
                    } else {
                        format!("{k},{v}")
                    }
                })
                .collect::<Vec<_>>()
                .join(",");
            let pairs = if explode && !simple {
                values
            } else {
                vec![(n, joined.clone())]
            };
            (joined, pairs)
        }
        _ => {
            let s = encode(&scalar(v)?);
            (s.clone(), vec![(n, s)])
        }
    };
    Ok(Serialized { value, pairs })
}

fn walk_parameter(
    value: &Value,
    parameter: &Parameter,
    emit: impl FnMut(crate::style::Event<'_>) -> Result<(), Diagnostic>,
) -> Result<(), Diagnostic> {
    if let Some(codec) = parameter.content {
        crate::style::walk_content(value, &parameter.name, codec, emit)
    } else {
        crate::style::walk(
            value,
            &parameter.name,
            crate::style::Config {
                style: parameter.style,
                explode: parameter.explode,
                reserved: parameter.reserved,
            },
            false,
            &parameter.node,
            emit,
        )
    }
}

fn serialize_style(
    value: &Value,
    parameter: &Parameter,
    limits: &Limits,
    cancel: &crate::Cancellation,
    aggregate: &mut usize,
) -> Result<Serialized, Diagnostic> {
    use crate::style::{Atom, Config, Escape, Event};
    let location = parameter.location;
    let pairs = matches!(
        location,
        ParameterLocation::Query | ParameterLocation::Cookie
    );
    let header = matches!(
        location,
        ParameterLocation::Header | ParameterLocation::Cookie
    );
    let (maximum, kind) = if header {
        (limits.document_bytes, LimitKind::InputBytes)
    } else {
        (limits.target_bytes, LimitKind::TargetBytes)
    };
    let cfg = Config {
        style: parameter.style,
        explode: parameter.explode,
        reserved: parameter.reserved,
    };
    let mode = if location == ParameterLocation::Header {
        Escape::Literal
    } else if parameter.content.is_some() && location == ParameterLocation::Query {
        Escape::FormContent
    } else {
        Escape::Style {
            reserved: cfg.reserved,
            path: location == ParameterLocation::Path,
            cookie: location == ParameterLocation::Cookie,
            protected: cfg.protected(value),
        }
    };
    if let Some(codec) = parameter.content
        && location == ParameterLocation::Header
    {
        let bytes = match codec {
            crate::style::ContentCodec::Json => value.raw().as_bytes(),
            crate::style::ContentCodec::Text => crate::style::bytes(value)?,
        };
        for chunk in bytes.chunks(4096) {
            cancel.check()?;
            if chunk.iter().any(|b| *b == 127 || *b < 32 && *b != b'\t') {
                return Err(value.error(Code::InvalidHeader));
            }
        }
        if bytes.first().is_some_and(|b| b" \t".contains(b))
            || bytes.last().is_some_and(|b| b" \t".contains(b))
        {
            return Err(value.error(Code::InvalidHeader));
        }
    }
    // Count each output name/value before any encoded strings are allocated.
    // The count vector has one entry per emitted pair, bounded independently by nodes.
    let mut lengths: Vec<(usize, usize)> = Vec::new();
    let mut total = *aggregate;
    walk_parameter(value, parameter, |event| {
        cancel.check()?;
        let mut add = |n: usize| -> Result<(), Diagnostic> {
            total = total
                .checked_add(n)
                .ok_or_else(|| Diagnostic::limited(kind, maximum, usize::MAX))?;
            if total > maximum {
                return Err(Diagnostic::limited(kind, maximum, total));
            }
            Ok(())
        };
        match event {
            Event::Begin(name) => {
                if lengths.len() >= limits.nodes {
                    return Err(Diagnostic::limited(
                        LimitKind::Nodes,
                        limits.nodes,
                        lengths.len().saturating_add(1),
                    ));
                }
                let mut size = 0;
                if pairs {
                    if !lengths.is_empty() {
                        add(if location == ParameterLocation::Cookie {
                            2
                        } else {
                            1
                        })?;
                    }
                    add(1)?; // '='
                    for text in name.pieces() {
                        crate::style::encode(text.as_bytes(), mode.name(), |chunk| {
                            cancel.check()?;
                            add(chunk.len())?;
                            size += chunk.len();
                            Ok(())
                        })?;
                    }
                }
                crate::form::reserve_admitted(&mut lengths, limits.nodes);
                lengths.push((size, 0));
            }
            Event::Atom(atom) => {
                let (bytes, escaping) = match atom {
                    Atom::Scalar(v) => (crate::style::bytes(v)?, mode),
                    Atom::Json(v) => (v.raw().as_bytes(), mode),
                    Atom::Key(k) => (k.as_bytes(), mode.name()),
                    Atom::Separator(s) => (s, Escape::Literal),
                    Atom::Delimiter(s) => (s, mode.delimiter(s)),
                };
                crate::style::encode(bytes, escaping, |chunk| {
                    cancel.check()?;
                    add(chunk.len())?;
                    lengths.last_mut().unwrap().1 += chunk.len();
                    Ok(())
                })?;
            }
            Event::End | Event::Check => {}
        }
        Ok(())
    })?;
    let mut output = Vec::with_capacity(lengths.len());
    let mut ordinal = 0;
    walk_parameter(value, parameter, |event| {
        cancel.check()?;
        match event {
            Event::Begin(name) => {
                let (n, v) = lengths[ordinal];
                ordinal += 1;
                let mut encoded = String::with_capacity(n);
                if pairs {
                    for text in name.pieces() {
                        crate::style::encode(text.as_bytes(), mode.name(), |chunk| {
                            cancel.check()?;
                            encoded.push_str(std::str::from_utf8(chunk).expect("encoded name"));
                            Ok(())
                        })?;
                    }
                }
                output.push((encoded, String::with_capacity(v)));
            }
            Event::Atom(atom) => {
                let (bytes, escaping) = match atom {
                    Atom::Scalar(v) => (crate::style::bytes(v)?, mode),
                    Atom::Json(v) => (v.raw().as_bytes(), mode),
                    Atom::Key(k) => (k.as_bytes(), mode.name()),
                    Atom::Separator(s) => (s, Escape::Literal),
                    Atom::Delimiter(s) => (s, mode.delimiter(s)),
                };
                // Literal UTF-8 chunks can cross scalar boundaries; append bytes to the
                // admitted UTF-8 value via one whole logical atom for headers.
                if matches!(escaping, Escape::Literal) {
                    for _ in bytes.chunks(4096) {
                        cancel.check()?;
                    }
                    output
                        .last_mut()
                        .unwrap()
                        .1
                        .push_str(std::str::from_utf8(bytes).expect("logical text"));
                } else {
                    crate::style::encode(bytes, escaping, |chunk| {
                        cancel.check()?;
                        output
                            .last_mut()
                            .unwrap()
                            .1
                            .push_str(std::str::from_utf8(chunk).expect("ASCII encoding"));
                        Ok(())
                    })?;
                }
            }
            Event::End | Event::Check => {}
        }
        Ok(())
    })?;
    *aggregate = total;
    if pairs {
        Ok(Serialized {
            value: String::new(),
            pairs: output,
        })
    } else {
        Ok(Serialized {
            value: output.pop().map(|(_, v)| v).unwrap_or_default(),
            pairs: Vec::new(),
        })
    }
}

pub(crate) struct Media {
    pub(crate) essence: String,
    pub(crate) parameters: BTreeMap<String, String>,
    pub(crate) json: bool,
}
pub(crate) fn parse_media(s: &str, ranges: bool) -> Result<Media, Diagnostic> {
    let bad = || Diagnostic::new(Code::InvalidMedia);
    let (head, tail) = s.split_once(';').map_or((s, ""), |(a, b)| (a, b));
    let essence = head.trim_matches([' ', '\t']);
    let (ty, sub) = essence.split_once('/').ok_or_else(bad)?;
    if ty.is_empty() || sub.is_empty() || !ty.bytes().all(token) || !sub.bytes().all(token) {
        return Err(bad());
    }
    if (ty.contains('*') || sub.contains('*'))
        && (!ranges || !(ty == "*" && sub == "*" || !ty.contains('*') && sub == "*"))
    {
        return Err(bad());
    }
    let mut parameters = BTreeMap::new();
    let mut rest = tail;
    while !rest.is_empty() {
        rest = rest.trim_start_matches([' ', '\t']);
        if rest.is_empty() {
            break;
        }
        if let Some(after) = rest.strip_prefix(';') {
            rest = after;
            continue;
        }
        let (name, after) = rest.split_once('=').ok_or_else(bad)?;
        if name.is_empty() || !name.bytes().all(token) {
            return Err(bad());
        }
        let (value, after) = if let Some(quoted) = after.strip_prefix('"') {
            let mut value = String::new();
            let mut chars = quoted.char_indices();
            let mut end = None;
            while let Some((i, c)) = chars.next() {
                if c == '"' {
                    end = Some(i + 1);
                    break;
                }
                let c = if c == '\\' {
                    chars.next().ok_or_else(bad)?.1
                } else {
                    c
                };
                if c.is_ascii_control() && c != '\t' {
                    return Err(bad());
                }
                value.push(c);
            }
            let end = end.ok_or_else(bad)?;
            (value, &quoted[end..])
        } else {
            let end = after.bytes().position(|b| !token(b)).unwrap_or(after.len());
            if end == 0 {
                return Err(bad());
            }
            (after[..end].to_owned(), &after[end..])
        };
        if parameters
            .insert(name.to_ascii_lowercase(), value)
            .is_some()
        {
            return Err(bad());
        }
        let after = after.trim_start_matches([' ', '\t']);
        if after.is_empty() {
            break;
        }
        rest = after.strip_prefix(';').ok_or_else(bad)?;
    }
    let json = ty.eq_ignore_ascii_case("application")
        && (sub.eq_ignore_ascii_case("json") || sub.to_ascii_lowercase().ends_with("+json"));
    Ok(Media {
        essence: essence.to_ascii_lowercase(),
        parameters,
        json,
    })
}
pub(crate) fn media_type(s: &str) -> Result<(String, bool), Diagnostic> {
    let m = parse_media(s, false)?;
    Ok((m.essence, m.json))
}
pub(crate) fn media_matches(key: &Media, selected: &Media) -> Option<(u8, usize)> {
    let (ty, sub) = key.essence.split_once('/')?;
    let (st, ss) = selected.essence.split_once('/')?;
    if !(ty == "*" || ty == st)
        || !(sub == "*" || sub == ss)
        || !key.parameters.iter().all(|(k, v)| {
            selected.parameters.get(k).is_some_and(|s| {
                if k == "charset" {
                    s.eq_ignore_ascii_case(v)
                } else {
                    s == v
                }
            })
        })
    {
        return None;
    }
    Some((
        u8::from(ty != "*") + u8::from(sub != "*"),
        key.parameters.len(),
    ))
}
pub(crate) fn token(b: u8) -> bool {
    b.is_ascii_alphanumeric() || b"!#$%&'*+-.^_`|~".contains(&b)
}
type BodyEncoding = (Option<Arc<[u8]>>, &'static str, Option<String>);
fn prepare_body(
    graph: &crate::graph::Graph,
    decl: Option<&BodyDeclaration>,
    input: &Input,
    body: AdmittedBody<'_>,
    edition: Edition,
    limits: &Limits,
    cancellation: &crate::Cancellation,
) -> Result<BodyEncoding, Diagnostic> {
    let absent = matches!(body, AdmittedBody::Absent);
    let Some(decl) = decl else {
        if absent && input.selection.media.is_none() {
            return Ok((None, "absent", None));
        }
        return Err(Diagnostic::new(Code::InvalidSelection));
    };
    let source = &decl.declaration.target;
    if absent {
        if decl.required {
            return Err(source.error(Code::MissingBody));
        }
        return Ok((None, "absent", None));
    }
    let mut media = vec![];
    for representation in &decl.media {
        if let Ok(parsed) = parse_media(&representation.media_type, true) {
            media.push((
                representation.media_type.clone(),
                parsed,
                representation.authored.clone(),
            ));
        }
    }
    let content = source;
    let selected = if decl.media_unrestricted {
        if let Some(s) = &input.selection.media {
            parse_media(s, false).map_err(|e| source.error(e.code))?;
            s.trim_matches([' ', '\t']).to_owned()
        } else if matches!(body, AdmittedBody::Json(_)) {
            "application/json".into()
        } else {
            return Err(source.error(Code::InvalidSelection));
        }
    } else if let Some(s) = &input.selection.media {
        let selected = parse_media(s, false).map_err(|e| content.error(e.code))?;
        let mut matching = media
            .iter()
            .filter_map(|(_, key, value)| media_matches(key, &selected).map(|rank| (rank, value)))
            .collect::<Vec<_>>();
        matching.sort_by_key(|(rank, _)| *rank);
        let Some((rank, value)) = matching.pop() else {
            return Err(content.error(Code::InvalidSelection));
        };
        if matching.last().is_some_and(|(r, _)| *r == rank) {
            return Err(content.error(Code::AmbiguousSelection));
        }
        let _ = value;
        s.trim_matches([' ', '\t']).to_owned()
    } else {
        let concrete = media
            .iter()
            .filter(|(_, m, _)| !m.essence.contains('*'))
            .collect::<Vec<_>>();
        let usable = concrete
            .iter()
            .filter(|(_, m, _)| {
                (!matches!(body, AdmittedBody::Json(_)) || m.json)
                    && (!matches!(body, AdmittedBody::Form(_))
                        || m.essence.starts_with("multipart/")
                        || m.essence == "application/x-www-form-urlencoded")
            })
            .collect::<Vec<_>>();
        match usable.len() {
            0 => {
                if !concrete.is_empty() {
                    return Err(source.error(Code::UnsupportedMedia));
                }
                return Err(content.error(if media.is_empty() {
                    Code::InvalidMedia
                } else {
                    Code::InvalidSelection
                }));
            }
            1 => usable[0].0.clone(),
            _ => return Err(content.error(Code::AmbiguousSelection)),
        }
    };
    if edition == Edition::Swagger20
        && source
            .get("in")
            .is_some_and(|v| v.as_str() == Some("formData"))
        && !matches!(
            parse_media(&selected, false)?.essence.as_str(),
            "application/x-www-form-urlencoded" | "multipart/form-data"
        )
    {
        return Err(source.error(Code::UnsupportedMedia));
    }
    if let AdmittedBody::Form(form) = body {
        let selected_parsed = parse_media(&selected, false)?;
        let representation = decl
            .media
            .iter()
            .filter_map(|m| {
                parse_media(&m.media_type, true)
                    .ok()
                    .and_then(|key| media_matches(&key, &selected_parsed).map(|rank| (rank, m)))
            })
            .max_by_key(|(rank, _)| *rank)
            .map(|(_, m)| m)
            .ok_or_else(|| source.error(Code::UnsupportedMedia))?;
        let (bytes, media) = crate::form::encode(
            graph,
            form,
            representation,
            &selected,
            edition,
            limits,
            cancellation,
        )?;
        return Ok((Some(bytes), "form", Some(media)));
    }
    let (bytes, kind) = match body {
        AdmittedBody::Absent | AdmittedBody::Form(_) => unreachable!(),
        AdmittedBody::Json(v) => {
            if v.raw().len() > limits.body_bytes {
                return Err(v.locate(Diagnostic::limited(
                    LimitKind::BodyBytes,
                    limits.body_bytes,
                    v.raw().len(),
                )));
            }
            if !media_type(&selected)?.1 {
                return Err(source.error(Code::UnsupportedMedia));
            }
            (Arc::<[u8]>::from(v.raw().as_bytes()), "json")
        }
        AdmittedBody::Raw(b) => (b.clone(), "raw"),
    };
    if bytes.len() > limits.body_bytes {
        return Err(
            Diagnostic::limited(LimitKind::BodyBytes, limits.body_bytes, bytes.len())
                .contextual(DiagnosticContext::CallerInput),
        );
    }
    Ok((Some(bytes), kind, Some(selected)))
}
struct SecurityFields<'a> {
    headers: &'a mut Vec<Header>,
    query: &'a mut Vec<(String, String)>,
    cookies: &'a mut Vec<(String, String)>,
}
fn apply_security(
    graph: &crate::graph::Graph,
    root: &Value,
    requirements: Option<&Value>,
    target: &str,
    input: &Input,
    fields: SecurityFields<'_>,
    limits: &Limits,
) -> Result<(), Diagnostic> {
    let SecurityFields {
        headers,
        query,
        cookies,
    } = fields;
    let choices = match requirements {
        None => vec![],
        Some(v) => v
            .elements()
            .ok_or_else(|| v.error(Code::InvalidDeclaration))?
            .collect::<Vec<_>>(),
    };
    if choices.is_empty() {
        if input.selection.security.is_some() {
            return Err(root.error(Code::InvalidSelection));
        }
        return Ok(());
    }
    let index = match input.selection.security {
        Some(i) => i,
        None if choices.len() == 1 => 0,
        None => return Err(requirements.unwrap().error(Code::AmbiguousSelection)),
    };
    let choice = choices
        .get(index)
        .ok_or_else(|| root.error(Code::InvalidSelection))?;
    let entries = choice
        .members()
        .ok_or_else(|| choice.error(Code::InvalidDeclaration))?;
    if entries.len() > limits.reference_steps {
        return Err(choice.locate(Diagnostic::limited(
            LimitKind::InputCount,
            limits.reference_steps,
            entries.len(),
        )));
    }
    if entries.len() > limits.descriptor_work {
        return Err(choice.locate(Diagnostic::limited(
            LimitKind::DescriptorWork,
            limits.descriptor_work,
            entries.len(),
        )));
    }
    let mut work = DescriptorWork::new(limits);
    let origin = uri::origin(target)?;
    for (name, scopes) in entries {
        if scopes.kind() != ValueKind::Array {
            return Err(scopes.error(Code::InvalidDeclaration));
        }
        let source = security_source(graph, root, name, &scopes)?;
        let scheme = work
            .resolve(graph, &source, ReferenceKind::SecurityScheme)?
            .target;
        let kind = security_mechanism(&scheme, Edition::detect(root)?)?;
        if matches!(
            kind,
            SecurityMechanism::Unsupported | SecurityMechanism::MutualTls
        ) {
            return Err(scheme.error(Code::UnsupportedSecurity));
        }
        if scopes.elements().unwrap().any(|v| v.as_str().is_none()) {
            return Err(scopes.error(Code::InvalidDeclaration));
        }
        if matches!(
            Edition::detect(root)?,
            Edition::Swagger20 | Edition::OpenApi30
        ) && !matches!(
            kind,
            SecurityMechanism::OAuth2 | SecurityMechanism::OpenIdConnect
        ) && scopes.elements().unwrap().next().is_some()
        {
            return Err(scopes.error(Code::InvalidDeclaration));
        }
        let credential = input
            .credentials
            .get(name)
            .ok_or_else(|| scheme.error(Code::MissingCredential))?;
        if !credential
            .origins
            .iter()
            .any(|s| uri::origin(s).is_ok_and(|s| s == origin))
        {
            return Err(scheme.error(Code::CredentialOrigin));
        }
        let (location, name, value) = if !matches!(kind, SecurityMechanism::ApiKey) {
            let mechanism = if matches!(kind, SecurityMechanism::Basic) {
                "basic"
            } else {
                "bearer"
            };
            let value = match (mechanism, &credential.value) {
                ("basic", CredentialValue::Basic { username, password }) => {
                    if username.contains(':')
                        || username
                            .chars()
                            .chain(password.chars())
                            .any(char::is_control)
                    {
                        return Err(scheme.error(Code::MissingCredential));
                    }
                    format!(
                        "Basic {}",
                        base64::engine::general_purpose::STANDARD
                            .encode(format!("{username}:{password}").as_bytes())
                    )
                }
                ("bearer", CredentialValue::Bearer(s)) => {
                    let unpadded = s.trim_end_matches('=');
                    if unpadded.is_empty()
                        || !unpadded
                            .bytes()
                            .all(|b| b.is_ascii_alphanumeric() || b"-._~+/".contains(&b))
                    {
                        return Err(scheme.error(Code::MissingCredential));
                    }
                    format!("Bearer {s}")
                }
                ("basic" | "bearer", _) => return Err(scheme.error(Code::MissingCredential)),
                _ => return Err(scheme.error(Code::UnsupportedSecurity)),
            };
            (ParameterLocation::Header, "Authorization".to_owned(), value)
        } else {
            let name = required_string(&scheme, "name")?;
            let location = ParameterLocation::parse(&required_string(&scheme, "in")?)
                .filter(|l| *l != ParameterLocation::Path)
                .ok_or_else(|| scheme.error(Code::InvalidDeclaration))?;
            let CredentialValue::ApiKey(value) = &credential.value else {
                return Err(scheme.error(Code::MissingCredential));
            };
            (location, name, value.clone())
        };
        match location {
            ParameterLocation::Header => {
                if headers.iter().any(|h| h.name().eq_ignore_ascii_case(&name)) {
                    return Err(scheme.error(Code::CredentialCollision));
                }
                headers.push(Header::sensitive(&name, value.as_bytes()));
            }
            ParameterLocation::Query | ParameterLocation::Cookie => {
                if name.is_empty() || name.contains('%') && uri::percent_decode(&name).is_err() {
                    return Err(scheme.error(Code::InvalidQuery));
                }
                let pairs = if location == ParameterLocation::Query {
                    &mut *query
                } else {
                    &mut *cookies
                };
                let encoded = uri::encode(&name);
                if pairs.iter().any(|(n, _)| n == &encoded) {
                    return Err(scheme.error(Code::CredentialCollision));
                }
                pairs.push((encoded, uri::encode(&value)));
            }
            ParameterLocation::Path => unreachable!(),
        }
    }
    Ok(())
}
impl PreparedRequest {
    /// Selected HTTP method.
    pub fn method(&self) -> Method {
        self.method.clone()
    }
    /// Exact resolved transport target. May contain query credentials; do not log indiscriminately.
    pub fn target(&self) -> &str {
        &self.target
    }
    /// Ordered complete header lines. Sensitive fields remain available for actual dispatch.
    pub fn headers(&self) -> &[Header] {
        &self.headers
    }
    /// Finite owned payload, distinct from absence.
    pub fn body(&self) -> Option<&[u8]> {
        self.body.as_deref()
    }
    /// Payload category: absent, json, raw or form.
    pub fn body_kind(&self) -> &'static str {
        self.body_kind
    }
    /// Operation owner retained by this request.
    pub fn operation(&self) -> Operation {
        self.operation.clone()
    }
    /// Match the exact status, then range, then default response declaration.
    pub fn response_declaration(&self, status: u16) -> Option<(&str, Value)> {
        let declarations = self
            .compiled
            .request
            .get()
            .expect("prepared request declarations")
            .as_ref()
            .expect("successful request declarations");
        let exact = status.to_string();
        let range = format!("{}XX", status / 100);
        for key in [exact.as_str(), range.as_str(), "default"] {
            if let Some((k, v)) = declarations.responses.iter().find(|(k, _)| k == key) {
                return Some((k, v.clone()));
            }
        }
        None
    }
}

impl std::fmt::Debug for Selection {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Selection")
            .field("server", &self.server)
            .field("security", &self.security)
            .field("variable_count", &self.variables.len())
            .field("has_media", &self.media.is_some())
            .finish()
    }
}
impl std::fmt::Debug for Input {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Input")
            .field("parameter_count", &self.parameters.len())
            .field("header_count", &self.headers.len())
            .field("credential_count", &self.credentials.len())
            .finish()
    }
}

impl std::fmt::Debug for Body {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Absent => f.write_str("Body::Absent"),
            Self::Json(v) => f.debug_tuple("Body::Json").field(&v.raw().len()).finish(),
            Self::Raw(b) => f.debug_tuple("Body::Raw").field(&b.len()).finish(),
            Self::Form(form) => form.fmt(f),
        }
    }
}

#[cfg(test)]
mod parameter_work_tests {
    use super::*;
    use crate::{Cancellation, Document};
    use serde_json::json;

    thread_local! {
        pub(super) static CHECKPOINTS: std::cell::Cell<usize> = const { std::cell::Cell::new(0) };
    }

    fn document(n: usize, swagger: bool, limits: Limits) -> Document {
        let parameters: Vec<_> = (0..n)
            .map(|i| {
                if swagger {
                    json!({"in":"query","name":format!("p{i}"),"type":"string"})
                } else {
                    json!({"in":"query","name":format!("p{i}"),"schema":{"type":"string"}})
                }
            })
            .collect();
        let mut source =
            json!({"paths":{"/":{"get":{"operationId":"read","parameters":parameters}}}});
        if swagger {
            source["swagger"] = json!("2.0");
            source["schemes"] = json!(["https"]);
            source["host"] = json!("api.example");
        } else {
            source["openapi"] = json!("3.1.2");
            source["servers"] = json!([{"url":"https://api.example"}]);
        }
        Document::parse(source.to_string(), None, limits).unwrap()
    }

    #[test]
    fn parameter_identity_and_declaration_work_is_linear() {
        for n in [1_000, 2_000, 4_000, 8_000] {
            let doc = document(n, false, Limits::default());
            let op = doc.operation_id("read").unwrap();
            let mut work = DescriptorWork::new(&doc.0.limits);
            let parameters = op.parameter_declarations(&mut work).unwrap();
            // One reference visit, one map operation and one declaration visit
            // per authored parameter. There is no pairwise parameter search.
            assert_eq!(parameters.len(), n);
            assert_eq!(work.usage.references, 3 * n);
            assert!(work.usage.nodes <= 16 * n);
            println!(
                "N={n}: descriptor_work={}, nodes={}",
                work.usage.references, work.usage.nodes
            );
        }
    }

    #[test]
    fn cold_cancellation_stops_at_a_work_boundary_and_does_not_poison_cache() {
        let n = 8_000;
        let doc = document(n, false, Limits::default());
        let op = doc.operation_id("read").unwrap();
        let cancel = Cancellation::default();
        CHECKPOINTS.with(|count| count.set(0));
        let guard = crate::schema::cancel_after_for_test(1_000);
        let error = op
            .prepare_with_cancellation(&Input::default(), &cancel)
            .unwrap_err();
        assert_eq!(error.code(), Code::Cancelled);
        assert_eq!(CHECKPOINTS.with(|count| count.get()), 1_000);
        assert!(op.record().compiled.get().is_none());
        drop(guard);

        CHECKPOINTS.with(|count| count.set(0));
        op.prepare(&Input::default()).unwrap();
        assert!(CHECKPOINTS.with(|count| count.get()) > n);
        let first = op.parameters().unwrap();
        let second = op.clone().parameters().unwrap();
        assert_eq!(first.iter().len(), n);
        assert!(Arc::ptr_eq(&first.entries, &second.entries));
        let compiled = op.record().compiled.get().unwrap().as_ref().unwrap();
        assert!(Arc::ptr_eq(&first.entries, &compiled.parameters));
    }

    #[test]
    fn request_compilation_cancellation_keeps_only_completed_parameters() {
        let doc = document(8_000, true, Limits::default());
        let op = doc.operation_id("read").unwrap();
        let parameters = op.parameters().unwrap();
        let compiled = op.record().compiled.get().unwrap().as_ref().unwrap();
        assert!(compiled.request.get().is_none());
        let cancel = Cancellation::default();
        let guard = crate::schema::cancel_after_for_test(1_000);
        CHECKPOINTS.with(|count| count.set(0));
        assert_eq!(
            op.prepare_with_cancellation(&Input::default(), &cancel)
                .unwrap_err()
                .code(),
            Code::Cancelled
        );
        assert_eq!(CHECKPOINTS.with(|count| count.get()), 1_000);
        assert!(compiled.request.get().is_none());
        drop(guard);
        op.prepare(&Input::default()).unwrap();
        assert!(compiled.request.get().unwrap().is_ok());
        assert!(Arc::ptr_eq(
            &parameters.entries,
            &op.parameters().unwrap().entries
        ));
    }

    #[test]
    fn cached_parameters_resume_the_aggregate_request_budget() {
        let doc = document(
            1,
            false,
            Limits {
                descriptor_work: 4,
                ..Limits::default()
            },
        );
        let source = doc.root().raw().to_owned();
        let mut source: serde_json::Value = serde_json::from_str(&source).unwrap();
        source["paths"]["/"]["get"]["responses"] = json!({"200":{"description":"ok"}});
        let doc = Document::parse(source.to_string(), None, doc.0.limits.clone()).unwrap();
        let op = doc.operation_id("read").unwrap();
        assert_eq!(op.parameters().unwrap().iter().len(), 1);
        let error = op.prepare(&Input::default()).unwrap_err();
        assert_eq!(error.code(), Code::Limit);
        assert_eq!(
            error.reason(),
            Some(&DiagnosticReason::Limit {
                kind: LimitKind::DescriptorWork,
                maximum: 4,
                actual: 5,
            })
        );
        assert_eq!(op.parameters().unwrap().iter().len(), 1);
    }
}
