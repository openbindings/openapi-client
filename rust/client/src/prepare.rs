use crate::{Code, Diagnostic, Header, Limits, Method, Operation, ReferenceKind, Value, ValueKind};
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
/// Explicit per-call elections. None permits only unique usable choices.
#[derive(Clone, Default)]
pub struct Selection {
    /// Index in the effective authored server array.
    pub server: Option<usize>,
    /// Server-variable overrides; values are checked after substitution.
    pub variables: BTreeMap<String, String>,
    /// Exact concrete request media spelling (surrounding whitespace is trimmed).
    pub media: Option<String>,
    /// Index in the effective security alternatives, including an allowed empty alternative.
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
#[derive(Clone)]
struct Parameter {
    node: Value,
    name: String,
    location: ParameterLocation,
    required: bool,
    explode: bool,
    unsupported: bool,
}
pub(crate) struct Compiled {
    parameters: Vec<Parameter>,
    body: Option<Value>,
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
fn same_name(a: &str, b: &str, location: ParameterLocation) -> bool {
    if location == ParameterLocation::Header {
        a.eq_ignore_ascii_case(b)
    } else {
        a == b
    }
}

impl Operation {
    fn compile(&self) -> Result<Arc<Compiled>, Diagnostic> {
        let root = self.store.json.root();
        let node = self.value();
        if !root
            .get("openapi")
            .is_some_and(|v| matches!(v.as_str(), Some("3.1.0" | "3.1.1" | "3.1.2")))
        {
            return Err(root
                .get("openapi")
                .unwrap_or(root)
                .error(Code::UnsupportedVersion));
        }
        if let Some(e) = &self.record().path_error {
            return Err(e.clone());
        }
        if node.kind() != ValueKind::Object {
            return Err(node.error(Code::InvalidDeclaration));
        }
        let mut parameters: Vec<Parameter> = vec![];
        let mut budget = self.store.limits.reference_steps;
        for list in [self.path_field("parameters"), node.get("parameters")]
            .into_iter()
            .flatten()
        {
            let entries = list
                .elements()
                .ok_or_else(|| list.error(Code::InvalidDeclaration))?;
            let mut local = HashSet::new();
            for authored in entries {
                if budget == 0 {
                    return Err(authored.error(Code::Limit));
                }
                budget -= 1;
                let p = document::resolve(&root, &authored, ReferenceKind::Parameter, budget + 1)?
                    .target;
                let name = required_string(&p, "name")?;
                let location = ParameterLocation::parse(&required_string(&p, "in")?)
                    .ok_or_else(|| p.error(Code::UnsupportedParameter))?;
                if location == ParameterLocation::Header
                    && ["accept", "content-type", "authorization"]
                        .iter()
                        .any(|n| name.eq_ignore_ascii_case(n))
                {
                    continue;
                }
                let identity = (
                    if location == ParameterLocation::Header {
                        name.to_ascii_lowercase()
                    } else {
                        name.clone()
                    },
                    format!("{location:?}"),
                );
                if !local.insert(identity) {
                    return Err(p.error(Code::InvalidDeclaration));
                }
                let required = bool_field(&p, "required", false)?;
                if location == ParameterLocation::Path
                    && (!required || !self.path().contains(&format!("{{{name}}}")))
                {
                    return Err(p.error(Code::InvalidDeclaration));
                }
                let style = p
                    .get("style")
                    .map(|v| {
                        v.as_str()
                            .map(str::to_owned)
                            .ok_or_else(|| v.error(Code::InvalidDeclaration))
                    })
                    .transpose()?
                    .unwrap_or_else(|| {
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
                if p.get("schema").is_none() && p.get("content").is_none()
                    || p.get("schema").is_some() && p.get("content").is_some()
                {
                    return Err(p.error(Code::InvalidDeclaration));
                }
                let unsupported = style != expected
                    || p.get("content").is_some()
                    || bool_field(&p, "allowReserved", false)?
                    || location == ParameterLocation::Header && name.eq_ignore_ascii_case("cookie");
                let param = Parameter {
                    node: p.clone(),
                    name: name.clone(),
                    location,
                    required,
                    explode: bool_field(&p, "explode", style == "form")?,
                    unsupported,
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
        }
        let body = node
            .get("requestBody")
            .map(|b| {
                document::resolve(&root, &b, ReferenceKind::RequestBody, budget).map(|r| r.target)
            })
            .transpose()?;
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
                            && (key[1..] == *"XX" || key.bytes().all(|b| b.is_ascii_digit())));
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
        let mut target = choose_server(
            desc.servers.as_ref(),
            self.store.base.as_deref(),
            &input.selection,
            &root,
            &limits,
        )
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
            let scalar = serialize(&value.value, p.location, p.explode, &p.name)
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
            &mut headers,
            &mut query,
            &mut cookies,
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
                    .setting(format!("server.variables.{name}"))
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
                        .setting(format!("server.variables.{name}"))
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
            e.location = Some(server.location());
            e = e.sourced(server.source_context());
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
    if ty.contains('*') || sub.contains('*') {
        if !ranges || !(ty == "*" && sub == "*" || !ty.contains('*') && sub == "*") {
            return Err(bad());
        }
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
fn prepare_body(
    decl: Option<&Value>,
    input: &Input,
    limits: &Limits,
) -> Result<(Option<Arc<[u8]>>, &'static str, Option<String>), Diagnostic> {
    let absent = matches!(input.body, Body::Absent);
    let Some(decl) = decl else {
        if absent && input.selection.media.is_none() {
            return Ok((None, "absent", None));
        }
        return Err(Diagnostic::new(Code::InvalidSelection));
    };
    if absent {
        if bool_field(decl, "required", false)? {
            return Err(decl.error(Code::MissingBody));
        }
        return Ok((None, "absent", None));
    }
    let content = decl
        .get("content")
        .ok_or_else(|| decl.error(Code::InvalidDeclaration))?;
    let mut media = vec![];
    for (k, v) in content
        .members()
        .ok_or_else(|| content.error(Code::InvalidDeclaration))?
    {
        if let Ok(parsed) = parse_media(k, true) {
            media.push((k.to_owned(), parsed, v));
        }
    }
    let selected = if let Some(s) = &input.selection.media {
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
        if value.kind() != ValueKind::Object {
            return Err(value.error(Code::InvalidDeclaration));
        }
        s.trim_matches([' ', '\t']).to_owned()
    } else {
        let concrete = media
            .iter()
            .filter(|(_, m, v)| !m.essence.contains('*') && v.kind() == ValueKind::Object)
            .collect::<Vec<_>>();
        match concrete.len() {
            0 => {
                return Err(content.error(if media.is_empty() {
                    Code::InvalidMedia
                } else {
                    Code::InvalidSelection
                }));
            }
            1 => concrete[0].0.clone(),
            _ => return Err(content.error(Code::AmbiguousSelection)),
        }
    };
    let (bytes, kind) = match &input.body {
        Body::Absent => unreachable!(),
        Body::Json(v) => {
            if v.raw().len() > limits.body_bytes {
                return Err(Diagnostic::limited(
                    LimitKind::BodyBytes,
                    limits.body_bytes,
                    v.raw().len(),
                )
                .at(v.location())
                .sourced(v.source_context()));
            }
            if !media_type(&selected)?.1 {
                return Err(decl.error(Code::UnsupportedMedia));
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
fn apply_security(
    root: &Value,
    requirements: Option<&Value>,
    target: &str,
    input: &Input,
    headers: &mut Vec<Header>,
    query: &mut Vec<(String, String)>,
    cookies: &mut Vec<(String, String)>,
    budget: usize,
) -> Result<(), Diagnostic> {
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
        let schemes = root
            .get("components")
            .and_then(|c| c.get("securitySchemes"));
        let declared = schemes
            .and_then(|s| s.get(name))
            .ok_or_else(|| choice.error(Code::InvalidDeclaration))?;
        let scheme =
            document::resolve(root, &declared, ReferenceKind::SecurityScheme, budget)?.target;
        let ty = required_string(&scheme, "type")?;
        if !matches!(ty.as_str(), "http" | "apiKey") {
            return Err(scheme.error(Code::UnsupportedSecurity));
        }
        if scopes.elements().unwrap().any(|v| v.as_str().is_none()) {
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
        let (location, name, value) = if ty == "http" {
            let mechanism = required_string(&scheme, "scheme")?;
            let value = match (mechanism.to_ascii_lowercase().as_str(), &credential.value) {
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
        self.method
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
