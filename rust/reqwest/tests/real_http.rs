use dynamic_openapi_client::{
    BodyState, Cancellation, Credential, CredentialValue, DispatchEvidence, Document, ExactJson,
    Header, Limits, ParameterLocation, Provenance, UploadState, policies,
};
use dynamic_openapi_client_reqwest::{Certificate, Client};
use std::{sync::Arc, time::Duration};
use tokio::{
    io::{AsyncRead, AsyncReadExt, AsyncWriteExt},
    net::TcpListener,
};

#[derive(Debug)]
struct Captured {
    head: String,
    body: Vec<u8>,
}
async fn capture(stream: &mut (impl AsyncRead + Unpin)) -> Captured {
    let mut data = Vec::new();
    let end = loop {
        let byte = stream.read_u8().await.unwrap();
        data.push(byte);
        assert!(data.len() < 32_768);
        if data.ends_with(b"\r\n\r\n") {
            break data.len();
        }
    };
    let head = String::from_utf8(data).unwrap();
    let length = head
        .lines()
        .find_map(|line| {
            let (name, value) = line.split_once(':')?;
            name.eq_ignore_ascii_case("content-length")
                .then(|| value.trim().parse::<usize>().unwrap())
        })
        .unwrap_or(0);
    assert!(length < 2_000_000);
    let mut body = vec![0; length];
    stream.read_exact(&mut body).await.unwrap();
    assert_eq!(end, head.len());
    Captured { head, body }
}
fn response(status: &str, headers: &str, body: &[u8]) -> Vec<u8> {
    let mut bytes = format!("HTTP/1.1 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\n{headers}\r\n", body.len()).into_bytes();
    bytes.extend_from_slice(body);
    bytes
}
async fn one_reply(bytes: Vec<u8>) -> (String, tokio::task::JoinHandle<Captured>) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let origin = format!("http://{}", listener.local_addr().unwrap());
    let task = tokio::spawn(async move {
        let (mut stream, _) = listener.accept().await.unwrap();
        let request = capture(&mut stream).await;
        stream.write_all(&bytes).await.unwrap();
        request
    });
    (origin, task)
}
fn document(origin: &str, limit: usize, secure: bool) -> Document {
    let security = if secure {
        serde_json::json!([{"bearer":[]}])
    } else {
        serde_json::json!([])
    };
    let source = serde_json::json!({
        "openapi":"3.1.0", "info":{"title":"local test","version":"1"},
        "servers":[{"url":origin}],
        "components":{"securitySchemes":{"bearer":{"type":"http","scheme":"bearer"}}},
        "paths":{"/items/{id}":{"post":{"operationId":"run", "security":security,
            "parameters":[{"in":"path","name":"id","required":true,"schema":{"type":"string"}},
                          {"in":"query","name":"q","schema":{"type":"string"}}],
            "requestBody":{"content":{"application/json":{"schema":{}}}},
            "responses":{"200":{"description":"ok"}}}}}
    });
    Document::parse(
        serde_json::to_vec(&source).unwrap(),
        None,
        Limits {
            body_bytes: limit,
            ..Limits::default()
        },
    )
    .unwrap()
}
fn prepared(doc: &Document) -> dynamic_openapi_client::PreparedRequest {
    doc.operation_id("run")
        .unwrap()
        .request()
        .parameter(ParameterLocation::Path, "id", "alpha")
        .prepare()
        .unwrap()
}
async fn execute(
    client: &Client,
    request: &dynamic_openapi_client::PreparedRequest,
    cancel: Cancellation,
) -> dynamic_openapi_client::Outcome {
    tokio::time::timeout(Duration::from_secs(3), client.execute(request, cancel))
        .await
        .expect("adapter did not finish")
}

