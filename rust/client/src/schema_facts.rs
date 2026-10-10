//! Owner-free, demand-driven declaration facts for the future shared encoder.
//! No instance validation, branches, defaults, coercion, acquisition or Store cache.
#![allow(dead_code)] // G2a qualifies this seam privately; G2b owns encoder integration.
use crate::graph::{Graph, NeedFact, NodeKey, bound, key};
use crate::schema::{SchemaProfile, SchemaReason, SchemaReferenceKind};
use crate::{Cancellation, Code, Diagnostic, DiagnosticReason, LimitKind, Limits, Value};
use std::collections::HashSet;

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
enum Task {
    Visit(NodeKey, usize, usize, Option<NodeKey>),
    Exit(NodeKey),
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
        stack.push(Task::Visit(node, 0, 0, None));
    }
    let mut active = HashSet::new();
    let mut provenance = Vec::new();
    let mut result = None;
    while let Some(task) = stack.pop() {
        let (node, depth, refs, incoming) = match task {
            Task::Exit(node) => {
                active.remove(&node);
                continue;
            }
            Task::Visit(node, d, r, incoming) => (node, d, r, incoming),
        };
        let value = graph.value(node);
        work.charge(1, &value)?;
        bound(
            0,
            depth,
            work.limits.schema_fact_depth,
            LimitKind::SchemaFactDepth,
        )
        .map_err(|e| value.locate(e))?;
        if !active.insert(node) {
            return Ok(indeterminate(
                SchemaReason::RecursiveFact,
                incoming.unwrap_or(node),
            ));
        }
        if let Err(error) = graph.schemas.check(node, graph) {
            return failure(error);
        }
        let profile = &graph.schemas.scope(node).expect("checked").profile;
        work.charge(1, &value)?;
        stack.push(Task::Exit(node));
        if value.as_bool() == Some(false) {
            return Ok(indeterminate(SchemaReason::ProhibitingSchema, node));
        }
        if value.as_bool() == Some(true) {
            continue;
        }
        let mut has_reference = false;
        for keyword in ["$dynamicRef", "$ref"] {
            if keyword == "$dynamicRef" && !profile.modern() {
                continue;
            }
            if let Some(edge) = value.get(keyword) {
                has_reference = true;
                work.charge(1, &edge)?;
                bound(
                    0,
                    refs.saturating_add(1),
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
                stack.push(Task::Visit(
                    target.target,
                    depth.saturating_add(1),
                    refs + 1,
                    Some(key(&edge)),
                ));
            }
        }
        if has_reference && !profile.modern() {
            continue;
        }
        if let Some(applicator) = ["allOf", "anyOf", "oneOf", "not", "if", "then", "else"]
            .iter()
            .find_map(|name| value.get(name))
        {
            return Ok(indeterminate(SchemaReason::Composition, key(&applicator)));
        }
        let local = local_fact(&value, profile, &query, work)?;
        match local {
            Fact::Known {
                value,
                provenance: sites,
            } => {
                provenance.extend(sites);
                match merge(result, value) {
                    Ok(value) => result = Some(value),
                    Err(reason) => {
                        return Ok(Fact::Indeterminate {
                            reason,
                            sites: provenance,
                        });
                    }
                }
            }
            Fact::Absent { provenance: sites } => {
                provenance.extend(sites);
            }
            other => return Ok(other),
        }
    }
    Ok(match result {
        Some(value) => Fact::Known { value, provenance },
        None => Fact::Absent { provenance },
    })
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
fn merge(previous: Option<FactValue>, next: FactValue) -> Result<FactValue, SchemaReason> {
    let Some(previous) = previous else {
        return Ok(next);
    };
    if previous == next {
        return Ok(previous);
    }
    Ok(match (previous, next) {
        (FactValue::Selection(mut a), FactValue::Selection(b)) => {
            a.0.extend(b.0);
            FactValue::Selection(a)
        }
        (FactValue::Types(a), FactValue::Types(b)) => {
            let common: Vec<_> = a.into_iter().filter(|x| b.contains(x)).collect();
            if common.is_empty() {
                return Err(SchemaReason::ConflictingFacts);
            }
            FactValue::Types(common)
        }
        (FactValue::Items(Items::Homogeneous(mut a)), FactValue::Items(Items::Homogeneous(b))) => {
            a.0.extend(b.0);
            FactValue::Items(Items::Homogeneous(a))
        }
        (FactValue::Shape(mut a), FactValue::Shape(b)) => {
            let mut known: HashSet<String> = a.names.iter().cloned().collect();
            for name in b.names {
                if known.insert(name.clone()) {
                    a.names.push(name);
                }
            }
            a.open &= b.open;
            a.incomplete |= b.incomplete;
            FactValue::Shape(a)
        }
        _ => return Err(SchemaReason::ConflictingFacts),
    })
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
