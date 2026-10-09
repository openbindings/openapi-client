use dynamic_openapi_client::*;
use policies::complete_2xx_json as accept;
use std::{
    future::Future,
    task::{Context, Poll, Waker},
};

const DOC: &str = r#"{"openapi":"3.1.2","servers":[{"url":"https://api.example"}],"paths":{"/":{"get":{"operationId":"op","requestBody":{"content":{"application/json":{},"text/plain":{}}},"responses":{"200":{"description":"ok"}}}}}}"#;
fn operation(limits: Limits) -> Operation {
    Document::parse(DOC, None, limits)
        .unwrap()
        .operation_id("op")
        .unwrap()
}
fn ready<F: Future>(f: F) -> F::Output {
    let mut f = Box::pin(f);
    let mut cx = Context::from_waker(Waker::noop());
    match f.as_mut().poll(&mut cx) {
        Poll::Ready(x) => x,
        Poll::Pending => panic!("finite fixture must be ready"),
    }
}
fn outcome(
    status: u16,
    upload: UploadState,
    late: bool,
    cancel: bool,
    pre: bool,
    body: &[u8],
) -> Outcome {
    let p = operation(Limits::default())
        .prepare(&Input::default())
        .unwrap();
    let token = Cancellation::default();
    if pre {
        token.cancel();
    }
    ready(p.invoke(
        token,
        &HostCapabilities::programmable(),
        |_, c| async move {
            if cancel {
                c.cancel();
            }
            TransportResult {
                upload,
                error_detail: late.then(|| "synthetic-host-secret".into()),
                response: Some(ResponseInput {
                    status,
                    headers: vec![Header::new("Content-Type", "application/json")],
                    body: DeliveredBody {
                        bytes: body.into(),
                        state: BodyState::Complete,
                        provenance: Provenance::Wire,
                    },
                    opaque_redirect: false,
                }),
            }
        },
    ))
}
const JSON: &[u8] = br#"{"count":9007199254740993}"#;

