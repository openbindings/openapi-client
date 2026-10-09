use dynamic_openapi_client::*;
use std::error::Error;

fn parse(text: &str) -> Document {
    Document::parse(text, None, Limits::default()).unwrap()
}

#[test]
fn parameter_views_share_resolution_override_and_retained_sources() {
    let text = r##"{"openapi":"3.1.2","paths":{"/items/{id}":{
      "parameters":[{"$ref":"#/components/parameters/Id","description":"use site"},
       {"name":"X-Key","in":"header","schema":{"type":"integer"}}],
      "get":{"operationId":"get","parameters":[
       {"name":"x-key","in":"header","schema":{"type":"string"}},
       {"name":"content","in":"query","content":{"application/json":{"schema":{}}}}],
       "requestBody":{"$ref":"https://outside.invalid/body"},"responses":17,"security":42}}},
      "components":{"parameters":{"Id":{"name":"id","in":"path","required":true,
      "schema":{"x-exact":9007199254740993}}}}}"##;
    let document = parse(text);
    let operation = document.operation_id("get").unwrap();
    let parameters = operation.parameters().unwrap();
    drop(document);
    drop(operation);
    let parameters: Vec<_> = parameters.iter().collect();
    assert_eq!(
        parameters.iter().map(Parameter::name).collect::<Vec<_>>(),
        ["id", "x-key", "content"]
    );
    assert!(parameters[0].required());
    assert!(!parameters[1].required());
    assert_eq!(
        parameters[0]
            .schema()
            .unwrap()
            .get("x-exact")
            .unwrap()
            .raw(),
        "9007199254740993"
    );
    let resolved = parameters[0].resolved();
    assert!(parameters[0].authored().get("$ref").is_some());
    assert!(resolved.target.get("$ref").is_none());
    assert_eq!(resolved.description.unwrap().as_str(), Some("use site"));
    assert_eq!(resolved.chain.len(), 2);
    assert_eq!(
        resolved.authored.source_context(),
        resolved.target.source_context()
    );
    assert!(parameters[2].schema().is_none());
    assert!(parameters[2].resolved().target.get("content").is_some());
}

#[test]
fn parameter_views_refuse_invalid_typed_facts_without_losing_raw_inspection() {
    for (version, location, required, expected) in [
        ("3.0.3", "query", "", Code::UnsupportedVersion),
        ("3.1.0", "path", "", Code::InvalidDeclaration),
        (
            "3.1.0",
            "path",
            ",\"required\":false",
            Code::InvalidDeclaration,
        ),
        (
            "3.1.0",
            "query",
            ",\"required\":null",
            Code::InvalidDeclaration,
        ),
    ] {
        let text = format!(
            r#"{{"openapi":"{version}","paths":{{"/{{id}}":{{"get":{{"operationId":"get","parameters":[{{"name":"id","in":"{location}"{required},"schema":{{}}}}]}}}}}}}}"#
        );
        let operation = parse(&text).operation_id("get").unwrap();
        let error = operation.parameters().unwrap_err();
        assert_eq!(error.code(), expected);
        assert!(error.location().is_some());
        assert!(operation.inspect().parameters.is_some());
    }
}

#[test]
fn immutable_parse_pair_still_composes_as_standard_error() {
    fn owned_error<E: Error + Send + Sync + 'static>(_: &E) {}
    let error = Document::parse(b"{", None, Limits::default()).unwrap_err();
    owned_error(&error);
    assert_eq!(error.source_bytes(), b"{");
    assert_eq!(error.diagnostic().code(), Code::InvalidJson);
    let source = error
        .source()
        .unwrap()
        .downcast_ref::<Diagnostic>()
        .unwrap();
    assert_eq!(source, error.diagnostic());
    let too_large = Document::parse(
        b"{}",
        None,
        Limits {
            document_bytes: 1,
            ..Limits::default()
        },
    )
    .unwrap_err();
    assert!(too_large.source_bytes().is_empty());
    assert_eq!(too_large.diagnostic().code(), Code::Limit);
}
