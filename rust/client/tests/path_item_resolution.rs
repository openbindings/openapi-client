//! Consumer regressions for Path Item reference locality. These exercise both
//! inspection and request preparation; finding a raw operation alone is insufficient.
use dynamic_openapi_client::*;
use serde_json::{Value as Json, json};

fn document(version: &str, paths: Json, items: Json, limits: Limits) -> Document {
    let mut source = json!({"paths": paths, "components": {"pathItems": items},
        "servers": [{"url": "https://api.example"}]});
    if version == "2.0" {
        source["swagger"] = json!(version);
        source["host"] = json!("api.example");
        source["schemes"] = json!(["https"]);
    } else {
        source["openapi"] = json!(version);
    }
    Document::parse(
        source.to_string(),
        Some("https://docs.example/root"),
        limits,
    )
    .unwrap()
}
fn resolved(doc: &Document, path: &str) -> Result<ResolvedReference, Diagnostic> {
    doc.resolve_protocol(&doc.protocol_at(path, ReferenceKind::PathItem).unwrap())
}
fn prepared(doc: &Document, method: Method) -> PreparedRequest {
    let operation = doc.operation("/x", method.clone()).unwrap();
    operation.describe().unwrap();
    let request = operation.prepare(&Input::default()).unwrap();
    assert_eq!(request.method(), method);
    assert_eq!(request.target(), "https://api.example/x");
    request
}

#[test]
fn unknown_members_do_not_hide_direct_or_referenced_methods() {
    for version in ["2.0", "3.0.4", "3.1.2", "3.2.1"] {
        // Referencing another standard path keeps the contrast valid in every edition.
        let doc = document(
            version,
            json!({
                "/x": {"$ref":"#/paths/~1definition", "future":true, "x-note":1},
                "/definition":{"get":{"operationId":"read"},"future":null,"x-note":2}
            }),
            json!({}),
            Limits::default(),
        );
        assert!(doc.discovery_complete());
        assert_eq!(doc.graph_issues().count(), 0);
        let path = resolved(&doc, "/paths/~1x").unwrap();
        assert!(path.path_item_conflicts.is_empty());
        assert_eq!(path.chain.len(), 2);
        assert_eq!(
            path.authored.get("future").unwrap().kind(),
            ValueKind::Boolean
        );
        assert_eq!(path.target.get("future").unwrap().kind(), ValueKind::Null);
        prepared(&doc, Method::Get);
        doc.operation("/definition", Method::Get)
            .unwrap()
            .request()
            .prepare()
            .unwrap();
    }
}

#[test]
fn method_conflicts_preserve_other_methods_and_all_operation_ids() {
    let doc = document(
        "3.1.2",
        json!({
            "/x":{"$ref":"#/components/pathItems/P","get":{"operationId":"outer"}},
            "/other":{"get":{"operationId":"inner"}}
        }),
        json!({"P":{"get":{"operationId":"inner"},"post":{"operationId":"create"}}}),
        Limits::default(),
    );
    assert!(doc.discovery_complete());
    let path = resolved(&doc, "/paths/~1x").unwrap();
    assert_eq!(path.path_item_conflicts.len(), 1);
    let conflict = &path.path_item_conflicts[0];
    assert_eq!(conflict.method, Method::Get);
    assert_eq!(conflict.diagnostic.code(), Code::AmbiguousReference);
    assert_eq!(
        conflict.diagnostic.location().unwrap().pointer,
        "/components/pathItems/P/get"
    );
    assert_eq!(doc.graph_issues().count(), 1);
    assert!(doc.operations().any(|op| op.method() == Method::Post));
    prepared(&doc, Method::Post);
    assert_eq!(
        doc.operation_id("create")
            .unwrap()
            .request()
            .prepare()
            .unwrap()
            .method(),
        Method::Post
    );
    let ambiguous = doc.operation("/x", Method::Get).unwrap();
    assert_eq!(
        ambiguous.prepare(&Input::default()).unwrap_err().code(),
        Code::AmbiguousReference
    );
    assert_eq!(
        ambiguous.describe().unwrap_err().code(),
        Code::AmbiguousReference
    );
    assert_eq!(
        doc.operation_id("outer").unwrap_err().code(),
        Code::AmbiguousReference
    );
    // An ID in the non-preferred declaration still prevents a false unique match.
    assert_eq!(
        doc.operation_id("inner").unwrap_err().code(),
        Code::AmbiguousOperation
    );
    assert_eq!(
        doc.operation("/x", Method::Delete).unwrap_err().code(),
        Code::MissingOperation
    );
}

