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
        interpret(outcome)?;
    }
    server.await??;
    Ok(())
}

pub(crate) fn interpret(outcome: dynamic_openapi_client::Outcome) -> Result<(), support::Error> {
    println!("upload: {:?}", outcome.upload());
    if outcome.status().is_some() {
        let response = &outcome;
        println!(
            "status: {}, body: {:?}",
            response.status().unwrap(),
            response.body_state().unwrap()
        );
        for h in response.headers().unwrap() {
            println!("{}: {}", h.name(), String::from_utf8_lossy(h.value()));
        }
        if !(200..300).contains(&response.status().unwrap()) {
            println!(
                "error body: {}",
                String::from_utf8_lossy(response.raw().unwrap())
            );
        }
    }
    if outcome
        .status()
        .is_some_and(|status| !(200..300).contains(&status))
    {
        // docs-start: error-body
        let body = outcome
            .body_json()
            .and_then(|json| json.deserialize::<support::ApiError>());
        // docs-end: error-body
        println!("independent error-body inspection: {body:?}");
        if outcome.error().is_some() || outcome.cancelled() {
            let _: support::Created = outcome.received_2xx_json()?;
        }
        let error = body?;
        println!("handled API error: {}", error.error);
    } else {
        // docs-start: success-body
        let created: support::Created = outcome.received_2xx_json()?;
        assert_eq!(created.id, 9_007_199_254_740_993);
        println!("created: {}", created.name);
        // docs-end: success-body
    }
    Ok(())
}
