use crate::{Code, Diagnostic, ExactJson, Limits, ParseFailure, Value, ValueKind};
use std::{
    collections::{HashMap, HashSet},
    sync::{
        Arc, OnceLock,
        atomic::{AtomicUsize, Ordering},
    },
};

/// HTTP operation methods recognized by OpenAPI 3.1.
#[derive(Clone, Copy, Debug, Eq, PartialEq, Hash)]
#[allow(missing_docs)]
pub enum Method {
    Get,
    Put,
    Post,
    Delete,
    Options,
    Head,
    Patch,
    Trace,
}
impl Method {
    /// Parse an OpenAPI method field case-insensitively for caller lookup.
    pub fn parse(s: &str) -> Option<Self> {
        Some(match s.to_ascii_lowercase().as_str() {
            "get" => Self::Get,
            "put" => Self::Put,
            "post" => Self::Post,
            "delete" => Self::Delete,
            "options" => Self::Options,
            "head" => Self::Head,
            "patch" => Self::Patch,
            "trace" => Self::Trace,
            _ => return None,
        })
    }
    /// Uppercase HTTP method.
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Get => "GET",
            Self::Put => "PUT",
            Self::Post => "POST",
            Self::Delete => "DELETE",
            Self::Options => "OPTIONS",
            Self::Head => "HEAD",
            Self::Patch => "PATCH",
            Self::Trace => "TRACE",
        }
    }
}
/// Expected protocol-object kind. Schema references are intentionally absent.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[allow(missing_docs)]
pub enum ReferenceKind {
    Parameter,
    RequestBody,
    Response,
    SecurityScheme,
    PathItem,
}
/// Local reference result with authored siblings and destination kept separate.
#[derive(Clone, Debug)]
pub struct ResolvedReference {
    /// Original object at the use site.
    pub authored: Value,
    /// Target object after following the supported local chain.
    pub target: Value,
    /// Effective Reference Object summary, if applicable to the target kind.
    pub summary: Option<Value>,
    /// Effective description, with nearest supported Reference Object override.
    pub description: Option<Value>,
    /// Authored local chain in traversal order, including the terminal target.
    pub chain: Vec<Value>,
}
pub(crate) fn resolve(
    root: &Value,
    node: &Value,
    kind: ReferenceKind,
    limit: usize,
) -> Result<ResolvedReference, Diagnostic> {
    let mut current = node.clone();
    let mut seen = HashSet::new();
    let (mut summary, mut description) = (None, None);
    let mut steps = 0;
    let mut chain: Vec<Value> = vec![];
    let mut path_fields = HashSet::new();
    loop {
        if current.kind() != ValueKind::Object {
            return Err(current.error(Code::WrongReferenceKind));
        }
        if !seen.insert(current.identity()) {
            return Err(current.error(Code::ReferenceCycle));
        }
        if kind == ReferenceKind::PathItem {
            for (name, _) in current.members().unwrap() {
                if name != "$ref" && !name.starts_with("x-") && !path_fields.insert(name.to_owned())
                {
                    return Err(current.error(Code::AmbiguousReference));
                }
            }
        }
        chain.push(current.clone());
        let Some(reference) = current.get("$ref") else {
            break;
        };
        if steps >= limit {
            return Err(reference.error(Code::Limit));
        }
        steps += 1;
        if description.is_none() {
            description = current.get("description");
        }
        if summary.is_none() && kind == ReferenceKind::PathItem {
            summary = current.get("summary");
        }
        let s = reference
            .as_str()
            .ok_or_else(|| reference.error(Code::InvalidReference))?;
        if !s.starts_with('#') {
            return Err(reference.error(Code::ExternalReference));
        }
        fluent_uri::UriRef::parse(s).map_err(|_| reference.error(Code::InvalidReference))?;
        let pointer = crate::uri::percent_decode(&s[1..]).map_err(|e| reference.error(e.code))?;
        let next = root
            .pointer(&pointer)
            .map_err(|e| reference.error(e.code))?;
        if kind == ReferenceKind::PathItem {
            for (name, _) in current.members().expect("object checked") {
                if name != "$ref" && !name.starts_with("x-") && next.get(name).is_some() {
                    return Err(current.error(Code::AmbiguousReference));
                }
            }
        }
        current = next;
    }
    let valid = match kind {
        ReferenceKind::Parameter => {
            current.get("name").is_some_and(|v| v.as_str().is_some())
                && current.get("in").is_some_and(|v| v.as_str().is_some())
        }
        ReferenceKind::RequestBody => current
            .get("content")
            .is_some_and(|v| v.kind() == ValueKind::Object),
        ReferenceKind::Response => current
            .get("description")
            .is_some_and(|v| v.as_str().is_some()),
        ReferenceKind::SecurityScheme => current.get("type").is_some_and(|v| v.as_str().is_some()),
        ReferenceKind::PathItem => current.members().unwrap().all(|(name, _)| {
            name.starts_with("x-")
                || matches!(name, "summary" | "description" | "servers" | "parameters")
                || Method::parse(name).is_some_and(|_| name == name.to_ascii_lowercase())
        }),
    };
    if !valid {
        return Err(current.error(Code::WrongReferenceKind));
    }
    if description.is_none() {
        description = current.get("description");
    }
    if summary.is_none() && kind == ReferenceKind::PathItem {
        summary = current.get("summary");
    }
    Ok(ResolvedReference {
        authored: node.clone(),
        target: current,
        summary,
        description,
        chain,
    })
}
pub(crate) struct Record {
    pub path: String,
    pub method: Method,
    pub node: Value,
    pub path_chain: Vec<Value>,
    pub path_error: Option<Diagnostic>,
    pub cache_slot: OnceLock<bool>,
    pub compiled: OnceLock<Result<Arc<crate::prepare::Compiled>, Diagnostic>>,
}
pub(crate) struct Store {
    pub json: ExactJson,
    pub base: Option<String>,
    pub limits: Limits,
    pub records: Vec<Record>,
    pub ids: HashMap<String, Vec<usize>>,
    pub paths: HashMap<(String, Method), usize>,
    pub path_errors: HashMap<String, Diagnostic>,
    pub cached: AtomicUsize,
}
/// Immutable OpenAPI document with stable owned operation handles.
#[derive(Clone)]
pub struct Document(pub(crate) Arc<Store>);
impl std::fmt::Debug for Document {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Document")
            .field("operations", &self.0.records.len())
            .finish()
    }
}
/// An operation that keeps its document and source locations alive.
#[derive(Clone)]
pub struct Operation {
    pub(crate) store: Arc<Store>,
    pub(crate) index: usize,
}
impl std::fmt::Debug for Operation {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Operation")
            .field("method", &self.method())
            .field(
                "source_range",
                &(self.value().location().start, self.value().location().end),
            )
            .finish()
    }
}
/// Selected authored/effective operation declarations; raw schemas are retained.
#[derive(Clone, Debug)]
pub struct OperationDescription {
    /// Authored path spelling.
    pub path: String,
    /// Method.
    pub method: Method,
    /// Optional operation ID; uniqueness is checked by ID lookup.
    pub operation_id: Option<String>,
    /// Original operation value, including unknown members.
    pub authored: Value,
    /// Effective server array, or absent for the protocol default.
    pub servers: Option<Value>,
    /// Path-level authored parameter list.
    pub path_parameters: Option<Value>,
    /// Operation-level authored parameter list.
    pub parameters: Option<Value>,
    /// Effective authored security alternatives.
    pub security: Option<Value>,
    /// Authored request body declaration/reference.
    pub request_body: Option<Value>,
    /// Authored response declarations/references.
    pub responses: Option<Value>,
}
impl Document {
    /// Index JSON bytes. Unsupported editions and malformed operations remain inspectable.
    pub fn parse(
        bytes: impl AsRef<[u8]>,
        base: Option<&str>,
        limits: Limits,
    ) -> Result<Self, ParseFailure> {
        Self::parse_with_cancellation(bytes, base, limits, &crate::Cancellation::default())
    }
    /// Parse/index with cooperative cancellation checkpoints in owned work.
    pub fn parse_with_cancellation(
        bytes: impl AsRef<[u8]>,
        base: Option<&str>,
        limits: Limits,
        cancellation: &crate::Cancellation,
    ) -> Result<Self, ParseFailure> {
        let json =
            ExactJson::parse_with_cancellation(bytes.as_ref(), limits.clone(), cancellation)?;
        let root = json.root();
        if base.is_some_and(|b| b.len() > limits.target_bytes) {
            return Err(ParseFailure {
                diagnostic: Diagnostic::limited(
                    crate::LimitKind::TargetBytes,
                    limits.target_bytes,
                    base.unwrap().len(),
                )
                .contextual(crate::DiagnosticContext::CallerInput),
                source: Arc::from(bytes.as_ref()),
            });
        }
        let mut store = Store {
            json,
            base: base.map(str::to_owned),
            limits,
            records: vec![],
            ids: HashMap::new(),
            paths: HashMap::new(),
            path_errors: HashMap::new(),
            cached: AtomicUsize::new(0),
        };
        if let Some(paths) = root.get("paths") {
            if let Some(items) = paths.members() {
                for (path, item) in items {
                    cancellation.check().map_err(|diagnostic| ParseFailure {
                        diagnostic,
                        source: Arc::from(bytes.as_ref()),
                    })?;
                    if !path.starts_with('/') {
                        continue;
                    }
                    let resolved = resolve(
                        &root,
                        &item,
                        ReferenceKind::PathItem,
                        store.limits.reference_steps,
                    );
                    let (path_chain, path_error) = match resolved {
                        Ok(r) => (r.chain, None),
                        Err(e) => {
                            store.path_errors.insert(path.to_owned(), e.clone());
                            (vec![item.clone()], Some(e))
                        }
                    };
                    let mut methods = HashSet::new();
                    for container in &path_chain {
                        if let Some(members) = container.members() {
                            for (name, node) in members {
                                let Some(method) = Method::parse(name)
                                    .filter(|_| name == &name.to_ascii_lowercase())
                                else {
                                    continue;
                                };
                                if !methods.insert(method) {
                                    continue;
                                }
                                if store.records.len() >= store.limits.operations {
                                    return Err(ParseFailure {
                                        diagnostic: node.error(Code::Limit),
                                        source: Arc::from(bytes.as_ref()),
                                    });
                                }
                                let index = store.records.len();
                                if let Some(id) = node
                                    .get("operationId")
                                    .and_then(|v| v.as_str().map(str::to_owned))
                                {
                                    store.ids.entry(id).or_default().push(index);
                                }
                                store.paths.insert((path.to_owned(), method), index);
                                store.records.push(Record {
                                    path: path.to_owned(),
                                    method,
                                    node,
                                    path_chain: path_chain.clone(),
                                    path_error: path_error.clone(),
                                    cache_slot: OnceLock::new(),
                                    compiled: OnceLock::new(),
                                });
                            }
                        }
                    }
                }
            }
        }
        Ok(Self(Arc::new(store)))
    }
    /// Exact original bytes/text, including unknown declarations.
    pub fn source(&self) -> &str {
        self.0.json.source()
    }
    /// Owned root JSON inspection handle.
    pub fn root(&self) -> Value {
        self.0.json.root()
    }
    /// Version spelling, with missing/non-string represented as absent.
    pub fn version(&self) -> Option<String> {
        self.root()
            .get("openapi")
            .and_then(|v| v.as_str().map(str::to_owned))
    }
    /// Operations in authored path/method order.
    pub fn operations(&self) -> impl ExactSizeIterator<Item = Operation> + '_ {
        (0..self.0.records.len()).map(|index| Operation {
            store: self.0.clone(),
            index,
        })
    }
    /// Select by path and method; a path reference failure remains located.
    pub fn operation(&self, path: &str, method: Method) -> Result<Operation, Diagnostic> {
        if let Some(&index) = self.0.paths.get(&(path.to_owned(), method)) {
            Ok(Operation {
                store: self.0.clone(),
                index,
            })
        } else if let Some(e) = self.0.path_errors.get(path) {
            Err(e.clone())
        } else {
            Err(self.root().error(Code::MissingOperation))
        }
    }
    /// Select by a unique operation ID; duplicates are not silently resolved.
    pub fn operation_id(&self, id: &str) -> Result<Operation, Diagnostic> {
        match self.0.ids.get(id) {
            Some(v) if v.len() == 1 => Ok(Operation {
                store: self.0.clone(),
                index: v[0],
            }),
            Some(_) => Err(self.root().error(Code::AmbiguousOperation)),
            None => Err(self.root().error(Code::MissingOperation)),
        }
    }
    /// Resolve a meaningful protocol Reference Object at a caller-selected document pointer.
    /// This never follows Schema Object references or performs acquisition.
    pub fn resolve_protocol(
        &self,
        pointer: &str,
        kind: ReferenceKind,
    ) -> Result<ResolvedReference, Diagnostic> {
        let root = self.root();
        let node = root.pointer(pointer)?;
        resolve(&root, &node, kind, self.0.limits.reference_steps)
    }
    /// Accounted immutable JSON storage; caches are reported separately.
    pub fn retained_bytes(&self) -> usize {
        self.0.json.retained_bytes()
    }
    /// Number of retained operation compilation entries, bounded by configuration.
    pub fn cached_operations(&self) -> usize {
        self.0.cached.load(Ordering::Relaxed)
    }
}
impl Operation {
    pub(crate) fn record(&self) -> &Record {
        &self.store.records[self.index]
    }
    /// Authored path.
    pub fn path(&self) -> &str {
        &self.record().path
    }
    /// HTTP method.
    pub fn method(&self) -> Method {
        self.record().method
    }
    /// Original operation value, even when its declaration is malformed.
    pub fn value(&self) -> Value {
        self.record().node.clone()
    }
    /// Optional operation ID.
    pub fn id(&self) -> Option<String> {
        self.value()
            .get("operationId")
            .and_then(|v| v.as_str().map(str::to_owned))
    }
    pub(crate) fn path_field(&self, key: &str) -> Option<Value> {
        self.record().path_chain.iter().find_map(|v| v.get(key))
    }
    /// Effective declarations and raw inspection handles without preparing the operation.
    pub fn inspect(&self) -> OperationDescription {
        let n = self.value();
        OperationDescription {
            path: self.path().to_owned(),
            method: self.method(),
            operation_id: self.id(),
            authored: n.clone(),
            servers: n
                .get("servers")
                .or_else(|| self.path_field("servers"))
                .or_else(|| self.store.json.root().get("servers")),
            path_parameters: self.path_field("parameters"),
            parameters: n.get("parameters"),
            security: n
                .get("security")
                .or_else(|| self.store.json.root().get("security")),
            request_body: n.get("requestBody"),
            responses: n.get("responses"),
        }
    }
}
