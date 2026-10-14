use dynamic_openapi_client::diagnostics::{
    Code, DiagnosticContext, DiagnosticReason, LimitKind, SerializationReason,
};
use dynamic_openapi_client::transport::{DispatchEvidence, UploadState};
use dynamic_openapi_client::{
    Cancellation, Credential, Document, Header, Limits, Outcome, ParameterLocation,
};
use dynamic_openapi_client_reqwest::{Client, advanced};
use serde::Serialize;
use std::cell::Cell;

fn document() -> Document {
    // Uppercase host deliberately forces native preflight refusal after successful
    // preparation. These tests never need a socket or an external service.
    Document::parse(
        r#"{
        "openapi":"3.1.0", "servers":[{"url":"https://EXAMPLE.test"}],
        "components":{"securitySchemes":{"token":{"type":"http","scheme":"bearer"}}},
        "paths":{"/items/{id}":{"post":{"operationId":"create","security":[{"token":[]}],
          "parameters":[{"in":"path","name":"id","required":true,"schema":{"type":"string"}}],
          "requestBody":{"content":{"application/json":{"schema":{}}}}}}}
    }"#,
        None,
        Limits::default(),
    )
    .unwrap()
}
fn credential() -> Credential {
    Credential::bearer("private-token", vec!["https://example.test".into()])
}

#[test]
fn constructor_failure_is_corrective_and_redacts_backend_material() {
    use dynamic_openapi_client::diagnostics::HostRefusal;
    use dynamic_openapi_client_reqwest::tls::Certificate;
    use std::error::Error;
    let certificate = Certificate::from_der(b"private-invalid-certificate").unwrap();
    let error = Client::builder()
        .add_root_certificate(certificate)
        .build()
        .unwrap_err();
    assert_eq!(error.code(), Code::HostCapability);
    assert_eq!(
        error.reason(),
        Some(&DiagnosticReason::Host(HostRefusal::Configuration))
    );
    assert!(
        error
            .to_string()
            .contains("check additional TLS trust certificates")
    );
    assert!(!format!("{error:?}").contains("private-invalid-certificate"));
    assert!(error.source().is_none());
    assert!(error.source_position().is_none());
}

#[tokio::test]
async fn defaults_apply_once_and_refuse_duplicates_and_collisions() {
    let doc = document();
    let op = doc.operation_id("create").unwrap();
    let request = || op.request().parameter(ParameterLocation::Path, "id", &"x");
    let client = Client::builder()
        .credential("token", credential())
        .build()
        .unwrap();
    let out = client.send(request()).await;
    assert_eq!(out.dispatch(), DispatchEvidence::NotDispatched);
    assert_eq!(out.upload(), UploadState::NotStarted);
    // Reaching backend target admission proves preparation accepted one credential.
    assert_eq!(out.error().unwrap().code(), Code::HostCapability);
    assert!(!out.cancelled());
    assert_eq!(out.replay_assurance(), None);

    let out = client
        .send(request().credential("token", credential()))
        .await;
    assert!(matches!(
        out.error().unwrap().reason(),
        Some(DiagnosticReason::DuplicateAssignment { .. })
    ));
    assert_eq!(out.dispatch(), DispatchEvidence::NotDispatched);
    let duplicated = Client::builder()
        .credential("token", credential())
        .credential("token", credential())
        .build()
        .unwrap();
    let out = duplicated.send(request()).await;
    assert!(matches!(
        out.error().unwrap().reason(),
        Some(DiagnosticReason::DuplicateAssignment { .. })
    ));
    assert_eq!(out.dispatch(), DispatchEvidence::NotDispatched);
    let out = client
        .send(request().header(Header::new("Authorization", "explicit")))
        .await;
    assert_eq!(out.error().unwrap().code(), Code::CredentialCollision);
    assert_eq!(out.dispatch(), DispatchEvidence::NotDispatched);

    let wrong = Client::builder()
        .credential(
            "token",
            Credential::bearer("private-token", vec!["https://elsewhere.test".into()]),
        )
        .build()
        .unwrap();
    let out = wrong.send(request()).await;
    assert_eq!(out.error().unwrap().code(), Code::CredentialOrigin);
    assert_eq!(out.dispatch(), DispatchEvidence::NotDispatched);
}

