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

#[test]
fn yaml_exact_children_charge_both_sources_once_per_allocation() {
    let limits = Limits {
        document_bytes: 1024,
        ..Limits::default()
    };
    let op = operation(limits, "application/json");
    let owner = Document::parse(
        format!(
            "# {}\nopenapi: 3.1.2\nx-value: 1\nx-other: 2\n",
            "x".repeat(20_000)
        ),
        None,
        Limits::default(),
    )
    .unwrap()
    .root();
    let value = owner.get("x-value").unwrap();
    let total = value.source().len() + value.source_trace().authored.unwrap().source().len();
    let error = op.request().json_exact(&value).prepare().unwrap_err();
    assert_eq!(
        error.reason(),
        Some(&DiagnosticReason::Limit {
            kind: LimitKind::InputBytes,
            maximum: 1024,
            actual: total,
        })
    );

    // Both sources fit once, but their sum exceeds the allowance if charged twice.
    let owner = Document::parse(
        format!(
            "# {}\nopenapi: 3.1.2\nx-value: 1\nx-other: 2\n",
            "x".repeat(600)
        ),
        None,
        Limits::default(),
    )
    .unwrap()
    .root();
    let value = owner.get("x-value").unwrap();
    let other = owner.get("x-other").unwrap();
    drop(owner);
    assert!(
        op.request()
            .parameter_exact(ParameterLocation::Query, "p", &value)
            .parameter_exact(ParameterLocation::Query, "q", &other)
            .json_exact(&value)
            .prepare()
            .is_ok()
    );

    // Distinct allocations with equal text must each be charged.
    let first = Value::parse(
        format!("{{\"p\":1,\"pad\":\"{}\"}}", "x".repeat(600)),
        Limits::default(),
    )
    .unwrap();
    let second = Value::parse(first.source(), Limits::default()).unwrap();
    let error = op
        .request()
        .parameter_exact(ParameterLocation::Query, "p", &first.get("p").unwrap())
        .json_exact(&second.get("p").unwrap())
        .prepare()
        .unwrap_err();
    assert_eq!(kind(&error), LimitKind::InputBytes);
}

#[test]
fn exact_and_generated_sources_share_one_byte_allowance_in_both_orders() {
    let op = operation(
        Limits {
            document_bytes: 1024,
            ..Limits::default()
        },
        "application/json",
    );
    let owner = Value::parse(
        format!("{{\"pad\":\"{}\",\"p\":1}}", "x".repeat(900)),
        Limits::default(),
    )
    .unwrap();
    assert_eq!(owner.source().len(), 916);
    let value = owner.get("p").unwrap();
    let data = "y".repeat(900);
    for error in [
        op.request()
            .parameter_exact(ParameterLocation::Query, "p", &value)
            .json(&data)
            .prepare()
            .unwrap_err(),
        op.request()
            .json(&data)
            .parameter_exact(ParameterLocation::Query, "p", &value)
            .prepare()
            .unwrap_err(),
    ] {
        assert_eq!(kind(&error), LimitKind::InputBytes);
    }
    // Metadata + complete retained exact source + generated JSON exactly fill the allowance.
    let fits = "y".repeat(105);
    assert!(
        op.request()
            .parameter_exact(ParameterLocation::Query, "p", &value)
            .json(&fits)
            .prepare()
            .is_ok()
    );
    assert!(
        op.request()
            .json(&fits)
            .parameter_exact(ParameterLocation::Query, "p", &value)
            .prepare()
            .is_ok()
    );
    let calls = Cell::new(0);
    let full = Value::parse(
        format!("{{\"pad\":\"{}\",\"p\":1}}", "x".repeat(1007)),
        Limits::default(),
    )
    .unwrap();
    let error = op
        .request()
        .parameter_exact(ParameterLocation::Query, "p", &full.get("p").unwrap())
        .json(&Counted {
            calls: &calls,
            value: &1,
        })
        .prepare()
        .unwrap_err();
    assert_eq!(kind(&error), LimitKind::InputBytes);
    assert_eq!(calls.get(), 0);
}

