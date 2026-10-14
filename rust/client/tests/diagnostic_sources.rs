#[path = "support/request_case.rs"]
mod request_case;
use dynamic_openapi_client::*;
use dynamic_openapi_client::{diagnostics::*, exact::*, source::*};
use request_case::*;

#[test]
fn unlocated_and_parser_refusals_never_fabricate_primary_text() {
    assert!(
        Diagnostic::new(Code::InvalidJson)
            .source_position()
            .is_none()
    );
    let host: Diagnostic = HostRefusal::Replay.into();
    assert!(host.source_position().is_none());
    let invalid = Value::parse([0xff, 0xfe], Limits::default()).unwrap_err();
    assert_eq!(invalid.source_bytes(), &[0xff, 0xfe]);
    assert!(invalid.source_position().is_none());
    let over_limit = Value::parse(
        "[1]",
        Limits {
            document_bytes: 1,
            ..Limits::default()
        },
    )
    .unwrap_err();
    assert!(over_limit.source_bytes().is_empty());
    assert!(over_limit.source_position().is_none());
}

#[test]
fn yaml_wrong_kind_related_ranges_share_the_retained_authored_source() {
    let error = {
        let input = "openapi: 3.1.0\ninfo: {title: retained-related-marker, version: '1'}\npaths: {}\ncomponents:\n  schemas:\n    Broken:\n      $ref: '#/info'\n";
        let doc = Document::parse(input, None, Limits::default()).unwrap();
        doc.entry_resource()
            .schema_at("/components/schemas/Broken")
            .unwrap()
            .references()
            .unwrap()
            .next()
            .unwrap()
            .resolve_static()
            .unwrap_err()
    };
    assert_eq!(error.code(), Code::WrongReferenceKind);
    let primary = error.source_position().unwrap();
    assert_eq!(primary.raw(), "'#/info'");
    assert_eq!(
        (primary.location().start, primary.location().end),
        (125, 133)
    );
    let related = &error.related()[0];
    assert_eq!((related.start, related.end), (21, 67));
    assert_eq!(
        &primary.source()[related.start..related.end],
        "{title: retained-related-marker, version: '1'}"
    );
    assert!(!format!("{error} {error:?} {error:#?}").contains("retained-related-marker"));
}

const DOC: &str = r#"{"openapi":"3.2.0","servers":[{"url":"https://api.example"}],"paths":{"/send":{"get":{"operationId":"send","parameters":[{"name":"p","in":"query","style":"deepObject","explode":true,"schema":{}}]}}}}"#;

#[test]
fn primary_positions_survive_caller_scope_and_explicit_error_drop() {
    let ordinary_error = {
        let doc = Document::parse(DOC, None, Limits::default()).unwrap();
        let secret = String::from("primary-source-marker");
        let nested = [("value", TestValue::String(&secret))];
        let fields = [("nested", TestValue::Object(&nested))];
        doc.operation_id("send")
            .unwrap()
            .request()
            .parameter(ParameterLocation::Query, "p", &TestValue::Object(&fields))
            .prepare()
            .unwrap_err()
    };
    let diagnostic = &ordinary_error;
    assert!(ordinary_error.context().is_some());
    assert_eq!(
        diagnostic.reason(),
        Some(&DiagnosticReason::UnsupportedStyleValue)
    );
    assert_eq!(
        diagnostic.source_context().unwrap().origin,
        SourceOrigin::Constructed
    );
    assert_eq!(diagnostic.location().unwrap().pointer, "/nested");
    assert!(diagnostic.resource_need().is_none());
    assert!(!format!("{ordinary_error:?}").contains("primary-source-marker"));
    let generated = diagnostic.source_position().unwrap();
    assert_eq!(generated.context(), diagnostic.source_context().unwrap());
    assert_eq!(generated.location(), diagnostic.location().unwrap());
    assert_eq!(generated.raw(), r#"{"value":"primary-source-marker"}"#);
    let cloned = ordinary_error.clone();
    drop(ordinary_error);
    assert_eq!(cloned.source_position().unwrap(), generated);
    drop(cloned);
    assert_eq!(
        generated.source(),
        r#"{"nested":{"value":"primary-source-marker"}}"#
    );
    assert!(!format!("{generated:?} {generated:#?}").contains("primary-source-marker"));

    let (exact_error, trace) = {
        let doc = Document::parse(DOC, None, Limits::default()).unwrap();
        let exact = Value::parse(
            r#"{"nested":{"value":"exact-source-marker"}}"#,
            Limits::default(),
        )
        .unwrap();
        let trace = exact.clone().get("nested").unwrap().source_trace();
        let err = doc
            .operation_id("send")
            .unwrap()
            .request()
            .parameter_exact(ParameterLocation::Query, "p", &exact.clone())
            .prepare()
            .unwrap_err();
        (err, trace)
    };
    assert_eq!(
        exact_error.source_context(),
        Some(trace.representation.context())
    );
    assert_eq!(
        exact_error.location(),
        Some(trace.representation.location())
    );
    assert_eq!(
        trace.representation.raw(),
        r#"{"value":"exact-source-marker"}"#
    );
    assert!(!format!("{exact_error:?}").contains("exact-source-marker"));
    let exact_position = exact_error.source_position().unwrap();
    assert_eq!(exact_position, trace.representation);
    drop(exact_error);
    drop(trace);
    assert_eq!(exact_position.raw(), r#"{"value":"exact-source-marker"}"#);

    let (yaml_error, yaml_trace) = {
        let yaml = "openapi: 3.1.0\nservers: [{url: 'https://example.test'}]\nx-p: &p {name: id, in: path, required: true, schema: {type: string}}\npaths:\n  /items/{id}:\n    get:\n      operationId: get\n      parameters: [*p]\n      responses:\n        '200': {description: ok}\n";
        let doc = Document::parse(yaml, None, Limits::default()).unwrap();
        let trace = doc
            .root()
            .pointer("/paths/~1items~1{id}/get/parameters/0")
            .unwrap()
            .source_trace();
        let err = doc
            .operation_id("get")
            .unwrap()
            .request()
            .prepare()
            .unwrap_err();
        (err, trace)
    };
    let authored = yaml_trace.authored.unwrap();
    assert_eq!(yaml_error.source_context(), Some(authored.context()));
    assert_eq!(yaml_error.location(), Some(authored.location()));
    assert!(authored.raw().contains("name: id"));
    assert_eq!(yaml_trace.alias_uses[0].raw(), "*p");
    let position = yaml_error.source_position().unwrap();
    assert_eq!(position, authored);
    let aliases = yaml_error.related().to_vec();
    drop(yaml_error);
    assert_eq!(&position.source()[aliases[0].start..aliases[0].end], "*p");

    let parse_failure = {
        let source = String::from("{\"parse-source-marker\":");
        Value::parse(source, Limits::default()).unwrap_err()
    };
    assert_eq!(parse_failure.source_bytes(), b"{\"parse-source-marker\":");
    assert!(!format!("{parse_failure:?}").contains("parse-source-marker"));
    assert!(parse_failure.source_position().is_none());
}