#[tokio::test]
async fn advanced_execution_does_not_reapply_defaults() {
    let doc = document();
    let prepared = doc
        .operation_id("create")
        .unwrap()
        .request()
        .parameter(ParameterLocation::Path, "id", &"x")
        .credential("token", credential())
        .prepare()
        .unwrap();
    let client = Client::builder()
        .credential("token", credential())
        .credential("token", credential())
        .build()
        .unwrap();
    let out = advanced::execute(&client, &prepared, Cancellation::default()).await;
    assert_eq!(out.error().unwrap().code(), Code::HostCapability);
    assert_eq!(out.dispatch(), DispatchEvidence::NotDispatched);
    assert_eq!(
        prepared
            .headers()
            .iter()
            .filter(|h| h.name().eq_ignore_ascii_case("authorization"))
            .count(),
        1
    );
}

struct Count<'a>(&'a Cell<usize>);
impl Serialize for Count<'_> {
    fn serialize<S: serde::Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        self.0.set(self.0.get() + 1);
        serializer.serialize_str("body")
    }
}

#[tokio::test]
async fn send_keeps_the_selected_token_first_error_and_single_serialization() {
    let doc = document();
    let op = doc.operation_id("create").unwrap();
    let client = Client::builder()
        .credential("token", credential())
        .build()
        .unwrap();
    let count = Cell::new(0);
    let out = client
        .send(
            op.request()
                .parameter(ParameterLocation::Path, "id", &"x")
                .json(&Count(&count)),
        )
        .await;
    assert_eq!(out.error().unwrap().code(), Code::HostCapability);
    assert_eq!(count.get(), 1);
    count.set(0);
    let token = Cancellation::default();
    let request = op
        .request()
        .cancellation(token.clone())
        .parameter(ParameterLocation::Path, "id", &"x")
        .json(&Count(&count));
    assert_eq!(count.get(), 1);
    token.cancel();
    let out = client.send(request).await;
    assert_eq!(out.error().unwrap().code(), Code::Cancelled);
    assert!(out.cancelled());
    assert_eq!(out.upload(), UploadState::NotStarted);
    assert_eq!(count.get(), 1);
    let out = client
        .send(
            op.request()
                .cancellation(token.clone())
                .json(&Count(&count)),
        )
        .await;
    assert_eq!(out.error().unwrap().code(), Code::Cancelled);
    assert_eq!(count.get(), 1);

    let request = op.request().cancellation(token).json(&f64::NAN);
    let out = client.send(request).await;
    assert_eq!(out.error().unwrap().code(), Code::Cancelled);
    let token = Cancellation::default();
    let request = op.request().cancellation(token.clone()).json(&f64::NAN);
    token.cancel();
    let out = client.send(request).await;
    assert_eq!(out.error().unwrap().code(), Code::Serialization);
    assert!(out.cancelled());
    assert_eq!(
        out.received_2xx_json_exact().unwrap_err(),
        *out.error().unwrap()
    );
    let retained = out.received_2xx_json_exact().unwrap_err();
    drop(out);
    assert_eq!(retained.code(), Code::Serialization);
}

// Match the paired stage-4/stage-5 review probes: one JSON body, no parameter
// assignments, and a scoped bearer credential. These refuse before any I/O.
fn ordering_document(document_bytes: usize) -> Document {
    let spec = serde_json::json!({
        "openapi": "3.1.0", "servers": [{"url": "https://EXAMPLE.test"}],
        "components": {"securitySchemes": {"token": {"type": "http", "scheme": "bearer"}}},
        "paths": {"/": {"post": {"operationId": "probe", "security": [{"token": []}],
            "requestBody": {"content": {"application/json": {"schema": {}}}}}}}
    });
    Document::parse(
        spec.to_string(),
        None,
        Limits {
            document_bytes,
            ..Limits::default()
        },
    )
    .unwrap()
}

fn ordering_credential() -> Credential {
    Credential::bearer("secret", vec!["https://example.test".into()])
}

struct Count150<'a>(&'a Cell<usize>);
impl Serialize for Count150<'_> {
    fn serialize<S: serde::Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        self.0.set(self.0.get() + 1);
        serializer.serialize_str(&"x".repeat(150))
    }
}

