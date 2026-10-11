//! Source-only selected form descriptions. All semantic queries use the one
//! indexed graph and the caller's shared, call-scoped fact budget.
use crate::graph::{Graph, NodeKey, key};
use crate::prepare::{BodyDeclaration, DescriptorWork, FieldDeclarations, MediaDeclaration};
use crate::schema_facts::{
    self, Fact, FactQuery, FactValue, FactWork, SchemaSelection, SchemaType,
};
use crate::{
    Code, Diagnostic, Edition, FieldRequirement, FieldShapeIssue, FieldShapeReason, Operation,
    SchemaProfile, SourcePosition, Value,
};

/// Original schema location and actual lexical interpretation; no flattened schema.
#[derive(Clone, Debug)]
#[non_exhaustive]
pub struct SchemaDescription {
    /// Original source node.
    pub value: Value,
    /// Indexed lexical vocabulary, including an explicitly opaque dialect.
    pub profile: SchemaProfile,
}
/// Whether a supported static object shape permits additional names.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum FieldOpenness {
    /// Additional names are not prohibited by the supported static declarations.
    Open,
    /// A closed-schema hint; preparation does not enforce schema validation.
    ClosedHint,
    /// Static inspection cannot establish openness.
    Unknown,
}
/// Facts applicable to one selected field representation.
#[derive(Clone)]
#[non_exhaustive]
pub struct FormRepresentationDescription {
    /// Ordered Encoding content types or the one inferred default.
    pub media_types: Vec<String>,
    /// Applicable effective coding. None means proved absence for content-based
    /// paths, or inapplicability for an explicit style; it is not a raw-source claim.
    pub content_encoding: Option<String>,
    /// Source-only contributing declaration positions.
    pub sources: Vec<SourcePosition>,
}
/// Selected or directly inventoried logical field sources.
#[derive(Clone)]
#[non_exhaustive]
pub struct BodyFieldDescription {
    /// Exact decoded logical name.
    pub name: String,
    /// Protocol obligation or an inspection-only schema hint.
    pub requirement: FieldRequirement,
    /// Original contributing roots. Partial inventories retain only directly
    /// authored applicable roots, not a complete effective contributor set.
    pub schemas: Vec<SchemaDescription>,
    /// Original declaration establishing the selection or inventory.
    pub authored: Value,
    /// Field-specific original Encoding Object.
    pub encoding: Option<Value>,
    /// None means representation facts were not queried.
    pub representation: Option<FormRepresentationDescription>,
}
/// Completeness of a shallow logical field inventory, not instance validation.
#[derive(Clone, Debug)]
#[non_exhaustive]
pub enum BodyFields {
    /// Not a form representation.
    NotApplicable,
    /// Complete supported static inventory.
    Declared {
        /// Fields in declaration/contributor order.
        fields: Vec<BodyFieldDescription>,
        /// Static additional-name hint.
        openness: FieldOpenness,
    },
    /// No useful supported field inventory could be established.
    Opaque(FieldShapeIssue),
    /// Incomplete directly authored source inventory. Selected fact queries may
    /// still refuse; these roots are not a successful effective Property query.
    Partial {
        /// Direct applicable fields only.
        fields: Vec<BodyFieldDescription>,
        /// Unknown if the omitted semantics can change openness.
        openness: FieldOpenness,
        /// Source of the uninspected remainder.
        issue: FieldShapeIssue,
    },
}
/// One representation retaining source-only declarations and optional field inventory.
#[derive(Clone)]
#[non_exhaustive]
pub struct MediaDescription {
    /// Effective media type or range.
    pub media_type: String,
    /// Original declaration.
    pub authored: Value,
    /// Original schema use site.
    pub schema: Option<SchemaDescription>,
    /// Opaque Encoding Objects.
    pub encoding: Option<Value>,
    /// OAS 3.2 sequential-item use site, if applicable.
    pub item_schema: Option<SchemaDescription>,
    /// Named form inventory.
    pub fields: BodyFields,
    /// OAS 3.2 positional encodings.
    pub prefix_encoding: Option<Value>,
    /// OAS 3.2 repeated encoding.
    pub item_encoding: Option<Value>,
}
/// Effective body declaration with original reference provenance.
#[derive(Clone, Debug)]
#[non_exhaustive]
pub struct RequestBodyDescription {
    /// Original wrapper, target and reference chain.
    pub declaration: crate::ResolvedReference,
    /// Whether omission is permitted.
    pub required: bool,
    /// Effective representations.
    pub media: Vec<MediaDescription>,
    /// No representation constraint was authored.
    pub media_unrestricted: bool,
}
macro_rules! safe_debug {
    ($($t:ty),*) => {$(impl std::fmt::Debug for $t {
        fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result { f.write_str(concat!(stringify!($t), " { .. }")) }
    })*};
}
safe_debug!(
    FormRepresentationDescription,
    BodyFieldDescription,
    MediaDescription
);

