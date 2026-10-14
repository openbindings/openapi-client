use dynamic_openapi_client::{
    Cancellation, Diagnostic, Document, Limits, Operation, ParameterLocation,
    diagnostics::{Code, DiagnosticReason, LimitKind},
    exact::Value,
    form::{FormField, Part},
};
use serde::{Serialize, Serializer, ser::SerializeMap};
use std::{cell::Cell, rc::Rc, sync::Arc};

fn operation(limits: Limits, media: &str) -> Operation {
    let source = serde_json::json!({"openapi":"3.1.2","servers":[{"url":"https://api.example"}],"paths":{"/send":{"post":{"operationId":"send","parameters":[{"name":"p","in":"query","content":{"application/json":{}}},{"name":"q","in":"query","content":{"application/json":{}}}],"requestBody":{"content":{media:{"schema":{"type":"object","properties":{"file":{"type":"string","format":"binary"}}}}}}}}}}).to_string();
    Document::parse(source, None, limits)
        .unwrap()
        .operation_id("send")
        .unwrap()
}
struct Counted<'a, T: ?Sized> {
    calls: &'a Cell<usize>,
    value: &'a T,
}
impl<T: Serialize + ?Sized> Serialize for Counted<'_, T> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        self.calls.set(self.calls.get() + 1);
        self.value.serialize(serializer)
    }
}
fn kind(error: &Diagnostic) -> LimitKind {
    match error.reason().unwrap() {
        DiagnosticReason::Limit { kind, .. } => *kind,
        reason => panic!("{reason:?}"),
    }
}
#[test]
fn setters_own_immediately_and_prepare_never_runs_user_serializers() {
    let calls = Rc::new(Cell::new(0));
    let op = operation(Limits::default(), "application/json");
    let builder = {
        let text = String::from("borrowed secret");
        op.request().json(&Counted {
            calls: &calls,
            value: &text,
        })
    };
    assert_eq!(calls.get(), 1);
    let request = builder.prepare().unwrap();
    assert_eq!(calls.get(), 1);
    assert_eq!(request.body().unwrap().as_ref(), br#""borrowed secret""#);
    assert!(!format!("{request:?}").contains("borrowed secret"));
}
#[test]
fn duplicate_first_failure_cancellation_and_remaining_budgets_suppress_callbacks() {
    let op = operation(Limits::default(), "application/json");
    let calls = Cell::new(0);
    let counted = Counted {
        calls: &calls,
        value: &1,
    };
    let cancel = Cancellation::default();
    cancel.cancel();
    let error = op
        .request()
        .json(&0)
        .cancellation(cancel.clone())
        .json(&counted)
        .prepare()
        .unwrap_err();
    assert_eq!(error.code(), Code::InvalidSelection);
    assert!(matches!(
        error.reason(),
        Some(DiagnosticReason::DuplicateAssignment {
            first_assignment: 0,
            repeated_assignment: 1
        })
    ));
    assert_eq!(calls.get(), 0);
    assert_eq!(
        op.request()
            .cancellation(cancel)
            .json(&counted)
            .prepare()
            .unwrap_err()
            .code(),
        Code::Cancelled
    );
    let error = op
        .request()
        .json(&f64::NAN)
        .parameter(ParameterLocation::Query, "p", &counted)
        .prepare()
        .unwrap_err();
    assert_eq!(error.code(), Code::Serialization);
    assert_eq!(calls.get(), 0);
    let op = operation(
        Limits {
            reference_steps: 0,
            ..Limits::default()
        },
        "application/json",
    );
    assert_eq!(
        kind(
            &op.request()
                .parameter(ParameterLocation::Query, "p", &counted)
                .prepare()
                .unwrap_err()
        ),
        LimitKind::InputCount
    );
    assert_eq!(calls.get(), 0);
    let no_body_room = operation(
        Limits {
            body_bytes: 0,
            ..Limits::default()
        },
        "application/json",
    );
    assert_eq!(
        kind(&no_body_room.request().json(&counted).prepare().unwrap_err()),
        LimitKind::BodyBytes
    );
    assert_eq!(calls.get(), 0);
    let op = operation(
        Limits {
            nodes: 128,
            ..Limits::default()
        },
        "application/json",
    );
    let first = vec![0; 127]; // Array node plus its 127 children exhausts aggregate nodes.
    let error = op
        .request()
        .parameter(ParameterLocation::Query, "p", &first)
        .json(&counted)
        .prepare()
        .unwrap_err();
    assert_eq!(kind(&error), LimitKind::Nodes);
    assert_eq!(calls.get(), 0);
}
#[test]
fn aggregate_bytes_include_json_forms_parameters_and_exact_backing_owners() {
    let limits = Limits {
        document_bytes: 1024,
        ..Limits::default()
    };
    let op = operation(limits.clone(), "application/json");
    let large = "x".repeat(600);
    let error = op
        .request()
        .parameter(ParameterLocation::Query, "p", &large)
        .json(&large)
        .prepare()
        .unwrap_err();
    assert_eq!(kind(&error), LimitKind::InputBytes);
    let op = operation(limits.clone(), "application/x-www-form-urlencoded");
    let error = op
        .request()
        .parameter(ParameterLocation::Query, "p", &large)
        .form(&std::collections::BTreeMap::from([("q", &large)]))
        .prepare()
        .unwrap_err();
    assert_eq!(kind(&error), LimitKind::InputBytes);
    let huge = Value::parse(
        format!(r#"{{"padding":"{}","p":1}}"#, "x".repeat(1024)),
        Limits::default(),
    )
    .unwrap();
    let error = op
        .request()
        .parameter_exact(ParameterLocation::Query, "p", &huge.get("p").unwrap())
        .prepare()
        .unwrap_err();
    assert_eq!(kind(&error), LimitKind::InputBytes);
    let op = operation(
        Limits {
            nodes: 128,
            ..Limits::default()
        },
        "application/json",
    );
    let exact = Value::from_serializable(&vec![0; 63], Limits::default()).unwrap();
    let error = op
        .request()
        .parameter_exact(ParameterLocation::Query, "p", &exact)
        .json(&vec![0; 64])
        .prepare()
        .unwrap_err();
    assert_eq!(kind(&error), LimitKind::Nodes);
}
#[test]
fn selected_exact_subtree_projects_and_retains_original_source_and_tokens() {
    let text = r#" {"selected":{"big":9007199254740993,"exponent":1.2300e+400,"decimal":0.12345678901234567890123456789},"name":"secret"} "#;
    let child = {
        let owner = Value::parse(text, Limits::default()).unwrap();
        owner.get("selected").unwrap()
    };
    assert_eq!(child.source(), text);
    #[derive(serde::Deserialize)]
    struct Selected {
        big: u64,
    }
    assert_eq!(
        child.deserialize::<Selected>().unwrap().big,
        9_007_199_254_740_993
    );
    let op = operation(Limits::default(), "application/json");
    let request = op.request().json_exact(&child).prepare().unwrap();
    assert_eq!(request.body().unwrap().as_ref(), child.raw().as_bytes());
    let error = child
        .get("exponent")
        .unwrap()
        .deserialize::<u64>()
        .unwrap_err();
    assert_eq!(error.code(), Code::Deserialization);
    assert!(error.detail().is_some());
    assert!(error.line() > 0);
    assert_eq!(error.source_position().unwrap().raw(), "1.2300e+400");
    assert!(!format!("{error:?} {error}").contains("secret"));
}
#[test]
fn direct_form_admission_counts_fields_before_next_value_callback() {
    struct Fields<'a> {
        calls: &'a Cell<usize>,
    }
    impl Serialize for Fields<'_> {
        fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
            let mut map = s.serialize_map(Some(2))?;
            map.serialize_entry("first", &"x")?;
            map.serialize_entry(
                "second",
                &Counted {
                    calls: self.calls,
                    value: &"y",
                },
            )?;
            map.end()
        }
    }
    let calls = Cell::new(0);
    for (limits, expected) in [
        (
            Limits {
                form_fields: 1,
                ..Limits::default()
            },
            LimitKind::FormFields,
        ),
        (
            Limits {
                form_metadata_bytes: 5,
                ..Limits::default()
            },
            LimitKind::FormMetadataBytes,
        ),
        (
            Limits {
                form_work: 1,
                ..Limits::default()
            },
            LimitKind::FormWork,
        ),
    ] {
        let error = operation(limits, "application/x-www-form-urlencoded")
            .request()
            .form(&Fields { calls: &calls })
            .prepare()
            .unwrap_err();
        assert_eq!(kind(&error), expected);
        assert_eq!(calls.get(), 0);
    }
    let op = operation(Limits::default(), "application/x-www-form-urlencoded");
    assert_eq!(
        op.request().form(&[1, 2]).prepare().unwrap_err().code(),
        Code::UnsupportedValue
    );
    let cancel = Cancellation::default();
    cancel.cancel();
    assert_eq!(
        op.request()
            .cancellation(cancel)
            .form(&Counted {
                calls: &calls,
                value: &Fields { calls: &calls }
            })
            .prepare()
            .unwrap_err()
            .code(),
        Code::Cancelled
    );
    assert_eq!(calls.get(), 0);
}
#[test]
fn form_fields_copy_borrows_share_binary_and_stop_after_duplicate() {
    let op = operation(Limits::default(), "multipart/form-data");
    let shared: Arc<[u8]> = Arc::from(b"shared".as_slice());
    let builder = {
        let bytes = vec![0, 255, 42];
        let filename = String::from("file.bin");
        op.request().form_fields([
            FormField::part(
                "file",
                Part::bytes(bytes.as_slice()).filename(filename.as_str()),
            ),
            FormField::part("other", Part::bytes(shared.clone())),
        ])
    };
    assert_eq!(Arc::strong_count(&shared), 2);
    let request = builder.prepare().unwrap();
    assert!(
        request
            .body()
            .unwrap()
            .windows(3)
            .any(|v| v == [0, 255, 42])
    );
    assert_eq!(Arc::strong_count(&shared), 1);
    let calls = Cell::new(0);
    let fields = std::iter::from_fn(|| {
        calls.set(calls.get() + 1);
        Some(FormField::value("same", &1, Limits::default()).unwrap())
    });
    assert_eq!(
        op.request()
            .form_fields(fields)
            .prepare()
            .unwrap_err()
            .code(),
        Code::InvalidSelection
    );
    assert_eq!(calls.get(), 2);
}
