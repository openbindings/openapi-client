#[path = "support/request_case.rs"]
mod request_case;
use dynamic_openapi_client::*;
use dynamic_openapi_client::{diagnostics::*, form::*, graph::*, inspect::*};
use request_case::*;
use std::{collections::HashMap, sync::Arc};

struct GraphFixture {
    name: &'static str,
    resources: Vec<(&'static str, &'static [u8])>,
    checks: Vec<(
        &'static str,
        &'static str,
        ReferenceKind,
        &'static str,
        &'static str,
    )>,
}
include!("graph-fixtures/cases.rs");
fn input(uri: &str, bytes: &[u8]) -> ResourceInput {
    ResourceInput::at(uri, Arc::<[u8]>::from(bytes)).unwrap()
}
fn root(parameter: &str) -> Vec<u8> {
    format!(r##"{{"openapi":"3.2.1","info":{{"title":"Graph","version":"1"}},"servers":[{{"url":"https://api.example"}}],"paths":{{"/ok":{{"get":{{"operationId":"ok","responses":{{"200":{{"description":"ok"}}}}}}}},"/x":{{"get":{{"operationId":"x","parameters":[{{"$ref":"{parameter}"}}],"responses":{{"200":{{"description":"ok"}}}}}}}}}}}}"##).into_bytes()
}

#[test]
fn frozen_all_family_json_yaml_protocol_graphs_and_request() {
    for fixture in graphs() {
        let mut builder = DocumentBuilder::new(Limits::default(), GraphLimits::default());
        let mut ids = HashMap::new();
        for (uri, bytes) in fixture.resources {
            ids.insert(uri, builder.add(input(uri, bytes)).unwrap());
        }
        let doc = builder
            .build(ids["https://docs.example/api/entry"])
            .unwrap();
        assert!(
            doc.discovery_complete(),
            "{}: {:?}",
            fixture.name,
            doc.graph_issues().collect::<Vec<_>>()
        );
        assert_eq!(doc.operations().count(), 3, "{}", fixture.name);
        for (source, pointer, kind, target, target_pointer) in fixture.checks {
            let source = doc.resource(ids[source]).unwrap();
            let site = source.protocol_at(pointer, kind).unwrap();
            let resolved = doc.resolve_protocol(&site).unwrap();
            let expected = doc
                .resource(ids[target])
                .unwrap()
                .root()
                .pointer(target_pointer)
                .unwrap();
            assert_eq!(
                resolved.target.source_context(),
                expected.source_context(),
                "{} {pointer}",
                fixture.name
            );
            assert_eq!(
                resolved.target.location().pointer,
                target_pointer,
                "{}",
                fixture.name
            );
            assert!(resolved.canonical);
        }
        let request = doc
            .operation_id("getThing")
            .unwrap()
            .request()
            .parameter(ParameterLocation::Query, "id", &"42")
            .credential(
                "Shared",
                Credential::api_key("disposable", vec!["https://api.example".into()]),
            )
            .prepare()
            .unwrap();
        assert_eq!(request.method(), Method::Get);
        assert_eq!(
            request.target(),
            "https://api.example/things?id=42",
            "{}",
            fixture.name
        );
        assert!(
            request
                .headers()
                .iter()
                .any(|h| h.name() == "X-Key" && h.value() == b"disposable")
        );
    }
}

#[test]
fn missing_bytes_recover_uniformly_without_mutating_old_snapshots() {
    let doc = Document::parse(
        root("params"),
        Some("https://docs.example/root"),
        Limits::default(),
    )
    .unwrap();
    let op = doc.operation_id("x").unwrap();
    let direct = op.prepare_case(&CaseInput::default()).unwrap_err();
    let fluent = op.request().prepare().unwrap_err();
    let effective = op.describe().unwrap_err();
    for error in [&direct, &fluent, &effective] {
        assert_eq!(error.code(), Code::ResourceUnavailable);
        let need = error.resource_need().expect("uniform corrective need");
        assert_eq!(need.kind(), ResourceNeedKind::Bytes);
        assert_eq!(need.requested_uri(), Some("https://docs.example/params"));
        assert_eq!(need.reference_kind(), Some(ReferenceKind::Parameter));
        assert_eq!(need.source().as_str(), Some("params"));
    }
    assert!(
        doc.operation_id("ok")
            .unwrap()
            .prepare_case(&CaseInput::default())
            .is_ok()
    );
    let mut builder = DocumentBuilder::from_document(&doc);
    builder
        .supply(
            direct.resource_need().unwrap(),
            input(
                "https://docs.example/params",
                br#"{"name":"q","in":"query","schema":{"type":"string"}}"#,
            ),
        )
        .unwrap();
    let completed = builder.build(doc.entry_resource().id()).unwrap();
    assert!(
        completed
            .operation_id("x")
            .unwrap()
            .prepare_case(&CaseInput::default())
            .is_ok()
    );
    assert_eq!(
        op.prepare_case(&CaseInput::default()).unwrap_err().code(),
        Code::ResourceUnavailable
    );
    #[cfg(feature = "qualification")]
    assert!(qualification::cached_operations(&doc) > 0);
}

#[test]
fn layout_enrichment_preserves_source_identity_and_rejects_stale_needs_sites() {
    let mut builder = DocumentBuilder::new(Limits::default(), GraphLimits::default());
    let entry = builder
        .add(input("https://docs.example/root", &root("bundle#/p")))
        .unwrap();
    let bundle = builder.add(input("https://docs.example/bundle", br#"{"p":{"name":"q","in":"query","schema":{}},"payload":{"$id":"https://evil.example/data"}}"#)).unwrap();
    let old = builder.build(entry).unwrap();
    let old_source = old.resource(bundle).unwrap().root().source_context();
    let error = old
        .operation_id("x")
        .unwrap()
        .prepare_case(&CaseInput::default())
        .unwrap_err();
    let need = error.resource_need().unwrap();
    assert_eq!(need.kind(), ResourceNeedKind::Layout);
    let old_site = old
        .protocol_at("/paths/~1x/get/parameters/0", ReferenceKind::Parameter)
        .unwrap();
    let layout = ResourceLayout::new().protocol("/p", ReferenceKind::Parameter, Edition::OpenApi32);
    let mut repair = DocumentBuilder::from_document(&old);
    repair.apply_layout(need, layout.clone()).unwrap();
    let new = repair.build(entry).unwrap();
    assert_eq!(
        new.resource(bundle).unwrap().root().source_context(),
        old_source
    );
    assert_eq!(
        new.resolve_protocol(&old_site).unwrap_err().code(),
        Code::SnapshotMismatch
    );
    assert_eq!(
        DocumentBuilder::from_document(&new)
            .apply_layout(need, layout)
            .unwrap_err()
            .code(),
        Code::SnapshotMismatch
    );
    let site = new
        .protocol_at("/paths/~1x/get/parameters/0", ReferenceKind::Parameter)
        .unwrap();
    assert!(new.resolve_protocol(&site).is_ok());
    assert!(
        new.operation_id("x")
            .unwrap()
            .prepare_case(&CaseInput::default())
            .is_ok()
    );
    assert_eq!(
        old.operation_id("x")
            .unwrap()
            .prepare_case(&CaseInput::default())
            .unwrap_err()
            .code(),
        Code::ResourceLayoutRequired
    );
    let mut conflict = DocumentBuilder::from_document(&new);
    assert_eq!(
        conflict
            .enrich_layout(
                bundle,
                ResourceLayout::new().protocol("/p", ReferenceKind::Response, Edition::OpenApi32)
            )
            .unwrap_err()
            .code(),
        Code::ConflictingObjectContext
    );
}

#[test]
fn incomplete_path_discovery_does_not_block_an_explicit_usable_path() {
    let bytes = br##"{"openapi":"3.2.1","servers":[{"url":"https://api.example"}],"paths":{"/known":{"get":{"operationId":"duplicate","responses":{"200":{"description":"ok"}}}},"/other":{"$ref":"parts"}}}"##;
    let old = Document::parse(bytes, Some("https://docs.example/root"), Limits::default()).unwrap();
    assert!(!old.discovery_complete());
    assert_eq!(
        old.operation_id("duplicate").unwrap_err().code(),
        Code::IncompleteDiscovery
    );
    assert!(
        old.operation("/known", Method::Get)
            .unwrap()
            .prepare_case(&CaseInput::default())
            .is_ok()
    );
    let error = old.operation("/other", Method::Get).unwrap_err();
    let mut builder = DocumentBuilder::from_document(&old);
    builder
        .supply(
            error.resource_need().unwrap(),
            input(
                "https://docs.example/parts",
                br#"{"get":{"operationId":"duplicate","responses":{"200":{"description":"ok"}}}}"#,
            ),
        )
        .unwrap();
    let complete = builder.build(old.entry_resource().id()).unwrap();
    assert!(complete.discovery_complete());
    assert_eq!(
        complete.operation_id("duplicate").unwrap_err().code(),
        Code::AmbiguousOperation
    );
    assert!(
        complete
            .operation("/other", Method::Get)
            .unwrap()
            .prepare_case(&CaseInput::default())
            .is_ok()
    );
}

#[test]
fn declared_self_does_not_rebase_servers_and_aliases_are_unconditional() {
    let mut builder = DocumentBuilder::new(Limits::default(), GraphLimits::default());
    let entry = builder.add(input("https://retrieval.example/dir/root", br##"{"openapi":"3.2.1","$self":"https://identity.example/root#self","servers":[{"url":"./api"}],"paths":{"/x":{"$ref":"parts"}}}"##)
        .aliases(["https://requested.example/start".into(), "https://requested.example/hop".into()]).unwrap()).unwrap();
    builder
        .add(input(
            "https://identity.example/parts",
            br#"{"get":{"responses":{"200":{"description":"ok"}}}}"#,
        ))
        .unwrap();
    let doc = builder.build(entry).unwrap();
    let ready = doc
        .operation("/x", Method::Get)
        .unwrap()
        .prepare_case(&CaseInput::default())
        .unwrap();
    assert_eq!(ready.target(), "https://retrieval.example/dir/api/x");
    assert_eq!(doc.entry_resource().aliases().count(), 3);
}

#[test]
fn security_uri_keys_recover_and_exact_uri_like_names_take_precedence() {
    let bytes = br##"{"openapi":"3.2.1","servers":[{"url":"https://api.example"}],"paths":{"/x":{"get":{"security":[{"scheme":[]}],"responses":{"200":{"description":"ok"}}}}}}"##;
    let doc = Document::parse(bytes, Some("https://docs.example/root"), Limits::default()).unwrap();
    let op = doc.operation("/x", Method::Get).unwrap();
    let direct = op.prepare_case(&CaseInput::default()).unwrap_err();
    let effective = op.describe().unwrap_err();
    let fluent = op.request().prepare().unwrap_err();
    for error in [&direct, &effective, &fluent] {
        let need = error
            .resource_need()
            .expect("URI-key need at every public boundary");
        assert_eq!(need.requested_uri(), Some("https://docs.example/scheme"));
        assert_eq!(need.reference_kind(), Some(ReferenceKind::SecurityScheme));
    }
    let mut builder = DocumentBuilder::from_document(&doc);
    builder
        .supply(
            direct.resource_need().unwrap(),
            input(
                "https://docs.example/scheme",
                br#"{"type":"apiKey","in":"header","name":"X-External"}"#,
            ),
        )
        .unwrap();
    let fixed = builder.build(doc.entry_resource().id()).unwrap();
    let ready = fixed
        .operation("/x", Method::Get)
        .unwrap()
        .request()
        .credential(
            "scheme",
            Credential::api_key("test", vec!["https://api.example".into()]),
        )
        .prepare()
        .unwrap();
    assert!(ready.headers().iter().any(|h| h.name() == "X-External"));
    let named = br##"{"openapi":"3.2.1","servers":[{"url":"https://api.example"}],"components":{"securitySchemes":{"https://unavailable.example/scheme":{"type":"apiKey","in":"header","name":"X-Named"}}},"paths":{"/x":{"get":{"security":[{"https://unavailable.example/scheme":[]}],"responses":{"200":{"description":"ok"}}}}}}"##;
    let doc = Document::parse(named, None, Limits::default()).unwrap();
    let ready = doc
        .operation("/x", Method::Get)
        .unwrap()
        .request()
        .credential(
            "https://unavailable.example/scheme",
            Credential::api_key("test", vec!["https://api.example".into()]),
        )
        .prepare()
        .unwrap();
    assert!(ready.headers().iter().any(|h| h.name() == "X-Named"));
}

#[test]
fn descriptor_width_does_not_spend_reference_chain_depth() {
    let parameters = (0..129)
        .map(|i| serde_json::json!({"name":format!("p{i}"),"in":"query","schema":{}}))
        .collect::<Vec<_>>();
    let bytes = serde_json::json!({"openapi":"3.2.1","paths":{"/x":{"get":{"parameters":parameters,"responses":{"200":{"description":"ok"}}}}}}).to_string();
    let limits = Limits {
        reference_steps: 1,
        ..Limits::default()
    };
    let doc = Document::parse(bytes, None, limits).unwrap();
    assert_eq!(
        doc.operation("/x", Method::Get)
            .unwrap()
            .describe()
            .unwrap()
            .parameters
            .iter()
            .count(),
        129
    );
}

#[test]
fn conflicting_identity_and_unrelated_malformed_input_do_not_replace_existing_source() {
    let mut builder = DocumentBuilder::new(Limits::default(), GraphLimits::default());
    let entry = builder
        .add(input(
            "https://docs.example/root",
            &root("#/components/parameters/p"),
        ))
        .unwrap();
    let error = builder
        .add(input(
            "https://docs.example/root",
            br#"{"openapi":"3.2.1","paths":{}}"#,
        ))
        .unwrap_err();
    assert_eq!(error.code(), Code::IdentityCollision);
    assert!(
        builder
            .add(input("https://docs.example/bad", b"{"))
            .is_err()
    );
    let doc = builder.build(entry).unwrap();
    assert!(
        doc.operation("/ok", Method::Get)
            .unwrap()
            .prepare_case(&CaseInput::default())
            .is_ok()
    );
    assert_eq!(doc.resources().count(), 1);
}

#[test]
fn empty_self_uses_real_retrieval_base_or_reports_located_missing_base() {
    let source = br#"{"openapi":"3.2.1","$self":"","paths":{}}"#;
    let doc =
        Document::parse(source, Some("https://docs.example/root"), Limits::default()).unwrap();
    assert_eq!(
        doc.entry_resource().description_base(),
        Some("https://docs.example/root")
    );
    let error = Document::parse(source, None, Limits::default()).unwrap_err();
    assert_eq!(error.code(), Code::MissingResourceBase);
    assert_eq!(error.location().unwrap().pointer, "/$self");
}

#[test]
fn reference_metadata_overrides_only_fields_supported_by_the_target_kind() {
    for (version, summary) in [("3.0.4", "target"), ("3.1.2", "near"), ("3.2.1", "near")] {
        let bytes = serde_json::json!({"openapi":version,"paths":{},"components":{
            "examples":{"Use":{"$ref":"#/components/examples/Target","summary":"near","description":"near description"},"Target":{"summary":"target","description":"target description","value":{"$ref":"https://inert.example/value"}}},
            "callbacks":{"Use":{"$ref":"#/components/callbacks/Target","summary":"ignored","description":"ignored"},"Target":{}}
        }}).to_string();
        let doc = Document::parse(bytes, None, Limits::default()).unwrap();
        let site = doc
            .protocol_at("/components/examples/Use", ReferenceKind::Example)
            .unwrap();
        let resolved = doc.resolve_protocol(&site).unwrap();
        assert_eq!(resolved.summary.unwrap().as_str(), Some(summary));
        assert_eq!(
            resolved.description.unwrap().as_str(),
            Some(if version.starts_with("3.0") {
                "target description"
            } else {
                "near description"
            })
        );
        let site = doc
            .protocol_at("/components/callbacks/Use", ReferenceKind::Callback)
            .unwrap();
        let resolved = doc.resolve_protocol(&site).unwrap();
        assert!(resolved.summary.is_none() && resolved.description.is_none());
    }
}

#[test]
fn issues_and_first_corrective_need_follow_authored_path_order() {
    let doc = Document::parse(
        br#"{"openapi":"3.2.1","paths":{"/z":{"$ref":"z"},"/a":{"$ref":"a"}}}"#,
        Some("https://docs.example/root"),
        Limits::default(),
    )
    .unwrap();
    let paths = doc
        .graph_issues()
        .map(|e| e.location().unwrap().pointer.clone())
        .collect::<Vec<_>>();
    assert_eq!(paths, ["/paths/~1z/$ref", "/paths/~1a/$ref"]);
    let error = doc.operation_id("unknown").unwrap_err();
    assert_eq!(
        error.resource_need().unwrap().requested_uri(),
        Some("https://docs.example/z")
    );
}

#[test]
fn alias_and_layout_admission_are_bounded_before_indexing_or_idempotent_growth() {
    let source = br#"{"openapi":"3.2.1","paths":{}}"#;
    let mut caps = GraphLimits::default();
    caps.identities = 3;
    let mut builder = DocumentBuilder::new(Limits::default(), caps);
    let entry = builder
        .add(
            input("https://docs.example/root", source)
                .aliases([
                    "https://docs.example/a".into(),
                    "https://docs.example/b".into(),
                ])
                .unwrap(),
        )
        .unwrap();
    let error = builder
        .add(
            input("https://docs.example/root", source)
                .aliases(["https://docs.example/c".into()])
                .unwrap(),
        )
        .unwrap_err();
    assert!(matches!(
        error.reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::GraphIdentities,
            maximum: 3,
            actual: 4
        })
    ));
    let doc = builder.build(entry).unwrap();
    assert_eq!(doc.entry_resource().aliases().count(), 3);
    let cancelled = Cancellation::default();
    cancelled.cancel();
    let error = DocumentBuilder::from_document(&doc)
        .add_with_cancellation(input("https://docs.example/root", source), &cancelled)
        .unwrap_err();
    assert_eq!(error.code(), Code::Cancelled);
    let mut caps = GraphLimits::default();
    caps.metadata_bytes = "https://docs.example/root".len() * 3 - 1;
    let error = DocumentBuilder::new(Limits::default(), caps)
        .add(input("https://docs.example/root", source))
        .unwrap_err();
    assert!(matches!(
        error.reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::GraphMetadataBytes,
            ..
        })
    ));
    let mut caps = GraphLimits::default();
    caps.layout_roots = 2;
    let mut builder = DocumentBuilder::new(Limits::default(), caps);
    let id = builder
        .add(input(
            "https://docs.example/bundle",
            br#"{"p":{"name":"q","in":"query"}}"#,
        ))
        .unwrap();
    let mut wide = ResourceLayout::new();
    for i in 0..1000 {
        wide = wide.protocol(
            format!("/p{i}"),
            ReferenceKind::Parameter,
            Edition::OpenApi32,
        );
    }
    assert!(matches!(
        builder.enrich_layout(id, wide).unwrap_err().reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::GraphLayouts,
            maximum: 2,
            actual: 1000
        })
    ));
}

