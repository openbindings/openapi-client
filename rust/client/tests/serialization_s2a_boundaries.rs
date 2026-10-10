use dynamic_openapi_client::*;
use serde_json::json;
use std::{
    future::Future,
    sync::Arc,
    task::{Context, Poll, Waker},
};

fn exact(text: &str) -> Value {
    ExactJson::parse(text, Limits::default()).unwrap().root()
}
fn document(parameter: serde_json::Value, limits: Limits) -> Document {
    let path = if parameter["in"] == "path" {
        "/prefix{p}"
    } else {
        "/send"
    };
    Document::parse(json!({"openapi":"3.2.1","servers":[{"url":"https://api.example"}],"paths":{path:{"get":{"operationId":"send","parameters":[parameter]}}}}).to_string(), Some("https://docs.example/root"), limits).unwrap()
}
fn ready<F: Future>(f: F) -> F::Output {
    let mut f = Box::pin(f);
    let mut context = Context::from_waker(Waker::noop());
    match f.as_mut().poll(&mut context) {
        Poll::Ready(v) => v,
        Poll::Pending => panic!("unexpected suspension"),
    }
}
fn response() -> TransportResult {
    TransportResult {
        response: Some(ResponseInput::new(
            204,
            vec![],
            DeliveredBody {
                bytes: Arc::from([]),
                state: BodyState::Complete,
                provenance: Provenance::Wire,
            },
        )),
        upload: UploadState::Complete,
        error_detail: None,
    }
}
#[test]
fn media_type_reference_recovery_is_protocol_owned_and_schema_independent() {
    let d = document(
        json!({"name":"p","in":"query","content":{"application/json":{"$ref":"media"}}}),
        Limits::default(),
    );
    let op = d.operation_id("send").unwrap();
    let input = exact(r#"{"n":1e9999}"#);
    let error = op
        .request()
        .parameter(ParameterLocation::Query, "p", input.clone())
        .prepare()
        .unwrap_err();
    let need = error.diagnostic().resource_need().unwrap();
    assert_eq!(need.reference_kind(), Some(ReferenceKind::MediaType));
    assert_eq!(need.requested_uri(), Some("https://docs.example/media"));
    let mut builder = DocumentBuilder::from_document(&d);
    builder
        .supply(
            need,
            ResourceInput::at(
                "https://docs.example/media",
                br#"{"schema":{"$ref":"missing-schema"}}"#.as_slice(),
            )
            .unwrap(),
        )
        .unwrap();
    let completed = builder.build(d.entry_resource().id()).unwrap();
    let request = completed
        .operation_id("send")
        .unwrap()
        .request()
        .parameter(ParameterLocation::Query, "p", input)
        .prepare()
        .unwrap();
    assert_eq!(
        request.target(),
        "https://api.example/send?p=%7B%22n%22%3A1e9999%7D"
    );
    assert_eq!(
        op.request().prepare().unwrap_err().diagnostic().code(),
        Code::ResourceUnavailable
    );
}
#[test]
fn credential_collision_keeps_external_scheme_source_after_parent_drop() {
    for yaml in [false, true] {
        let root=json!({"openapi":"3.2.1","servers":[{"url":"https://api.example"}],"paths":{"/send":{"get":{"operationId":"send","parameters":[{"name":"token","in":"query","content":{"text/plain":{}}}]}}},"security":[{"key":[]}],"components":{"securitySchemes":{"key":{"$ref":"security#/components/securitySchemes/External"}}}}).to_string();
        let external = if yaml {
            "openapi: 3.2.1\npaths: {}\ncomponents:\n  securitySchemes:\n    External:\n      type: apiKey\n      in: query\n      name: token\n".to_owned()
        } else {
            json!({"openapi":"3.2.1","paths":{},"components":{"securitySchemes":{"External":{"type":"apiKey","in":"query","name":"token"}}}}).to_string()
        };
        let mut builder = DocumentBuilder::new(Limits::default(), GraphLimits::default());
        let entry = builder
            .add(ResourceInput::at("https://docs.example/root", root.as_bytes()).unwrap())
            .unwrap();
        let resource = builder
            .add(ResourceInput::at("https://docs.example/security", external.as_bytes()).unwrap())
            .unwrap();
        let document = builder.build(entry).unwrap();
        let scheme = document
            .resource(resource)
            .unwrap()
            .root()
            .pointer("/components/securitySchemes/External")
            .unwrap();
        let operation = document.operation_id("send").unwrap();
        let error = operation
            .request()
            .parameter(ParameterLocation::Query, "token", "caller-private-value")
            .credential(
                "key",
                Credential {
                    value: CredentialValue::ApiKey("credential-private-value".into()),
                    origins: vec!["https://api.example".into()],
                },
            )
            .prepare()
            .unwrap_err();
        assert_ne!(scheme.source_context(), document.root().source_context());
        drop(operation);
        drop(document);
        let authored = scheme.source_trace().authored.unwrap();
        assert_eq!(error.diagnostic().code(), Code::CredentialCollision);
        assert_eq!(
            error.diagnostic().source_context(),
            Some(authored.context())
        );
        assert_eq!(error.diagnostic().location(), Some(authored.location()));
        assert!(error.diagnostic().related().is_empty());
        let routine = format!("{error} {error:?}");
        assert!(!routine.contains("caller-private-value"));
        assert!(!routine.contains("credential-private-value"));
    }
}
#[test]
fn empty_collections_and_completed_dot_segments_keep_existing_policy() {
    for style in ["matrix", "label", "spaceDelimited", "pipeDelimited"] {
        let path = matches!(style, "matrix" | "label");
        let d = document(
            json!({"name":"p","in":if path{"path"}else{"query"},"required":path,"schema":{},"style":style,"explode":false}),
            Limits::default(),
        );
        for value in ["[]", "{}"] {
            let e = d
                .operation_id("send")
                .unwrap()
                .request()
                .parameter(
                    if path {
                        ParameterLocation::Path
                    } else {
                        ParameterLocation::Query
                    },
                    "p",
                    exact(value),
                )
                .prepare()
                .unwrap_err();
            assert_eq!(
                e.diagnostic().reason(),
                Some(&DiagnosticReason::UnsupportedStyleCombination)
            );
        }
    }
    let d=Document::parse(r#"{"openapi":"3.2.1","servers":[{"url":"https://api.example"}],"paths":{"/{p}":{"get":{"operationId":"send","parameters":[{"name":"p","in":"path","required":true,"schema":{},"style":"label"}]}}}}"#,None,Limits::default()).unwrap();
    assert_eq!(
        d.operation_id("send")
            .unwrap()
            .request()
            .parameter(ParameterLocation::Path, "p", "")
            .prepare()
            .unwrap_err()
            .diagnostic()
            .code(),
        Code::InvalidDestination
    );
    for version in ["3.0.4", "3.1.2", "3.2.1"] {
        let d=Document::parse(json!({"openapi":version,"servers":[{"url":"https://api.example"}],"paths":{"/send":{"post":{"operationId":"send","requestBody":{"content":{"application/x-www-form-urlencoded":{"schema":{"type":"object","properties":{"p":{"type":"object"}}},"encoding":{"p":{"style":"pipeDelimited","explode":false}}}}}}}}}).to_string(),None,Limits::default()).unwrap();
        assert_eq!(
            d.operation_id("send")
                .unwrap()
                .request()
                .form([FormField::new("p", exact("{}"))])
                .prepare()
                .unwrap()
                .body(),
            Some(b"".as_slice())
        );
    }
}
#[test]
fn delimiters_cross_chunks_and_caller_locations_remain_exact() {
    for padding in [4094, 4095, 4096] {
        let text = format!("{}%7c", "x".repeat(padding));
        let value = ExactJson::from_serializable(&json!([text, "y"]), Limits::default())
            .unwrap()
            .root();
        let d = document(
            json!({"name":"p","in":"query","schema":{},"style":"pipeDelimited","allowReserved":true}),
            Limits::default(),
        );
        let e = d
            .operation_id("send")
            .unwrap()
            .request()
            .parameter(ParameterLocation::Query, "p", value.clone())
            .prepare()
            .unwrap_err();
        assert_eq!(
            e.diagnostic().reason(),
            Some(&DiagnosticReason::AmbiguousStyleDelimiter)
        );
        assert_eq!(
            e.diagnostic().source_context(),
            Some(value.source_context())
        );
        assert_eq!(
            e.diagnostic().location(),
            Some(&value.pointer("/0").unwrap().location())
        );
    }
}
#[test]
fn content_and_new_style_bounds_precede_invocation() {
    let p = json!({"name":"p","in":"path","required":true,"schema":{},"style":"matrix"});
    let expected = "https://api.example/prefix;p=x";
    for cap in [expected.len() - 1, expected.len()] {
        let d = document(
            p.clone(),
            Limits {
                target_bytes: cap,
                form_work: 0,
                ..Limits::default()
            },
        );
        let prepared = d
            .operation_id("send")
            .unwrap()
            .request()
            .parameter(ParameterLocation::Path, "p", "x")
            .prepare();
        if cap == expected.len() {
            assert_eq!(prepared.unwrap().target(), expected);
        } else {
            assert!(matches!(
                prepared.unwrap_err().diagnostic().reason(),
                Some(DiagnosticReason::Limit {
                    kind: LimitKind::TargetBytes,
                    ..
                })
            ));
        }
    }
    let d = document(
        json!({"name":"p","in":"query","content":{"text/plain":{}}}),
        Limits {
            form_work: 0,
            ..Limits::default()
        },
    );
    let op = d.operation_id("send").unwrap();
    let cancellation = Cancellation::default();
    cancellation.cancel();
    assert_eq!(
        op.request()
            .parameter(ParameterLocation::Query, "p", "ok")
            .prepare_with_cancellation(&cancellation)
            .unwrap_err()
            .diagnostic()
            .code(),
        Code::Cancelled
    );
    let prepared = op
        .request()
        .parameter(ParameterLocation::Query, "p", "a +")
        .prepare()
        .unwrap();
    let calls = std::cell::Cell::new(0);
    let outcome = ready(prepared.invoke(
        Cancellation::default(),
        &HostCapabilities::programmable(),
        |r, _| {
            calls.set(calls.get() + 1);
            assert_eq!(r.target, "https://api.example/send?p=a+%2B");
            async { response() }
        },
    ));
    assert_eq!(calls.get(), 1);
    assert_eq!(outcome.response.unwrap().status(), 204);
    let invalid = document(
        json!({"name":"p","in":"query","schema":{},"style":"pipeDelimited"}),
        Limits::default(),
    );
    let rejected = invalid
        .operation_id("send")
        .unwrap()
        .request()
        .parameter(ParameterLocation::Query, "p", exact(r#"["a|b"]"#))
        .prepare();
    if let Ok(prepared) = rejected {
        ready(prepared.invoke(
            Cancellation::default(),
            &HostCapabilities::programmable(),
            |_, _| async {
                calls.set(calls.get() + 1);
                response()
            },
        ));
    }
    assert_eq!(calls.get(), 1);
}
#[test]
fn ordinary_outer_object_items_share_the_32_form_path() {
    let d = Document::parse(
        json!({
            "openapi":"3.2.1", "servers":[{"url":"https://api.example"}],
            "paths":{"/send":{"post":{
                "operationId":"send",
                "requestBody":{"content":{"application/x-www-form-urlencoded":{
                    "schema":{"type":"object","properties":{"p":{
                        "type":"array","items":{"type":"object"}
                    }}},
                    "encoding":{"p":{"style":"pipeDelimited","explode":false}}
                }}}
            }}}
        })
        .to_string(),
        None,
        Limits::default(),
    )
    .unwrap();
    let first = [("x", OrdinaryValue::String("a"))];
    let second = [("y", OrdinaryValue::String("b"))];
    let items = [
        OrdinaryValue::Object(&first),
        OrdinaryValue::Object(&second),
    ];
    assert_eq!(
        d.operation_id("send")
            .unwrap()
            .request()
            .form([FormField::new("p", OrdinaryValue::Array(&items))])
            .prepare()
            .unwrap()
            .body(),
        Some(b"p=x%7Ca&p=y%7Cb".as_slice())
    );
}

#[test]
fn content_header_and_generated_node_limits_keep_existing_admission() {
    for cap in [0, 1] {
        let d = document(
            json!({"name":"p","in":"header","content":{"application/json":{}}}),
            Limits {
                headers: cap,
                ..Limits::default()
            },
        );
        let request = d
            .operation_id("send")
            .unwrap()
            .request()
            .parameter(ParameterLocation::Header, "p", exact(r#"{"n":1e9999}"#))
            .prepare();
        if cap == 1 {
            assert_eq!(request.unwrap().headers()[0].value(), br#"{"n":1e9999}"#);
        } else {
            assert!(matches!(
                request.unwrap_err().diagnostic().reason(),
                Some(DiagnosticReason::Limit {
                    kind: LimitKind::HeaderCount,
                    ..
                })
            ));
        }
    }
    let d = document(
        json!({"name":"p","in":"query","content":{"application/json":{}}}),
        Limits {
            nodes: 64,
            ..Limits::default()
        },
    );
    let values = vec![OrdinaryValue::Bool(true); 65];
    let error = d
        .operation_id("send")
        .unwrap()
        .request()
        .parameter(ParameterLocation::Query, "p", OrdinaryValue::Array(&values))
        .prepare()
        .unwrap_err();
    assert!(matches!(
        error.diagnostic().reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::Nodes,
            ..
        })
    ));
}

#[test]
fn new_form_delimiters_are_included_in_complete_output_admission() {
    let expected = b"p=x%7Ca%7Cy%7Cb";
    for cap in [expected.len() - 1, expected.len()] {
        let d = Document::parse(
            json!({
                "openapi":"3.1.2", "servers":[{"url":"https://api.example"}],
                "paths":{"/send":{"post":{
                    "operationId":"send",
                    "requestBody":{"content":{"application/x-www-form-urlencoded":{
                        "schema":{"type":"object","properties":{"p":{"type":"object"}}},
                        "encoding":{"p":{"style":"pipeDelimited","explode":false}}
                    }}}
                }}}
            })
            .to_string(),
            None,
            Limits {
                body_bytes: cap,
                ..Limits::default()
            },
        )
        .unwrap();
        let request = d
            .operation_id("send")
            .unwrap()
            .request()
            .form([FormField::new("p", exact(r#"{"x":"a","y":"b"}"#))])
            .prepare();
        if cap == expected.len() {
            assert_eq!(request.unwrap().body(), Some(expected.as_slice()));
        } else {
            assert!(matches!(
                request.unwrap_err().diagnostic().reason(),
                Some(DiagnosticReason::Limit {
                    kind: LimitKind::BodyBytes,
                    ..
                })
            ));
        }
    }
}
