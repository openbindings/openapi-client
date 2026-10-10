//! Author contract checks. Frozen literals are copied without changing expectations.
use super::*;
use crate::schema_facts::{Fact, FactQuery, FactValue, FactWork, SchemaSelection, query};
use serde_json::{Value as Json, json};
use std::{collections::HashMap, sync::Arc};

#[test]
fn frozen_nullable_declaration_controls() {
    let controls: Json = serde_json::from_str(include_str!(
        "../tests/fixtures/schema-nullable-controls.json"
    ))
    .unwrap();
    for case in controls["cases"].as_array().unwrap() {
        let doc = Document::parse(case["document"].to_string(), None, Limits::default()).unwrap();
        let selection = SchemaSelection(
            case["selection"]
                .as_array()
                .unwrap()
                .iter()
                .map(|p| {
                    doc.entry_resource()
                        .schema_at(p.as_str().unwrap())
                        .unwrap()
                        .key
                })
                .collect(),
        );
        let token = Cancellation::default();
        let mut work = FactWork::new(&doc.0.limits, &token);
        let fact = query(&doc.0.graph, &selection, FactQuery::Types, &mut work).unwrap();
        let mut actual = fact_json(fact, &doc.0.graph);
        if actual["kind"] == "Known" {
            actual["types"] = actual["value"].clone();
        }
        for (key, expected) in case["expect"].as_object().unwrap() {
            assert_eq!(&actual[key], expected, "{}: {key}", case["id"]);
        }
    }
}

