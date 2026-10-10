use dynamic_openapi_client::{Code, Document, Limits, Method, SourceFormat, SourceOrigin};

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
    let diagnostic = match &error {
        dynamic_openapi_client::RequestError::Preparation(diagnostic) => diagnostic,
        _ => panic!("expected preparation error"),
    };
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
