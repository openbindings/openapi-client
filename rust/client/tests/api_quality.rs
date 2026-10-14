#[path = "support/request_case.rs"]
mod request_case;
use dynamic_openapi_client::*;
use dynamic_openapi_client::{diagnostics::*, exact::*, inspect::*, source::*};
use request_case::*;
use std::error::Error;

fn parse(text: &str) -> Document {
    Document::parse(text, None, Limits::default()).unwrap()
}

#[test]
fn parameter_views_share_resolution_override_and_retained_sources() {
    let text = r##"{"openapi":"3.1.2","paths":{"/items/{id}":{
      "parameters":[{"$ref":"#/components/parameters/Id","description":"use site"},
       {"name":"X-Key","in":"header","schema":{"type":"integer"}}],
      "get":{"operationId":"get","parameters":[
       {"name":"x-key","in":"header","schema":{"type":"string"}},
       {"name":"content","in":"query","content":{"application/json":{"schema":{}}}}],
       "requestBody":{"$ref":"https://outside.invalid/body"},"responses":17,"security":42}}},
      "components":{"parameters":{"Id":{"name":"id","in":"path","required":true,
      "schema":{"x-exact":9007199254740993}}}}}"##;
    let document = parse(text);
    let operation = document.operation_id("get").unwrap();
    let parameters = operation.parameters().unwrap();
    drop(document);
    drop(operation);
    let parameters: Vec<_> = parameters.iter().collect();
    assert_eq!(
        parameters.iter().map(Parameter::name).collect::<Vec<_>>(),
        ["id", "x-key", "content"]
    );
    assert!(parameters[0].required());
    assert!(!parameters[1].required());
    assert_eq!(
        parameters[0]
            .schema_description()
            .map(|s| s.value)
            .unwrap()
            .get("x-exact")
            .unwrap()
            .raw(),
        "9007199254740993"
    );
    let resolved = parameters[0].resolved();
    assert!(parameters[0].authored().get("$ref").is_some());
    assert!(resolved.target.get("$ref").is_none());
    assert_eq!(resolved.description.unwrap().as_str(), Some("use site"));
    assert_eq!(resolved.chain.len(), 2);
    assert_eq!(
        resolved.authored.source_context(),
        resolved.target.source_context()
    );
    assert!(
        parameters[2]
            .schema_description()
            .map(|s| s.value)
            .is_none()
    );
    assert!(parameters[2].resolved().target.get("content").is_some());
}

#[test]
fn parameter_views_refuse_invalid_typed_facts_without_losing_raw_inspection() {
    for (version, location, required, expected) in [
        ("3.3.0", "query", "", Code::UnsupportedVersion),
        ("3.1.0", "path", "", Code::InvalidDeclaration),
        (
            "3.1.0",
            "path",
            ",\"required\":false",
            Code::InvalidDeclaration,
        ),
        (
            "3.1.0",
            "query",
            ",\"required\":null",
            Code::InvalidDeclaration,
        ),
    ] {
        let text = format!(
            r#"{{"openapi":"{version}","paths":{{"/{{id}}":{{"get":{{"operationId":"get","parameters":[{{"name":"id","in":"{location}"{required},"schema":{{}}}}]}}}}}}}}"#
        );
        let operation = parse(&text).operation_id("get").unwrap();
        let error = operation.parameters().unwrap_err();
        assert_eq!(error.code(), expected);
        assert!(error.location().is_some());
        assert!(operation.inspect().parameters.is_some());
    }
}

#[test]
fn immutable_parse_pair_still_composes_as_standard_error() {
    fn owned_error<E: Error + Send + Sync + 'static>(_: &E) {}
    let error = Document::parse(b"{", None, Limits::default()).unwrap_err();
    owned_error(&error);
    assert_eq!(error.source_bytes(), b"{");
    assert_eq!(error.code(), Code::InvalidJson);
    assert!(error.source().is_none());
    let too_large = Document::parse(
        b"{}",
        None,
        Limits {
            document_bytes: 1,
            ..Limits::default()
        },
    )
    .unwrap_err();
    assert!(too_large.source_bytes().is_empty());
    assert_eq!(too_large.code(), Code::Limit);
}

