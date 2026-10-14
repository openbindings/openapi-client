#[path = "support/request_case.rs"]
mod request_case;
use dynamic_openapi_client::*;
use dynamic_openapi_client::{diagnostics::*, exact::*, transport::*};
use request_case::*;
use serde_json::{Value as Json, json};

fn operation(path: &str, parameters: &[(&str, Json)], content: Option<Json>) -> Operation {
    let mut operation = json!({
        "operationId": "call",
        "parameters": parameters.iter().map(|(name, _)| json!({
            "name": name, "in": "path", "required": true, "schema": {}
        })).collect::<Vec<_>>()
    });
    if let Some(content) = content {
        operation["requestBody"] = json!({"content": content});
    }
    Document::parse(
        json!({
            "openapi": "3.1.2",
            "servers": [{"url": "https://api.example/base/../v1"}],
            "paths": {path: {"post": operation}}
        })
        .to_string(),
        None,
        Limits::default(),
    )
    .unwrap()
    .operation_id("call")
    .unwrap()
}
fn parameters(values: &[(&str, Json)]) -> CaseInput {
    CaseInput {
        parameters: values
            .iter()
            .map(|(name, value)| CaseParameter {
                location: ParameterLocation::Path,
                name: (*name).into(),
                value: Value::from_serializable(value, Limits::default()).unwrap(),
            })
            .collect(),
        ..CaseInput::default()
    }
}
fn json_body() -> CaseBody {
    CaseBody::Json(Value::parse("{\"n\":9007199254740993}", Limits::default()).unwrap())
}

#[test]
fn complete_literal_dot_segments_refuse_including_composed_values() {
    for (path, values) in [
        ("/items/{p}", vec![("p", json!("."))]),
        ("/items/{p}/tail", vec![("p", json!(".."))]),
        ("/items/.{p}", vec![("p", json!(""))]),
        ("/items/{p}.", vec![("p", json!("."))]),
        ("/items/{p}{q}", vec![("p", json!(".")), ("q", json!("."))]),
        ("/items/{p}", vec![("p", json!(["."]))]),
        ("/items/{p}", vec![("p", json!([".."]))]),
        ("/items/./tail", vec![]),
        ("/items/../tail", vec![]),
    ] {
        let error = operation(path, &values, None)
            .prepare_case(&parameters(&values))
            .unwrap_err();
        assert_eq!(error.code(), Code::InvalidDestination, "{path}");
        assert!(matches!(
            error.context(),
            Some(DiagnosticContext::PreparedRequest)
        ));
        assert!(error.location().is_none());
        assert!(error.source_context().is_none());
    }
}

#[test]
fn empty_embedded_and_encoded_segments_keep_their_exact_targets() {
    for (path, value, suffix) in [
        ("/items/{p}", json!(""), "/items/"),
        ("/items/{p}/tail", json!(""), "/items//tail"),
        ("/items/x{p}", json!("."), "/items/x."),
        ("/items/{p}", json!("..."), "/items/..."),
        ("/items/{p}", json!("../x"), "/items/..%2Fx"),
        ("/items/{p}", json!("%2e"), "/items/%252e"),
        ("/items/{p}", json!([".", ".."]), "/items/.,.."),
    ] {
        let values = [("p", value)];
        let request = operation(path, &values, None)
            .prepare_case(&parameters(&values))
            .unwrap();
        assert_eq!(request.target(), format!("https://api.example/v1{suffix}"));
    }
    let request = operation("/items/%2e", &[], None)
        .prepare_case(&CaseInput::default())
        .unwrap();
    assert_eq!(request.target(), "https://api.example/v1/items/%2e");
    HostCapabilities::programmable().admit(&request).unwrap();
    let mut host = HostCapabilities::programmable();
    host.encoded_dot_segments = false;
    assert_eq!(
        host.admit(&request).unwrap_err().code(),
        Code::HostCapability
    );
}

