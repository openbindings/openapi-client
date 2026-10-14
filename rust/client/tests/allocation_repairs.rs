use dynamic_openapi_client::*;
use serde_json::json;

fn exact(text: &str) -> Value {
    ExactJson::parse(text, Limits::default()).unwrap().root()
}
fn path_document(version: &str, limits: Limits) -> Document {
    let mut p = json!({"name":"p","in":"path","required":true,"schema":{"type":"string"}});
    let mut q = p.clone();
    q["name"] = json!("q");
    let source = if version == "2.0" {
        p.as_object_mut().unwrap().remove("schema");
        q.as_object_mut().unwrap().remove("schema");
        p["type"] = json!("string");
        q["type"] = json!("string");
        json!({"swagger":version,"schemes":["https"],"host":"api.example","paths":{"/pre{p}mid{p}/tail{q}suffix":{"get":{"operationId":"send","parameters":[p,q]}}}})
    } else {
        json!({"openapi":version,"servers":[{"url":"https://api.example"}],"paths":{"/pre{p}mid{p}/tail{q}suffix":{"get":{"operationId":"send","parameters":[p,q]}}}})
    };
    Document::parse(source.to_string(), None, limits).unwrap()
}
#[test]
fn repeated_path_markers_preserve_framing_and_treat_supplied_markers_as_data() {
    for version in ["2.0", "3.0.4", "3.1.2", "3.2.1"] {
        let document = path_document(version, Limits::default());
        let operation = document.operation_id("send").unwrap();
        for (p, encoded) in [
            ("", ""),
            ("x", "x"),
            ("é +%/{q}", "%C3%A9%20%2B%25%2F%7Bq%7D"),
        ] {
            let expected = format!("https://api.example/pre{encoded}mid{encoded}/tailQsuffix");
            let ordinary = operation
                .request()
                .parameter(ParameterLocation::Path, "p", p)
                .parameter(ParameterLocation::Path, "q", "Q")
                .prepare()
                .unwrap();
            assert_eq!(ordinary.target(), expected, "{version}");
            let input = Input {
                parameters: vec![
                    ParameterInput {
                        name: "p".into(),
                        location: ParameterLocation::Path,
                        value: exact(&json!(p).to_string()),
                    },
                    ParameterInput {
                        name: "q".into(),
                        location: ParameterLocation::Path,
                        value: exact("\"Q\""),
                    },
                ],
                ..Input::default()
            };
            assert_eq!(
                operation.prepare(&input).unwrap().target(),
                expected,
                "{version}"
            );
        }
    }
}
#[test]
fn completed_repeated_target_admission_and_cancellation_keep_context() {
    let expected = "https://api.example/prelongmidlong/tailQsuffix";
    for cap in [expected.len() - 1, expected.len()] {
        let document = path_document(
            "3.2.1",
            Limits {
                target_bytes: cap,
                ..Limits::default()
            },
        );
        let operation = document.operation_id("send").unwrap();
        let result = operation
            .request()
            .parameter(ParameterLocation::Path, "p", "long")
            .parameter(ParameterLocation::Path, "q", "Q")
            .prepare();
        if cap == expected.len() {
            assert_eq!(result.unwrap().target(), expected);
        } else {
            let error = result.unwrap_err();
            assert_eq!(
                error.diagnostic().reason(),
                Some(&DiagnosticReason::Limit {
                    kind: LimitKind::TargetBytes,
                    maximum: cap,
                    actual: expected.len(),
                })
            );
            assert_eq!(
                error.diagnostic().context(),
                Some(&DiagnosticContext::PreparedRequest)
            );
        }
        let cancel = Cancellation::default();
        cancel.cancel();
        assert_eq!(
            operation
                .request()
                .parameter(ParameterLocation::Path, "p", "long")
                .parameter(ParameterLocation::Path, "q", "Q")
                .prepare_with_cancellation(&cancel)
                .unwrap_err()
                .diagnostic()
                .code(),
            Code::Cancelled
        );
        assert_eq!(
            operation
                .prepare_with_cancellation(&Input::default(), &cancel)
                .unwrap_err()
                .code(),
            Code::Cancelled
        );
    }
}
#[test]
fn joined_form_keys_keep_decoded_text_exact_numbers_and_owned_result() {
    for version in ["3.0.4", "3.1.2", "3.2.1"] {
        for multipart in [false, true] {
            let media = if multipart {
                "multipart/form-data"
            } else {
                "application/x-www-form-urlencoded"
            };
            let document = Document::parse(json!({"openapi":version,"servers":[{"url":"https://api.example"}],"paths":{"/send":{"post":{"operationId":"send","requestBody":{"content":{media:{"schema":{"type":"object","properties":{"p":{"type":"object"}}},"encoding":{"p":{"style":"pipeDelimited","explode":false}}}}}}}}}).to_string(),None,Limits::default()).unwrap();
            let input = exact(r#"{"caf\u00e9 x":1e+3,"q\"":-0}"#);
            let operation = document.operation_id("send").unwrap();
            let request = operation
                .request()
                .form([FormField::new("p", input.clone())])
                .prepare()
                .unwrap();
            drop(input);
            drop(operation);
            drop(document);
            let expected = if multipart {
                let content_type = std::str::from_utf8(
                    request
                        .headers()
                        .iter()
                        .find(|h| h.name().eq_ignore_ascii_case("content-type"))
                        .unwrap()
                        .value(),
                )
                .unwrap();
                let boundary = content_type.split_once("boundary=").unwrap().1;
                let (part_media, body) = if version == "3.0.4" {
                    // OAS 3.0 multipart ignores style/explode and selects the
                    // default object JSON representation, preserving lexemes.
                    ("application/json", r#"{"caf\u00e9 x":1e+3,"q\"":-0}"#)
                } else {
                    ("text/plain", "café x|1e+3|q\"|-0")
                };
                format!(
                    "--{boundary}\r\nContent-Disposition: form-data; name=\"p\"\r\nContent-Type: {part_media}\r\n\r\n{body}\r\n--{boundary}--\r\n"
                )
            } else {
                "p=caf%C3%A9%20x%7C1e%2B3%7Cq%22%7C-0".into()
            };
            assert_eq!(
                request.body().map(AsRef::as_ref),
                Some(expected.as_bytes()),
                "{version}"
            );
        }
    }
}