#[test]
fn repeated_conflicts_are_bounded_and_ids_never_select_a_winner() {
    let doc = document(
        "3.1.2",
        json!({"/x":{"$ref":"#/components/pathItems/P","get":{"operationId":"same"}}}),
        json!({"P":{"$ref":"#/components/pathItems/Q","get":{"operationId":"same"}},
            "Q":{"get":{"operationId":"third"},"post":{}}}),
        Limits::default(),
    );
    let path = resolved(&doc, "/paths/~1x").unwrap();
    assert_eq!(path.chain.len(), 3);
    assert_eq!(path.path_item_conflicts.len(), 1);
    assert_eq!(doc.operations().count(), 2);
    for id in ["same", "third"] {
        assert_eq!(
            doc.operation_id(id).unwrap_err().code(),
            Code::AmbiguousReference
        );
    }
    prepared(&doc, Method::Post);
}

#[test]
fn external_conflict_evidence_retains_the_declaring_source_and_shared_parameters() {
    let mut builder = DocumentBuilder::new(Limits::default(), GraphLimits::default());
    let entry = builder
        .add(
            ResourceInput::at(
                "https://docs.example/root",
                br#"{
        "openapi":"3.1.2", "servers":[{"url":"https://api.example"}],
        "paths":{"/x":{"$ref":"parts", "get":{},
            "parameters":[{"name":"q","in":"query","schema":{"type":"string"}}]}}}"#
                    .as_slice(),
            )
            .unwrap(),
        )
        .unwrap();
    builder
        .add(
            ResourceInput::at(
                "https://docs.example/parts",
                br#"{"get":{},"post":{"operationId":"create"}}"#.as_slice(),
            )
            .unwrap(),
        )
        .unwrap();
    let doc = builder.build(entry).unwrap();
    let path = resolved(&doc, "/paths/~1x").unwrap();
    let error = path.path_item_conflicts[0].diagnostic.clone();
    assert_eq!(error.location().unwrap().pointer, "/get");
    assert_eq!(error.source_context(), Some(path.target.source_context()));
    assert_ne!(error.source_context(), Some(path.authored.source_context()));
    let request = doc
        .operation_id("create")
        .unwrap()
        .request()
        .parameter(ParameterLocation::Query, "q", "visible")
        .prepare()
        .unwrap();
    assert_eq!(request.target(), "https://api.example/x?q=visible");
    drop(path);
    drop(doc);
    assert_eq!(error.source_position().unwrap().raw(), "{}");
}

#[test]
fn recognized_shared_conflicts_still_block_the_whole_mount() {
    for field in ["summary", "description", "parameters", "servers"] {
        let value = if matches!(field, "parameters" | "servers") {
            json!([])
        } else {
            json!("same")
        };
        let doc = document(
            "3.1.2",
            json!({"/x":{"$ref":"#/components/pathItems/P",field:value}}),
            json!({"P":{field:value,"get":{},"post":{}}}),
            Limits::default(),
        );
        assert!(!doc.discovery_complete());
        assert_eq!(
            resolved(&doc, "/paths/~1x").unwrap_err().code(),
            Code::AmbiguousReference
        );
        assert_eq!(
            doc.operation("/x", Method::Post).unwrap_err().code(),
            Code::AmbiguousReference
        );
    }
    // Preserve the current conservative policy for duplicate 3.2 extension maps.
    let doc = document(
        "3.2.1",
        json!({"/x":{"$ref":"#/components/pathItems/P","additionalOperations":{}}}),
        json!({"P":{"additionalOperations":{},"post":{}}}),
        Limits::default(),
    );
    assert_eq!(
        resolved(&doc, "/paths/~1x").unwrap_err().code(),
        Code::AmbiguousReference
    );
}

#[test]
fn conflict_recognition_respects_edition_boundaries() {
    for version in ["3.0.4", "3.1.2"] {
        let doc = document(
            version,
            json!({"/x":{"$ref":"#/paths/~1definition","query":{},"additionalOperations":{}}}),
            json!({}),
            Limits::default(),
        );
        let mut source: Json = serde_json::from_str(doc.source()).unwrap();
        source["paths"]["/definition"] = json!({"query":{},"additionalOperations":{},"post":{}});
        let doc = Document::parse(source.to_string(), None, Limits::default()).unwrap();
        assert!(
            resolved(&doc, "/paths/~1x")
                .unwrap()
                .path_item_conflicts
                .is_empty()
        );
        prepared(&doc, Method::Post);
        // Later-edition fields remain visible as authored values, but are not
        // operations of this edition and cannot enter its semantic inventory.
        assert!(doc.root().pointer("/paths/~1x/query").is_ok());
        assert!(doc.root().pointer("/paths/~1definition/query").is_ok());
        assert_eq!(
            doc.operation("/x", Method::Query).unwrap_err().code(),
            Code::MissingOperation
        );
    }
    let doc = document(
        "3.2.1",
        json!({"/x":{"$ref":"#/components/pathItems/P","query":{}}}),
        json!({"P":{"query":{},"post":{}}}),
        Limits::default(),
    );
    assert_eq!(
        resolved(&doc, "/paths/~1x").unwrap().path_item_conflicts[0].method,
        Method::Query
    );
    prepared(&doc, Method::Post);
}

