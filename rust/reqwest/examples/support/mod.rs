#![allow(dead_code)]
use serde::{Deserialize, Serialize};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::TcpListener,
};
pub type Error = Box<dyn std::error::Error + Send + Sync>;
#[derive(Serialize)]
pub struct Create<'a> {
    pub name: &'a str,
}
#[derive(Debug, Deserialize)]
pub struct Created {
    pub id: u64,
    pub name: String,
}
pub fn document(origin: &str) -> Vec<u8> {
    serde_json::to_vec(&serde_json::json!({
        "openapi":"3.1.2", "info":{"title":"Dynamic client tutorial","version":"1"},
        "servers":[{"url":origin}],
        "components":{"securitySchemes":{"token":{"type":"http","scheme":"bearer"}}},
        "paths":{"/items/{id}":{"post":{
            "operationId":"create", "summary":"Create an item",
            "security":[{"token":[]}],
            "parameters":[
                {"in":"path","name":"id","required":true,"schema":{"type":"integer"}},
                {"in":"query","name":"mode","schema":{"type":"string"}},
                {"in":"header","name":"X-Trace","schema":{"type":"string"}}
            ],
            "requestBody":{"required":true,"content":{"application/json":{"schema":{"type":"object"}}}},
            "responses":{
                "201":{"description":"created","content":{"application/json":{"schema":{"type":"object"}}}},
                "422":{"description":"rejected","content":{"application/json":{"schema":{"type":"object"}}}}
            }
        }}}
    })).unwrap()
}
pub async fn service() -> Result<
    (
        Vec<u8>,
        String,
        tokio::task::JoinHandle<std::io::Result<()>>,
    ),
    Error,
> {
    let listener = TcpListener::bind("127.0.0.1:0").await?;
    let origin = format!("http://{}", listener.local_addr()?);
    let source = document(&origin);
    let task = tokio::spawn(async move {
        for _ in 0..2 {
            let (mut stream, _) = listener.accept().await?;
            let mut head = Vec::new();
            while !head.ends_with(b"\r\n\r\n") {
                head.push(stream.read_u8().await?);
                assert!(head.len() < 16384);
            }
            let head = String::from_utf8(head).unwrap();
            assert!(head.starts_with("POST /items/7?mode="));
            assert!(head.contains("authorization: Bearer local-token\r\n"));
            assert!(head.contains("x-trace: trace-1\r\n"));
            let length: usize = head
                .lines()
                .find_map(|s| s.strip_prefix("content-length: "))
                .unwrap()
                .parse()
                .unwrap();
            assert!(length < 4096);
            let mut body = vec![0; length];
            stream.read_exact(&mut body).await?;
            assert_eq!(body, br#"{"name":"example"}"#);
            let (status, body) = if head.starts_with("POST /items/7?mode=reject ") {
                (
                    "422 Unprocessable Content",
                    br#"{"error":"rejected"}"#.as_slice(),
                )
            } else {
                (
                    "201 Created",
                    br#"{"id":9007199254740993,"name":"example"}"#.as_slice(),
                )
            };
            stream.write_all(format!("HTTP/1.1 {status}\r\nContent-Type: application/json\r\nX-Request-Id: local-1\r\nContent-Length: {}\r\nConnection: close\r\n\r\n", body.len()).as_bytes()).await?;
            stream.write_all(body).await?;
        }
        Ok(())
    });
    Ok((source, origin, task))
}

/// Answer the create call with 303 See Other, then serve the status it names.
pub async fn redirect_service() -> Result<
    (
        Vec<u8>,
        String,
        tokio::task::JoinHandle<std::io::Result<()>>,
    ),
    Error,
> {
    let listener = TcpListener::bind("127.0.0.1:0").await?;
    let origin = format!("http://{}", listener.local_addr()?);
    let source = document(&origin);
    let task = tokio::spawn(async move {
        for hop in 0..2 {
            let (mut stream, _) = listener.accept().await?;
            let mut head = Vec::new();
            while !head.ends_with(b"\r\n\r\n") {
                head.push(stream.read_u8().await?);
                assert!(head.len() < 16384);
            }
            let head = String::from_utf8(head).unwrap();
            // The same origin keeps the credential on both hops.
            assert!(head.contains("authorization: Bearer local-token\r\n"));
            if hop == 0 {
                assert!(head.starts_with("POST /items/7 "));
                let length: usize = head
                    .lines()
                    .find_map(|s| s.strip_prefix("content-length: "))
                    .unwrap()
                    .parse()
                    .unwrap();
                let mut body = vec![0; length];
                stream.read_exact(&mut body).await?;
                stream.write_all(b"HTTP/1.1 303 See Other\r\nLocation: /items/7/status\r\nContent-Length: 0\r\nConnection: close\r\n\r\n").await?;
            } else {
                // 303 turned the POST into a GET without content.
                assert!(head.starts_with("GET /items/7/status "));
                assert!(!head.contains("content-type"));
                let body = br#"{"id":7,"name":"example"}"#;
                stream.write_all(format!("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n", body.len()).as_bytes()).await?;
                stream.write_all(body).await?;
            }
        }
        Ok(())
    });
    Ok((source, origin, task))
}

#[derive(Debug, Deserialize)]
pub struct ApiError {
    pub error: String,
}
