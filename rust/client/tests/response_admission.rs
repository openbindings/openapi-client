//! Public shared-response controls from the frozen H1 amendment and Sol review.
use dynamic_openapi_client::*;
use dynamic_openapi_client::{diagnostics::*, transport::*};
use std::{
    future::Future,
    sync::Arc,
    task::{Context, Poll, Waker},
};

const SOURCE: &str = r#"{"openapi":"3.2.1","servers":[{"url":"https://example.test"}],"paths":{"/":{"get":{"operationId":"read"}}}}"#;

fn ready<F: Future>(future: F) -> F::Output {
    let mut future = Box::pin(future);
    match future
        .as_mut()
        .poll(&mut Context::from_waker(Waker::noop()))
    {
        Poll::Ready(value) => value,
        Poll::Pending => panic!("supplied finite callback unexpectedly suspended"),
    }
}
fn prepare(headers: usize) -> PreparedRequest {
    Document::parse(
        SOURCE,
        None,
        Limits {
            document_bytes: 1024,
            body_bytes: 64,
            headers,
            ..Limits::default()
        },
    )
    .unwrap()
    .operation_id("read")
    .unwrap()
    .request()
    .prepare()
    .unwrap()
}
fn input(delivery: HeaderDelivery, fidelity: HeaderFidelity) -> ResponseInput {
    let mut input = ResponseInput::new(
        200,
        vec![Header::new("content-type", "application/json")],
        Arc::from(b"{}".as_slice()),
        BodyState::Complete,
        Provenance::Unknown,
    );
    input.header_delivery = delivery;
    input.header_fidelity = fidelity;
    input
}
fn invoke(
    request: &PreparedRequest,
    input: ResponseInput,
    upload: UploadState,
    detail: Option<&str>,
) -> Outcome {
    ready(request.invoke(
        Cancellation::default(),
        &HostCapabilities::programmable(),
        |dispatch, _| async move {
            assert_eq!(dispatch.response_admission(), request.response_admission());
            TransportResult {
                response: Some(input),
                upload,
                error_detail: detail.map(Arc::from),
            }
        },
    ))
}

#[test]
fn immutable_admission_is_copied_into_a_genuine_dispatch() {
    let request = prepare(2);
    let expected = request.response_admission();
    assert_eq!(
        (
            expected.body_bytes(),
            expected.header_entries(),
            expected.header_bytes()
        ),
        (64, 2, 1024)
    );
    let outcome = ready(request.invoke(
        Cancellation::default(),
        &HostCapabilities::programmable(),
        |dispatch, _| async move {
            let copy = dispatch.clone();
            assert_eq!(copy.response_admission(), expected);
            TransportResult {
                response: Some(input(
                    HeaderDelivery::Complete,
                    HeaderFidelity::HostProcessed,
                )),
                upload: UploadState::Complete,
                error_detail: None,
            }
        },
    ));
    assert_eq!(
        outcome.replay_assurance(),
        Some(ReplayAssurance::NoReplayDeclared)
    );
    assert!(policies::complete_2xx_json(outcome).is_ok());
}

#[test]
fn unspecified_constructor_preserves_acceptance_without_inventing_completeness() {
    assert_eq!(HeaderDelivery::default(), HeaderDelivery::Unspecified);
    let constructed = ResponseInput::new(
        200,
        vec![],
        Arc::from([]),
        BodyState::Complete,
        Provenance::Unknown,
    );
    assert_eq!(constructed.header_delivery, HeaderDelivery::Unspecified);
    for delivery in [HeaderDelivery::Unspecified, HeaderDelivery::Complete] {
        for fidelity in [
            HeaderFidelity::Unknown,
            HeaderFidelity::HostProcessed,
            HeaderFidelity::ExactFields,
        ] {
            let out = invoke(
                &prepare(2),
                input(delivery, fidelity),
                UploadState::Complete,
                None,
            );
            let response = out.response.as_ref().unwrap();
            assert_eq!(response.header_delivery(), delivery);
            assert_eq!(response.header_fidelity(), fidelity);
            assert!(policies::complete_2xx_json(out).is_ok());
        }
    }
}

