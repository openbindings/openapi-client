use dynamic_openapi_client::*;
use serde::{
    Serialize, Serializer,
    ser::{SerializeMap, SerializeSeq, SerializeStruct},
};
use std::{cell::Cell, collections::BTreeMap, fmt, rc::Rc};

fn construct<T: Serialize + ?Sized>(value: &T) -> ExactJson {
    ExactJson::from_serializable(value, Limits::default()).unwrap()
}
fn failure<T: Serialize + ?Sized>(value: &T, reason: SerializationReason) -> ParseFailure {
    let error = ExactJson::from_serializable(value, Limits::default()).unwrap_err();
    assert_eq!(error.diagnostic().code(), Code::Serialization);
    assert_eq!(
        error.diagnostic().reason(),
        Some(&DiagnosticReason::Serialization(reason))
    );
    assert_eq!(
        error.diagnostic().context(),
        Some(&DiagnosticContext::Construction)
    );
    assert!(error.source_bytes().is_empty());
    assert!(error.diagnostic().location().is_none());
    assert!(error.diagnostic().source_context().is_none());
    error
}
fn operation(limits: Limits) -> Operation {
    Document::parse(br#"{"openapi":"3.1.0","servers":[{"url":"https://api.example.test"}],"paths":{"/items/{id}":{"post":{"operationId":"create","parameters":[{"in":"path","name":"id","required":true,"schema":{}}],"requestBody":{"content":{"application/json":{"schema":{}}}},"responses":{"200":{"description":"OK"}}}}}}"#, None, limits)
        .unwrap().operation_id("create").unwrap()
}

#[test]
fn borrowed_struct_dynamic_value_and_retained_handles_prepare_identical_bytes() {
    #[derive(Serialize)]
    struct Payload<'a> {
        name: &'a str,
        number: u64,
        explicit_null: Option<bool>,
        #[serde(skip_serializing_if = "Option::is_none")]
        absent: Option<bool>,
        nested: serde_json::Value,
    }
    let text = String::from("A/\"B\n雪");
    let payload = Payload {
        name: &text,
        number: 9_007_199_254_740_993,
        explicit_null: None,
        absent: None,
        nested: serde_json::json!({"a/b~": [true, {}, []]}),
    };
    let expected = serde_json::to_vec(&payload).unwrap();
    let owner = construct(&payload);
    assert_eq!(owner.root().raw().as_bytes(), expected);
    let root = owner.root();
    assert!(root.get("absent").is_none());
    assert_eq!(root.get("explicit_null").unwrap().kind(), ValueKind::Null);
    assert_eq!(root.get("number").unwrap().raw(), "9007199254740993");
    let child = root.get("nested").unwrap().get("a/b~").unwrap();
    let source = child.source_context();
    assert_eq!(source.origin, SourceOrigin::Constructed);
    drop(owner);
    drop(payload);
    drop(text);
    let op = operation(Limits::default());
    let request = op
        .request()
        .parameter(ParameterLocation::Path, "id", 3)
        .json(root)
        .prepare()
        .unwrap();
    drop(op);
    assert_eq!(request.body().unwrap(), expected);
    assert_eq!(child.source_context(), source);
    assert_eq!(child.location().pointer, "/nested/a~1b~0");
    assert_eq!(child.at(0).unwrap().as_bool(), Some(true));
    let independent = construct(&expected);
    assert_ne!(independent.root().source_context(), source);
}

#[test]
fn host_local_input_does_not_weaken_owned_auto_traits() {
    struct Local<'a>(&'a Rc<Cell<u32>>);
    impl Serialize for Local<'_> {
        fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
            serializer.serialize_u32(self.0.get())
        }
    }
    fn shared<T: Send + Sync>(_: &T) {}
    let local = Rc::new(Cell::new(7));
    let owner = construct(&Local(&local));
    shared(&owner);
    shared(&owner.root());
    shared(&operation(Limits::default()).request());
    let error = failure(&f64::NAN, SerializationReason::NonFiniteNumber);
    shared(&error);
    assert_eq!(owner.root().raw(), "7");
    assert_eq!(construct("unsized").root().raw(), "\"unsized\"");
}

