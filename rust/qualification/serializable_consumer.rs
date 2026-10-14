//! Public packaged-crate controls; feature flags are owned by the external consumer.
use dynamic_openapi_client::Limits;
use dynamic_openapi_client::diagnostics::{
    Code, DiagnosticContext, DiagnosticReason, LimitKind, SerializationReason,
};
use dynamic_openapi_client::exact::{Value, ValueKind};
use dynamic_openapi_client::source::SourceOrigin;
use serde::Serialize;
use std::collections::BTreeMap;

const NUMBER: &str = "$serde_json::private::Number";
const RAW: &str = "$serde_json::private::RawValue";

#[derive(Serialize)]
struct Flatten<T: Serialize> {
    #[serde(flatten)]
    inner: T,
}

fn checked<T: Serialize + ?Sized>(value: &T) -> Value {
    let result = Value::from_serializable(value, Limits::default()).unwrap();
    assert_eq!(
        result.clone().source_context().origin,
        SourceOrigin::Constructed
    );
    result
}

#[test]
fn actual_value_order_and_number_tokens_follow_unified_features() {
    let value: serde_json::Value =
        serde_json::from_str(r#"{"z":9007199254740993,"a":1.2300,"m":{"second":2,"first":1}}"#)
            .unwrap();
    let expected = match (
        cfg!(feature = "arbitrary_precision"),
        cfg!(feature = "preserve_order"),
    ) {
        (false, false) => r#"{"a":1.23,"m":{"first":1,"second":2},"z":9007199254740993}"#,
        (true, false) => r#"{"a":1.2300,"m":{"first":1,"second":2},"z":9007199254740993}"#,
        (false, true) => r#"{"z":9007199254740993,"a":1.23,"m":{"second":2,"first":1}}"#,
        (true, true) => r#"{"z":9007199254740993,"a":1.2300,"m":{"second":2,"first":1}}"#,
    };
    assert_eq!(serde_json::to_string(&value).unwrap(), expected);
    let owner = checked(&value);
    assert_eq!(owner.clone().raw(), expected);
    assert_eq!(owner.clone().get("z").unwrap().raw(), "9007199254740993");
}

#[test]
fn direct_number_and_primitive_integer_have_explicit_feature_semantics() {
    let number: serde_json::Number = serde_json::from_str("1.2300").unwrap();
    let expected = if cfg!(feature = "arbitrary_precision") {
        "1.2300"
    } else {
        "1.23"
    };
    let owner = checked(&number);
    assert_eq!(owner.clone().kind(), ValueKind::Number);
    assert_eq!(owner.clone().raw(), expected);
    let wide = serde_json::Number::from_u128(u128::MAX);
    if cfg!(feature = "arbitrary_precision") {
        assert_eq!(checked(&wide.unwrap()).clone().raw(), u128::MAX.to_string());
    } else {
        assert!(wide.is_none());
    }
    assert_eq!(checked(&u128::MAX).clone().raw(), u128::MAX.to_string());
    assert_eq!(checked(&i128::MIN).clone().raw(), i128::MIN.to_string());
}

#[test]
fn direct_raw_protocol_refuses_without_source_or_custom_content() {
    let raw =
        serde_json::value::RawValue::from_string(r#"{"secret":"raw-wrapper-sentinel"}"#.to_owned())
            .unwrap();
    let error = Value::from_serializable(&raw, Limits::default()).unwrap_err();
    assert_eq!(error.code(), Code::Serialization);
    assert_eq!(
        error.reason(),
        Some(&DiagnosticReason::Serialization(
            SerializationReason::UnsupportedRepresentation,
        )),
    );
    assert_eq!(error.context(), Some(&DiagnosticContext::Construction));
    assert!(error.source_bytes().is_empty());
    assert!(error.source_context().is_none());
    assert!(error.location().is_none());
    assert!(!format!("{error:?} {error}").contains("raw-wrapper-sentinel"));
}

#[test]
fn flattened_raw_is_the_same_literal_map_stream_as_application_data() {
    let raw = serde_json::value::RawValue::from_string(r#"{"x":1}"#.to_owned()).unwrap();
    let flattened = Flatten { inner: raw };
    let expected = r#"{"$serde_json::private::RawValue":"{\"x\":1}"}"#;
    assert_eq!(serde_json::to_string(&flattened).unwrap(), expected);
    let owner = checked(&flattened);
    assert_eq!(owner.clone().raw(), expected);
    assert_eq!(owner.clone().kind(), ValueKind::Object);
    assert_eq!(owner.clone().get(RAW).unwrap().as_str(), Some(r#"{"x":1}"#));
    assert!(owner.clone().get("x").is_none());
    assert_eq!(
        checked(&BTreeMap::from([(RAW, r#"{"x":1}"#)]))
            .clone()
            .raw(),
        expected
    );
}

#[test]
fn flattened_number_records_feature_dependent_provenance_erasure() {
    let number: serde_json::Number = serde_json::from_str("1.2300").unwrap();
    let flattened = Flatten { inner: number };
    if cfg!(feature = "arbitrary_precision") {
        let expected = r#"{"$serde_json::private::Number":"1.2300"}"#;
        assert_eq!(serde_json::to_string(&flattened).unwrap(), expected);
        let owner = checked(&flattened);
        assert_eq!(owner.clone().raw(), expected);
        assert_eq!(owner.clone().get(NUMBER).unwrap().as_str(), Some("1.2300"));
        assert_eq!(
            checked(&BTreeMap::from([(NUMBER, "1.2300")])).clone().raw(),
            expected
        );
    } else {
        assert!(serde_json::to_string(&flattened).is_err());
        let error = Value::from_serializable(&flattened, Limits::default()).unwrap_err();
        assert_eq!(error.code(), Code::Serialization);
        assert_eq!(
            error.reason(),
            Some(&DiagnosticReason::Serialization(
                SerializationReason::Custom
            )),
        );
        assert!(error.source_bytes().is_empty());
    }
}

#[test]
fn private_marker_spellings_are_literal_keys_in_maps_and_values() {
    let map = BTreeMap::from([
        (NUMBER, "1.2300"),
        (RAW, r#"{"x":1}"#),
        ("$serde_json::private::Unknown", "literal"),
    ]);
    let expected = r#"{"$serde_json::private::Number":"1.2300","$serde_json::private::RawValue":"{\"x\":1}","$serde_json::private::Unknown":"literal"}"#;
    let owner = checked(&map);
    assert_eq!(owner.clone().raw(), expected);
    // Construct the application Value directly. serde_json's deserializer has
    // its own private-key recognition; that is outside this Serialize boundary.
    let mut object = serde_json::Map::new();
    for (name, text) in &map {
        object.insert(
            (*name).to_owned(),
            serde_json::Value::String((*text).to_owned()),
        );
    }
    let value = serde_json::Value::Object(object);
    assert_eq!(checked(&value).clone().raw(), expected);
    for name in [NUMBER, RAW, "$serde_json::private::Unknown"] {
        assert_eq!(owner.clone().get(name).unwrap().kind(), ValueKind::String);
    }
}

#[test]
fn actual_value_escaping_and_number_limits_remain_checked() {
    let text = serde_json::json!("\n\n");
    let owner = Value::from_serializable(
        &text,
        Limits {
            document_bytes: 6,
            ..Limits::default()
        },
    )
    .unwrap();
    assert_eq!(owner.clone().raw(), r#""\n\n""#);
    let number: serde_json::Number = serde_json::from_str("12345").unwrap();
    for error in [
        Value::from_serializable(
            &text,
            Limits {
                document_bytes: 5,
                ..Limits::default()
            },
        )
        .unwrap_err(),
        Value::from_serializable(
            &number,
            Limits {
                document_bytes: 4,
                ..Limits::default()
            },
        )
        .unwrap_err(),
    ] {
        assert_eq!(error.code(), Code::Limit);
        assert!(matches!(
            error.reason(),
            Some(DiagnosticReason::Limit {
                kind: LimitKind::DocumentBytes,
                ..
            })
        ));
        assert!(error.source_bytes().is_empty());
    }
}

#[test]
fn unit_variant_and_string_newtype_keys_have_literal_golden_forms() {
    #[derive(Serialize, PartialEq, Eq, PartialOrd, Ord)]
    enum UnitKey {
        Alpha,
    }
    #[derive(Serialize, PartialEq, Eq, PartialOrd, Ord)]
    struct StringKey(&'static str);
    assert_eq!(
        checked(&BTreeMap::from([(UnitKey::Alpha, 1)]))
            .clone()
            .raw(),
        r#"{"Alpha":1}"#,
    );
    assert_eq!(
        checked(&BTreeMap::from([(StringKey("a/b~"), 1)]))
            .clone()
            .raw(),
        r#"{"a/b~":1}"#,
    );
}

#[test]
fn json_container_depth_boundary_runs_on_a_default_spawned_thread() {
    use serde::{Serializer, ser::SerializeSeq};
    struct Containers(usize);
    impl Serialize for Containers {
        fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
            if self.0 == 0 {
                return serializer.serialize_unit();
            }
            let mut array = serializer.serialize_seq(Some(1))?;
            array.serialize_element(&Containers(self.0 - 1))?;
            array.end()
        }
    }
    std::thread::spawn(|| {
        assert!(Value::from_serializable(&Containers(128), Limits::default()).is_ok());
        let error = Value::from_serializable(&Containers(129), Limits::default()).unwrap_err();
        assert!(matches!(
            error.reason(),
            Some(DiagnosticReason::Limit {
                kind: LimitKind::Depth,
                maximum: 128,
                actual: 129,
            }),
        ));
        // Transparent wrapper depth is intentionally separate from JSON depth.
        #[derive(Serialize)]
        struct Transparent(Option<Box<Transparent>>);
        let mut value = Transparent(None);
        for _ in 0..8 {
            value = Transparent(Some(Box::new(value)));
        }
        let owner = Value::from_serializable(
            &value,
            Limits {
                depth: 0,
                nodes: 1,
                ..Limits::default()
            },
        )
        .unwrap();
        assert_eq!(owner.clone().raw(), "null");
    })
    .join()
    .unwrap();
}
