use dynamic_openapi_client::*;
use serde::Deserialize;
use std::{
    error::Error,
    future::Future,
    sync::Arc,
    task::{Context, Poll, Waker},
};

#[test]
fn typed_projection_keeps_exact_source_and_uses_explicit_serde_numeric_semantics() {
    let exact = ExactJson::parse("9007199254740993", Limits::default()).unwrap();
    assert_eq!(exact.deserialize::<u64>().unwrap(), 9_007_199_254_740_993);
    assert_eq!(exact.deserialize::<f64>().unwrap(), 9_007_199_254_740_992.0);
    assert_eq!(
        exact
            .root()
            .number()
            .unwrap()
            .to_f64_exact()
            .unwrap_err()
            .code(),
        Code::NumericConversion
    );
    assert_eq!(exact.source(), "9007199254740993");
    assert!(exact.deserialize::<u8>().is_err());
    let max = ExactJson::parse(u128::MAX.to_string(), Limits::default()).unwrap();
    assert_eq!(max.deserialize::<u128>().unwrap(), u128::MAX);
    let min = ExactJson::parse(i128::MIN.to_string(), Limits::default()).unwrap();
    assert_eq!(min.deserialize::<i128>().unwrap(), i128::MIN);
    assert!(
        ExactJson::parse("1e9999", Limits::default())
            .unwrap()
            .deserialize::<f64>()
            .is_err()
    );
}

#[test]
fn borrowed_projection_and_sensitive_error_detail() {
    #[derive(Deserialize)]
    struct Item<'a> {
        name: &'a str,
    }
    let exact = ExactJson::parse(r#"{"name":"item"}"#, Limits::default()).unwrap();
    assert_eq!(exact.deserialize::<Item<'_>>().unwrap().name, "item");
    let secret = ExactJson::parse(r#""response-secret""#, Limits::default()).unwrap();
    let error = secret.deserialize::<u64>().unwrap_err();
    assert!(!format!("{error} {error:?}").contains("response-secret"));
    assert!(error.source().is_none());
    assert!(error.detail().to_string().contains("response-secret"));
    assert!(error.column() > 0);
}

fn ready<F: Future>(future: F) -> F::Output {
    match Box::pin(future)
        .as_mut()
        .poll(&mut Context::from_waker(Waker::noop()))
    {
        Poll::Ready(value) => value,
        Poll::Pending => panic!("fixture future unexpectedly pending"),
    }
}

fn outcome(upload: UploadState, status: u16, token: Cancellation) -> Outcome {
    let document = Document::parse(br#"{"openapi":"3.1.0","servers":[{"url":"https://api.example"}],"paths":{"/items":{"get":{"operationId":"items","responses":{"200":{"description":"ok"}}}}}}"#, None, Limits::default()).unwrap();
    let request = document
        .operation_id("items")
        .unwrap()
        .request()
        .prepare()
        .unwrap();
    ready(request.invoke(
        token,
        &HostCapabilities::programmable(),
        |_, _| async move {
            TransportResult {
                response: Some(ResponseInput {
                    status,
                    headers: vec![Header::new("Content-Type", "application/json")],
                    body: DeliveredBody {
                        bytes: Arc::from(br#"{"id":9007199254740993}"#.as_slice()),
                        state: BodyState::Complete,
                        provenance: Provenance::Wire,
                    },
                    opaque_redirect: false,
                }),
                upload,
                error_detail: None,
            }
        },
    ))
}

#[test]
fn explicit_response_policy_preserves_uncertain_upload_and_http_error_evidence() {
    let received = outcome(UploadState::Unknown, 200, Cancellation::default());
    assert_eq!(
        policies::complete_2xx_json(received.clone())
            .unwrap_err()
            .reason,
        CompleteJsonRefusal::UploadUncertain
    );
    let accepted = policies::received_2xx_json(received).unwrap();
    assert_eq!(accepted.outcome.upload, UploadState::Unknown);
    #[derive(Deserialize)]
    struct Item {
        id: u64,
    }
    assert_eq!(
        accepted.json.deserialize::<Item>().unwrap().id,
        9_007_199_254_740_993
    );
    let refusal =
        policies::received_2xx_json(outcome(UploadState::Unknown, 422, Cancellation::default()))
            .unwrap_err();
    assert_eq!(refusal.reason, CompleteJsonRefusal::HttpStatus);
    assert_eq!(refusal.outcome.response.as_ref().unwrap().status(), 422);
    assert!(refusal.decoded.is_some());
    let token = Cancellation::default();
    token.cancel();
    let refusal =
        policies::received_2xx_json(outcome(UploadState::Unknown, 200, token)).unwrap_err();
    assert_eq!(refusal.reason, CompleteJsonRefusal::Cancelled);
    assert_eq!(refusal.outcome.dispatch, DispatchEvidence::NotDispatched);
}

#[test]
fn default_errors_are_readable_and_explicit_location_formatting_is_escaped() {
    let failure =
        ExactJson::parse("{\"secret\\nkey\":0,\"secret\\nkey\":1}", Limits::default()).unwrap_err();
    let error = failure.diagnostic();
    assert!(error.to_string().contains("duplicate decoded member names"));
    assert!(!format!("{error} {error:?}").contains("secret"));
    let exact = ExactJson::parse("{\"secret\\nkey\":1.1}", Limits::default()).unwrap();
    let value = exact.root().get("secret\nkey").unwrap();
    let numeric = value.number().unwrap().to_i64().unwrap_err();
    let located = numeric.display_with_location().to_string();
    assert!(located.contains("JSON pointer"));
    assert!(located.contains("secret\\nkey"));
    assert!(!located.contains('\n'));
}
