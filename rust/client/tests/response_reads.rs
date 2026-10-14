use dynamic_openapi_client::{
    Cancellation, Document, Header, Limits, Outcome,
    diagnostics::{Code, DiagnosticContext, DiagnosticReason, LimitKind},
    exact::Value,
    transport::*,
};
use serde::Deserialize;
use std::{
    cell::{Cell, RefCell},
    future::Future,
    sync::{Arc, Barrier},
    task::{Context, Poll, Waker},
};

fn ready<F: Future>(future: F) -> F::Output {
    match Box::pin(future)
        .as_mut()
        .poll(&mut Context::from_waker(Waker::noop()))
    {
        Poll::Ready(value) => value,
        Poll::Pending => panic!("in-memory host must finish"),
    }
}
fn invoke(
    response: Option<ResponseInput>,
    upload: UploadState,
    cancel: bool,
    late: bool,
) -> Outcome {
    let document = Document::parse(
        br#"{"openapi":"3.1.2","servers":[{"url":"https://api.example"}],"paths":{"/":{"get":{"operationId":"get"}}}}"#,
        None,
        Limits::default(),
    ).unwrap();
    ready(
        document
            .operation_id("get")
            .unwrap()
            .request()
            .prepare()
            .unwrap()
            .invoke(
                Cancellation::default(),
                &HostCapabilities::programmable(),
                |_, token| async move {
                    if cancel {
                        token.cancel();
                    }
                    TransportResult {
                        response,
                        upload,
                        error_detail: late.then(|| "HOST-SECRET".into()),
                    }
                },
            ),
    )
}
fn input(status: u16, bytes: &[u8]) -> ResponseInput {
    ResponseInput::new(
        status,
        vec![Header::new("content-type", "application/json")],
        bytes.into(),
        BodyState::Complete,
        Provenance::Wire,
    )
}

#[test]
fn absence_empty_body_and_invalid_zero_are_distinct() {
    let absent = invoke(None, UploadState::Unknown, false, false);
    assert_eq!(absent.status(), None);
    assert_eq!(absent.headers(), None);
    assert_eq!(absent.raw(), None);
    assert_eq!(absent.body_state(), None);
    assert_eq!(absent.provenance(), None);
    assert_eq!(absent.header_delivery(), None);
    assert_eq!(absent.header_fidelity(), None);
    assert_eq!(absent.omitted_header_lines(), None);
    assert_eq!(
        absent.received_2xx_json::<()>().unwrap_err(),
        *absent.error().unwrap()
    );
    assert_eq!(
        absent.body_json().unwrap_err().code(),
        Code::InvalidHostEvidence
    );
    let empty = invoke(Some(input(204, b"")), UploadState::Unknown, false, false);
    assert_eq!(empty.status(), Some(204));
    assert_eq!(empty.raw(), Some(b"".as_slice()));
    assert_eq!(empty.body_state(), Some(BodyState::Complete));
    assert_eq!(
        empty.received_2xx_json::<()>().unwrap_err().code(),
        Code::EmptyBody
    );
    let mut zero = input(0, b"{}");
    zero.headers.clear();
    let zero = invoke(Some(zero), UploadState::Unknown, false, false);
    assert_eq!(zero.status(), Some(0));
    assert_eq!(zero.headers(), Some([].as_slice()));
    assert_eq!(zero.raw(), Some(b"{}".as_slice()));
    assert_eq!(
        zero.received_2xx_json::<()>().unwrap_err().code(),
        Code::InvalidHostEvidence
    );
}

