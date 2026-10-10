use dynamic_openapi_client::*;
use serde_json::json;
use std::{cell::Cell, sync::Arc};

fn document(
    version: &str,
    media: &str,
    properties: serde_json::Value,
    encoding: serde_json::Value,
    limits: Limits,
) -> Document {
    let spec = if version == "2.0" {
        let parameters: Vec<_> = properties
            .as_object()
            .unwrap()
            .iter()
            .map(|(name, s)| {
                let mut p = s.clone();
                p["name"] = json!(name);
                p["in"] = json!("formData");
                p
            })
            .collect();
        json!({"swagger":version,"host":"api.example","schemes":["https"],"consumes":[media],"paths":{"/send":{"post":{"operationId":"send","parameters":parameters}}}})
    } else {
        json!({"openapi":version,"servers":[{"url":"https://api.example"}],"paths":{"/send":{"post":{"operationId":"send","requestBody":{"content":{media:{"schema":{"type":"object","properties":properties},"encoding":encoding}}}}}}})
    };
    Document::parse(
        spec.to_string(),
        Some("https://api.example/api.json"),
        limits,
    )
    .unwrap()
}
fn content_type(request: &PreparedRequest) -> &str {
    std::str::from_utf8(
        request
            .headers()
            .iter()
            .find(|h| h.name().eq_ignore_ascii_case("content-type"))
            .unwrap()
            .value(),
    )
    .unwrap()
}
#[test]
fn one_consumer_preserves_authored_editions_and_form_utf8_exact_values() {
    for version in [
        "2.0", "3.0.0", "3.0.1", "3.0.2", "3.0.3", "3.0.4", "3.1.0", "3.1.1", "3.1.2", "3.2.0",
        "3.2.1",
    ] {
        let doc = document(
            version,
            "application/x-www-form-urlencoded",
            json!({"message":{"type":"string"},"count":{"type":"integer"}}),
            json!({}),
            Limits::default(),
        );
        let number = ExactJson::parse(b"9007199254740993", Limits::default()).unwrap();
        let request = doc
            .operation_id("send")
            .unwrap()
            .request()
            .form([
                FormField::new("message", "caf\u{e9} +&=/%"),
                FormField::new("count", number.root()),
            ])
            .prepare()
            .unwrap();
        assert_eq!(doc.version().as_deref(), Some(version));
        assert_eq!(request.body_kind(), "form");
        assert_eq!(
            request.body().unwrap(),
            b"message=caf%C3%A9+%2B%26%3D%2F%25&count=9007199254740993"
        );
        assert_eq!(content_type(&request), "application/x-www-form-urlencoded");
    }
}
#[test]
fn borrowed_shared_files_and_exact_metadata_use_owned_prepared_bytes() {
    for version in ["2.0", "3.0.4", "3.1.2", "3.2.1"] {
        let file_schema = if version == "2.0" {
            json!({"type":"file"})
        } else if version.starts_with("3.0") {
            json!({"type":"string","format":"binary"})
        } else {
            json!({})
        };
        let doc = document(
            version,
            "multipart/form-data",
            json!({"file":file_schema,"label":{"type":"string"}}),
            json!({}),
            Limits::default(),
        );
        let owner: Arc<[u8]> = Arc::from(b"a\0b\xff".as_slice());
        let request = doc
            .operation_id("send")
            .unwrap()
            .request()
            .form([
                FormField::new("label", "na\u{ef}ve"),
                FormField::new(
                    "file",
                    Part::bytes(owner.clone()).filename("r\u{e9}sum\u{e9}\".bin"),
                ),
            ])
            .prepare()
            .unwrap();
        drop(owner);
        drop(doc);
        let body = request.body().unwrap();
        assert!(body.windows(4).any(|v| v == b"a\0b\xff"));
        let text = String::from_utf8_lossy(body);
        assert!(text.contains("name=\"label\"\r\nContent-Type: text/plain\r\n\r\nna\u{ef}ve"));
        assert!(text.contains("filename=\"r\u{e9}sum\u{e9}\\\".bin\""));
        assert!(text.contains("Content-Type: application/octet-stream"));
        let boundary = content_type(&request).split("boundary=").nth(1).unwrap();
        assert!(body.ends_with(format!("--{boundary}--\r\n").as_bytes()));
    }
}
#[test]
fn arrays_and_media_style_precedence_follow_minor_profiles() {
    let values = [OrdinaryValue::String("a"), OrdinaryValue::String("b")];
    for version in ["3.0.4", "3.1.2", "3.2.1"] {
        let doc = document(
            version,
            "multipart/form-data",
            json!({"values":{"type":"array","items":{"type":"string"}}}),
            json!({"values":{"explode":false,"contentType":"application/json"}}),
            Limits::default(),
        );
        let req = doc
            .operation_id("send")
            .unwrap()
            .request()
            .form([FormField::new("values", OrdinaryValue::Array(&values))])
            .prepare()
            .unwrap();
        let body = std::str::from_utf8(req.body().unwrap()).unwrap();
        if version == "3.0.4" {
            assert_eq!(body.matches("name=\"values\"").count(), 2);
            assert!(body.contains("Content-Type: application/json\r\n\r\n\"a\""));
        } else if version == "3.1.2" {
            assert_eq!(body.matches("name=\"values\"").count(), 1);
            assert!(body.contains("Content-Type: text/plain\r\n\r\na,b"));
        } else {
            assert_eq!(body.matches("name=\"values\"").count(), 2);
            assert!(body.contains("Content-Type: text/plain\r\n\r\na\r\n"));
        }
    }
}
#[test]
fn strict_header_rules_do_not_drop_supplied_values_or_optional_declarations() {
    let doc = document(
        "3.1.2",
        "multipart/form-data",
        json!({"x":{"type":"string"}}),
        json!({"x":{"headers":{"X-Optional":{"schema":{"type":"string"}}}}}),
        Limits::default(),
    );
    let op = doc.operation_id("send").unwrap();
    assert!(
        op.request()
            .form([FormField::new("x", "ok")])
            .prepare()
            .is_ok()
    );
    let headers = [("X-Optional", Argument::from("secret"))];
    let err = op
        .request()
        .form([FormField::new("x", Part::value("ok").headers(&headers))])
        .prepare()
        .unwrap_err();
    assert_eq!(
        err.diagnostic().reason(),
        Some(&DiagnosticReason::Form(FormReason::UnsupportedHeader))
    );
    assert!(!format!("{err:?}").contains("secret"));
    // Array joining must use the same header policy as scalar encoding.
    let array = ExactJson::parse(br#"["a","b"]"#, Limits::default()).unwrap();
    let doc = document(
        "3.1.2",
        "multipart/form-data",
        json!({"x":{"type":"array","items":{"type":"string"}}}),
        json!({"x":{"explode":false}}),
        Limits::default(),
    );
    assert_eq!(
        doc.operation_id("send")
            .unwrap()
            .request()
            .form([FormField::new(
                "x",
                Part::value(array.root()).headers(&headers)
            )])
            .prepare()
            .unwrap_err()
            .diagnostic()
            .reason(),
        Some(&DiagnosticReason::Form(FormReason::UnsupportedHeader))
    );
}
#[test]
fn explicit_json_needs_no_unrelated_schema_evaluation_and_keeps_number_lexemes() {
    let spec = json!({"openapi":"3.1.2","servers":[{"url":"https://api.example"}],"paths":{"/":{"post":{"operationId":"send","requestBody":{"content":{"multipart/form-data":{"schema":{"allOf":[{"$ref":"external.json"}],"required":["ignored"],"properties":{"metadata":{"$ref":"other.json"}}},"encoding":{"metadata":{"contentType":"application/json"}}}}}}}}});
    let doc = Document::parse(spec.to_string(), None, Limits::default()).unwrap();
    assert!(matches!(
        doc.operation_id("send")
            .unwrap()
            .describe()
            .unwrap()
            .request_body
            .unwrap()
            .media[0]
            .fields,
        BodyFields::Partial { .. }
    ));
    let exact = ExactJson::parse(
        br#"{"large":9007199254740993,"exp":1e+09}"#,
        Limits::default(),
    )
    .unwrap();
    let req = doc
        .operation_id("send")
        .unwrap()
        .request()
        .form([FormField::new("metadata", exact.root())])
        .prepare()
        .unwrap();
    assert!(
        std::str::from_utf8(req.body().unwrap())
            .unwrap()
            .contains(exact.root().raw())
    );
}
#[test]
fn field_iterator_stops_at_first_refusal_and_does_not_convert_later_data() {
    let doc = document(
        "3.1.2",
        "application/x-www-form-urlencoded",
        json!({"x":{"type":"string"}}),
        json!({}),
        Limits {
            form_fields: 1,
            ..Limits::default()
        },
    );
    let visits = Cell::new(0);
    let fields = (0..100).map(|i| {
        visits.set(visits.get() + 1);
        FormField::new(format!("x{i}"), "v")
    });
    let err = doc
        .operation_id("send")
        .unwrap()
        .request()
        .form(fields)
        .prepare()
        .unwrap_err();
    assert_eq!(visits.get(), 2);
    assert_eq!(limit_kind(err.diagnostic()), LimitKind::FormFields);
}
#[test]
fn emitted_metadata_and_complete_wire_are_independently_bounded() {
    let base = |limits| {
        document(
            "3.1.2",
            "application/x-www-form-urlencoded",
            json!({"x":{"type":"string"}}),
            json!({}),
            limits,
        )
    };
    let doc = base(Limits {
        body_bytes: 4,
        ..Limits::default()
    });
    let err = doc
        .operation_id("send")
        .unwrap()
        .request()
        .form([FormField::new("x", "+")])
        .prepare()
        .unwrap_err();
    assert_eq!(limit_kind(err.diagnostic()), LimitKind::BodyBytes);
    let doc = base(Limits {
        form_metadata_bytes: 10,
        ..Limits::default()
    });
    let err = doc
        .operation_id("send")
        .unwrap()
        .request()
        .form([FormField::new("x", "v")])
        .prepare()
        .unwrap_err();
    assert_eq!(limit_kind(err.diagnostic()), LimitKind::FormMetadataBytes);
    let doc = base(Limits {
        form_work: 0,
        ..Limits::default()
    });
    let err = doc
        .operation_id("send")
        .unwrap()
        .request()
        .form([FormField::new("x", "v")])
        .prepare()
        .unwrap_err();
    assert_eq!(limit_kind(err.diagnostic()), LimitKind::FormWork);
    let doc = base(Limits::default());
    let cancel = Cancellation::default();
    cancel.cancel();
    assert_eq!(
        doc.operation_id("send")
            .unwrap()
            .request()
            .form([FormField::new("x", "v")])
            .prepare_with_cancellation(&cancel)
            .unwrap_err()
            .diagnostic()
            .code(),
        Code::Cancelled
    );
}

fn limit_kind(d: &Diagnostic) -> LimitKind {
    match d.reason().unwrap() {
        DiagnosticReason::Limit { kind, .. } => *kind,
        other => panic!("expected limit: {other:?}"),
    }
}

#[test]
fn media_fields_remain_representation_specific_and_schema_required_is_a_hint() {
    let spec = json!({"openapi":"3.1.2","servers":[{"url":"https://api.example"}],"paths":{"/":{"post":{"operationId":"send","requestBody":{"content":{
        "application/x-www-form-urlencoded":{"schema":{"type":"object","properties":{"plain":{"type":"string"}},"required":["plain"]}},
        "multipart/form-data":{"schema":{"type":"object","properties":{"binary":{} }},"encoding":{"binary":{"contentType":"application/octet-stream"}}}
    }}}}}});
    let doc = Document::parse(spec.to_string(), None, Limits::default()).unwrap();
    let op = doc.operation_id("send").unwrap();
    let body = op.describe().unwrap().request_body.unwrap();
    for media in &body.media {
        let BodyFields::Declared(fields) = &media.fields else {
            panic!("declared")
        };
        if media.media_type.starts_with("application/") {
            assert_eq!(fields[0].name, "plain");
            assert_eq!(fields[0].requirement, FieldRequirement::SchemaRequired);
        } else {
            assert_eq!(fields[0].name, "binary");
            assert_eq!(
                fields[0]
                    .encoding
                    .as_ref()
                    .unwrap()
                    .get("contentType")
                    .unwrap()
                    .as_str(),
                Some("application/octet-stream")
            );
        }
    }
    assert_eq!(
        op.request()
            .form([])
            .prepare()
            .unwrap_err()
            .diagnostic()
            .code(),
        Code::AmbiguousSelection
    );
    let req = op
        .request()
        .selection(Selection {
            media: Some("application/x-www-form-urlencoded".into()),
            ..Selection::default()
        })
        .form([])
        .prepare()
        .unwrap();
    assert_eq!(req.body(), Some(b"".as_slice()));
}
#[test]
fn multipart_required_header_refusal_keeps_original_yaml_source_and_safe_context() {
    let text = "openapi: 3.1.2\nservers:\n  - url: https://api.example\npaths:\n  /:\n    post:\n      operationId: send\n      requestBody:\n        content:\n          multipart/form-data:\n            schema:\n              type: object\n              properties:\n                message: {type: string}\n            encoding:\n              message:\n                headers:\n                  X-Private:\n                    required: true\n                    schema: {type: string}\n";
    let doc = Document::parse(text, None, Limits::default()).unwrap();
    let error = doc
        .operation_id("send")
        .unwrap()
        .request()
        .form([FormField::new("message", "secret-message")])
        .prepare()
        .unwrap_err();
    let d = error.diagnostic();
    assert_eq!(
        d.reason(),
        Some(&DiagnosticReason::Form(FormReason::UnsupportedHeader))
    );
    assert!(
        d.location()
            .unwrap()
            .pointer
            .ends_with("/headers/X-Private")
    );
    assert_eq!(d.source_context().unwrap().origin, SourceOrigin::Authored);
    let location = d.location().unwrap();
    assert!(doc.source()[location.start..location.end].contains("required: true"));
    assert!(matches!(
        d.context(),
        Some(DiagnosticContext::FormField {
            index: 0,
            item: None,
            ..
        })
    ));
    assert!(!format!("{error:?}").contains("secret-message"));
    assert!(!format!("{error}").contains("X-Private"));
}
#[test]
fn form_preparation_still_requires_scoped_credentials_and_refuses_fixed_boundaries() {
    let mut spec = json!({"openapi":"3.1.2","servers":[{"url":"https://api.example"}],"components":{"securitySchemes":{"auth":{"type":"http","scheme":"bearer"}}},"security":[{"auth":[]}],"paths":{"/":{"post":{"operationId":"send","requestBody":{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"x":{"type":"string"}}}}}}}}}});
    let doc = Document::parse(spec.to_string(), None, Limits::default()).unwrap();
    let op = doc.operation_id("send").unwrap();
    assert!(
        op.request()
            .form([FormField::new("x", "v")])
            .prepare()
            .is_err()
    );
    let credential = Credential {
        value: CredentialValue::Bearer("not-logged".into()),
        origins: vec!["https://api.example".into()],
    };
    assert!(
        op.request()
            .credential("auth", credential)
            .form([FormField::new("x", "v")])
            .prepare()
            .is_ok()
    );
    spec["security"] = json!([]);
    let doc = Document::parse(spec.to_string(), None, Limits::default()).unwrap();
    let error = doc
        .operation_id("send")
        .unwrap()
        .request()
        .form([FormField::new("x", "v")])
        .selection(Selection {
            media: Some("multipart/form-data; boundary=caller".into()),
            ..Selection::default()
        })
        .prepare()
        .unwrap_err();
    assert_eq!(
        error.diagnostic().reason(),
        Some(&DiagnosticReason::Form(FormReason::FramingConflict))
    );
}
#[test]
fn admission_precedes_expensive_name_comparisons_and_counts_supplied_parts() {
    let doc = document(
        "3.1.2",
        "multipart/form-data",
        json!({"x":{"type":"string"}}),
        json!({}),
        Limits {
            form_metadata_bytes: 8,
            ..Limits::default()
        },
    );
    let long = "x".repeat(5000);
    let error = doc
        .operation_id("send")
        .unwrap()
        .request()
        .form([FormField::new("x", "v"), FormField::new(long.as_str(), "v")])
        .prepare()
        .unwrap_err();
    assert_eq!(limit_kind(error.diagnostic()), LimitKind::FormMetadataBytes);
    assert!(error.diagnostic().context_omitted_for_limit());
    let doc = document(
        "3.1.2",
        "multipart/form-data",
        json!({"x":{"type":"array","items":{"type":"string"}}}),
        json!({}),
        Limits {
            form_work: 2,
            ..Limits::default()
        },
    );
    let parts = [Part::value("a"), Part::value("b")];
    let error = doc
        .operation_id("send")
        .unwrap()
        .request()
        .form([FormField::new("x", FormValue::Parts(&parts))])
        .prepare()
        .unwrap_err();
    assert_eq!(limit_kind(error.diagnostic()), LimitKind::FormWork);
}

