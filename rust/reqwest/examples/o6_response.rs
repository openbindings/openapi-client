#[path = "o6_support/mod.rs"]
mod support;
use dynamic_openapi_client::{Cancellation, Credential, Document, Limits, ParameterLocation};
use dynamic_openapi_client_reqwest::Client;
#[tokio::main]
async fn main() -> Result<(), support::Error> {
    let (bytes, origin, server) = support::service().await?;
    let document = Document::parse(bytes, None, Limits::default())?;
    let operation = document.operation_id("create")?;
    let client = Client::new()?;
    for mode in ["ok", "reject"] {
        let prepared = operation
            .request()
            .parameter(ParameterLocation::Path, "id", &7_u64)
            .parameter(ParameterLocation::Query, "mode", &mode)
            .parameter(ParameterLocation::Header, "X-Trace", &"trace-1")
            .json(&support::Create { name: "example" })
            .credential(
                "token",
                Credential::bearer("local-token", vec![origin.clone()]),
            )
            .prepare()?;
        let outcome = client.execute(&prepared, Cancellation::default()).await;
        interpret(outcome)?;
    }
    server.await??;
    Ok(())
}

pub(crate) fn interpret(outcome: dynamic_openapi_client::Outcome) -> Result<(), support::Error> {
    println!("upload: {:?}", outcome.upload);
    if let Some(response) = &outcome.response {
        println!(
            "status: {}, body: {:?}",
            response.status(),
            response.body_state()
        );
        for h in response.headers() {
            println!("{}: {}", h.name(), String::from_utf8_lossy(h.value()));
        }
        if !(200..300).contains(&response.status()) {
            println!("error body: {}", String::from_utf8_lossy(response.raw()));
        }
    }
    if outcome
        .response
        .as_ref()
        .is_some_and(|r| !(200..300).contains(&r.status()))
    {
        let body: Result<support::ApiError, support::Error> =
            (|| Ok(outcome.response.as_ref().unwrap().json()?.deserialize()?))();
        println!("independent error-body inspection: {body:?}");
        if outcome.error.is_some() || outcome.cancelled {
            dynamic_openapi_client::policies::received_2xx_json(outcome)?;
        }
        let error = body?;
        println!("handled API error: {}", error.error);
    } else {
        let accepted = dynamic_openapi_client::policies::received_2xx_json(outcome)?;
        let created: support::Created = accepted.json.deserialize()?;
        assert_eq!(created.id, 9_007_199_254_740_993);
        println!("created: {}", created.name);
    }
    Ok(())
}