#[test]
fn actual_diagnostic_precedes_cancel_status_and_decode_without_losing_facts() {
    for cancel in [false, true] {
        for bytes in [b"{".as_slice(), br#"{"error":"rejected"}"#] {
            let out = invoke(
                Some(input(422, bytes)),
                UploadState::Incomplete,
                cancel,
                true,
            );
            assert_eq!(
                out.received_2xx_json::<()>().unwrap_err(),
                *out.error().unwrap()
            );
            assert_eq!(
                out.received_2xx_json_exact().unwrap_err().code(),
                Code::TransportFailure
            );
            assert_eq!(out.cancelled(), cancel);
            assert_eq!(out.status(), Some(422));
            assert_eq!(out.upload(), UploadState::Incomplete);
            assert_eq!(out.raw(), Some(bytes));
            assert_eq!(out.host_error_detail(), Some("HOST-SECRET"));
            assert_eq!(out.body_json().is_ok(), bytes != b"{");
            assert!(
                !format!("{out:?} {}", out.received_2xx_json::<()>().unwrap_err())
                    .contains("HOST-SECRET")
            );
        }
    }
    let cancelled = invoke(Some(input(422, b"{")), UploadState::Unknown, true, false);
    assert_eq!(
        cancelled.received_2xx_json::<()>().unwrap_err().code(),
        Code::Cancelled
    );
    assert_eq!(cancelled.body_json().unwrap_err().code(), Code::InvalidJson);
    let status = invoke(Some(input(422, b"{")), UploadState::Unknown, false, false);
    let error = status.received_2xx_json::<()>().unwrap_err();
    assert_eq!(error.code(), Code::HttpStatus);
    assert!(error.to_string().contains("422"));
    assert!(error.source_bytes().is_empty());
    assert_eq!(status.body_json().unwrap_err().code(), Code::InvalidJson);
}

#[test]
fn unspecified_unknown_and_incomplete_upload_are_not_tightened() {
    for upload in [
        UploadState::Unknown,
        UploadState::Incomplete,
        UploadState::Complete,
    ] {
        let out = invoke(Some(input(200, b"9007199254740993")), upload, false, false);
        assert_eq!(out.header_delivery(), Some(HeaderDelivery::Unspecified));
        assert_eq!(out.header_fidelity(), Some(HeaderFidelity::Unknown));
        assert_eq!(
            out.received_2xx_json::<u64>().unwrap(),
            9_007_199_254_740_993
        );
        assert_eq!(
            out.received_2xx_json_exact().unwrap().raw(),
            "9007199254740993"
        );
        assert_eq!(out.upload(), upload);
    }
}

