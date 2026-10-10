use crate::{
    Code, Diagnostic, Edition, Header, Limits, Method, Operation, ReferenceKind, Value, ValueKind,
};
use crate::{DiagnosticContext, DiagnosticReason, LimitKind, SelectionKind, document, uri};
use base64::Engine;
use std::{
    collections::{BTreeMap, HashSet},
    sync::{Arc, atomic::Ordering},
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
pub enum Body {
    /// No request body.
    #[default]
    Absent,
    /// Exact JSON token/subtree.
    Json(Value),
    /// Explicit finite bytes for a concretely elected media type.
    Raw(Arc<[u8]>),
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
    unsupported: bool,
    collection: Option<String>,
    schema_context: Option<SchemaDescription>,
}
/// Effective parameter declarations retaining their exact source owners.
/// Structured refusal leaves Operation::inspect available for raw inspection.
#[derive(Clone, Debug)]
pub struct Parameters {
    entries: Vec<Parameter>,
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
    pub fn schema_description(&self) -> Option<SchemaDescription> {
        self.schema_context.clone()
    }
}
/// A schema value and its interpretation context; no evaluation is performed.
#[derive(Clone, Debug)]
pub struct SchemaDescription {
    /// Original schema value, including dialect and reference keywords.
    pub value: Value,
    /// The surrounding OpenAPI vocabulary.
    pub edition: crate::Edition,
    /// Effective JSON Schema dialect URI, absent for Swagger/OAS 3.0 vocabularies.
    pub dialect: Option<String>,
}
/// An effective representation, retaining its original declaration.
#[derive(Clone, Debug)]
pub struct MediaDescription {
    /// Authored media type or range.
    pub media_type: String,
    /// Original Media Type Object, or Swagger consumes/produces string.
    pub authored: Value,
    /// Opaque payload schema.
    pub schema: Option<SchemaDescription>,
    /// Opaque encoding declarations.
    pub encoding: Option<Value>,
    /// Opaque sequential-item schema when declared.
    pub item_schema: Option<SchemaDescription>,
}
/// A logical request body independent of the edition's declaration syntax.
#[derive(Clone, Debug)]
pub struct RequestBodyDescription {
    /// Original declaration and local-reference provenance.
    pub declaration: crate::ResolvedReference,
    /// Whether omitting the logical body is permitted.
    pub required: bool,
    /// Effective request representations. Malformed media keys are excluded;
    /// their original values remain accessible through `declaration`.
    pub media: Vec<MediaDescription>,
    /// No media constraint was declared; an explicit concrete type or the JSON codec determines it.
    pub media_unrestricted: bool,
    /// Logical form fields, or an explicit unresolved schema-derived shape.
    pub fields: BodyFields,
}
/// A logical form field; automatic form serialization is qualified separately.
#[derive(Clone, Debug)]
pub struct BodyFieldDescription {
    /// Logical field name.
    pub name: String,
    /// Whether this field is required.
    pub required: bool,
    /// Opaque field schema and vocabulary.
    pub schema: SchemaDescription,
    /// Authored declaration, retaining format/encoding details.
    pub authored: Value,
}
/// Completeness of logical form-field inspection.
#[derive(Clone, Debug)]
pub enum BodyFields {
    /// The body has no form representations.
    NotApplicable,
    /// Explicit logical fields are available.
    Declared(Vec<BodyFieldDescription>),
    /// Form representations exist but schema-derived fields have not been resolved.
    Opaque,
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
    pub media: Vec<MediaDescription>,
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
    /// Common flow name: implicit, password, clientCredentials or authorizationCode.
    pub flow: String,
    /// Authorization endpoint when applicable.
    pub authorization_url: Option<String>,
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
    pub request_body: Option<RequestBodyDescription>,
    /// Effective status and representation declarations.
    pub responses: Vec<ResponseDescription>,
    /// Effective security alternatives.
    pub security: Vec<SecurityAlternative>,
}

pub(crate) struct Compiled {
    parameters: Vec<Parameter>,
    body: Option<RequestBodyDescription>,
    pub responses: Vec<(String, Value)>,
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

fn optional_string(node: &Value, key: &str) -> Result<Option<String>, Diagnostic> {
    node.get(key)
        .map(|v| {
            v.as_str()
                .map(str::to_owned)
                .ok_or_else(|| v.error(Code::InvalidDeclaration))
        })
        .transpose()
}

impl Operation {
    fn schema_description(&self, value: Value) -> SchemaDescription {
        let edition = self.store.edition.clone().expect("edition checked");
        let dialect = if matches!(edition, Edition::OpenApi31 | Edition::OpenApi32) {
            value
                .get("$schema")
                .or_else(|| self.store.json.root().get("jsonSchemaDialect"))
                .and_then(|v| v.as_str().map(str::to_owned))
                .or_else(|| {
                    Some(
                        if edition == Edition::OpenApi31 {
                            "https://spec.openapis.org/oas/3.1/dialect/base"
                        } else {
                            "https://spec.openapis.org/oas/3.2/dialect/base"
                        }
                        .into(),
                    )
                })
        } else {
            None
        };
        SchemaDescription {
            value,
            edition,
            dialect,
        }
    }
    fn effective_parameter_references(&self) -> Result<Vec<crate::ResolvedReference>, Diagnostic> {
        self.store.edition.clone()?;
        let root = self.store.json.root();
        let mut parameters: Vec<crate::ResolvedReference> = vec![];
        let mut budget = self.store.limits.reference_steps;
        for list in [
            self.path_field("parameters"),
            self.value().get("parameters"),
        ]
        .into_iter()
        .flatten()
        {
            let mut local = HashSet::new();
            for authored in list
                .elements()
                .ok_or_else(|| list.error(Code::InvalidDeclaration))?
            {
                if budget == 0 {
                    return Err(authored.error(Code::Limit));
                }
                budget -= 1;
                let resolved =
                    document::resolve(&root, &authored, ReferenceKind::Parameter, budget + 1)?;
                let p = &resolved.target;
                let location = required_string(p, "in")?;
                let name = required_string(p, "name")?;
                let identity = (
                    location.clone(),
                    if location == "header" {
                        name.to_ascii_lowercase()
                    } else {
                        name.clone()
                    },
                );
                if !local.insert(identity) {
                    return Err(p.error(Code::InvalidDeclaration));
                }
                if let Some(index) = parameters.iter().position(|previous| {
                    let p = &previous.target;
                    p.get("in").is_some_and(|v| v.as_str() == Some(&location))
                        && p.get("name")
                            .and_then(|v| {
                                v.as_str().map(|old| {
                                    if location == "header" {
                                        old.eq_ignore_ascii_case(&name)
                                    } else {
                                        old == name
                                    }
                                })
                            })
                            .unwrap_or(false)
                }) {
                    parameters[index] = resolved;
                } else {
                    parameters.push(resolved);
                }
            }
        }
        Ok(parameters)
    }
    fn media_descriptions(
        &self,
        node: &Value,
        legacy_key: Option<&str>,
    ) -> Result<Vec<MediaDescription>, Diagnostic> {
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
                            let media_type = media
                                .as_str()
                                .ok_or_else(|| media.error(Code::InvalidDeclaration))?
                                .to_owned();
                            Ok(MediaDescription {
                                media_type,
                                authored: media,
                                schema: node.get("schema").map(|v| self.schema_description(v)),
                                encoding: None,
                                item_schema: None,
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
                if value.kind() != ValueKind::Object {
                    return Err(value.error(Code::InvalidDeclaration));
                }
                // Media Type Object references were added in 3.2; resolve them intentionally.
                let resolved = if value.get("$ref").is_some() {
                    if self.store.edition != Ok(Edition::OpenApi32) {
                        return Err(value.error(Code::InvalidDeclaration));
                    }
                    document::resolve(
                        &self.store.json.root(),
                        &value,
                        ReferenceKind::MediaType,
                        self.store.limits.reference_steps,
                    )?
                    .target
                } else {
                    value.clone()
                };
                Ok(MediaDescription {
                    media_type: key.into(),
                    authored: value,
                    schema: resolved.get("schema").map(|v| self.schema_description(v)),
                    encoding: resolved.get("encoding"),
                    item_schema: resolved
                        .get("itemSchema")
                        .map(|v| self.schema_description(v)),
                })
            })
            .collect()
    }
    fn body_description(&self) -> Result<Option<RequestBodyDescription>, Diagnostic> {
        let edition = self.store.edition.clone()?;
        if edition == Edition::Swagger20 {
            let parameters = self.effective_parameter_references()?;
            let bodies: Vec<_> = parameters
                .iter()
                .filter(|p| {
                    p.target
                        .get("in")
                        .is_some_and(|v| v.as_str() == Some("body"))
                })
                .collect();
            let forms: Vec<_> = parameters
                .iter()
                .filter(|p| {
                    p.target
                        .get("in")
                        .is_some_and(|v| v.as_str() == Some("formData"))
                })
                .collect();
            if bodies.len() > 1 || !bodies.is_empty() && !forms.is_empty() {
                return Err(self.value().error(Code::InvalidDeclaration));
            }
            let Some(declaration) = bodies.first().or_else(|| forms.first()) else {
                return Ok(None);
            };
            if !bodies.is_empty() && declaration.target.get("schema").is_none() {
                return Err(declaration.target.error(Code::InvalidDeclaration));
            }
            let fields = if forms.is_empty() {
                BodyFields::NotApplicable
            } else {
                BodyFields::Declared(
                    forms
                        .iter()
                        .map(|field| {
                            Ok(BodyFieldDescription {
                                name: required_string(&field.target, "name")?,
                                required: bool_field(&field.target, "required", false)?,
                                schema: self.schema_description(field.target.clone()),
                                authored: field.authored.clone(),
                            })
                        })
                        .collect::<Result<Vec<_>, Diagnostic>>()?,
                )
            };
            let required = if forms.is_empty() {
                bool_field(&declaration.target, "required", false)?
            } else {
                forms.iter().try_fold(false, |required, f| {
                    Ok::<_, Diagnostic>(required || bool_field(&f.target, "required", false)?)
                })?
            };
            return Ok(Some(RequestBodyDescription {
                declaration: (*declaration).clone(),
                required,
                media: self.media_descriptions(&declaration.target, Some("consumes"))?,
                media_unrestricted: self
                    .value()
                    .get("consumes")
                    .or_else(|| self.store.json.root().get("consumes"))
                    .is_none_or(|v| v.elements().is_some_and(|mut v| v.next().is_none())),
                fields,
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
        let declaration = document::resolve(
            &self.store.json.root(),
            &body,
            ReferenceKind::RequestBody,
            self.store.limits.reference_steps,
        )?;
        let media = self.media_descriptions(&declaration.target, None)?;
        let fields = if media.iter().any(|m| {
            m.media_type.starts_with("multipart/")
                || m.media_type == "application/x-www-form-urlencoded"
        }) {
            BodyFields::Opaque
        } else {
            BodyFields::NotApplicable
        };
        Ok(Some(RequestBodyDescription {
            required: bool_field(&declaration.target, "required", false)?,
            declaration,
            media,
            media_unrestricted: false,
            fields,
        }))
    }
    fn server_descriptions(&self) -> Result<Vec<ServerDescription>, Diagnostic> {
        if self.store.edition.clone()? != Edition::Swagger20 {
            return match self.inspect().servers {
                None => Ok(vec![ServerDescription {
                    url: "/".into(),
                    variables: None,
                    authored: vec![],
                }]),
                Some(list) => {
                    let result = list
                        .elements()
                        .ok_or_else(|| list.error(Code::InvalidDeclaration))?
                        .map(|v| {
                            Ok(ServerDescription {
                                url: required_string(&v, "url")?,
                                variables: v.get("variables"),
                                authored: vec![v],
                            })
                        })
                        .collect::<Result<Vec<_>, Diagnostic>>()?;
                    Ok(if result.is_empty() {
                        vec![ServerDescription {
                            url: "/".into(),
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
        let host = optional_string(&root, "host")?
            .or_else(|| {
                base.as_ref()
                    .and_then(|b| b.authority().map(|a| a.as_str().into()))
            })
            .ok_or_else(|| root.error(Code::MissingBase))?;
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
        let path = optional_string(&root, "basePath")?.unwrap_or_default();
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
                    v.as_str()
                        .map(|s| (s.to_owned(), Some(v.clone())))
                        .ok_or_else(|| v.error(Code::InvalidDeclaration))
                })
                .collect::<Result<Vec<_>, _>>()?,
            None => vec![],
        };
        if scheme_values.is_empty() {
            scheme_values.push((
                base.as_ref()
                    .and_then(|b| b.scheme().map(|s| s.as_str().to_owned()))
                    .ok_or_else(|| root.error(Code::MissingBase))?,
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
                Ok(ServerDescription {
                    url: format!("{scheme}://{host}{path}"),
                    variables: None,
                    authored: [host_node.clone(), path_node.clone(), source]
                        .into_iter()
                        .flatten()
                        .collect(),
                })
            })
            .collect()
    }
    fn choose_target(&self, selection: &Selection) -> Result<String, Diagnostic> {
        let root = self.store.json.root();
        if self.store.edition.clone()? != Edition::Swagger20 {
            return choose_server(
                self.inspect().servers.as_ref(),
                self.store.base.as_deref(),
                selection,
                &root,
                &self.store.limits,
            );
        }
        if !selection.variables.is_empty() {
            return Err(root.error(Code::InvalidSelection));
        }
        let servers = self.server_descriptions()?;
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
    pub fn describe(&self) -> Result<EffectiveOperationDescription, Diagnostic> {
        let compiled = self.compiled()?;
        let mut responses = vec![];
        if let Some(values) = self.value().get("responses") {
            for (status, value) in values
                .members()
                .ok_or_else(|| values.error(Code::InvalidDeclaration))?
            {
                if status.starts_with("x-") {
                    continue;
                }
                let declaration = document::resolve(
                    &self.store.json.root(),
                    &value,
                    ReferenceKind::Response,
                    self.store.limits.reference_steps,
                )?;
                let media = self.media_descriptions(
                    &declaration.target,
                    (self.store.edition == Ok(Edition::Swagger20)).then_some("produces"),
                )?;
                responses.push(ResponseDescription {
                    status: status.into(),
                    declaration,
                    media,
                });
            }
        }
        Ok(EffectiveOperationDescription {
            path: self.path().into(),
            method: self.method(),
            operation_id: self.id(),
            parameters: Parameters {
                entries: compiled.parameters.clone(),
            },
            servers: self.server_descriptions()?,
            request_body: compiled.body.clone(),
            responses,
            security: self.security_descriptions()?,
        })
    }
    fn security_descriptions(&self) -> Result<Vec<SecurityAlternative>, Diagnostic> {
        let Some(security) = self.inspect().security else {
            return Ok(vec![]);
        };
        let root = self.store.json.root();
        let edition = self.store.edition.clone()?;
        security
            .elements()
            .ok_or_else(|| security.error(Code::InvalidDeclaration))?
            .map(|authored| {
                let requirements = authored
                    .members()
                    .ok_or_else(|| authored.error(Code::InvalidDeclaration))?
                    .map(|(name, scopes)| {
                        let scopes_source = scopes.clone();
                        let scopes: Vec<String> = scopes
                            .elements()
                            .ok_or_else(|| scopes.error(Code::InvalidDeclaration))?
                            .map(|v| {
                                v.as_str()
                                    .map(str::to_owned)
                                    .ok_or_else(|| v.error(Code::InvalidDeclaration))
                            })
                            .collect::<Result<_, _>>()?;
                        let declaration =
                            security_declaration(&root, name, self.store.limits.reference_steps)?;
                        let scheme = describe_security(&declaration.target, edition)?;
                        if matches!(edition, Edition::Swagger20 | Edition::OpenApi30)
                            && !matches!(
                                scheme,
                                SecurityScheme::OAuth2 { .. }
                                    | SecurityScheme::OpenIdConnect { .. }
                            )
                            && !scopes.is_empty()
                        {
                            return Err(scopes_source.error(Code::InvalidDeclaration));
                        }
                        Ok(SecurityRequirement {
                            name: name.into(),
                            scopes,
                            declaration,
                            scheme,
                        })
                    })
                    .collect::<Result<_, Diagnostic>>()?;
                Ok(SecurityAlternative {
                    authored,
                    requirements,
                })
            })
            .collect()
    }
}

fn security_declaration(
    root: &Value,
    name: &str,
    budget: usize,
) -> Result<crate::ResolvedReference, Diagnostic> {
    let schemes = if Edition::detect(root)? == Edition::Swagger20 {
        root.get("securityDefinitions")
    } else {
        root.get("components")
            .and_then(|v| v.get("securitySchemes"))
    };
    let declared = schemes
        .and_then(|v| v.get(name))
        .ok_or_else(|| root.error(Code::InvalidDeclaration))?;
    document::resolve(root, &declared, ReferenceKind::SecurityScheme, budget)
}
fn describe_security(scheme: &Value, edition: Edition) -> Result<SecurityScheme, Diagnostic> {
    let ty = required_string(scheme, "type")?;
    Ok(match ty.as_str() {
        "basic" if edition == Edition::Swagger20 => SecurityScheme::Basic,
        "http" if edition != Edition::Swagger20 => match required_string(scheme, "scheme")?
            .to_ascii_lowercase()
            .as_str()
        {
            "basic" => SecurityScheme::Basic,
            "bearer" => SecurityScheme::Bearer {
                format: optional_string(scheme, "bearerFormat")?,
            },
            other => SecurityScheme::Unsupported {
                type_name: format!("http:{other}"),
            },
        },
        "apiKey" => {
            let location = ParameterLocation::parse(&required_string(scheme, "in")?)
                .filter(|l| {
                    *l != ParameterLocation::Path
                        && (edition != Edition::Swagger20 || *l != ParameterLocation::Cookie)
                })
                .ok_or_else(|| scheme.error(Code::InvalidDeclaration))?;
            SecurityScheme::ApiKey {
                name: required_string(scheme, "name")?,
                location,
            }
        }
        "oauth2" => {
            let flow = |name: &str, value: &Value| -> Result<OAuthFlowDescription, Diagnostic> {
                let flow = match name {
                    "accessCode" => "authorizationCode",
                    "application" => "clientCredentials",
                    name => name,
                };
                if !matches!(
                    flow,
                    "implicit" | "password" | "clientCredentials" | "authorizationCode"
                ) {
                    return Err(value.error(Code::InvalidDeclaration));
                }
                let scopes = value
                    .get("scopes")
                    .ok_or_else(|| value.error(Code::InvalidDeclaration))?;
                let scopes = scopes
                    .members()
                    .ok_or_else(|| scopes.error(Code::InvalidDeclaration))?
                    .map(|(name, description)| {
                        Ok((
                            name.into(),
                            description
                                .as_str()
                                .ok_or_else(|| description.error(Code::InvalidDeclaration))?
                                .into(),
                        ))
                    })
                    .collect::<Result<_, Diagnostic>>()?;
                if matches!(flow, "implicit" | "authorizationCode") {
                    required_string(value, "authorizationUrl")?;
                }
                if matches!(flow, "password" | "clientCredentials" | "authorizationCode") {
                    required_string(value, "tokenUrl")?;
                }
                Ok(OAuthFlowDescription {
                    flow: flow.into(),
                    authorization_url: optional_string(value, "authorizationUrl")?,
                    token_url: optional_string(value, "tokenUrl")?,
                    refresh_url: optional_string(value, "refreshUrl")?,
                    scopes,
                })
            };
            let flows = if edition == Edition::Swagger20 {
                vec![flow(&required_string(scheme, "flow")?, scheme)?]
            } else {
                let values = scheme
                    .get("flows")
                    .ok_or_else(|| scheme.error(Code::InvalidDeclaration))?;
                values
                    .members()
                    .ok_or_else(|| values.error(Code::InvalidDeclaration))?
                    .filter(|(name, _)| !name.starts_with("x-"))
                    .map(|(name, value)| flow(name, &value))
                    .collect::<Result<_, _>>()?
            };
            SecurityScheme::OAuth2 { flows }
        }
        "openIdConnect" if edition != Edition::Swagger20 => SecurityScheme::OpenIdConnect {
            url: required_string(scheme, "openIdConnectUrl")?,
        },
        "mutualTLS" if matches!(edition, Edition::OpenApi31 | Edition::OpenApi32) => {
            SecurityScheme::MutualTls
        }
        _ => SecurityScheme::Unsupported { type_name: ty },
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
        self.parameter_declarations()
            .map(|(entries, _)| Parameters { entries })
    }
    fn parameter_declarations(&self) -> Result<(Vec<Parameter>, usize), Diagnostic> {
        let node = self.value();
        let edition = self.store.edition.clone()?;
        Method::from_token(self.method().as_str())?;
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
        let mut parameters: Vec<Parameter> = vec![];
        let mut budget = self.store.limits.reference_steps;
        for resolved in self.effective_parameter_references()? {
            if budget == 0 {
                return Err(resolved.authored.error(Code::Limit));
            }
            budget -= 1;
            let p = resolved.target.clone();
            if edition == Edition::Swagger20
                && p.get("in")
                    .is_some_and(|v| matches!(v.as_str(), Some("body" | "formData")))
            {
                continue;
            }
            let name = required_string(&p, "name")?;
            let location = ParameterLocation::parse(&required_string(&p, "in")?)
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
            if location == ParameterLocation::Path
                && (!required || !self.path().contains(&format!("{{{name}}}")))
            {
                return Err(p.error(Code::InvalidDeclaration));
            }
            let (explode, unsupported, collection, schema_context) = if edition
                == Edition::Swagger20
            {
                if location == ParameterLocation::Cookie {
                    return Err(p.error(Code::InvalidDeclaration));
                }
                let ty = required_string(&p, "type")?;
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
                    let format =
                        optional_string(&p, "collectionFormat")?.unwrap_or_else(|| "csv".into());
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
                    false,
                    collection,
                    Some(self.schema_description(p.clone())),
                )
            } else {
                let style = optional_string(&p, "style")?.unwrap_or_else(|| {
                    if matches!(
                        location,
                        ParameterLocation::Path | ParameterLocation::Header
                    ) {
                        "simple".into()
                    } else {
                        "form".into()
                    }
                });
                let expected = if matches!(
                    location,
                    ParameterLocation::Path | ParameterLocation::Header
                ) {
                    "simple"
                } else {
                    "form"
                };
                if p.get("schema").is_none() == p.get("content").is_none() {
                    return Err(p.error(Code::InvalidDeclaration));
                }
                let unsupported = style != expected
                    || p.get("content").is_some()
                    || bool_field(&p, "allowReserved", false)?
                    || location == ParameterLocation::Header && name.eq_ignore_ascii_case("cookie");
                (
                    bool_field(&p, "explode", style == "form")?,
                    unsupported,
                    None,
                    p.get("schema").map(|v| self.schema_description(v)),
                )
            };
            let param = Parameter {
                resolved,
                node: p.clone(),
                name: name.clone(),
                location,
                required,
                explode,
                unsupported,
                collection,
                schema_context,
            };
            if let Some(i) = parameters
                .iter()
                .position(|p| p.location == location && same_name(&p.name, &name, location))
            {
                parameters[i] = param;
            } else {
                parameters.push(param);
            }
        }
        Ok((parameters, budget))
    }
    fn compile(&self) -> Result<Arc<Compiled>, Diagnostic> {
        let (parameters, mut budget) = self.parameter_declarations()?;
        let root = self.store.json.root();
        let node = self.value();
        let body = self.body_description()?;
        let mut matched = vec![];
        if let Some(responses) = node.get("responses") {
            for (key, value) in responses
                .members()
                .ok_or_else(|| responses.error(Code::InvalidDeclaration))?
            {
                if key.starts_with("x-") {
                    continue;
                }
                let valid = key == "default"
                    || key.len() == 3
                        && ((key.as_bytes()[0] >= b'1' && key.as_bytes()[0] <= b'5')
                            && (self.store.edition.clone()? != Edition::Swagger20
                                && key[1..] == *"XX"
                                || key.bytes().all(|b| b.is_ascii_digit())));
                if !valid {
                    return Err(value.error(Code::InvalidDeclaration));
                }
                if budget == 0 {
                    return Err(value.error(Code::Limit));
                }
                budget -= 1;
                matched.push((
                    key.to_owned(),
                    document::resolve(&root, &value, ReferenceKind::Response, budget + 1)?.target,
                ));
            }
            if matched.is_empty() {
                return Err(responses.error(Code::InvalidDeclaration));
            }
        }
        Ok(Arc::new(Compiled {
            parameters,
            body,
            responses: matched,
        }))
    }
    fn compiled(&self) -> Result<Arc<Compiled>, Diagnostic> {
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
        if can_cache {
            record.compiled.get_or_init(|| self.compile()).clone()
        } else {
            self.compile()
        }
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
        self.prepare_inner(input, cancellation).map_err(|mut e| {
            if e.location.is_none() && e.context().is_none() {
                e = e.contextual(DiagnosticContext::PreparedRequest);
            }
            e
        })
    }
    fn prepare_inner(
        &self,
        input: &Input,
        cancellation: &crate::Cancellation,
    ) -> Result<PreparedRequest, Diagnostic> {
        cancellation.check()?;
        let compiled = self.compiled()?;
        admit_input(input, &self.store.limits)?;
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
        for p in &compiled.parameters {
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
                return Err(p.node.error(Code::UnsupportedParameter));
            }
            let scalar = if self.store.edition == Ok(Edition::Swagger20) {
                serialize_legacy(&value.value, p.location, p.collection.as_deref(), &p.name)
            } else {
                serialize(&value.value, p.location, p.explode, &p.name)
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
                    path = path.replace(&marker, &scalar.value)
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
        target.push_str(&path);
        let (body, body_kind, media) = prepare_body(compiled.body.as_ref(), input, &limits)
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
            &root,
            desc.security.as_ref(),
            &target,
            input,
            SecurityFields {
                headers: &mut headers,
                query: &mut query,
                cookies: &mut cookies,
            },
            limits.reference_steps,
        )
        .map_err(|e| e.election(SelectionKind::Security, input.selection.security, None))?;
        if !cookies.is_empty() {
            if headers
                .iter()
                .any(|h| h.name().eq_ignore_ascii_case("cookie"))
            {
                return Err(Diagnostic::new(Code::CredentialCollision));
            }
            headers.push(Header::sensitive(
                "Cookie",
                cookies
                    .iter()
                    .map(|(k, v)| format!("{k}={v}"))
                    .collect::<Vec<_>>()
                    .join("; ")
                    .as_bytes(),
            ));
        }
        if !query.is_empty() {
            target.push('?');
            target.push_str(
                &query
                    .iter()
                    .map(|(k, v)| format!("{k}={v}"))
                    .collect::<Vec<_>>()
                    .join("&"),
            );
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

struct Media {
    essence: String,
    parameters: BTreeMap<String, String>,
    json: bool,
}
fn parse_media(s: &str, ranges: bool) -> Result<Media, Diagnostic> {
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
fn media_matches(key: &Media, selected: &Media) -> Option<(u8, usize)> {
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
    decl: Option<&RequestBodyDescription>,
    input: &Input,
    limits: &Limits,
) -> Result<BodyEncoding, Diagnostic> {
    let absent = matches!(input.body, Body::Absent);
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
        } else if matches!(input.body, Body::Json(_)) {
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
            .filter(|(_, m, _)| !matches!(input.body, Body::Json(_)) || m.json)
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
    let (bytes, kind) = match &input.body {
        Body::Absent => unreachable!(),
        Body::Json(v) => {
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
        Body::Raw(b) => (b.clone(), "raw"),
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
    root: &Value,
    requirements: Option<&Value>,
    target: &str,
    input: &Input,
    fields: SecurityFields<'_>,
    budget: usize,
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
    if entries.len() > budget {
        return Err(choice.error(Code::Limit));
    }
    let origin = uri::origin(target)?;
    for (name, scopes) in entries {
        if scopes.kind() != ValueKind::Array {
            return Err(scopes.error(Code::InvalidDeclaration));
        }
        let scheme = security_declaration(root, name, budget)?.target;
        let kind = describe_security(&scheme, Edition::detect(root)?)?;
        if matches!(
            kind,
            SecurityScheme::Unsupported { .. } | SecurityScheme::MutualTls
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
            SecurityScheme::OAuth2 { .. } | SecurityScheme::OpenIdConnect { .. }
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
        let (location, name, value) = if !matches!(kind, SecurityScheme::ApiKey { .. }) {
            let mechanism = if matches!(kind, SecurityScheme::Basic) {
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
    /// Payload category: absent, json or raw.
    pub fn body_kind(&self) -> &'static str {
        self.body_kind
    }
    /// Operation owner retained by this request.
    pub fn operation(&self) -> Operation {
        self.operation.clone()
    }
    /// Match the exact status, then range, then default response declaration.
    pub fn response_declaration(&self, status: u16) -> Option<(&str, Value)> {
        let exact = status.to_string();
        let range = format!("{}XX", status / 100);
        for key in [exact.as_str(), range.as_str(), "default"] {
            if let Some((k, v)) = self.compiled.responses.iter().find(|(k, _)| k == key) {
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
        }
    }
}