#[test]
fn r01_safe_numeric_and_ordinary_debug() {
    let owner = ExactJson::parse(
        r#"{"number":739182645,"string":"synthetic-ordinary-secret"}"#,
        Limits::default(),
    )
    .unwrap();
    let n = owner.root().get("number").unwrap();
    let s = owner.root().get("string").unwrap();
    let input = Input {
        parameters: vec![
            ParameterInput {
                location: ParameterLocation::Query,
                name: "token".into(),
                value: n.clone(),
            },
            ParameterInput {
                location: ParameterLocation::Query,
                name: "other".into(),
                value: s.clone(),
            },
        ],
        ..Default::default()
    };
    let formatted = format!(
        "{owner:?} {n:?} {s:?} {:?} {:?} {input:?} {:?}",
        n.number(),
        n.number().unwrap(),
        OrdinaryValue::U64(739182645)
    );
    assert!(!formatted.contains("739182645"));
    assert!(!formatted.contains("synthetic-ordinary-secret"));
    assert_eq!(n.number().unwrap().token(), "739182645");
    assert_eq!(s.as_str(), Some("synthetic-ordinary-secret"));
}
#[test]
fn r02_policy_success() {
    let r = accept(outcome(
        200,
        UploadState::Complete,
        false,
        false,
        false,
        JSON,
    ))
    .unwrap();
    assert_eq!(
        r.json.root().get("count").unwrap().raw(),
        "9007199254740993"
    );
    assert_eq!(r.outcome.dispatch, DispatchEvidence::Dispatched);
    assert_eq!(r.outcome.upload, UploadState::Complete);
}
#[test]
fn r03_policy_late_error_retains_decode_and_response() {
    for bytes in [JSON, b"{".as_slice()] {
        let e = accept(outcome(
            200,
            UploadState::Complete,
            true,
            false,
            false,
            bytes,
        ))
        .unwrap_err();
        assert!(matches!(e.reason, CompleteJsonRefusal::Transport));
        assert_eq!(
            e.outcome.error.as_ref().unwrap().code(),
            Code::TransportFailure
        );
        assert_eq!(e.outcome.response.as_ref().unwrap().raw(), bytes);
        if bytes == JSON {
            assert!(e.decoded.is_some());
            assert!(e.decode_error.is_none());
        } else {
            assert_eq!(e.decode_error.as_ref().unwrap().code(), Code::InvalidJson);
        }
        assert!(std::error::Error::source(e.as_ref()).is_some());
        assert!(!format!("{e:?} {e}").contains("synthetic-host-secret"));
    }
}
#[test]
fn r04_policy_cancelled_with_response() {
    let e = accept(outcome(
        200,
        UploadState::Complete,
        false,
        true,
        false,
        JSON,
    ))
    .unwrap_err();
    assert!(matches!(e.reason, CompleteJsonRefusal::Cancelled));
    assert!(e.outcome.cancelled);
    assert!(e.decoded.is_some());
    assert_eq!(e.outcome.response.as_ref().unwrap().status(), 200);
}
#[test]
fn r05_policy_pre_dispatch_cancellation() {
    let e = accept(outcome(
        200,
        UploadState::Complete,
        false,
        false,
        true,
        JSON,
    ))
    .unwrap_err();
    assert!(matches!(e.reason, CompleteJsonRefusal::Cancelled));
    assert_eq!(e.outcome.dispatch, DispatchEvidence::NotDispatched);
    assert!(e.outcome.response.is_none());
    assert!(e.decoded.is_none());
}
#[test]
fn r06_policy_http_error() {
    let e = accept(outcome(
        429,
        UploadState::Complete,
        false,
        false,
        false,
        JSON,
    ))
    .unwrap_err();
    assert!(matches!(e.reason, CompleteJsonRefusal::HttpStatus));
    assert_eq!(e.outcome.response.as_ref().unwrap().status(), 429);
    assert!(e.outcome.error.is_none());
    assert!(e.decoded.is_some());
}
#[test]
fn r07_policy_unknown_upload_policy() {
    let e = accept(outcome(
        200,
        UploadState::Unknown,
        false,
        false,
        false,
        JSON,
    ))
    .unwrap_err();
    assert!(matches!(e.reason, CompleteJsonRefusal::UploadUncertain));
    assert_eq!(e.outcome.upload, UploadState::Unknown);
    assert!(e.decoded.is_some());
}
#[test]
fn r08_numeric_reasons_and_exact_policy() {
    for (text, reason) in [
        ("1.5", NumericReason::UnsupportedLexicalForm),
        ("1e0", NumericReason::UnsupportedLexicalForm),
        ("9223372036854775808", NumericReason::SignedRange),
        ("9007199254740993", NumericReason::PrecisionLoss),
    ] {
        let json = ExactJson::parse(text, Limits::default()).unwrap();
        let value = json.root();
        let error = value.number().unwrap().to_f64_exact().unwrap_err();
        assert_eq!(
            error.reason(),
            Some(&DiagnosticReason::Numeric {
                target: NumericTarget::F64Exact,
                reason
            })
        );
        assert_eq!(error.source_context(), Some(value.source_context()));
        assert_eq!(error.location().cloned(), Some(value.location()));
    }
    let v = ExactJson::parse(
        "123",
        Limits {
            number_conversion_chars: 2,
            ..Default::default()
        },
    )
    .unwrap()
    .root();
    assert!(matches!(
        v.number().unwrap().to_u64().unwrap_err().reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::NumberCharacters,
            maximum: 2,
            actual: 3
        })
    ));
    let v = ExactJson::parse("-1", Limits::default()).unwrap().root();
    assert!(matches!(
        v.number().unwrap().to_u64().unwrap_err().reason(),
        Some(DiagnosticReason::Numeric {
            reason: NumericReason::UnsignedRange,
            ..
        })
    ));
    let v = ExactJson::parse("-0", Limits::default()).unwrap().root();
    assert!(
        v.number()
            .unwrap()
            .to_f64_exact()
            .unwrap()
            .is_sign_negative()
    );
}
#[test]
fn r09_corrective_input_selection_and_limits() {
    let error = operation(Limits {
        target_bytes: 5,
        ..Default::default()
    })
    .prepare(&Input::default())
    .unwrap_err();
    assert!(matches!(
        error.reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::TargetBytes,
            maximum: 5,
            ..
        })
    ));
    assert_eq!(error.context(), Some(&DiagnosticContext::PreparedRequest));
    assert!(error.location().is_none());
    let error = operation(Limits {
        headers: 0,
        ..Default::default()
    })
    .prepare(&Input {
        headers: vec![Header::new("X-Test", "v")],
        ..Default::default()
    })
    .unwrap_err();
    assert!(matches!(
        error.reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::HeaderCount,
            maximum: 0,
            actual: 1
        })
    ));
    assert!(error.location().is_none());
    let value = ExactJson::from_ordinary(OrdinaryValue::U64(4), Limits::default())
        .unwrap()
        .root();
    let input = Input {
        parameters: vec![ParameterInput {
            location: ParameterLocation::Query,
            name: "synthetic-secret-key".into(),
            value: value.clone(),
        }],
        ..Default::default()
    };
    let error = operation(Limits::default()).prepare(&input).unwrap_err();
    assert_eq!(error.reason(), Some(&DiagnosticReason::UnknownParameter));
    assert!(
        matches!(error.context(),Some(DiagnosticContext::Parameter{index:0,name,..})if name=="synthetic-secret-key")
    );
    assert!(error.location().is_none());
    assert!(!format!("{error} {error:?} {:?}", error.context()).contains("synthetic-secret-key"));
    for (kind, input) in [
        (
            SelectionKind::Server,
            Input {
                selection: Selection {
                    server: Some(9),
                    ..Default::default()
                },
                ..Default::default()
            },
        ),
        (
            SelectionKind::Media,
            Input {
                selection: Selection {
                    media: Some("application/unknown".into()),
                    ..Default::default()
                },
                body: Body::Json(value.clone()),
                ..Default::default()
            },
        ),
        (
            SelectionKind::Security,
            Input {
                selection: Selection {
                    security: Some(9),
                    ..Default::default()
                },
                ..Default::default()
            },
        ),
    ] {
        let error = operation(Limits::default()).prepare(&input).unwrap_err();
        assert_eq!(error.reason(), Some(&DiagnosticReason::Selection(kind)));
        assert!(matches!(error.context(),Some(DiagnosticContext::Selection{kind:k,..})if *k==kind));
    }
}
#[test]
fn r10_supported_ordinary_values_and_provenance() {
    let json = ExactJson::from_ordinary(
        OrdinaryValue::Object(&[
            ("quoted", OrdinaryValue::String("A/\"B\n雪")),
            ("boolean", OrdinaryValue::Bool(true)),
            ("signed", OrdinaryValue::I64(i64::MIN)),
            ("unsigned", OrdinaryValue::U64(u64::MAX)),
            ("large", OrdinaryValue::U64(9_007_199_254_740_993)),
            (
                "array",
                OrdinaryValue::Array(&[OrdinaryValue::Bool(false), OrdinaryValue::String("x")]),
            ),
            (
                "flat",
                OrdinaryValue::Object(&[("limit", OrdinaryValue::U64(3))]),
            ),
            ("null", OrdinaryValue::Null),
        ]),
        Limits::default(),
    )
    .unwrap();
    let root = json.root();
    assert_eq!(root.source_context().origin, SourceOrigin::Constructed);
    assert_eq!(root.get("quoted").unwrap().as_str(), Some("A/\"B\n雪"));
    assert_eq!(
        root.get("signed")
            .unwrap()
            .number()
            .unwrap()
            .to_i64()
            .unwrap(),
        i64::MIN
    );
    assert_eq!(
        root.get("unsigned")
            .unwrap()
            .number()
            .unwrap()
            .to_u64()
            .unwrap(),
        u64::MAX
    );
    assert_eq!(root.get("large").unwrap().raw(), "9007199254740993");
    assert!(root.get("missing").is_none());
    assert_eq!(root.get("null").unwrap().kind(), ValueKind::Null);
    assert!(matches!(Body::default(), Body::Absent));
    let retained = root.get("flat").unwrap();
    drop(json);
    drop(root);
    assert_eq!(retained.get("limit").unwrap().raw(), "3");
}
#[test]
fn r11_construction_limits_and_duplicate_names() {
    let large = "x".repeat(1_000_000);
    let failure = ExactJson::from_ordinary(
        OrdinaryValue::String(&large),
        Limits {
            document_bytes: 8,
            ..Default::default()
        },
    )
    .unwrap_err();
    assert!(failure.source_bytes().is_empty());
    assert!(matches!(
        failure.diagnostic().reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::DocumentBytes,
            maximum: 8,
            actual: 1_000_000
        })
    ));

    for maximum in [2, 3, 4] {
        let result = ExactJson::from_ordinary(
            OrdinaryValue::String("x"),
            Limits {
                document_bytes: maximum,
                ..Default::default()
            },
        );
        assert_eq!(result.is_ok(), maximum >= 3);
        if let Err(e) = result {
            assert!(e.source_bytes().is_empty());
            assert!(matches!(
                e.diagnostic().reason(),
                Some(DiagnosticReason::Limit {
                    kind: LimitKind::DocumentBytes,
                    ..
                })
            ));
            assert_eq!(
                e.diagnostic().context(),
                Some(&DiagnosticContext::Construction)
            );
        }
    }
    let array = [OrdinaryValue::U64(1), OrdinaryValue::U64(2)];
    assert!(
        ExactJson::from_ordinary(
            OrdinaryValue::Array(&array),
            Limits {
                nodes: 3,
                depth: 1,
                ..Default::default()
            }
        )
        .is_ok()
    );
    for limits in [
        Limits {
            nodes: 2,
            ..Default::default()
        },
        Limits {
            depth: 0,
            ..Default::default()
        },
    ] {
        assert_eq!(
            ExactJson::from_ordinary(OrdinaryValue::Array(&array), limits)
                .unwrap_err()
                .diagnostic()
                .code(),
            Code::Limit
        );
    }
    let error = ExactJson::from_ordinary(
        OrdinaryValue::Object(&[
            ("same", OrdinaryValue::Null),
            ("same", OrdinaryValue::Bool(true)),
        ]),
        Limits::default(),
    )
    .unwrap_err();
    assert_eq!(error.diagnostic().code(), Code::DuplicateMember);
    assert_eq!(
        error.diagnostic().source_context().unwrap().origin,
        SourceOrigin::Constructed
    );
    assert!(!error.source_bytes().is_empty());
}
#[test]
fn r12_error_chaining_and_source_domains() {
    let failure = Document::parse(
        "{}",
        Some("https://synthetic-base.example"),
        Limits {
            target_bytes: 2,
            ..Default::default()
        },
    )
    .unwrap_err();
    assert!(failure.diagnostic().location().is_none());
    assert!(failure.diagnostic().source_context().is_none());
    assert_eq!(
        failure.diagnostic().context(),
        Some(&DiagnosticContext::CallerInput)
    );

    let failure = ExactJson::parse("[", Limits::default()).unwrap_err();
    let cause = std::error::Error::source(&failure)
        .unwrap()
        .downcast_ref::<Diagnostic>()
        .unwrap();
    assert_eq!(cause, failure.diagnostic());
    assert_eq!(
        cause.source_context().unwrap().origin,
        SourceOrigin::Authored
    );
    let a = ExactJson::parse("1", Limits::default()).unwrap().root();
    let b = ExactJson::parse("1", Limits::default()).unwrap().root();
    assert_ne!(a.source_context(), b.source_context());
    let c = ExactJson::from_ordinary(OrdinaryValue::U64(9_007_199_254_740_993), Limits::default())
        .unwrap()
        .root();
    let error = c.number().unwrap().to_f64_exact().unwrap_err();
    assert_eq!(error.source_context(), Some(c.source_context()));
    assert_eq!(error.location().cloned(), Some(c.location()));
    let text = r#"{"openapi":"3.1.2","servers":[{"url":"https://api.example"}],"paths":{"/":{"get":{"operationId":"op","parameters":[{"name":"q","in":"query","schema":{}}]}}}}"#;
    let op = Document::parse(text, None, Limits::default())
        .unwrap()
        .operation_id("op")
        .unwrap();
    let bad = ExactJson::from_ordinary(OrdinaryValue::Null, Limits::default())
        .unwrap()
        .root();
    let input = Input {
        parameters: vec![ParameterInput {
            location: ParameterLocation::Query,
            name: "q".into(),
            value: bad.clone(),
        }],
        ..Default::default()
    };
    let error = op.prepare(&input).unwrap_err();
    assert_eq!(error.code(), Code::UnsupportedValue);
    assert_eq!(error.source_context(), Some(bad.source_context()));
    assert_eq!(error.location().cloned(), Some(bad.location()));
}