fn contract() -> Json {
    serde_json::from_str(include_str!("../tests/fixtures/schema-contract.json")).unwrap()
}
fn profile(uri: &str) -> SchemaProfile {
    SchemaProfile::JsonSchema {
        dialect_uri: uri.into(),
    }
}
fn input(fixture: &Json) -> ResourceInput {
    let bytes: Arc<[u8]> = Arc::from(serde_json::to_vec(&fixture["document"]).unwrap());
    let mut input = if let Some(uri) = fixture["retrieval"].as_str() {
        ResourceInput::at(uri, bytes).unwrap()
    } else {
        ResourceInput::anonymous(bytes)
    };
    if let Some(layout) = fixture.get("layout") {
        let mut roots = ResourceLayout::new();
        for root in layout.as_array().unwrap() {
            roots = roots.schema(
                root["schema_root"].as_str().unwrap(),
                profile(
                    root["profile"]
                        .as_str()
                        .or(root["profile"]["JsonSchema"].as_str())
                        .unwrap(),
                ),
            );
        }
        input = input.layout(roots);
    }
    input
}
fn build(
    resources: &[Json],
    fixtures: &Json,
) -> Result<(Document, HashMap<String, ResourceId>), Diagnostic> {
    let mut builder = DocumentBuilder::new(Limits::default(), GraphLimits::default());
    let mut ids = HashMap::new();
    for resource in resources {
        let name = resource.as_str().unwrap();
        ids.insert(
            name.to_owned(),
            builder.add(input(&fixtures[name])).unwrap(),
        );
    }
    let entry = ids[resources[0].as_str().unwrap()];
    Ok((builder.build(entry)?, ids))
}
fn handle(doc: &Document, id: ResourceId, pointer: &str) -> Result<SchemaHandle, Diagnostic> {
    ResourceHandle {
        store: doc.0.clone(),
        id,
    }
    .schema_at(pointer)
}
fn fact_json(fact: Fact<FactValue>, graph: &crate::graph::Graph) -> Json {
    match fact {
        Fact::Known { value, provenance } => {
            json!({"kind":"Known", "source_pointers":provenance.into_iter().map(|k|graph.value(k).location().pointer).collect::<Vec<_>>(), "value": match value {
                FactValue::Text(text) => json!(text),
                FactValue::Types(types) => json!(types.into_iter().map(|t| format!("{t:?}").to_lowercase()).collect::<Vec<_>>()),
                FactValue::Selection(s) => json!({"schema_pointers": s.0.into_iter().map(|k| graph.value(k).location().pointer).collect::<Vec<_>>() }),
                FactValue::Items(crate::schema_facts::Items::Homogeneous(s)) => json!({"kind":"Homogeneous", "schema_pointer": graph.value(s.0[0]).location().pointer}),
                FactValue::Items(crate::schema_facts::Items::Positional(s)) => json!({"kind":"Positional", "prefix_pointers": s.into_iter().map(|s| graph.value(s.0[0]).location().pointer).collect::<Vec<_>>(), "tail":"Absent"}),
                FactValue::RequiredHint => json!(true),
                FactValue::Shape(s) => json!({"named":s.names, "openness":if s.open {"Open"} else {"ClosedHint"}}),
            }})
        }
        Fact::Absent { provenance } => {
            json!({"kind":"Absent","source_pointers":provenance.into_iter().map(|k|graph.value(k).location().pointer).collect::<Vec<_>>()})
        }
        Fact::Need(need) => {
            json!({"kind":"Need", "code":if need.need == ResourceNeedKind::Bytes {"ResourceUnavailable"} else {"ResourceLayoutRequired"}, "action":format!("{:?}",need.need), "requested_uri":need.requested_uri, "source_pointer":need.pointer,"target_resource_id":need.target.map(|id|id.0),
            "fragment":match need.fragment {ResourceFragment::Root=>json!({"Root":null}),ResourceFragment::Pointer(p)=>json!({"Pointer":p}),ResourceFragment::Anchor(a)=>json!({"Anchor":a})},
            "interpretation":match need.interpretation {ResourceInterpretation::Schema{default_profile:SchemaProfile::JsonSchema{dialect_uri}}=>json!({"Schema":dialect_uri}),_=>Json::Null}})
        }
        Fact::Indeterminate { reason, sites } => {
            json!({"kind":"Indeterminate", "reason":format!("{reason:?}"),"source_pointers":sites.into_iter().map(|k|graph.value(k).location().pointer).collect::<Vec<_>>()})
        }
        Fact::Invalid(error) => diagnostic_json(&error, "Invalid"),
    }
}
fn diagnostic_json(error: &Diagnostic, kind: &str) -> Json {
    json!({"kind":kind,"code":format!("{:?}",error.code()),"source_pointer":error.location().map(|l| &l.pointer),"target_pointer":error.related().first().map(|l| &l.pointer),"resource_need":error.resource_need().is_some(),"reason": match error.reason() {Some(DiagnosticReason::Schema(r))=>Some(format!("{r:?}")),_=>None}})
}
fn select_query(action: &str) -> FactQuery<'_> {
    match action {
        "Types" => FactQuery::Types,
        "ContentEncoding" => FactQuery::ContentEncoding,
        "ContentMediaType" => FactQuery::ContentMediaType,
        "LegacyFormat" => FactQuery::LegacyFormat,
        "ObjectShape" => FactQuery::ObjectShape,
        "Items" => FactQuery::Items,
        _ => FactQuery::Property(action.strip_prefix("Property:").unwrap()),
    }
}
fn run_fact(doc: &Document, schema: &SchemaHandle, action: &str) -> Json {
    let token = Cancellation::default();
    let mut work = FactWork::new(&doc.0.limits, &token);
    let mut selection = SchemaSelection(vec![schema.key]);
    let mut answer = None;
    for action in action.split('/') {
        let fact = query(&doc.0.graph, &selection, select_query(action), &mut work).unwrap();
        if let Fact::Known {
            value: FactValue::Selection(next),
            ..
        } = &fact
        {
            selection = next.clone();
        }
        answer = Some(fact);
    }
    let mut result = fact_json(answer.unwrap(), &doc.0.graph);
    if let Ok(mut edges) = schema.references()
        && let Some(edge) = edges.next()
        && let Ok(target) = edge.resolve_static()
    {
        result["target_pointer"] = json!(target.target().authored().location().pointer);
        result["requires_dynamic_scope"] = json!(target.requires_dynamic_scope());
    }
    result
}
#[test]
fn frozen_literal_contract() {
    let contract = contract();
    let fixtures = &contract["fixtures"];
    let mut executed = 0;
    for case in contract["cases"].as_array().unwrap() {
        let Some(resources) = case["resources"].as_array() else {
            continue;
        };
        if case["slice"] != "G2a" {
            continue;
        }
        let name = case["id"].as_str().unwrap();
        let action = case["action"].as_str().unwrap();
        let built = build(resources, fixtures);
        if action == "build" {
            let error = built.expect_err(name);
            assert_eq!(
                diagnostic_json(&error, "Diagnostic")["code"],
                case["expect"]["code"],
                "{name}"
            );
            executed += 1;
            continue;
        }
        let (doc, ids) = built.unwrap_or_else(|e| panic!("{name}: build {e:?}"));
        let site = &case["site"];
        let id = ids[site["resource"].as_str().unwrap()];
        let found = handle(&doc, id, site["pointer"].as_str().unwrap());
        let mut observed = if action == "schema_at" {
            diagnostic_json(&found.unwrap_err(), "Diagnostic")
        } else {
            let schema = found.unwrap_or_else(|e| panic!("{name}: site {e:?}"));
            if let Some(action) = action.strip_prefix("fact:") {
                run_fact(&doc, &schema, action)
            } else if action == "profile" {
                match schema.profile() {
                    SchemaProfile::JsonSchema { dialect_uri } => {
                        json!({"dialect":dialect_uri,"capability":format!("{:?}",schema.dialect_capability())})
                    }
                    _ => panic!("{name}"),
                }
            } else if action == "references" {
                diagnostic_json(&schema.references().err().expect(name), "Diagnostic")
            } else if action.starts_with("resolve_static:") {
                match schema
                    .references()
                    .unwrap()
                    .next()
                    .unwrap()
                    .resolve_static()
                {
                    Ok(resolved) => {
                        json!({"kind":"Resolved","target_resource":ids.iter().find(|(_,id)| **id==resolved.target().resource().id()).unwrap().0,"target_pointer":resolved.target().authored().location().pointer,"canonical":resolved.canonical(),"requires_dynamic_scope":resolved.requires_dynamic_scope(),"target_dialect":match resolved.target().profile(){SchemaProfile::JsonSchema{dialect_uri}=>Some(dialect_uri),_=>None}})
                    }
                    Err(error) => diagnostic_json(&error, "Diagnostic"),
                }
            } else if action == "identity_inventory" {
                let mut identities = doc
                    .0
                    .graph
                    .schemas
                    .identity_keys()
                    .cloned()
                    .collect::<Vec<_>>();
                if let Some(uri) = schema.schema_resource_uri() {
                    identities.push(uri.into());
                }
                identities.sort();
                identities.dedup();
                assert_eq!(identities, vec!["https://schemas.example/safe"]);
                assert_eq!(doc.0.graph.reference_count, 0);
                executed += 1;
                continue;
            } else if action == "compare_routes" {
                let a = schema
                    .references()
                    .unwrap()
                    .next()
                    .unwrap()
                    .resolve_static()
                    .unwrap();
                let b = handle(&doc, id, case["expect"]["other_pointer"].as_str().unwrap())
                    .unwrap()
                    .references()
                    .unwrap()
                    .next()
                    .unwrap()
                    .resolve_static()
                    .unwrap();
                assert_eq!(a.target().key, b.target().key);
                assert!(!a.canonical());
                assert!(b.canonical());
                assert_eq!(
                    run_fact(&doc, a.target(), "Property:amount/Types")["value"],
                    json!(["number"])
                );
                executed += 1;
                continue;
            } else if action == "inspect_and_resolve_self_edges" {
                assert_eq!(schema.effective_base(), case["expect"]["base"].as_str());
                assert_eq!(schema.resource_relative_pointer(), "");
                for (suffix, expected) in case["expect"]["self_targets"].as_object().unwrap() {
                    let child = handle(
                        &doc,
                        id,
                        &format!("{}{suffix}", site["pointer"].as_str().unwrap()),
                    )
                    .unwrap();
                    let target = child
                        .references()
                        .unwrap()
                        .next()
                        .unwrap()
                        .resolve_static()
                        .unwrap();
                    assert_eq!(
                        target.target().authored().location().pointer,
                        expected.as_str().unwrap()
                    );
                }
                executed += 1;
                continue;
            } else if action == "apply_layout_then_reacquire" {
                let error = schema
                    .references()
                    .unwrap()
                    .next()
                    .unwrap()
                    .resolve_static()
                    .unwrap_err();
                let need = error.resource_need().unwrap();
                let mut builder = DocumentBuilder::from_document(&doc);
                builder
                    .apply_layout(
                        need,
                        ResourceLayout::new().schema(
                            case["layout"]["schema_root"].as_str().unwrap(),
                            profile(case["layout"]["profile"].as_str().unwrap()),
                        ),
                    )
                    .unwrap();
                let next = builder.build(doc.entry_resource().id()).unwrap();
                assert_eq!(
                    schema
                        .references()
                        .unwrap()
                        .next()
                        .unwrap()
                        .resolve_static()
                        .unwrap_err()
                        .code(),
                    Code::ResourceLayoutRequired
                );
                let current = handle(&next, id, site["pointer"].as_str().unwrap()).unwrap();
                run_fact(&next, &current, "Types")
            } else {
                panic!("unhandled registered literal {name}: {action}")
            }
        };
        if let Some(target) = observed["target_resource_id"].as_u64() {
            observed["target_resource"] = json!(
                ids.iter()
                    .find(|(_, id)| id.0 == target as usize)
                    .unwrap()
                    .0
            );
        }
        for field in [
            "kind",
            "code",
            "resource_need",
            "fragment",
            "interpretation",
            "reason",
            "value",
            "dialect",
            "capability",
            "target_resource",
            "target_pointer",
            "canonical",
            "requires_dynamic_scope",
            "target_dialect",
            "requested_uri",
            "source_pointer",
            "action",
        ] {
            if let Some(expected) = case["expect"].get(field) {
                assert_eq!(
                    &observed[field], expected,
                    "{name} field {field}: {observed}"
                );
            }
        }
        if let Some(expected) = case["expect"]["source_pointers"].as_array() {
            let actual = observed["source_pointers"].as_array().expect(name);
            assert_eq!(actual, expected, "{name}: exact ordered contributors");
        }
        executed += 1;
    }
    assert_eq!(executed, 52);
}