pub(crate) fn root_selection(media: &MediaDeclaration) -> SchemaSelection {
    SchemaSelection(media.schema.iter().map(|s| key(&s.value)).collect())
}
pub(crate) fn fact(
    graph: &Graph,
    selection: &SchemaSelection,
    query: FactQuery<'_>,
    work: &mut FactWork<'_>,
    source: &Value,
) -> Result<(Option<FactValue>, Vec<NodeKey>), Diagnostic> {
    fact_result(
        graph,
        schema_facts::query(graph, selection, query, work)?,
        source,
    )
}
fn fact_result(
    graph: &Graph,
    result: Fact<FactValue>,
    source: &Value,
) -> Result<(Option<FactValue>, Vec<NodeKey>), Diagnostic> {
    match result {
        Fact::Known { value, provenance } => Ok((Some(value), provenance)),
        Fact::Absent { provenance } => Ok((None, provenance)),
        Fact::Need(need) => {
            let site = graph
                .resource(need.source)
                .expect("need source")
                .json
                .root()
                .pointer(&need.pointer)
                .expect("need site");
            let code = match need.need {
                crate::ResourceNeedKind::Bytes => Code::ResourceUnavailable,
                crate::ResourceNeedKind::Layout => Code::ResourceLayoutRequired,
            };
            Err(site.error(code).need_fact(need))
        }
        Fact::Indeterminate { reason, sites } => {
            let site = sites
                .first()
                .map(|n| graph.value(*n))
                .unwrap_or_else(|| source.clone());
            Err(crate::schema::schema_error(
                &site,
                Code::UnsupportedSchema,
                reason,
            ))
        }
        Fact::Invalid(error) => Err(error),
    }
}
pub(crate) fn selected_property(
    graph: &Graph,
    roots: &SchemaSelection,
    name: &str,
    facts: &mut FactWork<'_>,
    source: &Value,
) -> Result<SchemaSelection, Diagnostic> {
    match fact(graph, roots, FactQuery::Property(name), facts, source)?.0 {
        Some(FactValue::Selection(s)) => Ok(s),
        None => Ok(SchemaSelection(vec![])),
        _ => unreachable!("property query"),
    }
}
pub(crate) fn selected_items(
    graph: &Graph,
    roots: &SchemaSelection,
    facts: &mut FactWork<'_>,
    source: &Value,
) -> Result<SchemaSelection, Diagnostic> {
    match fact(graph, roots, FactQuery::Items, facts, source)?.0 {
        Some(FactValue::Items(schema_facts::Items::Homogeneous(s))) => Ok(s),
        None => Ok(SchemaSelection(vec![])),
        Some(_) => {
            Err(source
                .error(Code::UnsupportedMedia)
                .reasoned(crate::DiagnosticReason::Form(
                    crate::FormReason::StructuredMultipart,
                )))
        }
    }
}
pub(crate) fn effective_coding(
    graph: &Graph,
    roots: &SchemaSelection,
    facts: &mut FactWork<'_>,
    source: &Value,
) -> Result<(Option<String>, Vec<NodeKey>), Diagnostic> {
    let (value, sites) = fact(graph, roots, FactQuery::EffectiveEncoding, facts, source)?;
    Ok((
        match value {
            Some(FactValue::Text(s)) => Some(s),
            None => None,
            _ => unreachable!("coding query"),
        },
        sites,
    ))
}
pub(crate) fn default_media(
    graph: &Graph,
    roots: &SchemaSelection,
    facts: &mut FactWork<'_>,
    source: &Value,
) -> Result<(&'static str, Vec<NodeKey>), Diagnostic> {
    let (value, mut sites) = fact(graph, roots, FactQuery::Types, facts, source)?;
    let Some(FactValue::Types(types)) = value else {
        return Ok(("application/octet-stream", sites));
    };
    let needs_legacy_format = types.contains(&SchemaType::String);
    let mut result = None;
    for ty in types {
        let media = match ty {
            SchemaType::String | SchemaType::Boolean | SchemaType::Number | SchemaType::Integer => {
                "text/plain"
            }
            SchemaType::Object | SchemaType::Array => "application/json",
            SchemaType::Null => {
                return Err(crate::schema::schema_error(
                    source,
                    Code::UnsupportedSchema,
                    crate::SchemaReason::ConflictingFacts,
                ));
            }
        };
        if result.is_some_and(|previous| previous != media) {
            return Err(crate::schema::schema_error(
                source,
                Code::UnsupportedSchema,
                crate::SchemaReason::ConflictingFacts,
            ));
        }
        result = Some(media);
    }
    // Legacy binary/byte only applies in the lexical OAS3.0 profile, including refs.
    let (binary, legacy_sites) = if needs_legacy_format {
        fact(graph, roots, FactQuery::LegacyBinary, facts, source)?
    } else {
        (None, vec![])
    };
    let media = if binary.is_some() {
        "application/octet-stream"
    } else {
        result.unwrap_or("application/octet-stream")
    };
    facts.charge(legacy_sites.len(), source)?;
    sites.extend(legacy_sites);
    Ok((media, sites))
}
impl Operation {
    pub(crate) fn describe_body(
        &self,
        body: BodyDeclaration,
        work: &mut DescriptorWork<'_>,
        facts: &mut FactWork<'_>,
    ) -> Result<RequestBodyDescription, Diagnostic> {
        Ok(RequestBodyDescription {
            declaration: body.declaration,
            required: body.required,
            media: body
                .media
                .into_iter()
                .map(|m| self.describe_media(m, work, facts))
                .collect::<Result<_, _>>()?,
            media_unrestricted: body.media_unrestricted,
        })
    }
    pub(crate) fn describe_media(
        &self,
        media: MediaDeclaration,
        work: &mut DescriptorWork<'_>,
        facts: &mut FactWork<'_>,
    ) -> Result<MediaDescription, Diagnostic> {
        let fields = self.describe_inventory(&media, work, facts)?;
        Ok(MediaDescription {
            media_type: media.media_type,
            authored: media.authored,
            schema: media.schema.map(|s| s.into_description()),
            encoding: media.encoding,
            item_schema: media.item_schema.map(|s| s.into_description()),
            fields,
            prefix_encoding: media.prefix_encoding,
            item_encoding: media.item_encoding,
        })
    }
    fn field_sources(
        &self,
        selection: &SchemaSelection,
        work: &mut DescriptorWork<'_>,
    ) -> Result<Vec<SchemaDescription>, Diagnostic> {
        selection
            .0
            .iter()
            .map(|n| {
                self.schema_description(self.store.graph.value(*n), work)
                    .map(|d| d.into_description())
            })
            .collect()
    }
    fn hint(
        &self,
        roots: &SchemaSelection,
        name: &str,
        facts: &mut FactWork<'_>,
        source: &Value,
    ) -> Result<FieldRequirement, Diagnostic> {
        let graph = &self.store.graph;
        match schema_facts::query(graph, roots, FactQuery::RequiredHint(name), facts)? {
            Fact::Known {
                value: FactValue::RequiredHint,
                ..
            } => Ok(FieldRequirement::SchemaRequired),
            Fact::Absent { .. } => Ok(FieldRequirement::Optional),
            Fact::Indeterminate { .. } => Ok(FieldRequirement::UnknownHint),
            Fact::Invalid(e) => Err(e),
            Fact::Need(need) => {
                let site = graph
                    .resource(need.source)
                    .expect("need source")
                    .json
                    .root()
                    .pointer(&need.pointer)
                    .expect("need source");
                Err(site
                    .error(if need.need == crate::ResourceNeedKind::Bytes {
                        Code::ResourceUnavailable
                    } else {
                        Code::ResourceLayoutRequired
                    })
                    .need_fact(need))
            }
            Fact::Known { .. } => Err(source.error(Code::UnsupportedSchema)),
        }
    }
    fn describe_inventory(
        &self,
        media: &MediaDeclaration,
        work: &mut DescriptorWork<'_>,
        facts: &mut FactWork<'_>,
    ) -> Result<BodyFields, Diagnostic> {
        let parsed = crate::prepare::parse_media(&media.media_type, true)?;
        if !parsed.essence.starts_with("multipart/")
            && parsed.essence != "application/x-www-form-urlencoded"
        {
            return Ok(BodyFields::NotApplicable);
        }
        if let FieldDeclarations::Declared(fields) = &media.fields {
            let mut output = Vec::new();
            for f in fields {
                work.entry(&f.authored)?;
                output.push(BodyFieldDescription {
                    name: work.text(&f.authored, &f.name)?,
                    requirement: f.requirement,
                    schemas: vec![f.schema.describe()],
                    authored: f.authored.clone(),
                    encoding: f.encoding.clone(),
                    representation: None,
                });
            }
            return Ok(BodyFields::Declared {
                fields: output,
                openness: FieldOpenness::ClosedHint,
            });
        }
        let Some(schema) = media.schema.as_ref() else {
            return Ok(BodyFields::Opaque(FieldShapeIssue {
                reason: FieldShapeReason::MissingSchema,
                source: media.authored.clone(),
            }));
        };
        if media.prefix_encoding.is_some() || media.item_encoding.is_some() {
            return Ok(BodyFields::Opaque(FieldShapeIssue {
                reason: FieldShapeReason::PositionalEncoding,
                source: media.authored.clone(),
            }));
        }
        let roots = root_selection(media);
        let shape = schema_facts::query(&self.store.graph, &roots, FactQuery::ObjectShape, facts)?;
        let (names, openness) = match shape {
            Fact::Known {
                value: FactValue::Shape(shape),
                ..
            } if !shape.incomplete => (
                shape.names,
                if shape.open {
                    FieldOpenness::Open
                } else {
                    FieldOpenness::ClosedHint
                },
            ),
            Fact::Known { .. } => {
                return self.partial_inventory(media, FieldShapeReason::SchemaShape, work);
            }
            Fact::Indeterminate { reason, .. } => {
                return self.partial_inventory(
                    media,
                    if reason == crate::SchemaReason::Composition {
                        FieldShapeReason::Composition
                    } else {
                        FieldShapeReason::SchemaShape
                    },
                    work,
                );
            }
            other => {
                // Preserve actual needs and malformed declarations with the same conversion.
                fact_result(&self.store.graph, other, &schema.value)?;
                (vec![], FieldOpenness::Open)
            }
        };
        let mut fields = Vec::new();
        for name in names {
            work.entry(&schema.value)?;
            let selection =
                selected_property(&self.store.graph, &roots, &name, facts, &schema.value)?;
            let authored = selection
                .0
                .first()
                .map(|n| self.store.graph.value(*n))
                .unwrap_or_else(|| schema.value.clone());
            let requirement = self.hint(&roots, &name, facts, &schema.value)?;
            fields.push(BodyFieldDescription {
                name,
                requirement,
                schemas: self.field_sources(&selection, work)?,
                authored,
                encoding: None,
                representation: None,
            });
            let last = fields.last_mut().unwrap();
            last.encoding = media.encoding.as_ref().and_then(|e| e.get(&last.name));
        }
        Ok(BodyFields::Declared { fields, openness })
    }
    fn partial_inventory(
        &self,
        media: &MediaDeclaration,
        reason: FieldShapeReason,
        work: &mut DescriptorWork<'_>,
    ) -> Result<BodyFields, Diagnostic> {
        let schema = media.schema.as_ref().expect("schema checked");
        let mut fields = Vec::new();
        if !(schema.value.get("$ref").is_some() && !schema.profile.modern())
            && let Some(properties) = schema.value.get("properties")
        {
            for (name, property) in properties
                .members()
                .ok_or_else(|| properties.error(Code::InvalidDeclaration))?
            {
                work.declaration(&property)?;
                if self.store.graph.schemas.scope(key(&property)).is_none() {
                    continue;
                }
                fields.push(BodyFieldDescription {
                    name: work.text(&property, name)?,
                    requirement: FieldRequirement::UnknownHint,
                    schemas: vec![
                        self.schema_description(property.clone(), work)?
                            .into_description(),
                    ],
                    authored: property,
                    encoding: media.encoding.as_ref().and_then(|e| e.get(name)),
                    representation: None,
                });
            }
        }
        Ok(BodyFields::Partial {
            fields,
            openness: FieldOpenness::Unknown,
            issue: FieldShapeIssue {
                reason,
                source: schema.value.clone(),
            },
        })
    }
    /// Inspect only one form field's applicable default representation and schema sources.
    /// This does not compile credentials/responses or inspect unrelated field schemas.
    pub fn describe_form_field(
        &self,
        media_type: &str,
        name: &str,
    ) -> Result<BodyFieldDescription, Diagnostic> {
        self.describe_form_field_with_cancellation(
            media_type,
            name,
            &crate::Cancellation::default(),
        )
    }
    /// Selected form inspection with the same typed resource recovery as preparation.
    pub fn describe_form_field_with_cancellation(
        &self,
        media_type: &str,
        name: &str,
        cancellation: &crate::Cancellation,
    ) -> Result<BodyFieldDescription, Diagnostic> {
        self.describe_form_field_inner(media_type, name, cancellation)
            .map_err(|e| e.bind_graph(&self.store))
    }
    fn describe_form_field_inner(
        &self,
        media_type: &str,
        name: &str,
        cancellation: &crate::Cancellation,
    ) -> Result<BodyFieldDescription, Diagnostic> {
        crate::schema::checkpoint(cancellation)?;
        let mut work = DescriptorWork::with_cancellation(&self.store.limits, cancellation);
        let mut facts = FactWork::new(&self.store.limits, cancellation);
        let body = self
            .body_description(&mut work)?
            .ok_or_else(|| self.value().error(Code::InvalidSelection))?;
        let selected = crate::prepare::parse_media(media_type, false)?;
        if !matches!(
            selected.essence.as_str(),
            "multipart/form-data" | "application/x-www-form-urlencoded"
        ) {
            return Err(self.value().error(Code::UnsupportedMedia));
        }
        let mut matching = body
            .media
            .iter()
            .filter_map(|m| {
                crate::prepare::parse_media(&m.media_type, true)
                    .ok()
                    .and_then(|key| {
                        crate::prepare::media_matches(&key, &selected).map(|rank| (rank, m))
                    })
            })
            .collect::<Vec<_>>();
        matching.sort_by_key(|(rank, _)| *rank);
        let Some((rank, media)) = matching.pop() else {
            return Err(self.value().error(Code::InvalidSelection));
        };
        if matching
            .last()
            .is_some_and(|(previous, _)| previous == &rank)
        {
            return Err(media.authored.error(Code::AmbiguousSelection));
        }
        let edition = self.store.edition.clone()?;
        let encoding = media.encoding.as_ref().and_then(|e| e.get(name));
        let styled = crate::form::style(
            encoding.as_ref(),
            edition,
            selected.essence == "multipart/form-data",
        )?
        .is_some();
        let roots = root_selection(media);
        let legacy = if let FieldDeclarations::Declared(fields) = &media.fields {
            fields.iter().find(|f| f.name == name)
        } else {
            None
        };
        if edition == Edition::Swagger20 && legacy.is_none() {
            return Err(media.authored.error(Code::InvalidSelection).reasoned(
                crate::DiagnosticReason::Form(crate::FormReason::UnknownField),
            ));
        }
        let selection = if styled {
            SchemaSelection(vec![])
        } else {
            selected_property(&self.store.graph, &roots, name, &mut facts, &media.authored)?
        };
        let authored = legacy
            .map(|f| f.authored.clone())
            .or_else(|| selection.0.first().map(|n| self.store.graph.value(*n)))
            .unwrap_or_else(|| media.authored.clone());
        let requirement = if let Some(f) = legacy {
            f.requirement
        } else {
            self.hint(&roots, name, &mut facts, &media.authored)?
        };
        let schemas = if let Some(f) = legacy {
            vec![f.schema.describe()]
        } else if styled {
            // Styles don't demand selected property semantics. Retain only an indexed
            // direct local source when it is applicable, never follow raw references.
            match media
                .schema
                .as_ref()
                .filter(|s| s.profile.modern() || s.value.get("$ref").is_none())
                .and_then(|s| s.value.get("properties"))
                .and_then(|p| p.get(name))
            {
                Some(v) if self.store.graph.schemas.scope(key(&v)).is_some() => {
                    vec![self.schema_description(v, &mut work)?.into_description()]
                }
                _ => vec![],
            }
        } else {
            self.field_sources(&selection, &mut work)?
        };
        let representation = crate::form::describe_representation(
            &self.store.graph,
            &selection,
            legacy.map(|f| &f.schema.value),
            encoding.as_ref(),
            &authored,
            edition,
            selected.essence == "multipart/form-data",
            &mut work,
            &mut facts,
        )?;
        Ok(BodyFieldDescription {
            name: work.text(&authored, name)?,
            requirement,
            schemas,
            authored,
            encoding,
            representation: Some(representation),
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::*;
    use serde_json::json;
    #[test]
    fn frozen_required_profile_syntax_twins() {
        let packet: serde_json::Value =
            serde_json::from_str(include_str!("../tests/fixtures/g2b/required-syntax.json"))
                .unwrap();
        for case in packet["controls"].as_array().unwrap() {
            let edition = case["edition"].as_str().unwrap();
            let spec = if edition == "2.0" {
                json!({"swagger":edition,"paths":{},"definitions":{"Root":case["schema"],"Base":case["referenced_Base"]}})
            } else {
                json!({"openapi":edition,"paths":{},"components":{"schemas":{"Root":case["schema"],"Base":case["referenced_Base"]}}})
            };
            let doc = Document::parse(spec.to_string(), None, Limits::default()).unwrap();
            let pointer = if edition == "2.0" {
                "/definitions/Root"
            } else {
                "/components/schemas/Root"
            };
            let schema = doc.root().pointer(pointer).unwrap();
            let cancel = Cancellation::default();
            let limits = Limits::default();
            let mut work = FactWork::new(&limits, &cancel);
            let result = fact(
                &doc.0.graph,
                &SchemaSelection(vec![key(&schema)]),
                FactQuery::RequiredHint("p"),
                &mut work,
                &schema,
            );
            match case["expected_requested_hint"].as_str().unwrap() {
                "SchemaRequired" => assert!(
                    matches!(result.unwrap().0, Some(FactValue::RequiredHint)),
                    "{}",
                    case["id"]
                ),
                "Optional" => assert!(result.unwrap().0.is_none(), "{}", case["id"]),
                _ => {
                    let e = result.unwrap_err();
                    assert_eq!(e.code(), Code::InvalidDeclaration, "{}", case["id"]);
                    assert_eq!(
                        e.reason(),
                        Some(&DiagnosticReason::Schema(SchemaReason::InvalidKeyword))
                    );
                }
            }
        }
    }
    #[test]
    fn completed_required_membership_retains_only_positive_contributors() {
        let doc=Document::parse(br##"{"openapi":"3.1.2","paths":{},"components":{"schemas":{"R":{"$ref":"#/components/schemas/B","required":["p"]},"B":{"required":["q"]}}}}"##,None,Limits::default()).unwrap();
        let schema = doc.root().pointer("/components/schemas/R").unwrap();
        let cancel = Cancellation::default();
        let limits = Limits::default();
        let mut work = FactWork::new(&limits, &cancel);
        let (value, sites) = fact(
            &doc.0.graph,
            &SchemaSelection(vec![key(&schema)]),
            FactQuery::RequiredHint("p"),
            &mut work,
            &schema,
        )
        .unwrap();
        assert_eq!(value, Some(FactValue::RequiredHint));
        assert_eq!(sites.len(), 1);
        assert_eq!(
            doc.0.graph.value(sites[0]).location().pointer,
            "/components/schemas/R/required"
        );
    }
    #[test]
    fn escaped_successful_descriptors_and_form_do_not_retain_store() {
        let doc=Document::parse(br#"{"openapi":"3.1.2","servers":[{"url":"https://api.example"}],"paths":{"/send":{"post":{"operationId":"send","requestBody":{"content":{"application/x-www-form-urlencoded":{"schema":{"type":"object","properties":{"SECRET-NAME":{"type":"string"}}}}}}}}}}"#,None,Limits::default()).unwrap();
        let weak = std::sync::Arc::downgrade(&doc.0);
        let op = doc.operation_id("send").unwrap();
        let field = op
            .describe_form_field("application/x-www-form-urlencoded", "SECRET-NAME")
            .unwrap();
        let all = op.describe().unwrap();
        let form = Form::new(
            [FormField::new("SECRET-NAME", "SECRET-VALUE")],
            Limits::default(),
        )
        .unwrap();
        let request = op
            .prepare(&Input {
                body: Body::Form(form.clone()),
                ..Input::default()
            })
            .unwrap();
        drop(request);
        drop(op);
        drop(doc);
        assert!(weak.upgrade().is_none());
        assert_eq!(field.name, "SECRET-NAME");
        assert!(field.schemas[0].value.raw().contains("string"));
        assert!(all.request_body.is_some());
        assert_eq!(form.len(), 1);
        assert!(!format!("{field:?} {form:?}").contains("SECRET"));
    }
    #[test]
    fn partial_descriptors_drop_store_and_cancelled_copy_publishes_no_form() {
        let doc=Document::parse(br#"{"openapi":"3.1.2","paths":{"/send":{"post":{"operationId":"send","requestBody":{"content":{"application/x-www-form-urlencoded":{"schema":{"properties":{"p":{"type":"string"}},"allOf":[{}]}}}}}}}}"#,None,Limits::default()).unwrap();
        let weak = std::sync::Arc::downgrade(&doc.0);
        let description = doc.operation_id("send").unwrap().describe().unwrap();
        drop(doc);
        assert!(weak.upgrade().is_none());
        assert!(matches!(
            description.request_body.unwrap().media[0].fields,
            BodyFields::Partial { .. }
        ));
        let cancel = Cancellation::default();
        let _guard = crate::schema::cancel_after_for_test(5);
        let bytes = vec![7_u8; 131072];
        assert_eq!(
            Form::new_with_cancellation(
                [FormField::new("file", Part::bytes(bytes.as_slice()))],
                Limits::default(),
                &cancel
            )
            .unwrap_err()
            .diagnostic()
            .code(),
            Code::Cancelled
        );
    }
}
