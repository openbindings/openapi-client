//! Closed resource graphs. No acquisition or shared mutable semantic state.
use crate::document::Store;
use crate::{
    Code, Diagnostic, Edition, ExactJson, Limits, ParseFailure, ReferenceKind, Value, ValueKind,
};
use std::{
    collections::{HashMap, HashSet, VecDeque},
    sync::Arc,
};

/// Stable process-local identity of admitted source bytes, never a retrieval URI.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct ResourceId(pub(crate) usize);

/// Aggregate admission limits for a closed resource graph.
#[derive(Clone, Debug)]
#[non_exhaustive]
pub struct GraphLimits {
    /// Maximum distinct admitted sources. Aliases do not consume source slots.
    pub resources: usize,
    /// Aggregate original bytes in admitted resources.
    pub original_bytes: usize,
    /// Aggregate accounted JSON/YAML source storage; graph metadata is separately bounded.
    pub retained_bytes: usize,
    /// Aggregate indexed source values.
    pub nodes: usize,
    /// Recognized protocol and schema reference edges, excluding instance payloads.
    pub reference_edges: usize,
    /// Maximum retained retrieval/alias/canonical identity entries.
    pub identities: usize,
    /// Maximum explicit interpretation roots across all resources.
    pub layout_roots: usize,
    /// Maximum retained URI/base/pointer string bytes, separately from source arenas.
    pub metadata_bytes: usize,
}
impl Default for GraphLimits {
    fn default() -> Self {
        Self {
            resources: 256,
            original_bytes: 64 << 20,
            retained_bytes: 128 << 20,
            nodes: 2_000_000,
            reference_edges: 100_000,
            identities: 4096,
            layout_roots: 16_384,
            metadata_bytes: 4 << 20,
        }
    }
}

/// Explicit protocol roots in a non-OpenAPI resource. Unlisted payload is inert.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ResourceLayout {
    pub(crate) roots: Vec<LayoutRoot>,
}
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum LayoutRoot {
    Protocol {
        pointer: String,
        kind: ReferenceKind,
        edition: Edition,
    },
    Schema(Box<SchemaLayoutRoot>),
}
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct SchemaLayoutRoot {
    pointer: String,
    profile: crate::SchemaProfile,
}
impl LayoutRoot {
    pub(crate) fn pointer(&self) -> &str {
        match self {
            Self::Protocol { pointer, .. } => pointer,
            Self::Schema(root) => &root.pointer,
        }
    }
    pub(crate) fn schema_profile(&self) -> Option<&crate::SchemaProfile> {
        match self {
            Self::Schema(root) => Some(&root.profile),
            Self::Protocol { .. } => None,
        }
    }
    fn matches_interpretation(&self, interpretation: &ResourceInterpretation) -> bool {
        match (self, interpretation) {
            (
                Self::Protocol { kind, edition, .. },
                ResourceInterpretation::Protocol {
                    kind: other_kind,
                    edition: other_edition,
                },
            ) => kind == other_kind && edition == other_edition,
            (Self::Schema(root), ResourceInterpretation::Schema { default_profile }) => {
                &root.profile == default_profile
            }
            _ => false,
        }
    }
    fn compact(mut self) -> Self {
        let pointer = match &mut self {
            Self::Protocol { pointer, .. } => pointer,
            Self::Schema(root) => &mut root.pointer,
        };
        *pointer = std::mem::take(pointer).into_boxed_str().into_string();
        self
    }
    fn profile_bytes(&self) -> usize {
        match self.schema_profile() {
            Some(crate::SchemaProfile::JsonSchema { dialect_uri }) => dialect_uri.len(),
            _ => 0,
        }
    }
    fn metadata_bytes(&self) -> usize {
        self.pointer().len().saturating_add(self.profile_bytes())
    }
}
impl ResourceLayout {
    /// Start with no explicit interpretation roots.
    pub fn new() -> Self {
        Self::default()
    }
    /// Add a typed root. Pointer existence and interpretation are checked at admission.
    pub fn protocol(
        mut self,
        pointer: impl Into<String>,
        kind: ReferenceKind,
        edition: Edition,
    ) -> Self {
        self.roots.push(LayoutRoot::Protocol {
            pointer: pointer.into(),
            kind,
            edition,
        });
        self
    }
    /// Declare a schema root in an otherwise untyped resource.
    pub fn schema(mut self, pointer: impl Into<String>, profile: crate::SchemaProfile) -> Self {
        self.roots
            .push(LayoutRoot::Schema(Box::new(SchemaLayoutRoot {
                pointer: pointer.into(),
                profile,
            })));
        self
    }
}

/// Caller-held original bytes and optional real retrieval identity.
#[derive(Clone)]
pub struct ResourceInput {
    bytes: Arc<[u8]>,
    retrieval: Option<String>,
    aliases: Vec<String>,
    layout: ResourceLayout,
}
impl std::fmt::Debug for ResourceInput {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ResourceInput")
            .field("bytes", &self.bytes.len())
            .finish_non_exhaustive()
    }
}
impl ResourceInput {
    /// Supply bytes without inventing a base URI. Local fragments remain meaningful.
    pub fn anonymous(bytes: impl Into<Arc<[u8]>>) -> Self {
        Self {
            bytes: bytes.into(),
            retrieval: None,
            aliases: vec![],
            layout: ResourceLayout::new(),
        }
    }
    /// Supply bytes at an absolute fragment-free retrieval URI, including custom schemes.
    pub fn at(uri: &str, bytes: impl Into<Arc<[u8]>>) -> Result<Self, Diagnostic> {
        absolute_uri(uri, false)?;
        Ok(Self {
            retrieval: Some(uri.to_owned()),
            ..Self::anonymous(bytes)
        })
    }
    /// Record caller-observed requested/hop identities alongside the effective retrieval URI.
    /// This metadata grants no acquisition authority; this module never performs I/O.
    pub fn aliases(
        mut self,
        aliases: impl IntoIterator<Item = String>,
    ) -> Result<Self, Diagnostic> {
        for alias in aliases {
            absolute_uri(&alias, false)?;
            self.aliases.push(alias);
        }
        Ok(self)
    }
    /// Attach an explicit interpretation for an untyped fragment bundle.
    pub fn layout(mut self, layout: ResourceLayout) -> Self {
        self.layout = layout;
        self
    }
}

/// Which caller action can complete a resource edge.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum ResourceNeedKind {
    /// Supply the missing original resource bytes at the requested identity.
    Bytes,
    /// Enrich the existing resource with an explicit protocol-root layout.
    Layout,
}
/// Typed interpretation required at a resource target.
#[derive(Clone, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum ResourceInterpretation {
    /// A protocol declaration under its applied feature family.
    Protocol {
        /// Expected protocol declaration kind.
        kind: ReferenceKind,
        /// Applied OpenAPI feature family.
        edition: Edition,
    },
    /// A schema root, with a default when the target does not declare its dialect.
    Schema {
        /// Default profile for an otherwise unlabelled schema target.
        default_profile: crate::SchemaProfile,
    },
}
/// Decoded resource selector; authored spelling remains at ResourceNeed::source.
#[derive(Clone, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum ResourceFragment {
    /// Absent or empty URI fragment.
    Root,
    /// URI-decoded JSON Pointer, whose tilde escapes remain for pointer traversal.
    Pointer(String),
    /// URI-decoded plain-name anchor.
    Anchor(String),
}
impl ResourceFragment {
    pub(crate) fn pointer(value: String) -> Self {
        if value.is_empty() {
            Self::Root
        } else {
            Self::Pointer(value)
        }
    }
    pub(crate) fn as_pointer(&self) -> Option<&str> {
        match self {
            Self::Root => Some(""),
            Self::Pointer(p) => Some(p),
            Self::Anchor(_) => None,
        }
    }
}
#[derive(Clone, PartialEq, Eq)]
pub(crate) struct NeedFact {
    pub source: ResourceId,
    pub pointer: String,
    pub requested_uri: Option<String>,
    pub fragment: ResourceFragment,
    pub target: Option<ResourceId>,
    pub interpretation: ResourceInterpretation,
    pub need: ResourceNeedKind,
}
/// Corrective resource facts bound to the exact snapshot that produced them.
#[derive(Clone)]
pub struct ResourceNeed {
    pub(crate) store: Arc<Store>,
    pub(crate) fact: NeedFact,
}
impl PartialEq for ResourceNeed {
    fn eq(&self, other: &Self) -> bool {
        Arc::ptr_eq(&self.store, &other.store) && self.fact == other.fact
    }
}
impl Eq for ResourceNeed {}
impl std::fmt::Debug for ResourceNeed {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ResourceNeed")
            .field("kind", &self.fact.need)
            .field("expected", &self.fact.interpretation)
            .finish_non_exhaustive()
    }
}
impl ResourceNeed {
    /// Required caller action.
    pub fn kind(&self) -> ResourceNeedKind {
        self.fact.need
    }
    /// Absolute fragment-free identity to supply, when a real base exists.
    /// Document-controlled URI text is untrusted and omitted by Debug.
    pub fn requested_uri(&self) -> Option<&str> {
        self.fact.requested_uri.as_deref()
    }
    /// Expected protocol object interpretation.
    pub fn reference_kind(&self) -> Option<ReferenceKind> {
        match self.fact.interpretation {
            ResourceInterpretation::Protocol { kind, .. } => Some(kind),
            _ => None,
        }
    }
    /// Applied feature family for the required interpretation.
    pub fn edition(&self) -> Option<Edition> {
        match self.fact.interpretation {
            ResourceInterpretation::Protocol { edition, .. } => Some(edition),
            _ => None,
        }
    }
    /// Pointer selected inside the needed resource.
    pub fn target_pointer(&self) -> Option<&str> {
        self.fact.fragment.as_pointer()
    }
    /// Complete expected interpretation, including schema defaults.
    pub fn interpretation(&self) -> &ResourceInterpretation {
        &self.fact.interpretation
    }
    /// Decoded root, pointer or anchor selector.
    pub fn fragment(&self) -> &ResourceFragment {
        &self.fact.fragment
    }
    /// Existing target source for a layout need; absent for missing bytes.
    pub fn target_resource(&self) -> Option<ResourceId> {
        self.fact.target
    }
    /// Source edge retaining its original arena and exact location.
    pub fn source(&self) -> Value {
        self.store
            .graph
            .resource(self.fact.source)
            .expect("owned need resource")
            .json
            .root()
            .pointer(&self.fact.pointer)
            .expect("owned need pointer")
    }
}