#[test]
fn named_policy_orders_primary_reason_without_hiding_other_facts() {
    let p = operation(Limits::default()).request().prepare().unwrap();
    let mut caps = HostCapabilities::programmable();
    caps.no_retries = false;
    let out = ready(p.invoke(Cancellation::default(), &caps, |_, _| async {
        panic!("preflight must not dispatch")
    }));
    let failure = accept(out).unwrap_err();
    assert_eq!(failure.reason, CompleteJsonRefusal::NotDispatched);
    assert!(failure.outcome.error.is_some());
    let out = ready(p.invoke(
        Cancellation::default(),
        &HostCapabilities::programmable(),
        |_, _| async {
            TransportResult {
                response: None,
                upload: UploadState::Unknown,
                error_detail: None,
            }
        },
    ));
    let failure = accept(out).unwrap_err();
    assert_eq!(failure.reason, CompleteJsonRefusal::MissingResponse);
    assert_eq!(failure.outcome.upload, UploadState::Unknown);
    assert!(failure.decoded.is_none());
    assert!(failure.decode_error.is_none());
    for (cancel, late, status, upload, expected) in [
        (
            true,
            true,
            500,
            UploadState::Unknown,
            CompleteJsonRefusal::Cancelled,
        ),
        (
            false,
            true,
            500,
            UploadState::Unknown,
            CompleteJsonRefusal::Transport,
        ),
        (
            false,
            false,
            500,
            UploadState::Unknown,
            CompleteJsonRefusal::HttpStatus,
        ),
        (
            false,
            false,
            200,
            UploadState::Unknown,
            CompleteJsonRefusal::UploadUncertain,
        ),
        (
            false,
            false,
            200,
            UploadState::Complete,
            CompleteJsonRefusal::Decode,
        ),
    ] {
        let failure = accept(outcome(status, upload, late, cancel, false, b"{")).unwrap_err();
        assert_eq!(failure.reason, expected);
        assert_eq!(failure.outcome.cancelled, cancel);
        assert_eq!(failure.outcome.error.is_some(), late);
        assert_eq!(failure.outcome.upload, upload);
        assert_eq!(failure.outcome.response.as_ref().unwrap().status(), status);
        assert_eq!(failure.outcome.response.as_ref().unwrap().raw(), b"{");
        assert_eq!(
            failure.decode_error.as_ref().unwrap().code(),
            Code::InvalidJson
        );
        assert!(failure.decoded.is_none());
    }
}