#[test]
fn direct_forms_reject_duplicate_names_before_value_callbacks_with_field_context() {
    use dynamic_openapi_client::{diagnostics::DiagnosticContext, form::FormReason};
    struct Later<'a> {
        calls: &'a Cell<usize>,
        fail: bool,
    }
    impl Serialize for Later<'_> {
        fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
            self.calls.set(self.calls.get() + 1);
            if self.fail {
                Err(serde::ser::Error::custom("private detail"))
            } else {
                s.serialize_i32(2)
            }
        }
    }
    struct Duplicate<'a> {
        calls: &'a Cell<usize>,
        fail: bool,
    }
    impl Serialize for Duplicate<'_> {
        fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
            let mut map = s.serialize_map(Some(2))?;
            map.serialize_entry("p\n", &1)?;
            map.serialize_entry(
                "p\n",
                &Later {
                    calls: self.calls,
                    fail: self.fail,
                },
            )?;
            map.end()
        }
    }
    let op = operation(Limits::default(), "application/x-www-form-urlencoded");
    let explicit = op
        .request()
        .form_fields([
            FormField::value("p\n", &1, Limits::default()).unwrap(),
            FormField::value("p\n", &2, Limits::default()).unwrap(),
        ])
        .prepare()
        .unwrap_err();
    for fail in [false, true] {
        let calls = Cell::new(0);
        let error = op
            .request()
            .form(&Duplicate {
                calls: &calls,
                fail,
            })
            .prepare()
            .unwrap_err();
        assert_eq!(calls.get(), 0);
        assert_eq!(error.code(), Code::InvalidSelection);
        assert_eq!(
            error.reason(),
            Some(&DiagnosticReason::Form(FormReason::DuplicateField))
        );
        assert_eq!(
            error.context(),
            Some(&DiagnosticContext::FormField {
                index: 1,
                name: "p\n".into(),
                item: None
            })
        );
        assert_eq!(error.reason(), explicit.reason());
        assert_eq!(error.context(), explicit.context());
        assert!(!format!("{error} {error:?}").contains("private detail"));
    }
}

#[test]
fn projection_keeps_root_whitespace_coordinates_and_projects_selected_children() {
    let value = Value::parse("\n\n  \"bad\"\n", Limits::default()).unwrap();
    let error = value.deserialize::<u64>().unwrap_err();
    let parent = serde_json::from_str::<u64>(value.source()).unwrap_err();
    assert_eq!((error.line(), error.column()), (3, 7));
    assert_eq!(error.detail().unwrap().to_string(), parent.to_string());
    let child = Value::parse("\n{\"selected\": 7, \"other\": false}\n", Limits::default())
        .unwrap()
        .get("selected")
        .unwrap();
    assert_eq!(child.deserialize::<u64>().unwrap(), 7);
}

#[test]
fn projection_preserves_structured_serde_error_without_exposing_it_in_the_chain() {
    use std::error::Error;
    let data = Value::parse("\"private-data\"", Limits::default())
        .unwrap()
        .deserialize::<u64>()
        .unwrap_err();
    let syntax = Value::parse("1e400", Limits::default())
        .unwrap()
        .deserialize::<f64>()
        .unwrap_err();
    assert_eq!(data.code(), syntax.code());
    assert_eq!(
        data.detail().unwrap().classify(),
        serde_json::error::Category::Data
    );
    assert_eq!(
        syntax.detail().unwrap().classify(),
        serde_json::error::Category::Syntax
    );
    assert!(data.detail().unwrap().is_data());
    assert!(syntax.detail().unwrap().is_syntax());
    let cloned = data.clone();
    assert!(std::ptr::eq(
        data.detail().unwrap(),
        cloned.detail().unwrap()
    ));
    assert_eq!(data, cloned);
    assert!(!format!("{data:?} {data}").contains("private-data"));
    assert!(data.source().is_none()); // Parent projection errors also withheld unsafe Serde text.
}

#[test]
fn restored_string_lower_bound_and_safe_parse_request_source_chains() {
    use std::error::Error;
    let limit = operation(
        Limits {
            body_bytes: 8,
            ..Limits::default()
        },
        "application/json",
    )
    .request()
    .json(&"x".repeat(1_000_000))
    .prepare()
    .unwrap_err();
    assert_eq!(
        limit.reason(),
        Some(&DiagnosticReason::Limit {
            kind: LimitKind::BodyBytes,
            maximum: 8,
            actual: 1_000_002
        })
    );
    assert!(limit.source_bytes().is_empty());
    let parse = Value::parse("{\"private-source\":", Limits::default()).unwrap_err();
    let op = operation(Limits::default(), "application/json");
    let prepare = op
        .request()
        .parameter(ParameterLocation::Query, "private-unknown", &1)
        .prepare()
        .unwrap_err();
    let construction = op.request().json(&f64::NAN).prepare().unwrap_err();
    for error in [limit, parse, prepare, construction] {
        let cause = error
            .source()
            .unwrap()
            .downcast_ref::<Diagnostic>()
            .unwrap();
        assert_eq!(cause, &error);
        assert_eq!(cause.code(), error.code());
        assert_eq!(cause.reason(), error.reason());
        assert_eq!(cause.context(), error.context());
        assert_eq!(cause.location(), error.location());
        assert_eq!(cause.source_context(), error.source_context());
        assert_eq!(cause.source_position(), error.source_position());
        assert_eq!(cause.source_bytes(), error.source_bytes());
        assert!(cause.source().is_none());
        assert!(!format!("{cause:?} {cause}").contains("private-"));
    }
}