#[tokio::test]
async fn configured_auth_exact_body_and_explicit_server_close() {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let origin = format!("http://{}", listener.local_addr().unwrap());
    let server = tokio::spawn(async move {
        let mut requests = Vec::new();
        for _ in 0..2 {
            let (mut stream, _) = listener.accept().await.unwrap();
            requests.push(capture(&mut stream).await);
            stream
                .write_all(&response(
                    "200 OK",
                    "Connection: close\r\nSet-Cookie: ambient=forbidden\r\n",
                    br#"{"ok":true}"#,
                ))
                .await
                .unwrap();
            // Explicit server closure requires a new connection for the next call.
        }
        requests
    });
    let client = Client::builder()
        .credential(
            "bearer",
            Credential {
                value: CredentialValue::Bearer("local-test-token".into()),
                origins: vec![origin.clone()],
            },
        )
        .build()
        .unwrap();
    let doc = document(&origin, 4096, true);
    let exact = ExactJson::parse(
        br#"{"n":9007199254740993123456789,"nested":[false,"x"]}"#,
        Limits::default(),
    )
    .unwrap();
    let op = doc.operation_id("run").unwrap();
    let request = client
        .request(&op)
        .parameter(ParameterLocation::Path, "id", "a/b")
        .parameter(ParameterLocation::Query, "q", "x y&z")
        .json(exact.root())
        .prepare()
        .unwrap();
    for _ in 0..2 {
        let outcome = execute(&client, &request, Cancellation::default()).await;
        assert_eq!(outcome.dispatch, DispatchEvidence::Dispatched);
        assert_eq!(outcome.upload, UploadState::Unknown);
        let accepted = policies::received_2xx_json(outcome).unwrap();
        assert_eq!(accepted.json.root().raw(), r#"{"ok":true}"#);
    }
    for received in server.await.unwrap() {
        assert!(
            received
                .head
                .starts_with("POST /items/a%2Fb?q=x%20y%26z HTTP/1.1\r\n"),
            "{}",
            received.head
        );
        assert!(
            received
                .head
                .contains("authorization: Bearer local-test-token\r\n")
        );
        assert!(!received.head.to_ascii_lowercase().contains("\r\ncookie:"));
        assert_eq!(received.body, exact.root().raw().as_bytes());
    }
    assert!(!format!("{client:?}").contains("local-test-token"));
    let wrong_origin = document("http://127.0.0.1:1", 4096, true);
    assert!(
        client
            .request(&wrong_origin.operation_id("run").unwrap())
            .parameter(ParameterLocation::Path, "id", "x")
            .prepare()
            .is_err()
    );
}

#[tokio::test]
async fn non_success_json_and_duplicate_response_headers_survive() {
    let (origin, server) = one_reply(response(
        "422 Unprocessable Entity",
        "X-Evidence: one\r\nX-Evidence: two\r\n",
        br#"{"error":"bad input"}"#,
    ))
    .await;
    let outcome = execute(
        &Client::new().unwrap(),
        &prepared(&document(&origin, 4096, false)),
        Cancellation::default(),
    )
    .await;
    assert!(outcome.error.is_none());
    let refusal = policies::received_2xx_json(outcome).unwrap_err();
    assert_eq!(refusal.reason, policies::CompleteJsonRefusal::HttpStatus);
    assert_eq!(
        refusal.decoded.unwrap().root().raw(),
        r#"{"error":"bad input"}"#
    );
    let response = refusal.outcome.response.unwrap();
    assert_eq!(
        response
            .headers()
            .iter()
            .filter(|h| h.name() == "x-evidence")
            .map(|h| h.value())
            .collect::<Vec<_>>(),
        vec![b"one".as_slice(), b"two".as_slice()]
    );
    server.await.unwrap();
}

#[tokio::test]
async fn redirects_are_evidence_and_never_followed() {
    let destination = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let location = format!(
        "Location: http://{}/moved\r\n",
        destination.local_addr().unwrap()
    );
    let (origin, server) = one_reply(response(
        "307 Temporary Redirect",
        &location,
        br#"{"moved":true}"#,
    ))
    .await;
    let outcome = execute(
        &Client::new().unwrap(),
        &prepared(&document(&origin, 4096, false)),
        Cancellation::default(),
    )
    .await;
    let received = outcome.response.unwrap();
    assert_eq!(received.status(), 307);
    assert_eq!(received.raw(), br#"{"moved":true}"#);
    assert!(
        tokio::time::timeout(Duration::from_millis(50), destination.accept())
            .await
            .is_err()
    );
    server.await.unwrap();
}

#[tokio::test]
async fn response_limit_distinguishes_eof_from_more_bytes() {
    for (limit, body, state) in [
        (0, b"".as_slice(), BodyState::Complete),
        (0, b"x", BodyState::Truncated),
        (3, b"abc", BodyState::Complete),
        (3, b"abcd", BodyState::Truncated),
    ] {
        let (origin, server) = one_reply(response("200 OK", "", body)).await;
        let outcome = execute(
            &Client::new().unwrap(),
            &prepared(&document(&origin, limit, false)),
            Cancellation::default(),
        )
        .await;
        let received = outcome.response.unwrap();
        assert_eq!(received.raw(), &body[..body.len().min(limit)]);
        assert_eq!(received.body_state(), state);
        assert_eq!(received.provenance(), Provenance::Wire);
        server.await.unwrap();
    }
}

#[tokio::test]
async fn late_disconnect_preserves_received_prefix() {
    let (origin, server) = one_reply(b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"partial\":".to_vec()).await;
    let outcome = execute(
        &Client::new().unwrap(),
        &prepared(&document(&origin, 4096, false)),
        Cancellation::default(),
    )
    .await;
    assert!(outcome.error.is_some());
    assert_eq!(outcome.upload, UploadState::Unknown);
    let received = outcome.response.unwrap();
    assert_eq!(received.raw(), br#"{"partial":"#);
    assert_eq!(received.body_state(), BodyState::Failed);
    assert!(received.json().is_err());
    server.await.unwrap();
}

#[tokio::test]
async fn cancelled_before_dispatch_never_opens_a_connection() {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let doc = document(
        &format!("http://{}", listener.local_addr().unwrap()),
        4096,
        false,
    );
    let cancellation = Cancellation::default();
    cancellation.cancel();
    let outcome = execute(&Client::new().unwrap(), &prepared(&doc), cancellation).await;
    assert_eq!(outcome.dispatch, DispatchEvidence::NotDispatched);
    assert_eq!(outcome.upload, UploadState::NotStarted);
    assert!(outcome.cancelled);
    assert!(
        tokio::time::timeout(Duration::from_millis(50), listener.accept())
            .await
            .is_err()
    );
}

#[tokio::test]
async fn cancellation_interrupts_stalled_headers_and_body() {
    for with_headers in [false, true] {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let doc = document(
            &format!("http://{}", listener.local_addr().unwrap()),
            4096,
            false,
        );
        let (sent, seen) = tokio::sync::oneshot::channel();
        let server = tokio::spawn(async move {
            let (mut stream, _) = listener.accept().await.unwrap();
            capture(&mut stream).await;
            if with_headers {
                stream.write_all(b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"partial\":").await.unwrap();
                stream.flush().await.unwrap();
            }
            sent.send(()).unwrap();
            std::future::pending::<()>().await;
        });
        let cancellation = Cancellation::default();
        let trigger = cancellation.clone();
        let cancel_task = tokio::spawn(async move {
            seen.await.unwrap();
            trigger.cancel();
        });
        let outcome = execute(&Client::new().unwrap(), &prepared(&doc), cancellation).await;
        assert!(outcome.cancelled);
        assert_eq!(outcome.dispatch, DispatchEvidence::Dispatched);
        assert_eq!(outcome.upload, UploadState::Unknown);
        assert!(outcome.error.is_some());
        if let Some(received) = outcome.response {
            assert!(with_headers);
            assert!(br#"{"partial":"#.starts_with(received.raw()));
            assert_eq!(received.body_state(), BodyState::Failed);
        }
        cancel_task.await.unwrap();
        server.abort();
    }
}

#[tokio::test]
async fn deadline_interrupts_body_and_keeps_metadata() {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let doc = document(
        &format!("http://{}", listener.local_addr().unwrap()),
        4096,
        false,
    );
    let server = tokio::spawn(async move {
        let (mut stream, _) = listener.accept().await.unwrap();
        capture(&mut stream).await;
        stream
            .write_all(b"HTTP/1.1 503 Unavailable\r\nContent-Length: 100\r\n\r\nabc")
            .await
            .unwrap();
        std::future::pending::<()>().await;
    });
    let client = Client::builder()
        .timeout(Duration::from_millis(150))
        .build()
        .unwrap();
    let outcome = execute(&client, &prepared(&doc), Cancellation::default()).await;
    assert!(!outcome.cancelled);
    assert!(outcome.error.is_some());
    let received = outcome.response.unwrap();
    assert_eq!(received.status(), 503);
    assert_eq!(received.raw(), b"abc");
    assert_eq!(received.body_state(), BodyState::Failed);
    server.abort();
}

#[tokio::test]
async fn target_normalization_and_framing_headers_refuse_before_dispatch() {
    let client = Client::new().unwrap();
    for origin in [
        "http://LOCALHOST:12345",
        "http://localhost:80",
        "http://localhost:12345/a/%2e%2e",
    ] {
        let request = prepared(&document(origin, 4096, false));
        let outcome = execute(&client, &request, Cancellation::default()).await;
        assert_eq!(
            outcome.dispatch,
            DispatchEvidence::NotDispatched,
            "{origin}"
        );
        assert_eq!(outcome.upload, UploadState::NotStarted);
        assert!(outcome.error.is_some());
    }
    let doc = document("http://127.0.0.1:1", 4096, false);
    let request = doc
        .operation_id("run")
        .unwrap()
        .request()
        .parameter(ParameterLocation::Path, "id", "x")
        .header(Header::new("Content-Length", b"0"))
        .prepare()
        .unwrap();
    let outcome = execute(&client, &request, Cancellation::default()).await;
    assert_eq!(outcome.dispatch, DispatchEvidence::NotDispatched);
}

#[tokio::test]
async fn encoded_representation_is_not_silently_decoded() {
    let compressed = include_bytes!("fixtures/body.json.gz");
    let (origin, server) =
        one_reply(response("200 OK", "Content-Encoding: gzip\r\n", compressed)).await;
    let outcome = execute(
        &Client::new().unwrap(),
        &prepared(&document(&origin, 4096, false)),
        Cancellation::default(),
    )
    .await;
    let received = outcome.response.unwrap();
    assert_eq!(received.raw(), compressed);
    assert_eq!(received.provenance(), Provenance::Wire);
    assert!(received.json().is_err());
    server.await.unwrap();
}

#[tokio::test]
async fn failure_after_request_does_not_replay_on_a_fresh_connection() {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let doc = document(
        &format!("http://{}", listener.local_addr().unwrap()),
        4096,
        false,
    );
    let server = tokio::spawn(async move {
        let (mut stream, _) = listener.accept().await.unwrap();
        capture(&mut stream).await;
        drop(stream);
        assert!(
            tokio::time::timeout(Duration::from_millis(100), listener.accept())
                .await
                .is_err()
        );
    });
    let outcome = execute(
        &Client::new().unwrap(),
        &prepared(&doc),
        Cancellation::default(),
    )
    .await;
    assert!(outcome.error.is_some());
    assert!(outcome.response.is_none());
    assert_eq!(outcome.upload, UploadState::Unknown);
    server.await.unwrap();
}

fn tls_acceptor() -> tokio_rustls::TlsAcceptor {
    use tokio_rustls::rustls::{
        ServerConfig,
        pki_types::{CertificateDer, PrivateKeyDer},
    };
    let config = ServerConfig::builder()
        .with_no_client_auth()
        .with_single_cert(
            vec![CertificateDer::from(
                include_bytes!("fixtures/server.der").to_vec(),
            )],
            PrivateKeyDer::try_from(include_bytes!("fixtures/server-key.der").to_vec()).unwrap(),
        )
        .unwrap();
    tokio_rustls::TlsAcceptor::from(Arc::new(config))
}

#[tokio::test]
async fn https_checks_trust_and_additional_roots_are_additive() {
    for trusted in [false, true] {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let origin = format!("https://{}", listener.local_addr().unwrap());
        let acceptor = tls_acceptor();
        let server = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            match acceptor.accept(stream).await {
                Ok(mut stream) => {
                    assert!(trusted);
                    let received = capture(&mut stream).await;
                    assert!(
                        received
                            .head
                            .contains("authorization: Bearer tls-test-token\r\n")
                    );
                    stream
                        .write_all(&response("200 OK", "", br#"{"tls":true}"#))
                        .await
                        .unwrap();
                }
                Err(_) => assert!(!trusted),
            }
        });
        let mut builder = Client::builder().credential(
            "bearer",
            Credential {
                value: CredentialValue::Bearer("tls-test-token".into()),
                origins: vec![origin.clone()],
            },
        );
        if trusted {
            builder = builder
                .add_root_certificate(
                    Certificate::from_pem(include_bytes!("fixtures/ca.pem")).unwrap(),
                )
                .add_root_certificate(
                    Certificate::from_pem(include_bytes!("fixtures/other-ca.pem")).unwrap(),
                );
        }
        let client = builder.build().unwrap();
        let doc = document(&origin, 4096, true);
        let operation = doc.operation_id("run").unwrap();
        let request = client
            .request(&operation)
            .parameter(ParameterLocation::Path, "id", "alpha")
            .prepare()
            .unwrap();
        let outcome = execute(&client, &request, Cancellation::default()).await;
        if trusted {
            assert_eq!(
                policies::received_2xx_json(outcome)
                    .unwrap()
                    .json
                    .root()
                    .raw(),
                r#"{"tls":true}"#
            );
        } else {
            assert!(outcome.error.is_some());
            assert!(outcome.response.is_none());
            assert_eq!(outcome.upload, UploadState::Unknown);
        }
        server.await.unwrap();
    }
}