#[test]
fn automatic_media_election_counts_body_compatible_choices() {
    for content in [
        json!({"application/json": {}, "text/plain": {}}),
        json!({"application/json": {}, "application/xml": {}, "bad/type/extra": {}}),
        json!({"application/json": {}, "application/*": {}}),
    ] {
        let request = operation("/", &[], Some(content))
            .prepare_case(&CaseInput {
                body: json_body(),
                ..CaseInput::default()
            })
            .unwrap();
        assert_eq!(
            request.body().map(AsRef::as_ref),
            Some(b"{\"n\":9007199254740993}".as_slice())
        );
        assert_eq!(
            request
                .headers()
                .iter()
                .find(|h| h.name() == "Content-Type")
                .unwrap()
                .value(),
            b"application/json"
        );
    }
    let request = operation(
        "/",
        &[],
        Some(json!({"Application/Problem+Json; profile=compact": {}, "text/plain": {}})),
    )
    .prepare_case(&CaseInput {
        body: json_body(),
        ..CaseInput::default()
    })
    .unwrap();
    assert_eq!(
        request
            .headers()
            .iter()
            .find(|h| h.name() == "Content-Type")
            .unwrap()
            .value(),
        b"Application/Problem+Json; profile=compact"
    );

    for (content, body, code) in [
        (
            json!({"application/json": {}, "application/problem+json": {}}),
            json_body(),
            Code::AmbiguousSelection,
        ),
        (
            json!({"text/plain": {}}),
            json_body(),
            Code::UnsupportedMedia,
        ),
        (
            json!({"text/plain": {}, "application/xml": {}}),
            json_body(),
            Code::UnsupportedMedia,
        ),
        (
            json!({"application/json": {}, "text/plain": {}}),
            CaseBody::Raw(b"raw".as_slice().into()),
            Code::AmbiguousSelection,
        ),
        (
            json!({"application/*": {}}),
            json_body(),
            Code::InvalidSelection,
        ),
        (
            json!({"bad/type/extra": {}}),
            json_body(),
            Code::InvalidMedia,
        ),
    ] {
        assert_eq!(
            operation("/", &[], Some(content))
                .prepare_case(&CaseInput {
                    body,
                    ..CaseInput::default()
                })
                .unwrap_err()
                .code(),
            code
        );
    }
}

#[test]
fn explicit_media_selection_keeps_range_and_body_compatibility_rules() {
    let request = operation("/", &[], Some(json!({"application/*": {}})))
        .prepare_case(&CaseInput {
            body: json_body(),
            selection: CaseSelection {
                media: Some("application/json".into()),
                ..CaseSelection::default()
            },
            ..CaseInput::default()
        })
        .unwrap();
    assert_eq!(request.body_kind(), "json");
    let error = operation(
        "/",
        &[],
        Some(json!({"application/json": {}, "text/plain": {}})),
    )
    .prepare_case(&CaseInput {
        body: json_body(),
        selection: CaseSelection {
            media: Some("text/plain".into()),
            ..CaseSelection::default()
        },
        ..CaseInput::default()
    })
    .unwrap_err();
    assert_eq!(error.code(), Code::UnsupportedMedia);
    let request = operation("/", &[], Some(json!({"text/plain": {}})))
        .prepare_case(&CaseInput {
            body: CaseBody::Raw(b"plain".as_slice().into()),
            ..CaseInput::default()
        })
        .unwrap();
    assert_eq!(request.body().map(AsRef::as_ref), Some(b"plain".as_slice()));
}

#[test]
fn security_alternatives_require_an_election_independent_of_credentials() {
    for security in [json!([{"a": []}, {"b": []}]), json!([{"a": []}, {}])] {
        let document = Document::parse(
            json!({
                "openapi": "3.1.2", "servers": [{"url": "https://api.example"}],
                "paths": {"/": {"get": {"operationId": "call", "security": security}}},
                "components": {"securitySchemes": {
                    "a": {"type": "apiKey", "in": "header", "name": "X-A"},
                    "b": {"type": "apiKey", "in": "header", "name": "X-B"}
                }}
            })
            .to_string(),
            None,
            Limits::default(),
        )
        .unwrap();
        let operation = document.operation_id("call").unwrap();
        let mut input = CaseInput::default();
        assert_eq!(
            operation.prepare_case(&input).unwrap_err().code(),
            Code::AmbiguousSelection
        );
        input.credentials.insert(
            "a".into(),
            Credential::api_key("synthetic", vec!["https://api.example".into()]),
        );
        assert_eq!(
            operation.prepare_case(&input).unwrap_err().code(),
            Code::AmbiguousSelection
        );
        input.selection.security = Some(0);
        let request = operation.prepare_case(&input).unwrap();
        assert_eq!(request.headers()[0].value(), b"synthetic");
    }
}
