//! Supplied finite host evidence; this example performs no network I/O.
use dynamic_openapi_client::diagnostics::Code;
use dynamic_openapi_client::transport::{
    BodyState, HeaderDelivery, HeaderFidelity, HostCapabilities, ResponseInput, TransportResult,
    UploadState,
};
use dynamic_openapi_client::{Cancellation, Document, Header, Limits};
use std::{
    error::Error,
    future::Future,
    task::{Context, Poll, Waker},
};

async fn run() -> Result<(), Box<dyn Error>> {
    let document = Document::parse(
        r#"{"openapi":"3.2.1","servers":[{"url":"https://example.test"}],"paths":{"/":{"get":{"operationId":"read"}}}}"#,
        None,
        Limits {
            headers: 1,
            body_bytes: 64,
            ..Limits::default()
        },
    )?;
    let prepared = document.operation_id("read")?.request().prepare()?;
    let outcome = prepared
        .invoke(
            Cancellation::default(),
            &HostCapabilities::programmable(),
            |dispatch, _| async move {
                let admission = dispatch.response_admission();
                // The fixture host already owns these borrowed fields. Inspect lengths
                // before allocating a core Header. Stop at the first inadmissible field.
                let fields = [
                    ("content-type", "application/json"),
                    ("x-extra", "available at host"),
                ];
                let mut headers = Vec::new();
                let mut bytes = 0usize;
                let mut delivery = HeaderDelivery::Complete;
                for (name, value) in fields {
                    let next = bytes
                        .checked_add(name.len())
                        .and_then(|n| n.checked_add(value.len()));
                    if headers.len() == admission.header_entries()
                        || next.is_none_or(|n| n > admission.header_bytes())
                    {
                        delivery = HeaderDelivery::Incomplete;
                        break;
                    }
                    bytes = next.unwrap_or(bytes); // Successful admission established Some.
                    headers.push(Header::new(name, value));
                }
                let body = b"{}";
                let kept = body.len().min(admission.body_bytes());
                let mut response = ResponseInput::new(
                    200,
                    headers,
                    body[..kept].into(),
                    if kept == body.len() {
                        BodyState::Complete
                    } else {
                        BodyState::Truncated
                    },
                    dynamic_openapi_client::transport::Provenance::Wire,
                );
                response.header_fidelity = HeaderFidelity::HostProcessed;
                response.header_delivery = delivery;
                TransportResult {
                    response: Some(response),
                    upload: UploadState::Complete,
                    error_detail: None,
                }
            },
        )
        .await;
    match outcome.received_2xx_json_exact() {
        Ok(accepted) => println!("{}", accepted.clone().raw()),
        Err(refused) => {
            if refused.code() == Code::IncompleteHeaders {
                if outcome.status().is_some() {
                    let response = &outcome;
                    // Adapter cutoff is independent of core omission. Both raw
                    // body and precise metadata facts remain available.
                    println!(
                        "delivery={:?}, core omissions={}, raw bytes={}",
                        response.header_delivery().unwrap(),
                        response.omitted_header_lines().unwrap(),
                        response.raw().unwrap().len()
                    );
                }
            } else {
                return Err(Box::new(refused));
            }
        }
    }
    Ok(())
}

fn main() -> Result<(), Box<dyn Error>> {
    let mut run = std::pin::pin!(run());
    match run.as_mut().poll(&mut Context::from_waker(Waker::noop())) {
        Poll::Ready(result) => result,
        Poll::Pending => Err("this finite fixture unexpectedly suspended".into()),
    }
}
