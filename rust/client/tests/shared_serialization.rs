use dynamic_openapi_client::*;
use serde_json::{Value as Json, json};

const VERSIONS: &[&str] = &["3.0.0", "3.0.4", "3.1.0", "3.1.2", "3.2.0", "3.2.1"];
fn exact(s: &str) -> Value {
    ExactJson::parse(s, Limits::default()).unwrap().root()
}
fn parameter_doc(
    version: &str,
    location: &str,
    name: &str,
    config: Json,
    limits: Limits,
    key: Option<&str>,
) -> Document {
    let mut p = config;
    p["name"] = json!(name);
    p["in"] = json!(location);
    p["schema"] = json!({});
    let path = if location == "path" {
        p["required"] = json!(true);
        format!("/{{{name}}}")
    } else {
        "/send".into()
    };
    let mut d = json!({"openapi":version,"servers":[{"url":"https://api.example"}],"paths":{path:{"get":{"operationId":"send","parameters":[p]}}}});
    if let Some(key) = key {
        d["components"] =
            json!({"securitySchemes":{"Key":{"type":"apiKey","in":"query","name":key}}});
        d["security"] = json!([{"Key":[]}]);
    }
    Document::parse(d.to_string(), Some("https://api.example/spec"), limits).unwrap()
}
fn form_doc(version: &str, media: &str, schema: Json, encoding: Json, limits: Limits) -> Document {
    let d = json!({"openapi":version,"servers":[{"url":"https://api.example"}],"paths":{"/send":{"post":{"operationId":"send","requestBody":{"content":{media:{"schema":{"type":"object","properties":{"p":schema}},"encoding":{"p":encoding}}}}}}}});
    Document::parse(d.to_string(), None, limits).unwrap()
}
fn query(version: &str, config: Json, value: &str) -> String {
    parameter_doc(version, "query", "p", config, Limits::default(), None)
        .operation_id("send")
        .unwrap()
        .request()
        .parameter(ParameterLocation::Query, "p", exact(value))
        .prepare()
        .unwrap()
        .target()
        .split_once('?')
        .unwrap()
        .1
        .into()
}
#[test]
fn shared_query_atoms_separate_names_data_delimiters_and_percent_domains() {
    for version in VERSIONS {
        for (config, value, expected) in [
            (
                json!({"style":"form","explode":false}),
                r#"["a,b","c d"]"#,
                "p=a%2Cb,c%20d",
            ),
            (
                json!({"style":"form","explode":false}),
                r#"{"x":1,"y":"a b"}"#,
                "p=x,1,y,a%20b",
            ),
            (
                json!({"style":"form","explode":true}),
                r#"{"x":1,"y":"a b"}"#,
                "x=1&y=a%20b",
            ),
            (
                json!({"style":"deepObject","explode":true}),
                r#"{"order":"o 7","source":"web"}"#,
                "p%5Border%5D=o%207&p%5Bsource%5D=web",
            ),
            (
                json!({"style":"deepObject","explode":true,"allowReserved":true}),
                r#"{"café +":"é"}"#,
                "p%5Bcaf%C3%A9%20%2B%5D=%C3%A9",
            ),
            (
                json!({"allowReserved":true}),
                r#""x=1&admin=true+#[]""#,
                "p=x%3D1%26admin%3Dtrue%2B%23%5B%5D",
            ),
            (
                json!({"allowReserved":true}),
                r#""%2f%2F%26%GG%""#,
                "p=%2f%2F%26%25GG%25",
            ),
            (json!({}), r#""%2F""#, "p=%252F"),
            (json!({"allowReserved":true}), r#""a\r\nb""#, "p=a%0D%0Ab"),
            (json!({"allowReserved":true}), r#""a%0d%0Ab""#, "p=a%0d%0Ab"),
            (
                json!({"explode":true,"allowReserved":true}),
                r#"{"x&token":"attacker"}"#,
                "x%26token=attacker",
            ),
            (
                json!({"explode":true,"allowReserved":true}),
                r#"{"%74oken":"attacker"}"#,
                "%2574oken=attacker",
            ),
            (
                json!({"explode":false,"allowReserved":true}),
                r#"{"a,b":1,"k":"x=y&z"}"#,
                "p=a%2Cb,1,k,x%3Dy%26z",
            ),
            (
                json!({"explode":false,"allowReserved":true}),
                r#"["a,b","c/d"]"#,
                "p=a%2Cb,c/d",
            ),
        ] {
            assert_eq!(
                query(version, config, value),
                expected,
                "{version}: {value}"
            );
        }
    }
}
#[test]
fn deep_object_refusals_retain_source_and_caller_context() {
    for version in VERSIONS {
        for (value, reason) in [
            (r#"["x"]"#, DiagnosticReason::UnsupportedStyleCombination),
            (r#"{"x":{"y":1}}"#, DiagnosticReason::UnsupportedStyleValue),
            (r#"{"x[y]":1}"#, DiagnosticReason::AmbiguousStyleDelimiter),
        ] {
            let doc = parameter_doc(
                version,
                "query",
                "p",
                json!({"style":"deepObject","explode":true}),
                Limits::default(),
                None,
            );
            let error = doc
                .operation_id("send")
                .unwrap()
                .request()
                .parameter(ParameterLocation::Query, "p", exact(value))
                .prepare()
                .unwrap_err();
            assert_eq!(error.diagnostic().reason(), Some(&reason));
            assert!(error.diagnostic().location().is_some());
            assert!(
                matches!(error.diagnostic().context(),Some(DiagnosticContext::Parameter { name,.. }) if name=="p")
            );
        }
        let doc = parameter_doc(
            version,
            "query",
            "p",
            json!({"style":"deepObject","explode":false}),
            Limits::default(),
            None,
        );
        let result = doc
            .operation_id("send")
            .unwrap()
            .request()
            .parameter(ParameterLocation::Query, "p", exact(r#"{"x":1}"#))
            .prepare();
        if version.starts_with("3.2") {
            assert!(result.unwrap().target().ends_with("p%5Bx%5D=1"));
        } else {
            assert_eq!(
                result.unwrap_err().diagnostic().reason(),
                Some(&DiagnosticReason::UnsupportedStyleCombination)
            );
        }
    }
}
#[test]
fn path_reserved_applicability_and_literal_header_atoms() {
    for version in VERSIONS {
        let d = parameter_doc(
            version,
            "path",
            "p",
            json!({"allowReserved":true}),
            Limits::default(),
            None,
        );
        let r = d
            .operation_id("send")
            .unwrap()
            .request()
            .parameter(ParameterLocation::Path, "p", "a/b?c#d:~")
            .prepare()
            .unwrap();
        assert_eq!(
            r.target(),
            if version.starts_with("3.2") {
                "https://api.example/a%2Fb%3Fc%23d:~"
            } else {
                "https://api.example/a%2Fb%3Fc%23d%3A~"
            }
        );
        for (value, explode, expected) in [
            (r#"["a b","c%20d"]"#, false, "a b,c%20d"),
            (r#"{"x":1,"y":"a b"}"#, true, "x=1,y=a b"),
        ] {
            let d = parameter_doc(
                version,
                "header",
                "p",
                json!({"explode":explode}),
                Limits::default(),
                None,
            );
            let r = d
                .operation_id("send")
                .unwrap()
                .request()
                .parameter(ParameterLocation::Header, "p", exact(value))
                .prepare()
                .unwrap();
            assert_eq!(r.headers()[0].value(), expected.as_bytes());
        }
        let d = parameter_doc(version, "header", "p", json!({}), Limits::default(), None);
        assert!(
            d.operation_id("send")
                .unwrap()
                .request()
                .parameter(ParameterLocation::Header, "p", "x\r\nAuthorization: stolen")
                .prepare()
                .is_err()
        );
    }
}
#[test]
fn form_style_and_content_profiles_keep_32_outer_array_expansion() {
    for version in VERSIONS {
        for (encoding, input, schema, expected) in [
            (json!({}), r#""~* ""#, json!({"type":"string"}), "p=%7E*+"),
            (
                json!({"style":"form"}),
                r#""~* ""#,
                json!({"type":"string"}),
                "p=~%2A%20",
            ),
            (
                json!({"allowReserved":true}),
                r#""/?:@!$'()*+,;""#,
                json!({"type":"string"}),
                "p=/?:@!$'()*%2B,;",
            ),
            (
                json!({"style":"deepObject","explode":true}),
                r#"{"x":1,"label":"a b"}"#,
                json!({"type":"object"}),
                "p%5Bx%5D=1&p%5Blabel%5D=a%20b",
            ),
            (
                json!({"style":"form","explode":false}),
                r#"["a,b","c d"]"#,
                json!({"type":"array","items":{"type":"string"}}),
                if version.starts_with("3.2") {
                    "p=a%2Cb&p=c%20d"
                } else {
                    "p=a%2Cb,c%20d"
                },
            ),
        ] {
            let d = form_doc(
                version,
                "application/x-www-form-urlencoded",
                schema,
                encoding,
                Limits::default(),
            );
            let r = d
                .operation_id("send")
                .unwrap()
                .request()
                .form([FormField::new("p", exact(input))])
                .prepare()
                .unwrap();
            assert_eq!(r.body().unwrap(), expected.as_bytes(), "{version}");
        }
    }
}
#[test]
fn multipart_style_is_literal_and_30_ignores_style_fields() {
    for version in VERSIONS {
        let d = form_doc(
            version,
            "multipart/form-data",
            json!({"type":"object"}),
            json!({"style":"deepObject","explode":true,"allowReserved":true}),
            Limits::default(),
        );
        let r = d
            .operation_id("send")
            .unwrap()
            .request()
            .form([FormField::new("p", exact(r#"{"x":"a%2F&=+é"}"#))])
            .prepare()
            .unwrap();
        let body = std::str::from_utf8(r.body().unwrap()).unwrap();
        if version.starts_with("3.0") {
            assert!(body.contains(
                "name=\"p\"\r\nContent-Type: application/json\r\n\r\n{\"x\":\"a%2F&=+é\"}"
            ));
        } else {
            assert!(body.contains("name=\"p[x]\"\r\nContent-Type: text/plain\r\n\r\na%2F&=+é\r\n"));
        }
    }
}
fn key() -> Credential {
    Credential {
        value: CredentialValue::ApiKey("dummy-token".into()),
        origins: vec!["https://api.example".into()],
    }
}
#[test]
fn security_compares_final_names_and_preserves_namespace_separation() {
    for version in VERSIONS {
        for (style, credential_name, value) in [
            ("form", "token", r#"{"token":"attacker"}"#),
            ("deepObject", "p[x]", r#"{"x":1}"#),
        ] {
            let d = parameter_doc(
                version,
                "query",
                "p",
                json!({"style":style,"explode":true}),
                Limits::default(),
                Some(credential_name),
            );
            let e = d
                .operation_id("send")
                .unwrap()
                .request()
                .parameter(ParameterLocation::Query, "p", exact(value))
                .credential("Key", key())
                .prepare()
                .unwrap_err();
            assert_eq!(e.diagnostic().code(), Code::CredentialCollision);
        }
        let d = parameter_doc(
            version,
            "query",
            "p",
            json!({"explode":true,"allowReserved":true}),
            Limits::default(),
            Some("token"),
        );
        let r = d
            .operation_id("send")
            .unwrap()
            .request()
            .parameter(
                ParameterLocation::Query,
                "p",
                exact(r#"{"x&token":"attacker","%74oken":"attacker"}"#),
            )
            .credential("Key", key())
            .prepare()
            .unwrap();
        assert!(
            r.target()
                .ends_with("x%26token=attacker&%2574oken=attacker&token=dummy-token")
        );
        let source = json!({"openapi":version,"servers":[{"url":"https://api.example"}],"security":[{"Key":[]}],"components":{"securitySchemes":{"Key":{"type":"apiKey","in":"query","name":"token"}}},"paths":{"/send":{"post":{"operationId":"send","requestBody":{"content":{"application/x-www-form-urlencoded":{"schema":{"type":"object","properties":{"token":{"type":"string"}}}}}}}}}});
        let d = Document::parse(source.to_string(), None, Limits::default()).unwrap();
        let r = d
            .operation_id("send")
            .unwrap()
            .request()
            .form([FormField::new("token", "body-value")])
            .credential("Key", key())
            .prepare()
            .unwrap();
        assert_eq!(r.body().unwrap(), b"token=body-value");
        assert!(r.target().ends_with("?token=dummy-token"));
    }
}
#[test]
fn transformed_growth_is_admitted_before_output_and_form_limits_do_not_cap_parameters() {
    let limits = Limits {
        target_bytes: 64,
        form_work: 0,
        form_fields: 0,
        ..Limits::default()
    };
    let d = parameter_doc(
        "3.2.1",
        "query",
        "p",
        json!({"style":"deepObject"}),
        limits.clone(),
        None,
    );
    let op = d.operation_id("send").unwrap();
    assert!(
        op.request()
            .parameter(ParameterLocation::Query, "p", exact(r#"{"x":"ok"}"#))
            .prepare()
            .is_ok()
    );
    let big = "é".repeat(5000);
    let v = ExactJson::from_serializable(&json!({"x":big}), Limits::default()).unwrap();
    let e = op
        .request()
        .parameter(ParameterLocation::Query, "p", v.root())
        .prepare()
        .unwrap_err();
    assert!(matches!(
        e.diagnostic().reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::TargetBytes,
            ..
        })
    ));
    let cancel = Cancellation::default();
    cancel.cancel();
    assert_eq!(
        op.request()
            .parameter(ParameterLocation::Query, "p", "x")
            .prepare_with_cancellation(&cancel)
            .unwrap_err()
            .diagnostic()
            .code(),
        Code::Cancelled
    );
    let d = form_doc(
        "3.2.1",
        "application/x-www-form-urlencoded",
        json!({"type":"object"}),
        json!({"style":"deepObject"}),
        Limits {
            form_metadata_bytes: 4,
            ..Limits::default()
        },
    );
    let e = d
        .operation_id("send")
        .unwrap()
        .request()
        .form([FormField::new("p", exact(r#"{"long":1}"#))])
        .prepare()
        .unwrap_err();
    assert!(matches!(
        e.diagnostic().reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::FormMetadataBytes,
            ..
        })
    ));
}

#[test]
fn chunk_boundaries_keep_unicode_and_percent_triplets_and_exact_numbers() {
    let input = format!("{}é%2f%GG", "x".repeat(3071));
    let d = parameter_doc(
        "3.2.1",
        "query",
        "p",
        json!({"allowReserved":true}),
        Limits::default(),
        None,
    );
    let r = d
        .operation_id("send")
        .unwrap()
        .request()
        .parameter(ParameterLocation::Query, "p", input.as_str())
        .prepare()
        .unwrap();
    assert_eq!(
        r.target().split_once('?').unwrap().1,
        format!("p={}%C3%A9%2f%25GG", "x".repeat(3071))
    );
    let input = format!("{}é", "x".repeat(4095));
    let d = parameter_doc("3.2.1", "header", "p", json!({}), Limits::default(), None);
    let r = d
        .operation_id("send")
        .unwrap()
        .request()
        .parameter(ParameterLocation::Header, "p", input.as_str())
        .prepare()
        .unwrap();
    assert_eq!(r.headers()[0].value(), input.as_bytes());
    assert_eq!(
        query(
            "3.2.1",
            json!({"style":"deepObject"}),
            r#"{"n":9007199254740993,"exponent":1.2300e+8}"#
        ),
        "p%5Bn%5D=9007199254740993&p%5Bexponent%5D=1.2300e%2B8"
    );
}

#[test]
fn yaml_declaration_refusal_retains_authored_location_and_ignored_boolean_is_typed() {
    let source = "openapi: 3.1.2\nservers:\n- url: https://api.example\npaths:\n  /send:\n    get:\n      operationId: send\n      parameters:\n      - name: p\n        in: query\n        style: deepObject\n        explode: false\n        schema: {}\n";
    let d = Document::parse(
        source,
        Some("https://api.example/spec.yaml"),
        Limits::default(),
    )
    .unwrap();
    let e = d
        .operation_id("send")
        .unwrap()
        .request()
        .parameter(ParameterLocation::Query, "p", exact(r#"{"x":1}"#))
        .prepare()
        .unwrap_err();
    assert_eq!(
        e.diagnostic().reason(),
        Some(&DiagnosticReason::UnsupportedStyleCombination)
    );
    let location = e.diagnostic().location().unwrap();
    assert!(source[location.start..location.end].contains("style: deepObject"));
    assert_eq!(
        e.diagnostic().source_context().unwrap().origin,
        SourceOrigin::Authored
    );
    let d = parameter_doc(
        "3.2.1",
        "query",
        "p",
        json!({"style":"deepObject","explode":"false"}),
        Limits::default(),
        None,
    );
    let e = d
        .operation_id("send")
        .unwrap()
        .request()
        .parameter(ParameterLocation::Query, "p", exact(r#"{"x":1}"#))
        .prepare()
        .unwrap_err();
    assert_eq!(e.diagnostic().code(), Code::InvalidDeclaration);
    let d = parameter_doc(
        "3.2.1",
        "query",
        "p[x]",
        json!({"style":"deepObject"}),
        Limits::default(),
        None,
    );
    let e = d
        .operation_id("send")
        .unwrap()
        .request()
        .parameter(ParameterLocation::Query, "p[x]", exact(r#"{"x":1}"#))
        .prepare()
        .unwrap_err();
    assert_eq!(
        e.diagnostic().reason(),
        Some(&DiagnosticReason::AmbiguousStyleDelimiter)
    );
}