/// Owned source handle with identities and typed site construction.
#[derive(Clone)]
pub struct ResourceHandle {
    pub(crate) store: Arc<Store>,
    pub(crate) id: ResourceId,
}
impl std::fmt::Debug for ResourceHandle {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ResourceHandle")
            .field("id", &self.id)
            .finish_non_exhaustive()
    }
}
impl ResourceHandle {
    /// Stable source identity, which can be shared by distinct semantic snapshots.
    pub fn id(&self) -> ResourceId {
        self.id
    }
    /// Original semantic root; its arena remains valid after this graph handle drops.
    pub fn root(&self) -> Value {
        self.resource().json.root()
    }
    /// Original JSON or YAML text.
    pub fn source(&self) -> &str {
        self.resource().json.original_source()
    }
    /// Effective retrieval identity, never replaced by `$self`.
    pub fn retrieval_uri(&self) -> Option<&str> {
        self.resource().retrieval.as_deref()
    }
    /// Applied description base, independently from the API deployment base.
    pub fn description_base(&self) -> Option<&str> {
        self.resource().base.as_deref()
    }
    /// Caller-recorded retrieval aliases; authored canonical identity is separate.
    pub fn aliases(&self) -> impl Iterator<Item = &str> {
        self.resource().aliases.iter().map(String::as_str)
    }
    /// Obtain a typed site after checking its declared protocol position.
    pub fn protocol_at(
        &self,
        pointer: &str,
        kind: ReferenceKind,
    ) -> Result<ProtocolSite, Diagnostic> {
        let node = self.root().pointer(pointer)?;
        self.store
            .graph
            .context(&node, kind)
            .map_err(|e| e.bind_graph(&self.store))?;
        Ok(ProtocolSite {
            store: self.store.clone(),
            node,
            kind,
        })
    }
    fn resource(&self) -> &Resource {
        self.store.graph.resource(self.id).expect("owned source")
    }
}
/// Typed protocol location valid only in its originating semantic snapshot.
#[derive(Clone)]
pub struct ProtocolSite {
    pub(crate) store: Arc<Store>,
    pub(crate) node: Value,
    pub(crate) kind: ReferenceKind,
}
impl std::fmt::Debug for ProtocolSite {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ProtocolSite")
            .field("kind", &self.kind)
            .finish_non_exhaustive()
    }
}
impl ProtocolSite {
    /// Authored wrapper/source value.
    pub fn authored(&self) -> Value {
        self.node.clone()
    }
    /// Validated protocol object kind.
    pub fn kind(&self) -> ReferenceKind {
        self.kind
    }
}

