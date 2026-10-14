use dynamic_openapi_client::{diagnostics::*, exact::*, transport::*, *};

#[test]
fn checked_serde_string_limit_reports_encoded_size() {
    let e = Value::from_serializable(
        &"x".repeat(1_000_000),
        Limits {
            document_bytes: 8,
            ..Limits::default()
        },
    )
    .unwrap_err();
    println!("checked Serde reason: {:?}", e.reason());
    assert_eq!(
        e.reason(),
        Some(&DiagnosticReason::Limit {
            kind: LimitKind::DocumentBytes,
            maximum: 8,
            actual: 1_000_002
        })
    );
}
#[test]
fn malformed_response_reports_json_error_without_a_cause() {
    use std::{
        error::Error,
        future::Future,
        task::{Context, Poll, Waker},
    };
    let doc=Document::parse(r#"{"openapi":"3.1.2","servers":[{"url":"https://api.example"}],"paths":{"/":{"get":{"operationId":"get"}}}}"#,None,Limits::default()).unwrap();
    let req = doc
        .operation_id("get")
        .unwrap()
        .request()
        .prepare()
        .unwrap();
    let host = HostCapabilities::programmable();
    let fut = req.invoke(Cancellation::default(), &host, |_, _| async {
        TransportResult {
            response: Some(ResponseInput::new(
                200,
                vec![Header::new("Content-Type", "application/json")],
                b"{".as_slice().into(),
                BodyState::Complete,
                Provenance::Wire,
            )),
            upload: UploadState::Complete,
            error_detail: None,
        }
    });
    let Poll::Ready(out) = Box::pin(fut)
        .as_mut()
        .poll(&mut Context::from_waker(Waker::noop()))
    else {
        panic!("pending")
    };
    let e = out.body_json().unwrap_err();
    println!("malformed response cause: {:?}", e.source());
    assert_eq!(e.code(), Code::InvalidJson);
    assert!(e.source().is_none());
}
#[test]
fn parse_cause_preserves_full_diagnostic_equality() {
    use std::error::Error;
    let e = Value::parse("{", Limits::default()).unwrap_err();
    let cause = e.source().unwrap().downcast_ref::<Diagnostic>().unwrap();
    assert_eq!(cause, &e);
}
