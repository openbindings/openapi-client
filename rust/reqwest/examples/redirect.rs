#[path = "support/mod.rs"]
mod support;
use dynamic_openapi_client::{Cancellation, Credential, Document, Limits, ParameterLocation};
use dynamic_openapi_client_reqwest::{Client, advanced};
#[tokio::main]
async fn main() -> Result<(), support::Error> {
    let (bytes, origin, server) = support::redirect_service().await?;
    let document = Document::parse(bytes, None, Limits::default())?;
    let client = Client::new()?;
    let prepared = document
        .operation_id("create")?
        .request()
        .parameter(ParameterLocation::Path, "id", &7_u64)
        .json(&support::Create { name: "example" })
        .credential(
            "token",
            Credential::bearer("local-token", vec![origin.clone()]),
        )
        .prepare()?;
    let cancellation = Cancellation::default();
    // docs-start: follow-redirect
    let mut request = prepared;
    let mut outcome = advanced::execute(&client, &request, cancellation.clone()).await;
    while let Some(next) = request.follow_redirect(&outcome)? {
        request = next;
        outcome = advanced::execute(&client, &request, cancellation.clone()).await;
    }
    // docs-end: follow-redirect
    let created: support::Created = outcome.received_2xx_json()?;
    println!(
        "{} {}: {}",
        request.method().as_str(),
        request.target(),
        created.name
    );
    server.await??;
    Ok(())
}
