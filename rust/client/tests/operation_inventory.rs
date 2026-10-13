use dynamic_openapi_client::{Code, Document, Input, Limits, Method};
use serde_json::{Value as Json, json};

fn source(version: &str, paths: Json) -> Json {
    let mut source = json!({
        "info": {"title": "Operation inventory", "version": "1"},
        "paths": paths
    });
    if version == "2.0" {
        source["swagger"] = json!(version);
        source["host"] = json!("api.example");
        source["schemes"] = json!(["https"]);
    } else {
        source["openapi"] = json!(version);
        source["servers"] = json!([{"url": "https://api.example"}]);
    }
    source
}

fn later_members(version: &str) -> Json {
    let operation = json!({"operationId": "same", "responses": {"200": {"description": "ok"}}});
    let mut item = json!({
        "get": operation,
        "query": operation,
        "additionalOperations": {"COPY": operation}
    });
    if version == "2.0" {
        item["trace"] = operation;
    }
    item
}

fn assert_earlier_inventory(version: &str) {
    let doc = Document::parse(
        source(version, json!({"/items": later_members(version)})).to_string(),
        None,
        Limits::default(),
    )
    .unwrap();
    assert_eq!(
        doc.operations().map(|op| op.method()).collect::<Vec<_>>(),
        [Method::Get]
    );
    let get = doc.operation_id("same").unwrap();
    assert_eq!(get.method(), Method::Get);
    assert_eq!(
        get.prepare(&Input::default()).unwrap().target(),
        "https://api.example/items"
    );
    assert!(doc.discovery_complete());
    assert_eq!(doc.graph_issues().count(), 0);
    let item = doc.root().pointer("/paths/~1items").unwrap();
    for (pointer, method) in [
        ("/query", Method::Query),
        (
            "/additionalOperations/COPY",
            Method::from_token("COPY").unwrap(),
        ),
    ]
    .into_iter()
    .chain((version == "2.0").then_some(("/trace", Method::Trace)))
    {
        assert_eq!(
            item.pointer(pointer)
                .unwrap()
                .get("operationId")
                .unwrap()
                .as_str(),
            Some("same")
        );
        assert_eq!(
            doc.operation("/items", method).unwrap_err().code(),
            Code::MissingOperation
        );
    }
}

#[test]
fn swagger20_inventory_ignores_query_trace_and_additional_operations() {
    assert_earlier_inventory("2.0");
}

#[test]
fn openapi30_inventory_ignores_query_and_additional_operations() {
    assert_earlier_inventory("3.0.4");
}

#[test]
fn openapi31_inventory_ignores_query_and_additional_operations() {
    assert_earlier_inventory("3.1.2");
}

#[test]
fn openapi32_indexes_query_and_additional_operations_and_refuses_duplicate_ids() {
    let methods = [
        Method::Get,
        Method::Query,
        Method::from_token("COPY").unwrap(),
    ];
    // Check each pair as well as the three-way collision so omitting any one
    // method from the ID index cannot be hidden by the other two colliding.
    for duplicate_members in [vec![], vec![0, 1], vec![0, 2], vec![1, 2], vec![0, 1, 2]] {
        let mut item = later_members("3.2.1");
        for (index, pointer) in ["/get", "/query", "/additionalOperations/COPY"]
            .into_iter()
            .enumerate()
        {
            item.pointer_mut(pointer).unwrap()["operationId"] =
                if duplicate_members.contains(&index) {
                    json!("same")
                } else {
                    json!(methods[index].as_str())
                };
        }
        let doc = Document::parse(
            source("3.2.1", json!({"/items": item})).to_string(),
            None,
            Limits::default(),
        )
        .unwrap();
        assert_eq!(doc.operations().count(), 3);
        for method in &methods {
            let op = doc.operation("/items", method.clone()).unwrap();
            assert_eq!(op.prepare(&Input::default()).unwrap().method(), *method);
            if duplicate_members.is_empty() {
                assert_eq!(doc.operation_id(method.as_str()).unwrap().method(), *method);
            }
        }
        if !duplicate_members.is_empty() {
            assert_eq!(
                doc.operation_id("same").unwrap_err().code(),
                Code::AmbiguousOperation
            );
        }
    }
}

#[test]
fn inert_members_do_not_consume_the_operation_limit() {
    let limits = Limits {
        operations: 1,
        ..Limits::default()
    };
    for version in ["2.0", "3.0.4", "3.1.2"] {
        let mut source = source(version, json!({"/items": later_members(version)}));
        let doc = Document::parse(source.to_string(), None, limits.clone()).unwrap();
        assert_eq!(doc.operations().count(), 1);
        assert_eq!(doc.operation_id("same").unwrap().method(), Method::Get);
        source["paths"]["/items"]["post"] = json!({"responses": {"200": {"description": "ok"}}});
        assert_eq!(
            Document::parse(source.to_string(), None, limits.clone())
                .unwrap_err()
                .diagnostic()
                .code(),
            Code::Limit
        );
    }
    assert_eq!(
        Document::parse(
            source("3.2.1", json!({"/items": later_members("3.2.1")})).to_string(),
            None,
            limits
        )
        .unwrap_err()
        .diagnostic()
        .code(),
        Code::Limit
    );
}

#[test]
fn inert_fields_in_reference_chains_do_not_conflict_or_add_id_evidence() {
    for version in ["2.0", "3.0.4", "3.1.2"] {
        let mut outer = later_members(version);
        outer["$ref"] = json!("#/paths/~1definition");
        let mut inner = later_members(version);
        inner.as_object_mut().unwrap().remove("get");
        inner["post"] = json!({"responses": {"200": {"description": "ok"}}});
        let doc = Document::parse(
            source(version, json!({"/items": outer, "/definition": inner})).to_string(),
            None,
            Limits {
                operations: 3,
                ..Limits::default()
            },
        )
        .unwrap();
        assert_eq!(doc.operations().count(), 3);
        assert_eq!(doc.operation_id("same").unwrap().method(), Method::Get);
        assert!(doc.discovery_complete());
        assert_eq!(doc.graph_issues().count(), 0);
        assert_eq!(
            doc.operation("/items", Method::Post)
                .unwrap()
                .prepare(&Input::default())
                .unwrap()
                .method(),
            Method::Post
        );
    }
}
