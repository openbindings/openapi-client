#[path = "support/strict_json.rs"]
mod policies;
use dynamic_openapi_client::*;
use dynamic_openapi_client::{diagnostics::*, exact::*, source::*, transport::*};
use policies::CompleteJsonRefusal;
use serde::Deserialize;
use std::{
    error::Error,
    future::Future,
    sync::Arc,
    task::{Context, Poll, Waker},
};

#[test]
fn typed_projection_keeps_exact_source_and_uses_explicit_serde_numeric_semantics() {
    let exact = Value::parse("9007199254740993", Limits::default()).unwrap();
    assert_eq!(exact.deserialize::<u64>().unwrap(), 9_007_199_254_740_993);
    assert_eq!(exact.deserialize::<f64>().unwrap(), 9_007_199_254_740_992.0);
    assert_eq!(
        exact
            .clone()
            .number()
            .unwrap()
            .to_f64_exact()
            .unwrap_err()
            .code(),
        Code::NumericConversion
    );
    assert_eq!(exact.source(), "9007199254740993");
    let negative_zero = Value::parse("-0", Limits::default()).unwrap();
    assert!(negative_zero.deserialize::<i64>().is_err());
    assert!(
        negative_zero
            .deserialize::<f64>()
            .unwrap()
            .is_sign_negative()
    );
    assert_eq!(negative_zero.clone().number().unwrap().to_i64().unwrap(), 0);
    assert_eq!(negative_zero.source(), "-0");
    assert!(exact.deserialize::<u8>().is_err());
    let max = Value::parse(u128::MAX.to_string(), Limits::default()).unwrap();
    assert_eq!(max.deserialize::<u128>().unwrap(), u128::MAX);
    let min = Value::parse(i128::MIN.to_string(), Limits::default()).unwrap();
    assert_eq!(min.deserialize::<i128>().unwrap(), i128::MIN);
    assert!(
        Value::parse("1e9999", Limits::default())
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
    let exact = Value::parse(r#"{"name":"item"}"#, Limits::default()).unwrap();
    assert_eq!(exact.deserialize::<Item<'_>>().unwrap().name, "item");
    let secret = Value::parse(r#""response-secret""#, Limits::default()).unwrap();
    let error = secret.deserialize::<u64>().unwrap_err();
    assert!(!format!("{error} {error:?}").contains("response-secret"));
    assert!(error.source().is_none());
    assert!(
        error
            .detail()
            .unwrap()
            .to_string()
            .contains("response-secret")
    );
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
                response: Some(ResponseInput::new(
                    status,
                    vec![Header::new("Content-Type", "application/json")],
                    Arc::from(br#"{"id":9007199254740993}"#.as_slice()),
                    BodyState::Complete,
                    Provenance::Wire,
                )),
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
    let accepted = (received).received_2xx_json_exact().unwrap();
    assert_eq!(received.upload(), UploadState::Unknown);
    #[derive(Deserialize)]
    struct Item {
        id: u64,
    }
    assert_eq!(
        accepted.deserialize::<Item>().unwrap().id,
        9_007_199_254_740_993
    );
    let rejected = outcome(UploadState::Unknown, 422, Cancellation::default());
    let refusal = rejected.received_2xx_json_exact().unwrap_err();
    assert_eq!(refusal.code(), Code::HttpStatus);
    assert_eq!(rejected.status(), Some(422));
    assert!(rejected.body_json().is_ok());
    let token = Cancellation::default();
    token.cancel();
    let rejected = outcome(UploadState::Unknown, 200, token);
    let refusal = rejected.received_2xx_json_exact().unwrap_err();
    assert_eq!(refusal.code(), Code::Cancelled);
    assert_eq!(rejected.dispatch(), DispatchEvidence::NotDispatched);
}

#[test]
fn default_errors_are_readable_and_explicit_location_formatting_is_escaped() {
    let failure =
        Value::parse("{\"secret\\nkey\":0,\"secret\\nkey\":1}", Limits::default()).unwrap_err();
    let error = failure;
    assert!(error.to_string().contains("duplicate decoded member names"));
    assert!(!format!("{error} {error:?}").contains("secret"));
    let exact = Value::parse("{\"secret\\nkey\":1.1}", Limits::default()).unwrap();
    let value = exact.clone().get("secret\nkey").unwrap();
    let numeric = value.number().unwrap().to_i64().unwrap_err();
    let located = numeric.display_with_location().to_string();
    assert!(located.contains("JSON pointer"));
    assert!(located.contains("secret\\nkey"));
    assert!(!located.contains('\n'));
}

#[test]
fn repeated_response_json_calls_share_one_exact_owner() {
    let outcome = outcome(UploadState::Complete, 200, Cancellation::default());
    let response = &outcome;
    let first = response.body_json().unwrap();
    let second = response.body_json().unwrap();
    assert_eq!(first.source(), second.source());
    assert_eq!(
        first.clone().source_context().id,
        second.clone().source_context().id
    );
    assert_eq!(
        first.clone().source_context().origin,
        SourceOrigin::Authored
    );
    let child = first.clone().get("id").unwrap();
    let context = child.source_context();
    drop(first);
    drop(second);
    drop(outcome);
    assert_eq!(child.raw(), "9007199254740993");
    assert_eq!(child.location().pointer, "/id");
    assert_eq!(child.source_context(), context);
}