#[derive(Clone)]
pub(crate) struct Resource {
    pub id: ResourceId,
    pub json: ExactJson,
    pub retrieval: Option<String>,
    aliases: Vec<String>,
    pub base: Option<String>,
    canonical: Option<String>,
    pub(crate) layout: ResourceLayout,
}
/// An unpublished graph transaction. Published documents are never mutated.
pub struct DocumentBuilder {
    resources: Vec<Arc<Resource>>,
    admission: Admission,
    limits: Limits,
    graph_limits: GraphLimits,
    seed: Option<Arc<Store>>,
}
impl DocumentBuilder {
    /// Start an empty, bounded resource set.
    pub fn new(limits: Limits, graph_limits: GraphLimits) -> Self {
        Self {
            resources: vec![],
            admission: Admission::default(),
            limits,
            graph_limits,
            seed: None,
        }
    }
    /// Share source arenas from a snapshot, without copying its semantic caches.
    pub fn from_document(document: &crate::Document) -> Self {
        Self {
            resources: document.0.graph.resources.clone(),
            admission: document.0.graph.admission.clone(),
            limits: document.0.limits.clone(),
            graph_limits: document.0.graph.limits.clone(),
            seed: Some(document.0.clone()),
        }
    }
    /// Check that a need belongs to the exact source snapshot of this transaction.
    /// Shared ResourceIds do not authorize a need from another snapshot.
    pub fn check_need(&self, need: &ResourceNeed) -> Result<(), Diagnostic> {
        if self
            .seed
            .as_ref()
            .is_some_and(|s| Arc::ptr_eq(s, &need.store))
        {
            Ok(())
        } else {
            Err(Diagnostic::new(Code::SnapshotMismatch))
        }
    }
    /// Supply bytes for a need from this builder's seed snapshot.
    pub fn supply(
        &mut self,
        need: &ResourceNeed,
        input: ResourceInput,
    ) -> Result<ResourceId, ParseFailure> {
        let refusal = |diagnostic| ParseFailure {
            diagnostic,
            source: input.bytes.clone(),
        };
        self.check_need(need).map_err(refusal)?;
        metadata_input(
            &input,
            &self.limits,
            &self.graph_limits,
            &crate::Cancellation::default(),
        )
        .map_err(refusal)?;
        let requested = identity_key(
            need.requested_uri()
                .ok_or_else(|| refusal(Diagnostic::new(Code::ConflictingObjectContext)))?,
        )
        .map_err(refusal)?;
        if need.kind() != ResourceNeedKind::Bytes
            || !input
                .retrieval
                .iter()
                .chain(&input.aliases)
                .any(|uri| identity_key(uri).is_ok_and(|key| key == requested))
        {
            return Err(refusal(Diagnostic::new(Code::ConflictingObjectContext)));
        }
        self.add(input)
    }
    /// Apply a layout repair from this builder's exact seed snapshot.
    pub fn apply_layout(
        &mut self,
        need: &ResourceNeed,
        layout: ResourceLayout,
    ) -> Result<(), Diagnostic> {
        self.check_need(need)?;
        bound(
            0,
            layout.roots.len(),
            self.graph_limits.layout_roots,
            crate::LimitKind::GraphLayouts,
        )?;
        if need.kind() != ResourceNeedKind::Layout {
            return Err(Diagnostic::new(Code::ConflictingObjectContext));
        }
        if !layout.roots.iter().any(|r| {
            r.matches_interpretation(&need.fact.interpretation)
                && match &need.fact.fragment {
                    ResourceFragment::Root => r.pointer().is_empty(),
                    ResourceFragment::Pointer(p) => {
                        if r.schema_profile().is_some() {
                            p == r.pointer()
                                || r.pointer().is_empty()
                                || p.strip_prefix(r.pointer())
                                    .is_some_and(|s| s.starts_with('/'))
                        } else {
                            p == r.pointer()
                        }
                    }
                    ResourceFragment::Anchor(_) => r.schema_profile().is_some(),
                }
        }) {
            return Err(Diagnostic::new(Code::ConflictingObjectContext));
        }
        self.enrich_layout(need.fact.target.expect("layout target"), layout)
    }
    /// Admit original bytes. Identical effective identity and bytes are idempotent;
    /// layout enrichment is explicit and never silently inferred by re-insertion.
    pub fn add(&mut self, input: ResourceInput) -> Result<ResourceId, ParseFailure> {
        self.add_with_cancellation(input, &crate::Cancellation::default())
    }
    /// Admit bytes with cooperative parser checkpoints.
    pub fn add_with_cancellation(
        &mut self,
        input: ResourceInput,
        cancellation: &crate::Cancellation,
    ) -> Result<ResourceId, ParseFailure> {
        let fail = |diagnostic| ParseFailure {
            diagnostic,
            source: input.bytes.clone(),
        };
        cancellation.check().map_err(fail)?;
        let input_usage =
            metadata_input(&input, &self.limits, &self.graph_limits, cancellation).map_err(fail)?;
        for uri in input.retrieval.iter().chain(&input.aliases) {
            cancellation.check().map_err(fail)?;
            if uri.len() > self.limits.target_bytes {
                return Err(fail(Diagnostic::limited(
                    crate::LimitKind::TargetBytes,
                    self.limits.target_bytes,
                    uri.len(),
                )));
            }
        }
        if let Some(uri) = &input.retrieval {
            let key = identity_key(uri).map_err(fail)?;
            if let Some(id) = self.admission.aliases.get(&key) {
                let index = self.admission.indices[id];
                let resource = &self.resources[index];
                if resource
                    .retrieval
                    .as_ref()
                    .is_some_and(|s| identity_key(s).is_ok_and(|s| s == key))
                {
                    if resource.json.original_source().as_bytes() != input.bytes.as_ref() {
                        return Err(fail(Diagnostic::identity_collision(&resource.json.root())));
                    }
                    if resource.layout != input.layout && !input.layout.roots.is_empty() {
                        return Err(fail(Diagnostic::new(Code::ConflictingObjectContext)));
                    }
                    let id = resource.id;
                    let mut known = HashSet::new();
                    let mut pending = Vec::with_capacity(input.aliases.len());
                    for alias in &input.aliases {
                        cancellation.check().map_err(fail)?;
                        if !resource.aliases.contains(alias) && known.insert(alias) {
                            pending.push(alias);
                        }
                    }
                    let extra = MetadataUse {
                        identities: pending.len(),
                        layouts: 0,
                        bytes: pending.iter().map(|s| s.len()).sum(),
                    };
                    self.metadata_total()
                        .plus(extra)
                        .check(&self.graph_limits)
                        .map_err(fail)?;
                    let identities = stage_aliases(
                        id,
                        &resource.json.root(),
                        pending.iter().map(|s| s.as_str()),
                        &self.admission.aliases,
                        |id| self.resources[self.admission.indices[&id]].json.root(),
                        cancellation,
                    )
                    .map_err(fail)?;
                    cancellation.check().map_err(fail)?;
                    // All fallible checks precede the commit. A resource shared with
                    // a published snapshot is copied only when it actually changes.
                    if !pending.is_empty() {
                        let updated = Arc::make_mut(&mut self.resources[index]);
                        for alias in pending {
                            reserve_vec(&mut updated.aliases, self.graph_limits.identities);
                            updated.aliases.push(alias.clone());
                        }
                        self.admission.aliases.extend(identities);
                        self.admission.metadata = self.admission.metadata.plus(extra);
                    }
                    return Ok(id);
                }
            }
        }
        self.metadata_total()
            .plus(input_usage)
            .check(&self.graph_limits)
            .map_err(fail)?;
        let (bytes, nodes, _) = self.totals();
        bound(
            self.resources.len(),
            1,
            self.graph_limits.resources,
            crate::LimitKind::GraphResources,
        )
        .map_err(fail)?;
        bound(
            bytes,
            input.bytes.len(),
            self.graph_limits.original_bytes,
            crate::LimitKind::GraphOriginalBytes,
        )
        .map_err(fail)?;
        let mut limits = self.limits.clone();
        limits.nodes = limits
            .nodes
            .min(self.graph_limits.nodes.saturating_sub(nodes));
        let json = crate::loading::parse(&input.bytes, limits, cancellation)?;
        let source = input.bytes.clone();
        self.admit_parsed(json, input, cancellation)
            .map_err(|diagnostic| ParseFailure { diagnostic, source })
    }
    // Shared by borrowed Document::parse and caller-owned ResourceInput admission.
    // The convenience parse path does not allocate another full-document byte Arc.
    pub(crate) fn admit_parsed(
        &mut self,
        json: ExactJson,
        input: ResourceInput,
        cancellation: &crate::Cancellation,
    ) -> Result<ResourceId, Diagnostic> {
        cancellation.check()?;
        let usage = metadata_input(&input, &self.limits, &self.graph_limits, cancellation)?;
        let (bytes, nodes, retained) = self.totals();
        bound(
            self.resources.len(),
            1,
            self.graph_limits.resources,
            crate::LimitKind::GraphResources,
        )?;
        bound(
            bytes,
            json.original_source().len(),
            self.graph_limits.original_bytes,
            crate::LimitKind::GraphOriginalBytes,
        )?;
        bound(
            nodes,
            json.node_count(),
            self.graph_limits.nodes,
            crate::LimitKind::GraphNodes,
        )?;
        bound(
            retained,
            json.retained_bytes(),
            self.graph_limits.retained_bytes,
            crate::LimitKind::GraphRetainedBytes,
        )?;
        let id = ResourceId(json.root().source_context().id);
        let canonical = if Edition::detect_optional(&json.root()) == Ok(Some(Edition::OpenApi32)) {
            json.root()
                .get("$self")
                .map(|v| {
                    let reference = v
                        .as_str()
                        .ok_or_else(|| v.error(Code::InvalidResourceUri))?;
                    bound(
                        0,
                        reference.len(),
                        self.limits.target_bytes,
                        crate::LimitKind::TargetBytes,
                    )
                    .map_err(|e| v.locate(e))?;
                    let resolved = resolve_uri(reference, input.retrieval.as_deref())
                        .map_err(|e| v.locate(e))?;
                    bound(
                        0,
                        resolved.len(),
                        self.limits.target_bytes,
                        crate::LimitKind::TargetBytes,
                    )
                    .map_err(|e| v.locate(e))?;
                    Ok::<_, Diagnostic>(resolved)
                })
                .transpose()?
        } else {
            None
        };
        let base = canonical
            .as_ref()
            .or(input.retrieval.as_ref())
            .map(|s| without_fragment(s));
        let extra = MetadataUse {
            identities: usize::from(canonical.is_some()) * 2,
            layouts: 0,
            bytes: input
                .retrieval
                .as_ref()
                .map_or(0, String::len)
                .saturating_add(base.map_or(0, str::len))
                .saturating_add(canonical.as_ref().map_or(0, String::len)),
        };
        self.metadata_total()
            .plus(usage)
            .plus(extra)
            .check(&self.graph_limits)?;
        let base = base.map(str::to_owned);
        let mut aliases =
            Vec::with_capacity(input.aliases.len() + usize::from(input.retrieval.is_some()));
        aliases.extend(
            input
                .aliases
                .into_iter()
                .map(|s| s.into_boxed_str().into_string()),
        );
        if let Some(uri) = &input.retrieval {
            aliases.push(uri.clone());
        }
        let mut roots = Vec::with_capacity(input.layout.roots.len());
        roots.extend(input.layout.roots.into_iter().map(LayoutRoot::compact));
        let resource = Resource {
            id,
            json,
            retrieval: input.retrieval,
            aliases,
            base,
            canonical,
            layout: ResourceLayout { roots },
        };
        validate_layout(&resource, cancellation)?;
        let identities = stage_aliases(
            id,
            &resource.json.root(),
            resource
                .aliases
                .iter()
                .map(String::as_str)
                .chain(resource.canonical.as_deref())
                .chain(resource.canonical.as_deref().map(without_fragment)),
            &self.admission.aliases,
            |id| self.resources[self.admission.indices[&id]].json.root(),
            cancellation,
        )?;
        cancellation.check()?;
        // Commit only the incoming resource and its validated identity delta.
        // Rebuilding every earlier URI index here made graph admission quadratic.
        self.admission.original_bytes = bytes.saturating_add(resource.json.original_source().len());
        self.admission.nodes = nodes.saturating_add(resource.json.node_count());
        self.admission.retained_bytes = retained.saturating_add(resource.json.retained_bytes());
        self.admission.metadata = self.admission.metadata.plus(resource.metadata());
        self.admission.aliases.extend(identities);
        self.admission.indices.insert(id, self.resources.len());
        reserve_vec(&mut self.resources, self.graph_limits.resources);
        self.resources.push(Arc::new(resource));
        Ok(id)
    }
    /// Add compatible interpretation roots to identical already-admitted bytes.
    /// The source arena/ResourceId is retained; a later build reindexes a new snapshot.
    pub fn enrich_layout(
        &mut self,
        id: ResourceId,
        layout: ResourceLayout,
    ) -> Result<(), Diagnostic> {
        self.enrich_layout_with_cancellation(id, layout, &crate::Cancellation::default())
    }
    /// Enrich interpretation with cancellation checkpoints and aggregate metadata admission.
    pub fn enrich_layout_with_cancellation(
        &mut self,
        id: ResourceId,
        layout: ResourceLayout,
        cancellation: &crate::Cancellation,
    ) -> Result<(), Diagnostic> {
        cancellation.check()?;
        bound(
            0,
            layout.roots.len(),
            self.graph_limits.layout_roots,
            crate::LimitKind::GraphLayouts,
        )?;
        let mut incoming_bytes = 0usize;
        for root in &layout.roots {
            bound(
                0,
                root.profile_bytes(),
                self.limits.target_bytes,
                crate::LimitKind::TargetBytes,
            )?;
            cancellation.check()?;
            bound(
                0,
                root.pointer().len(),
                self.limits.target_bytes,
                crate::LimitKind::TargetBytes,
            )?;
            incoming_bytes = incoming_bytes.saturating_add(root.metadata_bytes());
            bound(
                0,
                incoming_bytes,
                self.graph_limits.metadata_bytes,
                crate::LimitKind::GraphMetadataBytes,
            )?;
        }
        let index = self
            .admission
            .indices
            .get(&id)
            .copied()
            .ok_or_else(|| Diagnostic::new(Code::MissingReference))?;
        let mut known: HashMap<&str, &LayoutRoot> = self.resources[index]
            .layout
            .roots
            .iter()
            .map(|r| (r.pointer(), r))
            .collect();
        let mut extra = MetadataUse::default();
        for root in &layout.roots {
            cancellation.check()?;
            if let Some(previous) = known.insert(root.pointer(), root) {
                if previous != root {
                    return Err(Diagnostic::new(Code::ConflictingObjectContext));
                }
            } else {
                extra.layouts += 1;
                extra.bytes = extra.bytes.saturating_add(root.metadata_bytes());
            }
        }
        self.metadata_total()
            .plus(extra)
            .check(&self.graph_limits)?;
        drop(known);
        let mut updated = (*self.resources[index]).clone();
        let mut known: HashSet<String> = updated
            .layout
            .roots
            .iter()
            .map(|r| r.pointer().to_owned())
            .collect();
        updated.layout.roots.reserve_exact(extra.layouts);
        for root in layout.roots {
            cancellation.check()?;
            if known.insert(root.pointer().to_owned()) {
                updated.layout.roots.push(root.compact());
            }
        }
        validate_layout(&updated, cancellation)?;
        cancellation.check()?;
        self.resources[index] = Arc::new(updated);
        self.admission.metadata = self.admission.metadata.plus(extra);
        Ok(())
    }
    /// Publish a closed graph with the selected entry OpenAPI source.
    pub fn build(self, entry: ResourceId) -> Result<crate::Document, Diagnostic> {
        self.build_with_cancellation(entry, &crate::Cancellation::default())
    }
    /// Publish with cooperative indexing checkpoints; there is no remote work to cancel.
    pub fn build_with_cancellation(
        self,
        entry: ResourceId,
        cancellation: &crate::Cancellation,
    ) -> Result<crate::Document, Diagnostic> {
        let mut graph = Graph::new(
            self.resources,
            self.admission,
            entry,
            self.graph_limits,
            self.limits.target_bytes,
            cancellation,
        )?;
        crate::schema::SchemaIndex::build(&mut graph, &self.limits, cancellation)?;
        crate::Document::from_graph(graph, entry, self.limits, cancellation)
    }
    fn totals(&self) -> (usize, usize, usize) {
        (
            self.admission.original_bytes,
            self.admission.nodes,
            self.admission.retained_bytes,
        )
    }
    fn metadata_total(&self) -> MetadataUse {
        self.admission.metadata
    }
}

