#[path = "o6_support/mod.rs"]
mod support;
use dynamic_openapi_client::diagnostics::{DiagnosticContext, DiagnosticReason};
use dynamic_openapi_client::{Credential, Document, Limits, ParameterLocation};

fn main() -> Result<(), support::Error> {
    let document = Document::parse(
        support::document("https://api.example"),
        None,
        Limits::default(),
    )?;
    let error = document
        .operation_id("create")?
        .request()
        .parameter(ParameterLocation::Query, "typo", &1)
        .parameter(ParameterLocation::Path, "id", &7)
        .json(&support::Create { name: "example" })
        .credential(
            "token",
            Credential::bearer("local-token", vec!["https://api.example".into()]),
        )
        .prepare()
        .unwrap_err();
    drop(document);
    let diagnostic = error;
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