#[test]
fn observable_local_validation_issues_do_not_hide_operation_ids() {
    let source = r#"{"openapi":"3.2.1","servers":[{"url":"https://api.example"}],"paths":{"/bad":{"additionalOperations":{"bad token":{"operationId":"invalid"}}},"/scalar":42,"/healthy":{"get":{"operationId":"healthy"}}}}"#;
    let doc = Document::parse(source, None, Limits::default()).unwrap();
    assert!(doc.discovery_complete());
    assert_eq!(doc.graph_issues().count(), 2);
    assert!(
        doc.operation_id("healthy")
            .unwrap()
            .request()
            .prepare()
            .is_ok()
    );
    assert_eq!(
        doc.operation_id("invalid").unwrap_err().code(),
        Code::MissingOperation
    );
}

#[test]
fn uri_like_exact_security_name_does_not_infer_unrelated_supplied_bundle() {
    let root = r#"{"openapi":"3.2.1","servers":[{"url":"https://api.example"}],"paths":{"/":{"get":{"security":[{"https://docs.example/fragment":[]}]}}},"components":{"securitySchemes":{"https://docs.example/fragment":{"type":"apiKey","in":"header","name":"X-Auth"}}}}"#;
    let mut builder = DocumentBuilder::new(Limits::default(), GraphLimits::default());
    let entry = builder
        .add(ResourceInput::at("https://docs.example/root", root.as_bytes()).unwrap())
        .unwrap();
    builder
        .add(
            ResourceInput::at(
                "https://docs.example/fragment",
                br#"{"name":"id","in":"query"}"#.as_slice(),
            )
            .unwrap()
            .layout(ResourceLayout::new().protocol(
                "",
                ReferenceKind::Parameter,
                Edition::OpenApi32,
            )),
        )
        .unwrap();
    let doc = builder.build(entry).unwrap();
    let description = doc.operation("/", Method::Get).unwrap().describe().unwrap();
    assert_eq!(
        description.security[0].requirements[0].name,
        "https://docs.example/fragment"
    );
}

