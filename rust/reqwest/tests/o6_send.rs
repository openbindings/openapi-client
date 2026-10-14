use dynamic_openapi_client::diagnostics::{Code, DiagnosticReason};
use dynamic_openapi_client::transport::{DispatchEvidence, UploadState};
use dynamic_openapi_client::{
    Cancellation, Credential, Document, Header, Limits, ParameterLocation,
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
