use dynamic_openapi_client::*;
use serde_json::json;

fn parameter(version: &str, location: &str, name: &str) -> serde_json::Value {
    if version == "2.0" {
        json!({"in":location,"name":name,"type":"string"})
    } else {
        json!({"in":location,"name":name,"schema":{"type":"string"}})
    }
}

fn document(
    version: &str,
    path_parameters: Vec<serde_json::Value>,
    operation_parameters: Vec<serde_json::Value>,
    limits: Limits,
) -> Document {
    let mut source = json!({"paths":{"/":{"parameters":path_parameters,"get":{
        "operationId":"read","parameters":operation_parameters
    }}}});
    if version == "2.0" {
        source["swagger"] = json!(version);
        source["host"] = json!("api.example");
        source["schemes"] = json!(["https"]);
    } else {
        source["openapi"] = json!(version);
        source["servers"] = json!([{"url":"https://api.example"}]);
    }
    Document::parse(source.to_string(), None, limits).unwrap()
}

#[test]
fn overrides_keep_source_order_provenance_and_location_identity() {
    for version in ["2.0", "3.0.4", "3.1.2", "3.2.1"] {
        let doc = document(
            version,
            vec![
                parameter(version, "query", "first"),
                parameter(version, "header", "X-Trace"),
                parameter(version, "query", "keep"),
                parameter(version, "query", "Case"),
            ],
            vec![
                parameter(version, "query", "new"),
                parameter(version, "header", "x-TRACE"),
                parameter(version, "query", "first"),
                parameter(version, "query", "case"),
                parameter(version, "header", "first"),
            ],
            Limits::default(),
        );
        let op = doc.operation_id("read").unwrap();
        for parameters in [op.parameters().unwrap(), op.describe().unwrap().parameters] {
            let parameters: Vec<_> = parameters.iter().collect();
            let names: Vec<_> = parameters.iter().map(Parameter::name).collect();
            assert_eq!(
                names,
                ["first", "x-TRACE", "keep", "Case", "new", "case", "first"]
            );
            assert_eq!(
                parameters[0].authored().location().pointer,
                "/paths/~1/get/parameters/2"
            );
            assert_eq!(
                parameters[1].authored().location().pointer,
                "/paths/~1/get/parameters/1"
            );
            assert_eq!(
                parameters[2].authored().location().pointer,
                "/paths/~1/parameters/2"
            );
            assert_eq!(parameters[6].location(), ParameterLocation::Header);
        }
        let prepared = op.prepare(&Input::default()).unwrap();
        assert_eq!(prepared.target(), "https://api.example/");
        assert_eq!(op.parameters().unwrap().iter().len(), 7);
    }
}

#[test]
fn duplicate_identity_in_either_list_is_still_refused() {
    for version in ["2.0", "3.0.4", "3.1.2", "3.2.1"] {
        for location in ["query", "header"] {
            for at_operation in [false, true] {
                let duplicates = vec![
                    parameter(version, location, "X-Trace"),
                    parameter(
                        version,
                        location,
                        if location == "header" {
                            "x-TRACE"
                        } else {
                            "X-Trace"
                        },
                    ),
                ];
                let (path, operation) = if at_operation {
                    // Also exercises a repeated operation override of a path entry.
                    (vec![parameter(version, location, "X-Trace")], duplicates)
                } else {
                    (duplicates, vec![parameter(version, location, "X-Trace")])
                };
                let doc = document(version, path, operation, Limits::default());
                let op = doc.operation_id("read").unwrap();
                let expected = if at_operation {
                    "/paths/~1/get/parameters/1"
                } else {
                    "/paths/~1/parameters/1"
                };
                for error in [
                    op.parameters().unwrap_err(),
                    op.describe().unwrap_err(),
                    op.prepare(&Input::default()).unwrap_err(),
                ] {
                    assert_eq!(error.code(), Code::InvalidDeclaration);
                    assert_eq!(error.location().unwrap().pointer, expected);
                }
            }
        }
    }
}

#[test]
fn resolved_overrides_retain_the_operation_use_site_and_target() {
    let doc = Document::parse(json!({"openapi":"3.1.2","paths":{"/":{"parameters":[{"$ref":"#/components/parameters/path"}],"get":{"operationId":"read","parameters":[{"$ref":"#/components/parameters/operation"}]}}},"components":{"parameters":{
        "path":{"in":"header","name":"X-Trace","schema":{"type":"string"}},
        "operation":{"in":"header","name":"x-TRACE","schema":{"type":"integer"}}
    }}}).to_string(), None, Limits::default()).unwrap();
    let op = doc.operation_id("read").unwrap();
    let parameters = op.parameters().unwrap();
    assert_eq!(parameters.iter().len(), 1);
    let p = parameters.iter().next().unwrap();
    assert_eq!(
        p.authored().location().pointer,
        "/paths/~1/get/parameters/0"
    );
    assert_eq!(
        p.resolved().target.location().pointer,
        "/components/parameters/operation"
    );
    assert_eq!(
        p.schema().unwrap().get("type").unwrap().as_str(),
        Some("integer")
    );
}

#[test]
fn parameter_map_and_declaration_visits_are_budgeted() {
    for maximum in [1, 2] {
        let doc = document(
            "3.1.2",
            vec![],
            vec![parameter("3.1.2", "query", "q")],
            Limits {
                descriptor_work: maximum,
                ..Limits::default()
            },
        );
        let error = doc.operation_id("read").unwrap().parameters().unwrap_err();
        assert_eq!(error.code(), Code::Limit);
        assert_eq!(
            error.reason(),
            Some(&DiagnosticReason::Limit {
                kind: LimitKind::DescriptorWork,
                maximum,
                actual: maximum + 1
            })
        );
        assert_eq!(
            error.location().unwrap().pointer,
            "/paths/~1/get/parameters/0"
        );
    }
}

#[test]
fn parameter_inspection_remains_available_after_request_declaration_failure() {
    let doc = Document::parse(
        json!({"openapi":"3.1.2","paths":{"/":{"post":{"operationId":"read",
            "parameters":[parameter("3.1.2", "query", "q")], "requestBody":{"$ref":42}
        }}}})
        .to_string(),
        None,
        Limits::default(),
    )
    .unwrap();
    let op = doc.operation_id("read").unwrap();
    assert_eq!(op.parameters().unwrap().iter().len(), 1);
    assert_eq!(
        op.prepare(&Input::default()).unwrap_err().code(),
        Code::InvalidReference
    );
    assert_eq!(op.parameters().unwrap().iter().len(), 1);
}

#[test]
fn cache_disabled_still_merges_and_prepares() {
    let doc = document(
        "3.1.2",
        vec![parameter("3.1.2", "header", "X-Trace")],
        vec![parameter("3.1.2", "header", "x-TRACE")],
        Limits {
            cached_operations: 0,
            ..Limits::default()
        },
    );
    let op = doc.operation_id("read").unwrap();
    for _ in 0..2 {
        assert_eq!(
            op.parameters().unwrap().iter().next().unwrap().name(),
            "x-TRACE"
        );
        op.prepare(&Input::default()).unwrap();
        assert_eq!(doc.cached_operations(), 0);
    }
}
