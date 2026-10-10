use dynamic_openapi_client::*;
use serde_json::json;

fn parameter_document(
    version: &str,
    yaml: bool,
    name: &str,
    location: &str,
    style: Option<&str>,
    explode: bool,
    reserved: bool,
) -> Document {
    let source = if yaml {
        format!(
            "openapi: {version}\nservers: [{{url: 'https://api.example'}}]\npaths:\n  /send:\n    get:\n      operationId: send\n      parameters:\n      - name: '{name}'\n        in: {location}\n{}        explode: {explode}\n        allowReserved: {reserved}\n        schema: {{type: object}}\n",
            style.map_or(String::new(), |s| format!("        style: {s}\n"))
        )
    } else {
        let mut parameter = json!({"name":name,"in":location,"explode":explode,"allowReserved":reserved,"schema":{"type":"object"}});
        if let Some(style) = style {
            parameter["style"] = json!(style);
        }
        json!({"openapi":version,"servers":[{"url":"https://api.example"}],"paths":{"/send":{"get":{"operationId":"send","parameters":[parameter]}}}}).to_string()
    };
    Document::parse(source, Some("https://api.example/spec"), Limits::default()).unwrap()
}
fn exact(text: &str) -> Value {
    ExactJson::parse(text, Limits::default()).unwrap().root()
}
fn direct(
    operation: &Operation,
    location: ParameterLocation,
    name: &str,
    value: Value,
) -> Result<PreparedRequest, Diagnostic> {
    operation.prepare(&Input {
        parameters: vec![ParameterInput {
            location,
            name: name.into(),
            value,
        }],
        ..Input::default()
    })
}
fn check_value_error(error: &Diagnostic, value: &Value, child: &str, reason: DiagnosticReason) {
    let selected = value.pointer(child).unwrap();
    assert_eq!(error.code(), Code::UnsupportedValue);
    assert_eq!(error.reason(), Some(&reason));
    assert_eq!(error.source_context(), Some(selected.source_context()));
    assert_eq!(error.location(), Some(&selected.location()));
}
#[test]
fn deep_object_errors_point_to_exact_and_generated_caller_values() {
    for version in ["3.0.0", "3.0.4", "3.1.0", "3.1.2", "3.2.0", "3.2.1"] {
        for yaml in [false, true] {
            let d =
                parameter_document(version, yaml, "p", "query", Some("deepObject"), true, false);
            let op = d.operation_id("send").unwrap();
            for (text, child, reason) in [
                (r#"["x"]"#, "", DiagnosticReason::UnsupportedStyleValue),
                (r#""scalar""#, "", DiagnosticReason::UnsupportedStyleValue),
                (
                    r#"{"x[y]":1}"#,
                    "/x[y]",
                    DiagnosticReason::AmbiguousStyleDelimiter,
                ),
                (
                    r#"{"x":{"y":1}}"#,
                    "/x",
                    DiagnosticReason::UnsupportedStyleValue,
                ),
            ] {
                let value = exact(text);
                let error = direct(&op, ParameterLocation::Query, "p", value.clone()).unwrap_err();
                check_value_error(&error, &value, child, reason.clone());
                let error = op
                    .request()
                    .parameter(ParameterLocation::Query, "p", value.clone())
                    .prepare()
                    .unwrap_err();
                check_value_error(error.diagnostic(), &value, child, reason);
                assert!(
                    matches!(error.diagnostic().context(),Some(DiagnosticContext::Parameter {index:0,location:ParameterLocation::Query,name}) if name=="p")
                );
            }
            let array = [OrdinaryValue::String("x")];
            let members = [("x[y]", OrdinaryValue::U64(1))];
            for (value, child, reason) in [
                (
                    OrdinaryValue::Array(&array),
                    "/0",
                    DiagnosticReason::UnsupportedStyleValue,
                ),
                (
                    OrdinaryValue::Object(&members),
                    "/0/x[y]",
                    DiagnosticReason::AmbiguousStyleDelimiter,
                ),
            ] {
                let error = op
                    .request()
                    .parameter(ParameterLocation::Query, "p", value)
                    .prepare()
                    .unwrap_err();
                assert_eq!(error.diagnostic().code(), Code::UnsupportedValue);
                assert_eq!(error.diagnostic().reason(), Some(&reason));
                assert_eq!(
                    error.diagnostic().source_context().unwrap().origin,
                    SourceOrigin::Constructed
                );
                assert_ne!(
                    error.diagnostic().source_context(),
                    Some(d.root().source_context())
                );
                assert_eq!(error.diagnostic().location().unwrap().pointer, child);
                let equivalent =
                    ExactJson::from_ordinary(OrdinaryValue::Array(&[value]), Limits::default())
                        .unwrap();
                assert_eq!(
                    error.diagnostic().location(),
                    Some(&equivalent.root().pointer(child).unwrap().location())
                );
            }
        }
    }
}
#[test]
fn authored_deep_object_configuration_stays_at_the_declaration() {
    for yaml in [false, true] {
        for (version, name, explode, reason) in [
            (
                "3.1.2",
                "p",
                false,
                DiagnosticReason::UnsupportedStyleCombination,
            ),
            (
                "3.2.1",
                "p[x]",
                true,
                DiagnosticReason::AmbiguousStyleDelimiter,
            ),
        ] {
            let d = parameter_document(
                version,
                yaml,
                name,
                "query",
                Some("deepObject"),
                explode,
                false,
            );
            let error = d
                .operation_id("send")
                .unwrap()
                .request()
                .parameter(ParameterLocation::Query, name, exact(r#"{"x":1}"#))
                .prepare()
                .unwrap_err();
            assert_eq!(error.diagnostic().code(), Code::UnsupportedParameter);
            assert_eq!(error.diagnostic().reason(), Some(&reason));
            assert_eq!(
                error.diagnostic().source_context(),
                Some(d.root().source_trace().authored.unwrap().context())
            );
            assert_eq!(
                error.diagnostic().location().unwrap().pointer,
                "/paths/~1send/get/parameters/0"
            );
        }
    }
}
#[test]
fn cookie_false_explosion_refuses_before_values_for_all_32_schema_cookie_styles() {
    let array = [OrdinaryValue::String("a"), OrdinaryValue::String("b")];
    let object = [("x", OrdinaryValue::String("a"))];
    for version in ["3.2.0", "3.2.1"] {
        for yaml in [false, true] {
            for style in [None, Some("form"), Some("cookie")] {
                for reserved in [false, true] {
                    let d =
                        parameter_document(version, yaml, "p", "cookie", style, false, reserved);
                    let op = d.operation_id("send").unwrap();
                    for (text, ordinary) in [
                        (r#""a""#, OrdinaryValue::String("a")),
                        (r#"["a","b"]"#, OrdinaryValue::Array(&array)),
                        (r#"{"x":"a"}"#, OrdinaryValue::Object(&object)),
                    ] {
                        let direct =
                            direct(&op, ParameterLocation::Cookie, "p", exact(text)).unwrap_err();
                        let fluent = op
                            .request()
                            .parameter(ParameterLocation::Cookie, "p", exact(text))
                            .prepare()
                            .unwrap_err();
                        let generated = op
                            .request()
                            .parameter(ParameterLocation::Cookie, "p", ordinary)
                            .prepare()
                            .unwrap_err();
                        for error in [&direct, fluent.diagnostic(), generated.diagnostic()] {
                            assert_eq!(
                                error.code(),
                                Code::InvalidDeclaration,
                                "{version} {yaml} {style:?} {reserved}"
                            );
                            assert_eq!(
                                error.reason(),
                                Some(&DiagnosticReason::UnsupportedStyleCombination)
                            );
                            assert_eq!(
                                error.source_context(),
                                Some(d.root().source_trace().authored.unwrap().context())
                            );
                            let at = error.location().unwrap();
                            assert_eq!(at.pointer, "/paths/~1send/get/parameters/0/explode");
                            assert_eq!(&d.source()[at.start..at.end], "false");
                        }
                    }
                }
            }
        }
    }
}
#[test]
fn valid_exploded_cookies_and_legacy_false_explosion_keep_existing_behavior() {
    for version in ["3.0.4", "3.1.2", "3.2.0", "3.2.1"] {
        let d = parameter_document(version, false, "p", "cookie", Some("form"), true, true);
        let r = d
            .operation_id("send")
            .unwrap()
            .request()
            .parameter(ParameterLocation::Cookie, "p", exact(r#"["a","b"]"#))
            .prepare()
            .unwrap();
        assert_eq!(
            r.headers()
                .iter()
                .find(|h| h.name().eq_ignore_ascii_case("Cookie"))
                .unwrap()
                .value(),
            b"p=a; p=b"
        );
        if !version.starts_with("3.2") {
            let d = parameter_document(version, false, "p", "cookie", Some("form"), false, false);
            let r = d
                .operation_id("send")
                .unwrap()
                .request()
                .parameter(ParameterLocation::Cookie, "p", exact(r#"["a","b"]"#))
                .prepare()
                .unwrap();
            assert_eq!(
                r.headers()
                    .iter()
                    .find(|h| h.name().eq_ignore_ascii_case("Cookie"))
                    .unwrap()
                    .value(),
                b"p=a,b"
            );
        }
    }
}
#[test]
fn form_member_ambiguity_retains_the_caller_owner_and_form_context() {
    for version in ["3.1.2", "3.2.1"] {
        let d=Document::parse(json!({"openapi":version,"servers":[{"url":"https://api.example"}],"paths":{"/send":{"post":{"operationId":"send","requestBody":{"content":{"application/x-www-form-urlencoded":{"schema":{"type":"object","properties":{"p":{"type":"object"}}},"encoding":{"p":{"style":"deepObject","explode":true}}}}}}}}}).to_string(),None,Limits::default()).unwrap();
        let op = d.operation_id("send").unwrap();
        let value = exact(r#"{"x[y]":1}"#);
        let error = op
            .request()
            .form([FormField::new("p", value.clone())])
            .prepare()
            .unwrap_err();
        check_value_error(
            error.diagnostic(),
            &value,
            "/x[y]",
            DiagnosticReason::AmbiguousStyleDelimiter,
        );
        assert!(matches!(
            error.diagnostic().context(),
            Some(DiagnosticContext::FormField { index: 0, .. })
        ));
        let members = [("x[y]", OrdinaryValue::String("a"))];
        let error = op
            .request()
            .form([FormField::new("p", OrdinaryValue::Object(&members))])
            .prepare()
            .unwrap_err();
        assert_eq!(error.diagnostic().code(), Code::UnsupportedValue);
        assert_eq!(
            error.diagnostic().source_context().unwrap().origin,
            SourceOrigin::Constructed
        );
        assert_eq!(error.diagnostic().location().unwrap().pointer, "/0/x[y]");
    }
}
