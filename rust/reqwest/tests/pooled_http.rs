//! Actual-socket qualification, with no performance assertions. Pool readiness
//! is observed through pinned test-only tracing, never inferred from a sleep.
mod support;
use dynamic_openapi_client::{
    BodyState, Cancellation, Credential, CredentialValue, DispatchEvidence, Document, ExactJson,
    Limits, Outcome, ParameterLocation, PreparedRequest, Provenance, UploadState, policies,
};
use dynamic_openapi_client_reqwest::{Certificate, Client};
use std::time::Duration;
use support::{Observation, PoolEvents, ReadMode, Server, bounded};
use tokio::task::JoinHandle;

const IDLE: &str = "pooling idle connection";
fn response(status: &str, headers: &str, body: &[u8]) -> Vec<u8> {
    let mut bytes = format!("HTTP/1.1 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\n{headers}\r\n", body.len()).into_bytes();
    bytes.extend_from_slice(body);
    bytes
}
fn request(
    client: &Client,
    origin: &str,
    id: &str,
    body: &str,
    limits: Limits,
    secure: bool,
) -> PreparedRequest {
    let security = if secure {
        serde_json::json!([{"bearer":[]}])
    } else {
        serde_json::json!([])
    };
    let source = serde_json::json!({"openapi":"3.1.0", "info":{"title":"pool qualification","version":"1"}, "servers":[{"url":origin}], "components":{"securitySchemes":{"bearer":{"type":"http","scheme":"bearer"}}}, "paths":{"/calls/{id}":{"post":{"operationId":"call", "security":security, "parameters":[{"in":"path","name":"id","required":true,"schema":{"type":"string"}}], "requestBody":{"content":{"application/json":{"schema":{}}}}, "responses":{"200":{"description":"ok"}}}}}});
    let document = Document::parse(serde_json::to_vec(&source).unwrap(), None, limits).unwrap();
    let operation = document.operation_id("call").unwrap();
    let mut builder = client
        .request(&operation)
        .parameter(ParameterLocation::Path, "id", id);
    // Empty body is useful for zero-byte response limits, which also limit uploads.
    let exact = (!body.is_empty()).then(|| ExactJson::parse(body, Limits::default()).unwrap());
    if let Some(exact) = &exact {
        builder = builder.json(exact.root());
    }
    builder.prepare().unwrap()
}
fn start(
    client: &Client,
    request: PreparedRequest,
    cancellation: Cancellation,
) -> JoinHandle<Outcome> {
    let client = client.clone();
    tokio::spawn(async move { client.execute(&request, cancellation).await })
}
fn call(client: &Client, origin: &str, id: &str) -> JoinHandle<Outcome> {
    start(
        client,
        request(client, origin, id, "{}", Limits::default(), false),
        Cancellation::default(),
    )
}
async fn done(task: JoinHandle<Outcome>) -> Outcome {
    bounded(task).await.unwrap()
}
fn complete(outcome: Outcome, body: &[u8]) {
    assert!(!outcome.cancelled);
    assert!(outcome.error.is_none(), "{outcome:?}");
    assert_eq!(outcome.dispatch, DispatchEvidence::Dispatched);
    assert_eq!(outcome.upload, UploadState::Unknown);
    let received = outcome.response.unwrap();
    assert_eq!(received.body_state(), BodyState::Complete);
    assert_eq!(received.raw(), body);
    assert_eq!(received.provenance(), Provenance::Wire);
}
async fn good_followup(server: &Server, client: &Client, index: usize, id: &str) -> usize {
    let task = call(client, &server.origin, id);
    let capture = server.request(index).await;
    assert!(
        capture
            .head
            .starts_with(&format!("POST /calls/{id} HTTP/1.1\r\n"))
    );
    server
        .reply(
            capture.connection,
            response("200 OK", "", br#"{"clean":true}"#),
        )
        .await;
    complete(done(task).await, br#"{"clean":true}"#);
    capture.connection
}

#[tokio::test(flavor = "current_thread")]
async fn eligible_http_and_tls_connections_reuse_across_clones() {
    for tls in [false, true] {
        let events = PoolEvents::new();
        let _trace = tracing::subscriber::set_default(events.clone());
        let server = Server::start(tls).await;
        let mut builder = Client::builder().credential(
            "bearer",
            Credential {
                value: CredentialValue::Bearer("fixture-token".into()),
                origins: vec![server.origin.clone()],
            },
        );
        if tls {
            builder = builder
                .add_root_certificate(
                    Certificate::from_pem(include_bytes!("fixtures/ca.pem")).unwrap(),
                )
                .add_root_certificate(
                    Certificate::from_pem(include_bytes!("fixtures/other-ca.pem")).unwrap(),
                );
        }
        let client = builder.build().unwrap();
        let cloned = client.clone();
        let body = r#"{"n":9007199254740993123456789,"id":"exact"}"#;
        for index in 0..3 {
            let active = if index == 1 { &cloned } else { &client };
            let task = start(
                active,
                request(
                    active,
                    &server.origin,
                    &format!("id{index}"),
                    body,
                    Limits::default(),
                    true,
                ),
                Cancellation::default(),
            );
            let capture = server.request(index).await;
            assert_eq!(capture.connection, 1);
            assert!(
                capture
                    .head
                    .starts_with(&format!("POST /calls/id{index} HTTP/1.1\r\n"))
            );
            assert_eq!(capture.body, body.as_bytes());
            assert!(
                capture
                    .head
                    .contains("authorization: Bearer fixture-token\r\n")
            );
            assert!(!capture.head.to_ascii_lowercase().contains("\r\ncookie:"));
            server
                .reply(
                    capture.connection,
                    response(
                        "200 OK",
                        "Set-Cookie: ambient=forbidden\r\n",
                        br#"{"ok":true}"#,
                    ),
                )
                .await;
            complete(done(task).await, br#"{"ok":true}"#);
            events.wait(IDLE, index + 1).await;
        }
        assert_eq!(server.accepted(), vec![1]);
        assert_eq!(server.requests().len(), 3);
        let handshakes: Vec<_> = server
            .events()
            .into_iter()
            .filter_map(|e| {
                if let Observation::TlsSuccess(id) = e {
                    Some(id)
                } else {
                    None
                }
            })
            .collect();
        assert_eq!(handshakes, if tls { vec![1] } else { vec![] });
        drop(cloned);
        drop(client);
        server.finish().await;
    }
}

#[tokio::test(flavor = "current_thread")]
async fn origin_and_independent_client_trust_are_isolated() {
    let events = PoolEvents::new();
    let _trace = tracing::subscriber::set_default(events.clone());
    let a = Server::start(false).await;
    let b = Server::start(false).await;
    let client = Client::builder()
        .credential(
            "bearer",
            Credential {
                value: CredentialValue::Bearer("origin-a-token".into()),
                origins: vec![a.origin.clone()],
            },
        )
        .build()
        .unwrap();
    for (index, server) in [&a, &b, &a, &b].into_iter().enumerate() {
        let secure = index % 2 == 0;
        let id = format!("origin{index}");
        let task = start(
            &client,
            request(
                &client,
                &server.origin,
                &id,
                "{}",
                Limits::default(),
                secure,
            ),
            Cancellation::default(),
        );
        let capture = server.request(index / 2).await;
        assert_eq!(capture.connection, 1);
        assert!(
            capture
                .head
                .starts_with(&format!("POST /calls/{id} HTTP/1.1\r\n"))
        );
        assert_eq!(
            capture
                .head
                .contains("authorization: Bearer origin-a-token\r\n"),
            secure
        );
        if !secure {
            assert!(
                !capture
                    .head
                    .to_ascii_lowercase()
                    .contains("\r\nauthorization:")
            );
        }
        server
            .reply(capture.connection, response("200 OK", "", b"{}"))
            .await;
        complete(done(task).await, b"{}");
        events.wait(IDLE, index + 1).await;
    }
    assert_eq!(a.accepted(), vec![1]);
    assert_eq!(b.accepted(), vec![1]);
    drop(client);
    a.finish().await;
    b.finish().await;
    let tls = Server::start(true).await;
    let trusted = Client::builder()
        .add_root_certificate(Certificate::from_pem(include_bytes!("fixtures/ca.pem")).unwrap())
        .build()
        .unwrap();
    good_followup(&tls, &trusted, 0, "trusted").await;
    events.wait(IDLE, 5).await;
    for other_root in [false, true] {
        let mut builder = Client::builder();
        if other_root {
            builder = builder.add_root_certificate(
                Certificate::from_pem(include_bytes!("fixtures/other-ca.pem")).unwrap(),
            );
        }
        let untrusted = builder.build().unwrap();
        let outcome = done(call(&untrusted, &tls.origin, "must-not-arrive")).await;
        assert!(outcome.error.is_some());
        assert!(outcome.response.is_none());
        assert_eq!(outcome.upload, UploadState::Unknown);
    }
    tls.wait(|observations| {
        observations
            .iter()
            .filter(|o| matches!(o, Observation::TlsFailure(_)))
            .count()
            == 2
    })
    .await;
    assert_eq!(
        good_followup(&tls, &trusted.clone(), 1, "trusted-again").await,
        1
    );
    events.wait(IDLE, 6).await;
    assert_eq!(tls.requests().len(), 2);
    assert_eq!(tls.accepted().len(), 3);
    let failed: Vec<_> = tls
        .events()
        .into_iter()
        .filter_map(|o| {
            if let Observation::TlsFailure(id) = o {
                Some(id)
            } else {
                None
            }
        })
        .collect();
    assert_eq!(failed, vec![2, 3]);
    drop(trusted);
    tls.finish().await;
}

#[tokio::test(flavor = "current_thread")]
async fn complete_framing_and_application_refusals_do_not_require_eviction() {
    let events = PoolEvents::new();
    let _trace = tracing::subscriber::set_default(events.clone());
    let server = Server::start(false).await;
    let client = Client::new().unwrap();
    let cases = [
        (response("200 OK", "", b""), b"".as_slice()),
        (b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n2\r\n{}\r\n0\r\nX-Trailer: ignored\r\n\r\n".to_vec(), b"{}".as_slice()),
        (response("422 Unprocessable Entity", "", br#"{"error":true}"#), br#"{"error":true}"#.as_slice()),
        (response("307 Temporary Redirect", &format!("Location: {}/forbidden\r\n", server.origin), b"{}"), b"{}".as_slice()),
        (response("200 OK", "", b"not json"), b"not json".as_slice()),
        (response("200 OK", "Content-Type: text/plain\r\n", b"{}"), b"{}".as_slice()),
        (response("200 OK", "Content-Encoding: gzip\r\n", include_bytes!("fixtures/body.json.gz")), include_bytes!("fixtures/body.json.gz").as_slice()),
    ];
    for (index, (bytes, body)) in cases.into_iter().enumerate() {
        let task = call(&client, &server.origin, &format!("case{index}"));
        let capture = server.request(index).await;
        assert_eq!(capture.connection, 1);
        server.reply(capture.connection, bytes).await;
        let outcome = done(task).await;
        complete(outcome.clone(), body);
        if index == 1 {
            assert!(policies::received_2xx_json(outcome).is_ok());
        } else {
            assert!(policies::received_2xx_json(outcome).is_err());
        }
        events.wait(IDLE, index + 1).await;
    }
    // Backend header parsing succeeds, but core retention refuses extra metadata.
    let limits = Limits {
        headers: 4,
        ..Limits::default()
    };
    let task = start(
        &client,
        request(&client, &server.origin, "metadata", "{}", limits, false),
        Cancellation::default(),
    );
    let capture = server.request(7).await;
    server
        .reply(
            capture.connection,
            response("200 OK", "X-A: 1\r\nX-B: 2\r\nX-C: 3\r\n", b"{}"),
        )
        .await;
    let outcome = done(task).await;
    assert!(outcome.error.is_some());
    let received = outcome.response.unwrap();
    assert_eq!(received.raw(), b"{}");
    assert_eq!(received.body_state(), BodyState::Complete);
    assert_eq!(received.headers().len(), 4);
    assert_eq!(received.omitted_header_lines(), 1);
    events.wait(IDLE, 8).await;
    assert_eq!(
        good_followup(&server, &client, 8, "after-refusals").await,
        1
    );
    assert_eq!(server.requests().len(), 9);
    assert_eq!(server.accepted(), vec![1]);
    drop(client);
    server.finish().await;
}

#[tokio::test(flavor = "current_thread")]
async fn close_delimited_and_explicit_close_require_fresh_sockets() {
    let server = Server::start(false).await;
    let client = Client::new().unwrap();
    for (index, bytes) in [
        b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{}".to_vec(),
        response("200 OK", "Connection: close\r\n", b"{}"),
    ]
    .into_iter()
    .enumerate()
    {
        let task = call(&client, &server.origin, &format!("closed{index}"));
        let capture = server.request(index).await;
        assert_eq!(capture.connection, index + 1);
        server
            .reply_mode(capture.connection, bytes, ReadMode::Full, true)
            .await;
        complete(done(task).await, b"{}");
    }
    assert_eq!(good_followup(&server, &client, 2, "fresh").await, 3);
    assert_eq!(server.requests().len(), 3);
    drop(client);
    server.finish().await;
}

#[tokio::test(flavor = "current_thread")]
async fn malformed_or_incomplete_framing_cannot_contaminate_a_following_response() {
    let mut huge_headers = b"HTTP/1.1 200 OK\r\nContent-Length: 0\r\n".to_vec();
    for _ in 0..257 {
        huge_headers.extend_from_slice(b"X-Count: x\r\n");
    }
    huge_headers.extend_from_slice(b"\r\n");
    let cases = [
        b"HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nabc".to_vec(),
        b"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nab".to_vec(),
        b"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\nZ\r\nwrong\r\n".to_vec(),
        huge_headers,
    ];
    for (index, bytes) in cases.into_iter().enumerate() {
        // Separate clients isolate parser disposal from the independently tested
        // race of a successful idle connection closing before the next request.
        let server = Server::start(false).await;
        let client = Client::new().unwrap();
        let task = call(&client, &server.origin, &format!("broken{index}"));
        let capture = server.request(0).await;
        server
            .reply_mode(capture.connection, bytes, ReadMode::Full, true)
            .await;
        let outcome = done(task).await;
        assert!(outcome.error.is_some());
        assert_eq!(outcome.upload, UploadState::Unknown);
        if index == 3 {
            assert!(outcome.response.is_none());
        } else if let Some(received) = outcome.response {
            assert_eq!(received.body_state(), BodyState::Failed);
            assert!(b"abc".starts_with(received.raw()) || b"ab".starts_with(received.raw()));
        }
        assert_ne!(
            good_followup(&server, &client, 1, &format!("clean{index}")).await,
            capture.connection
        );
        drop(client);
        let final_events = server.finish().await;
        assert_eq!(
            final_events
                .iter()
                .filter(|e| matches!(e, Observation::Started(_)))
                .count(),
            2
        );
        assert_eq!(
            final_events
                .iter()
                .filter(|e| matches!(e, Observation::Request(_)))
                .count(),
            2
        );
    }
}

#[tokio::test(flavor = "current_thread")]
async fn retained_body_limits_distinguish_complete_and_withheld_framing() {
    let events = PoolEvents::new();
    let _trace = tracing::subscriber::set_default(events.clone());
    let server = Server::start(false).await;
    let client = Client::new().unwrap();
    // Exact-limit completion, including zero, is reusable.
    for (index, body) in [b"".as_slice(), b"123".as_slice()].into_iter().enumerate() {
        let task = start(
            &client,
            request(
                &client,
                &server.origin,
                "exact",
                "",
                Limits {
                    body_bytes: body.len(),
                    ..Limits::default()
                },
                false,
            ),
            Cancellation::default(),
        );
        let capture = server.request(index).await;
        assert_eq!(capture.connection, 1);
        server
            .reply(capture.connection, response("200 OK", "", body))
            .await;
        complete(done(task).await, body);
        events.wait(IDLE, index + 1).await;
    }
    // A fully written small response can be backend-complete before the retained
    // cap refuses it. This fixture permits either disposal or clean reuse.
    let task = start(
        &client,
        request(
            &client,
            &server.origin,
            "complete-over-limit",
            "",
            Limits {
                body_bytes: 3,
                ..Limits::default()
            },
            false,
        ),
        Cancellation::default(),
    );
    let capture = server.request(2).await;
    server
        .reply(capture.connection, response("200 OK", "", b"12345"))
        .await;
    let outcome = done(task).await;
    let received = outcome.response.unwrap();
    assert_eq!(received.raw(), b"123");
    assert_eq!(received.body_state(), BodyState::Truncated);
    // Wait for an actual backend disposition, not a guessed chunk boundary.
    tokio::select! { _ = events.wait(IDLE, 3) => {}, _ = server.wait(|observations| observations.iter().any(|e| matches!(e, Observation::Closed(id) if *id == capture.connection))) => {} }
    good_followup(&server, &client, 3, "after-complete-limit").await;
    // Remaining declared bytes are deliberately unavailable; disposal must close.
    let task = start(
        &client,
        request(
            &client,
            &server.origin,
            "incomplete-over-limit",
            "",
            Limits {
                body_bytes: 3,
                ..Limits::default()
            },
            false,
        ),
        Cancellation::default(),
    );
    let capture = server.request(4).await;
    server
        .reply(
            capture.connection,
            b"HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n12345".to_vec(),
        )
        .await;
    let outcome = done(task).await;
    let received = outcome.response.unwrap();
    assert_eq!(received.raw(), b"123");
    assert_eq!(received.body_state(), BodyState::Truncated);
    server
        .wait(|events| {
            events
                .iter()
                .any(|e| matches!(e, Observation::Closed(id) if *id == capture.connection))
        })
        .await;
    assert_ne!(
        good_followup(&server, &client, 5, "after-incomplete-limit").await,
        capture.connection
    );
    assert_eq!(server.requests().len(), 6);
    drop(client);
    server.finish().await;
}

#[tokio::test(flavor = "current_thread")]
async fn pending_cancellation_and_deadlines_dispose_unavailable_framing() {
    for deadline in [false, true] {
        for headers in [false, true] {
            let server = Server::start(false).await;
            let client = Client::builder()
                .timeout(Duration::from_secs(1))
                .build()
                .unwrap();
            let cancellation = Cancellation::default();
            let task = start(
                &client,
                request(
                    &client,
                    &server.origin,
                    "pending",
                    "{}",
                    Limits::default(),
                    false,
                ),
                cancellation.clone(),
            );
            let capture = server.request(0).await;
            if headers {
                server
                    .reply(
                        capture.connection,
                        b"HTTP/1.1 503 Unavailable\r\nContent-Length: 100\r\n\r\nabc".to_vec(),
                    )
                    .await;
                server
                    .wait(|events| {
                        events.iter().any(
                            |e| matches!(e, Observation::Written(id) if *id == capture.connection),
                        )
                    })
                    .await;
            }
            if deadline {
                tokio::time::pause();
                tokio::time::advance(Duration::from_secs(2)).await;
                tokio::time::resume();
            } else {
                cancellation.cancel();
            }
            let outcome = done(task).await;
            assert!(outcome.error.is_some());
            assert_eq!(outcome.cancelled, !deadline);
            assert_eq!(outcome.dispatch, DispatchEvidence::Dispatched);
            assert_eq!(outcome.upload, UploadState::Unknown);
            if let Some(received) = outcome.response {
                assert!(headers);
                assert_eq!(received.status(), 503);
                assert!(b"abc".starts_with(received.raw()));
                assert_eq!(received.body_state(), BodyState::Failed);
            }
            server
                .wait(|events| {
                    events
                        .iter()
                        .any(|e| matches!(e, Observation::Closed(id) if *id == capture.connection))
                })
                .await;
            assert_ne!(
                good_followup(&server, &client, 1, "after-pending").await,
                capture.connection
            );
            assert_eq!(server.requests().len(), 2);
            drop(client);
            server.finish().await;
        }
    }
}

#[tokio::test(flavor = "current_thread")]
async fn ten_active_connections_are_allowed_but_only_eight_idle_are_retained() {
    let events = PoolEvents::new();
    let _trace = tracing::subscriber::set_default(events.clone());
    let server = Server::start(false).await;
    let client = Client::new().unwrap();
    let mut calls = Vec::new();
    for index in 0..10 {
        calls.push(call(
            &client.clone(),
            &server.origin,
            &format!("first{index}"),
        ));
    }
    server.request(9).await;
    let first = server.requests();
    assert_eq!(server.accepted().len(), 10);
    for capture in first.iter().rev() {
        let path = capture.head.split_whitespace().nth(1).unwrap();
        server
            .reply(
                capture.connection,
                response("200 OK", "", format!("{path:?}").as_bytes()),
            )
            .await;
    }
    for (index, task) in calls.into_iter().enumerate() {
        complete(
            done(task).await,
            format!("\"/calls/first{index}\"").as_bytes(),
        );
    }
    events.wait(IDLE, 8).await;
    events.wait("max idle per host", 2).await;
    server
        .wait(|observations| {
            observations
                .iter()
                .filter(|e| matches!(e, Observation::Closed(_)))
                .count()
                == 2
        })
        .await;
    let closed = server.closed();
    let retained: Vec<_> = server
        .accepted()
        .into_iter()
        .filter(|id| !closed.contains(id))
        .collect();
    assert_eq!(retained.len(), 8);
    let mut calls = Vec::new();
    for index in 0..8 {
        calls.push(call(&client, &server.origin, &format!("second{index}")));
    }
    server.request(17).await;
    let second = server.requests()[10..].to_vec();
    assert_eq!(server.accepted().len(), 10);
    for capture in second.iter().rev() {
        assert!(retained.contains(&capture.connection));
        let path = capture.head.split_whitespace().nth(1).unwrap();
        server
            .reply(
                capture.connection,
                response("200 OK", "", format!("{path:?}").as_bytes()),
            )
            .await;
    }
    for (index, task) in calls.into_iter().enumerate() {
        complete(
            done(task).await,
            format!("\"/calls/second{index}\"").as_bytes(),
        );
    }
    assert_eq!(server.requests().len(), 18);
    drop(client);
    server.finish().await;
}

#[tokio::test(flavor = "current_thread")]
async fn observed_server_close_and_racy_close_do_not_duplicate_an_application_call() {
    for wait_for_server_close in [true, false] {
        let events = PoolEvents::new();
        let _trace = tracing::subscriber::set_default(events.clone());
        let server = Server::start(false).await;
        let client = Client::new().unwrap();
        good_followup(&server, &client, 0, "warm").await;
        events.wait(IDLE, 1).await;
        server.close(1).await;
        if wait_for_server_close {
            server
                .wait(|observations| {
                    observations
                        .iter()
                        .any(|e| matches!(e, Observation::Closed(1)))
                })
                .await;
        }
        // Server closure is observed, but this does not claim the backend has
        // consumed its FIN. Hyper's idle-EOF trace is disabled in this profile.
        let task = call(&client, &server.origin, "stale");
        // The racy attempt can fail after starting. Keep accepting/recording while
        // awaiting either its one request or its failure, with no forced schedule.
        let mut task = task;
        tokio::select! {
            result = &mut task => { let outcome = result.unwrap(); assert!(outcome.error.is_some()); assert_eq!(outcome.upload, UploadState::Unknown); }
            capture = server.request(1) => { assert!(capture.head.starts_with("POST /calls/stale HTTP/1.1\r\n")); server.reply(capture.connection, response("200 OK", "", b"{}")).await; complete(done(task).await, b"{}"); }
        }
        let before = server.requests().len();
        good_followup(&server, &client, before, "explicit-next").await;
        drop(client);
        let final_events = server.finish().await;
        assert!(
            final_events
                .iter()
                .filter(|e| matches!(e, Observation::Started(_)))
                .count()
                <= 3
        );
        let requests: Vec<_> = final_events
            .iter()
            .filter_map(|e| {
                if let Observation::Request(r) = e {
                    Some(r)
                } else {
                    None
                }
            })
            .collect();
        assert!(
            requests
                .iter()
                .filter(|r| r.head.starts_with("POST /calls/stale "))
                .count()
                <= 1
        );
        assert_eq!(
            requests
                .iter()
                .filter(|r| r.head.starts_with("POST /calls/warm "))
                .count(),
            1
        );
        assert_eq!(
            requests
                .iter()
                .filter(|r| r.head.starts_with("POST /calls/explicit-next "))
                .count(),
            1
        );
    }
}

#[tokio::test(flavor = "current_thread")]
async fn started_partial_and_complete_post_are_never_replayed() {
    for partial in [true, false] {
        let events = PoolEvents::new();
        let _trace = tracing::subscriber::set_default(events.clone());
        let server = Server::start(false).await;
        let client = Client::new().unwrap();
        let warm = call(&client, &server.origin, "warm");
        let capture = server.request(0).await;
        server
            .reply_mode(
                capture.connection,
                response("200 OK", "", b"{}"),
                if partial {
                    ReadMode::PrefixThenClose(7)
                } else {
                    ReadMode::Full
                },
                false,
            )
            .await;
        complete(done(warm).await, b"{}");
        events.wait(IDLE, 1).await;
        let body = format!("\"{}\"", "x".repeat(256 * 1024));
        let task = start(
            &client,
            request(
                &client,
                &server.origin,
                "started-once",
                &body,
                Limits::default(),
                false,
            ),
            Cancellation::default(),
        );
        if partial {
            server
                .wait(|events| {
                    events
                        .iter()
                        .any(|e| matches!(e, Observation::Prefix(1, bytes) if bytes == b"POST /c"))
                })
                .await;
        } else {
            let capture = server.request(1).await;
            assert_eq!(capture.connection, 1);
            assert_eq!(capture.body, body.as_bytes());
            server.close(capture.connection).await;
        }
        let outcome = done(task).await;
        assert!(outcome.error.is_some());
        assert!(outcome.response.is_none());
        assert_eq!(outcome.upload, UploadState::Unknown);
        let index = if partial { 1 } else { 2 };
        assert_ne!(
            good_followup(&server, &client, index, "explicit-next").await,
            1
        );
        drop(client);
        let final_events = server.finish().await;
        let starts: Vec<_> = final_events
            .iter()
            .filter_map(|e| {
                if let Observation::Started(id) = e {
                    Some(*id)
                } else {
                    None
                }
            })
            .collect();
        assert_eq!(
            starts.len(),
            3,
            "every positive first request read must belong to warm, started-once, or explicit-next"
        );
        assert_eq!(&starts[..2], &[1, 1]);
        assert_ne!(starts[2], 1);
        let requests: Vec<_> = final_events
            .iter()
            .filter_map(|e| {
                if let Observation::Request(r) = e {
                    Some(r)
                } else {
                    None
                }
            })
            .collect();
        assert_eq!(requests.len(), index + 1);
        assert_eq!(
            requests
                .iter()
                .filter(|r| r.head.starts_with("POST /calls/started-once "))
                .count(),
            usize::from(!partial)
        );
        let prefixes: Vec<_> = final_events
            .into_iter()
            .filter_map(|e| {
                if let Observation::Prefix(id, bytes) = e {
                    Some((id, bytes))
                } else {
                    None
                }
            })
            .collect();
        assert_eq!(
            prefixes,
            if partial {
                vec![(1, b"POST /c".to_vec())]
            } else {
                vec![]
            }
        );
    }
}

#[tokio::test(flavor = "current_thread")]
async fn idle_connection_expires_after_ninety_seconds_on_the_tokio_clock() {
    let events = PoolEvents::new();
    let _trace = tracing::subscriber::set_default(events.clone());
    let server = Server::start(false).await;
    let client = Client::new().unwrap();
    let first = good_followup(&server, &client, 0, "before-expiry").await;
    events.wait(IDLE, 1).await;
    tokio::time::pause();
    tokio::time::advance(Duration::from_secs(91)).await;
    tokio::time::resume();
    // Checkout checks age even if interval cleanup has not closed the socket yet.
    assert_ne!(
        good_followup(&server, &client, 1, "after-expiry").await,
        first
    );
    drop(client);
    let final_events = server.finish().await;
    assert_eq!(
        final_events
            .iter()
            .filter(|e| matches!(e, Observation::Started(_)))
            .count(),
        2
    );
}