#[test]
fn serializer_errors_can_be_formatted_into_successful_output_without_custom_text() {
    struct Secret;
    impl fmt::Display for Secret {
        fn fmt(&self, _: &mut fmt::Formatter<'_>) -> fmt::Result {
            panic!("custom error input must not be formatted")
        }
    }
    struct ErrorAsData;
    impl Serialize for ErrorAsData {
        fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
            let error = <S::Error as serde::ser::Error>::custom(Secret);
            assert!(std::error::Error::source(&error).is_none());
            [
                format!("{error}"),
                format!("{error:#}"),
                format!("{error:?}"),
                format!("{error:#?}"),
            ]
            .serialize(serializer)
        }
    }
    let owner = construct(&ErrorAsData);
    let expected = [
        "Serialization: Serialization(Custom)",
        "Serialization: Serialization(Custom)",
        "Error(Serialization: Serialization(Custom))",
        "Error(\n    Serialization: Serialization(Custom),\n)",
    ];
    for (index, expected) in expected.into_iter().enumerate() {
        assert_eq!(owner.root().at(index).unwrap().as_str(), Some(expected));
    }
}

#[test]
fn numbers_keep_exact_integer_and_finite_float_representation() {
    assert_eq!(construct(&i128::MIN).root().raw(), i128::MIN.to_string());
    assert_eq!(construct(&u128::MAX).root().raw(), u128::MAX.to_string());
    assert_eq!(construct(&u64::MAX).root().raw(), u64::MAX.to_string());
    for value in [
        0.0_f64,
        -0.0,
        f64::MIN_POSITIVE,
        f64::from_bits(1),
        f64::MAX,
        0.1,
        1e100,
    ] {
        assert_eq!(
            construct(&value).root().raw(),
            serde_json::to_string(&value).unwrap()
        );
    }
    for value in [0.1_f32, -0.0, f32::MAX] {
        assert_eq!(
            construct(&value).root().raw(),
            serde_json::to_string(&value).unwrap()
        );
    }
    for value in [f64::NAN, f64::INFINITY, f64::NEG_INFINITY] {
        failure(&value, SerializationReason::NonFiniteNumber);
    }
    failure(&f32::NAN, SerializationReason::NonFiniteNumber);
}

#[test]
fn standard_serde_shapes_and_string_keys_match_the_documented_profile() {
    #[derive(Serialize)]
    enum Enum {
        Unit,
        Newtype(u8),
        Tuple(u8, bool),
        Struct { value: u8 },
    }
    #[derive(Serialize)]
    struct Newtype(u8);
    struct Bytes;
    impl Serialize for Bytes {
        fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
            serializer.serialize_bytes(&[0, 127, 255])
        }
    }
    for variant in [
        Enum::Unit,
        Enum::Newtype(7),
        Enum::Tuple(7, true),
        Enum::Struct { value: 7 },
    ] {
        assert_eq!(
            construct(&variant).root().raw(),
            serde_json::to_string(&variant).unwrap()
        );
    }
    assert_eq!(construct(&Bytes).root().raw(), "[0,127,255]");
    assert_eq!(construct(&Newtype(7)).root().raw(), "7");
    assert_eq!(construct(&Some(Some(7))).root().raw(), "7");
    assert_eq!(construct(&None::<u8>).root().raw(), "null");
    assert_eq!(construct(&()).root().raw(), "null");
    assert_eq!(construct(&('雪', 4)).root().raw(), "[\"雪\",4]");
    assert_eq!(
        construct(&BTreeMap::from([('a', 1)])).root().raw(),
        "{\"a\":1}"
    );
    failure(
        &BTreeMap::from([(7, true)]),
        SerializationReason::NonStringKey,
    );
    failure(
        &BTreeMap::from([(true, 1)]),
        SerializationReason::NonStringKey,
    );
    failure(
        &BTreeMap::from([(None::<String>, 1)]),
        SerializationReason::NonStringKey,
    );
}

