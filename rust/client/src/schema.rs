//! Immutable schema identity and direct-edge inspection. This is not validation.
use crate::document::Store;
use crate::graph::{
    Context, Graph, NeedFact, NodeKey, ObjectKind, bound, identity_key, key, resolve_uri,
    without_fragment,
};
use crate::{
    Cancellation, Code, Diagnostic, DiagnosticReason, Edition, LimitKind, Limits, Location,
    ReferenceKind, ResourceFragment, ResourceHandle, ResourceId, ResourceInterpretation,
    ResourceNeedKind, SourceContext, Value, ValueKind,
};
use std::{
    collections::{HashMap, HashSet, VecDeque},
    sync::Arc,
};

const DRAFT2020: &str = "https://json-schema.org/draft/2020-12/schema";
const OAS31: &str = "https://spec.openapis.org/oas/3.1/dialect/base";
const OAS32: &str = "https://spec.openapis.org/oas/3.2/dialect/2026-02-26";

/// Explicit schema interpretation for authors of otherwise untyped layouts.
#[derive(Clone, PartialEq, Eq)]
#[non_exhaustive]
pub enum SchemaProfile {
    /// Swagger 2.0's Schema Object subset, with reference-object sibling rules.
    Swagger20,
    /// OpenAPI 3.0's Schema Object subset, with reference-object sibling rules.
    OpenApi30,
    /// A declared JSON Schema dialect. Unrecognized dialects remain opaque.
    JsonSchema {
        /// Absolute dialect identity; no retrieval is performed.
        dialect_uri: String,
    },
}
impl std::fmt::Debug for SchemaProfile {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(match self {
            Self::Swagger20 => "Swagger20",
            Self::OpenApi30 => "OpenApi30",
            Self::JsonSchema { .. } => "JsonSchema { .. }",
        })
    }
}
impl SchemaProfile {
    pub(crate) fn modern(&self) -> bool {
        matches!(self, Self::JsonSchema { .. })
    }
    fn capability(&self) -> DialectCapability {
        match self {
            Self::JsonSchema { dialect_uri }
                if !matches!(
                    dialect_uri.as_str(),
                    DRAFT2020
                        | OAS31
                        | OAS32
                        | "https://spec.openapis.org/oas/3.1/dialect/2024-11-10"
                ) =>
            {
                DialectCapability::Opaque
            }
            _ => DialectCapability::Supported,
        }
    }
    fn for_edition(edition: Edition) -> Self {
        match edition {
            Edition::Swagger20 => Self::Swagger20,
            Edition::OpenApi30 => Self::OpenApi30,
            Edition::OpenApi31 => Self::JsonSchema {
                dialect_uri: OAS31.into(),
            },
            Edition::OpenApi32 => Self::JsonSchema {
                dialect_uri: OAS32.into(),
            },
        }
    }
}
/// Whether the library can interpret this schema dialect's keywords.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum DialectCapability {
    /// Supported built-in profile.
    Supported,
    /// Original data remains inspectable; keyword semantics are unknown.
    Opaque,
}
/// Precise selected-schema refusal, never an instance validation result.
#[derive(Clone, PartialEq, Eq)]
#[non_exhaustive]
#[allow(missing_docs)]
pub enum SchemaReason {
    OpaqueDialect,
    InvalidDialectSite,
    InvalidKeyword,
    RequiresDynamicScope,
    Composition,
    RecursiveFact,
    ConflictingFacts,
    DynamicPropertyFacts,
    ProhibitingSchema,
    ContextConflict {
        other_source: SourceContext,
        other_location: Location,
    },
}
impl std::fmt::Debug for SchemaReason {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let name = match self {
            Self::OpaqueDialect => "OpaqueDialect",
            Self::InvalidDialectSite => "InvalidDialectSite",
            Self::InvalidKeyword => "InvalidKeyword",
            Self::RequiresDynamicScope => "RequiresDynamicScope",
            Self::Composition => "Composition",
            Self::RecursiveFact => "RecursiveFact",
            Self::ConflictingFacts => "ConflictingFacts",
            Self::DynamicPropertyFacts => "DynamicPropertyFacts",
            Self::ProhibitingSchema => "ProhibitingSchema",
            Self::ContextConflict {
                other_source,
                other_location,
            } => {
                return f
                    .debug_struct("ContextConflict")
                    .field("other_source", other_source)
                    .field("start", &other_location.start)
                    .field("end", &other_location.end)
                    .finish_non_exhaustive();
            }
        };
        f.write_str(name)
    }
}
pub(crate) fn schema_error(value: &Value, code: Code, reason: SchemaReason) -> Diagnostic {
    value.error(code).reasoned(DiagnosticReason::Schema(reason))
}

