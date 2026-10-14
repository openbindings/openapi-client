#[path = "o6_support/mod.rs"]
mod support;
use dynamic_openapi_client::{
    Credential, CredentialValue, DiagnosticContext, DiagnosticReason, Document, ExactJson, Limits,
    ParameterLocation,
};

fn main() -> Result<(), support::Error> {
    let document = Document::parse(
        support::document("https://api.example"),
        None,
        Limits::default(),
    )?;
    let json =
        ExactJson::from_serializable(&support::Create { name: "example" }, Limits::default())?;
    let error = document
        .operation_id("create")?
        .request()
        .parameter(ParameterLocation::Query, "typo", 1)
        .parameter(ParameterLocation::Path, "id", 7)
        .json(json.root())
        .credential(
            "token",
            Credential {
                value: CredentialValue::Bearer("local-token".into()),
                origins: vec!["https://api.example".into()],
            },
        )
        .prepare()
        .unwrap_err();
    drop(document);
    let diagnostic = error.diagnostic();
    assert!(matches!(
        diagnostic.reason(),
        Some(DiagnosticReason::UnknownParameter)
    ));
    assert!(
        matches!(diagnostic.context(), Some(DiagnosticContext::Parameter { name, .. }) if name == "typo")
    );
    println!(
        "{diagnostic}; reason={:?}; context={:?}",
        diagnostic.reason(),
        diagnostic.context()
    );
    Ok(())
}