#[test]
fn nested_and_positional_encoding_headers_are_32_protocol_positions_only() {
    for version in ["3.0.4", "3.1.2", "3.2.1"] {
        let source = format!(
            r##"{{"openapi":"{version}","paths":{{"/":{{"post":{{"requestBody":{{"examples":{{"inert":{{"$ref":"missing"}}}},"content":{{"multipart/mixed":{{"prefixEncoding":[{{"headers":{{"X-First":{{"$ref":"#/components/headers/H"}}}}}}],"itemEncoding":{{"encoding":{{"nested":{{"headers":{{"X-Nested":{{"$ref":"#/components/headers/H"}}}}}}}}}}}}}}}}}}}}}},"components":{{"headers":{{"H":{{"schema":{{"type":"string"}}}}}}}}}}"##
        );
        let doc = Document::parse(source, None, Limits::default()).unwrap();
        for pointer in [
            "/paths/~1/post/requestBody/content/multipart~1mixed/prefixEncoding/0/headers/X-First",
            "/paths/~1/post/requestBody/content/multipart~1mixed/itemEncoding/encoding/nested/headers/X-Nested",
        ] {
            let site = doc.protocol_at(pointer, ReferenceKind::Header);
            if version == "3.2.1" {
                assert_eq!(
                    doc.resolve_protocol(&site.unwrap())
                        .unwrap()
                        .target
                        .location()
                        .pointer,
                    "/components/headers/H"
                );
            } else {
                assert_eq!(site.unwrap_err().code(), Code::WrongReferenceKind);
            }
        }
        assert_eq!(
            doc.protocol_at(
                "/paths/~1/post/requestBody/examples/inert",
                ReferenceKind::Example
            )
            .unwrap_err()
            .code(),
            Code::WrongReferenceKind
        );
    }
}