const REQUEST_DOC: &str = r#"{"openapi":"3.1.2","servers":[{"url":"https://{host}/{base}","variables":{"host":{"default":"api.example"},"base":{"default":"v1"}}}],"paths":{"/items/{id}":{"post":{"operationId":"post","parameters":[{"name":"id","in":"path","required":true,"schema":{}},{"name":"q","in":"query","schema":{}},{"name":"X-Key","in":"header","schema":{}}],"requestBody":{"content":{"application/json":{}}},"responses":{"200":{"description":"ok"}},"security":[{}, {"auth":[]}]}}},"components":{"securitySchemes":{"auth":{"type":"http","scheme":"bearer"}}}}"#;
fn request_operation(limits: Limits) -> Operation {
    Document::parse(REQUEST_DOC, None, limits)
        .unwrap()
        .operation_id("post")
        .unwrap()
}
fn duplicate(builder: RequestBuilder, first: usize, repeated: usize) -> Diagnostic {
    let error = builder.prepare().unwrap_err();
    let d = &error;
    assert_eq!(d.code(), Code::InvalidSelection);
    assert_eq!(
        d.reason(),
        Some(&DiagnosticReason::DuplicateAssignment {
            first_assignment: first,
            repeated_assignment: repeated,
        })
    );
    assert!(d.location().is_none());
    assert!(d.source_context().is_none());
    d.clone()
}
fn credential() -> Credential {
    Credential::bearer("test-only-token", vec!["https://api.example".into()])
}
#[test]
fn builder_assignment_algebra_has_typed_context_and_stable_events() {
    let op = request_operation(Limits::default());
    let d = duplicate(
        op.request()
            .parameter(ParameterLocation::Header, "X-Key", &1)
            .parameter(ParameterLocation::Header, "x-key", &1),
        0,
        1,
    );
    assert!(
        matches!(d.context(), Some(DiagnosticContext::Parameter { location: ParameterLocation::Header, name, .. }) if name == "x-key")
    );
    for kind in [
        SelectionKind::Server,
        SelectionKind::Media,
        SelectionKind::Security,
    ] {
        let selection = match kind {
            SelectionKind::Server => CaseSelection {
                server: Some(0),
                ..Default::default()
            },
            SelectionKind::Media => CaseSelection {
                media: Some("application/json".into()),
                ..Default::default()
            },
            _ => CaseSelection {
                security: Some(0),
                ..Default::default()
            },
        };
        let d = duplicate(
            op.request()
                .case_selection(selection.clone())
                .case_selection(CaseSelection::default())
                .case_selection(selection),
            0,
            1,
        );
        assert!(
            matches!(d.context(), Some(DiagnosticContext::Selection { kind: actual, .. }) if *actual == kind)
        );
    }
    let mut selection = CaseSelection {
        server: Some(0),
        media: Some("application/json".into()),
        security: Some(0),
        ..Default::default()
    };
    selection
        .variables
        .insert("host".into(), "api.example".into());
    selection.variables.insert("base".into(), "v1".into());
    let d = duplicate(
        op.request()
            .case_selection(selection)
            .server_variable("host", "api.example"),
        4,
        5,
    );
    assert!(
        matches!(d.context(), Some(DiagnosticContext::Selection {kind: SelectionKind::ServerVariable, key: Some(key), ..}) if key == "host")
    );
    let d = duplicate(op.request().no_body().json(&1_i32), 0, 1);
    assert_eq!(d.context(), Some(&DiagnosticContext::Body));
    let d = duplicate(
        op.request()
            .credential("auth", credential())
            .credential("auth", credential()),
        0,
        1,
    );
    assert!(
        matches!(d.context(), Some(DiagnosticContext::Credential {scheme}) if scheme == "auth")
    );
    let long = "test-only-secret".repeat(400);
    let d = duplicate(
        op.request()
            .server_variable(&long, "x")
            .server_variable(&long, "x"),
        0,
        1,
    );
    assert!(d.context().is_none());
    assert!(d.context_omitted_for_limit());
    assert!(!format!("{d:?} {d}").contains("test-only-secret"));
}
#[test]
fn builder_matches_equivalent_input_and_distinct_assignments_commute() {
    let op = request_operation(Limits::default());
    let body = TestValue::Object(&[
        ("count", TestValue::U64(u64::MAX)),
        ("null", TestValue::Null),
    ]);
    let mut selection = CaseSelection {
        security: Some(0),
        ..Default::default()
    };
    selection.variables.insert("base".into(), "v2".into());
    let a = op
        .request()
        .parameter(ParameterLocation::Path, "id", &9_007_199_254_740_993_u64)
        .parameter(ParameterLocation::Query, "q", &"A/\"B\n雪")
        .json(&body)
        .case_selection(selection.clone())
        .header(Header::new("X-Extra", "a"))
        .header(Header::new("X-Extra", "b"))
        .prepare()
        .unwrap();
    let b = op
        .request()
        .security(0)
        .server_variable("base", "v2")
        .json(&body)
        .parameter(ParameterLocation::Query, "q", &"A/\"B\n雪")
        .parameter(ParameterLocation::Path, "id", &9_007_199_254_740_993_u64)
        .header(Header::new("X-Extra", "a"))
        .header(Header::new("X-Extra", "b"))
        .prepare()
        .unwrap();
    let owner = Value::from_serializable(
        &TestValue::Array(&[
            TestValue::U64(9_007_199_254_740_993),
            TestValue::String("A/\"B\n雪"),
            body,
        ]),
        Limits::default(),
    )
    .unwrap();
    let input = CaseInput {
        selection,
        parameters: vec![
            CaseParameter {
                location: ParameterLocation::Path,
                name: "id".into(),
                value: owner.clone().at(0).unwrap(),
            },
            CaseParameter {
                location: ParameterLocation::Query,
                name: "q".into(),
                value: owner.clone().at(1).unwrap(),
            },
        ],
        body: CaseBody::Json(owner.clone().at(2).unwrap()),
        headers: vec![Header::new("X-Extra", "a"), Header::new("X-Extra", "b")],
        ..Default::default()
    };
    let raw = op.prepare_case(&input).unwrap();
    for actual in [&a, &b] {
        assert_eq!(actual.method(), raw.method());
        assert_eq!(actual.target(), raw.target());
        assert_eq!(actual.headers(), raw.headers());
        assert_eq!(
            actual.body().map(AsRef::as_ref),
            raw.body().map(AsRef::as_ref)
        );
        assert_eq!(actual.body_kind(), raw.body_kind());
    }
    assert!(a.target().contains("9007199254740993"));
    assert_eq!(
        a.body().map(AsRef::as_ref),
        Some(br#"{"count":18446744073709551615,"null":null}"#.as_slice())
    );
}
#[test]
fn first_refusal_stops_later_conversions_and_precedes_cancellation() {
    struct Never;
    impl serde::Serialize for Never {
        fn serialize<S: serde::Serializer>(&self, _: S) -> Result<S::Ok, S::Error> {
            panic!("unadmitted serializer called")
        }
    }
    impl AsRef<str> for Never {
        fn as_ref(&self) -> &str {
            panic!("post-refusal name accessed")
        }
    }
    let op = request_operation(Limits {
        reference_steps: 0,
        ..Default::default()
    });
    let builder = op
        .request()
        .parameter(ParameterLocation::Path, "id", &Never)
        .parameter(ParameterLocation::Query, Never, &Never)
        .json(&Never)
        .server_variable(Never, Never);
    let cancel = Cancellation::default();
    cancel.cancel();
    let e = builder.cancellation(cancel.clone()).prepare().unwrap_err();
    assert!(matches!(
        e.reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::InputCount,
            maximum: 0,
            actual: 1
        })
    ));
    let op = request_operation(Limits::default());
    let invalid = TestValue::Object(&[
        ("duplicate", TestValue::Null),
        ("duplicate", TestValue::Null),
    ]);
    let e = op
        .request()
        .cancellation(cancel.clone())
        .json(&invalid)
        .prepare()
        .unwrap_err();
    assert_eq!(e.code(), Code::Cancelled);
    assert!(
        op.request()
            .parameter(ParameterLocation::Path, "id", &1_usize)
            .security(0)
            .prepare()
            .is_ok()
    );
}
#[test]
fn builder_preserves_source_domains_and_bounded_construction_failures() {
    let op = request_operation(Limits::default());
    let exact = Value::parse("null", Limits::default()).unwrap();
    let e = op
        .request()
        .parameter_exact(ParameterLocation::Path, "id", &exact.clone())
        .prepare()
        .unwrap_err();
    assert_eq!(e.code(), Code::UnsupportedValue);
    assert_eq!(e.source_context(), Some(exact.source_context()));
    assert_eq!(e.location(), Some(&exact.location()));
    let e = op
        .request()
        .parameter(ParameterLocation::Path, "id", &TestValue::Null)
        .prepare()
        .unwrap_err();
    assert_eq!(e.code(), Code::UnsupportedValue);
    assert_eq!(
        e.source_context().unwrap().origin,
        SourceOrigin::Constructed
    );
    let invalid = TestValue::Object(&[
        ("duplicate", TestValue::Null),
        ("duplicate", TestValue::Null),
    ]);
    let e = op.request().json(&invalid).prepare().unwrap_err();
    assert!(e.context().is_some());
    assert_eq!(e.code(), Code::DuplicateMember);
    assert!(!e.source_bytes().is_empty());
    assert_eq!(
        e.source_context().unwrap().origin,
        SourceOrigin::Constructed
    );
    let op = request_operation(Limits {
        body_bytes: 4,
        ..Default::default()
    });
    let e = op.request().json(&"\n\n").prepare().unwrap_err();
    assert!(e.context().is_some());
    assert!(e.source_bytes().is_empty());
    assert!(matches!(
        e.reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::BodyBytes,
            maximum: 4,
            ..
        })
    ));
    let e = op.request().json(&"long").prepare().unwrap_err();
    assert!(e.context().is_some());
    assert!(matches!(
        e.reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::BodyBytes,
            maximum: 4,
            ..
        })
    ));
    let huge = "x".repeat(REQUEST_DOC.len() + 1);
    let op = request_operation(Limits {
        document_bytes: REQUEST_DOC.len(),
        ..Default::default()
    });
    let e = op
        .request()
        .parameter(ParameterLocation::Path, &huge, &1)
        .prepare()
        .unwrap_err();
    assert!(matches!(
        e.reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::InputBytes,
            ..
        })
    ));
}