/// Owning typed schema location in one immutable document snapshot.
#[derive(Clone)]
pub struct SchemaHandle {
    pub(crate) store: Arc<Store>,
    pub(crate) key: NodeKey,
    relative: String,
}
impl std::fmt::Debug for SchemaHandle {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("SchemaHandle")
            .field("capability", &self.dialect_capability())
            .finish_non_exhaustive()
    }
}
impl ResourceHandle {
    /// Inspect an indexed schema position; arbitrary JSON is not a schema assertion.
    pub fn schema_at(&self, pointer: &str) -> Result<SchemaHandle, Diagnostic> {
        let value = self.root().pointer(pointer)?;
        SchemaHandle::new(self.store.clone(), key(&value)).map_err(|e| e.bind_graph(&self.store))
    }
}
impl SchemaHandle {
    fn new(store: Arc<Store>, node: NodeKey) -> Result<Self, Diagnostic> {
        if let Some(issue) = store
            .graph
            .schemas
            .nodes
            .get(&node)
            .and_then(|n| n.issue.as_ref())
        {
            return Err(issue.clone());
        }
        let scope = store
            .graph
            .schemas
            .scope(node)
            .ok_or_else(|| store.graph.value(node).error(Code::WrongReferenceKind))?;
        let physical = store.graph.value(node).location().pointer;
        let prefix = store.graph.value(scope.root).location().pointer;
        let relative = physical
            .strip_prefix(&prefix)
            .expect("lexical schema ancestor")
            .to_owned();
        Ok(Self {
            store,
            key: node,
            relative,
        })
    }
    fn scope(&self) -> &Scope {
        self.store
            .graph
            .schemas
            .scope(self.key)
            .expect("typed handle")
    }
    /// Exact authored schema, without flattening or defaults.
    pub fn authored(&self) -> Value {
        self.store.graph.value(self.key)
    }
    /// Owning physical source resource.
    pub fn resource(&self) -> ResourceHandle {
        ResourceHandle {
            store: self.store.clone(),
            id: ResourceId(self.key.0),
        }
    }
    /// Applied schema profile, independently from the authored OpenAPI patch spelling.
    pub fn profile(&self) -> &SchemaProfile {
        &self.scope().profile
    }
    /// Whether direct keyword semantics are supported.
    pub fn dialect_capability(&self) -> DialectCapability {
        self.profile().capability()
    }
    /// Lexical schema resource root; the physical source root when no `$id` intervenes.
    pub fn schema_resource_root(&self) -> Value {
        self.store.graph.value(self.scope().root)
    }
    /// Canonical lexical resource identity, when one exists.
    pub fn schema_resource_uri(&self) -> Option<&str> {
        self.scope().uri.as_deref()
    }
    /// Base used for this schema's relative references.
    pub fn effective_base(&self) -> Option<&str> {
        self.scope().base.as_deref()
    }
    /// Physical pointer relative to the lexical schema resource root.
    pub fn resource_relative_pointer(&self) -> &str {
        &self.relative
    }
    /// Direct reference keywords in authored order. Opaque semantics are an error.
    pub fn references(&self) -> Result<SchemaReferences, Diagnostic> {
        self.store
            .graph
            .schemas
            .check(self.key, &self.store.graph)
            .map_err(|e| e.bind_graph(&self.store))?;
        let mut edges = Vec::with_capacity(2);
        if let Some(members) = self.authored().members() {
            for (name, value) in members {
                let kind = match name {
                    "$ref" => SchemaReferenceKind::Ref,
                    "$dynamicRef" if self.profile().modern() => SchemaReferenceKind::DynamicRef,
                    _ => continue,
                };
                if value.as_str().is_none() {
                    return Err(value.error(Code::InvalidReference));
                }
                edges.push(SchemaReference {
                    source: self.clone(),
                    edge: key(&value),
                    kind,
                });
            }
        }
        Ok(SchemaReferences(edges.into_iter()))
    }
}
/// Iterator over direct authored schema edges.
pub struct SchemaReferences(std::vec::IntoIter<SchemaReference>);
impl Iterator for SchemaReferences {
    type Item = SchemaReference;
    fn next(&mut self) -> Option<Self::Item> {
        self.0.next()
    }
    fn size_hint(&self) -> (usize, Option<usize>) {
        self.0.size_hint()
    }
}
impl ExactSizeIterator for SchemaReferences {}
/// Authored schema reference keyword.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum SchemaReferenceKind {
    /// Ordinary static reference.
    Ref,
    /// Initial static target of a dynamic reference.
    DynamicRef,
}
/// Owning edge bound to its originating schema snapshot.
#[derive(Clone, Debug)]
pub struct SchemaReference {
    source: SchemaHandle,
    edge: NodeKey,
    kind: SchemaReferenceKind,
}
impl SchemaReference {
    /// Schema containing this edge.
    pub fn source(&self) -> &SchemaHandle {
        &self.source
    }
    /// Authored reference keyword value and exact source location.
    pub fn authored(&self) -> Value {
        self.source.store.graph.value(self.edge)
    }
    /// Static or dynamic keyword.
    pub fn kind(&self) -> SchemaReferenceKind {
        self.kind
    }
    /// Resolve one static edge without fetching, evaluation, or following a chain.
    pub fn resolve_static(&self) -> Result<ResolvedSchemaReference, Diagnostic> {
        self.resolve_static_with_cancellation(&Cancellation::default())
    }
    /// One-edge resolution with caller cancellation and the configured allowance.
    pub fn resolve_static_with_cancellation(
        &self,
        cancellation: &Cancellation,
    ) -> Result<ResolvedSchemaReference, Diagnostic> {
        checkpoint(cancellation)?;
        bound(
            0,
            1,
            self.source.store.limits.reference_steps,
            LimitKind::ReferenceDepth,
        )
        .map_err(|e| self.authored().locate(e))?;
        let graph = &self.source.store.graph;
        let resolved = graph
            .schema_target(self.source.key, self.edge, self.kind, cancellation)
            .map_err(|e| e.bind_graph(&self.source.store))?;
        let target = SchemaHandle::new(self.source.store.clone(), resolved.target)?;
        Ok(ResolvedSchemaReference {
            reference: self.clone(),
            target,
            canonical: resolved.reason.is_none(),
            dynamic: resolved.dynamic,
            reason: resolved.reason,
        })
    }
}
/// Why a selected physical path is a noncanonical access to a schema resource.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum SchemaAccessReason {
    /// A retrieval alias selected a canonical `$id` resource.
    RetrievalAlias,
    /// A physical JSON Pointer crossed an embedded `$id` boundary.
    CrossingPointer,
}
/// One direct edge's initial static target and access provenance.
#[derive(Clone, Debug)]
pub struct ResolvedSchemaReference {
    reference: SchemaReference,
    target: SchemaHandle,
    canonical: bool,
    dynamic: bool,
    reason: Option<SchemaAccessReason>,
}
impl ResolvedSchemaReference {
    /// Original owning edge.
    pub fn reference(&self) -> &SchemaReference {
        &self.reference
    }
    /// Indexed target under its lexical context.
    pub fn target(&self) -> &SchemaHandle {
        &self.target
    }
    /// Whether the access uses the target's lexical canonical resource identity.
    pub fn canonical(&self) -> bool {
        self.canonical
    }
    /// Whether evaluating this dynamic anchor requires runtime dynamic scope.
    pub fn requires_dynamic_scope(&self) -> bool {
        self.dynamic
    }
    /// Noncanonical access reason, when applicable.
    pub fn access_reason(&self) -> Option<SchemaAccessReason> {
        self.reason
    }
}