#[test]
fn missing_cycles_wrong_kinds_editions_and_depth_remain_resolution_failures() {
    for (reference, items, expected) in [
        ("missing", json!({}), Code::ResourceUnavailable),
        ("#/paths/~1x", json!({}), Code::ReferenceCycle),
        (
            "#/components/pathItems/P",
            json!({"P":null}),
            Code::WrongReferenceKind,
        ),
    ] {
        let doc = document(
            "3.1.2",
            json!({"/x":{"$ref":reference,"get":{},"future":true}}),
            items,
            Limits::default(),
        );
        let error = resolved(&doc, "/paths/~1x").unwrap_err();
        assert_eq!(error.code(), expected);
        if expected == Code::ResourceUnavailable {
            assert_eq!(
                error.resource_need().unwrap().requested_uri(),
                Some("https://docs.example/missing")
            );
        }
        assert!(!doc.discovery_complete());
        assert_eq!(
            doc.operation("/x", Method::Get)
                .unwrap()
                .prepare(&Input::default())
                .unwrap_err()
                .code(),
            expected
        );
    }
    let doc = document(
        "3.1.2",
        json!({"/x":{"$ref":"#/components/pathItems/P","get":{}}}),
        json!({"P":{"get":{},"post":{}}}),
        Limits {
            reference_steps: 0,
            ..Limits::default()
        },
    );
    assert_eq!(
        resolved(&doc, "/paths/~1x").unwrap_err().code(),
        Code::Limit
    );
    let mut builder = DocumentBuilder::new(Limits::default(), GraphLimits::default());
    let entry = builder
        .add(
            ResourceInput::at(
                "https://docs.example/root",
                br#"{"openapi":"3.1.2","paths":{"/x":{"$ref":"other#/paths/~1x"}}}"#.as_slice(),
            )
            .unwrap(),
        )
        .unwrap();
    builder
        .add(
            ResourceInput::at(
                "https://docs.example/other",
                br#"{"openapi":"3.0.4","paths":{"/x":{"get":{}}}}"#.as_slice(),
            )
            .unwrap(),
        )
        .unwrap();
    let doc = builder.build(entry).unwrap();
    assert_eq!(
        resolved(&doc, "/paths/~1x").unwrap_err().code(),
        Code::ReferencedEditionMismatch
    );
}

#[test]
fn duplicate_declarations_do_not_spend_the_mounted_operation_budget_twice() {
    let source = br##"{"openapi":"3.1.2","servers":[{"url":"https://api.example"}],"paths":{"/x":{"$ref":"#/components/pathItems/P","get":{"operationId":"outer"}}},"components":{"pathItems":{"P":{"get":{"operationId":"inner"},"post":{}}}}}"##;
    let doc = Document::parse(
        source,
        None,
        Limits {
            operations: 2,
            ..Limits::default()
        },
    )
    .unwrap();
    assert_eq!(doc.operations().count(), 2);
    prepared(&doc, Method::Post);
    assert_eq!(
        Document::parse(
            source,
            None,
            Limits {
                operations: 1,
                ..Limits::default()
            }
        )
        .unwrap_err()
        .diagnostic()
        .code(),
        Code::Limit
    );
}

#[test]
fn repeated_ids_across_many_mounts_preserve_global_ambiguity() {
    let mut paths = serde_json::Map::new();
    for i in 0..1024 {
        paths.insert(
            format!("/m{i}"),
            json!({"$ref":"#/components/pathItems/P","get":{"operationId":"same"}}),
        );
    }
    let doc = document(
        "3.1.2",
        Json::Object(paths),
        json!({"P":{"get":{"operationId":"same"},"post":{"operationId":"shared"}}}),
        Limits {
            operations: 2048,
            ..Limits::default()
        },
    );
    assert!(doc.discovery_complete());
    assert_eq!(doc.operations().count(), 2048);
    for id in ["same", "shared"] {
        assert_eq!(
            doc.operation_id(id).unwrap_err().code(),
            Code::AmbiguousOperation
        );
    }
    assert_eq!(
        doc.operation("/m1023", Method::Post)
            .unwrap()
            .prepare(&Input::default())
            .unwrap()
            .target(),
        "https://api.example/m1023"
    );
}
