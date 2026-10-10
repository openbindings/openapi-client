//! Prepare the preserved provider document supplied as a local file. No dispatch.
//! cargo run --example stripe_metadata -- /path/to/stripe.document
use dynamic_openapi_client::*;
fn main() -> Result<(), Box<dyn std::error::Error>> {
    let path = std::env::args()
        .nth(1)
        .ok_or("supply a local Stripe OpenAPI document")?;
    let source = std::fs::read(path)?;
    let document = Document::parse(
        &source,
        Some("https://api.stripe.com/openapi.yaml"),
        Limits::default(),
    )?;
    let trace = document.root().source_trace();
    assert_eq!(trace.authored.as_ref().unwrap().source().as_bytes(), source);
    let metadata = [
        ("order", OrdinaryValue::String("o 7")),
        ("source", OrdinaryValue::String("web")),
    ];
    for name in ["GetCreditNotesPreview", "GetCreditNotesPreviewLines"] {
        let operation = document.operation_id(name)?;
        let prepared = operation
            .request()
            .selection(Selection {
                security: Some(1),
                ..Selection::default()
            })
            .credential(
                "bearerAuth",
                Credential {
                    value: CredentialValue::Bearer("dummy-token".into()),
                    origins: vec!["https://api.stripe.com".into()],
                },
            )
            .parameter(ParameterLocation::Query, "invoice", "in_example")
            .parameter(
                ParameterLocation::Query,
                "metadata",
                OrdinaryValue::Object(&metadata),
            )
            .prepare()?;
        assert!(
            prepared
                .target()
                .contains("metadata%5Border%5D=o%207&metadata%5Bsource%5D=web")
        );
        assert!(prepared.target().contains("invoice=in_example"));
        println!(
            "{}",
            serde_json::json!({"operation":name,"authored_version":document.version(),"prepared":true,"dispatched":false,"original_source_preserved":true})
        );
    }
    let expand = [OrdinaryValue::String("customer")];
    let refusal = document
        .operation_id("GetCustomers")?
        .request()
        .parameter(
            ParameterLocation::Query,
            "expand",
            OrdinaryValue::Array(&expand),
        )
        .prepare()
        .unwrap_err();
    assert_eq!(
        refusal.diagnostic().reason(),
        Some(&DiagnosticReason::UnsupportedStyleCombination)
    );
    println!(
        "{}",
        serde_json::json!({"operation":"GetCustomers","undefined_array_convention":"still refused","dispatched":false})
    );
    Ok(())
}
