//! Dynamic public consumer using a supplied in-memory transport.
use dynamic_openapi_client::*;
#[path = "support/outcome_policy.rs"]
mod outcome_policy;
use std::{
    future::Future,
    task::{Context, Poll, Waker},
};
fn main() -> Result<(), Box<dyn std::error::Error>> {
    let text=std::env::args().nth(1).map(std::fs::read).transpose()?.unwrap_or_else(||br#"{"openapi":"3.1.2","servers":[{"url":"https://api.example"}],"paths":{"/items":{"get":{"operationId":"items","responses":{"200":{"description":"ok"}}}}}}"#.to_vec());
    let doc = Document::parse(text, None, Limits::default())?;
    let operation = doc.operations().next().ok_or("no operations")?;
    let description = operation.inspect();
    assert!(description.authored.get("operationId").is_some());
    drop(doc);
    let request = operation.prepare(&Input::default())?;
    let host = HostCapabilities::programmable();
    let mut future =
        Box::pin(
            request.invoke(Cancellation::default(), &host, |request, _| async move {
                assert!(request.target.starts_with("https://"));
                TransportResult {
                    response: Some(ResponseInput {
                        status: 200,
                        headers: vec![Header::new("Content-Type", "application/json")],
                        body: DeliveredBody {
                            bytes: br#"{"n":9007199254740993}"#.as_slice().into(),
                            state: BodyState::Complete,
                            provenance: Provenance::Wire,
                        },
                        opaque_redirect: false,
                    }),
                    upload: UploadState::Complete,
                    error_detail: None,
                }
            }),
        );
    let mut cx = Context::from_waker(Waker::noop());
    let Poll::Ready(outcome) = future.as_mut().poll(&mut cx) else {
        panic!("fixture must be ready")
    };
    let completed = outcome_policy::accept(outcome).map_err(|failure| {
        eprintln!(
            "{}; response_available={}, decoded_available={}",
            failure,
            failure.outcome.response.is_some(),
            failure.decoded.is_some()
        );
        failure // The caller receives all evidence, not only the displayed text.
    })?;
    assert_eq!(completed.outcome.upload, UploadState::Complete);
    assert_eq!(
        completed.json.root().get("n").unwrap().raw(),
        "9007199254740993"
    );
    println!(
        "public consumer passed: exact value, retained operation, prepared request and finite response"
    );
    Ok(())
}
