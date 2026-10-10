//! Owner-free, demand-driven declaration facts for the future shared encoder.
//! No instance validation, branches, defaults, coercion, acquisition or Store cache.
#![allow(dead_code)] // G2a qualifies this seam privately; G2b owns encoder integration.
use crate::graph::{Graph, NeedFact, NodeKey, bound, key};
use crate::schema::{SchemaProfile, SchemaReason, SchemaReferenceKind};
use crate::{Cancellation, Code, Diagnostic, DiagnosticReason, LimitKind, Limits, Value};
use std::collections::{HashMap, HashSet};

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct SchemaSelection(pub Vec<NodeKey>);
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum FactQuery<'a> {
    Types,
    ContentEncoding,
    ContentMediaType,
    LegacyFormat,
    Property(&'a str),
    Items,
    ObjectShape,
}
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub(crate) enum SchemaType {
    Null,
    Boolean,
    Object,
    Array,
    Number,
    Integer,
    String,
}
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum Items {
    Homogeneous(SchemaSelection),
    Positional(Vec<SchemaSelection>),
}
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct Shape {
    pub names: Vec<String>,
    pub open: bool,
    pub incomplete: bool,
}
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum FactValue {
    Types(Vec<SchemaType>),
    Text(String),
    Selection(SchemaSelection),
    Items(Items),
    Shape(Shape),
}
#[derive(Clone)]
pub(crate) enum Fact<T> {
    Known {
        value: T,
        provenance: Vec<NodeKey>,
    },
    Absent {
        provenance: Vec<NodeKey>,
    },
    Need(NeedFact),
    Indeterminate {
        reason: SchemaReason,
        sites: Vec<NodeKey>,
    },
    Invalid(Diagnostic),
}
pub(crate) struct FactWork<'a> {
    limits: &'a Limits,
    cancellation: &'a Cancellation,
    used: usize,
}
impl<'a> FactWork<'a> {
    pub fn new(limits: &'a Limits, cancellation: &'a Cancellation) -> Self {
        Self {
            limits,
            cancellation,
            used: 0,
        }
    }
    pub fn used(&self) -> usize {
        self.used
    }
    fn charge(&mut self, amount: usize, value: &Value) -> Result<(), Diagnostic> {
        crate::schema::checkpoint(self.cancellation)?;
        bound(
            self.used,
            amount,
            self.limits.schema_fact_work,
            LimitKind::SchemaFactWork,
        )
        .map_err(|e| value.locate(e))?;
        self.used += amount;
        Ok(())
    }
}
// One query owns one memo and accumulator. Keys are source identities; no Store
// owners or caller field names survive the call. A completed node contributes
// once because conjunction is idempotent, but every incoming path is admitted.
#[derive(Clone, Copy)]
struct Suffix {
    hops: usize,
    // Immediate edges retain source order. No copied transitive paths.
    children: [Option<(NodeKey, NodeKey)>; 2],
}
enum Task {
    Visit(NodeKey, usize, Option<NodeKey>),
    Exit(NodeKey, [Option<(NodeKey, NodeKey)>; 2]),
}
#[derive(Clone, Copy, PartialEq, Eq)]
pub(crate) enum WorkStage {
    Memo,
    Accumulator,
    Provenance,
}
impl FactWork<'_> {
    fn at(&mut self, amount: usize, value: &Value, stage: WorkStage) -> Result<(), Diagnostic> {
        #[cfg(test)]
        TEST_CANCEL_STAGE.with(|selected| {
            if selected.get() == Some(stage) {
                self.cancellation.cancel();
            }
        });
        #[cfg(not(test))]
        let _ = stage;
        self.charge(amount, value)
    }
}
#[cfg(test)]
thread_local! {static TEST_CANCEL_STAGE: std::cell::Cell<Option<WorkStage>> = const {std::cell::Cell::new(None)};}
#[cfg(test)]
pub(crate) struct StageGuard;
#[cfg(test)]
impl Drop for StageGuard {
    fn drop(&mut self) {
        TEST_CANCEL_STAGE.with(|selected| selected.set(None));
    }
}
#[cfg(test)]
pub(crate) fn cancel_at_for_test(stage: WorkStage) -> StageGuard {
    TEST_CANCEL_STAGE.with(|selected| selected.set(Some(stage)));
    StageGuard
}
pub(crate) fn query(
    graph: &Graph,
    selection: &SchemaSelection,
    query: FactQuery<'_>,
    work: &mut FactWork<'_>,
) -> Result<Fact<FactValue>, Diagnostic> {
    crate::schema::checkpoint(work.cancellation)?;
    let mut stack = Vec::new();
    for &node in selection.0.iter().rev() {
        work.charge(1, &graph.value(node))?;
        stack.push(Task::Visit(node, 0, None));
    }
    let mut active = HashSet::new();
    let mut completed: HashMap<NodeKey, Suffix> = HashMap::new();
    let mut result = Accumulator::default();
    while let Some(task) = stack.pop() {
        let (node, depth, incoming) = match task {
            Task::Exit(node, children) => {
                let value = graph.value(node);
                let mut suffix = Suffix { hops: 0, children };
                for (child, _) in children.into_iter().flatten() {
                    work.charge(1, &value)?;
                    let child = completed.get(&child).expect("completed child");
                    suffix.hops = suffix.hops.max(child.hops + 1);
                }
                work.charge(1, &value)?;
                active.remove(&node);
                completed.insert(node, suffix);
                continue;
            }
            Task::Visit(node, depth, incoming) => (node, depth, incoming),
        };
        let value = graph.value(node);
        // Hash lookups and possible insertion are admitted before doing them.
        work.charge(1, &value)?;
        bound(
            0,
            depth,
            work.limits.schema_fact_depth,
            LimitKind::SchemaFactDepth,
        )
        .map_err(|e| value.locate(e))?;
        if active.contains(&node) {
            return Ok(indeterminate(
                SchemaReason::RecursiveFact,
                incoming.unwrap_or(node),
            ));
        }
        if let Some(suffix) = completed.get(&node) {
            work.at(1, &value, WorkStage::Memo)?;
            let maximum = work
                .limits
                .reference_steps
                .min(work.limits.schema_fact_depth);
            if depth.saturating_add(suffix.hops) > maximum {
                check_memo_path(graph, &completed, node, depth, work)?;
            }
            continue;
        }
        active.insert(node);
        if let Err(error) = graph.schemas.check(node, graph) {
            return failure(error);
        }
        let profile = &graph.schemas.scope(node).expect("checked").profile;
        if value.as_bool() == Some(false) {
            return Ok(indeterminate(SchemaReason::ProhibitingSchema, node));
        }
        let mut children = [None; 2];
        let mut has_reference = false;
        for (index, keyword) in ["$dynamicRef", "$ref"].into_iter().enumerate() {
            if keyword == "$dynamicRef" && !profile.modern() {
                continue;
            }
            if let Some(edge) = value.get(keyword) {
                has_reference = true;
                work.charge(
                    edge.as_str().map_or(1, |s| s.len().saturating_add(1)),
                    &edge,
                )?;
                bound(
                    0,
                    depth.saturating_add(1),
                    work.limits.reference_steps,
                    LimitKind::ReferenceDepth,
                )
                .map_err(|e| edge.locate(e))?;
                let kind = if keyword == "$ref" {
                    SchemaReferenceKind::Ref
                } else {
                    SchemaReferenceKind::DynamicRef
                };
                let target = match graph.schema_target(node, key(&edge), kind, work.cancellation) {
                    Ok(t) => t,
                    Err(error) => return failure(error),
                };
                if target.dynamic {
                    return Ok(indeterminate(
                        SchemaReason::RequiresDynamicScope,
                        key(&edge),
                    ));
                }
                children[index] = Some((target.target, key(&edge)));
            }
        }
        work.charge(1, &value)?;
        stack.push(Task::Exit(node, children));
        for (child, edge) in children.into_iter().flatten() {
            work.charge(1, &value)?;
            stack.push(Task::Visit(child, depth.saturating_add(1), Some(edge)));
        }
        if value.as_bool() == Some(true) || (has_reference && !profile.modern()) {
            continue;
        }
        if let Some(applicator) = ["allOf", "anyOf", "oneOf", "not", "if", "then", "else"]
            .iter()
            .find_map(|name| value.get(name))
        {
            return Ok(indeterminate(SchemaReason::Composition, key(&applicator)));
        }
        match local_fact(&value, profile, &query, work)? {
            Fact::Known {
                value: next,
                provenance,
            } => {
                result.sites(provenance, true, work, &value)?;
                if let Some(reason) = result.merge(next, work, &value)? {
                    return Ok(Fact::Indeterminate {
                        reason,
                        sites: result.provenance,
                    });
                }
            }
            Fact::Absent { provenance } => result.sites(provenance, false, work, &value)?,
            other => return Ok(other),
        }
    }
    Ok(match result.value {
        Some(value) => Fact::Known {
            value,
            provenance: result.provenance,
        },
        None => Fact::Absent {
            provenance: result.provenance,
        },
    })
}
// A failing memo hit needs the first refused edge/node, not the deepest leaf.
// Successful subgraphs are pruned by their suffix bound. At most two immediate
// edges are inspected per step; the first failing path is traced once and exits.
fn check_memo_path(
    graph: &Graph,
    completed: &HashMap<NodeKey, Suffix>,
    mut node: NodeKey,
    mut depth: usize,
    work: &mut FactWork<'_>,
) -> Result<(), Diagnostic> {
    loop {
        let value = graph.value(node);
        work.at(1, &value, WorkStage::Memo)?;
        let suffix = completed.get(&node).expect("completed memo path");
        // Query resolves/checks its immediate edges in keyword order before
        // visiting either child. Preserve that order, including ties.
        for (_, edge) in suffix.children.into_iter().flatten() {
            work.charge(1, &value)?;
            bound(
                0,
                depth.saturating_add(1),
                work.limits.reference_steps,
                LimitKind::ReferenceDepth,
            )
            .map_err(|e| graph.value(edge).locate(e))?;
        }
        let maximum = work
            .limits
            .reference_steps
            .min(work.limits.schema_fact_depth);
        let mut next = None;
        // The iterative DFS stack visits $ref before $dynamicRef. Choose the
        // first child whose completed path cannot fit; earlier fitting children
        // cannot supply a refusal and need not be expanded again.
        for (child, _) in suffix.children.into_iter().rev().flatten() {
            work.charge(1, &value)?;
            let child_suffix = completed.get(&child).expect("completed child");
            if depth.saturating_add(1).saturating_add(child_suffix.hops) > maximum {
                bound(
                    0,
                    depth.saturating_add(1),
                    work.limits.schema_fact_depth,
                    LimitKind::SchemaFactDepth,
                )
                .map_err(|e| graph.value(child).locate(e))?;
                next = Some(child);
                break;
            }
        }
        let Some(child) = next else {
            return Ok(());
        };
        node = child;
        depth += 1;
    }
}
fn indeterminate(reason: SchemaReason, node: NodeKey) -> Fact<FactValue> {
    Fact::Indeterminate {
        reason,
        sites: vec![node],
    }
}
fn failure(error: Diagnostic) -> Result<Fact<FactValue>, Diagnostic> {
    if matches!(error.code(), Code::Limit | Code::Cancelled) {
        return Err(error);
    }
    if let Some(need) = error.need_data() {
        return Ok(Fact::Need(need.clone()));
    }
    if let Some(DiagnosticReason::Schema(reason)) = error.reason()
        && error.code() == Code::UnsupportedSchema
    {
        return Ok(Fact::Indeterminate {
            reason: reason.clone(),
            sites: vec![],
        });
    }
    Ok(Fact::Invalid(error))
}
fn local_fact(
    value: &Value,
    profile: &SchemaProfile,
    query: &FactQuery<'_>,
    work: &mut FactWork<'_>,
) -> Result<Fact<FactValue>, Diagnostic> {
    work.charge(1, value)?;
    let absent = || Fact::Absent {
        provenance: vec![key(value)],
    };
    let source_keyword = match query {
        FactQuery::Types => Some("type"),
        FactQuery::ContentEncoding => Some("contentEncoding"),
        FactQuery::ContentMediaType => Some("contentMediaType"),
        FactQuery::LegacyFormat => Some("format"),
        FactQuery::Items => Some(if value.get("prefixItems").is_some() {
            "prefixItems"
        } else {
            "items"
        }),
        _ => None,
    };
    let source = source_keyword
        .and_then(|name| value.get(name))
        .map_or(key(value), |v| key(&v));
    let known = |value| Fact::Known {
        value,
        provenance: vec![source],
    };
    let invalid = |site: &Value| {
        Fact::Invalid(crate::schema::schema_error(
            site,
            Code::InvalidDeclaration,
            SchemaReason::InvalidKeyword,
        ))
    };
    let text = match query {
        FactQuery::ContentEncoding if profile.modern() => Some("contentEncoding"),
        FactQuery::ContentMediaType if profile.modern() => Some("contentMediaType"),
        FactQuery::LegacyFormat => Some("format"),
        _ => None,
    };
    if let Some(keyword) = text {
        return Ok(match value.get(keyword) {
            None => absent(),
            Some(site) => match site.as_str() {
                Some(text) => {
                    work.charge(text.len().saturating_add(1), &site)?;
                    known(FactValue::Text(text.to_owned()))
                }
                None => invalid(&site),
            },
        });
    }
    Ok(match query {
        FactQuery::ContentEncoding | FactQuery::ContentMediaType => absent(),
        FactQuery::LegacyFormat => unreachable!(),
        FactQuery::Types => {
            let Some(site) = value.get("type") else {
                return Ok(absent());
            };
            let mut types = Vec::new();
            if let Some(name) = site.as_str() {
                let Some(ty) = schema_type(name) else {
                    return Ok(invalid(&site));
                };
                if !profile.modern() && ty == SchemaType::Null {
                    return Ok(invalid(&site));
                }
                types.push(ty);
            } else if profile.modern()
                && let Some(values) = site.elements()
            {
                for v in values {
                    work.charge(1, &v)?;
                    let Some(ty) = v.as_str().and_then(schema_type) else {
                        return Ok(invalid(&v));
                    };
                    if types.contains(&ty) {
                        return Ok(invalid(&v));
                    }
                    types.push(ty);
                }
                if types.is_empty() {
                    return Ok(invalid(&site));
                }
            } else {
                return Ok(invalid(&site));
            }
            if matches!(profile, SchemaProfile::OpenApi30) {
                // Nullable modifies only an explicitly local OAS 3.0 type.
                // This is a declaration fact; other constraints may reject null.
                work.charge(1, value)?;
                if let Some(nullable) = value.get("nullable") {
                    match nullable.as_bool() {
                        Some(true) => {
                            types.push(SchemaType::Null);
                            types.sort();
                            return Ok(Fact::Known {
                                value: FactValue::Types(types),
                                provenance: vec![source, key(&nullable)],
                            });
                        }
                        Some(false) => {}
                        None => return Ok(invalid(&nullable)),
                    }
                }
            }
            types.sort();
            known(FactValue::Types(types))
        }
        FactQuery::Property(name) => {
            work.charge(name.len().saturating_add(1), value)?;
            if profile.modern()
                && (value
                    .get("patternProperties")
                    .is_some_and(|p| p.members().is_none_or(|mut m| m.next().is_some()))
                    || value.get("unevaluatedProperties").is_some())
            {
                return Ok(indeterminate(
                    SchemaReason::DynamicPropertyFacts,
                    key(&value
                        .get("patternProperties")
                        .or_else(|| value.get("unevaluatedProperties"))
                        .expect("applicable dynamic member")),
                ));
            }
            if let Some(properties) = value.get("properties") {
                if properties.members().is_none() {
                    return Ok(invalid(&properties));
                }
                if let Some(property) = properties.get(name) {
                    return Ok(known(FactValue::Selection(SchemaSelection(vec![key(
                        &property,
                    )]))));
                }
            }
            match value.get("additionalProperties") {
                None => absent(),
                Some(v) if v.as_bool().is_some() => absent(),
                Some(v) if v.members().is_some() => {
                    known(FactValue::Selection(SchemaSelection(vec![key(&v)])))
                }
                Some(v) => invalid(&v),
            }
        }
        FactQuery::Items => {
            if profile.modern()
                && let Some(prefix) = value.get("prefixItems")
            {
                let Some(values) = prefix.elements() else {
                    return Ok(invalid(&prefix));
                };
                let mut selections = Vec::new();
                for v in values {
                    work.charge(1, &v)?;
                    selections.push(SchemaSelection(vec![key(&v)]));
                }
                known(FactValue::Items(Items::Positional(selections)))
            } else if let Some(item) = value.get("items") {
                if item.members().is_some() || (profile.modern() && item.as_bool().is_some()) {
                    known(FactValue::Items(Items::Homogeneous(SchemaSelection(vec![
                        key(&item),
                    ]))))
                } else {
                    invalid(&item)
                }
            } else {
                absent()
            }
        }
        FactQuery::ObjectShape => {
            let mut names = Vec::new();
            if let Some(properties) = value.get("properties") {
                let Some(members) = properties.members() else {
                    return Ok(invalid(&properties));
                };
                for (name, site) in members {
                    work.charge(name.len().saturating_add(1), &site)?;
                    names.push(name.to_owned());
                }
            }
            let open = value.get("additionalProperties").and_then(|v| v.as_bool()) != Some(false);
            let incomplete = profile.modern()
                && (value.get("patternProperties").is_some()
                    || value.get("unevaluatedProperties").is_some());
            known(FactValue::Shape(Shape {
                names,
                open,
                incomplete,
            }))
        }
    })
}
#[derive(Default)]
struct Accumulator {
    value: Option<FactValue>,
    provenance: Vec<NodeKey>,
    known_sites: HashSet<NodeKey>,
    known_names: HashSet<String>,
    known_selections: HashSet<NodeKey>,
}
impl Accumulator {
    fn sites(
        &mut self,
        sites: Vec<NodeKey>,
        contributes: bool,
        work: &mut FactWork<'_>,
        at: &Value,
    ) -> Result<(), Diagnostic> {
        // Before the first Known contribution these are absence sites. They do
        // not contribute to a Known payload; reuse the same bounded storage.
        if contributes && self.value.is_none() {
            work.at(self.known_sites.capacity(), at, WorkStage::Provenance)?;
            self.known_sites.clear();
            self.provenance.clear();
        }
        for site in sites {
            work.at(1, at, WorkStage::Provenance)?;
            // Later absence is still checked and charged, but does not explain
            // the Known payload already accumulated.
            if (contributes || self.value.is_none()) && self.known_sites.insert(site) {
                self.provenance.push(site);
            }
        }
        Ok(())
    }
    fn selection(
        &mut self,
        next: SchemaSelection,
        items: bool,
        work: &mut FactWork<'_>,
        at: &Value,
    ) -> Result<bool, Diagnostic> {
        if self.value.is_none() {
            let empty = SchemaSelection(Vec::new());
            self.value = Some(if items {
                FactValue::Items(Items::Homogeneous(empty))
            } else {
                FactValue::Selection(empty)
            });
        }
        let selected = match self.value.as_mut().expect("initialized") {
            FactValue::Selection(s) if !items => s,
            FactValue::Items(Items::Homogeneous(s)) if items => s,
            _ => return Ok(true),
        };
        for node in next.0 {
            work.charge(1, at)?;
            if self.known_selections.insert(node) {
                selected.0.push(node);
            }
        }
        Ok(false)
    }
    fn merge(
        &mut self,
        next: FactValue,
        work: &mut FactWork<'_>,
        at: &Value,
    ) -> Result<Option<SchemaReason>, Diagnostic> {
        work.at(1, at, WorkStage::Accumulator)?;
        match next {
            FactValue::Types(mut next) => {
                // At most seven primitive declarations. Number denotes both
                // integer and non-integer numbers; no instances are inspected.
                if let Some(FactValue::Types(previous)) = &self.value {
                    work.charge(28, at)?;
                    let mask = type_mask(previous) & type_mask(&next);
                    if mask == 0 {
                        return Ok(Some(SchemaReason::ConflictingFacts));
                    }
                    self.value = Some(FactValue::Types(types_from_mask(mask)));
                } else if self.value.is_none() {
                    work.charge(next.len(), at)?;
                    if next.contains(&SchemaType::Number) {
                        next.retain(|t| *t != SchemaType::Integer);
                    }
                    self.value = Some(FactValue::Types(next));
                } else {
                    return Ok(Some(SchemaReason::ConflictingFacts));
                }
            }
            FactValue::Text(next) => {
                if let Some(FactValue::Text(previous)) = &self.value {
                    work.charge(previous.len().min(next.len()).saturating_add(1), at)?;
                    if previous != &next {
                        return Ok(Some(SchemaReason::ConflictingFacts));
                    }
                } else if self.value.is_none() {
                    self.value = Some(FactValue::Text(next));
                } else {
                    return Ok(Some(SchemaReason::ConflictingFacts));
                }
            }
            FactValue::Selection(next) => {
                if self.selection(next, false, work, at)? {
                    return Ok(Some(SchemaReason::ConflictingFacts));
                }
            }
            FactValue::Items(Items::Homogeneous(next)) => {
                if self.selection(next, true, work, at)? {
                    return Ok(Some(SchemaReason::ConflictingFacts));
                }
            }
            FactValue::Items(Items::Positional(next)) => {
                if let Some(FactValue::Items(Items::Positional(previous))) = &self.value {
                    work.charge(1, at)?;
                    if previous.len() != next.len() {
                        return Ok(Some(SchemaReason::ConflictingFacts));
                    }
                    for (a, b) in previous.iter().zip(&next) {
                        work.charge(a.0.len().min(b.0.len()).saturating_add(1), at)?;
                        if a != b {
                            return Ok(Some(SchemaReason::ConflictingFacts));
                        }
                    }
                } else if self.value.is_none() {
                    self.value = Some(FactValue::Items(Items::Positional(next)));
                } else {
                    return Ok(Some(SchemaReason::ConflictingFacts));
                }
            }
            FactValue::Shape(next) => {
                if self.value.is_none() {
                    self.value = Some(FactValue::Shape(Shape {
                        names: Vec::new(),
                        open: true,
                        incomplete: false,
                    }));
                }
                let Some(FactValue::Shape(previous)) = &mut self.value else {
                    return Ok(Some(SchemaReason::ConflictingFacts));
                };
                for name in next.names {
                    // One hash/copy for this incoming name, never a clone of all
                    // accumulated names. Membership and output grow geometrically.
                    work.at(
                        name.len().saturating_mul(3).saturating_add(3),
                        at,
                        WorkStage::Accumulator,
                    )?;
                    if !self.known_names.contains(&name) {
                        self.known_names.insert(name.clone());
                        previous.names.push(name);
                    }
                }
                previous.open &= next.open;
                previous.incomplete |= next.incomplete;
            }
        }
        Ok(None)
    }
}
fn type_mask(types: &[SchemaType]) -> u8 {
    types.iter().fold(0, |mask, ty| {
        mask | match ty {
            SchemaType::Null => 1,
            SchemaType::Boolean => 2,
            SchemaType::Object => 4,
            SchemaType::Array => 8,
            SchemaType::Number => 16 | 32,
            SchemaType::Integer => 32,
            SchemaType::String => 64,
        }
    })
}
fn types_from_mask(mask: u8) -> Vec<SchemaType> {
    let mut types = Vec::new();
    for (bit, ty) in [
        (1, SchemaType::Null),
        (2, SchemaType::Boolean),
        (4, SchemaType::Object),
        (8, SchemaType::Array),
        (16, SchemaType::Number),
        (32, SchemaType::Integer),
        (64, SchemaType::String),
    ] {
        if mask & bit != 0 && !(ty == SchemaType::Integer && mask & 16 != 0) {
            types.push(ty);
        }
    }
    types
}
fn schema_type(name: &str) -> Option<SchemaType> {
    Some(match name {
        "null" => SchemaType::Null,
        "boolean" => SchemaType::Boolean,
        "object" => SchemaType::Object,
        "array" => SchemaType::Array,
        "number" => SchemaType::Number,
        "integer" => SchemaType::Integer,
        "string" => SchemaType::String,
        _ => return None,
    })
}
