use crate::{Code, Diagnostic, ExactJson, Limits, ParseFailure, Value};
use std::{
    collections::{HashMap, HashSet},
    sync::{
        Arc, OnceLock,
        atomic::{AtomicUsize, Ordering},
    },
};

/// Semantic OpenAPI family. Calling an operation never requires selecting this value.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
#[non_exhaustive]
pub enum Edition {
    /// Swagger 2.0.
    Swagger20,
    /// OpenAPI 3.0, including compatible patch editions.
    OpenApi30,
    /// OpenAPI 3.1, including compatible patch editions.
    OpenApi31,
    /// OpenAPI 3.2, including compatible patch editions.
    OpenApi32,
}
impl Edition {
    pub(crate) fn detect(root: &Value) -> Result<Self, Diagnostic> {
        use crate::VersionReason;
        let fail = |node: &Value, reason| {
            node.error(Code::UnsupportedVersion)
                .reasoned(crate::DiagnosticReason::Version(reason))
        };
        match (root.get("swagger"), root.get("openapi")) {
            (Some(_), Some(_)) => Err(fail(root, VersionReason::Conflicting)),
            (None, None) => Err(fail(root, VersionReason::Missing)),
            (Some(v), None) => match v.as_str() {
                Some("2.0") => Ok(Self::Swagger20),
                Some(_) => Err(fail(&v, VersionReason::UnsupportedFamily)),
                None => Err(fail(&v, VersionReason::Malformed)),
            },
            (None, Some(v)) => {
                let text = v
                    .as_str()
                    .ok_or_else(|| fail(&v, VersionReason::Malformed))?;
                let mut pieces = text.split('.');
                let parts = [pieces.next(), pieces.next(), pieces.next()];
                if pieces.next().is_some()
                    || parts.iter().any(|p| {
                        p.is_none_or(|p| {
                            p.is_empty()
                                || !p.bytes().all(|b| b.is_ascii_digit())
                                || p.len() > 1 && p.starts_with('0')
                        })
                    })
                {
                    return Err(fail(&v, VersionReason::Malformed));
                }
                match (parts[0].unwrap(), parts[1].unwrap()) {
                    ("3", "0") => Ok(Self::OpenApi30),
                    ("3", "1") => Ok(Self::OpenApi31),
                    ("3", "2") => Ok(Self::OpenApi32),
                    _ => Err(fail(&v, VersionReason::UnsupportedFamily)),
                }
            }
        }
    }
    pub(crate) fn reference_siblings(self) -> bool {
        matches!(self, Self::OpenApi31 | Self::OpenApi32)
    }
}
/// HTTP method with standard conveniences and exact extension-token spelling.
#[derive(Clone, Debug, Eq, PartialEq, Hash)]
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
    Query,
    Extension(ExtensionMethod),
}
impl Method {
    /// Parse a standard OpenAPI method field case-insensitively for caller lookup.
    /// Use `from_token` for case-sensitive HTTP wire tokens and extension methods.
    pub fn parse(s: &str) -> Option<Self> {
        if s.len() > 7 {
            return None;
        }
        Some(match s.to_ascii_lowercase().as_str() {
            "get" => Self::Get,
            "put" => Self::Put,
            "post" => Self::Post,
            "delete" => Self::Delete,
            "options" => Self::Options,
            "head" => Self::Head,
            "patch" => Self::Patch,
            "trace" => Self::Trace,
            "query" => Self::Query,
            _ => return None,
        })
    }
    /// Validate an HTTP token, preserving its case-sensitive wire spelling.
    /// Known uppercase tokens use the corresponding standard variant.
    pub fn from_token(token: &str) -> Result<Self, Diagnostic> {
        if token.is_empty() || !token.bytes().all(crate::prepare::token) {
            return Err(Diagnostic::new(Code::InvalidDeclaration));
        }
        if token.bytes().all(|b| !b.is_ascii_lowercase())
            && let Some(known) = Self::parse(token)
        {
            return Ok(known);
        }
        Ok(Self::Extension(ExtensionMethod(Arc::from(token))))
    }
    /// HTTP wire spelling: uppercase for standard variants, exact for extensions.
    pub fn as_str(&self) -> &str {
        match self {
            Self::Get => "GET",
            Self::Put => "PUT",
            Self::Post => "POST",
            Self::Delete => "DELETE",
            Self::Options => "OPTIONS",
            Self::Head => "HEAD",
            Self::Patch => "PATCH",
            Self::Trace => "TRACE",
            Self::Query => "QUERY",
            Self::Extension(token) => &token.0,
        }
    }
}
/// Validated case-sensitive HTTP extension method token.
/// Construct using `Method::from_token`; use `Method::as_str` for its spelling.
#[derive(Clone, Debug, Eq, PartialEq, Hash)]
pub struct ExtensionMethod(Arc<str>);
/// Expected protocol-object kind. Schema references are intentionally absent.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[allow(missing_docs)]
#[non_exhaustive]
pub enum ReferenceKind {
    Parameter,
    RequestBody,
    Response,
    SecurityScheme,
    PathItem,
    MediaType,
    Header,
    Example,
    Link,
    Callback,
}
/// Reference result retaining original wrappers, source arenas and targets.
#[derive(Clone, Debug)]
#[non_exhaustive]
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
    /// False when an authored identity exists but resolution used a retrieval alias.
    pub canonical: bool,
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
    pub graph: crate::graph::Graph,
    pub entry: crate::ResourceId,
    pub json: ExactJson,
    pub edition: Result<Edition, Diagnostic>,
    pub base: Option<String>,
    pub limits: Limits,
    pub records: Vec<Record>,
    pub ids: HashMap<String, Vec<usize>>,
    pub paths: HashMap<(String, Method), usize>,
    pub path_errors: HashMap<String, Diagnostic>,
    incomplete_paths: HashSet<String>,
    pub path_order: Vec<String>,
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
/// Raw operation declarations with inherited raw fields; use `Operation::describe` for common semantics.
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
    /// Detect JSON/YAML bytes and index a document with original source provenance.
    /// Unsupported editions and malformed operations remain inspectable.
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
        cancellation.check().map_err(|diagnostic| ParseFailure {
            diagnostic,
            source: Arc::from([]),
        })?;
        if bytes.as_ref().len() > limits.document_bytes {
            return Err(ParseFailure {
                diagnostic: Diagnostic::limited(
                    crate::LimitKind::DocumentBytes,
                    limits.document_bytes,
                    bytes.as_ref().len(),
                ),
                source: Arc::from([]),
            });
        }
        if let Some(base) = base {
            crate::graph::bound(
                0,
                base.len(),
                limits.target_bytes,
                crate::LimitKind::TargetBytes,
            )
            .map_err(|diagnostic| ParseFailure {
                diagnostic: diagnostic.contextual(crate::DiagnosticContext::CallerInput),
                source: Arc::from([]),
            })?;
        }
        let json = crate::loading::parse(bytes.as_ref(), limits.clone(), cancellation)?;
        let metadata = match base {
            Some(base) => {
                crate::ResourceInput::at(base, Arc::<[u8]>::from([])).map_err(|diagnostic| {
                    ParseFailure {
                        diagnostic,
                        source: Arc::from(bytes.as_ref()),
                    }
                })?
            }
            None => crate::ResourceInput::anonymous(Arc::<[u8]>::from([])),
        };
        let mut builder = crate::DocumentBuilder::new(limits, crate::GraphLimits::default());
        let entry = builder
            .admit_parsed(json, metadata, cancellation)
            .map_err(|diagnostic| ParseFailure {
                diagnostic,
                source: Arc::from(bytes.as_ref()),
            })?;
        builder
            .build_with_cancellation(entry, cancellation)
            .map_err(|diagnostic| ParseFailure {
                diagnostic,
                source: Arc::from(bytes.as_ref()),
            })
    }

    pub(crate) fn from_graph(
        graph: crate::graph::Graph,
        entry: crate::ResourceId,
        limits: Limits,
        cancellation: &crate::Cancellation,
    ) -> Result<Self, Diagnostic> {
        let resource = graph
            .resource(entry)
            .ok_or_else(|| Diagnostic::new(Code::MissingReference))?;
        let json = resource.json.clone();
        let base = resource.retrieval.clone();
        let root = json.root();
        let edition = Edition::detect(&root);
        let mut store = Store {
            graph,
            entry,
            json,
            edition,
            base,
            limits,
            records: vec![],
            ids: HashMap::new(),
            paths: HashMap::new(),
            path_errors: HashMap::new(),
            incomplete_paths: HashSet::new(),
            path_order: vec![],
            cached: AtomicUsize::new(0),
        };
        if let Some(paths) = root.get("paths")
            && let Some(items) = paths.members()
        {
            for (path, item) in items {
                cancellation.check()?;
                if !path.starts_with('/') {
                    continue;
                }
                store.path_order.push(path.to_owned());
                // Unsupported editions still expose authored operations for raw inspection.
                // Semantic consumers report the edition diagnostic before compiling them.
                let resolved = if store.edition.is_err() {
                    Ok(ResolvedReference {
                        authored: item.clone(),
                        target: item.clone(),
                        summary: None,
                        description: None,
                        chain: vec![item.clone()],
                        canonical: true,
                    })
                } else {
                    store.graph.resolve(
                        &item,
                        ReferenceKind::PathItem,
                        store.limits.reference_steps,
                    )
                };
                let (path_chain, path_error) = match resolved {
                    Ok(r) => (r.chain, None),
                    Err(e) => {
                        if item.get("$ref").is_some() {
                            store.incomplete_paths.insert(path.to_owned());
                        }
                        store.path_errors.insert(path.to_owned(), e.clone());
                        (vec![item.clone()], Some(e))
                    }
                };
                let mut methods = HashSet::new();
                for container in &path_chain {
                    if let Some(members) = container.members() {
                        let mut operations = vec![];
                        for (name, node) in members {
                            if let Some(method) =
                                Method::parse(name).filter(|_| name == name.to_ascii_lowercase())
                            {
                                if store.records.len().saturating_add(operations.len())
                                    >= store.limits.operations
                                {
                                    return Err(node.error(Code::Limit));
                                }
                                operations.push((method, node, None));
                            } else if name == "additionalOperations" {
                                if let Some(additional) = node.members() {
                                    for (token, operation) in additional {
                                        match Method::from_token(token) {
                                            Ok(method) => {
                                                let error = (!matches!(
                                                    method,
                                                    Method::Extension(_)
                                                ))
                                                .then(|| operation.error(Code::InvalidDeclaration));
                                                if store
                                                    .records
                                                    .len()
                                                    .saturating_add(operations.len())
                                                    >= store.limits.operations
                                                {
                                                    return Err(operation.error(Code::Limit));
                                                }
                                                operations.push((method, operation, error));
                                            }
                                            Err(_) => {
                                                store
                                                    .path_errors
                                                    .entry(path.to_owned())
                                                    .or_insert_with(|| {
                                                        operation.error(Code::InvalidDeclaration)
                                                    });
                                            }
                                        }
                                    }
                                } else {
                                    store
                                        .path_errors
                                        .entry(path.to_owned())
                                        .or_insert_with(|| node.error(Code::InvalidDeclaration));
                                }
                            }
                        }
                        for (method, node, operation_error) in operations {
                            if !methods.insert(method.clone()) && operation_error.is_none() {
                                continue;
                            }
                            if store.records.len() >= store.limits.operations {
                                return Err(node.error(Code::Limit));
                            }
                            let index = store.records.len();
                            if let Some(id) = node
                                .get("operationId")
                                .and_then(|v| v.as_str().map(str::to_owned))
                            {
                                store.ids.entry(id).or_default().push(index);
                            }
                            store.paths.insert((path.to_owned(), method.clone()), index);
                            store.records.push(Record {
                                path: path.to_owned(),
                                method,
                                node,
                                path_chain: path_chain.clone(),
                                path_error: operation_error.or_else(|| path_error.clone()),
                                cache_slot: OnceLock::new(),
                                compiled: OnceLock::new(),
                            });
                        }
                    }
                }
            }
        }
        Ok(Self(Arc::new(store)))
    }
    /// Exact original bytes/text, including unknown declarations.
    pub fn source(&self) -> &str {
        self.0.json.original_source()
    }
    /// Owned root JSON inspection handle.
    pub fn root(&self) -> Value {
        self.0.json.root()
    }
    /// Serialization detected from the original document bytes.
    pub fn format(&self) -> crate::SourceFormat {
        self.0.json.source_format()
    }
    /// Version spelling, with missing/non-string represented as absent.
    pub fn version(&self) -> Option<String> {
        self.root()
            .get("openapi")
            .or_else(|| self.root().get("swagger"))
            .and_then(|v| v.as_str().map(str::to_owned))
    }
    /// Detected semantic family; invalid or unsupported declarations remain inspectable.
    pub fn edition(&self) -> Result<Edition, Diagnostic> {
        self.0.edition.clone()
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
            Err(e.clone().bind_graph(&self.0))
        } else {
            Err(self.root().error(Code::MissingOperation))
        }
    }
    /// Select by a unique operation ID; duplicates are not silently resolved.
    pub fn operation_id(&self, id: &str) -> Result<Operation, Diagnostic> {
        if let Some(error) = self
            .0
            .path_order
            .iter()
            .filter(|path| self.0.incomplete_paths.contains(*path))
            .find_map(|path| self.0.path_errors.get(path))
        {
            let mut error = error.clone();
            error.code = Code::IncompleteDiscovery;
            return Err(error.bind_graph(&self.0));
        }
        match self.0.ids.get(id) {
            Some(v) if v.len() == 1 => Ok(Operation {
                store: self.0.clone(),
                index: v[0],
            }),
            Some(_) => Err(self.root().error(Code::AmbiguousOperation)),
            None => Err(self.root().error(Code::MissingOperation)),
        }
    }
    /// Obtain a typed protocol site in the entry source.
    pub fn protocol_at(
        &self,
        pointer: &str,
        kind: ReferenceKind,
    ) -> Result<crate::ProtocolSite, Diagnostic> {
        self.entry_resource().protocol_at(pointer, kind)
    }
    /// Resolve a validated site from this exact snapshot. This never acquires resources.
    pub fn resolve_protocol(
        &self,
        site: &crate::ProtocolSite,
    ) -> Result<ResolvedReference, Diagnostic> {
        if !Arc::ptr_eq(&self.0, &site.store) {
            return Err(Diagnostic::new(Code::SnapshotMismatch));
        }
        self.0
            .graph
            .resolve(&site.node, site.kind, self.0.limits.reference_steps)
            .map_err(|e| e.bind_graph(&self.0))
    }
    /// Entry source handle, retaining its original identity and bytes.
    pub fn entry_resource(&self) -> crate::ResourceHandle {
        crate::ResourceHandle {
            store: self.0.clone(),
            id: self.0.entry,
        }
    }
    /// Select an admitted source by its stable identity.
    pub fn resource(&self, id: crate::ResourceId) -> Option<crate::ResourceHandle> {
        self.0.graph.resource(id).map(|_| crate::ResourceHandle {
            store: self.0.clone(),
            id,
        })
    }
    /// All admitted original sources in admission order.
    pub fn resources(&self) -> impl ExactSizeIterator<Item = crate::ResourceHandle> + '_ {
        self.0
            .graph
            .resources
            .iter()
            .map(|r| crate::ResourceHandle {
                store: self.0.clone(),
                id: r.id,
            })
    }
    /// Whether all mounted entry paths were discovered without local path errors.
    pub fn discovery_complete(&self) -> bool {
        self.0.incomplete_paths.is_empty()
    }
    /// Located path issues, with uniformly available typed resource needs.
    pub fn graph_issues(&self) -> impl Iterator<Item = Diagnostic> + '_ {
        self.0
            .path_order
            .iter()
            .filter_map(|path| self.0.path_errors.get(path))
            .cloned()
            .map(|e| e.bind_graph(&self.0))
    }
    /// Accounted immutable JSON storage; caches are reported separately.
    pub fn retained_bytes(&self) -> usize {
        self.0
            .graph
            .resources
            .iter()
            .map(|r| r.json.retained_bytes())
            .sum()
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
        self.record().method.clone()
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
    /// Raw declarations and inherited raw fields without semantic preparation.
    /// Use `describe` for edition-independent body, server and security descriptions.
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
