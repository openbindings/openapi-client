#[path = "support/mod.rs"]
mod support;
use dynamic_openapi_client::{Credential, Document, Limits, ParameterLocation};
use dynamic_openapi_client_reqwest::Client;
#[tokio::main]
async fn main() -> Result<(), support::Error> {
    let (bytes, origin, server) = support::service().await?;
    let document = Document::parse(bytes, None, Limits::default())?;
    let operation = document.operation_id("create")?;
    let client = Client::new()?;
    for mode in ["ok", "reject"] {
        // docs-start: send
        let request = operation
            .request()
            .parameter(ParameterLocation::Path, "id", &7_u64)
            .parameter(ParameterLocation::Query, "mode", &mode)
            .parameter(ParameterLocation::Header, "X-Trace", &"trace-1")
            .json(&support::Create { name: "example" })
            .credential(
                "token",
                Credential::bearer("local-token", vec![origin.clone()]),
            );
        let outcome = client.send(request).await;
        // docs-end: send
        println!("status: {:?}", outcome.status());
    }
    server.await??;
    Ok(())
}