struct Duplicate;
impl Serialize for Duplicate {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut map = serializer.serialize_map(Some(2))?;
        map.serialize_entry("same", &1)?;
        map.serialize_entry("same", &2)?;
        map.end()
    }
}
#[test]
fn duplicates_use_existing_exact_parser_with_complete_constructed_source() {
    let error = ExactJson::from_serializable(&Duplicate, Limits::default()).unwrap_err();
    let diagnostic = error.diagnostic();
    assert_eq!(diagnostic.code(), Code::DuplicateMember);
    assert_eq!(error.source_bytes(), br#"{"same":1,"same":2}"#);
    assert_eq!(
        diagnostic.source_context().unwrap().origin,
        SourceOrigin::Constructed
    );
    assert_eq!(diagnostic.related().len(), 1);
    for range in [diagnostic.location().unwrap(), &diagnostic.related()[0]] {
        assert_eq!(&error.source_bytes()[range.start..range.end], br#""same""#);
    }
}

struct Counted<'a>(&'a Cell<usize>);
impl Serialize for Counted<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        self.0.set(self.0.get() + 1);
        serializer.serialize_u8(1)
    }
}
struct Ignored<'a> {
    later: &'a Cell<usize>,
    key: bool,
}
impl Serialize for Ignored<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        if self.key {
            let mut map = serializer.serialize_map(None)?;
            let _ = map.serialize_key(&7);
            for _ in 0..100 {
                let _ = map.serialize_entry(&Counted(self.later), &Counted(self.later));
            }
            map.end()
        } else {
            let mut seq = serializer.serialize_seq(None)?;
            let _ = seq.serialize_element(&f64::NAN);
            for _ in 0..100 {
                let _ = seq.serialize_element(&Counted(self.later));
            }
            seq.end()
        }
    }
}
#[test]
fn first_error_is_fused_before_later_serializer_callbacks() {
    for key in [false, true] {
        let later = Cell::new(0);
        failure(
            &Ignored { later: &later, key },
            if key {
                SerializationReason::NonStringKey
            } else {
                SerializationReason::NonFiniteNumber
            },
        );
        assert_eq!(later.get(), 0);
    }
    let later = Cell::new(0);
    let error = ExactJson::from_serializable(
        &[Counted(&later)],
        Limits {
            nodes: 1,
            ..Limits::default()
        },
    )
    .unwrap_err();
    assert_eq!(later.get(), 0);
    assert!(matches!(
        error.diagnostic().reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::Nodes,
            ..
        })
    ));
    let error = ExactJson::from_serializable(
        &Counted(&later),
        Limits {
            nodes: 0,
            ..Limits::default()
        },
    )
    .unwrap_err();
    assert_eq!(error.diagnostic().code(), Code::Limit);
    assert_eq!(later.get(), 0);
}