const DRAFT: &str = "https://json-schema.org/draft/2020-12/schema";
#[test]
fn frozen_contributor_provenance_contract() {
    let controls: Json = serde_json::from_str(include_str!(
        "../tests/fixtures/schema-contributor-controls.json"
    ))
    .unwrap();
    let cases = controls["cases"].as_array().unwrap();
    assert_eq!(cases.len(), 20);
    for case in cases {
        let name = case["id"].as_str().unwrap();
        let mut builder = DocumentBuilder::new(Limits::default(), GraphLimits::default());
        let mut ids = HashMap::new();
        for resource in case["resources"].as_array().unwrap() {
            ids.insert(
                resource["name"].as_str().unwrap(),
                builder.add(input(resource)).unwrap(),
            );
        }
        let doc = builder.build(ids["entry"]).unwrap();
        let selection = SchemaSelection(
            case["selection"]
                .as_array()
                .unwrap()
                .iter()
                .map(|s| {
                    handle(
                        &doc,
                        ids[s["resource"].as_str().unwrap()],
                        s["pointer"].as_str().unwrap(),
                    )
                    .unwrap()
                    .key
                })
                .collect(),
        );
        let cancellation = Cancellation::default();
        let mut work = FactWork::new(&doc.0.limits, &cancellation);
        let fact = query(
            &doc.0.graph,
            &selection,
            select_query(case["query"].as_str().unwrap()),
            &mut work,
        )
        .unwrap();
        let mut observed = fact_json(fact.clone(), &doc.0.graph);
        let sites = match fact {
            Fact::Known { provenance, .. } | Fact::Absent { provenance } => provenance,
            Fact::Indeterminate { sites, .. } => sites,
            _ => panic!("{name}: unexpected {observed}"),
        };
        observed["source_sites"] = json!(
            sites
                .into_iter()
                .map(|key| {
                    json!({"resource": ids.iter().find(|(_, id)| id.0 == key.0).unwrap().0,
                "pointer": doc.0.graph.value(key).location().pointer})
                })
                .collect::<Vec<_>>()
        );
        for (field, expected) in case["expect"].as_object().unwrap() {
            assert_eq!(&observed[field], expected, "{name}: {field}");
        }
    }
}