// Bounded, owner-free admission indexes and exact retained-source counters.
// Cloned once when forking a snapshot; moved without rebuilding on publication.
#[derive(Clone, Default)]
struct Admission {
    aliases: HashMap<String, ResourceId>,
    indices: HashMap<ResourceId, usize>,
    original_bytes: usize,
    nodes: usize,
    retained_bytes: usize,
    metadata: MetadataUse,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
struct MetadataUse {
    identities: usize,
    layouts: usize,
    bytes: usize,
}
impl MetadataUse {
    fn plus(self, other: Self) -> Self {
        Self {
            identities: self.identities.saturating_add(other.identities),
            layouts: self.layouts.saturating_add(other.layouts),
            bytes: self.bytes.saturating_add(other.bytes),
        }
    }
    fn check(self, limits: &GraphLimits) -> Result<(), Diagnostic> {
        bound(
            0,
            self.identities,
            limits.identities,
            crate::LimitKind::GraphIdentities,
        )?;
        bound(
            0,
            self.layouts,
            limits.layout_roots,
            crate::LimitKind::GraphLayouts,
        )?;
        bound(
            0,
            self.bytes,
            limits.metadata_bytes,
            crate::LimitKind::GraphMetadataBytes,
        )
    }
}
impl Resource {
    fn metadata(&self) -> MetadataUse {
        MetadataUse {
            identities: self
                .aliases
                .len()
                .saturating_add(usize::from(self.canonical.is_some()) * 2),
            layouts: self.layout.roots.len(),
            bytes: self
                .aliases
                .iter()
                .chain(self.retrieval.iter())
                .chain(self.base.iter())
                .chain(self.canonical.iter())
                .map(String::len)
                .chain(self.layout.roots.iter().map(LayoutRoot::metadata_bytes))
                .fold(0usize, usize::saturating_add),
        }
    }
}
fn metadata_input(
    input: &ResourceInput,
    limits: &Limits,
    graph: &GraphLimits,
    cancellation: &crate::Cancellation,
) -> Result<MetadataUse, Diagnostic> {
    let mut usage = MetadataUse {
        identities: input
            .aliases
            .len()
            .saturating_add(usize::from(input.retrieval.is_some())),
        layouts: input.layout.roots.len(),
        bytes: input
            .layout
            .roots
            .iter()
            .map(LayoutRoot::profile_bytes)
            .fold(0usize, usize::saturating_add),
    };
    usage.check(graph)?;
    for root in &input.layout.roots {
        cancellation.check()?;
        bound(
            0,
            root.profile_bytes(),
            limits.target_bytes,
            crate::LimitKind::TargetBytes,
        )?;
    }
    for value in input
        .aliases
        .iter()
        .chain(input.retrieval.iter())
        .map(String::as_str)
        .chain(input.layout.roots.iter().map(LayoutRoot::pointer))
    {
        cancellation.check()?;
        bound(
            0,
            value.len(),
            limits.target_bytes,
            crate::LimitKind::TargetBytes,
        )?;
        usage.bytes = usage.bytes.saturating_add(value.len());
        usage.check(graph)?;
    }
    Ok(usage)
}
fn reserve_vec<T>(values: &mut Vec<T>, maximum: usize) {
    if values.len() == values.capacity() {
        let capacity = values.capacity().saturating_mul(2).max(4).min(maximum);
        values.reserve_exact(capacity.saturating_sub(values.len()));
    }
}

fn validate_layout(
    resource: &Resource,
    cancellation: &crate::Cancellation,
) -> Result<(), Diagnostic> {
    let mut roots = HashMap::new();
    for root in &resource.layout.roots {
        cancellation.check()?;
        let value = resource.json.root().pointer(root.pointer())?;
        if let Some(previous) = roots.insert(root.pointer(), root)
            && previous != root
        {
            return Err(value.error(Code::ConflictingObjectContext));
        }
    }
    Ok(())
}
pub(crate) fn bound(
    used: usize,
    more: usize,
    max: usize,
    kind: crate::LimitKind,
) -> Result<(), Diagnostic> {
    let actual = used.saturating_add(more);
    if actual > max {
        Err(Diagnostic::limited(kind, max, actual))
    } else {
        Ok(())
    }
}
fn absolute_uri(uri: &str, fragment: bool) -> Result<(), Diagnostic> {
    let parsed =
        fluent_uri::UriRef::parse(uri).map_err(|_| Diagnostic::new(Code::InvalidResourceUri))?;
    if parsed.scheme().is_none()
        || (!fragment && parsed.fragment().is_some())
        || parsed.authority().is_some_and(|a| a.userinfo().is_some())
    {
        return Err(Diagnostic::new(Code::InvalidResourceUri));
    }
    Ok(())
}
pub(crate) fn without_fragment(uri: &str) -> &str {
    uri.split_once('#').map_or(uri, |(s, _)| s)
}
pub(crate) fn resolve_uri(reference: &str, base: Option<&str>) -> Result<String, Diagnostic> {
    let parsed = fluent_uri::UriRef::parse(reference)
        .map_err(|_| Diagnostic::new(Code::InvalidResourceUri))?;
    if parsed.authority().is_some_and(|a| a.userinfo().is_some()) {
        return Err(Diagnostic::new(Code::InvalidResourceUri));
    }
    let base = if parsed.scheme().is_some() {
        reference
    } else {
        base.ok_or_else(|| Diagnostic::new(Code::MissingResourceBase))?
    };
    crate::uri::resolve(reference, without_fragment(base))
        .map_err(|_| Diagnostic::new(Code::InvalidResourceUri))
}
pub(crate) fn identity_key(uri: &str) -> Result<String, Diagnostic> {
    absolute_uri(uri, true)?;
    let parsed = fluent_uri::UriRef::parse(uri).expect("checked URI");
    let mut key = format!(
        "{}:",
        parsed
            .scheme()
            .expect("absolute")
            .as_str()
            .to_ascii_lowercase()
    );
    if let Some(authority) = parsed.authority() {
        key.push_str("//");
        key.push_str(&authority.as_str().to_ascii_lowercase());
    }
    key.push_str(parsed.path().as_str());
    if let Some(q) = parsed.query() {
        key.push('?');
        key.push_str(q.as_str());
    }
    if let Some(f) = parsed.fragment() {
        key.push('#');
        key.push_str(f.as_str());
    }
    let mut bytes = key.into_bytes();
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' {
            bytes[i + 1].make_ascii_uppercase();
            bytes[i + 2].make_ascii_uppercase();
            i += 3;
        } else {
            i += 1;
        }
    }
    Ok(String::from_utf8(bytes).expect("URI is UTF-8"))
}
fn stage_aliases<'a>(
    id: ResourceId,
    source: &Value,
    aliases: impl Iterator<Item = &'a str>,
    existing: &HashMap<String, ResourceId>,
    previous_source: impl Fn(ResourceId) -> Value,
    cancellation: &crate::Cancellation,
) -> Result<HashMap<String, ResourceId>, Diagnostic> {
    let mut pending = HashMap::new();
    for alias in aliases {
        cancellation.check()?;
        let key = identity_key(alias)?;
        match existing.get(&key) {
            Some(previous) if *previous != id => {
                return Err(
                    source.locate(Diagnostic::identity_collision(&previous_source(*previous)))
                );
            }
            Some(_) => {}
            None => {
                pending.insert(key, id);
            }
        }
    }
    Ok(pending)
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum ObjectKind {
    Document,
    Operation,
    Reference(ReferenceKind),
    Media,
    LegacyHeader,
    Encoding,
    SecurityRequirement,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Context {
    pub kind: ObjectKind,
    pub edition: Edition,
}
pub(crate) type NodeKey = (usize, usize);
pub(crate) fn key(value: &Value) -> NodeKey {
    (value.source_context().id, value.identity())
}
pub(crate) struct Graph {
    pub resources: Vec<Arc<Resource>>,
    pub limits: GraphLimits,
    admission: Admission,
    pub(crate) contexts: HashMap<NodeKey, Context>,
    pub(crate) entry: ResourceId,
    pub(crate) schemas: crate::schema::SchemaIndex,
    pub(crate) reference_count: usize,
    pub(crate) target_bytes: usize,
}
impl Graph {
    fn new(
        resources: Vec<Arc<Resource>>,
        admission: Admission,
        entry: ResourceId,
        limits: GraphLimits,
        target_bytes: usize,
        cancellation: &crate::Cancellation,
    ) -> Result<Self, Diagnostic> {
        let mut graph = Self {
            resources,
            entry,
            limits,
            admission,
            contexts: HashMap::new(),
            schemas: crate::schema::SchemaIndex::default(),
            reference_count: 0,
            target_bytes,
        };
        if graph.resource(entry).is_none() {
            return Err(Diagnostic::new(Code::MissingReference));
        }
        let mut queue = VecDeque::new();
        for resource in &graph.resources {
            cancellation.check()?;
            let root = resource.json.root();
            if let Ok(Some(edition)) = Edition::detect_optional(&root) {
                enqueue(
                    &mut queue,
                    resource.json.root(),
                    ObjectKind::Document,
                    edition,
                    graph.limits.nodes,
                )?;
            }
            for root in &resource.layout.roots {
                cancellation.check()?;
                if let LayoutRoot::Protocol {
                    pointer,
                    kind,
                    edition,
                } = root
                {
                    enqueue(
                        &mut queue,
                        resource.json.root().pointer(pointer)?,
                        ObjectKind::Reference(*kind),
                        *edition,
                        graph.limits.nodes,
                    )?;
                }
            }
        }
        let mut edges = 0usize;
        while let Some((node, context)) = queue.pop_front() {
            cancellation.check()?;
            if let Some(previous) = graph.contexts.insert(key(&node), context) {
                if previous != context {
                    return Err(node.error(Code::ConflictingObjectContext));
                }
                continue;
            }
            if let ObjectKind::Reference(kind) = context.kind
                && let Some(reference) = node.get("$ref")
            {
                bound(
                    edges,
                    1,
                    graph.limits.reference_edges,
                    crate::LimitKind::GraphReferenceEdges,
                )
                .map_err(|e| node.locate(e))?;
                edges += 1;
                if let Some(text) = reference.as_str()
                    && let Ok((target, _)) = graph.target_address(&reference, text)
                {
                    let resource = graph.owner(&target)?;
                    // Only a selected fragment root can infer its interpretation.
                    // A non-root pointer into an untyped bundle needs a layout.
                    if target.identity() == 0
                        && resource.json.root().get("openapi").is_none()
                        && resource.json.root().get("swagger").is_none()
                    {
                        enqueue(
                            &mut queue,
                            resource.json.root(),
                            ObjectKind::Reference(kind),
                            context.edition,
                            graph.limits.nodes,
                        )?;
                    }
                }
            }
            graph.children(&node, context, &mut queue, &mut edges, cancellation)?;
        }
        graph.reference_count = edges;
        Ok(graph)
    }
    pub(crate) fn resource(&self, id: ResourceId) -> Option<&Resource> {
        self.admission
            .indices
            .get(&id)
            .map(|index| self.resources[*index].as_ref())
    }
    pub(crate) fn owner(&self, value: &Value) -> Result<&Resource, Diagnostic> {
        self.resource(ResourceId(value.source_context().id))
            .ok_or_else(|| value.error(Code::SnapshotMismatch))
    }
    pub(crate) fn resource_root(&self, value: &Value) -> Result<Value, Diagnostic> {
        Ok(self.owner(value)?.json.root())
    }
    pub(crate) fn retrieval_base(&self, value: &Value) -> Option<&str> {
        self.owner(value).ok()?.retrieval.as_deref()
    }
    pub(crate) fn context(&self, node: &Value, kind: ReferenceKind) -> Result<Edition, Diagnostic> {
        match self.contexts.get(&key(node)) {
            Some(context) if context.kind == ObjectKind::Reference(kind) => Ok(context.edition),
            _ => Err(node.error(Code::WrongReferenceKind)),
        }
    }
    fn children(
        &self,
        node: &Value,
        context: Context,
        queue: &mut VecDeque<(Value, Context)>,
        edges: &mut usize,
        cancellation: &crate::Cancellation,
    ) -> Result<(), Diagnostic> {
        let edition = context.edition;
        let cap = self.limits.nodes;
        let mut child = |value, kind| {
            cancellation.check()?;
            enqueue(queue, value, kind, edition, cap)
        };
        let refs = |kind| ObjectKind::Reference(kind);
        if node.get("$ref").is_some()
            && matches!(context.kind, ObjectKind::Reference(kind) if kind != ReferenceKind::PathItem)
        {
            return Ok(());
        }
        match context.kind {
            ObjectKind::Document => {
                if let Some(security) = node.get("security")
                    && let Some(values) = security.elements()
                {
                    for value in values {
                        child(value, ObjectKind::SecurityRequirement)?;
                    }
                }
                for name in ["paths", "webhooks"] {
                    if name == "webhooks"
                        && matches!(edition, Edition::Swagger20 | Edition::OpenApi30)
                    {
                        continue;
                    }
                    if let Some(map) = node.get(name)
                        && let Some(members) = map.members()
                    {
                        for (key, value) in members {
                            if !key.starts_with("x-") {
                                child(value, refs(ReferenceKind::PathItem))?;
                            }
                        }
                    }
                }
                let map = if edition == Edition::Swagger20 {
                    Some(node.clone())
                } else {
                    node.get("components")
                };
                if let Some(map) = map {
                    for (name, kind) in [
                        ("parameters", ReferenceKind::Parameter),
                        ("responses", ReferenceKind::Response),
                        ("requestBodies", ReferenceKind::RequestBody),
                        ("headers", ReferenceKind::Header),
                        ("securitySchemes", ReferenceKind::SecurityScheme),
                        ("securityDefinitions", ReferenceKind::SecurityScheme),
                        ("examples", ReferenceKind::Example),
                        ("links", ReferenceKind::Link),
                        ("callbacks", ReferenceKind::Callback),
                        ("pathItems", ReferenceKind::PathItem),
                        ("mediaTypes", ReferenceKind::MediaType),
                    ] {
                        if edition == Edition::Swagger20
                            && !matches!(name, "parameters" | "responses" | "securityDefinitions")
                        {
                            continue;
                        }
                        if edition != Edition::Swagger20 && name == "securityDefinitions" {
                            continue;
                        }
                        if name == "mediaTypes" && edition != Edition::OpenApi32 {
                            continue;
                        }
                        if name == "pathItems" && edition == Edition::OpenApi30 {
                            continue;
                        }
                        if let Some(map) = map.get(name)
                            && let Some(members) = map.members()
                        {
                            for (_, value) in members {
                                child(value, refs(kind))?;
                            }
                        }
                    }
                }
            }
            ObjectKind::Reference(ReferenceKind::PathItem) => {
                if let Some(members) = node.members() {
                    for (name, value) in members {
                        if crate::Method::parse(name)
                            .is_some_and(|_| name == name.to_ascii_lowercase())
                        {
                            child(value, ObjectKind::Operation)?;
                        } else if name == "additionalOperations"
                            && edition == Edition::OpenApi32
                            && let Some(members) = value.members()
                        {
                            for (_, value) in members {
                                child(value, ObjectKind::Operation)?;
                            }
                        }
                    }
                }
                if let Some(parameters) = node.get("parameters")
                    && let Some(values) = parameters.elements()
                {
                    for value in values {
                        child(value, refs(ReferenceKind::Parameter))?;
                    }
                }
            }
            ObjectKind::Operation => {
                if let Some(security) = node.get("security")
                    && let Some(values) = security.elements()
                {
                    for value in values {
                        child(value, ObjectKind::SecurityRequirement)?;
                    }
                }
                if let Some(parameters) = node.get("parameters")
                    && let Some(values) = parameters.elements()
                {
                    for value in values {
                        child(value, refs(ReferenceKind::Parameter))?;
                    }
                }
                if let Some(responses) = node.get("responses")
                    && let Some(values) = responses.members()
                {
                    for (key, value) in values {
                        if !key.starts_with("x-") {
                            child(value, refs(ReferenceKind::Response))?;
                        }
                    }
                }
                if edition != Edition::Swagger20 {
                    if let Some(body) = node.get("requestBody") {
                        child(body, refs(ReferenceKind::RequestBody))?;
                    }
                    if let Some(callbacks) = node.get("callbacks")
                        && let Some(values) = callbacks.members()
                    {
                        for (_, value) in values {
                            child(value, refs(ReferenceKind::Callback))?;
                        }
                    }
                }
            }
            ObjectKind::Reference(ReferenceKind::Callback) => {
                if let Some(values) = node.members() {
                    for (name, value) in values {
                        if !name.starts_with("x-") {
                            child(value, refs(ReferenceKind::PathItem))?;
                        }
                    }
                }
            }
            ObjectKind::Reference(ReferenceKind::Response) => {
                if let Some(headers) = node.get("headers")
                    && let Some(values) = headers.members()
                {
                    for (_, value) in values {
                        child(
                            value,
                            if edition == Edition::Swagger20 {
                                ObjectKind::LegacyHeader
                            } else {
                                refs(ReferenceKind::Header)
                            },
                        )?;
                    }
                }
                if edition != Edition::Swagger20 {
                    if let Some(links) = node.get("links")
                        && let Some(values) = links.members()
                    {
                        for (_, value) in values {
                            child(value, refs(ReferenceKind::Link))?;
                        }
                    }
                    enqueue_content(node, queue, edition, cap, cancellation)?;
                }
            }
            ObjectKind::Reference(ReferenceKind::Parameter | ReferenceKind::Header) => {
                if edition != Edition::Swagger20 {
                    if let Some(examples) = node.get("examples")
                        && let Some(values) = examples.members()
                    {
                        for (_, value) in values {
                            child(value, refs(ReferenceKind::Example))?;
                        }
                    }
                    enqueue_content(node, queue, edition, cap, cancellation)?;
                }
            }
            ObjectKind::Reference(ReferenceKind::RequestBody) => {
                if edition != Edition::Swagger20 {
                    enqueue_content(node, queue, edition, cap, cancellation)?;
                }
            }
            ObjectKind::Media | ObjectKind::Reference(ReferenceKind::MediaType) => {
                if let Some(examples) = node.get("examples")
                    && let Some(values) = examples.members()
                {
                    for (_, value) in values {
                        child(value, refs(ReferenceKind::Example))?;
                    }
                }
                enqueue_encodings(node, queue, edition, cap, cancellation)?;
            }
            ObjectKind::Encoding => {
                if let Some(headers) = node.get("headers")
                    && let Some(values) = headers.members()
                {
                    for (_, value) in values {
                        child(value, refs(ReferenceKind::Header))?;
                    }
                }
                if edition == Edition::OpenApi32 {
                    enqueue_encodings(node, queue, edition, cap, cancellation)?;
                }
            }
            ObjectKind::SecurityRequirement if edition == Edition::OpenApi32 => {
                // URI-key targets can infer a protocol fragment root just like `$ref`.
                // Entry-root exact-name precedence is applied when the operation uses it.
                if let Some(values) = node.members() {
                    for (name, source) in values {
                        cancellation.check()?;
                        if self
                            .resource(self.entry)
                            .and_then(|r| r.json.root().get("components"))
                            .and_then(|v| v.get("securitySchemes"))
                            .and_then(|v| v.get(name))
                            .is_some()
                        {
                            continue;
                        }
                        bound(
                            *edges,
                            1,
                            self.limits.reference_edges,
                            crate::LimitKind::GraphReferenceEdges,
                        )
                        .map_err(|e| source.locate(e))?;
                        *edges += 1;
                        if let Ok((target, _)) = self.target_address(&source, name) {
                            let resource = self.owner(&target)?;
                            if target.identity() == 0
                                && resource.json.root().get("openapi").is_none()
                                && resource.json.root().get("swagger").is_none()
                            {
                                child(resource.json.root(), refs(ReferenceKind::SecurityScheme))?;
                            }
                        }
                    }
                }
            }
            _ => {}
        }
        Ok(())
    }
    pub(crate) fn value(&self, key: NodeKey) -> Value {
        self.resource(ResourceId(key.0))
            .expect("indexed resource")
            .json
            .root()
            .at_identity(key.1)
            .expect("indexed value")
    }
    pub(crate) fn physical_identity(&self, uri: &str) -> Result<Option<NodeKey>, Diagnostic> {
        Ok(self
            .admission
            .aliases
            .get(&identity_key(uri)?)
            .map(|id| (id.0, 0)))
    }
    fn indexed_identity(&self, normalized: &str) -> Option<NodeKey> {
        self.schemas
            .indexed_identity(normalized)
            .or_else(|| self.admission.aliases.get(normalized).map(|id| (id.0, 0)))
    }
    pub(crate) fn metadata_usage(&self) -> (usize, usize) {
        (
            self.admission.metadata.identities,
            self.admission.metadata.bytes,
        )
    }
    // Common address layer for protocol and schema references. Interpretation is
    // checked by the selected typed path, never guessed from a pointer's shape.
    pub(crate) fn address(
        &self,
        source: &Value,
        reference: &str,
        base: Option<&str>,
        local_root: NodeKey,
    ) -> Result<Address, Diagnostic> {
        bound(
            0,
            reference.len(),
            self.target_bytes,
            crate::LimitKind::TargetBytes,
        )
        .map_err(|e| source.locate(e))?;
        fluent_uri::UriRef::parse(reference).map_err(|_| source.error(Code::InvalidReference))?;
        if base.is_none() && (reference.is_empty() || reference.starts_with('#')) {
            return Ok(Address {
                root: Some(local_root),
                exact: None,
                uri: None,
                fragment: decode_fragment(
                    reference.strip_prefix('#').unwrap_or(reference),
                    source,
                )?,
            });
        }
        let uri = resolve_uri(reference, base).map_err(|e| source.locate(e))?;
        let fragment = decode_fragment(uri.split_once('#').map_or("", |(_, f)| f), source)?;
        let exact = self.indexed_identity(&identity_key(&uri)?);
        let root_uri = without_fragment(&uri);
        let root = if root_uri == uri {
            exact
        } else {
            self.indexed_identity(&identity_key(root_uri)?)
        };
        Ok(Address {
            root,
            exact,
            fragment,
            uri: Some(uri),
        })
    }
    fn target_address(&self, source: &Value, reference: &str) -> Result<(Value, bool), Diagnostic> {
        let owner = self.owner(source)?;
        let address = self.address(source, reference, owner.base.as_deref(), (owner.id.0, 0))?;
        let target = if let Some(exact) = address.exact {
            self.value(exact)
        } else {
            let root = address
                .root
                .ok_or_else(|| source.error(Code::ResourceUnavailable))?;
            let pointer = address
                .fragment
                .as_pointer()
                .ok_or_else(|| source.error(Code::InvalidReference))?;
            self.value(root)
                .pointer(pointer)
                .map_err(|e| source.locate(e))?
        };
        let resource = self.owner(&target)?;
        let canonical = resource.canonical.as_ref().is_none_or(|canonical| {
            address.uri.as_ref().is_some_and(|uri| {
                identity_key(without_fragment(canonical)).ok()
                    == identity_key(without_fragment(uri)).ok()
            })
        });
        Ok((target, canonical))
    }
    pub(crate) fn target(
        &self,
        source: &Value,
        reference: &str,
        kind: ReferenceKind,
        edition: Edition,
    ) -> Result<(Value, bool), Diagnostic> {
        let owner = self.owner(source)?;
        let (value, canonical) = match self.target_address(source, reference) {
            Ok(value) => value,
            Err(error) if error.code() == Code::ResourceUnavailable => {
                let requested =
                    resolve_uri(reference, owner.base.as_deref()).map_err(|e| source.locate(e))?;
                let pointer = requested
                    .split_once('#')
                    .map_or(Ok(String::new()), |(_, p)| crate::uri::percent_decode(p))?;
                return Err(error.need_fact(NeedFact {
                    source: owner.id,
                    pointer: source.location().pointer,
                    requested_uri: Some(without_fragment(&requested).to_owned()),
                    fragment: ResourceFragment::pointer(pointer),
                    target: None,
                    interpretation: ResourceInterpretation::Protocol { kind, edition },
                    need: ResourceNeedKind::Bytes,
                }));
            }
            Err(error) => return Err(error),
        };
        let target = self.owner(&value)?;
        if !self.contexts.contains_key(&key(&value))
            && target.json.root().get("openapi").is_none()
            && target.json.root().get("swagger").is_none()
        {
            return Err(source
                .error(Code::ResourceLayoutRequired)
                .need_fact(NeedFact {
                    source: owner.id,
                    pointer: source.location().pointer,
                    requested_uri: target.retrieval.clone().or(target.base.clone()),
                    fragment: ResourceFragment::pointer(value.location().pointer),
                    target: Some(target.id),
                    interpretation: ResourceInterpretation::Protocol { kind, edition },
                    need: ResourceNeedKind::Layout,
                }));
        }
        let target_edition = self.context(&value, kind)?;
        if target_edition != edition {
            return Err(source.error(Code::ReferencedEditionMismatch));
        }
        Ok((value, canonical))
    }
    pub(crate) fn resolve(
        &self,
        node: &Value,
        kind: ReferenceKind,
        limit: usize,
    ) -> Result<crate::ResolvedReference, Diagnostic> {
        let edition = self.context(node, kind)?;
        let mut current = node.clone();
        let mut seen = HashSet::new();
        let (mut summary, mut description) = (None, None);
        let mut chain = vec![];
        let mut path_fields = HashSet::new();
        let mut canonical = true;
        loop {
            if current.kind() != ValueKind::Object {
                return Err(current.error(Code::WrongReferenceKind));
            }
            if !seen.insert(key(&current)) {
                return Err(current.error(Code::ReferenceCycle));
            }
            if kind == ReferenceKind::PathItem {
                for (name, _) in current.members().expect("object") {
                    if name != "$ref"
                        && !name.starts_with("x-")
                        && !path_fields.insert(name.to_owned())
                    {
                        return Err(current.error(Code::AmbiguousReference));
                    }
                }
            }
            reserve_vec(&mut chain, limit.saturating_add(1));
            chain.push(current.clone());
            let Some(reference) = current.get("$ref") else {
                break;
            };
            if edition == Edition::Swagger20
                && !matches!(
                    kind,
                    ReferenceKind::PathItem | ReferenceKind::Parameter | ReferenceKind::Response
                )
            {
                return Err(reference
                    .error(Code::InvalidDeclaration)
                    .reasoned(crate::DiagnosticReason::UnsupportedReferencePosition));
            }
            if chain.len() > limit {
                return Err(reference.locate(Diagnostic::limited(
                    crate::LimitKind::ReferenceDepth,
                    limit,
                    chain.len(),
                )));
            }
            if description.is_none()
                && !matches!(kind, ReferenceKind::Callback | ReferenceKind::MediaType)
                && (kind == ReferenceKind::PathItem || edition.reference_siblings())
            {
                description = current.get("description");
            }
            if summary.is_none()
                && (kind == ReferenceKind::PathItem
                    || kind == ReferenceKind::Example && edition.reference_siblings())
            {
                summary = current.get("summary");
            }
            let text = reference
                .as_str()
                .ok_or_else(|| reference.error(Code::InvalidReference))?;
            let (next, is_canonical) = self.target(&reference, text, kind, edition)?;
            canonical &= is_canonical;
            current = next;
        }
        if !valid_kind(&current, kind, edition) {
            return Err(current.error(Code::WrongReferenceKind));
        }
        if description.is_none()
            && !matches!(kind, ReferenceKind::Callback | ReferenceKind::MediaType)
        {
            description = current.get("description");
        }
        if summary.is_none() && matches!(kind, ReferenceKind::PathItem | ReferenceKind::Example) {
            summary = current.get("summary");
        }
        Ok(crate::ResolvedReference {
            authored: node.clone(),
            target: current,
            summary,
            description,
            chain,
            canonical,
        })
    }
}
fn enqueue(
    queue: &mut VecDeque<(Value, Context)>,
    value: Value,
    kind: ObjectKind,
    edition: Edition,
    limit: usize,
) -> Result<(), Diagnostic> {
    bound(queue.len(), 1, limit, crate::LimitKind::GraphNodes).map_err(|e| value.locate(e))?;
    if queue.len() == queue.capacity() {
        let capacity = queue.capacity().saturating_mul(2).max(4).min(limit);
        queue.reserve_exact(capacity.saturating_sub(queue.len()));
    }
    queue.push_back((value, Context { kind, edition }));
    Ok(())
}
fn enqueue_content(
    node: &Value,
    queue: &mut VecDeque<(Value, Context)>,
    edition: Edition,
    limit: usize,
    cancellation: &crate::Cancellation,
) -> Result<(), Diagnostic> {
    if let Some(content) = node.get("content")
        && let Some(values) = content.members()
    {
        for (_, value) in values {
            cancellation.check()?;
            enqueue(
                queue,
                value,
                if edition == Edition::OpenApi32 {
                    ObjectKind::Reference(ReferenceKind::MediaType)
                } else {
                    ObjectKind::Media
                },
                edition,
                limit,
            )?;
        }
    }
    Ok(())
}
fn enqueue_encodings(
    node: &Value,
    queue: &mut VecDeque<(Value, Context)>,
    edition: Edition,
    limit: usize,
    cancellation: &crate::Cancellation,
) -> Result<(), Diagnostic> {
    if let Some(map) = node.get("encoding")
        && let Some(values) = map.members()
    {
        for (_, value) in values {
            cancellation.check()?;
            enqueue(queue, value, ObjectKind::Encoding, edition, limit)?;
        }
    }
    if edition == Edition::OpenApi32 {
        if let Some(array) = node.get("prefixEncoding")
            && let Some(values) = array.elements()
        {
            for value in values {
                cancellation.check()?;
                enqueue(queue, value, ObjectKind::Encoding, edition, limit)?;
            }
        }
        if let Some(value) = node.get("itemEncoding") {
            cancellation.check()?;
            enqueue(queue, value, ObjectKind::Encoding, edition, limit)?;
        }
    }
    Ok(())
}
fn valid_kind(value: &Value, kind: ReferenceKind, edition: Edition) -> bool {
    match kind {
        ReferenceKind::Parameter => {
            value.get("name").is_some_and(|v| v.as_str().is_some())
                && value.get("in").is_some_and(|v| v.as_str().is_some())
        }
        ReferenceKind::Response => value
            .get("description")
            .is_some_and(|v| v.as_str().is_some()),
        ReferenceKind::RequestBody => {
            edition != Edition::Swagger20
                && value
                    .get("content")
                    .is_some_and(|v| v.kind() == ValueKind::Object)
        }
        ReferenceKind::SecurityScheme => value.get("type").is_some_and(|v| v.as_str().is_some()),
        ReferenceKind::Header => {
            edition != Edition::Swagger20
                && value.get("name").is_none()
                && value.get("in").is_none()
        }
        ReferenceKind::Example | ReferenceKind::Link | ReferenceKind::Callback => {
            edition != Edition::Swagger20
        }
        ReferenceKind::MediaType => edition == Edition::OpenApi32,
        ReferenceKind::PathItem => value.members().expect("object checked").all(|(name, _)| {
            name.starts_with("x-")
                || matches!(
                    name,
                    "summary" | "description" | "servers" | "parameters" | "additionalOperations"
                )
                || crate::Method::parse(name).is_some_and(|_| name == name.to_ascii_lowercase())
        }),
    }
}

/// Owner-free result of URI resolution; no interpretation or acquisition implied.
pub(crate) struct Address {
    pub root: Option<NodeKey>,
    pub exact: Option<NodeKey>,
    pub fragment: ResourceFragment,
    pub uri: Option<String>,
}
pub(crate) fn decode_fragment(
    fragment: &str,
    source: &Value,
) -> Result<ResourceFragment, Diagnostic> {
    let decoded =
        crate::uri::percent_decode(fragment).map_err(|_| source.error(Code::InvalidReference))?;
    Ok(if decoded.is_empty() {
        ResourceFragment::Root
    } else if decoded.starts_with('/') {
        ResourceFragment::Pointer(decoded)
    } else {
        ResourceFragment::Anchor(decoded)
    })
}

#[cfg(test)]
mod ownership_tests {
    use super::*;

    fn check_admission(builder: &DocumentBuilder) {
        let mut aliases = HashMap::new();
        let mut totals = (0usize, 0usize, 0usize);
        let mut metadata = MetadataUse::default();
        for (index, resource) in builder.resources.iter().enumerate() {
            assert_eq!(builder.admission.indices.get(&resource.id), Some(&index));
            for alias in resource
                .aliases
                .iter()
                .map(String::as_str)
                .chain(resource.canonical.as_deref())
                .chain(resource.canonical.as_deref().map(without_fragment))
            {
                let previous = aliases.insert(identity_key(alias).unwrap(), resource.id);
                assert!(previous.is_none_or(|id| id == resource.id));
            }
            totals.0 += resource.json.original_source().len();
            totals.1 += resource.json.node_count();
            totals.2 += resource.json.retained_bytes();
            metadata = metadata.plus(resource.metadata());
        }
        assert_eq!(builder.admission.indices.len(), builder.resources.len());
        assert_eq!(builder.admission.aliases, aliases);
        assert_eq!(builder.totals(), totals);
        assert_eq!(builder.metadata_total(), metadata);
    }

    #[test]
    fn admission_deltas_are_atomic_and_snapshot_forks_keep_exact_accounting() {
        let bytes: Arc<[u8]> = Arc::from(br##"{"openapi":"3.2.1","$self":"https://canonical.example/root#named","paths":{},"x-p":{"name":"x","in":"query"}}"##.as_slice());
        let input = || ResourceInput::at("https://docs.example/root", bytes.clone()).unwrap();
        let mut builder = DocumentBuilder::new(Limits::default(), GraphLimits::default());
        let entry = builder.add(input()).unwrap();
        let other = builder
            .add(
                ResourceInput::at(
                    "https://docs.example/other",
                    Arc::<[u8]>::from(b"{}".as_slice()),
                )
                .unwrap(),
            )
            .unwrap();
        check_admission(&builder);

        // A late collision must not publish an earlier alias in the same call.
        let failure = builder
            .add(
                input()
                    .aliases([
                        "https://docs.example/pending".into(),
                        "https://docs.example/other".into(),
                    ])
                    .unwrap(),
            )
            .unwrap_err();
        assert_eq!(failure.diagnostic.code(), Code::IdentityCollision);
        assert!(
            !builder
                .admission
                .aliases
                .contains_key("https://docs.example/pending")
        );
        check_admission(&builder);
        assert_eq!(
            builder
                .add(
                    input()
                        .aliases([
                            "https://docs.example/pending".into(),
                            "https://docs.example/pending".into(),
                        ])
                        .unwrap()
                )
                .unwrap(),
            entry
        );
        assert_eq!(builder.resources[0].aliases.len(), 2);
        check_admission(&builder);

        // A new source's canonical collision also rolls back its retrieval key.
        let failure = builder
            .add(ResourceInput::at("https://docs.example/uncommitted", bytes.clone()).unwrap())
            .unwrap_err();
        assert_eq!(failure.diagnostic.code(), Code::IdentityCollision);
        assert!(
            !builder
                .admission
                .aliases
                .contains_key("https://docs.example/uncommitted")
        );
        check_admission(&builder);

        let doc = builder.build(entry).unwrap();
        let weak = Arc::downgrade(&doc.0);
        let mut fork = DocumentBuilder::from_document(&doc);
        check_admission(&fork);
        let layout =
            ResourceLayout::new().protocol("/x-p", ReferenceKind::Parameter, Edition::OpenApi32);
        fork.enrich_layout(entry, layout.clone()).unwrap();
        fork.enrich_layout(entry, layout).unwrap();
        check_admission(&fork);
        let error = fork
            .enrich_layout(
                entry,
                ResourceLayout::new().protocol("/x-p", ReferenceKind::Header, Edition::OpenApi32),
            )
            .unwrap_err();
        assert_eq!(error.code(), Code::ConflictingObjectContext);
        check_admission(&fork);
        assert_eq!(
            fork.add(
                input()
                    .aliases(["https://docs.example/later".into()])
                    .unwrap()
            )
            .unwrap(),
            entry
        );
        check_admission(&fork);
        assert_eq!(doc.entry_resource().aliases().count(), 2);
        assert_eq!(
            doc.entry_resource()
                .protocol_at("/x-p", ReferenceKind::Parameter)
                .unwrap_err()
                .code(),
            Code::WrongReferenceKind
        );
        let next = fork.build(entry).unwrap();
        assert_eq!(next.entry_resource().aliases().count(), 3);
        assert!(next.resource(other).is_some());
        assert!(
            next.entry_resource()
                .protocol_at("/x-p", ReferenceKind::Parameter)
                .is_ok()
        );
        drop(doc);
        assert!(
            weak.upgrade().is_none(),
            "new admission indexes may not retain the old Store"
        );
    }

    #[test]
    fn failed_or_cancelled_admission_does_not_spend_graph_allowances() {
        let bytes: Arc<[u8]> = Arc::from(b"{\"openapi\":\"3.2.1\",\"paths\":{}}".as_slice());
        let caps = GraphLimits {
            identities: 2,
            resources: 2,
            ..GraphLimits::default()
        };
        let mut builder = DocumentBuilder::new(Limits::default(), caps);
        let input = || ResourceInput::at("https://docs.example/root", bytes.clone()).unwrap();
        let entry = builder.add(input()).unwrap();
        let metadata = builder.metadata_total();
        let failure = builder
            .add(
                input()
                    .aliases([
                        "https://docs.example/a".into(),
                        "https://docs.example/b".into(),
                    ])
                    .unwrap(),
            )
            .unwrap_err();
        assert_eq!(failure.diagnostic.code(), Code::Limit);
        assert_eq!(builder.metadata_total(), metadata);
        check_admission(&builder);
        let cancelled = crate::Cancellation::default();
        cancelled.cancel();
        let failure = builder
            .add_with_cancellation(input(), &cancelled)
            .unwrap_err();
        assert_eq!(failure.diagnostic.code(), Code::Cancelled);
        check_admission(&builder);
        let failure = builder
            .add(
                ResourceInput::at(
                    "https://docs.example/bad",
                    Arc::<[u8]>::from(b"{]".as_slice()),
                )
                .unwrap(),
            )
            .unwrap_err();
        assert_ne!(failure.diagnostic.code(), Code::Limit);
        check_admission(&builder);
        builder
            .add(ResourceInput::at("https://docs.example/second", bytes).unwrap())
            .unwrap();
        check_admission(&builder);
        assert_eq!(builder.build(entry).unwrap().resources().len(), 2);
    }

    #[test]
    fn bounded_vectors_and_index_queue_grow_geometrically() {
        let maximum = 1025;
        let mut values = Vec::new();
        let mut queue = VecDeque::new();
        let node = ExactJson::parse("{}", Limits::default()).unwrap().root();
        let (mut vector_growth, mut queue_growth) = (0, 0);
        for i in 0..maximum {
            let old = values.capacity();
            reserve_vec(&mut values, maximum);
            values.push(i);
            vector_growth += usize::from(values.capacity() != old);
            assert!(values.capacity() <= maximum);
            let old = queue.capacity();
            enqueue(
                &mut queue,
                node.clone(),
                ObjectKind::Document,
                Edition::OpenApi32,
                maximum,
            )
            .unwrap();
            queue_growth += usize::from(queue.capacity() != old);
            assert!(queue.capacity() <= maximum);
        }
        assert!(vector_growth <= 11, "{vector_growth} vector allocations");
        assert!(queue_growth <= 11, "{queue_growth} queue allocations");
        assert_eq!(
            enqueue(
                &mut queue,
                node,
                ObjectKind::Document,
                Edition::OpenApi32,
                maximum
            )
            .unwrap_err()
            .code(),
            Code::Limit
        );
    }

    #[test]
    fn cached_success_failure_and_public_needs_release_the_snapshot() {
        let bytes = br##"{"openapi":"3.2.1","servers":[{"url":"https://api.example"}],"paths":{"/ok":{"get":{"responses":{"200":{"description":"ok"}}}},"/missing":{"get":{"parameters":[{"$ref":"other"}],"responses":{"200":{"description":"ok"}}}}}}"##;
        let doc =
            crate::Document::parse(bytes, Some("https://docs.example/root"), Limits::default())
                .unwrap();
        let weak = Arc::downgrade(&doc.0);
        let source = doc.root();
        let ready = doc
            .operation("/ok", crate::Method::Get)
            .unwrap()
            .prepare(&crate::Input::default())
            .unwrap();
        let operation = doc.operation("/missing", crate::Method::Get).unwrap();
        let error = operation.prepare(&crate::Input::default()).unwrap_err();
        assert!(
            operation
                .record()
                .compiled
                .get()
                .unwrap()
                .as_ref()
                .err()
                .unwrap()
                .resource_need()
                .is_none(),
            "cached diagnostic must remain owner-free"
        );
        let need = error.resource_need().unwrap().clone();
        let resource = doc.entry_resource();
        let site = resource
            .protocol_at(
                "/paths/~1missing/get/parameters/0",
                ReferenceKind::Parameter,
            )
            .unwrap();
        drop(error);
        drop(operation);
        drop(ready);
        drop(doc);
        assert!(weak.upgrade().is_some());
        drop(resource);
        drop(site);
        assert!(
            weak.upgrade().is_some(),
            "need is a deliberate public owner"
        );
        assert_eq!(need.source().as_str(), Some("other"));
        drop(need);
        assert!(
            weak.upgrade().is_none(),
            "cached success/failure may not keep Store alive"
        );
        assert_eq!(source.get("openapi").unwrap().as_str(), Some("3.2.1"));
    }
}
