use dynamic_openapi_client::*;
use serde_json::{Value as Json, json};

fn scalar(value: &Json) -> Option<OrdinaryValue<'_>> {
    Some(match value {
        Json::Null => OrdinaryValue::Null,
        Json::Bool(v) => OrdinaryValue::Bool(*v),
        Json::String(v) => OrdinaryValue::String(v),
        Json::Number(v) => {
            if let Some(v) = v.as_i64() {
                OrdinaryValue::I64(v)
            } else {
                OrdinaryValue::U64(v.as_u64()?)
            }
        }
        _ => return None,
    })
}
fn credential() -> Credential {
    Credential {
        value: CredentialValue::ApiKey("fixture-secret".into()),
        origins: vec!["https://api.example".into()],
    }
}
fn location(name: &str) -> ParameterLocation {
    match name {
        "path" => ParameterLocation::Path,
        "query" => ParameterLocation::Query,
        "header" => ParameterLocation::Header,
        "cookie" => ParameterLocation::Cookie,
        _ => panic!("location"),
    }
}
fn expected_code(reason: &str) -> (Code, Option<DiagnosticReason>) {
    match reason {
        "InvalidStyleLocation" | "UndefinedStyleCombination" => (
            Code::UnsupportedParameter,
            Some(DiagnosticReason::UnsupportedStyleCombination),
        ),
        "AmbiguousDelimiter" | "AmbiguousStyleDelimiter" => (
            Code::UnsupportedValue,
            Some(DiagnosticReason::AmbiguousStyleDelimiter),
        ),
        "UnsupportedStyleValue" | "NullSerializationPolicyRequired" => (
            Code::UnsupportedValue,
            Some(DiagnosticReason::UnsupportedStyleValue),
        ),
        "InvalidParameterContentCardinality" => (Code::InvalidDeclaration, None),
        "InvalidHeaderValue" => (Code::InvalidHeader, None),
        "FinalCredentialNameCollision" => (Code::CredentialCollision, None),
        _ => panic!("unmapped reason {reason}"),
    }
}
fn check(
    case: &Json,
    result: Result<PreparedRequest, Diagnostic>,
    input: &Value,
    ordinary: bool,
    document: &Document,
) {
    if let Some(reason) = case["expected_reason_contract"].as_str() {
        let error = result.unwrap_err();
        let (code, detail) = expected_code(reason);
        assert_eq!(error.code(), code, "{}", case["id"]);
        if let Some(detail) = detail {
            assert_eq!(error.reason(), Some(&detail));
        }
        assert!(error.location().is_some(), "{}", case["id"]);
        if matches!(code, Code::UnsupportedValue | Code::InvalidHeader) {
            if ordinary {
                assert_eq!(
                    error.source_context().unwrap().origin,
                    SourceOrigin::Constructed
                );
            } else {
                assert_eq!(error.source_context(), Some(input.source_context()));
            }
        } else {
            assert_eq!(
                error.source_context(),
                Some(document.root().source_context())
            );
        }
        let display = format!("{error} {error:?}");
        assert!(!display.contains("fixture-secret"));
        assert!(!display.contains("Authorization: secret"));
        return;
    }
    let prepared = result.unwrap();
    if let Some(parts) = case["expected_parts"].as_array() {
        let media = prepared
            .headers()
            .iter()
            .find(|h| h.name().eq_ignore_ascii_case("content-type"))
            .unwrap();
        let media = std::str::from_utf8(media.value()).unwrap();
        let boundary = media.split_once("boundary=").unwrap().1;
        let mut expected = String::new();
        for part in parts {
            expected.push_str(&format!("--{boundary}\r\nContent-Disposition: form-data; name=\"{}\"\r\nContent-Type: {}\r\n\r\n{}\r\n",part["name"].as_str().unwrap(),part["content_type"].as_str().unwrap(),part["body_utf8"].as_str().unwrap()));
        }
        expected.push_str(&format!("--{boundary}--\r\n"));
        assert_eq!(
            prepared.body().map(AsRef::as_ref).unwrap(),
            expected.as_bytes(),
            "{}",
            case["id"]
        );
    } else if let Some(expected) = case["expected_body_utf8"].as_str() {
        assert_eq!(
            prepared.body().map(AsRef::as_ref).unwrap(),
            expected.as_bytes(),
            "{}",
            case["id"]
        );
    } else {
        let expected = case["expected_fragment_utf8"]
            .as_str()
            .or_else(|| case["expected_utf8"].as_str())
            .unwrap();
        let p = &case["parameter"];
        let actual = match p["in"].as_str().unwrap() {
            "path" => prepared
                .target()
                .strip_prefix("https://api.example/prefix")
                .unwrap(),
            "query" => prepared
                .target()
                .split_once('?')
                .unwrap()
                .1
                .strip_suffix("&token=fixture-secret")
                .unwrap_or_else(|| prepared.target().split_once('?').unwrap().1),
            "header" => std::str::from_utf8(
                prepared
                    .headers()
                    .iter()
                    .find(|h| h.name().eq_ignore_ascii_case(p["name"].as_str().unwrap()))
                    .unwrap()
                    .value(),
            )
            .unwrap(),
            _ => unreachable!(),
        };
        assert_eq!(actual, expected, "{}", case["id"]);
    }
}
fn run(id: &str) {
    let cases: Json =
        serde_json::from_str(include_str!("s2a-fixtures/frozen-vectors.json")).unwrap();
    let case = cases
        .as_array()
        .unwrap()
        .iter()
        .find(|v| v["id"] == id)
        .unwrap();
    let input = ExactJson::parse(case["input_json"].as_str().unwrap(), Limits::default())
        .unwrap()
        .root();
    for family in case["families"].as_array().unwrap() {
        let versions: &[&str] = match family.as_str().unwrap() {
            "3.0" => &["3.0.0", "3.0.4"],
            "3.1" => &["3.1.0", "3.1.2"],
            "3.2" => &["3.2.0", "3.2.1"],
            _ => unreachable!(),
        };
        for version in versions {
            let p = case.get("parameter");
            let path = if p.is_some_and(|p| p["in"] == "path") {
                format!("/prefix{{{}}}", p.unwrap()["name"].as_str().unwrap())
            } else {
                "/send".into()
            };
            let mut op = json!({"operationId":"send","responses":{"200":{"description":"ok"}}});
            if let Some(p) = p {
                op["parameters"] = json!([p]);
            } else {
                op["requestBody"] = case["complete_request_body"].clone();
            }
            let mut source = json!({"openapi":version,"info":{"title":"S2a frozen vectors","version":"1"},"servers":[{"url":"https://api.example"}],"paths":{path:{"post":op}}});
            let secured = matches!(
                id,
                "content-name-credential-collision" | "content-value-cannot-create-credential"
            );
            if secured {
                source["components"] = json!({"securitySchemes":{"key":{"type":"apiKey","in":"query","name":"token"}}});
                source["security"] = json!([{"key":[]}]);
            }
            let document = Document::parse(source.to_string(), None, Limits::default()).unwrap();
            let operation = document.operation_id("send").unwrap();
            let prepare = |value: Argument<'_>| {
                let mut request = operation.request();
                if let Some(p) = p {
                    request = request.parameter(
                        location(p["in"].as_str().unwrap()),
                        p["name"].as_str().unwrap(),
                        value,
                    );
                } else {
                    request = request
                        .form([FormField::new(case["input_field"].as_str().unwrap(), value)]);
                }
                if secured {
                    request = request.credential("key", credential());
                }
                request.prepare().map_err(|e| e.diagnostic().clone())
            };
            check(
                case,
                prepare(Argument::Exact(input.clone())),
                &input,
                false,
                &document,
            );
            if let Some(p) = p {
                let mut direct = Input {
                    parameters: vec![ParameterInput {
                        location: location(p["in"].as_str().unwrap()),
                        name: p["name"].as_str().unwrap().into(),
                        value: input.clone(),
                    }],
                    ..Input::default()
                };
                if secured {
                    direct.credentials.insert("key".into(), credential());
                }
                check(case, operation.prepare(&direct), &input, false, &document);
            }
            if case["exact_only"] == true {
                continue;
            }
            let parsed: Json = serde_json::from_str(case["input_json"].as_str().unwrap()).unwrap();
            let object = parsed.as_object().and_then(|o| {
                o.iter()
                    .map(|(k, v)| Some((k.as_str(), scalar(v)?)))
                    .collect::<Option<Vec<_>>>()
            });
            let array = parsed
                .as_array()
                .and_then(|a| a.iter().map(scalar).collect::<Option<Vec<_>>>());
            // The supplied exact route above preserves authored object order. Ordinary
            // maps preserve their caller order, reconstructed below from that exact owner.
            let ordered = object.as_ref().map(|members| {
                input
                    .members()
                    .unwrap()
                    .map(|(k, _)| *members.iter().find(|(key, _)| *key == k).unwrap())
                    .collect::<Vec<_>>()
            });
            let value = scalar(&parsed)
                .or_else(|| ordered.as_ref().map(|v| OrdinaryValue::Object(v)))
                .or_else(|| array.as_ref().map(|v| OrdinaryValue::Array(v)));
            if let Some(value) = value {
                check(
                    case,
                    prepare(Argument::Ordinary(value)),
                    &input,
                    true,
                    &document,
                );
            }
        }
    }
}
macro_rules! case {
    ($name:ident, $id:literal) => {
        #[test]
        fn $name() {
            run($id);
        }
    };
}
include!("s2a-fixtures/cases.rs");
