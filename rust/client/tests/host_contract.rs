//! Public H0 controls. Literal expectations are mapped to the frozen review IDs
//! in the author evidence; this is not independent host qualification.
use dynamic_openapi_client::*;
use std::{
    cell::Cell,
    future::Future,
    rc::Rc,
    sync::Arc,
    task::{Context, Poll, Waker},
};
const DOC: &str = r#"{"openapi":"3.2.1","servers":[{"url":"https://api.example"}],"paths":{"/send":{"post":{"operationId":"send","requestBody":{"content":{"application/json":{}}}}}}}"#;
fn ready<F: Future>(f: F) -> F::Output {
    let mut f = Box::pin(f);
    let mut cx = Context::from_waker(Waker::noop());
    match f.as_mut().poll(&mut cx) {
        Poll::Ready(v) => v,
        Poll::Pending => panic!("unexpected suspension"),
    }
}
fn prepared(limits: Limits) -> PreparedRequest {
    Document::parse(DOC, None, limits)
        .unwrap()
        .operation_id("send")
        .unwrap()
        .request()
        .prepare()
        .unwrap()
}
fn input() -> ResponseInput {
    ResponseInput::new(
        200,
        vec![Header::new("Content-Type", "application/json")],
        Arc::from(br#"{"id":9007199254740993}"#.as_slice()),
        BodyState::Complete,
        Provenance::Decoded,
    )
}
fn result(
    input: Option<ResponseInput>,
    upload: UploadState,
    detail: Option<&str>,
) -> TransportResult {
    TransportResult {
        response: input,
        upload,
        error_detail: detail.map(Arc::from),
    }
}
fn invoke(
    p: &PreparedRequest,
    host: &HostCapabilities,
    policy: ReplayPolicy,
    evidence: TransportResult,
) -> (Outcome, usize) {
    let calls = Rc::new(Cell::new(0));
    let seen = calls.clone();
    let out = ready(p.invoke_with_replay(
        Cancellation::default(),
        host,
        policy,
        move |_, _| async move {
            seen.set(seen.get() + 1);
            evidence
        },
    ));
    (out, calls.get())
}
fn host_managed() -> HostCapabilities {
    let mut h = HostCapabilities::programmable();
    h.no_retries = false;
    h
}
fn host_error(error: &Diagnostic, reason: HostRefusal) {
    assert_eq!(error.code(), Code::HostCapability);
    assert_eq!(error.reason(), Some(&DiagnosticReason::Host(reason)));
}
fn refused(out: &Outcome, reason: HostRefusal) {
    assert_eq!(out.dispatch, DispatchEvidence::NotDispatched);
    assert_eq!(out.upload, UploadState::NotStarted);
    assert_eq!(out.replay_assurance(), None);
    assert!(out.response.is_none());
    host_error(out.error.as_ref().unwrap(), reason);
}
#[test]
fn strict_delegates_and_explicit_policy_agree_without_mutating_host() {
    let p = prepared(Limits::default());
    let h = host_managed();
    assert_eq!(ReplayPolicy::default(), ReplayPolicy::RequireNoReplay);
    host_error(&h.admit(&p).unwrap_err(), HostRefusal::Replay);
    let calls = Cell::new(0);
    let strict = ready(p.invoke(Cancellation::default(), &h, |_, _| async {
        calls.set(calls.get() + 1);
        result(Some(input()), UploadState::Complete, None)
    }));
    refused(&strict, HostRefusal::Replay);
    assert_eq!(calls.get(), 0);
    for policy in [
        ReplayPolicy::RequireNoReplay,
        ReplayPolicy::AllowHostManaged,
        ReplayPolicy::RequireNoReplay,
    ] {
        let admission = h.admit_with_replay(&p, policy);
        let (out, calls) = invoke(
            &p,
            &h,
            policy,
            result(Some(input()), UploadState::Complete, None),
        );
        if policy == ReplayPolicy::RequireNoReplay {
            host_error(&admission.unwrap_err(), HostRefusal::Replay);
            refused(&out, HostRefusal::Replay);
            assert_eq!(calls, 0);
        } else {
            assert_eq!(admission.unwrap(), ReplayAssurance::HostManaged);
            assert_eq!(out.replay_assurance(), Some(ReplayAssurance::HostManaged));
            assert_eq!(out.dispatch, DispatchEvidence::Dispatched);
            assert_eq!(calls, 1);
            assert_eq!(out.response.unwrap().body_state(), BodyState::Complete);
        }
        assert!(!h.no_retries);
    }
}
#[test]
fn strict_host_keeps_declared_assurance_and_identical_dispatch_bytes_under_both_policies() {
    let d = Document::parse(DOC, None, Limits::default()).unwrap();
    let p = d
        .operation_id("send")
        .unwrap()
        .request()
        .header(Header::sensitive("X-Secret", "header-secret"))
        .json(
            ExactJson::parse(r#"{"body":"body-secret"}"#, Limits::default())
                .unwrap()
                .root(),
        )
        .prepare()
        .unwrap();
    for (no_retries, policy, expected) in [
        (
            true,
            ReplayPolicy::RequireNoReplay,
            ReplayAssurance::NoReplayDeclared,
        ),
        (
            true,
            ReplayPolicy::AllowHostManaged,
            ReplayAssurance::NoReplayDeclared,
        ),
        (
            false,
            ReplayPolicy::AllowHostManaged,
            ReplayAssurance::HostManaged,
        ),
    ] {
        let mut h = HostCapabilities::programmable();
        h.no_retries = no_retries;
        assert_eq!(h.admit_with_replay(&p, policy).unwrap(), expected);
        let calls = Cell::new(0);
        let out = ready(
            p.invoke_with_replay(Cancellation::default(), &h, policy, |r, _| {
                calls.set(calls.get() + 1);
                assert_eq!(r.method(), p.method());
                assert_eq!(r.target(), p.target());
                assert_eq!(r.headers(), p.headers());
                assert_eq!(r.body().map(AsRef::as_ref), p.body().map(AsRef::as_ref));
                assert_eq!(
                    r.response_admission().body_bytes(),
                    Limits::default().body_bytes
                );
                async { result(Some(input()), UploadState::Unknown, None) }
            }),
        );
        assert_eq!(calls.get(), 1);
        assert_eq!(out.replay_assurance(), Some(expected));
    }
    let h = HostCapabilities::programmable();
    let out = ready(p.invoke(Cancellation::default(), &h, |_, _| async {
        result(Some(input()), UploadState::Unknown, None)
    }));
    assert_eq!(
        out.replay_assurance(),
        Some(ReplayAssurance::NoReplayDeclared)
    );
}
#[test]
fn non_replay_restrictions_precede_replay_and_opt_in_never_relaxes_them() {
    let base = prepared(Limits::default());
    for reason in [
        HostRefusal::Redirects,
        HostRefusal::AmbientAuth,
        HostRefusal::ExactTargets,
        HostRefusal::EncodedDotSegment,
        HostRefusal::ForbiddenHeader,
    ] {
        let p = match reason {
            HostRefusal::EncodedDotSegment => {
                Document::parse(DOC.replace("/send", "/%2e/send"), None, Limits::default())
                    .unwrap()
                    .operation_id("send")
                    .unwrap()
                    .request()
                    .prepare()
                    .unwrap()
            }
            HostRefusal::ForbiddenHeader => Document::parse(DOC, None, Limits::default())
                .unwrap()
                .operation_id("send")
                .unwrap()
                .request()
                .header(Header::new("X-PRIVATE-NAME-SENTINEL-7319", "v"))
                .prepare()
                .unwrap(),
            _ => base.clone(),
        };
        let mut h = host_managed();
        match reason {
            HostRefusal::Redirects => h.no_redirects = false,
            HostRefusal::AmbientAuth => h.no_ambient_auth = false,
            HostRefusal::ExactTargets => h.exact_targets = false,
            HostRefusal::EncodedDotSegment => h.encoded_dot_segments = false,
            HostRefusal::ForbiddenHeader => h
                .forbidden_headers
                .push("x-private-name-sentinel-7319".into()),
            _ => unreachable!(),
        }
        host_error(&h.admit(&p).unwrap_err(), reason);
        for policy in [
            ReplayPolicy::RequireNoReplay,
            ReplayPolicy::AllowHostManaged,
        ] {
            host_error(&h.admit_with_replay(&p, policy).unwrap_err(), reason);
            let (out, calls) = invoke(
                &p,
                &h,
                policy,
                result(Some(input()), UploadState::Complete, None),
            );
            refused(&out, reason);
            assert_eq!(calls, 0);
        }
    }
}
#[test]
fn cancellation_precedes_admission_and_callback_cancellation_preserves_evidence() {
    let p = prepared(Limits::default());
    let mut h = host_managed();
    h.no_redirects = false;
    for policy in [
        ReplayPolicy::RequireNoReplay,
        ReplayPolicy::AllowHostManaged,
    ] {
        let cancellation = Cancellation::default();
        cancellation.cancel();
        let calls = Cell::new(0);
        let out = ready(
            p.invoke_with_replay(cancellation, &h, policy, |_, _| async {
                calls.set(1);
                result(None, UploadState::Unknown, None)
            }),
        );
        assert_eq!(out.error.as_ref().unwrap().code(), Code::Cancelled);
        assert_eq!(calls.get(), 0);
        assert_eq!(out.dispatch, DispatchEvidence::NotDispatched);
        assert_eq!(out.replay_assurance(), None);
        assert!(out.cancelled);
    }
    let mut evidence = input();
    evidence.body_state = BodyState::Failed;
    evidence.bytes = Arc::from(b"{\"id\":".as_slice());
    let calls = Cell::new(0);
    let out = ready(p.invoke_with_replay(
        Cancellation::default(),
        &host_managed(),
        ReplayPolicy::AllowHostManaged,
        |_, c| {
            calls.set(calls.get() + 1);
            c.cancel();
            async move {
                result(
                    Some(evidence),
                    UploadState::Incomplete,
                    Some("host-private-error"),
                )
            }
        },
    ));
    assert_eq!(calls.get(), 1);
    assert!(out.cancelled);
    assert_eq!(out.replay_assurance(), Some(ReplayAssurance::HostManaged));
    assert_eq!(out.upload, UploadState::Incomplete);
    assert_eq!(out.host_error_detail(), Some("host-private-error"));
    assert_eq!(out.response.as_ref().unwrap().raw(), b"{\"id\":");
    assert_eq!(out.error.as_ref().unwrap().code(), Code::TransportFailure);
    let refusal = policies::received_2xx_json(out).unwrap_err();
    assert_eq!(refusal.reason, CompleteJsonRefusal::Cancelled);
    assert_eq!(
        refusal.outcome.replay_assurance(),
        Some(ReplayAssurance::HostManaged)
    );
}
#[test]
fn callback_failures_421_and_mutated_public_fields_never_rewrite_assurance() {
    let p = prepared(Limits::default());
    for (response, detail, code) in [
        (
            None,
            Some("host-secret-failure-19cf"),
            Code::TransportFailure,
        ),
        (None, None, Code::InvalidHostEvidence),
    ] {
        let (mut out, calls) = invoke(
            &p,
            &host_managed(),
            ReplayPolicy::AllowHostManaged,
            result(response, UploadState::NotStarted, detail),
        );
        assert_eq!(calls, 1);
        assert_eq!(out.dispatch, DispatchEvidence::Dispatched);
        assert_eq!(out.upload, UploadState::NotStarted);
        assert_eq!(out.error.as_ref().unwrap().code(), code);
        assert_eq!(out.host_error_detail(), detail);
        out.dispatch = DispatchEvidence::NotDispatched;
        out.upload = UploadState::Complete;
        assert_eq!(out.replay_assurance(), Some(ReplayAssurance::HostManaged));
    }
    let mut response = input();
    response.status = 421;
    let (out, calls) = invoke(
        &p,
        &host_managed(),
        ReplayPolicy::AllowHostManaged,
        result(Some(response), UploadState::Unknown, None),
    );
    assert_eq!(calls, 1);
    let refusal = policies::received_2xx_json(out).unwrap_err();
    assert_eq!(refusal.reason, CompleteJsonRefusal::HttpStatus);
    assert_eq!(refusal.outcome.response.as_ref().unwrap().status(), 421);
    assert_eq!(
        refusal.outcome.replay_assurance(),
        Some(ReplayAssurance::HostManaged)
    );
}
#[test]
fn acceptance_keeps_fidelity_replay_and_upload_independent() {
    let p = prepared(Limits::default());
    for fidelity in [HeaderFidelity::Unknown, HeaderFidelity::HostProcessed] {
        for upload in [UploadState::Unknown, UploadState::Complete] {
            let mut response = input();
            response.header_fidelity = fidelity;
            let (out, _) = invoke(
                &p,
                &host_managed(),
                ReplayPolicy::AllowHostManaged,
                result(Some(response), upload, None),
            );
            let received = policies::received_2xx_json(out.clone()).unwrap();
            assert_eq!(
                received.outcome.replay_assurance(),
                Some(ReplayAssurance::HostManaged)
            );
            assert_eq!(received.outcome.upload, upload);
            assert_eq!(
                received
                    .outcome
                    .response
                    .as_ref()
                    .unwrap()
                    .header_fidelity(),
                fidelity
            );
            assert_eq!(
                received.json.root().get("id").unwrap().raw(),
                "9007199254740993"
            );
            let complete = policies::complete_2xx_json(out);
            let preserved = if upload == UploadState::Complete {
                complete.unwrap().outcome
            } else {
                let e = complete.unwrap_err();
                assert_eq!(e.reason, CompleteJsonRefusal::UploadUncertain);
                e.outcome
            };
            assert_eq!(
                preserved.replay_assurance(),
                Some(ReplayAssurance::HostManaged)
            );
            assert_eq!(preserved.upload, upload);
            assert_eq!(preserved.response.unwrap().header_fidelity(), fidelity);
        }
    }
}
#[test]
fn response_constructor_fidelity_and_core_omission_are_distinct() {
    assert_eq!(HeaderFidelity::default(), HeaderFidelity::Unknown);
    let response = input();
    assert!(!response.opaque_redirect);
    assert_eq!(response.header_fidelity, HeaderFidelity::Unknown);
    let p = prepared(Limits {
        headers: 2,
        ..Limits::default()
    });
    let mut response = input();
    response.header_fidelity = HeaderFidelity::ExactFields;
    response.headers = vec![
        Header::new("X-A", "one"),
        Header::new("x-a", "two"),
        Header::new("Content-Type", "application/json"),
    ];
    let (out, _) = invoke(
        &p,
        &host_managed(),
        ReplayPolicy::AllowHostManaged,
        result(Some(response), UploadState::Unknown, None),
    );
    // H1 prospective correction: truthful core cutoff is a decode refusal,
    // not fabricated invalid-host or transport evidence. Original H0 is archived.
    assert!(out.error.is_none());
    let response = out.response.unwrap();
    assert_eq!(response.header_fidelity(), HeaderFidelity::ExactFields);
    assert_eq!(response.omitted_header_lines(), 1);
    assert_eq!(
        response.headers(),
        [Header::new("X-A", "one"), Header::new("x-a", "two")]
    );
    assert_eq!(response.json().unwrap_err().code(), Code::IncompleteHeaders);
    let mut response = input();
    response.header_fidelity = HeaderFidelity::HostProcessed;
    let (out, _) = invoke(
        &p,
        &host_managed(),
        ReplayPolicy::AllowHostManaged,
        result(Some(response), UploadState::Unknown, None),
    );
    let response = out.response.unwrap();
    assert_eq!(response.omitted_header_lines(), 0);
    assert_eq!(response.header_fidelity(), HeaderFidelity::HostProcessed);
    assert_eq!(response.headers().len(), 1); // Core cannot infer host-filtered entries.
}
#[test]
fn processed_metadata_never_decides_body_decoding_or_repairs_invalid_content_type() {
    let p = prepared(Limits::default());
    for provenance in [Provenance::Decoded, Provenance::Wire, Provenance::Unknown] {
        let mut response = input();
        response.header_fidelity = HeaderFidelity::HostProcessed;
        response
            .headers
            .push(Header::new("content-encoding", "gzip"));
        response.provenance = provenance;
        let (out, _) = invoke(
            &p,
            &host_managed(),
            ReplayPolicy::AllowHostManaged,
            result(Some(response), UploadState::Complete, None),
        );
        let decoded = out.response.unwrap().json();
        if provenance == Provenance::Decoded {
            assert!(decoded.is_ok());
        } else {
            assert_eq!(decoded.unwrap_err().code(), Code::UnsupportedCoding);
        }
    }
    for headers in [
        vec![Header::new("content-type", "application/json, text/plain")],
        vec![
            Header::new("content-type", "application/json"),
            Header::new("content-type", "application/json"),
        ],
    ] {
        let mut response = input();
        response.headers = headers;
        response.header_fidelity = HeaderFidelity::HostProcessed;
        let (out, _) = invoke(
            &p,
            &host_managed(),
            ReplayPolicy::AllowHostManaged,
            result(Some(response), UploadState::Complete, None),
        );
        assert_eq!(
            out.response.unwrap().json().unwrap_err().code(),
            Code::InvalidMedia
        );
    }
}
#[test]
fn opaque_redirect_and_inspectable_status_are_separate_for_every_fidelity() {
    let p = prepared(Limits::default());
    for fidelity in [
        HeaderFidelity::Unknown,
        HeaderFidelity::HostProcessed,
        HeaderFidelity::ExactFields,
    ] {
        for opaque in [false, true] {
            let mut response = input();
            response.header_fidelity = fidelity;
            response.opaque_redirect = opaque;
            response.status = if opaque { 0 } else { 302 };
            response.headers = if opaque {
                vec![]
            } else {
                vec![Header::new("Location", "/next")]
            };
            let (out, calls) = invoke(
                &p,
                &host_managed(),
                ReplayPolicy::AllowHostManaged,
                result(Some(response), UploadState::Unknown, None),
            );
            assert_eq!(calls, 1);
            assert_eq!(out.replay_assurance(), Some(ReplayAssurance::HostManaged));
            if opaque {
                assert_eq!(out.error.as_ref().unwrap().code(), Code::OpaqueRedirect);
            } else {
                assert!(out.error.is_none());
            }
            assert_eq!(
                out.response.as_ref().unwrap().status(),
                if opaque { 0 } else { 302 }
            );
            assert_eq!(out.response.as_ref().unwrap().header_fidelity(), fidelity);
            if !opaque {
                assert_eq!(
                    out.response.as_ref().unwrap().headers()[0].value(),
                    b"/next"
                );
                assert_eq!(
                    policies::received_2xx_json(out).unwrap_err().reason,
                    CompleteJsonRefusal::HttpStatus
                );
            }
        }
    }
}
#[test]
fn all_header_names_and_values_are_private_in_default_and_alternate_debug() {
    let p = prepared(Limits::default());
    for (name, value) in [
        (
            "X-PRIVATE-NAME-SENTINEL-7319",
            "PRIVATE-VALUE-SENTINEL-7319",
        ),
        ("x-secret-name-sentinel-6f7a9", "secret-value-sentinel-18b2"),
    ] {
        let header = Header::new(name, value);
        assert!(!header.is_sensitive());
        assert_eq!(header.name(), name);
        assert_eq!(header.value(), value.as_bytes());
        let mut response = input();
        response.headers.push(header.clone());
        let transport = result(
            Some(response.clone()),
            UploadState::Unknown,
            Some("host-detail-secret"),
        );
        let mut texts = vec![
            format!("{header:?} {header:#?}"),
            format!("{response:?} {response:#?}"),
            format!("{transport:?} {transport:#?}"),
        ];
        let (out, _) = invoke(
            &p,
            &host_managed(),
            ReplayPolicy::AllowHostManaged,
            transport,
        );
        texts.push(format!("{out:?} {out:#?}"));
        texts.push(format!("{:?} {:#?}", out.response, out.response));
        for text in texts {
            for secret in [name, value, "host-detail-secret"] {
                assert!(!text.contains(secret), "{text}");
            }
        }
        assert_eq!(out.response.unwrap().headers()[1], header);
    }
    let mut host = host_managed();
    host.forbidden_headers = vec![
        "X-PRIVATE-NAME-SENTINEL-7319".into(),
        "x-secret-capability-sentinel-54d8".into(),
    ];
    let text = format!("{host:?} {host:#?}");
    assert!(text.contains("forbidden_header_count"));
    for name in &host.forbidden_headers {
        assert!(!text.contains(name));
    }
}
#[test]
fn adapter_typed_refusal_is_safe_and_cannot_claim_callback_assurance() {
    let error: Diagnostic = HostRefusal::ForbiddenHeader.into();
    host_error(&error, HostRefusal::ForbiddenHeader);
    assert!(error.location().is_none());
    assert!(error.source_context().is_none());
    let out = Outcome::not_dispatched(
        error,
        false,
        Some("secret-adapter-detail-sentinel-19a4".into()),
    );
    refused(&out, HostRefusal::ForbiddenHeader);
    assert_eq!(
        out.host_error_detail(),
        Some("secret-adapter-detail-sentinel-19a4")
    );
    assert!(!format!("{out:?} {out:#?}").contains("secret-adapter-detail-sentinel-19a4"));
}
#[test]
fn source_request_and_response_owners_survive_only_as_explicitly_retained() {
    let entity: Arc<[u8]> = Arc::from(br#"{"secret":"entity"}"#.as_slice());
    let weak = Arc::downgrade(&entity);
    let d = Document::parse(DOC, None, Limits::default()).unwrap();
    let op = d.operation_id("send").unwrap();
    let p = op
        .request()
        .body(Body::Raw(entity.clone()))
        .prepare()
        .unwrap();
    drop(entity);
    drop(op);
    drop(d);
    let response_bytes: Arc<[u8]> = Arc::from(br#"{"id":9007199254740993}"#.as_slice());
    let response_weak = Arc::downgrade(&response_bytes);
    let mut response = input();
    response.bytes = response_bytes;
    let out = ready(p.invoke_with_replay(
        Cancellation::default(),
        &host_managed(),
        ReplayPolicy::AllowHostManaged,
        |r, _| async move {
            assert_eq!(r.target(), "https://api.example/send");
            assert_eq!(r.body().unwrap().as_ref(), br#"{"secret":"entity"}"#);
            result(Some(response), UploadState::Complete, None)
        },
    ));
    drop(p);
    assert!(weak.upgrade().is_none());
    assert_eq!(
        out.response
            .as_ref()
            .unwrap()
            .json()
            .unwrap()
            .root()
            .get("id")
            .unwrap()
            .raw(),
        "9007199254740993"
    );
    assert!(response_weak.upgrade().is_some());
    drop(out);
    assert!(response_weak.upgrade().is_none());
}
#[test]
fn invalid_public_inputs_refuse_in_preparation_and_delivered_bounds_remain_independent() {
    let d = Document::parse(DOC, None, Limits::default()).unwrap();
    let error = d
        .operation_id("send")
        .unwrap()
        .request()
        .header(Header::new("bad name", "v"))
        .prepare()
        .unwrap_err();
    assert_eq!(error.diagnostic().code(), Code::InvalidHeader);
    let invalid = Document::parse(
        DOC.replace("https://api.example", "https://api.example/#fragment"),
        None,
        Limits::default(),
    )
    .unwrap();
    assert!(
        invalid
            .operation_id("send")
            .unwrap()
            .request()
            .prepare()
            .is_err()
    );
    for fidelity in [
        HeaderFidelity::Unknown,
        HeaderFidelity::HostProcessed,
        HeaderFidelity::ExactFields,
    ] {
        for kind in ["bytes", "invalid", "body"] {
            let p = prepared(Limits {
                document_bytes: 1024,
                body_bytes: 4,
                ..Limits::default()
            });
            let mut response = input();
            response.header_fidelity = fidelity;
            match kind {
                "bytes" => response.headers.push(Header::new("X", "x".repeat(1024))),
                "invalid" => response.headers.push(Header::new("bad name", "v")),
                _ => {}
            }
            let (out, _) = invoke(
                &p,
                &host_managed(),
                ReplayPolicy::AllowHostManaged,
                result(Some(response), UploadState::Unknown, None),
            );
            let response = out.response.unwrap();
            assert_eq!(response.header_fidelity(), fidelity);
            assert_eq!(response.raw(), b"{\"id");
            assert_eq!(response.body_state(), BodyState::Truncated);
            assert_eq!(
                response.omitted_header_lines(),
                usize::from(kind == "bytes")
            );
            assert_eq!(
                response.json().unwrap_err().code(),
                match kind {
                    "body" => Code::IncompleteBody,
                    "bytes" => Code::IncompleteHeaders,
                    _ => Code::InvalidHostEvidence,
                }
            );
        }
    }
}
