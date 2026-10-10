use dynamic_openapi_client::{
    Code, Credential, CredentialValue, Diagnostic, DiagnosticReason, Document, Input, Limits,
    Method, SourceOrigin,
};
use serde_json::json;

fn assert_location(
    error: &Diagnostic,
    reason: DiagnosticReason,
    pointer: &str,
    source: &str,
    raw: &str,
) {
    assert_eq!(error.code(), Code::InvalidDeclaration);
    assert_eq!(error.reason(), Some(&reason));
    let location = error.location().unwrap();
    assert_eq!(location.pointer, pointer);
    assert_eq!(&source[location.start..location.end], raw);
    assert_eq!(
        error.source_context().unwrap().origin,
        SourceOrigin::Authored
    );
}

#[test]
fn undefined_scheme_locates_inherited_and_operation_requirement_in_every_family() {
    for version in ["2.0", "3.0.4", "3.1.2", "3.2.1"] {
        for inherited in [false, true] {
            let swagger = version == "2.0";
            let mut value = json!({
                "paths": {"/items": {"get": {
                    "operationId": "list", "responses": {"200": {"description": "ok"}}
                }}}
            });
            value[if swagger { "swagger" } else { "openapi" }] = json!(version);
            let requirement = json!([{"missing~/scheme": []}]);
            if inherited {
                value["security"] = requirement;
            } else {
                value["paths"]["/items"]["get"]["security"] = requirement;
            }
            let key = if swagger { "swagger" } else { "openapi" };
            let prefix = if inherited { "" } else { "      " };
            let yaml = format!(
                "{key}: '{version}'\npaths:\n  /items:\n    get:\n      operationId: list\n      responses: {{'200': {{description: ok}}}}\n{prefix}security: [{{'missing~/scheme': []}}]\n"
            );
            for source in [value.to_string(), yaml] {
                let doc = Document::parse(
                    &source,
                    Some("https://example.test/spec"),
                    Limits::default(),
                )
                .unwrap();
                let op = doc.operation_id("list").unwrap();
                let pointer = if inherited {
                    "/security/0/missing~0~1scheme"
                } else {
                    "/paths/~1items/get/security/0/missing~0~1scheme"
                };
                for error in [
                    op.describe().unwrap_err(),
                    op.prepare(&Input::default()).unwrap_err(),
                    op.request().prepare().unwrap_err().diagnostic().clone(),
                ] {
                    assert_location(
                        &error,
                        DiagnosticReason::UndefinedSecurityScheme,
                        pointer,
                        &source,
                        "[]",
                    );
                    assert!(!format!("{error:?} {error}").contains("missing~/scheme"));
                }
                assert_eq!(doc.source().as_bytes(), source.as_bytes());
            }
        }
    }
}

#[test]
fn missing_scheme_alias_retains_definition_and_use_without_leaking_name() {
    let source = "openapi: 3.1.2\nservers: [{url: 'https://example.test'}]\nx-security: &auth [{missing: []}]\nsecurity: *auth\npaths:\n  /items:\n    get:\n      operationId: list\n      responses: {'200': {description: ok}}\n";
    let doc = Document::parse(source, None, Limits::default()).unwrap();
    let error = doc
        .operation_id("list")
        .unwrap()
        .request()
        .prepare()
        .unwrap_err();
    let diagnostic = error.diagnostic();
    assert_eq!(
        diagnostic.reason(),
        Some(&DiagnosticReason::UndefinedSecurityScheme)
    );
    let location = diagnostic.location().unwrap();
    assert_eq!(&source[location.start..location.end], "[]");
    assert!(
        diagnostic
            .related()
            .iter()
            .any(|range| &source[range.start..range.end] == "*auth")
    );
}

