//! Run with `cargo run -p dynamic-openapi-client-reqwest --example real_http`.
//! All traffic is to the controlled loopback server below; no external service.
use dynamic_openapi_client::{
    Cancellation, Credential, CredentialValue, DispatchEvidence, Document, ExactJson, Limits,
    ParameterLocation, UploadState, policies,
};
use dynamic_openapi_client_reqwest::Client;
use serde::{Deserialize, Serialize};
use std::time::Duration;
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpListener,
};

#[derive(Serialize)]
struct Create<'a> {
    name: &'a str,
    labels: [&'a str; 2],
}
#[derive(Debug, Deserialize)]
struct Created {
    id: u64,
    name: String,
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let (source, origin, server, stalled) = loopback().await?;
    let document = Document::parse(source, None, Limits::default())?;
    let operation = document.operation_id("create")?;
    let client = Client::builder()
        .credential(
            "bearer",
            Credential {
                value: CredentialValue::Bearer("local-example-token".into()),
                origins: vec![origin],
            },
        )
        .build()?;

    // Caller code: runtime selection, ordinary typed input, explicit received-response policy.
    let body = ExactJson::from_serializable(
        &Create {
            name: "example",
            labels: ["local", "typed"],
        },
        Limits::default(),
    )?;
    let prepared = client
        .request(&operation)
        .parameter(ParameterLocation::Query, "mode", "normal")
        .json(body.root())
        .prepare()?;
    let accepted =
        policies::received_2xx_json(client.execute(&prepared, Cancellation::default()).await)?;
    let created: Created = accepted.json.deserialize()?;
    assert_eq!((created.id, created.name.as_str()), (42, "example"));
    assert_eq!(accepted.outcome.upload, UploadState::Unknown);
    println!(
        "authenticated typed HTTP response: id={}, name={}; upload remains Unknown",
        created.id, created.name
    );

    // Cancellation is triggered only after the server has received the second request.
    let prepared = client
        .request(&operation)
        .parameter(ParameterLocation::Query, "mode", "stall")
        .json(body.root())
        .prepare()?;
    let cancellation = Cancellation::default();
    let trigger = cancellation.clone();
    let cancel_task = tokio::spawn(async move {
        stalled.await.unwrap();
        trigger.cancel();
    });
    let cancelled = tokio::time::timeout(
        Duration::from_secs(3),
        client.execute(&prepared, cancellation),
    )
    .await?;
    assert!(cancelled.cancelled);
    assert_eq!(cancelled.dispatch, DispatchEvidence::Dispatched);
    assert_eq!(cancelled.upload, UploadState::Unknown);
    assert!(cancelled.response.is_none());
    cancel_task.await?;
    server.await??;
    println!(
        "in-flight HTTP cancellation: local wait ended; remote effects and upload remain unknown"
    );
    Ok(())
}

// Controlled service setup kept separate from the caller flow.
async fn loopback() -> Result<
    (
        Vec<u8>,
        String,
        tokio::task::JoinHandle<std::io::Result<()>>,
        tokio::sync::oneshot::Receiver<()>,
    ),
    Box<dyn std::error::Error>,
> {
    let listener = TcpListener::bind("127.0.0.1:0").await?;
    let origin = format!("http://{}", listener.local_addr()?);
    let source = serde_json::to_vec(&serde_json::json!({
        "openapi":"3.1.0", "info":{"title":"Loopback example","version":"1"},
        "servers":[{"url":origin}],
        "components":{"securitySchemes":{"bearer":{"type":"http","scheme":"bearer"}}},
        "paths":{"/items":{"post":{"operationId":"create","security":[{"bearer":[]}],
            "parameters":[{"name":"mode","in":"query","schema":{"type":"string"}}],
            "requestBody":{"content":{"application/json":{"schema":{}}}},
            "responses":{"201":{"description":"created"}}}}}
    }))?;
    let (signal, stalled) = tokio::sync::oneshot::channel();
    let server = tokio::spawn(async move {
        for mode in ["normal", "stall"] {
            let (mut stream, _) = listener.accept().await?;
            let mut head = Vec::new();
            while !head.ends_with(b"\r\n\r\n") {
                head.push(stream.read_u8().await?);
                assert!(head.len() < 16_384);
            }
            let head = String::from_utf8(head).unwrap();
            assert!(head.starts_with(&format!("POST /items?mode={mode} HTTP/1.1\r\n")));
            assert!(head.contains("authorization: Bearer local-example-token\r\n"));
            let length: usize = head
                .lines()
                .find_map(|line| line.strip_prefix("content-length: "))
                .unwrap()
                .parse()
                .unwrap();
            assert!(length < 4096);
            let mut body = vec![0; length];
            stream.read_exact(&mut body).await?;
            assert_eq!(body, br#"{"name":"example","labels":["local","typed"]}"#);
            if mode == "normal" {
                let body = br#"{"id":42,"name":"example"}"#;
                stream.write_all(format!("HTTP/1.1 201 Created\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n", body.len()).as_bytes()).await?;
                stream.write_all(body).await?;
            } else {
                signal.send(()).unwrap();
                // Observe local connection closure; this cannot prove remote rollback.
                assert_eq!(
                    stream.read_u8().await.unwrap_err().kind(),
                    std::io::ErrorKind::UnexpectedEof
                );
                break;
            }
        }
        Ok(())
    });
    Ok((source, origin, server, stalled))
}