struct Formatting<T>(T);
impl<T: fmt::Display> Serialize for Formatting<T> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        serializer.collect_str(&self.0)
    }
}
struct Overflow;
impl fmt::Display for Overflow {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let _ = f.write_str("too-long");
        for _ in 0..100 {
            let _ = f.write_str("later");
        }
        Ok(())
    }
}
struct BadDisplay;
impl fmt::Display for BadDisplay {
    fn fmt(&self, _: &mut fmt::Formatter<'_>) -> fmt::Result {
        Err(fmt::Error)
    }
}
#[test]
fn bounded_display_capture_and_error_text_do_not_escape() {
    let error = ExactJson::from_serializable(
        &Formatting(Overflow),
        Limits {
            document_bytes: 4,
            ..Limits::default()
        },
    )
    .unwrap_err();
    assert!(matches!(
        error.diagnostic().reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::DocumentBytes,
            maximum: 4,
            ..
        })
    ));
    failure(&Formatting(BadDisplay), SerializationReason::Custom);
    assert_eq!(construct(&Formatting(42)).root().raw(), "\"42\"");
    struct KeyMap;
    impl Serialize for KeyMap {
        fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
            let mut map = serializer.serialize_map(Some(1))?;
            map.serialize_entry(&Formatting(42), &true)?;
            map.end()
        }
    }
    assert_eq!(construct(&KeyMap).root().raw(), "{\"42\":true}");
    struct Secret;
    impl fmt::Display for Secret {
        fn fmt(&self, _: &mut fmt::Formatter<'_>) -> fmt::Result {
            panic!("custom error Display must not run")
        }
    }
    impl Serialize for Secret {
        fn serialize<S: Serializer>(&self, _: S) -> Result<S::Ok, S::Error> {
            Err(serde::ser::Error::custom(self))
        }
    }
    let error = failure(&Secret, SerializationReason::Custom);
    assert!(!format!("{error:?} {error}").contains("secret"));
}

struct Nest(usize);
impl Serialize for Nest {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        if self.0 == 0 {
            return serializer.serialize_unit();
        }
        let mut seq = serializer.serialize_seq(Some(1))?;
        seq.serialize_element(&Nest(self.0 - 1))?;
        seq.end()
    }
}
#[test]
fn exact_byte_node_depth_limits_and_untrusted_hints() {
    for (value, bytes) in [("a", 3), ("\n", 4), ("雪", 5)] {
        assert!(
            ExactJson::from_serializable(
                value,
                Limits {
                    document_bytes: bytes,
                    ..Limits::default()
                }
            )
            .is_ok()
        );
        let error = ExactJson::from_serializable(
            value,
            Limits {
                document_bytes: bytes - 1,
                ..Limits::default()
            },
        )
        .unwrap_err();
        assert_eq!(error.diagnostic().code(), Code::Limit);
        assert!(error.source_bytes().is_empty());
    }
    let object = BTreeMap::from([("key", 1)]);
    assert!(
        ExactJson::from_serializable(
            &object,
            Limits {
                nodes: 2,
                ..Limits::default()
            }
        )
        .is_ok(),
        "member names are not value nodes"
    );
    struct Hint;
    impl Serialize for Hint {
        fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
            serializer.serialize_seq(Some(usize::MAX))?.end()
        }
    }
    assert_eq!(construct(&Hint).root().raw(), "[]");
    std::thread::Builder::new()
        .stack_size(8 * 1024 * 1024)
        .spawn(|| {
            assert!(ExactJson::from_serializable(&Nest(128), Limits::default()).is_ok());
            for limits in [
                Limits::default(),
                Limits {
                    depth: 1000,
                    ..Limits::default()
                },
            ] {
                let error = ExactJson::from_serializable(&Nest(129), limits).unwrap_err();
                assert!(matches!(
                    error.diagnostic().reason(),
                    Some(DiagnosticReason::Limit {
                        kind: LimitKind::Depth,
                        maximum: 128,
                        actual: 129
                    })
                ));
            }
            assert!(
                ExactJson::from_serializable(
                    &Nest(3),
                    Limits {
                        depth: 3,
                        ..Limits::default()
                    }
                )
                .is_ok()
            );
            assert!(
                ExactJson::from_serializable(
                    &Nest(4),
                    Limits {
                        depth: 3,
                        ..Limits::default()
                    }
                )
                .is_err()
            );
        })
        .unwrap()
        .join()
        .unwrap();
}