fn schema_doc(value: Json) -> Document {
    let mut builder = DocumentBuilder::new(Limits::default(), GraphLimits::default());
    let id = builder
        .add(
            ResourceInput::anonymous(Arc::<[u8]>::from(serde_json::to_vec(&value).unwrap()))
                .layout(ResourceLayout::new().schema("", profile(DRAFT))),
        )
        .unwrap();
    builder.build(id).unwrap()
}
fn selected(doc: &Document, pointer: &str) -> SchemaHandle {
    doc.entry_resource().schema_at(pointer).unwrap()
}
#[test]
fn anonymous_layout_and_bytes_recovery_keep_old_snapshots() -> Result<(), Diagnostic> {
    let mut builder = DocumentBuilder::new(Limits::default(), GraphLimits::default());
    let bytes: Arc<[u8]> =
        Arc::from(br##"{"left":{"$ref":"#/right"},"right":{"type":"string"}}"##.as_slice());
    let id = builder
        .add(
            ResourceInput::anonymous(bytes)
                .layout(ResourceLayout::new().schema("/left", profile(DRAFT))),
        )
        .map_err(|e| e.diagnostic)?;
    let old = builder.build(id)?;
    let edge = selected(&old, "/left").references()?.next().unwrap();
    let failure = edge.resolve_static().unwrap_err();
    let need = failure.resource_need().unwrap();
    assert_eq!(need.kind(), ResourceNeedKind::Layout);
    assert_eq!(need.requested_uri(), None);
    assert_eq!(need.target_resource(), Some(id));
    assert_eq!(need.target_pointer(), Some("/right"));
    assert_eq!(need.reference_kind(), None);
    assert_eq!(need.edition(), None);
    let mut repair = DocumentBuilder::from_document(&old);
    repair.apply_layout(need, ResourceLayout::new().schema("/right", profile(DRAFT)))?;
    let next = repair.build(id)?;
    assert_eq!(
        next.entry_resource().source(),
        old.entry_resource().source()
    );
    assert_eq!(
        run_fact(&next, &selected(&next, "/left"), "Types")["value"],
        json!(["string"])
    );
    assert_eq!(
        edge.resolve_static().unwrap_err().code(),
        Code::ResourceLayoutRequired
    );
    assert_eq!(
        DocumentBuilder::from_document(&next)
            .check_need(need)
            .unwrap_err()
            .code(),
        Code::SnapshotMismatch
    );

    let old = schema_doc(json!({"$ref":"https://schema.example/missing"}));
    let edge = selected(&old, "").references()?.next().unwrap();
    let failure = edge.resolve_static().unwrap_err();
    let need = failure.resource_need().unwrap();
    assert_eq!(need.requested_uri(), Some("https://schema.example/missing"));
    let mut repair = DocumentBuilder::from_document(&old);
    let uri = need
        .requested_uri()
        .ok_or_else(|| Diagnostic::new(Code::MissingResourceBase))?;
    repair
        .supply(
            need,
            ResourceInput::at(uri, Arc::<[u8]>::from(br#"{"type":"string"}"#.as_slice()))?,
        )
        .map_err(|e| e.diagnostic)?;
    let next = repair.build(old.entry_resource().id())?;
    assert_eq!(
        run_fact(&next, &selected(&next, ""), "Types")["value"],
        json!(["string"])
    );
    assert_eq!(
        edge.resolve_static().unwrap_err().code(),
        Code::ResourceUnavailable
    );
    Ok(())
}
#[test]
fn supplied_root_closure_is_order_independent_and_conflicting_claims_refuse() {
    let mut fixtures = contract()["fixtures"].clone();
    for order in [
        json!(["bare-external-entry", "bare-external"]),
        json!(["bare-external", "bare-external-entry"]),
    ] {
        let (doc, ids) = build(order.as_array().unwrap(), &fixtures).unwrap();
        let target = handle(&doc, ids["bare-external-entry"], "/components/schemas/X")
            .unwrap()
            .references()
            .unwrap()
            .next()
            .unwrap()
            .resolve_static()
            .unwrap();
        assert_eq!(
            target.target().profile(),
            &profile("https://spec.openapis.org/oas/3.1/dialect/base")
        );
    }
    let target = fixtures["bare-external"]["retrieval"]
        .as_str()
        .unwrap()
        .to_owned();
    fixtures["other"] = json!({"retrieval":"https://other.example/doc","document":{"openapi":"3.2.1","info":{"title":"t","version":"1"},"paths":{},"components":{"schemas":{"X":{"$ref":target}}}}});
    for order in [
        json!(["bare-external-entry", "other", "bare-external"]),
        json!(["bare-external", "other", "bare-external-entry"]),
    ] {
        let error = build(order.as_array().unwrap(), &fixtures).err().unwrap();
        assert_eq!(error.code(), Code::ConflictingObjectContext);
        let Some(DiagnosticReason::Schema(SchemaReason::ContextConflict {
            other_source,
            other_location,
        })) = error.reason()
        else {
            panic!("{error:?}")
        };
        assert_ne!(Some(*other_source), error.source_context());
        assert_eq!(other_location.pointer, "/components/schemas/X/$ref");
    }
    fixtures["bare-external"]["document"]["$schema"] = json!(DRAFT);
    let (doc, ids) = build(
        json!(["bare-external-entry", "other", "bare-external"])
            .as_array()
            .unwrap(),
        &fixtures,
    )
    .unwrap();
    assert_eq!(
        handle(&doc, ids["bare-external"], "").unwrap().profile(),
        &profile(DRAFT)
    );
}
#[test]
fn property_facts_keep_each_conjuncts_lexical_membership_and_locality() {
    let doc = schema_doc(
        json!({"$defs":{"T":{"type":"object","properties":{"x":{"type":"string"}},"additionalProperties":{"type":"number"}}},"$ref":"#/$defs/T","properties":{"y":{"type":"string"}}}),
    );
    let schema = selected(&doc, "");
    assert_eq!(
        run_fact(&doc, &schema, "Property:x/Types")["value"],
        json!(["string"])
    );
    assert_eq!(
        run_fact(&doc, &schema, "Property:y/Types")["reason"],
        "ConflictingFacts"
    );
    assert_eq!(
        run_fact(&doc, &schema, "Property:z/Types")["value"],
        json!(["number"])
    );
    for remote in [
        json!({"$ref":"https://missing.example/schema"}),
        json!({"$ref":42}),
    ] {
        let doc = schema_doc(json!({"properties":{"known":{"type":"string"},"remote":remote}}));
        let schema = selected(&doc, "");
        assert_eq!(
            run_fact(&doc, &schema, "Property:known/Types")["value"],
            json!(["string"])
        );
        let remote = run_fact(&doc, &schema, "Property:remote/Types");
        assert_eq!(remote["source_pointer"], "/properties/remote/$ref");
        assert!(remote["kind"] == "Need" || remote["kind"] == "Invalid");
    }
}
#[test]
fn dialect_boundaries_and_opaque_data_do_not_activate_guessed_keywords() {
    let doc = schema_doc(json!({"$id":"https://schemas.example/root#","$defs":{
        "child":{"$id":"child","type":"string"},
        "bad":{"$schema":DRAFT,"type":"string"},
        "opaque":{"$id":"opaque","$schema":"https://custom.example/SECRET-DIALECT","properties":{"inert":{"$id":"https://evil.example/SECRET-ID"}}}
    }}));
    assert_eq!(
        selected(&doc, "").schema_resource_uri(),
        Some("https://schemas.example/root")
    );
    assert_eq!(selected(&doc, "/$defs/child").profile(), &profile(DRAFT));
    assert_eq!(
        selected(&doc, "/$defs/child").effective_base(),
        Some("https://schemas.example/child")
    );
    let error = doc.entry_resource().schema_at("/$defs/bad").unwrap_err();
    assert_eq!(
        error.reason(),
        Some(&DiagnosticReason::Schema(SchemaReason::InvalidDialectSite))
    );
    let opaque = selected(&doc, "/$defs/opaque");
    assert_eq!(opaque.dialect_capability(), DialectCapability::Opaque);
    assert!(
        doc.entry_resource()
            .schema_at("/$defs/opaque/properties/inert")
            .is_err()
    );
    assert!(!format!("{opaque:?} {:?}", opaque.profile()).contains("SECRET"));
    let raw = schema_doc(
        json!({"jsonSchemaDialect":"https://not-an-openapi.example/dialect","type":"string"}),
    );
    assert_eq!(selected(&raw, "").profile(), &profile(DRAFT));
}
#[test]
fn default_debug_never_exposes_dialect_target_fragment_or_property_name() {
    let mut builder = DocumentBuilder::new(Limits::default(), GraphLimits::default());
    let value = json!({"$schema":DRAFT,"properties":{"SECRET-PROPERTY":{"$ref":"https://schema.example/SECRET-URI#SECRET-ANCHOR"}}});
    let id = builder
        .add(
            ResourceInput::anonymous(Arc::<[u8]>::from(serde_json::to_vec(&value).unwrap()))
                .layout(
                    ResourceLayout::new()
                        .schema("", profile("https://dialect.example/SECRET-DIALECT")),
                ),
        )
        .unwrap();
    let doc = builder.build(id).unwrap();
    let handle = selected(&doc, "/properties/SECRET-PROPERTY");
    let edge = handle.references().unwrap().next().unwrap();
    let error = edge.resolve_static().unwrap_err();
    let need = error.resource_need().unwrap();
    assert!(matches!(need.fragment(), ResourceFragment::Anchor(_)));
    let printed = format!(
        "{doc:?} {handle:?} {edge:?} {error:?} {error} {need:?} {:?}",
        need.interpretation()
    );
    assert!(!printed.contains("SECRET"), "{printed}");
    assert_eq!(
        need.requested_uri(),
        Some("https://schema.example/SECRET-URI")
    );
}
#[test]
fn reference_work_depth_cancellation_and_lifetimes_are_independent() {
    let doc = schema_doc(json!({"$defs":{"T":{"type":"string"}},"$ref":"#/$defs/T"}));
    let schema = selected(&doc, "");
    let token = Cancellation::default();
    let limits = Limits {
        schema_fact_work: 0,
        ..Limits::default()
    };
    let mut work = FactWork::new(&limits, &token);
    let error = query(
        &doc.0.graph,
        &SchemaSelection(vec![schema.key]),
        FactQuery::Types,
        &mut work,
    )
    .err()
    .unwrap();
    assert_eq!(
        error.reason(),
        Some(&DiagnosticReason::Limit {
            kind: LimitKind::SchemaFactWork,
            maximum: 0,
            actual: 1
        })
    );
    let limits = Limits {
        schema_fact_work: 10,
        ..Limits::default()
    };
    let mut work = FactWork::new(&limits, &token);
    let direct = selected(&doc, "/$defs/T");
    assert!(
        query(
            &doc.0.graph,
            &SchemaSelection(vec![direct.key]),
            FactQuery::Types,
            &mut work
        )
        .is_ok()
    );
    let used = work.used();
    assert!(used > 0);
    loop {
        if let Err(error) = query(
            &doc.0.graph,
            &SchemaSelection(vec![direct.key]),
            FactQuery::Types,
            &mut work,
        ) {
            assert_eq!(error.code(), Code::Limit);
            break;
        }
    }
    let weak = Arc::downgrade(&doc.0);
    let raw = schema.authored();
    let edge = schema.references().unwrap().next().unwrap();
    let resolved = edge.resolve_static().unwrap();
    drop(doc);
    drop(schema);
    drop(direct);
    assert!(weak.upgrade().is_some());
    drop(resolved);
    drop(edge);
    assert!(weak.upgrade().is_none());
    assert_eq!(raw.get("$ref").unwrap().as_str(), Some("#/$defs/T"));
}
#[test]
fn width_is_not_reference_depth_and_long_chain_checks_exact_hop() {
    let mut properties = serde_json::Map::new();
    for n in 0..129 {
        properties.insert(format!("p{n}"), json!({"$ref":"#/$defs/T"}));
    }
    let doc = schema_doc(json!({"$defs":{"T":{"type":"string"}},"properties":properties}));
    let mut limits = Limits {
        reference_steps: 1,
        ..Limits::default()
    };
    let token = Cancellation::default();
    let mut work = FactWork::new(&limits, &token);
    for n in 0..129 {
        let h = selected(&doc, &format!("/properties/p{n}"));
        assert!(matches!(
            query(
                &doc.0.graph,
                &SchemaSelection(vec![h.key]),
                FactQuery::Types,
                &mut work
            )
            .unwrap(),
            Fact::Known { .. }
        ));
    }
    let mut defs = serde_json::Map::new();
    for n in 0..129 {
        defs.insert(
            format!("N{n}"),
            if n == 128 {
                json!({"type":"string"})
            } else {
                json!({"$ref":format!("#/$defs/N{}",n+1)})
            },
        );
    }
    let doc = schema_doc(json!({"$defs":defs,"$ref":"#/$defs/N0"}));
    let h = selected(&doc, "");
    limits.reference_steps = 128;
    limits.schema_fact_depth = 512;
    let mut work = FactWork::new(&limits, &token);
    let error = query(
        &doc.0.graph,
        &SchemaSelection(vec![h.key]),
        FactQuery::Types,
        &mut work,
    )
    .err()
    .unwrap();
    assert_eq!(
        error.reason(),
        Some(&DiagnosticReason::Limit {
            kind: LimitKind::ReferenceDepth,
            maximum: 128,
            actual: 129
        })
    );
}
#[test]
fn open_name_sessions_do_not_retain_query_state_in_snapshot() {
    let doc = schema_doc(json!({"type":"object"}));
    let handle = selected(&doc, "");
    let owners = Arc::strong_count(&doc.0);
    let token = Cancellation::default();
    for n in 0..1000 {
        let mut work = FactWork::new(&doc.0.limits, &token);
        assert!(matches!(
            query(
                &doc.0.graph,
                &SchemaSelection(vec![handle.key]),
                FactQuery::Property(&format!("p{n}")),
                &mut work
            )
            .unwrap(),
            Fact::Absent { .. }
        ));
        assert!(work.used() > 0);
        assert_eq!(Arc::strong_count(&doc.0), owners);
    }
}
#[test]
fn schema_edge_limits_and_cancellation_preserve_owner_free_failures() {
    let bytes: Arc<[u8]> = Arc::from(
        br##"{"$schema":"https://json-schema.org/draft/2020-12/schema","$ref":"#"}"##.as_slice(),
    );
    let graph_limits = GraphLimits {
        reference_edges: 0,
        ..GraphLimits::default()
    };
    let mut builder = DocumentBuilder::new(Limits::default(), graph_limits);
    let id = builder
        .add(ResourceInput::anonymous(bytes.clone()))
        .unwrap();
    let error = builder.build(id).unwrap_err();
    assert_eq!(
        error.reason(),
        Some(&DiagnosticReason::Limit {
            kind: LimitKind::GraphReferenceEdges,
            maximum: 0,
            actual: 1
        })
    );
    let limits = Limits {
        reference_steps: 0,
        ..Limits::default()
    };
    let mut builder = DocumentBuilder::new(limits, GraphLimits::default());
    let id = builder.add(ResourceInput::anonymous(bytes)).unwrap();
    let doc = builder.build(id).unwrap();
    let edge = selected(&doc, "").references().unwrap().next().unwrap();
    assert_eq!(
        edge.resolve_static().unwrap_err().reason(),
        Some(&DiagnosticReason::Limit {
            kind: LimitKind::ReferenceDepth,
            maximum: 0,
            actual: 1
        })
    );
    let token = Cancellation::default();
    token.cancel();
    assert_eq!(
        edge.resolve_static_with_cancellation(&token)
            .unwrap_err()
            .code(),
        Code::Cancelled
    );
    let builder = DocumentBuilder::from_document(&doc);
    assert_eq!(
        builder
            .build_with_cancellation(id, &token)
            .unwrap_err()
            .code(),
        Code::Cancelled
    );
    drop(edge);
    drop(doc);
    let doc = schema_doc(json!({"$ref":"https://missing.example/schema"}));
    let schema = selected(&doc, "");
    let weak = Arc::downgrade(&doc.0);
    let token = Cancellation::default();
    let mut work = FactWork::new(&doc.0.limits, &token);
    let fact = query(
        &doc.0.graph,
        &SchemaSelection(vec![schema.key]),
        FactQuery::Types,
        &mut work,
    )
    .unwrap();
    assert!(matches!(fact, Fact::Need(_)));
    drop(schema);
    drop(doc);
    assert!(weak.upgrade().is_none());
    drop(fact);
}
#[test]
fn edition_positions_and_modern_keywords_remain_precise() {
    for version in ["2.0", "3.0.4", "3.1.2", "3.2.0"] {
        let legacy = version == "2.0";
        let mut root = json!({"info":{"title":"t","version":"1"},"paths":{},"definitions":{"Old":{"type":"string"}},"components":{"schemas":{"T":{"type":"string","$id":"https://inert.example/id","$defs":{"X":{"type":"number"}}}},"parameters":{"P":{"name":"x","in":"query","schema":{"type":"string"}}},"headers":{"H":{"schema":{"type":"string"}}},"requestBodies":{"B":{"content":{"text/plain":{"schema":{"type":"string"},"itemSchema":{"type":"integer"}}}}}},"parameters":{"Body":{"in":"body","name":"b","schema":{"type":"string"}},"Other":{"in":"query","name":"q","type":"array","items":{"type":"string","$id":"https://inert.example/nonbody"}}}});
        root[if legacy { "swagger" } else { "openapi" }] = json!(version);
        let doc = Document::parse(
            serde_json::to_vec(&root).unwrap(),
            Some("https://api.example/doc"),
            Limits::default(),
        )
        .unwrap();
        if legacy {
            assert!(doc.entry_resource().schema_at("/definitions/Old").is_ok());
            assert!(
                doc.entry_resource()
                    .schema_at("/parameters/Body/schema")
                    .is_ok()
            );
            assert_eq!(
                doc.entry_resource()
                    .schema_at("/parameters/Other/items")
                    .unwrap_err()
                    .code(),
                Code::WrongReferenceKind
            );
            assert!(
                doc.entry_resource()
                    .schema_at("/components/schemas/T")
                    .is_err()
            );
        } else {
            for pointer in [
                "/components/schemas/T",
                "/components/parameters/P/schema",
                "/components/headers/H/schema",
                "/components/requestBodies/B/content/text~1plain/schema",
            ] {
                assert!(
                    doc.entry_resource().schema_at(pointer).is_ok(),
                    "{version} {pointer}"
                );
            }
            assert_eq!(
                doc.entry_resource()
                    .schema_at("/components/requestBodies/B/content/text~1plain/itemSchema")
                    .is_ok(),
                version.starts_with("3.2")
            );
            assert_eq!(
                doc.entry_resource()
                    .schema_at("/components/schemas/T/$defs/X")
                    .is_ok(),
                !version.starts_with("3.0")
            );
            assert_eq!(
                selected(&doc, "/components/schemas/T").effective_base(),
                Some(if version.starts_with("3.0") {
                    "https://api.example/doc"
                } else {
                    "https://inert.example/id"
                })
            );
        }
    }
}
#[test]
fn escaped_crossing_pointer_keeps_context_after_completion() {
    let value = json!({"openapi":"3.1.2","info":{"title":"t","version":"1"},"paths":{},"components":{"schemas":{
        "a/b":{"$id":"https://schemas.example/a/","type":"object","properties":{"amount":{"$ref":"money"}}},
        "Pointer":{"$ref":"#/components/schemas/a~1b"},"Canonical":{"$ref":"https://schemas.example/a/"}}}});
    let old = Document::parse(
        serde_json::to_vec(&value).unwrap(),
        Some("https://api.example/doc"),
        Limits::default(),
    )
    .unwrap();
    let pointer = selected(&old, "/components/schemas/Pointer")
        .references()
        .unwrap()
        .next()
        .unwrap()
        .resolve_static()
        .unwrap();
    let canonical = selected(&old, "/components/schemas/Canonical")
        .references()
        .unwrap()
        .next()
        .unwrap()
        .resolve_static()
        .unwrap();
    assert_eq!(pointer.target().key, canonical.target().key);
    assert_eq!(
        pointer.access_reason(),
        Some(SchemaAccessReason::CrossingPointer)
    );
    assert!(canonical.canonical());
    let edge = selected(&old, "/components/schemas/a~1b/properties/amount")
        .references()
        .unwrap()
        .next()
        .unwrap();
    let error = edge.resolve_static().unwrap_err();
    let need = error.resource_need().unwrap();
    assert_eq!(
        need.requested_uri(),
        Some("https://schemas.example/a/money")
    );
    let mut builder = DocumentBuilder::from_document(&old);
    builder
        .supply(
            need,
            ResourceInput::at(
                need.requested_uri().unwrap(),
                Arc::<[u8]>::from(br#"{"type":"number"}"#.as_slice()),
            )
            .unwrap(),
        )
        .unwrap();
    let next = builder.build(old.entry_resource().id()).unwrap();
    for route in ["Pointer", "Canonical"] {
        assert_eq!(
            run_fact(
                &next,
                &selected(&next, &format!("/components/schemas/{route}")),
                "Property:amount/Types"
            )["value"],
            json!(["number"])
        );
    }
    assert_eq!(
        edge.resolve_static().unwrap_err().code(),
        Code::ResourceUnavailable
    );
    assert_eq!(
        pointer.target().effective_base(),
        Some("https://schemas.example/a/")
    );
}
#[test]
fn opaque_targets_are_inspectable_only_at_independently_indexed_sites() {
    for (reference, layout, expected) in [
        ("https://schemas.example/opaque", false, None),
        ("https://schemas.example/opaque#", false, None),
        (
            "https://schemas.example/opaque#/properties/x",
            false,
            Some(Code::WrongReferenceKind),
        ),
        (
            "https://schemas.example/opaque#named",
            false,
            Some(Code::UnsupportedSchema),
        ),
        ("https://schemas.example/opaque#/properties/x", true, None),
    ] {
        let mut builder = DocumentBuilder::new(Limits::default(), GraphLimits::default());
        let entry = builder
            .add(ResourceInput::anonymous(Arc::<[u8]>::from(
                serde_json::to_vec(&json!({"$schema":DRAFT,"$ref":reference})).unwrap(),
            )))
            .unwrap();
        let mut target=ResourceInput::at("https://schemas.example/opaque",Arc::<[u8]>::from(serde_json::to_vec(&json!({"$schema":"https://custom.example/dialect","properties":{"x":{"type":"string"}}})).unwrap())).unwrap();
        if layout {
            target = target.layout(ResourceLayout::new().schema("/properties/x", profile(DRAFT)));
        }
        builder.add(target).unwrap();
        let doc = builder.build(entry).unwrap();
        let edge = selected(&doc, "").references().unwrap().next().unwrap();
        let result = edge.resolve_static();
        if let Some(code) = expected {
            let error = result.unwrap_err();
            assert_eq!(error.code(), code, "{reference}");
            assert!(error.resource_need().is_none());
        } else {
            let result = result.unwrap();
            if layout {
                assert_eq!(
                    result.target().dialect_capability(),
                    DialectCapability::Supported
                );
            } else {
                assert_eq!(
                    result.target().dialect_capability(),
                    DialectCapability::Opaque
                );
                assert_eq!(
                    result.target().references().err().unwrap().code(),
                    Code::UnsupportedSchema
                );
                assert_eq!(
                    run_fact(&doc, result.target(), "Types")["reason"],
                    "OpaqueDialect"
                );
            }
        }
    }
}
#[test]
fn anchor_grammar_and_anonymous_anchor_identity_budget() {
    for (anchor, valid) in [
        ("_x", true),
        ("a.b-c_2", true),
        ("a:b", false),
        ("2a", false),
        ("", false),
        ("a/b", false),
    ] {
        let doc = schema_doc(json!({"$anchor":anchor}));
        let result = doc.entry_resource().schema_at("");
        if valid {
            assert!(result.is_ok());
        } else {
            let error = result.unwrap_err();
            assert_eq!(error.code(), Code::InvalidDeclaration);
            assert_eq!(error.location().unwrap().pointer, "/$anchor");
        }
    }
    let doc = schema_doc(json!({"$ref":"#a:b"}));
    let error = selected(&doc, "")
        .references()
        .unwrap()
        .next()
        .unwrap()
        .resolve_static()
        .unwrap_err();
    assert_eq!(error.code(), Code::InvalidReference);
    assert!(error.resource_need().is_none());
    let graph_limits = GraphLimits {
        identities: 0,
        ..GraphLimits::default()
    };
    let mut builder = DocumentBuilder::new(Limits::default(), graph_limits);
    let id = builder
        .add(ResourceInput::anonymous(Arc::<[u8]>::from(
            br#"{"$schema":"https://json-schema.org/draft/2020-12/schema","$anchor":"x"}"#
                .as_slice(),
        )))
        .unwrap();
    let error = builder.build(id).unwrap_err();
    assert_eq!(
        error.reason(),
        Some(&DiagnosticReason::Limit {
            kind: LimitKind::GraphIdentities,
            maximum: 0,
            actual: 1
        })
    );
}
#[test]
fn rejected_layout_metadata_does_not_enrich_a_transaction() {
    let limits = GraphLimits {
        metadata_bytes: DRAFT.len() + 1,
        layout_roots: 1,
        ..GraphLimits::default()
    };
    let mut builder = DocumentBuilder::new(Limits::default(), limits);
    let id = builder
        .add(ResourceInput::anonymous(Arc::<[u8]>::from(
            br#"{"left":{"type":"string"},"right":{"type":"number"}}"#.as_slice(),
        )))
        .unwrap();
    let error = builder
        .enrich_layout(id, ResourceLayout::new().schema("/left", profile(DRAFT)))
        .unwrap_err();
    assert!(matches!(
        error.reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::GraphMetadataBytes,
            ..
        })
    ));
    // The rejected insertion has not silently installed a schema root.
    let doc = builder.build(id).unwrap();
    assert_eq!(
        doc.entry_resource().schema_at("/left").unwrap_err().code(),
        Code::WrongReferenceKind
    );
    let mut builder = DocumentBuilder::from_document(&doc);
    let error = builder
        .enrich_layout(
            id,
            ResourceLayout::new()
                .schema("/left", profile(DRAFT))
                .schema("/left", profile(DRAFT)),
        )
        .unwrap_err();
    assert_eq!(
        error.reason(),
        Some(&DiagnosticReason::Limit {
            kind: LimitKind::GraphLayouts,
            maximum: 1,
            actual: 2
        })
    );
    assert_eq!(
        builder.build(id).unwrap().entry_resource().source(),
        doc.entry_resource().source()
    );
}
#[test]
fn cancellation_inside_indexing_and_fact_work_never_publishes_partial_state() {
    let old = schema_doc(json!({"properties":{"a":{"type":"string"},"b":{"type":"number"}}}));
    let builder = DocumentBuilder::from_document(&old);
    let token = Cancellation::default();
    let guard = crate::schema::cancel_after_for_test(3);
    let error = builder
        .build_with_cancellation(old.entry_resource().id(), &token)
        .unwrap_err();
    assert_eq!(error.code(), Code::Cancelled);
    assert!(token.is_cancelled());
    drop(guard);
    assert_eq!(
        run_fact(&old, &selected(&old, ""), "Property:a/Types")["value"],
        json!(["string"])
    );
    let doc = schema_doc(json!({"$defs":{"T":{"type":"string"}},"$ref":"#/$defs/T"}));
    let schema = selected(&doc, "");
    let token = Cancellation::default();
    let guard = crate::schema::cancel_after_for_test(4);
    let mut work = FactWork::new(&doc.0.limits, &token);
    let error = query(
        &doc.0.graph,
        &SchemaSelection(vec![schema.key]),
        FactQuery::Types,
        &mut work,
    )
    .err()
    .unwrap();
    assert_eq!(error.code(), Code::Cancelled);
    assert!(work.used() > 0);
    drop(guard);
    assert_eq!(run_fact(&doc, &schema, "Types")["value"], json!(["string"]));
}
#[test]
fn conflicting_schema_identities_refuse_without_modifying_previous_snapshot() {
    let first = schema_doc(json!({"$id":"https://schemas.example/conflict","type":"string"}));
    let mut builder = DocumentBuilder::from_document(&first);
    builder
        .add(ResourceInput::anonymous(Arc::<[u8]>::from(
            serde_json::to_vec(
                &json!({"$schema":DRAFT,"$id":"https://schemas.example/conflict","type":"number"}),
            )
            .unwrap(),
        )))
        .unwrap();
    let error = builder.build(first.entry_resource().id()).unwrap_err();
    assert_eq!(error.code(), Code::IdentityCollision);
    assert_eq!(
        run_fact(&first, &selected(&first, ""), "Types")["value"],
        json!(["string"])
    );
}
#[test]
fn old_and_completed_edges_resolve_concurrently_in_their_own_snapshots() {
    let old = schema_doc(json!({"$ref":"https://schemas.example/missing"}));
    let edge = selected(&old, "").references().unwrap().next().unwrap();
    let error = edge.resolve_static().unwrap_err();
    let need = error.resource_need().unwrap();
    let mut builder = DocumentBuilder::from_document(&old);
    builder
        .supply(
            need,
            ResourceInput::at(
                need.requested_uri().unwrap(),
                Arc::<[u8]>::from(br#"{"type":"string"}"#.as_slice()),
            )
            .unwrap(),
        )
        .unwrap();
    let next = builder.build(old.entry_resource().id()).unwrap();
    let current = selected(&next, "").references().unwrap().next().unwrap();
    std::thread::scope(|scope| {
        scope.spawn(|| {
            for _ in 0..16 {
                assert_eq!(
                    edge.resolve_static().unwrap_err().code(),
                    Code::ResourceUnavailable
                );
            }
        });
        scope.spawn(|| {
            for _ in 0..16 {
                assert_eq!(
                    current
                        .resolve_static()
                        .unwrap()
                        .target()
                        .authored()
                        .get("type")
                        .unwrap()
                        .as_str(),
                    Some("string")
                );
            }
        });
    });
}
#[test]
fn context_conflict_default_formatting_redacts_both_sources_names() {
    let mut builder = DocumentBuilder::new(Limits::default(), GraphLimits::default());
    let mut entry = None;
    for (version, name) in [("3.1.2", "SECRET-FIRST"), ("3.2.1", "SECRET-SECOND")] {
        let value = json!({"openapi":version,"info":{"title":"t","version":"1"},"paths":{},"components":{"schemas":{name:{"$ref":"https://schemas.example/SECRET-TARGET"}}}});
        let id = builder
            .add(ResourceInput::anonymous(Arc::<[u8]>::from(
                serde_json::to_vec(&value).unwrap(),
            )))
            .unwrap();
        entry.get_or_insert(id);
    }
    builder
        .add(
            ResourceInput::at(
                "https://schemas.example/SECRET-TARGET",
                Arc::<[u8]>::from(br#"{"type":"string"}"#.as_slice()),
            )
            .unwrap(),
        )
        .unwrap();
    let error = builder.build(entry.unwrap()).unwrap_err();
    assert_eq!(error.code(), Code::ConflictingObjectContext);
    assert!(!format!("{error} {error:?} {:?}", error.reason()).contains("SECRET"));
    assert!(error.location().unwrap().pointer.contains("SECRET"));
    let Some(DiagnosticReason::Schema(SchemaReason::ContextConflict { other_location, .. })) =
        error.reason()
    else {
        panic!("missing corrective source")
    };
    assert!(other_location.pointer.contains("SECRET"));
}
