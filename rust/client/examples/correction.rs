#[path = "support/mod.rs"]
mod support;
use dynamic_openapi_client::diagnostics::{DiagnosticContext, DiagnosticReason};
use dynamic_openapi_client::{Credential, Document, Limits, ParameterLocation};

fn main() -> Result<(), support::Error> {
    let document = Document::parse(
        support::document("https://api.example"),
        None,
        Limits::default(),
    )?;
    let operation = document.operation_id("create")?;
    let diagnostic = operation
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
    // docs-start: correction
    assert!(matches!(
        diagnostic.reason(),
        Some(DiagnosticReason::UnknownParameter)
    ));
    assert!(
        matches!(diagnostic.context(), Some(DiagnosticContext::Parameter { name, .. }) if name == "typo")
    );
    // docs-end: correction
    // A refused builder stays refused. Correct the name in a fresh builder.
    let corrected = operation
        .request()
        .parameter(ParameterLocation::Query, "mode", &"ok")
        .parameter(ParameterLocation::Path, "id", &7)
        .json(&support::Create { name: "example" })
        .credential(
            "token",
            Credential::bearer("local-token", vec!["https://api.example".into()]),
        )
        .prepare()?;
    assert_eq!(corrected.target(), "https://api.example/items/7?mode=ok");
    println!(
        "{diagnostic}; reason={:?}; context={:?}",
        diagnostic.reason(),
        diagnostic.context()
    );
    Ok(())
}
