use dynamic_openapi_client::*;
use dynamic_openapi_client::{diagnostics::*, exact::*, form::*, schema::*, transport::*};

#[test]
fn authored_required_headers_still_refuse_for_scalar_and_joined_array_parts() {
    for (schema, payload) in [
        (serde_json::json!({"type":"string"}), "\"ok\""),
        (
            serde_json::json!({"type":"array","items":{"type":"string"}}),
            "[\"a\",\"b\"]",
        ),
    ] {
        for required in [false, true] {
            let source = serde_json::json!({
                "openapi":"3.1.2", "servers":[{"url":"https://api.example"}],
                "paths":{"/send":{"post":{"operationId":"send","requestBody":{
                    "content":{"multipart/form-data":{
                        "schema":{"type":"object","properties":{"x":schema}},
                        "encoding":{"x":{"explode":false,"headers":{
                            "SECRET-HEADER":{"required":required,"schema":{"type":"string"}}
                        }}}
                    }}
                }}}}
            });
            let document = Document::parse(source.to_string(), None, Limits::default()).unwrap();
            let value = Value::parse(payload, Limits::default()).unwrap();
            let result = document
                .operation_id("send")
                .unwrap()
                .request()
                .form_fields([FormField::part("x", Part::value(value.clone()))])
                .prepare();
            if required {
                let error = result.unwrap_err();
                assert_eq!(error.code(), Code::UnsupportedMedia);
                assert_eq!(
                    error.reason(),
                    Some(&DiagnosticReason::Form(FormReason::UnsupportedHeader))
                );
                assert!(
                    error
                        .location()
                        .unwrap()
                        .pointer
                        .ends_with("/headers/SECRET-HEADER")
                );
                assert!(!format!("{error:?} {error}").contains("SECRET-HEADER"));
            } else {
                assert!(result.is_ok());
            }
        }
    }
}

#[test]
fn schema_edges_outlive_handle_and_document_with_order_and_exact_length() {
    let document = Document::parse(
        br##"{"openapi":"3.1.2","paths":{},"components":{"schemas":{
          "Use":{"$ref":"#/components/schemas/Text","$dynamicRef":"#/components/schemas/Text"},
          "Text":{"type":"string"}}}}"##,
        None,
        Limits::default(),
    )
    .unwrap();
    let handle = document
        .entry_resource()
        .schema_at("/components/schemas/Use")
        .unwrap();
    assert!(handle.keywords_supported());
    let mut references = handle.references().unwrap();
    drop(handle);
    drop(document);
    assert_eq!(references.len(), 2);
    for (index, kind) in [SchemaReferenceKind::Ref, SchemaReferenceKind::DynamicRef]
        .into_iter()
        .enumerate()
    {
        let edge = references.next().unwrap();
        assert_eq!(references.len(), 1 - index);
        assert_eq!(edge.kind(), kind);
        assert_eq!(
            edge.resolve_static()
                .unwrap()
                .target()
                .authored()
                .get("type")
                .unwrap()
                .as_str(),
            Some("string")
        );
    }
    assert!(references.next().is_none());
}

#[test]
fn flattened_response_defaults_and_debug_preserve_independent_evidence() {
    for body_state in [BodyState::Complete, BodyState::Truncated, BodyState::Failed] {
        for provenance in [
            Provenance::Unknown,
            Provenance::Wire,
            Provenance::Decoded,
            Provenance::HostDelivered,
        ] {
            let input = ResponseInput::new(
                422,
                vec![Header::new("SECRET-HEADER-NAME", "SECRET-HEADER-VALUE")],
                b"SECRET-BODY".as_slice().into(),
                body_state,
                provenance,
            );
            assert_eq!(input.bytes.as_ref(), b"SECRET-BODY");
            assert_eq!(input.body_state, body_state);
            assert_eq!(input.provenance, provenance);
            assert_eq!(input.header_delivery, HeaderDelivery::Unspecified);
            assert_eq!(input.header_fidelity, HeaderFidelity::Unknown);
            assert!(!input.opaque_redirect);
            assert!(!format!("{input:?} {input:#?}").contains("SECRET"));
        }
    }
}
