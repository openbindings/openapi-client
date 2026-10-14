#[path = "o6_support/mod.rs"]
mod support;
use dynamic_openapi_client::{
    Cancellation, Credential, CredentialValue, Document, ExactJson, Limits, ParameterLocation,
};
use dynamic_openapi_client_reqwest::Client;
#[tokio::main]
async fn main() -> Result<(), support::Error> {
    let (bytes, origin, server) = support::service().await?;
    let document = Document::parse(bytes, None, Limits::default())?;
    let operation = document.operation_id("create")?;
    let client = Client::new()?;
    for mode in ["ok", "reject"] {
        let json =
            ExactJson::from_serializable(&support::Create { name: "example" }, Limits::default())?;
        let prepared = operation
            .request()
            .parameter(ParameterLocation::Path, "id", 7_u64)
            .parameter(ParameterLocation::Query, "mode", mode)
            .parameter(ParameterLocation::Header, "X-Trace", "trace-1")
            .json(json.root())
            .credential(
                "token",
                Credential {
                    value: CredentialValue::Bearer("local-token".into()),
                    origins: vec![origin.clone()],
                },
            )
            .prepare()?;
        let outcome = client.execute(&prepared, Cancellation::default()).await;
        println!(
            "status: {:?}",
            outcome.response.as_ref().map(|r| r.status())
        );
    }
    server.await??;
    Ok(())
}