#[test]
fn correcting_definition_preserves_credential_checks_and_scoped_success() {
    let source = r#"{"openapi":"3.1.2","servers":[{"url":"https://example.test"}],"components":{"securitySchemes":{"token":{"type":"http","scheme":"bearer"}}},"paths":{"/items":{"get":{"security":[{"token":[]}],"responses":{"200":{"description":"ok"}}}},"/healthy":{"get":{"responses":{"200":{"description":"ok"}}}}}}"#;
    let doc = Document::parse(source, None, Limits::default()).unwrap();
    let op = doc.operation("/items", Method::Get).unwrap();
    assert!(op.describe().is_ok());
    assert_eq!(
        op.request().prepare().unwrap_err().diagnostic().code(),
        Code::MissingCredential
    );
    let credential = Credential {
        value: CredentialValue::Bearer("test-token".into()),
        origins: vec!["https://example.test".into()],
    };
    let request = op
        .request()
        .credential("token", credential)
        .prepare()
        .unwrap();
    assert_eq!(request.target(), "https://example.test/items");
    assert!(
        doc.operation("/healthy", Method::Get)
            .unwrap()
            .request()
            .prepare()
            .is_ok()
    );
}

#[test]
fn operation_references_are_visible_but_never_silently_prepared() {
    for version in ["2.0", "3.0.4", "3.1.2", "3.2.1"] {
        let key = if version == "2.0" {
            "swagger"
        } else {
            "openapi"
        };
        let mut value = json!({
            "paths": {
                "/ref": {"get": {"$ref": "other.yaml#/operation"}},
                "/sibling": {"get": {"$ref": "#/x-operation", "operationId": "sibling", "responses": {"200":{"description":"ok"}}}},
                "/healthy": {"get": {"operationId":"healthy", "x-metadata":{"$ref":"inert"}, "responses":{"200":{"description":"ok"}}}},
                "/path-ref": {"$ref":"other.yaml#/path"}
            }
        });
        value[key] = json!(version);
        let yaml = format!(
            "{key}: '{version}'\npaths:\n  /ref:\n    get: {{$ref: 'other.yaml#/operation'}}\n  /sibling:\n    get: {{$ref: '#/x-operation', operationId: sibling, responses: {{'200': {{description: ok}}}}}}\n  /healthy:\n    get: {{operationId: healthy, x-metadata: {{$ref: inert}}, responses: {{'200': {{description: ok}}}}}}\n  /path-ref: {{$ref: 'other.yaml#/path'}}\n"
        );
        for source in [value.to_string(), yaml] {
            let doc = Document::parse(
                &source,
                Some("https://example.test/spec"),
                Limits::default(),
            )
            .unwrap();
            assert_eq!(doc.operations().count(), 3);
            for path in ["/ref", "/sibling"] {
                let op = doc.operation(path, Method::Get).unwrap();
                let authored = op.value().get("$ref").unwrap();
                let _ = op.inspect();
                for error in [
                    op.parameters().unwrap_err(),
                    op.describe().unwrap_err(),
                    op.request().prepare().unwrap_err().diagnostic().clone(),
                ] {
                    assert_eq!(
                        error.reason(),
                        Some(&DiagnosticReason::UnsupportedReferencePosition)
                    );
                    assert_eq!(error.code(), Code::InvalidDeclaration);
                    assert_eq!(
                        error.location().unwrap().pointer,
                        format!("/paths/~1{}/get/$ref", &path[1..])
                    );
                    let trace = authored.source_trace();
                    let location = error.location().unwrap();
                    assert_eq!(
                        &source[location.start..location.end],
                        trace.authored.unwrap().raw()
                    );
                }
            }
            assert!(
                doc.operation_id("healthy")
                    .unwrap()
                    .request()
                    .prepare()
                    .is_ok()
            );
            assert_eq!(
                doc.operation("/path-ref", Method::Get).unwrap_err().code(),
                Code::ExternalReference
            );
        }
    }
}

#[test]
fn additional_operation_reference_uses_the_same_guard() {
    let source = r#"{"openapi":"3.2.1","paths":{"/items":{"additionalOperations":{"COPY":{"$ref":"./copy.yaml"}}}}}"#;
    let doc =
        Document::parse(source, Some("https://example.test/spec"), Limits::default()).unwrap();
    let op = doc
        .operation("/items", Method::from_token("COPY").unwrap())
        .unwrap();
    let error = op.request().prepare().unwrap_err();
    assert_location(
        error.diagnostic(),
        DiagnosticReason::UnsupportedReferencePosition,
        "/paths/~1items/additionalOperations/COPY/$ref",
        source,
        "\"./copy.yaml\"",
    );
}
