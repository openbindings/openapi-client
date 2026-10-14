#[path = "support/request_case.rs"]
mod request_case;
use dynamic_openapi_client::diagnostics::Code;
use dynamic_openapi_client::inspect::Method;
use dynamic_openapi_client::source::{SourceFormat, SourceOrigin};
use dynamic_openapi_client::{Document, Limits};
use request_case::*;

#[test]
fn one_document_api_prepares_equivalent_json_and_yaml() {
    let json = br#"{"openapi":"3.1.0","servers":[{"url":"https://example.test"}],"paths":{"/items":{"get":{"operationId":"list","responses":{"200":{"description":"ok"}}}}}}"#;
    let yaml = b"openapi: 3.1.0\nservers: [{url: 'https://example.test'}]\npaths:\n  /items:\n    get:\n      operationId: list\n      responses:\n        '200': {description: ok}\n";
    for bytes in [json.as_slice(), yaml.as_slice()] {
        let document =
            Document::parse(bytes, Some("https://example.test/spec"), Limits::default()).unwrap();
        assert_eq!(document.source().as_bytes(), bytes);
        let operation = document.operation_id("list").unwrap();
        let prepared = operation.request().prepare().unwrap();
        assert_eq!(prepared.method(), Method::Get);
        assert_eq!(prepared.target(), "https://example.test/items");
    }
}

#[test]
fn diagnostic_and_retained_value_trace_use_original_yaml_definition() {
    let yaml = "openapi: 3.1.0\nservers: [{url: 'https://example.test'}]\nx-p: &p {name: id, in: path, required: true, schema: {type: string}}\npaths:\n  /items/{id}:\n    get:\n      operationId: get\n      parameters: [*p]\n      responses:\n        '200': {description: ok}\n";
    let document = Document::parse(yaml, None, Limits::default()).unwrap();
    assert_eq!(document.format(), SourceFormat::Yaml);
    let operation = document.operation_id("get").unwrap();
    let error = operation.request().prepare().unwrap_err();
    let diagnostic = &error;
    assert_eq!(diagnostic.code(), Code::MissingInput);
    let location = diagnostic.location().unwrap();
    assert!(yaml[location.start..location.end].contains("name: id"));
    assert_eq!(
        diagnostic.source_context().unwrap().origin,
        SourceOrigin::Authored
    );
    let value = document
        .root()
        .pointer("/paths/~1items~1{id}/get/parameters/0")
        .unwrap();
    let trace = value.source_trace();
    drop(document);
    drop(operation);
    drop(value);
    assert_eq!(trace.alias_uses[0].raw(), "*p");
    assert!(trace.authored.unwrap().raw().contains("name: id"));
}

#[test]
fn server_and_body_refusals_keep_yaml_definition_and_alias_locations() {
    let yaml = "openapi: 3.1.2\nx-server: &s {url: 'https://bad host'}\nservers: [*s]\npaths:\n  /items:\n    get:\n      operationId: list\n      responses: {'200': {description: ok}}\n";
    let document = Document::parse(yaml, None, Limits::default()).unwrap();
    let error = document
        .operation_id("list")
        .unwrap()
        .request()
        .prepare()
        .unwrap_err();
    let diagnostic = error;
    let location = diagnostic.location().unwrap();
    assert!(yaml[location.start..location.end].contains("https://bad host"));
    assert_eq!(
        diagnostic.source_context().unwrap().origin,
        SourceOrigin::Authored
    );
    assert!(
        diagnostic
            .related()
            .iter()
            .any(|p| &yaml[p.start..p.end] == "*s")
    );

    let yaml = "openapi: 3.1.2\nservers: [{url: 'https://example.test'}]\nx-data: &data {message: longer-than-body-limit}\nx-use: *data\npaths:\n  /items:\n    post:\n      operationId: create\n      requestBody:\n        content:\n          application/json: {schema: {type: object}}\n      responses: {'200': {description: ok}}\n";
    let limits = Limits {
        body_bytes: 8,
        ..Limits::default()
    };
    let document = Document::parse(yaml, None, limits).unwrap();
    // Explicit CaseInput reaches the body preparation diagnostic. The ordinary
    // builder applies its earlier body-size admission with caller CaseBody context.
    let diagnostic = document
        .operation_id("create")
        .unwrap()
        .prepare_case(&CaseInput {
            body: CaseBody::Json(document.root().get("x-use").unwrap()),
            ..Default::default()
        })
        .unwrap_err();
    assert_eq!(diagnostic.code(), Code::Limit);
    let location = diagnostic.location().unwrap();
    assert!(yaml[location.start..location.end].contains("message: longer-than-body-limit"));
    assert_eq!(
        diagnostic.source_context().unwrap().origin,
        SourceOrigin::Authored
    );
    assert!(
        diagnostic
            .related()
            .iter()
            .any(|p| &yaml[p.start..p.end] == "*data")
    );
}
