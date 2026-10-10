use dynamic_openapi_client::*;
use serde_json::json;
use std::{cell::Cell, sync::Arc};
const URL: &str = "application/x-www-form-urlencoded";
fn fixture(name: &str, limits: Limits) -> Document {
    let file = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/g2b")
        .join(format!("{name}.json"));
    Document::parse(
        std::fs::read(file).unwrap(),
        Some("https://docs.example/api"),
        limits,
    )
    .unwrap()
}
fn completed_fixture(root: &str, resource: &str) -> Document {
    let doc = fixture(root, Limits::default());
    let error = doc
        .operation_id("send")
        .unwrap()
        .describe_form_field(URL, "p")
        .unwrap_err();
    let need = error.resource_need().unwrap();
    let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("tests/fixtures/g2b")
        .join(format!("{resource}.json"));
    let mut builder = DocumentBuilder::from_document(&doc);
    builder
        .supply(
            need,
            ResourceInput::at("https://docs.example/field", std::fs::read(path).unwrap()).unwrap(),
        )
        .unwrap();
    builder.build(doc.entry_resource().id()).unwrap()
}
fn doc(
    schema: serde_json::Value,
    encoding: serde_json::Value,
    version: &str,
    limits: Limits,
) -> Document {
    Document::parse(json!({"openapi":version,"servers":[{"url":"https://api.example"}],"paths":{"/send":{"post":{"operationId":"send","requestBody":{"content":{URL:{"schema":schema,"encoding":encoding}}}}}}}).to_string(),Some("https://docs.example/api"),limits).unwrap()
}
fn three<'a>(
    op: &Operation,
    fields: Vec<FormField<'a>>,
) -> Vec<Result<PreparedRequest, RequestError>> {
    let form = Form::new(fields.clone(), Limits::default()).unwrap();
    let input = Input {
        body: Body::Form(form.clone()),
        ..Input::default()
    };
    vec![
        op.prepare(&input).map_err(RequestError::Preparation),
        op.request().form(fields).prepare(),
        op.request().body(Body::Form(form)).prepare(),
    ]
}
#[test]
fn original_literal_routes_and_unrelated_fact_locality() {
    for (name, field, value, expected) in [
        ("simple", "p", "x", "p=x"),
        ("unrelated", "p", "x", "p=x"),
        ("crossing", "p", "x", "p=x"),
        ("canonical", "p", "x", "p=x"),
        ("additional", "dynamic", "x", "dynamic=x"),
    ] {
        let d = fixture(name, Limits::default());
        let op = d.operation_id("send").unwrap();
        for result in three(&op, vec![FormField::new(field, value)]) {
            assert_eq!(result.unwrap().body(), Some(expected.as_bytes()), "{name}");
        }
    }
    let d = fixture("json", Limits::default());
    let op = d.operation_id("send").unwrap();
    let value = ExactJson::parse(br#"{"file":"AP8=","n":1e+00}"#, Limits::default())
        .unwrap()
        .root();
    for result in three(&op, vec![FormField::new("p", value)]) {
        assert_eq!(
            result.unwrap().body(),
            Some(b"p=%7B%22file%22%3A%22AP8%3D%22%2C%22n%22%3A1e%2B00%7D".as_slice())
        );
    }
    let limits = Limits {
        schema_fact_work: 0,
        ..Limits::default()
    };
    let d = fixture("whole-json", limits.clone());
    let value = ExactJson::parse(br#"{"file":"AP8=","n":1e+00}"#, Limits::default())
        .unwrap()
        .root();
    assert_eq!(
        d.operation_id("send")
            .unwrap()
            .request()
            .json(value)
            .prepare()
            .unwrap()
            .body(),
        Some(br#"{"file":"AP8=","n":1e+00}"#.as_slice())
    );
    let d = fixture("raw", limits);
    assert_eq!(
        d.operation_id("send")
            .unwrap()
            .request()
            .body(Body::Raw(Arc::from([0, 255])))
            .prepare()
            .unwrap()
            .body(),
        Some([0, 255].as_slice())
    );
}
#[test]
fn uniform_need_recovery_preserves_the_old_snapshot() {
    let d = fixture("missing", Limits::default());
    let op = d.operation_id("send").unwrap();
    let mut errors = three(&op, vec![FormField::new("p", "value")])
        .into_iter()
        .map(|r| r.unwrap_err().diagnostic().clone())
        .collect::<Vec<_>>();
    errors.push(op.describe_form_field(URL, "p").unwrap_err());
    for error in &errors {
        assert_eq!(error.code(), Code::ResourceUnavailable);
        assert_eq!(error.resource_need(), errors[0].resource_need());
        assert_eq!(
            error.resource_need().unwrap().requested_uri(),
            Some("https://docs.example/field")
        );
        assert!(
            error
                .location()
                .unwrap()
                .pointer
                .ends_with("/properties/p/$ref")
        );
    }
    let need = errors[0].resource_need().unwrap();
    let mut b = DocumentBuilder::from_document(&d);
    b.supply(
        need,
        ResourceInput::at(
            "https://docs.example/field",
            include_bytes!("fixtures/g2b/field.json").as_slice(),
        )
        .unwrap(),
    )
    .unwrap();
    let next = b.build(d.entry_resource().id()).unwrap();
    for result in three(
        &next.operation_id("send").unwrap(),
        vec![FormField::new("p", "value")],
    ) {
        assert_eq!(result.unwrap().body(), Some(b"p=value".as_slice()));
    }
    assert!(
        op.describe_form_field(URL, "p")
            .unwrap_err()
            .resource_need()
            .is_some()
    );
    let stale = DocumentBuilder::from_document(&next)
        .supply(
            need,
            ResourceInput::at("https://docs.example/field", b"{}".as_slice()).unwrap(),
        )
        .unwrap_err();
    assert_eq!(stale.diagnostic().code(), Code::SnapshotMismatch);
}
#[test]
fn literal_selected_fact_refusals_are_precise() {
    for (name, code, reason) in [
        (
            "opaque",
            Code::UnsupportedSchema,
            SchemaReason::OpaqueDialect,
        ),
        (
            "composition",
            Code::UnsupportedSchema,
            SchemaReason::Composition,
        ),
        (
            "dynamic",
            Code::UnsupportedSchema,
            SchemaReason::DynamicPropertyFacts,
        ),
    ] {
        let d = if name == "dynamic" {
            fixture(name, Limits::default())
        } else {
            completed_fixture("default-field", name)
        };
        let op = d.operation_id("send").unwrap();
        for result in three(
            &op,
            vec![FormField::new(
                if name == "dynamic" { "dynamic" } else { "p" },
                "x",
            )],
        ) {
            let error = result.unwrap_err();
            assert_eq!(error.diagnostic().code(), code, "{name}");
            assert_eq!(
                error.diagnostic().reason(),
                Some(&DiagnosticReason::Schema(reason.clone())),
                "{name}"
            );
        }
    }
    let d = completed_fixture("default-field", "unsupported-coding");
    let op = d.operation_id("send").unwrap();
    let description = op.describe_form_field(URL, "p").unwrap();
    assert!(
        description
            .representation
            .unwrap()
            .content_encoding
            .is_some()
    );
    assert!(matches!(
        op.request()
            .form([FormField::new("p", "x")])
            .prepare()
            .unwrap_err()
            .diagnostic()
            .reason(),
        Some(DiagnosticReason::Form(FormReason::UnsupportedCoding))
    ));
}
#[test]
fn required_hints_and_partial_inventory_are_not_validation() {
    let d = doc(
        json!({"type":"object","required":["p"],"properties":{"p":{"type":"string"},"q":{"type":"object","required":["inner"]}}}),
        json!({}),
        "3.1.2",
        Limits::default(),
    );
    let op = d.operation_id("send").unwrap();
    let BodyFields::Declared { fields, .. } =
        &op.describe().unwrap().request_body.unwrap().media[0].fields
    else {
        panic!()
    };
    assert_eq!(fields[0].requirement, FieldRequirement::SchemaRequired);
    assert_eq!(fields[1].requirement, FieldRequirement::Optional);
    assert!(op.request().form([]).prepare().is_ok());
    let d = doc(
        json!({"type":"object","properties":{"p":{"type":"string"}},"allOf":[{"required":["q"],"properties":{"q":{"type":"string"}}}]}),
        json!({}),
        "3.1.2",
        Limits::default(),
    );
    let op = d.operation_id("send").unwrap();
    let BodyFields::Partial {
        fields, openness, ..
    } = &op.describe().unwrap().request_body.unwrap().media[0].fields
    else {
        panic!()
    };
    assert_eq!(*openness, FieldOpenness::Unknown);
    assert_eq!(fields.len(), 1);
    assert_eq!(fields[0].name, "p");
    assert_eq!(fields[0].requirement, FieldRequirement::UnknownHint);
    assert_eq!(
        op.describe_form_field(URL, "p").unwrap_err().reason(),
        Some(&DiagnosticReason::Schema(SchemaReason::Composition))
    );
}
#[test]
fn explicit_style_makes_child_coding_inapplicable_and_hints_stay_inspection_only() {
    for required in [json!("p"), json!([1]), json!(["p", "p"])] {
        let d = doc(
            json!({"type":"object","required":required,"properties":{"p":{"$ref":"https://docs.example/missing-coding"}}}),
            json!({"p":{"style":"form","explode":true}}),
            "3.1.2",
            Limits::default(),
        );
        let op = d.operation_id("send").unwrap();
        for result in three(&op, vec![FormField::new("p", "x")]) {
            assert_eq!(result.unwrap().body(), Some(b"p=x".as_slice()));
        }
        assert_eq!(
            op.describe_form_field(URL, "p").unwrap_err().reason(),
            Some(&DiagnosticReason::Schema(SchemaReason::InvalidKeyword))
        );
    }
    let d = doc(
        json!({"type":"object","properties":{"p":{"$ref":"https://docs.example/missing-coding"}}}),
        json!({"p":{"style":"form"}}),
        "3.1.2",
        Limits::default(),
    );
    let field = d
        .operation_id("send")
        .unwrap()
        .describe_form_field(URL, "p")
        .unwrap();
    assert_eq!(
        field.schemas[0].value.get("$ref").unwrap().as_str(),
        Some("https://docs.example/missing-coding")
    );
    let repr = field.representation.unwrap();
    assert_eq!(repr.media_types, ["text/plain"]);
    assert_eq!(repr.content_encoding, None);
}
#[test]
fn constructors_stop_before_later_iterator_work_and_recheck_use_limits() {
    let calls = Cell::new(0);
    let fields = std::iter::from_fn(|| {
        calls.set(calls.get() + 1);
        Some(FormField::new("same", "x"))
    });
    let error = Form::new(fields, Limits::default()).unwrap_err();
    assert_eq!(calls.get(), 2);
    assert_eq!(
        error.diagnostic().reason(),
        Some(&DiagnosticReason::Form(FormReason::DuplicateField))
    );
    let cancel = Cancellation::default();
    cancel.cancel();
    let fields = std::iter::from_fn(|| {
        calls.set(calls.get() + 1);
        Some(FormField::new("never", "x"))
    });
    assert_eq!(
        Form::new_with_cancellation(fields, Limits::default(), &cancel)
            .unwrap_err()
            .diagnostic()
            .code(),
        Code::Cancelled
    );
    assert_eq!(calls.get(), 2);
    let form = Form::new(
        [FormField::new("p", "one"), FormField::new("q", "two")],
        Limits::default(),
    )
    .unwrap();
    for (limits, kind, actual) in [
        (
            Limits {
                form_fields: 1,
                ..Limits::default()
            },
            LimitKind::FormFields,
            2,
        ),
        (
            Limits {
                body_bytes: 2,
                ..Limits::default()
            },
            LimitKind::BodyBytes,
            3,
        ),
        (
            Limits {
                form_metadata_bytes: 0,
                ..Limits::default()
            },
            LimitKind::FormMetadataBytes,
            1,
        ),
        (
            Limits {
                form_work: 0,
                ..Limits::default()
            },
            LimitKind::FormWork,
            1,
        ),
    ] {
        let d = fixture("simple", limits);
        let error = d
            .operation_id("send")
            .unwrap()
            .prepare(&Input {
                body: Body::Form(form.clone()),
                ..Input::default()
            })
            .unwrap_err();
        let Some(DiagnosticReason::Limit {
            kind: got,
            actual: got_actual,
            ..
        }) = error.reason()
        else {
            panic!("{error:?}")
        };
        assert_eq!(*got, kind);
        assert_eq!(*got_actual, actual);
    }
}
#[test]
fn shared_binary_owner_is_retained_shallowly_and_released() {
    let bytes: Arc<[u8]> = Arc::from([0, 255, 7]);
    let weak = Arc::downgrade(&bytes);
    let form = Form::new(
        [FormField::new("file", Part::bytes(bytes.clone()))],
        Limits::default(),
    )
    .unwrap();
    assert_eq!(Arc::strong_count(&bytes), 2);
    let other = form.clone();
    assert_eq!(Arc::strong_count(&bytes), 2);
    drop(bytes);
    drop(form);
    assert!(weak.upgrade().is_some());
    drop(other);
    assert!(weak.upgrade().is_none());
    let borrowed = vec![1_u8; 131072];
    let form = Form::new(
        [FormField::new("file", Part::bytes(borrowed.as_slice()))],
        Limits::default(),
    )
    .unwrap();
    drop(borrowed);
    assert_eq!(form.len(), 1);
}
#[test]
fn finite_f64_uses_existing_explicit_serializable_boundary() {
    let value = ExactJson::from_serializable(&12.5_f64, Limits::default())
        .unwrap()
        .root();
    assert_eq!(value.raw(), "12.5");
    let d = doc(
        json!({"properties":{"p":{"type":["number","string"]}}}),
        json!({}),
        "3.1.2",
        Limits::default(),
    );
    for result in three(
        &d.operation_id("send").unwrap(),
        vec![FormField::new("p", value.clone())],
    ) {
        assert_eq!(result.unwrap().body(), Some(b"p=12.5".as_slice()));
    }
    assert_eq!(
        ExactJson::from_serializable(&f64::NAN, Limits::default())
            .unwrap_err()
            .diagnostic()
            .code(),
        Code::Serialization
    );
}
#[test]
fn precancel_suppresses_into_iterator_callback() {
    struct Caller<'a>(&'a Cell<usize>);
    impl IntoIterator for Caller<'_> {
        type Item = FormField<'static>;
        type IntoIter = std::iter::Empty<Self::Item>;
        fn into_iter(self) -> Self::IntoIter {
            self.0.set(self.0.get() + 1);
            std::iter::empty()
        }
    }
    let calls = Cell::new(0);
    let cancel = Cancellation::default();
    cancel.cancel();
    assert_eq!(
        Form::new_with_cancellation(Caller(&calls), Limits::default(), &cancel)
            .unwrap_err()
            .diagnostic()
            .code(),
        Code::Cancelled
    );
    assert_eq!(calls.get(), 0);
}

#[test]
#[ignore = "uses separately pinned original provider sources; run explicitly with G2B_PROVIDER_ROOT"]
fn pinned_original_provider_jobs_are_preparation_only() {
    let root = std::path::PathBuf::from(
        std::env::var("G2B_PROVIDER_ROOT").expect("explicit pinned provider directory"),
    );
    let load = |name: &str, url: &str| {
        Document::parse(
            std::fs::read(root.join(format!("{name}.document"))).unwrap(),
            Some(url),
            Limits::default(),
        )
        .unwrap()
    };
    let twilio = load(
        "twilio",
        "https://raw.githubusercontent.com/twilio/twilio-oai/main/spec/yaml/twilio_api_v2010.yaml",
    );
    let op = twilio.operation_id("CreateMessage").unwrap();
    let account = "AC00000000000000000000000000000000";
    let account_value = ExactJson::from_serializable(&account, Limits::default())
        .unwrap()
        .root();
    let credential = Credential {
        value: CredentialValue::Basic {
            username: account.into(),
            password: "wave01-FAKE-DO-NOT-SEND".into(),
        },
        origins: vec!["https://api.twilio.com".into()],
    };
    let original = [
        ("To", "+15005550006"),
        ("From", "+15005550006"),
        ("Body", "Preparation only"),
    ];
    let additives = [
        ("ContentRetention", "retain"),
        ("AddressRetention", "retain"),
        ("TrafficType", "free"),
        ("ScheduleType", "fixed"),
        ("RiskCheck", "enable"),
    ];
    for extra in [
        vec![],
        additives.to_vec(),
        vec![("ContentRetention", "not-an-enum-member")],
    ] {
        let fields = original
            .iter()
            .chain(extra.iter())
            .map(|(n, v)| FormField::new(*n, *v))
            .collect::<Vec<_>>();
        let owned = Form::new(fields.clone(), Limits::default()).unwrap();
        let mut input = Input {
            body: Body::Form(owned.clone()),
            ..Input::default()
        };
        input.parameters.push(ParameterInput {
            location: ParameterLocation::Path,
            name: "AccountSid".into(),
            value: account_value.clone(),
        });
        input
            .credentials
            .insert("accountSid_authToken".into(), credential.clone());
        let results = [
            op.prepare(&input).unwrap(),
            op.request()
                .parameter(ParameterLocation::Path, "AccountSid", account_value.clone())
                .credential("accountSid_authToken", credential.clone())
                .form(fields)
                .prepare()
                .unwrap(),
            op.request()
                .parameter(ParameterLocation::Path, "AccountSid", account_value.clone())
                .credential("accountSid_authToken", credential.clone())
                .body(Body::Form(owned))
                .prepare()
                .unwrap(),
        ];
        let suffix = if extra.len() == 5 {
            "&ContentRetention=retain&AddressRetention=retain&TrafficType=free&ScheduleType=fixed&RiskCheck=enable"
        } else if extra.len() == 1 {
            "&ContentRetention=not-an-enum-member"
        } else {
            ""
        };
        let expected =
            format!("To=%2B15005550006&From=%2B15005550006&Body=Preparation+only{suffix}");
        for request in results {
            assert_eq!(request.body(), Some(expected.as_bytes()));
            assert_eq!(
                request.target(),
                format!("https://api.twilio.com/2010-04-01/Accounts/{account}/Messages.json")
            );
        }
    }
    for (name, _) in additives {
        let description = op.describe_form_field(URL, name).unwrap();
        let representation = description.representation.unwrap();
        assert_eq!(representation.media_types, ["text/plain"]);
        assert_eq!(representation.content_encoding, None);
        assert_eq!(description.schemas[0].profile, SchemaProfile::OpenApi30);
    }
    let flyn = load("flyn", "https://www.flyn.to/api/openapi.json");
    let op = flyn.operation_id("recordConversion").unwrap();
    for (value, expected) in [
        (
            ExactJson::parse(b"12.5", Limits::default()).unwrap().root(),
            "click_id=V1StGXR8_Z5jdHi6&value=12.5",
        ),
        (
            ExactJson::parse(br#""12,5""#, Limits::default())
                .unwrap()
                .root(),
            "click_id=V1StGXR8_Z5jdHi6&value=12%2C5",
        ),
    ] {
        let fields = vec![
            FormField::new("click_id", "V1StGXR8_Z5jdHi6"),
            FormField::new("value", value),
        ];
        let owned = Form::new(fields.clone(), Limits::default()).unwrap();
        let selection = Selection {
            media: Some(URL.into()),
            ..Selection::default()
        };
        let input = Input {
            body: Body::Form(owned.clone()),
            selection: selection.clone(),
            ..Input::default()
        };
        for request in [
            op.prepare(&input).unwrap(),
            op.request()
                .selection(selection.clone())
                .form(fields)
                .prepare()
                .unwrap(),
            op.request()
                .selection(selection)
                .body(Body::Form(owned))
                .prepare()
                .unwrap(),
        ] {
            assert_eq!(request.body(), Some(expected.as_bytes()));
            assert_eq!(request.target(), "https://www.flyn.to/api/conversion");
            assert!(
                !request
                    .headers()
                    .iter()
                    .any(|h| h.name().eq_ignore_ascii_case("authorization"))
            );
        }
    }
    let transmute = load(
        "transmute",
        "https://raw.githubusercontent.com/transmute-app/openapi-specifications/main/openapi.json",
    );
    let op = transmute
        .operation_id("upload_file_api_files_post")
        .unwrap();
    let description = op
        .describe_form_field("multipart/form-data", "file")
        .unwrap();
    let representation = description.representation.unwrap();
    assert_eq!(representation.media_types, ["text/plain"]);
    assert_eq!(representation.content_encoding, None);
    let credential = Credential {
        value: CredentialValue::Bearer("wave01-FAKE-DO-NOT-SEND".into()),
        origins: vec!["http://0.0.0.0:3313".into()],
    };
    let fields = vec![FormField::new(
        "file",
        Part::bytes(b"hello").filename("fixture.txt"),
    )];
    let owned = Form::new(fields.clone(), Limits::default()).unwrap();
    let selection = Selection {
        media: Some("multipart/form-data".into()),
        ..Selection::default()
    };
    let mut input = Input {
        body: Body::Form(owned.clone()),
        selection: selection.clone(),
        ..Input::default()
    };
    input
        .credentials
        .insert("BearerAuth".into(), credential.clone());
    let errors = [
        op.prepare(&input).unwrap_err(),
        op.request()
            .selection(selection.clone())
            .credential("BearerAuth", credential.clone())
            .form(fields)
            .prepare()
            .unwrap_err()
            .diagnostic()
            .clone(),
        op.request()
            .selection(selection)
            .credential("BearerAuth", credential)
            .body(Body::Form(owned))
            .prepare()
            .unwrap_err()
            .diagnostic()
            .clone(),
    ];
    for error in errors {
        assert_eq!(error.code(), Code::InvalidDeclaration);
        assert!(matches!(
            error.reason(),
            Some(DiagnosticReason::UndefinedSecurityScheme)
        ));
        assert_eq!(
            error.location().unwrap().pointer,
            "/paths/~1api~1files/post/security/0/OAuth2PasswordBearer"
        );
    }
}
#[test]
fn selected_json_media_does_not_erase_an_unresolved_field_reference() {
    let d = fixture("selected-json-need", Limits::default());
    let op = d.operation_id("send").unwrap();
    let value = ExactJson::parse(br#"{"n":1e+00}"#, Limits::default())
        .unwrap()
        .root();
    let errors = three(&op, vec![FormField::new("p", value)])
        .into_iter()
        .map(|r| r.unwrap_err().diagnostic().clone())
        .collect::<Vec<_>>();
    let selected = op.describe_form_field(URL, "p").unwrap_err();
    for error in errors.iter().chain([&selected]) {
        assert_eq!(error.code(), Code::ResourceUnavailable);
        assert_eq!(error.resource_need(), selected.resource_need());
        assert_eq!(
            error.location().unwrap().pointer,
            "/paths/~1send/post/requestBody/content/application~1x-www-form-urlencoded/schema/properties/p/$ref"
        );
    }
}
#[test]
fn work_depth_layout_and_lower_descriptor_limits_remain_corrective() {
    for (name, limits, kind, actual, pointer) in [
        (
            "simple",
            Limits {
                schema_fact_work: 0,
                ..Limits::default()
            },
            LimitKind::SchemaFactWork,
            1,
            "/schema",
        ),
        (
            "chain",
            Limits {
                reference_steps: 1,
                ..Limits::default()
            },
            LimitKind::ReferenceDepth,
            2,
            "/components/schemas/T0/$ref",
        ),
    ] {
        let d = fixture(name, limits);
        let op = d.operation_id("send").unwrap();
        for result in three(&op, vec![FormField::new("p", "x")]) {
            let error = result.unwrap_err();
            let e = error.diagnostic();
            assert!(
                matches!(e.reason(),Some(DiagnosticReason::Limit{kind:k,actual:a,..}) if *k==kind && *a==actual)
            );
            assert!(e.location().unwrap().pointer.ends_with(pointer));
        }
    }
    let d = fixture(
        "parameter-only",
        Limits {
            schema_fact_work: 0,
            ..Limits::default()
        },
    );
    assert_eq!(
        d.operation_id("send")
            .unwrap()
            .request()
            .parameter(ParameterLocation::Query, "p", "x")
            .prepare()
            .unwrap()
            .target(),
        "https://api.example/send?p=x"
    );
    let d = fixture("layout", Limits::default());
    let mut b = DocumentBuilder::from_document(&d);
    b.add(
        ResourceInput::at(
            "https://docs.example/bundle",
            include_bytes!("fixtures/g2b/bundle.json").as_slice(),
        )
        .unwrap(),
    )
    .unwrap();
    let d = b.build(d.entry_resource().id()).unwrap();
    let op = d.operation_id("send").unwrap();
    let errors = three(&op, vec![FormField::new("p", "x")])
        .into_iter()
        .map(|r| r.unwrap_err().diagnostic().clone())
        .collect::<Vec<_>>();
    let selected = op.describe_form_field(URL, "p").unwrap_err();
    for error in &errors {
        assert_eq!(error.code(), Code::ResourceLayoutRequired);
        assert_eq!(error.resource_need(), selected.resource_need());
    }
    let mut b = DocumentBuilder::from_document(&d);
    b.apply_layout(
        selected.resource_need().unwrap(),
        ResourceLayout::new().schema(
            "/x",
            SchemaProfile::JsonSchema {
                dialect_uri: "https://spec.openapis.org/oas/3.1/dialect/base".into(),
            },
        ),
    )
    .unwrap();
    let next = b.build(d.entry_resource().id()).unwrap();
    for result in three(
        &next.operation_id("send").unwrap(),
        vec![FormField::new("p", "x")],
    ) {
        assert_eq!(result.unwrap().body(), Some(b"p=x".as_slice()));
    }
    assert_eq!(
        op.describe_form_field(URL, "p").unwrap_err().code(),
        Code::ResourceLayoutRequired
    );
    let d = fixture(
        "simple",
        Limits {
            descriptor_work: 0,
            ..Limits::default()
        },
    );
    let error = d
        .operation_id("send")
        .unwrap()
        .describe_form_field(URL, "p")
        .unwrap_err();
    assert!(matches!(
        error.reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::DescriptorWork,
            actual: 1,
            ..
        })
    ));
}
#[test]
fn absent_and_closed_object_inventory_remain_distinct_from_missing_schema() {
    for (schema, expected) in [
        (json!({"type":"object"}), FieldOpenness::Open),
        (
            json!({"type":"object","additionalProperties":false}),
            FieldOpenness::ClosedHint,
        ),
    ] {
        let d = doc(schema, json!({}), "3.1.2", Limits::default());
        let desc = d.operation_id("send").unwrap().describe().unwrap();
        let BodyFields::Declared { fields, openness } = &desc.request_body.unwrap().media[0].fields
        else {
            panic!()
        };
        assert!(fields.is_empty());
        assert_eq!(*openness, expected);
    }
    let d=Document::parse(json!({"openapi":"3.1.2","paths":{"/":{"post":{"operationId":"send","requestBody":{"content":{URL:{}}}}}}}).to_string(),None,Limits::default()).unwrap();
    assert!(matches!(
        d.operation_id("send")
            .unwrap()
            .describe()
            .unwrap()
            .request_body
            .unwrap()
            .media[0]
            .fields,
        BodyFields::Opaque(FieldShapeIssue {
            reason: FieldShapeReason::MissingSchema,
            ..
        })
    ));
}
#[test]
fn referenced_hints_conjoin_without_legacy_sibling_leak_or_early_success() {
    for version in ["3.0.4", "3.1.2"] {
        let spec = json!({"openapi":version,"servers":[{"url":"https://api.example"}],"components":{"schemas":{"Base":{"type":"object","required":["p"],"properties":{"p":{"type":"string"}}}}},"paths":{"/send":{"post":{"operationId":"send","requestBody":{"content":{URL:{"schema":{"$ref":"#/components/schemas/Base","required":["q"],"properties":{"q":{"type":"string"}}}}}}}}}});
        let d = Document::parse(spec.to_string(), None, Limits::default()).unwrap();
        let description = d.operation_id("send").unwrap().describe().unwrap();
        let BodyFields::Declared { fields, .. } =
            &description.request_body.unwrap().media[0].fields
        else {
            panic!()
        };
        assert_eq!(fields.len(), if version == "3.0.4" { 1 } else { 2 });
        assert!(
            fields
                .iter()
                .all(|f| f.requirement == FieldRequirement::SchemaRequired)
        );
        if version == "3.0.4" {
            assert_eq!(fields[0].name, "p");
        }
    }
    let d = doc(
        json!({"$ref":"https://docs.example/missing-root","type":"object","required":["p"],"properties":{"p":{"type":"string"}}}),
        json!({}),
        "3.1.2",
        Limits::default(),
    );
    assert_eq!(
        d.operation_id("send")
            .unwrap()
            .describe()
            .unwrap_err()
            .resource_need()
            .unwrap()
            .requested_uri(),
        Some("https://docs.example/missing-root")
    );
}
#[test]
fn one_request_fact_budget_is_shared_across_fields() {
    let d = doc(
        json!({"type":"object","additionalProperties":{"type":"string"}}),
        json!({}),
        "3.1.2",
        Limits {
            schema_fact_work: 200,
            ..Limits::default()
        },
    );
    let op = d.operation_id("send").unwrap();
    assert!(
        op.request()
            .form([FormField::new("one", "x")])
            .prepare()
            .is_ok()
    );
    let names = (0..32).map(|i| format!("p{i}")).collect::<Vec<_>>();
    let fields = names
        .iter()
        .map(|s| FormField::new(s.as_str(), "x"))
        .collect::<Vec<_>>();
    for result in three(&op, fields) {
        let error = result.unwrap_err();
        assert!(
            matches!(error.diagnostic().reason(),Some(DiagnosticReason::Limit{kind:LimitKind::SchemaFactWork,maximum:200,actual,..}) if *actual>200)
        );
    }
}