// Every retained key, diagnostic and scope below is owner-free. No public handle
// can be retained by the store that it owns.
#[derive(Clone)]
pub(crate) struct Scope {
    pub root: NodeKey,
    pub profile: Arc<SchemaProfile>,
    pub fallback: Arc<SchemaProfile>,
    pub base: Option<Arc<str>>,
    pub uri: Option<Arc<str>>,
}
struct Node {
    scope: usize,
    issue: Option<Diagnostic>,
}
#[derive(Default)]
pub(crate) struct SchemaIndex {
    nodes: HashMap<NodeKey, Node>,
    scopes: Vec<Scope>,
    pub identities: HashMap<String, NodeKey>,
    anchors: HashMap<NodeKey, HashMap<String, (NodeKey, bool)>>,
    roots: HashSet<NodeKey>,
    inferred: HashMap<NodeKey, (Arc<SchemaProfile>, NodeKey)>,
    bytes: usize,
    edges: usize,
    anonymous_anchors: usize,
}
impl SchemaIndex {
    pub(crate) fn scope(&self, node: NodeKey) -> Option<&Scope> {
        self.nodes.get(&node).map(|n| &self.scopes[n.scope])
    }
    pub(crate) fn check(&self, node: NodeKey, graph: &Graph) -> Result<(), Diagnostic> {
        let item = self
            .nodes
            .get(&node)
            .ok_or_else(|| graph.value(node).error(Code::WrongReferenceKind))?;
        if let Some(issue) = &item.issue {
            return Err(issue.clone());
        }
        if self.scopes[item.scope].profile.capability() == DialectCapability::Opaque {
            return Err(schema_error(
                &graph.value(node),
                Code::UnsupportedSchema,
                SchemaReason::OpaqueDialect,
            ));
        }
        Ok(())
    }
    fn charge(
        graph: &mut Graph,
        bytes: usize,
        identities: usize,
        value: &Value,
    ) -> Result<(), Diagnostic> {
        let (existing_identities, existing_bytes) = graph.metadata_usage();
        bound(
            existing_bytes,
            graph.schemas.bytes.saturating_add(bytes),
            graph.limits.metadata_bytes,
            LimitKind::GraphMetadataBytes,
        )
        .map_err(|e| value.locate(e))?;
        bound(
            existing_identities,
            graph
                .schemas
                .identities
                .len()
                .saturating_add(graph.schemas.anonymous_anchors)
                .saturating_add(identities),
            graph.limits.identities,
            LimitKind::GraphIdentities,
        )
        .map_err(|e| value.locate(e))?;
        graph.schemas.bytes += bytes;
        Ok(())
    }
    fn identity(graph: &mut Graph, uri: &str, value: &Value) -> Result<(), Diagnostic> {
        let identity = identity_key(uri).map_err(|e| value.locate(e))?;
        let previous = graph
            .schemas
            .identities
            .get(&identity)
            .copied()
            .or(graph.physical_identity(uri)?);
        if let Some(previous) = previous {
            if previous != key(value) {
                return Err(value.locate(Diagnostic::identity_collision(&graph.value(previous))));
            }
            return Ok(());
        }
        Self::charge(graph, identity.len(), 1, value)?;
        graph.schemas.identities.insert(identity, key(value));
        Ok(())
    }
    pub(crate) fn build(
        graph: &mut Graph,
        limits: &Limits,
        cancellation: &Cancellation,
    ) -> Result<(), Diagnostic> {
        let mut seeds = Vec::new();
        // Only schema-bearing contexts need ordering or owned profiles. Keep the
        // same authored order for their admission and diagnostics.
        let mut contexts = Vec::new();
        for (&node, &context) in &graph.contexts {
            checkpoint(cancellation)?;
            if has_protocol_schema(&graph.value(node), context) {
                contexts.push((node, context));
            }
        }
        contexts.sort_by_key(|(k, _)| *k);
        let mut profiles = None;
        for (node, context) in contexts {
            checkpoint(cancellation)?;
            let value = graph.value(node);
            if profiles.as_ref().is_none_or(|(source, edition, _, _)| {
                *source != node.0 || *edition != context.edition
            }) {
                let root = graph.resource_root(&value)?;
                let fallback = Arc::new(SchemaProfile::for_edition(context.edition));
                let mut profile = fallback.clone();
                // Only a recognized actual OpenAPI root can set jsonSchemaDialect.
                if matches!(context.edition, Edition::OpenApi31 | Edition::OpenApi32)
                    && let Some(dialect) = root.get("jsonSchemaDialect")
                    && Edition::detect(&root).is_ok()
                    && let Ok(declared) = profile_from(&dialect, limits.target_bytes)
                {
                    profile = Arc::new(declared);
                }
                profiles = Some((node.0, context.edition, profile, fallback));
            }
            let (_, _, profile, fallback) = profiles.as_ref().expect("selected context profiles");
            let mut seed = |value| {
                checkpoint(cancellation)?;
                push_seed(
                    &mut seeds,
                    Seed {
                        value,
                        profile: profile.clone(),
                        fallback: fallback.clone(),
                        origin: None,
                    },
                    graph.limits.nodes,
                )
            };
            match context.kind {
                ObjectKind::Document => {
                    let map = if context.edition == Edition::Swagger20 {
                        value.get("definitions")
                    } else {
                        value.get("components").and_then(|v| v.get("schemas"))
                    };
                    if let Some(map) = map
                        && let Some(values) = map.members()
                    {
                        for (_, value) in values {
                            seed(value)?;
                        }
                    }
                }
                ObjectKind::Reference(ReferenceKind::Parameter)
                    if context.edition != Edition::Swagger20
                        || value.get("in").is_some_and(|v| v.as_str() == Some("body")) =>
                {
                    if value.get("$ref").is_none()
                        && let Some(schema) = value.get("schema")
                    {
                        seed(schema)?;
                    }
                }
                ObjectKind::Reference(ReferenceKind::Header)
                    if context.edition != Edition::Swagger20 =>
                {
                    if value.get("$ref").is_none()
                        && let Some(schema) = value.get("schema")
                    {
                        seed(schema)?;
                    }
                }
                ObjectKind::Reference(ReferenceKind::Response)
                    if context.edition == Edition::Swagger20 =>
                {
                    if value.get("$ref").is_none()
                        && let Some(schema) = value.get("schema")
                    {
                        seed(schema)?;
                    }
                }
                ObjectKind::Media | ObjectKind::Reference(ReferenceKind::MediaType) => {
                    if let Some(schema) = value.get("schema") {
                        seed(schema)?;
                    }
                    if context.edition == Edition::OpenApi32
                        && let Some(schema) = value.get("itemSchema")
                    {
                        seed(schema)?;
                    }
                }
                _ => {}
            }
        }
        for resource in &graph.resources {
            checkpoint(cancellation)?;
            let root = resource.json.root();
            for layout in &resource.layout.roots {
                checkpoint(cancellation)?;
                if let Some(default_profile) = layout.schema_profile() {
                    validate_profile(default_profile).map_err(|e| root.locate(e))?;
                    let profile = Arc::new(default_profile.clone());
                    push_seed(
                        &mut seeds,
                        Seed {
                            value: root.pointer(layout.pointer())?,
                            profile: profile.clone(),
                            fallback: profile,
                            origin: None,
                        },
                        graph.limits.nodes,
                    )?;
                }
            }
            if root.get("openapi").is_none()
                && root.get("swagger").is_none()
                && let Some(dialect) = root.get("$schema")
            {
                let profile = Arc::new(profile_from(&dialect, limits.target_bytes).unwrap_or(
                    SchemaProfile::JsonSchema {
                        dialect_uri: DRAFT2020.into(),
                    },
                ));
                push_seed(
                    &mut seeds,
                    Seed {
                        value: root,
                        profile: profile.clone(),
                        fallback: profile,
                        origin: None,
                    },
                    graph.limits.nodes,
                )?;
            }
        }
        bound(0, seeds.len(), graph.limits.nodes, LimitKind::GraphNodes)?;
        let mut edges = VecDeque::new();
        for seed in seeds {
            Self::index_root(graph, seed, &mut edges, limits, cancellation)?;
        }
        // All explicit contexts exist before inferring any supplied bare root.
        // Each discovered edge is considered once. Already indexed targets do
        // not need re-traversal; additional fallback claims are still compared.
        while let Some((node, edge)) = edges.pop_front() {
            checkpoint(cancellation)?;
            let scope = graph.schemas.scope(node).expect("indexed edge").clone();
            let value = graph.value(edge);
            let Some(text) = value.as_str() else { continue };
            let Ok(address) = graph.address(&value, text, scope.base.as_deref(), scope.root) else {
                continue;
            };
            if address.fragment != ResourceFragment::Root {
                continue;
            }
            let Some(target) = address.exact.or(address.root) else {
                continue;
            };
            if target.1 != 0 {
                continue;
            }
            let target_value = graph.value(target);
            let target_resource = graph.owner(&target_value)?;
            if target_value.get("openapi").is_some()
                || target_value.get("swagger").is_some()
                || target_value.get("$schema").is_some()
                || target_resource
                    .layout
                    .roots
                    .iter()
                    .any(|r| r.pointer().is_empty())
            {
                continue;
            }
            if let Some((previous, previous_edge)) = graph.schemas.inferred.get(&target) {
                if previous.as_ref() != scope.fallback.as_ref() {
                    let other = graph.value(*previous_edge);
                    return Err(schema_error(
                        &value,
                        Code::ConflictingObjectContext,
                        SchemaReason::ContextConflict {
                            other_source: other.source_context(),
                            other_location: other.location(),
                        },
                    ));
                }
            } else if !graph.schemas.nodes.contains_key(&target) {
                graph
                    .schemas
                    .inferred
                    .insert(target, (scope.fallback.clone(), edge));
                Self::index_root(
                    graph,
                    Seed {
                        value: target_value,
                        profile: scope.fallback.clone(),
                        fallback: scope.fallback,
                        origin: Some(edge),
                    },
                    &mut edges,
                    limits,
                    cancellation,
                )?;
            }
        }
        Ok(())
    }
    fn index_root(
        graph: &mut Graph,
        seed: Seed,
        edges: &mut VecDeque<(NodeKey, NodeKey)>,
        limits: &Limits,
        cancellation: &Cancellation,
    ) -> Result<(), Diagnostic> {
        let owner = graph.owner(&seed.value)?;
        let base_len = owner.base.as_ref().map_or(0, String::len);
        let owner_id = owner.id;
        Self::charge(graph, base_len, 0, &seed.value)?;
        let owner = graph.resource(owner_id).expect("source resource");
        let base = owner.base.as_deref().map(Arc::<str>::from);
        let initial = Scope {
            root: (owner.id.0, 0),
            profile: seed.profile,
            fallback: seed.fallback,
            uri: base.clone(),
            base,
        };
        let mut queue = VecDeque::new();
        queue.push_back((seed.value, initial, true));
        while let Some((value, mut scope, entry)) = queue.pop_front() {
            checkpoint(cancellation)?;
            bound(
                graph.schemas.nodes.len(),
                usize::from(!graph.schemas.nodes.contains_key(&key(&value))),
                graph.limits.nodes,
                LimitKind::GraphNodes,
            )
            .map_err(|e| value.locate(e))?;
            let mut issue = if entry && value.get("$schema").is_none() {
                let owner = graph.owner(&value)?;
                let root = owner.json.root();
                if let Some(dialect) = root.get("jsonSchemaDialect")
                    && matches!(
                        Edition::detect(&root),
                        Ok(Edition::OpenApi31 | Edition::OpenApi32)
                    )
                {
                    profile_from(&dialect, limits.target_bytes).err()
                } else {
                    None
                }
            } else {
                None
            };
            let mut new_identity = None;
            let mut boundary = entry;
            if entry
                && scope.profile.modern()
                && let Some(dialect) = value.get("$schema")
            {
                match profile_from(&dialect, limits.target_bytes) {
                    Ok(profile) => scope.profile = Arc::new(profile),
                    Err(error) => issue = Some(error),
                }
            }
            if scope.profile.modern() && scope.profile.capability() == DialectCapability::Supported
            {
                if let Some(id) = value.get("$id") {
                    let result = (|| {
                        let text = id.as_str().ok_or_else(|| {
                            schema_error(
                                &id,
                                Code::InvalidDeclaration,
                                SchemaReason::InvalidKeyword,
                            )
                        })?;
                        bound(0, text.len(), limits.target_bytes, LimitKind::TargetBytes)
                            .map_err(|e| id.locate(e))?;
                        let uri =
                            resolve_uri(text, scope.base.as_deref()).map_err(|e| id.locate(e))?;
                        if uri.split_once('#').is_some_and(|(_, f)| !f.is_empty()) {
                            return Err(id.error(Code::InvalidResourceUri));
                        }
                        Ok(without_fragment(&uri).to_owned())
                    })();
                    match result {
                        Ok(uri) => {
                            new_identity = Some((uri.clone(), id.clone()));
                            scope.root = key(&value);
                            scope.base = Some(Arc::from(uri.as_str()));
                            scope.uri = scope.base.clone();
                            boundary = true;
                        }
                        Err(error) if matches!(error.code(), Code::Limit | Code::Cancelled) => {
                            return Err(error);
                        }
                        Err(error) => issue = Some(error),
                    }
                }
                if let Some(dialect) = value.get("$schema") {
                    if boundary {
                        match profile_from(&dialect, limits.target_bytes) {
                            Ok(profile) => {
                                Self::charge(
                                    graph,
                                    dialect.as_str().map_or(0, str::len),
                                    0,
                                    &dialect,
                                )?;
                                scope.profile = Arc::new(profile);
                            }
                            Err(error) => issue = Some(error),
                        }
                    } else {
                        issue = Some(schema_error(
                            &dialect,
                            Code::InvalidDeclaration,
                            SchemaReason::InvalidDialectSite,
                        ));
                    }
                }
            }
            if let Some(error) = &issue
                && matches!(error.code(), Code::Limit | Code::Cancelled)
            {
                return Err(error.clone());
            }
            if let Some(previous) = graph.schemas.scope(key(&value)) {
                if previous.profile != scope.profile
                    || previous.root != scope.root
                    || previous.base != scope.base
                {
                    return Err(value.error(Code::ConflictingObjectContext));
                }
                continue;
            }
            if let Some((uri, id)) = new_identity {
                Self::charge(graph, uri.len(), 0, &id)?;
                Self::identity(graph, &uri, &value)?;
            }
            if value.kind() != ValueKind::Object
                && !(scope.profile.modern() && value.kind() == ValueKind::Boolean)
            {
                issue = Some(schema_error(
                    &value,
                    Code::InvalidDeclaration,
                    SchemaReason::InvalidKeyword,
                ));
            }
            graph.schemas.roots.insert(scope.root);
            let scope_id = graph.schemas.scopes.len();
            graph.schemas.scopes.push(scope.clone());
            graph.schemas.nodes.insert(
                key(&value),
                Node {
                    scope: scope_id,
                    issue,
                },
            );
            if scope.profile.capability() == DialectCapability::Opaque
                || graph.schemas.nodes[&key(&value)].issue.is_some()
            {
                continue;
            }
            if scope.profile.modern() {
                for keyword in ["$anchor", "$dynamicAnchor"] {
                    if let Some(anchor) = value.get(keyword) {
                        let Some(name) = anchor.as_str().filter(|s| anchor_name(s)) else {
                            graph
                                .schemas
                                .nodes
                                .get_mut(&key(&value))
                                .expect("indexed")
                                .issue = Some(schema_error(
                                &anchor,
                                Code::InvalidDeclaration,
                                SchemaReason::InvalidKeyword,
                            ));
                            continue;
                        };
                        if let Some(&(previous, dynamic)) = graph
                            .schemas
                            .anchors
                            .get(&scope.root)
                            .and_then(|anchors| anchors.get(name))
                        {
                            let previous = graph
                                .value(previous)
                                .get(if dynamic { "$dynamicAnchor" } else { "$anchor" })
                                .expect("indexed anchor declaration");
                            return Err(anchor.locate(Diagnostic::identity_collision(&previous)));
                        }
                        Self::charge(graph, name.len(), 0, &anchor)?;
                        if let Some(base) = &scope.base {
                            let uri = format!("{}#{name}", without_fragment(base));
                            Self::identity(graph, &uri, &value)?;
                        } else {
                            Self::charge(graph, 0, 1, &anchor)?;
                            graph.schemas.anonymous_anchors += 1;
                        }
                        graph
                            .schemas
                            .anchors
                            .entry(scope.root)
                            .or_default()
                            .insert(name.to_owned(), (key(&value), keyword == "$dynamicAnchor"));
                    }
                }
            }
            for keyword in ["$ref", "$dynamicRef"] {
                if keyword == "$dynamicRef" && !scope.profile.modern() {
                    continue;
                }
                if let Some(edge) = value.get(keyword) {
                    bound(
                        graph.reference_count,
                        graph.schemas.edges.saturating_add(1),
                        graph.limits.reference_edges,
                        LimitKind::GraphReferenceEdges,
                    )
                    .map_err(|e| edge.locate(e))?;
                    graph.schemas.edges += 1;
                    edges.push_back((key(&value), key(&edge)));
                }
            }
            if !scope.profile.modern() && value.get("$ref").is_some() {
                continue;
            }
            if let Some(members) = value.members() {
                for (name, child) in members {
                    checkpoint(cancellation)?;
                    let mode = schema_child_mode(name, &scope.profile);
                    let mut enqueue = |child: Value| -> Result<(), Diagnostic> {
                        checkpoint(cancellation)?;
                        bound(
                            graph.schemas.nodes.len(),
                            queue.len().saturating_add(1),
                            graph.limits.nodes,
                            LimitKind::GraphNodes,
                        )
                        .map_err(|e| child.locate(e))?;
                        queue.push_back((child, scope.clone(), false));
                        Ok(())
                    };
                    match mode {
                        1 => enqueue(child)?,
                        2 => {
                            if let Some(members) = child.members() {
                                for (_, child) in members {
                                    enqueue(child)?;
                                }
                            }
                        }
                        3 => {
                            if let Some(items) = child.elements() {
                                for child in items {
                                    enqueue(child)?;
                                }
                            }
                        }
                        _ => {}
                    }
                }
            }
        }
        let _ = seed.origin; // origin is retained by inferred claims, never by an owning handle.
        Ok(())
    }
}
// This predicate performs no semantic admission: the ordered loop above still
// owns seed bounds, profiles and error sites. Empty/malformed containers produce
// no seeds, just as they do in that loop.
fn has_protocol_schema(value: &Value, context: Context) -> bool {
    match context.kind {
        ObjectKind::Document => {
            let map = if context.edition == Edition::Swagger20 {
                value.get("definitions")
            } else {
                value.get("components").and_then(|v| v.get("schemas"))
            };
            map.is_some_and(|v| v.members().is_some_and(|m| m.len() != 0))
        }
        ObjectKind::Reference(ReferenceKind::Parameter)
            if context.edition != Edition::Swagger20
                || value.get("in").is_some_and(|v| v.as_str() == Some("body")) =>
        {
            value.get("$ref").is_none() && value.get("schema").is_some()
        }
        ObjectKind::Reference(ReferenceKind::Header) if context.edition != Edition::Swagger20 => {
            value.get("$ref").is_none() && value.get("schema").is_some()
        }
        ObjectKind::Reference(ReferenceKind::Response) if context.edition == Edition::Swagger20 => {
            value.get("$ref").is_none() && value.get("schema").is_some()
        }
        ObjectKind::Media | ObjectKind::Reference(ReferenceKind::MediaType) => {
            value.get("schema").is_some()
                || context.edition == Edition::OpenApi32 && value.get("itemSchema").is_some()
        }
        _ => false,
    }
}
struct Seed {
    value: Value,
    profile: Arc<SchemaProfile>,
    fallback: Arc<SchemaProfile>,
    origin: Option<NodeKey>,
}
fn push_seed(seeds: &mut Vec<Seed>, seed: Seed, maximum: usize) -> Result<(), Diagnostic> {
    bound(seeds.len(), 1, maximum, LimitKind::GraphNodes).map_err(|e| seed.value.locate(e))?;
    seeds.push(seed);
    Ok(())
}
fn schema_child_mode(name: &str, profile: &SchemaProfile) -> u8 {
    match name {
        "properties" => 2,
        "items" | "additionalProperties" => 1,
        "allOf" => 3,
        "anyOf" | "oneOf" if *profile != SchemaProfile::Swagger20 => 3,
        "not" if *profile != SchemaProfile::Swagger20 => 1,
        "$defs" | "patternProperties" | "dependentSchemas" if profile.modern() => 2,
        "prefixItems" if profile.modern() => 3,
        "contains"
        | "propertyNames"
        | "if"
        | "then"
        | "else"
        | "unevaluatedItems"
        | "unevaluatedProperties"
        | "contentSchema"
            if profile.modern() =>
        {
            1
        }
        _ => 0,
    }
}
fn validate_profile(profile: &SchemaProfile) -> Result<(), Diagnostic> {
    if let SchemaProfile::JsonSchema { dialect_uri } = profile {
        identity_key(dialect_uri)?;
    }
    Ok(())
}
fn profile_from(value: &Value, maximum: usize) -> Result<SchemaProfile, Diagnostic> {
    let text = value.as_str().ok_or_else(|| {
        schema_error(
            value,
            Code::InvalidDeclaration,
            SchemaReason::InvalidKeyword,
        )
    })?;
    bound(0, text.len(), maximum, LimitKind::TargetBytes).map_err(|e| value.locate(e))?;
    let profile = SchemaProfile::JsonSchema {
        dialect_uri: text.to_owned(),
    };
    validate_profile(&profile).map_err(|_| {
        schema_error(
            value,
            Code::InvalidDeclaration,
            SchemaReason::InvalidKeyword,
        )
    })?;
    Ok(profile)
}
fn anchor_name(name: &str) -> bool {
    let mut chars = name.chars();
    chars
        .next()
        .is_some_and(|c| c.is_ascii_alphabetic() || c == '_')
        && chars.all(|c| c.is_ascii_alphanumeric() || matches!(c, '-' | '_' | '.'))
}
pub(crate) struct StaticTarget {
    pub target: NodeKey,
    pub dynamic: bool,
    pub reason: Option<SchemaAccessReason>,
}
impl Graph {
    pub(crate) fn schema_target(
        &self,
        node: NodeKey,
        edge: NodeKey,
        kind: SchemaReferenceKind,
        cancellation: &Cancellation,
    ) -> Result<StaticTarget, Diagnostic> {
        checkpoint(cancellation)?;
        self.schemas.check(node, self)?;
        let source = self.value(edge);
        let reference = source
            .as_str()
            .ok_or_else(|| source.error(Code::InvalidReference))?;
        let scope = self.schemas.scope(node).expect("checked");
        let address = self.address(&source, reference, scope.base.as_deref(), scope.root)?;
        checkpoint(cancellation)?;
        let request = address
            .uri
            .as_deref()
            .map(|u| without_fragment(u).to_owned());
        let need = |kind, target, uri| NeedFact {
            source: ResourceId(node.0),
            pointer: source.location().pointer,
            requested_uri: uri,
            fragment: address.fragment.clone(),
            target,
            interpretation: ResourceInterpretation::Schema {
                default_profile: scope.fallback.as_ref().clone(),
            },
            need: kind,
        };
        let Some(root) = address.root.or(address.exact) else {
            return Err(source.error(Code::ResourceUnavailable).need_fact(need(
                ResourceNeedKind::Bytes,
                None,
                request,
            )));
        };
        let resource = self
            .resource(ResourceId(root.0))
            .expect("target physical resource");
        let untyped = Edition::detect(&resource.json.root()).is_err()
            && resource.json.root().get("openapi").is_none()
            && resource.json.root().get("swagger").is_none();
        let (target, dynamic) = if let Some(exact) = address.exact {
            let dynamic = matches!(&address.fragment, ResourceFragment::Anchor(name)
                if self.schemas.anchors.get(&root).and_then(|anchors| anchors.get(name)).is_some_and(|(_, d)| *d));
            (exact, dynamic)
        } else {
            match &address.fragment {
                ResourceFragment::Root => (root, false),
                ResourceFragment::Pointer(pointer) => {
                    let target = self.value(root).pointer(pointer).map_err(|e| {
                        if e.code() == Code::InvalidPointer {
                            source.error(Code::InvalidReference)
                        } else {
                            source.locate(e)
                        }
                    })?;
                    (key(&target), false)
                }
                ResourceFragment::Anchor(name) => {
                    if !anchor_name(name) {
                        return Err(source.error(Code::InvalidReference));
                    }
                    if let Some(target) = self
                        .schemas
                        .anchors
                        .get(&root)
                        .and_then(|anchors| anchors.get(name))
                    {
                        *target
                    } else if self.schemas.nodes.contains_key(&root) || !untyped {
                        if self.schemas.scope(root).is_some() {
                            self.schemas.check(root, self)?;
                        }
                        return Err(source.error(Code::MissingReference));
                    } else if untyped {
                        return Err(source.error(Code::ResourceLayoutRequired).need_fact(need(
                            ResourceNeedKind::Layout,
                            Some(resource.id),
                            request,
                        )));
                    } else {
                        return Err(source.error(Code::MissingReference));
                    }
                }
            }
        };
        if !self.schemas.nodes.contains_key(&target) {
            // Data under a known schema/protocol tree is deliberately inert.
            // Only an untyped region is recoverable by adding a layout.
            let mut parent = self.value(target).parent_value();
            let mut inside = false;
            while let Some(value) = parent {
                checkpoint(cancellation)?;
                if self.schemas.nodes.contains_key(&key(&value)) {
                    inside = true;
                    break;
                }
                parent = value.parent_value();
            }
            if untyped && !inside && !self.contexts.contains_key(&target) {
                return Err(source.error(Code::ResourceLayoutRequired).need_fact(need(
                    ResourceNeedKind::Layout,
                    Some(resource.id),
                    request.or_else(|| resource.retrieval.clone()),
                )));
            }
            let mut error = source.error(Code::WrongReferenceKind);
            if target.0 == edge.0 {
                let previous = self.value(target).diagnostic_position();
                if error.source_context() == Some(previous.context) {
                    error.related.push(previous.location);
                }
            }
            return Err(error);
        }
        if let Err(error) = self.schemas.check(target, self)
            && error.code() != Code::UnsupportedSchema
        {
            return Err(error);
        }
        let target_scope = self.schemas.scope(target).expect("checked");
        let reason = if matches!(address.fragment, ResourceFragment::Pointer(_))
            && root != target_scope.root
        {
            Some(SchemaAccessReason::CrossingPointer)
        } else if let (Some(uri), Some(canonical)) = (&address.uri, &target_scope.uri) {
            (identity_key(without_fragment(uri))? != identity_key(canonical)?)
                .then_some(SchemaAccessReason::RetrievalAlias)
        } else {
            None
        };
        Ok(StaticTarget {
            target,
            dynamic: kind == SchemaReferenceKind::DynamicRef && dynamic,
            reason,
        })
    }
}

// Deterministic cancellation injection is private to tests. Production has only
// the ordinary caller token check, shared by indexing and selected fact work.
pub(crate) fn checkpoint(cancellation: &Cancellation) -> Result<(), Diagnostic> {
    #[cfg(test)]
    TEST_CANCEL_AFTER.with(|remaining| {
        if let Some(value) = remaining.get() {
            if value <= 1 {
                remaining.set(None);
                cancellation.cancel();
            } else {
                remaining.set(Some(value - 1));
            }
        }
    });
    cancellation.check()
}
#[cfg(test)]
thread_local! { static TEST_CANCEL_AFTER: std::cell::Cell<Option<usize>> = const { std::cell::Cell::new(None) }; }
#[cfg(test)]
pub(crate) struct TestCancelGuard;
#[cfg(test)]
impl Drop for TestCancelGuard {
    fn drop(&mut self) {
        TEST_CANCEL_AFTER.with(|remaining| remaining.set(None));
    }
}
#[cfg(test)]
pub(crate) fn cancel_after_for_test(steps: usize) -> TestCancelGuard {
    TEST_CANCEL_AFTER.with(|remaining| remaining.set(Some(steps)));
    TestCancelGuard
}