fn assert_ordering_refusal(outcome: &Outcome) {
    assert_eq!(outcome.dispatch(), DispatchEvidence::NotDispatched);
    assert_eq!(outcome.upload(), UploadState::NotStarted);
    assert!(!outcome.cancelled());
    assert_eq!(outcome.replay_assurance(), None);
    assert_eq!(outcome.error().unwrap().location(), None);
    assert_eq!(outcome.error().unwrap().setting(), None);
}

#[tokio::test]
async fn duplicate_defaults_follow_eager_body_serialization() {
    let doc = ordering_document(1_000_000);
    let client = Client::builder()
        .credential("token", ordering_credential())
        .credential("token", ordering_credential())
        .build()
        .unwrap();
    let count = Cell::new(0);
    let request = doc
        .operation_id("probe")
        .unwrap()
        .request()
        .json(&Count150(&count));
    assert_eq!(count.get(), 1);
    let outcome = client.send(request).await;
    assert_eq!(count.get(), 1);
    let error = outcome.error().unwrap();
    assert_eq!(error.code(), Code::InvalidSelection);
    assert_eq!(
        error.context(),
        Some(&DiagnosticContext::Credential {
            scheme: "token".into()
        })
    );
    assert_eq!(
        error.reason(),
        Some(&DiagnosticReason::DuplicateAssignment {
            first_assignment: 1,
            repeated_assignment: 2,
        })
    );
    assert_ordering_refusal(&outcome);
}

#[tokio::test]
async fn configured_and_per_call_duplicates_follow_eager_body_serialization() {
    let doc = ordering_document(1_000_000);
    let client = Client::builder()
        .credential("token", ordering_credential())
        .build()
        .unwrap();
    let count = Cell::new(0);
    let request = doc
        .operation_id("probe")
        .unwrap()
        .request()
        .credential("token", ordering_credential())
        .json(&Count150(&count));
    assert_eq!(count.get(), 1);
    let outcome = client.send(request).await;
    assert_eq!(count.get(), 1);
    let error = outcome.error().unwrap();
    assert_eq!(error.code(), Code::InvalidSelection);
    assert_eq!(
        error.context(),
        Some(&DiagnosticContext::Credential {
            scheme: "token".into()
        })
    );
    assert_eq!(
        error.reason(),
        Some(&DiagnosticReason::DuplicateAssignment {
            first_assignment: 0,
            repeated_assignment: 2,
        })
    );
    assert_ordering_refusal(&outcome);
}

#[tokio::test]
async fn non_finite_body_refusal_precedes_duplicate_defaults() {
    let doc = ordering_document(1_000_000);
    let client = Client::builder()
        .credential("token", ordering_credential())
        .credential("token", ordering_credential())
        .build()
        .unwrap();
    let outcome = client
        .send(doc.operation_id("probe").unwrap().request().json(&f64::NAN))
        .await;
    let error = outcome.error().unwrap();
    assert_eq!(error.code(), Code::Serialization);
    assert_eq!(error.context(), Some(&DiagnosticContext::Construction));
    assert_eq!(
        error.reason(),
        Some(&DiagnosticReason::Serialization(
            SerializationReason::NonFiniteNumber
        ))
    );
    assert_ordering_refusal(&outcome);
}

#[tokio::test]
async fn default_credential_budget_refusal_follows_eager_body_serialization() {
    let doc = ordering_document(700);
    let client = Client::builder()
        .credential(
            "token",
            Credential::bearer("x".repeat(600), vec!["https://example.test".into()]),
        )
        .build()
        .unwrap();
    let count = Cell::new(0);
    let request = doc
        .operation_id("probe")
        .unwrap()
        .request()
        .json(&Count150(&count));
    assert_eq!(count.get(), 1);
    let outcome = client.send(request).await;
    assert_eq!(count.get(), 1);
    let error = outcome.error().unwrap();
    assert_eq!(error.code(), Code::Limit);
    assert_eq!(error.context(), Some(&DiagnosticContext::CallerInput));
    assert_eq!(
        error.reason(),
        Some(&DiagnosticReason::Limit {
            kind: LimitKind::InputBytes,
            maximum: 700,
            actual: 757,
        })
    );
    assert_ordering_refusal(&outcome);
}
