use dynamic_openapi_client::*;
use std::{
    future::Future,
    rc::Rc,
    sync::Arc,
    task::{Context, Poll, Waker},
};
const DOC: &str = r#"{"openapi":"3.1.2","servers":[{"url":"https://api.example"}],"paths":{"/items":{"get":{"operationId":"items"}}}}"#;
fn ready<F: Future>(f: F) -> F::Output {
    let mut f = Box::pin(f);
    let mut cx = Context::from_waker(Waker::noop());
    match f.as_mut().poll(&mut cx) {
        Poll::Ready(v) => v,
        Poll::Pending => panic!("unexpected pending"),
    }
}
fn result() -> TransportResult {
    TransportResult {
        response: Some(ResponseInput {
            status: 503,
            headers: vec![Header::new("Content-Type", "application/json")],
            body: DeliveredBody {
                bytes: Arc::from(br#"{"n":9007199254740993}"#.as_slice()),
                state: BodyState::Complete,
                provenance: Provenance::Wire,
            },
            opaque_redirect: false,
        }),
        upload: UploadState::Incomplete,
        error_detail: Some("private dummy host detail".into()),
    }
}
#[test]
fn send_and_local_callbacks_preserve_response() {
    fn assert_send<T: Send>(_: &T) {}
    fn assert_sync<T: Sync>() {}
    assert_sync::<Document>();
    assert_sync::<Operation>();
    assert_sync::<PreparedRequest>();
    let d = Document::parse(DOC, None, Limits::default()).unwrap();
    let op = d.operation_id("items").unwrap();
    drop(d);
    let p = op.prepare(&Input::default()).unwrap();
    drop(op);
    let host = HostCapabilities::programmable();
    let send = p.invoke(Cancellation::default(), &host, |_, _| async { result() });
    assert_send(&send);
    let o = ready(send);
    assert_eq!(o.upload, UploadState::Incomplete);
    assert_eq!(o.error.unwrap().code, Code::TransportFailure);
    assert_eq!(
        o.response
            .unwrap()
            .json()
            .unwrap()
            .root()
            .get("n")
            .unwrap()
            .raw(),
        "9007199254740993"
    );
    let local = Rc::new(7);
    let o = ready(
        p.invoke(Cancellation::default(), &host, move |_, _| async move {
            assert_eq!(*local, 7);
            result()
        }),
    );
    assert_eq!(o.response.unwrap().status(), 503);
}
#[test]
fn cancellation_and_small_limits_are_explicit() {
    let c = Cancellation::default();
    c.cancel();
    assert_eq!(
        ExactJson::parse_with_cancellation(DOC, Limits::default(), &c)
            .unwrap_err()
            .diagnostic
            .code,
        Code::Cancelled
    );
    let d = Document::parse(DOC, None, Limits::default()).unwrap();
    assert_eq!(
        d.operation_id("items")
            .unwrap()
            .prepare_with_cancellation(&Input::default(), &c)
            .unwrap_err()
            .code,
        Code::Cancelled
    );
    let l = Limits {
        document_bytes: 8,
        ..Limits::default()
    };
    let e = ExactJson::parse(" ".repeat(1_000_000), l).unwrap_err();
    assert_eq!(e.diagnostic.code, Code::Limit);
    assert!(e.source.is_empty());
    let l = Limits {
        nodes: 2,
        ..Limits::default()
    };
    assert_eq!(
        ExactJson::parse(format!("[{}]", vec!["0"; 10_000].join(",")), l)
            .unwrap_err()
            .diagnostic
            .code,
        Code::Limit
    );
}
#[test]
fn credentials_and_document_names_are_not_formatted() {
    let e=Document::parse(r#"{"openapi":"3.1.2","servers":[{"url":"https://api.example"}],"paths":{"/secret-key":{"get":{"responses":{"secret-key":{}}}}}}"#,None,Limits::default()).unwrap().operations().next().unwrap().prepare(&Input::default()).unwrap_err();
    assert!(!format!("{e:?}").contains("secret-key"));
    assert!(!e.to_string().contains("secret-key"));
    assert!(e.location.unwrap().pointer.contains("secret-key"));
}
#[test]
fn raw_body_debug_and_pending_invocation_are_honest() {
    let b = Body::Raw(b"dummy-secret".as_slice().into());
    assert!(!format!("{b:?}").contains("dummy-secret"));
    let d = Document::parse(DOC, None, Limits::default()).unwrap();
    let p = d
        .operation_id("items")
        .unwrap()
        .prepare(&Input::default())
        .unwrap();
    let host = HostCapabilities::programmable();
    let cancel = Cancellation::default();
    let mut f = Box::pin(p.invoke(cancel.clone(), &host, |_, c| async move {
        std::future::poll_fn(move |_| {
            if c.is_cancelled() {
                Poll::Ready(result())
            } else {
                Poll::Pending
            }
        })
        .await
    }));
    let mut cx = Context::from_waker(Waker::noop());
    assert!(f.as_mut().poll(&mut cx).is_pending());
    cancel.cancel();
    let Poll::Ready(out) = f.as_mut().poll(&mut cx) else {
        panic!()
    };
    assert!(out.cancelled);
    assert_eq!(out.dispatch, DispatchEvidence::Dispatched);
    assert_eq!(out.response.unwrap().status(), 503);
}
