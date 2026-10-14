//! A third-party transport needs only the core's ordinary public API.
use dynamic_openapi_client::diagnostics::{
    Code, DiagnosticContext, DiagnosticReason, SerializationReason,
};
use dynamic_openapi_client::transport::{
    BodyState, DispatchEvidence, HostCapabilities, Provenance, ResponseInput, TransportResult,
    UploadState,
};
use dynamic_openapi_client::{Cancellation, Document, Header, Limits, Outcome, advanced};
use serde::Serialize;
use std::{
    cell::Cell,
    future::Future,
    task::{Context, Poll, Waker},
};

fn document() -> Document {
    Document::parse(r#"{"openapi":"3.1.0","servers":[{"url":"https://example.test"}],"paths":{"/":{"post":{"operationId":"send","requestBody":{"content":{"application/json":{"schema":{}}}}}}}}"#, None, Limits::default()).unwrap()
}

fn ready(future: impl Future<Output = Outcome>) -> Outcome {
    let mut future = Box::pin(future);
    match future
        .as_mut()
        .poll(&mut Context::from_waker(Waker::noop()))
    {
        Poll::Ready(outcome) => outcome,
        Poll::Pending => panic!("the in-memory host must complete immediately"),
    }
}

struct Count<'a>(&'a Cell<usize>);
impl Serialize for Count<'_> {
    fn serialize<S: serde::Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        self.0.set(self.0.get() + 1);
        serializer.serialize_u64(7)
    }
}

#[test]
fn custom_transport_uses_public_preparation_and_local_callback() {
    let calls = &Cell::new(0);
    let serializations = Cell::new(0);
    let request = document()
        .operation_id("send")
        .unwrap()
        .request()
        .json(&Count(&serializations));
    let (prepared, cancellation) = advanced::prepare(request);
    assert_eq!(serializations.get(), 1);
    assert!(!cancellation.is_cancelled());
    let outcome = ready(prepared.unwrap().invoke(
        cancellation,
        &HostCapabilities::programmable(),
        |request, cancellation| async move {
            // Borrowing Cell makes this a local host future; the public route imposes no Send bound.
            calls.set(calls.get() + 1);
            assert!(!cancellation.is_cancelled());
            assert_eq!(request.body().unwrap().as_ref(), b"7");
            TransportResult {
                response: Some(ResponseInput::new(
                    200,
                    vec![Header::new("Content-Type", "application/json")],
                    b"7".as_slice().into(),
                    BodyState::Complete,
                    Provenance::Wire,
                )),
                upload: UploadState::Complete,
                error_detail: None,
            }
        },
    ));
    assert_eq!(calls.get(), 1);
    assert_eq!(serializations.get(), 1);
    assert_eq!(outcome.received_2xx_json::<u64>().unwrap(), 7);
}

#[test]
fn selected_token_survives_success_and_prevents_later_dispatch() {
    let selected = Cancellation::default();
    let request = document()
        .operation_id("send")
        .unwrap()
        .request()
        .cancellation(selected.clone());
    let (prepared, cancellation) = advanced::prepare(request);
    let prepared = prepared.unwrap();
    selected.cancel();
    assert!(cancellation.is_cancelled());
    let outcome = ready(prepared.invoke(
        cancellation,
        &HostCapabilities::programmable(),
        |_, _| async { panic!("cancelled preparation must not enter the custom host") },
    ));
    assert_eq!(outcome.dispatch(), DispatchEvidence::NotDispatched);
    assert_eq!(outcome.error().unwrap().code(), Code::Cancelled);
    assert!(outcome.cancelled());
}

#[test]
fn failed_preparation_keeps_diagnostic_and_selected_token_independent() {
    let selected = Cancellation::default();
    let request = document()
        .operation_id("send")
        .unwrap()
        .request()
        .cancellation(selected.clone())
        .json(&f64::NAN);
    selected.cancel();
    let (prepared, cancellation) = advanced::prepare(request);
    let error = prepared.unwrap_err();
    assert_eq!(error.code(), Code::Serialization);
    assert_eq!(
        error.reason(),
        Some(&DiagnosticReason::Serialization(
            SerializationReason::NonFiniteNumber
        ))
    );
    assert_eq!(error.context(), Some(&DiagnosticContext::Construction));
    assert!(cancellation.is_cancelled());
    let outcome = Outcome::not_dispatched(error.clone(), cancellation.is_cancelled(), None);
    assert_eq!(outcome.dispatch(), DispatchEvidence::NotDispatched);
    assert_eq!(outcome.received_2xx_json_exact().unwrap_err(), error);
    assert!(outcome.cancelled());
}