#[test]
fn selected_form_header_references_use_uniform_graph_recovery() {
    for version in ["3.0.4", "3.1.2", "3.2.1"] {
        let source = serde_json::json!({"openapi":version,"servers":[{"url":"https://api.example"}],"paths":{"/":{"post":{"operationId":"send","requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"x":{"type":"string"}}},"encoding":{"x":{"headers":{"X-Optional":{"$ref":"header"},"Content-Type":{"$ref":"inert-missing-header"}}}}}}}}}}});
        let old = Document::parse(
            source.to_string(),
            Some("https://docs.example/root"),
            Limits::default(),
        )
        .unwrap();
        let error = old
            .operation_id("send")
            .unwrap()
            .request()
            .form_fields([FormField::value("x", &"value", Limits::default()).unwrap()])
            .prepare()
            .unwrap_err();
        assert_eq!(error.code(), Code::ResourceUnavailable);
        let need = error.resource_need().unwrap();
        assert_eq!(need.requested_uri(), Some("https://docs.example/header"));
        assert_eq!(need.reference_kind(), Some(ReferenceKind::Header));
        assert!(
            need.source()
                .location()
                .pointer
                .ends_with("/headers/X-Optional/$ref")
        );
        for required in [false, true] {
            let header = format!(r#"{{"required":{required},"schema":{{"type":"string"}}}}"#);
            let mut repair = DocumentBuilder::from_document(&old);
            let header_id = repair
                .supply(
                    need,
                    input("https://docs.example/header", header.as_bytes()),
                )
                .unwrap();
            let new = repair.build(old.entry_resource().id()).unwrap();
            let result = new
                .operation_id("send")
                .unwrap()
                .request()
                .form_fields([FormField::value("x", &"value", Limits::default()).unwrap()])
                .prepare();
            if required {
                let error = result.unwrap_err();
                assert_eq!(
                    error.reason(),
                    Some(&DiagnosticReason::Form(FormReason::UnsupportedHeader))
                );
                assert_eq!(
                    error.source_context(),
                    Some(new.resource(header_id).unwrap().root().source_context())
                );
            } else {
                let request = result.unwrap();
                assert!(
                    std::str::from_utf8(request.body().map(AsRef::as_ref).unwrap())
                        .unwrap()
                        .contains("\r\n\r\nvalue\r\n")
                );
            }
        }
        assert_eq!(
            old.operation_id("send")
                .unwrap()
                .request()
                .form_fields([FormField::value("x", &"value", Limits::default()).unwrap()])
                .prepare()
                .unwrap_err()
                .code(),
            Code::ResourceUnavailable
        );
    }
}

#[test]
fn mounted_resource_need_survives_later_local_path_validation_issue() {
    let source = r#"{"openapi":"3.2.1","paths":{"/mount":{"$ref":"missing","additionalOperations":{"bad token":{}}},"/ok":{"get":{"operationId":"ok"}}}}"#;
    let doc =
        Document::parse(source, Some("https://docs.example/root"), Limits::default()).unwrap();
    assert!(!doc.discovery_complete());
    let error = doc.operation_id("ok").unwrap_err();
    assert_eq!(error.code(), Code::IncompleteDiscovery);
    assert_eq!(
        error.resource_need().unwrap().requested_uri(),
        Some("https://docs.example/missing")
    );
    assert_eq!(
        doc.graph_issues()
            .next()
            .unwrap()
            .resource_need()
            .unwrap()
            .requested_uri(),
        Some("https://docs.example/missing")
    );
}

#[test]
fn empty_anonymous_reference_is_same_document_and_still_checks_cycles() {
    for retrieval in [None, Some("https://docs.example/part")] {
        let mut builder = DocumentBuilder::new(Limits::default(), GraphLimits::default());
        let entry = builder
            .add(input(
                "https://docs.example/root",
                include_bytes!("graph-fixtures/repair-parts.json"),
            ))
            .unwrap();
        let bytes = include_bytes!("graph-fixtures/repair-empty-part.json").as_slice();
        let fragment = match retrieval {
            Some(uri) => input(uri, bytes),
            None => ResourceInput::anonymous(bytes),
        }
        .layout(ResourceLayout::new().protocol(
            "",
            ReferenceKind::PathItem,
            Edition::OpenApi32,
        ));
        let id = builder.add(fragment).unwrap();
        let document = builder.build(entry).unwrap();
        let source = document.resource(id).unwrap();
        assert_eq!(source.retrieval_uri(), retrieval);
        assert_eq!(source.description_base(), retrieval);
        let site = source.protocol_at("", ReferenceKind::PathItem).unwrap();
        let error = document.resolve_protocol(&site).unwrap_err();
        assert_eq!(error.code(), Code::ReferenceCycle);
        assert_eq!(error.source_context(), Some(source.root().source_context()));
        assert!(error.resource_need().is_none());
    }
}

#[test]
fn security_uri_edges_consume_graph_quota_but_exact_names_do_not() {
    for maximum in [0, 1] {
        let mut limits = GraphLimits::default();
        limits.reference_edges = maximum;
        let mut builder = DocumentBuilder::new(Limits::default(), limits);
        let entry = builder
            .add(input(
                "https://docs.example/root",
                include_bytes!("graph-fixtures/repair-security-edge-entry.json"),
            ))
            .unwrap();
        builder
            .add(input(
                "https://defs.example/security",
                include_bytes!("graph-fixtures/repair-security-edge-defs.json"),
            ))
            .unwrap();
        let result = builder.build(entry);
        if maximum == 0 {
            let error = result.unwrap_err();
            assert_eq!(error.code(), Code::Limit);
            assert_eq!(
                error.reason(),
                Some(&DiagnosticReason::Limit {
                    kind: LimitKind::GraphReferenceEdges,
                    maximum: 0,
                    actual: 1
                })
            );
            assert_eq!(
                error.location().unwrap().pointer,
                "/paths/~1run/get/security/0/https:~1~1defs.example~1security#~1components~1securitySchemes~1T"
            );
        } else {
            let doc = result.unwrap();
            let request = doc
                .operation("/run", Method::Get)
                .unwrap()
                .request()
                .credential(
                    "https://defs.example/security#/components/securitySchemes/T",
                    Credential::api_key("dummy", vec!["https://api.example".into()]),
                )
                .prepare()
                .unwrap();
            assert!(request.target().starts_with("https://api.example/run"));
        }
    }
    let source = r#"{"openapi":"3.2.1","servers":[{"url":"https://api.example"}],"security":[{"https://defs.example/T":[]}],"components":{"securitySchemes":{"https://defs.example/T":{"type":"apiKey","in":"header","name":"X-Auth"}}},"paths":{"/":{"get":{}}}}"#;
    let mut limits = GraphLimits::default();
    limits.reference_edges = 0;
    let mut builder = DocumentBuilder::new(Limits::default(), limits);
    let entry = builder
        .add(input("https://docs.example/root", source.as_bytes()))
        .unwrap();
    let doc = builder.build(entry).unwrap();
    assert!(
        doc.operation("/", Method::Get)
            .unwrap()
            .request()
            .credential(
                "https://defs.example/T",
                Credential::api_key("dummy", vec!["https://api.example".into()])
            )
            .prepare()
            .is_ok()
    );
}
