// Execute the exact interpretation functions used by both native tutorials.
// Their native main functions are compiled but never called here.
#[allow(dead_code)]
#[path = "../examples/o6_response.rs"]
mod current_job;
use std::{
    future::Future,
    sync::Arc,
    task::{Context, Poll, Waker},
};
fn ready<F: Future>(f: F) -> F::Output {
    match Box::pin(f)
        .as_mut()
        .poll(&mut Context::from_waker(Waker::noop()))
    {
        Poll::Ready(v) => v,
        Poll::Pending => panic!("in-memory callback blocked"),
    }
}
const DOCUMENT: &[u8] = br#"{"openapi":"3.1.2","servers":[{"url":"https://api.example"}],"paths":{"/":{"get":{"operationId":"get"}}}}"#;
const BODY: &[u8] = br#"{"error":"rejected"}"#;
#[test]
fn tutorial_handles_only_clean_typed_error_body() -> Result<(), Box<dyn std::error::Error>> {
    for (transport_failure, cancel) in [(false, false), (true, false), (false, true), (true, true)]
    {
        let doc = dynamic_openapi_client::Document::parse(
            DOCUMENT,
            None,
            dynamic_openapi_client::Limits::default(),
        )?;
        let request = doc.operation_id("get")?.request().prepare()?;
        let outcome = ready(request.invoke(
            dynamic_openapi_client::Cancellation::default(),
            &dynamic_openapi_client::HostCapabilities::programmable(),
            |_, token| async move {
                if cancel {
                    token.cancel();
                }
                dynamic_openapi_client::TransportResult {
                    response: Some(dynamic_openapi_client::ResponseInput::new(
                        422,
                        vec![dynamic_openapi_client::Header::new(
                            "Content-Type",
                            "application/json",
                        )],
                        Arc::from(BODY),
                        dynamic_openapi_client::BodyState::Complete,
                        dynamic_openapi_client::Provenance::Wire,
                    )),
                    upload: dynamic_openapi_client::UploadState::Unknown,
                    error_detail: transport_failure.then(|| Arc::from("late transport failure")),
                }
            },
        ));
        // Decoding remains possible even when interpreting the call must fail.
        assert_eq!(
            outcome
                .response
                .as_ref()
                .unwrap()
                .json()?
                .root()
                .get("error")
                .unwrap()
                .as_str(),
            Some("rejected")
        );
        let current = current_job::interpret(outcome);
        assert_eq!(current.is_err(), transport_failure || cancel);
        println!(
            "current: transport={transport_failure} cancellation={cancel}, result={current:?}"
        );
    }
    Ok(())
}