#[test]
fn cache_is_shared_across_concurrent_reads_and_clones_before_initialization() {
    let out = invoke(
        Some(input(200, br#"{"n":1.2300e+400}"#)),
        UploadState::Unknown,
        false,
        false,
    );
    let barrier = Arc::new(Barrier::new(8));
    let threads: Vec<_> = (0..8)
        .map(|i| {
            let out = out.clone();
            let barrier = barrier.clone();
            std::thread::spawn(move || {
                barrier.wait();
                if i % 2 == 0 {
                    out.body_json().unwrap()
                } else {
                    out.received_2xx_json_exact().unwrap()
                }
            })
        })
        .collect();
    let values: Vec<_> = threads.into_iter().map(|t| t.join().unwrap()).collect();
    let context = values[0].source_context();
    for value in &values {
        assert_eq!(value.source_context(), context);
        assert_eq!(value.get("n").unwrap().raw(), "1.2300e+400");
    }
    let child = values[0].get("n").unwrap();
    drop(values);
    drop(out);
    assert_eq!(child.raw(), "1.2300e+400");
    assert_eq!(child.source_context(), context);
}

thread_local! {
    static REENTER: RefCell<Option<Outcome>> = const { RefCell::new(None) };
    static PROJECTIONS: Cell<usize> = const { Cell::new(0) };
}
#[derive(Debug)]
struct Reentrant;
impl<'de> Deserialize<'de> for Reentrant {
    fn deserialize<D: serde::Deserializer<'de>>(d: D) -> Result<Self, D::Error> {
        PROJECTIONS.with(|n| n.set(n.get() + 1));
        REENTER.with_borrow(|out| {
            assert_eq!(out.as_ref().unwrap().received_2xx_json::<u64>().unwrap(), 7);
        });
        let _ = u64::deserialize(d)?;
        Ok(Self)
    }
}
#[test]
fn projections_rerun_outside_cache_lock_and_do_not_poison_exact_decode() {
    let out = invoke(Some(input(200, b"7")), UploadState::Unknown, false, false);
    assert_eq!(PROJECTIONS.get(), 0);
    REENTER.set(Some(out.clone()));
    out.received_2xx_json::<Reentrant>().unwrap();
    out.received_2xx_json::<Reentrant>().unwrap();
    assert_eq!(PROJECTIONS.get(), 2);
    let error = out.received_2xx_json::<Vec<u64>>().unwrap_err();
    assert_eq!(error.code(), Code::Deserialization);
    assert!(error.detail().is_some());
    assert!(error.line() > 0);
    assert!(error.column() > 0);
    assert_eq!(out.received_2xx_json::<u64>().unwrap(), 7);
    assert_eq!(out.body_json().unwrap().raw(), "7");
    REENTER.set(None);
}

#[test]
fn cached_decode_failure_keeps_source_identity_limits_and_redaction() {
    let out = invoke(
        Some(input(200, br#"{"SECRET":1,"SECRET":2}"#)),
        UploadState::Unknown,
        false,
        false,
    );
    let first = out.body_json().unwrap_err();
    let second = out.clone().received_2xx_json::<()>().unwrap_err();
    assert_eq!(first, second);
    assert_eq!(first.code(), Code::DuplicateMember);
    assert_eq!(first.source_context(), second.source_context());
    assert_eq!(first.related(), second.related());
    assert!(!first.related().is_empty());
    assert!(!format!("{out:?} {first} {first:?}").contains("SECRET"));
    drop(out);
    assert_eq!(first.source_bytes(), br#"{"SECRET":1,"SECRET":2}"#);
    let limit = Value::parse(
        "[1]",
        Limits {
            nodes: 1,
            ..Limits::default()
        },
    )
    .unwrap_err();
    let out = Outcome::not_dispatched(limit.clone(), true, None);
    assert_eq!(out.received_2xx_json::<()>().unwrap_err(), limit);
    assert!(matches!(
        limit.reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::Nodes,
            maximum: 1,
            ..
        })
    ));
}

#[test]
fn temporary_outcome_returns_original_preparation_correction_and_retained_bytes() {
    let document = Document::parse(
        br#"{"openapi":"3.1.2","servers":[{"url":"https://api.example"}],"paths":{"/":{"get":{"operationId":"get","parameters":[{"in":"query","name":"q","schema":{"type":"integer"}}]}}}}"#,
        None, Limits::default(),
    ).unwrap();
    let value = Value::parse("null", Limits::default()).unwrap();
    let expected = document
        .operation_id("get")
        .unwrap()
        .request()
        .parameter_exact(
            dynamic_openapi_client::ParameterLocation::Query,
            "q",
            &value,
        )
        .prepare()
        .unwrap_err();
    let error = Outcome::not_dispatched(expected.clone(), true, None)
        .received_2xx_json::<()>()
        .unwrap_err();
    drop(document);
    drop(value);
    assert_eq!(error, expected);
    assert_eq!(error.code(), Code::UnsupportedValue);
    assert!(
        matches!(error.context(), Some(DiagnosticContext::Parameter { name, .. }) if name == "q")
    );
    assert_eq!(error.source_position().unwrap().source(), "null");
    let invalid = Value::parse([255], Limits::default()).unwrap_err();
    let error = Outcome::not_dispatched(invalid.clone(), true, None)
        .received_2xx_json::<()>()
        .unwrap_err();
    assert_eq!(error, invalid);
    assert_eq!(error.source_bytes(), &[255]);
}