#[test]
fn adapter_and_core_omissions_preserve_distinct_facts_and_policy_priority() {
    for (delivery, fidelity, extra, expected_omitted) in [
        (
            HeaderDelivery::Incomplete,
            HeaderFidelity::HostProcessed,
            false,
            0,
        ),
        (
            HeaderDelivery::Complete,
            HeaderFidelity::ExactFields,
            true,
            1,
        ),
        (HeaderDelivery::Incomplete, HeaderFidelity::Unknown, true, 1),
    ] {
        for upload in [UploadState::Complete, UploadState::Unknown] {
            let mut i = input(delivery, fidelity);
            if extra {
                i.headers.push(Header::new("x", "v"));
            }
            let out = invoke(&prepare(1), i, upload, None);
            assert!(out.error.is_none());
            assert_eq!(out.host_error_detail(), None);
            let response = out.response.as_ref().unwrap();
            assert_eq!(response.header_delivery(), delivery);
            assert_eq!(response.header_fidelity(), fidelity);
            assert_eq!(response.omitted_header_lines(), expected_omitted);
            assert_eq!(response.raw(), b"{}");
            assert_eq!(response.body_state(), BodyState::Complete);
            assert_eq!(response.json().unwrap_err().code(), Code::IncompleteHeaders);
            let refused = policies::complete_2xx_json(out.clone()).unwrap_err();
            assert_eq!(
                refused.reason,
                if upload == UploadState::Complete {
                    CompleteJsonRefusal::Decode
                } else {
                    CompleteJsonRefusal::UploadUncertain
                }
            );
            assert_eq!(
                refused.decode_error.unwrap().code(),
                Code::IncompleteHeaders
            );
            assert_eq!(
                policies::received_2xx_json(out).unwrap_err().reason,
                CompleteJsonRefusal::Decode
            );
        }
    }
}

#[test]
fn only_retained_fields_are_classified_and_opaque_wins_contradictions() {
    // The omitted malformed field is deliberately unclassified.
    let mut i = input(HeaderDelivery::Complete, HeaderFidelity::Unknown);
    i.headers.push(Header::new("bad name", "v"));
    let out = invoke(&prepare(1), i.clone(), UploadState::Complete, None);
    assert!(out.error.is_none());
    assert_eq!(
        out.response.unwrap().json().unwrap_err().code(),
        Code::IncompleteHeaders
    );
    i.header_delivery = HeaderDelivery::Incomplete;
    i.headers.swap(0, 1);
    let out = invoke(&prepare(1), i, UploadState::Complete, None);
    assert_eq!(out.error.unwrap().code(), Code::InvalidHostEvidence);
    assert_eq!(
        out.response.unwrap().json().unwrap_err().code(),
        Code::InvalidHostEvidence
    );
    for opaque in [false, true] {
        let mut i = input(HeaderDelivery::Incomplete, HeaderFidelity::ExactFields);
        i.opaque_redirect = opaque;
        if opaque {
            i.status = 0;
            i.headers.clear();
        }
        let out = invoke(&prepare(1), i, UploadState::Complete, None);
        let code = if opaque {
            Code::OpaqueRedirect
        } else {
            Code::InvalidHostEvidence
        };
        assert_eq!(out.error.as_ref().unwrap().code(), code);
        let response = out.response.as_ref().unwrap();
        assert_eq!(response.header_delivery(), HeaderDelivery::Incomplete);
        assert_eq!(response.header_fidelity(), HeaderFidelity::ExactFields);
        assert_eq!(response.json().unwrap_err().code(), code);
        assert_eq!(
            policies::complete_2xx_json(out).unwrap_err().reason,
            CompleteJsonRefusal::Transport
        );
    }
}

#[test]
fn metadata_refusal_precedes_body_while_execution_policy_keeps_its_order() {
    for (status, body, detail, cancelled, expected) in [
        (
            200,
            BodyState::Failed,
            None,
            false,
            CompleteJsonRefusal::Decode,
        ),
        (
            422,
            BodyState::Complete,
            None,
            false,
            CompleteJsonRefusal::HttpStatus,
        ),
        (
            200,
            BodyState::Failed,
            Some("controlled read failure"),
            false,
            CompleteJsonRefusal::Transport,
        ),
        (
            200,
            BodyState::Complete,
            None,
            true,
            CompleteJsonRefusal::Cancelled,
        ),
    ] {
        let mut i = input(HeaderDelivery::Incomplete, HeaderFidelity::HostProcessed);
        i.status = status;
        i.body_state = body;
        let mut out = invoke(&prepare(1), i, UploadState::Complete, detail);
        // Public policy input mutation is intentional; no host cancellation claim.
        out.cancelled = cancelled;
        let refusal = policies::complete_2xx_json(out).unwrap_err();
        assert_eq!(refusal.reason, expected);
        assert_eq!(
            refusal.decode_error.unwrap().code(),
            Code::IncompleteHeaders
        );
        assert_eq!(refusal.outcome.response.unwrap().body_state(), body);
    }
}

#[test]
fn host_refusals_remain_controlled_and_do_not_fabricate_locations() {
    for reason in [
        HostRefusal::Unavailable,
        HostRefusal::Method,
        HostRefusal::RequestBody,
    ] {
        let diagnostic: Diagnostic = reason.into();
        assert_eq!(diagnostic.code(), Code::HostCapability);
        assert_eq!(diagnostic.reason(), Some(&DiagnosticReason::Host(reason)));
        assert!(diagnostic.source_position().is_none());
        let out = Outcome::not_dispatched(diagnostic, false, None);
        assert_eq!(out.replay_assurance(), None);
        assert_eq!(out.upload, UploadState::NotStarted);
    }
}