#[test]
fn swagger_implicit_form_media_preserves_fields_and_requires_a_choice() {
    for yaml in [false, true] {
        for declaration in [
            "absent",
            "root-empty",
            "operation-empty",
            "operation-overrides-root",
        ] {
            let mut value = json!({"swagger":"2.0","host":"api.example","schemes":["https"],"paths":{"/send":{"post":{"operationId":"send","parameters":[{"in":"formData","name":"message","type":"string","required":true}]}}}});
            match declaration {
                "root-empty" => value["consumes"] = json!([]),
                "operation-empty" => value["paths"]["/send"]["post"]["consumes"] = json!([]),
                "operation-overrides-root" => {
                    value["consumes"] = json!(["application/json"]);
                    value["paths"]["/send"]["post"]["consumes"] = json!([]);
                }
                _ => (),
            }
            let source = format!("{}{}", if yaml { "---\n" } else { "" }, value);
            let doc = Document::parse(source, None, Limits::default()).unwrap();
            let op = doc.operation_id("send").unwrap();
            let body = op.describe().unwrap().request_body.unwrap();
            assert!(!body.media_unrestricted);
            assert_eq!(body.media.len(), 2);
            for representation in &body.media {
                let BodyFields::Declared(fields) = &representation.fields else {
                    panic!("known form fields")
                };
                assert_eq!(fields.len(), 1);
                assert_eq!(fields[0].name, "message");
                assert_eq!(fields[0].requirement, FieldRequirement::ProtocolRequired);
                assert_eq!(
                    representation.authored.source_context(),
                    doc.root().source_context()
                );
            }
            assert_eq!(
                op.request()
                    .form([FormField::new("message", "a b")])
                    .prepare()
                    .unwrap_err()
                    .diagnostic()
                    .code(),
                Code::AmbiguousSelection
            );
            let json_body = ExactJson::parse(br#"{"message":"a b"}"#, Limits::default()).unwrap();
            assert_eq!(
                op.request()
                    .json(json_body.root())
                    .prepare()
                    .unwrap_err()
                    .diagnostic()
                    .code(),
                Code::UnsupportedMedia
            );
            for media in ["application/x-www-form-urlencoded", "multipart/form-data"] {
                let request = op
                    .request()
                    .selection(Selection {
                        media: Some(media.into()),
                        ..Selection::default()
                    })
                    .form([FormField::new("message", "a b")])
                    .prepare()
                    .unwrap();
                if media.starts_with("application/") {
                    assert_eq!(request.body(), Some(b"message=a+b".as_slice()));
                } else {
                    assert!(
                        String::from_utf8_lossy(request.body().unwrap()).contains(
                            "name=\"message\"\r\nContent-Type: text/plain\r\n\r\na b\r\n"
                        )
                    );
                }
                let error = op
                    .request()
                    .selection(Selection {
                        media: Some(media.into()),
                        ..Selection::default()
                    })
                    .form(std::iter::empty::<FormField<'static>>())
                    .prepare()
                    .unwrap_err();
                assert_eq!(error.diagnostic().code(), Code::MissingInput);
            }
        }
    }
}

#[test]
fn swagger_form_parameters_cannot_be_sent_as_json_even_when_consumes_is_invalid() {
    let doc = document(
        "2.0",
        "application/json",
        json!({"message":{"type":"string"}}),
        json!({}),
        Limits::default(),
    );
    let op = doc.operation_id("send").unwrap();
    let payload = ExactJson::parse(br#"{"message":"hi"}"#, Limits::default()).unwrap();
    assert_eq!(
        op.request()
            .json(payload.root())
            .prepare()
            .unwrap_err()
            .diagnostic()
            .code(),
        Code::UnsupportedMedia
    );
    assert_eq!(
        op.request()
            .selection(Selection {
                media: Some("application/json".into()),
                ..Selection::default()
            })
            .body(Body::Raw(b"{}".as_slice().into()))
            .prepare()
            .unwrap_err()
            .diagnostic()
            .code(),
        Code::UnsupportedMedia
    );
}