#[test]
fn cancellation_precedes_callback_and_is_checked_mid_construction_without_poison() {
    let cancel = Cancellation::default();
    cancel.cancel();
    let calls = Cell::new(0);
    let error = ExactJson::from_serializable_with_cancellation(
        &Counted(&calls),
        Limits::default(),
        &cancel,
    )
    .unwrap_err();
    assert_eq!(error.diagnostic().code(), Code::Cancelled);
    assert_eq!(calls.get(), 0);
    struct Cancel<'a>(&'a Cancellation);
    impl Serialize for Cancel<'_> {
        fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
            self.0.cancel();
            serializer.serialize_unit()
        }
    }
    struct During<'a>(&'a Cancellation, &'a Cell<usize>);
    impl Serialize for During<'_> {
        fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
            let mut seq = serializer.serialize_seq(None)?;
            let _ = seq.serialize_element(&Cancel(self.0));
            let _ = seq.serialize_element(&Counted(self.1));
            seq.end()
        }
    }
    let cancel = Cancellation::default();
    let error = ExactJson::from_serializable_with_cancellation(
        &During(&cancel, &calls),
        Limits::default(),
        &cancel,
    )
    .unwrap_err();
    assert_eq!(error.diagnostic().code(), Code::Cancelled);
    assert!(error.source_bytes().is_empty());
    assert_eq!(calls.get(), 0);
    assert_eq!(construct(&1).root().raw(), "1");
}

const NUMBER: &str = "$serde_json::private::Number";
struct Token<'a> {
    text: &'a str,
    name: &'static str,
    field: &'static str,
    len: usize,
    repeat: bool,
}
impl Serialize for Token<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut value = serializer.serialize_struct(self.name, self.len)?;
        value.serialize_field(self.field, &self.text)?;
        if self.repeat {
            let _ = value.serialize_field(self.field, &self.text);
        }
        value.end()
    }
}
#[test]
fn private_number_protocol_is_validated_without_rounding_or_injection() {
    let token = |text| Token {
        text,
        name: NUMBER,
        field: NUMBER,
        len: 1,
        repeat: false,
    };
    for text in [
        "1",
        "-0",
        "1e+0002",
        "1.2300",
        "340282366920938463463374607431768211455",
        "1e999999999",
    ] {
        assert_eq!(construct(&token(text)).root().raw(), text);
    }
    for text in [
        "1,2", "1 ", " 1", "[1]", "null", "\"1\"", "NaN", "01", "-", "1\n2",
    ] {
        failure(&token(text), SerializationReason::InvalidRepresentation);
    }
    failure(
        &Token {
            field: "wrong",
            ..token("1")
        },
        SerializationReason::InvalidRepresentation,
    );
    failure(
        &Token {
            len: 2,
            ..token("1")
        },
        SerializationReason::InvalidRepresentation,
    );
    failure(
        &Token {
            repeat: true,
            ..token("1")
        },
        SerializationReason::InvalidRepresentation,
    );
    failure(
        &Token {
            name: "$serde_json::private::Unknown",
            ..token("1")
        },
        SerializationReason::UnsupportedRepresentation,
    );
    let raw = serde_json::value::RawValue::from_string("{\"x\":1}".into()).unwrap();
    failure(&raw, SerializationReason::UnsupportedRepresentation);
    let error = ExactJson::from_serializable(
        &token("12345"),
        Limits {
            document_bytes: 4,
            ..Limits::default()
        },
    )
    .unwrap_err();
    assert_eq!(error.diagnostic().code(), Code::Limit);
}

#[test]
fn malformed_compound_order_is_serialization_failure_and_body_budget_is_separate() {
    struct MissingKey;
    impl Serialize for MissingKey {
        fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
            let mut map = serializer.serialize_map(None)?;
            let _ = map.serialize_value(&1);
            map.end()
        }
    }
    failure(&MissingKey, SerializationReason::InvalidRepresentation);
    let owner = construct(&"long-body");
    let op = operation(Limits {
        body_bytes: 4,
        ..Limits::default()
    });
    let error = op
        .request()
        .parameter(ParameterLocation::Path, "id", 1)
        .json(owner.root())
        .prepare()
        .unwrap_err();
    assert!(matches!(
        error.diagnostic().reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::BodyBytes,
            maximum: 4,
            ..
        })
    ));
}
