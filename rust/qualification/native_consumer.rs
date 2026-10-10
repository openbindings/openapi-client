// Independent consuming-crate acceptance cases. Generated only from this lane's
// authored server/caller and frozen literal fixtures; no product/backend oracle.
mod server {
    //! Independent raw loopback server; no product/backend dependencies.
    use serde::Serialize;
    use std::{
        collections::BTreeMap,
        io::{Read, Write},
        net::{Shutdown, TcpListener, TcpStream},
        sync::{
            Arc, Condvar, Mutex,
            atomic::{AtomicBool, Ordering},
        },
        thread,
        time::Duration,
    };

    #[derive(Clone, Debug, Serialize)]
    pub struct Wire {
        pub method: String,
        pub target: String,
        pub version: String,
        pub fields: Vec<(String, String)>,
        pub body: String,
    }
    impl Wire {
        pub fn values(&self, name: &str) -> Vec<&str> {
            self.fields
                .iter()
                .filter(|(key, _)| key.eq_ignore_ascii_case(name))
                .map(|(_, value)| value.as_str())
                .collect()
        }
        pub fn normalized_fields(&self) -> BTreeMap<String, Vec<String>> {
            let mut fields: BTreeMap<String, Vec<String>> = BTreeMap::new();
            for (name, value) in &self.fields {
                fields
                    .entry(name.to_ascii_lowercase())
                    .or_default()
                    .push(value.clone());
            }
            fields
        }
    }

    pub struct Server {
        pub port: u16,
        requests: Arc<Mutex<Vec<Wire>>>,
        stop: Arc<AtomicBool>,
        failures: Arc<Mutex<Vec<String>>>,
        releases: Arc<Mutex<Vec<(String, Arc<Release>)>>>,
        listener: Option<thread::JoinHandle<()>>,
    }
    #[derive(Default)]
    struct Release(Mutex<bool>, Condvar);
    impl Release {
        fn release(&self) {
            *self.0.lock().unwrap() = true;
            self.1.notify_all();
        }
        fn wait(&self) {
            let (released, _) = self
                .1
                .wait_timeout_while(self.0.lock().unwrap(), Duration::from_secs(5), |released| {
                    !*released
                })
                .unwrap();
            assert!(*released, "independent response-release deadlock guard");
        }
    }
    fn read_request(stream: &mut TcpStream) -> Result<Wire, String> {
        stream
            .set_read_timeout(Some(Duration::from_secs(2)))
            .map_err(|e| e.to_string())?;
        let mut header = Vec::new();
        while !header.ends_with(b"\r\n\r\n") {
            if header.len() >= 65_536 {
                return Err("independent server header bound".into());
            }
            let mut byte = [0];
            if stream.read(&mut byte).map_err(|e| e.to_string())? == 0 {
                return Err("EOF before request fields".into());
            }
            header.push(byte[0]);
        }
        let text = std::str::from_utf8(&header).map_err(|e| e.to_string())?;
        let mut lines = text.split("\r\n");
        let request = lines.next().unwrap().split(' ').collect::<Vec<_>>();
        if request.len() != 3 {
            return Err("independent server malformed request line".into());
        }
        let mut fields = vec![];
        for line in lines.take_while(|line| !line.is_empty()) {
            let (name, value) = line
                .split_once(':')
                .ok_or("independent server malformed field")?;
            fields.push((
                name.to_owned(),
                value.trim_start_matches([' ', '\t']).to_owned(),
            ));
        }
        if fields
            .iter()
            .any(|(name, _)| name.eq_ignore_ascii_case("transfer-encoding"))
        {
            return Err("finite request unexpectedly used transfer encoding".into());
        }
        let length = fields
            .iter()
            .find(|(name, _)| name.eq_ignore_ascii_case("content-length"))
            .map(|(_, value)| value.parse::<usize>().map_err(|e| e.to_string()))
            .transpose()?
            .unwrap_or(0);
        if length > 1_048_576 {
            return Err("independent server request body bound".into());
        }
        let mut body = vec![0; length];
        stream.read_exact(&mut body).map_err(|e| e.to_string())?;
        Ok(Wire {
            method: request[0].to_owned(),
            target: request[1].to_owned(),
            version: request[2].to_owned(),
            fields,
            body: String::from_utf8(body).map_err(|e| e.to_string())?,
        })
    }

    fn body(name: &str) -> Vec<u8> {
        super::fixture(name)
    }
    fn send(
        stream: &mut TcpStream,
        port: u16,
        target: &str,
        release: &Release,
    ) -> std::io::Result<()> {
        let path = target.split('?').next().unwrap();
        if path == "/v1/once" {
            return Ok(());
        }
        let (status, media, content) = match path {
            "/v1/reject" => (422, "application/problem+json", body("problem.json")),
            "/v1/redirect" => (302, "application/json", body("success.json")),
            "/v1/large" => (200, "application/json", body("large.json")),
            "/v1/exact-limit" => (200, "application/json", body("exact-limit.json")),
            "/v1/numeric" => (200, "application/json", body("numeric.json")),
            "/v1/bad-type" => (200, "application/json", body("bad-type.json")),
            "/v1/custom-type" => (200, "application/json", body("custom-type.json")),
            "/v1/encoded" => (200, "application/json", body("encoded-success.gzip")),
            _ => (200, "application/json", body("success.json")),
        };
        if path == "/v1/wait" {
            release.wait();
            return Ok(());
        }
        let declared = if matches!(path, "/v1/broken" | "/v1/wait-body") {
            100
        } else {
            content.len()
        };
        let mut fields = format!(
            "HTTP/1.1 {status} Independent\r\nContent-Type: {media}\r\nContent-Length: {declared}\r\nX-Duplicate: first\r\nX-Duplicate: second\r\nX-Response-Secret: response-secret-R4\r\n"
        );
        if path == "/v1/redirect" {
            fields.push_str(&format!("Location: http://127.0.0.1:{port}/v1/landing\r\n"));
        }
        if path == "/v1/cookie-set" {
            fields.push_str("Set-Cookie: ambient-secret-cookie=server-value\r\n");
        }
        if path == "/v1/encoded" {
            fields.push_str("Content-Encoding: gzip\r\n");
        }
        fields.push_str("Connection: close\r\n\r\n");
        stream.write_all(fields.as_bytes())?;
        if path == "/v1/broken" {
            stream.write_all(&body("broken-prefix.bin"))?;
        } else if path == "/v1/wait-body" {
            stream.write_all(&body("pending-prefix.bin"))?;
            stream.flush()?;
            release.wait();
        } else {
            stream.write_all(&content)?;
        }
        stream.flush()
    }

    impl Server {
        pub fn start() -> Self {
            let listener = TcpListener::bind("127.0.0.1:0").unwrap();
            let port = listener.local_addr().unwrap().port();
            listener.set_nonblocking(true).unwrap();
            let requests = Arc::new(Mutex::new(vec![]));
            let stop = Arc::new(AtomicBool::new(false));
            let failures = Arc::new(Mutex::new(vec![]));
            let releases = Arc::new(Mutex::new(vec![]));
            let (r, s, f, gates) = (
                requests.clone(),
                stop.clone(),
                failures.clone(),
                releases.clone(),
            );
            let join = thread::spawn(move || {
                let mut handlers = Vec::new();
                while !s.load(Ordering::Acquire) {
                    match listener.accept() {
                        Ok((mut stream, _)) => {
                            // Darwin accept inherits the listener's O_NONBLOCK flag.
                            // Request parsing and scripted waits require blocking sockets.
                            if let Err(error) = stream.set_nonblocking(false) {
                                f.lock().unwrap().push(error.to_string());
                                continue;
                            }
                            let (requests, failures) = (r.clone(), f.clone());
                            let gates = gates.clone();
                            handlers.push(thread::spawn(move || {
                                match read_request(&mut stream) {
                                    Ok(wire) => {
                                        let target = wire.target.clone();
                                        let release = Arc::new(Release::default());
                                        gates
                                            .lock()
                                            .unwrap()
                                            .push((target.clone(), release.clone()));
                                        requests.lock().unwrap().push(wire);
                                        // Writes may fail when the client legitimately cancels or stops at its byte bound.
                                        let _ = send(&mut stream, port, &target, &release);
                                    }
                                    Err(error) => failures.lock().unwrap().push(error),
                                }
                                let _ = stream.shutdown(Shutdown::Both);
                            }));
                        }
                        Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => {
                            thread::sleep(Duration::from_millis(2))
                        }
                        Err(error) => {
                            f.lock().unwrap().push(error.to_string());
                            break;
                        }
                    }
                }
                for handler in handlers {
                    if handler.join().is_err() {
                        f.lock()
                            .unwrap()
                            .push("independent request handler panicked".into());
                    }
                }
            });
            Self {
                port,
                requests,
                stop,
                failures,
                releases,
                listener: Some(join),
            }
        }
        pub fn origin(&self) -> String {
            format!("http://127.0.0.1:{}", self.port)
        }
        pub fn records(&self) -> Vec<Wire> {
            self.requests.lock().unwrap().clone()
        }
        pub fn count(&self, target: &str) -> usize {
            self.requests
                .lock()
                .unwrap()
                .iter()
                .filter(|r| r.target == target)
                .count()
        }
        pub fn release(&self, target: &str) {
            let release = self
                .releases
                .lock()
                .unwrap()
                .iter()
                .rev()
                .find(|(path, _)| path == target)
                .expect("recorded request release")
                .1
                .clone();
            release.release();
        }
        fn release_all(&self) {
            let releases = self
                .releases
                .lock()
                .unwrap()
                .iter()
                .map(|(_, release)| release.clone())
                .collect::<Vec<_>>();
            for release in releases {
                release.release();
            }
        }
        pub async fn observed(&self, target: &str) -> Wire {
            for _ in 0..200 {
                if let Some(wire) = self
                    .requests
                    .lock()
                    .unwrap()
                    .iter()
                    .find(|r| r.target == target)
                    .cloned()
                {
                    return wire;
                }
                tokio::time::sleep(Duration::from_millis(10)).await;
            }
            panic!("independent server did not observe expected request")
        }
        pub fn finish(mut self) -> Vec<Wire> {
            self.stop.store(true, Ordering::Release);
            self.release_all();
            self.listener.take().unwrap().join().unwrap();
            let failures = self.failures.lock().unwrap().clone();
            assert!(
                failures.is_empty(),
                "independent server parse failures: {failures:?}"
            );
            self.records()
        }
    }
    impl Drop for Server {
        fn drop(&mut self) {
            self.stop.store(true, Ordering::Release);
            self.release_all();
            if let Some(join) = self.listener.take() {
                let _ = join.join();
            }
        }
    }
}

use dynamic_openapi_client::*;
use dynamic_openapi_client_reqwest::Client;
use serde::{Deserialize, Serialize};
use serde_json::{Value as Json, json};
use server::{Server, Wire};
use std::{
    future::Future,
    sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
        mpsc,
    },
    task::{Context, Wake, Waker},
    time::Duration,
};

const SPEC: &str = r###"{
  "openapi": "3.1.0",
  "info": {
    "title": "Independent native HTTP acceptance",
    "version": "0"
  },
  "servers": [
    {
      "url": "http://127.0.0.1:{port}/v1",
      "variables": {
        "port": {
          "default": "1"
        }
      }
    }
  ],
  "security": [],
  "paths": {
    "/items/{id}": {
      "post": {
        "operationId": "createItem",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        },
        "security": [
          {
            "bearerAuth": []
          }
        ],
        "parameters": [
          {
            "name": "id",
            "in": "path",
            "required": true,
            "schema": {
              "type": "string"
            }
          },
          {
            "name": "tag",
            "in": "query",
            "style": "form",
            "explode": true,
            "schema": {
              "type": "array",
              "items": {
                "type": "string"
              }
            }
          },
          {
            "name": "flag",
            "in": "query",
            "schema": {
              "type": "boolean"
            }
          },
          {
            "name": "limit",
            "in": "query",
            "schema": {
              "type": "integer"
            }
          },
          {
            "name": "X-Trace",
            "in": "header",
            "schema": {
              "type": "string"
            }
          },
          {
            "name": "sess",
            "in": "cookie",
            "schema": {
              "type": "string"
            }
          }
        ],
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "type": "object"
              }
            }
          }
        }
      }
    },
    "/ok": {
      "get": {
        "operationId": "ok",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        }
      }
    },
    "/reject": {
      "get": {
        "operationId": "reject",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        }
      }
    },
    "/large": {
      "get": {
        "operationId": "large",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        }
      }
    },
    "/exact-limit": {
      "get": {
        "operationId": "exact_limit",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        }
      }
    },
    "/broken": {
      "get": {
        "operationId": "broken",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        }
      }
    },
    "/wait": {
      "get": {
        "operationId": "wait",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        }
      }
    },
    "/wait-body": {
      "get": {
        "operationId": "wait_body",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        }
      }
    },
    "/redirect": {
      "get": {
        "operationId": "redirect",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        }
      }
    },
    "/landing": {
      "get": {
        "operationId": "landing",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        }
      }
    },
    "/cookie-set": {
      "get": {
        "operationId": "cookie_set",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        }
      }
    },
    "/cookie-check": {
      "get": {
        "operationId": "cookie_check",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        }
      }
    },
    "/once": {
      "get": {
        "operationId": "once",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        }
      }
    },
    "/numeric": {
      "get": {
        "operationId": "numeric",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        }
      }
    },
    "/bad-type": {
      "get": {
        "operationId": "bad_type",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        }
      }
    },
    "/custom-type": {
      "get": {
        "operationId": "custom_type",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        }
      }
    },
    "/query-secret": {
      "get": {
        "operationId": "query_secret",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        },
        "security": [
          {
            "queryKey": []
          }
        ]
      }
    },
    "/header-key": {
      "get": {
        "operationId": "header_key",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        },
        "security": [
          {
            "headerKey": []
          }
        ]
      }
    },
    "/basic": {
      "get": {
        "operationId": "basic",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        },
        "security": [
          {
            "basicAuth": []
          }
        ]
      }
    },
    "/cookie-key": {
      "get": {
        "operationId": "cookie_key",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        },
        "security": [
          {
            "cookieKey": []
          }
        ]
      }
    },
    "/exact": {
      "post": {
        "operationId": "sendExact",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        },
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {}
            }
          }
        }
      }
    },
    "/target/{id}": {
      "get": {
        "operationId": "normalizedTarget",
        "responses": {
          "200": {
            "description": "Independent finite response",
            "content": {
              "application/json": {
                "schema": {}
              }
            }
          },
          "422": {
            "description": "Independent finite response",
            "content": {
              "application/problem+json": {
                "schema": {}
              }
            }
          }
        },
        "parameters": [
          {
            "name": "id",
            "in": "path",
            "required": true,
            "schema": {
              "type": "string"
            }
          }
        ]
      }
    }
  },
  "components": {
    "securitySchemes": {
      "bearerAuth": {
        "type": "http",
        "scheme": "bearer"
      },
      "basicAuth": {
        "type": "http",
        "scheme": "basic"
      },
      "queryKey": {
        "type": "apiKey",
        "in": "query",
        "name": "api_key"
      },
      "headerKey": {
        "type": "apiKey",
        "in": "header",
        "name": "X-API-Key"
      },
      "cookieKey": {
        "type": "apiKey",
        "in": "cookie",
        "name": "sol_cookie"
      }
    }
  }
}
"###;
const WANTED: &str = r###"{
  "author": "Independent GPT-6.1 Sol; expected values authored without adapter calls or candidate output",
  "origin": "http://127.0.0.1:{port}",
  "wireHeaderComparison": "ASCII case-insensitive field names; exact bytes and value order within duplicate fields; no claim about global field order",
  "ordinary": {
    "method": "POST",
    "target": "/v1/items/A%2FB%20%3F%23%E9%9B%AA?tag=red%20blue&tag=x%2Fy&flag=true&limit=9007199254740993",
    "headers": {
      "x-trace": [
        "trace-A/42"
      ],
      "x-multi": [
        "first",
        "second"
      ],
      "authorization": [
        "Bearer bearer-secret-B11"
      ],
      "cookie": [
        "sess=s%2Fa"
      ],
      "content-type": [
        "application/json"
      ]
    },
    "bodyFixture": "ordinary-request.json",
    "responseFixture": "success.json",
    "status": 200,
    "typedSuccess": {
      "id": 9007199254740993,
      "name": "accepted",
      "ok": true
    },
    "nonemptyUploadEvidence": "Unknown unless new independently justified backend completion evidence exists"
  },
  "auth": {
    "query": {
      "target": "/v1/query-secret?api_key=query-secret-Q22",
      "credential": "query-secret-Q22"
    },
    "header": {
      "target": "/v1/header-key",
      "x-api-key": "header-secret-H33"
    },
    "cookie": {
      "target": "/v1/cookie-key",
      "cookie": "sol_cookie=cookie-secret-K44"
    },
    "basic": {
      "target": "/v1/basic",
      "username": "user",
      "password": "päss",
      "authorization": "Basic dXNlcjpww6Rzcw=="
    },
    "credentialOriginMismatch": {
      "networkRequests": 0,
      "preparationCode": "CredentialOrigin"
    }
  },
  "exactForwarding": {
    "method": "POST",
    "target": "/v1/exact",
    "bodyFixture": "exact-request.json",
    "content-type": "application/json"
  },
  "httpError": {
    "target": "/v1/reject",
    "status": 422,
    "content-type": "application/problem+json",
    "bodyFixture": "problem.json",
    "decode": "success",
    "policyPrimary": "HttpStatus",
    "responseAndDecodedOwnerRetained": true
  },
  "limits": {
    "exactLimitBytes": 33,
    "exactFixture": "exact-limit.json",
    "overLimitBytes": 32,
    "overFixture": "large.json",
    "overRawPrefix": "{\"value\":\"xxxxxxxxxxxxxxxxxxxxxx",
    "overBodyState": "Truncated",
    "overJsonCode": "IncompleteBody",
    "brokenFixture": "broken-prefix.bin",
    "declaredLength": 100,
    "brokenBodyState": "Failed",
    "brokenJsonCode": "IncompleteBody",
    "lateFailureKeepsStatusAndPrefix": true
  },
  "cancellation": {
    "serverObservedFullRequestBeforeCancel": true,
    "beforeHeadersResponse": null,
    "outcomeCancelled": true,
    "policyPrimary": "Cancelled",
    "remoteUndoClaim": false,
    "functionalDeadlineSeconds": 2,
    "deadlineIsNotPerformanceMeasurement": true,
    "bodyCancellation": "If response evidence is retained, status and bounded delivered prefix remain honest; no invented completion"
  },
  "redirect": {
    "status": 302,
    "location": "{origin}/v1/landing",
    "bodyFixture": "success.json",
    "landingRequests": 0,
    "responseRetained": true,
    "policyPrimary": "HttpStatus"
  },
  "reuse": {
    "setCookie": "ambient-secret-cookie=server-value",
    "followupCookieHeaderAbsent": true,
    "executeDoesNotInjectConfiguredCredentialsIntoSeparatelyPreparedPublicRequest": true,
    "disconnectAfterOneObservedRequestTotalAttempts": 1
  },
  "targets": {
    "allowed": [
      {
        "input": "%2e",
        "wire": "/v1/target/%252e"
      },
      {
        "input": "%",
        "wire": "/v1/target/%25"
      },
      {
        "input": "A/B",
        "wire": "/v1/target/A%2FB"
      }
    ],
    "normalizationRefusals": [
      "literal . or .. final path segment",
      "percent-encoded whole dot segment",
      "noncanonical IPv4 spelling",
      "uppercase scheme/host if backend serialization changes spelling",
      "explicit default port if backend drops it"
    ],
    "refusalMustPrecedeNetworkIO": true,
    "preparedTargetRemainsUnchanged": true,
    "dispatchMeaning": "Core callback entry is independent from socket/server receipt"
  },
  "typedProjection": {
    "exactSafeToken": "9007199254740993",
    "u64": 9007199254740993,
    "explicitF64Projection": 9007199254740992.0,
    "checkedF64ExactReason": "PrecisionLoss",
    "u128Max": "340282366920938463463374607431768211455",
    "i128Min": "-170141183460469231731687303715884105728",
    "u64OverflowRefuses": true,
    "originalTokensRemainUnchanged": true,
    "borrowedStrings": "Allowed while owner lives; no DeserializeOwned-only bound",
    "customDeserializerTextIsPrivate": "typed-secret-D99",
    "formattingDoesNotExposeVariantOrFieldOrCustomText": true
  },
  "secrets": [
    "bearer-secret-B11",
    "query-secret-Q22",
    "header-secret-H33",
    "cookie-secret-K44",
    "päss",
    "response-secret-R4",
    "variant-secret-V77",
    "unexpected-secret-field",
    "custom-secret-C88",
    "typed-secret-D99"
  ],
  "contentCoding": {
    "fixture": "encoded-success.gzip",
    "coding": "gzip",
    "baselineCoreWireJsonRefusal": "UnsupportedCoding",
    "requirement": "Adapter must declare delivered provenance accurately; codec policy is reviewed before coupled caller code"
  }
}
"###;
fn fixture(name: &str) -> Vec<u8> {
    match name {
        "bad-type.json" => b"\x7b\x22\x6b\x69\x6e\x64\x22\x3a\x22\x76\x61\x72\x69\x61\x6e\x74\x2d\x73\x65\x63\x72\x65\x74\x2d\x56\x37\x37\x22\x2c\x22\x75\x6e\x65\x78\x70\x65\x63\x74\x65\x64\x2d\x73\x65\x63\x72\x65\x74\x2d\x66\x69\x65\x6c\x64\x22\x3a\x74\x72\x75\x65\x7d".to_vec(),
        "broken-prefix.bin" => b"\x7b\x22\x69\x6e\x63\x6f\x6d\x70\x6c\x65\x74\x65\x22\x3a".to_vec(),
        "custom-type.json" => b"\x7b\x22\x69\x6e\x70\x75\x74\x22\x3a\x22\x63\x75\x73\x74\x6f\x6d\x2d\x73\x65\x63\x72\x65\x74\x2d\x43\x38\x38\x22\x7d".to_vec(),
        "encoded-success.gzip" => b"\x1f\x8b\x08\x00\x00\x00\x00\x00\x02\xff\xab\x56\xca\x4c\x51\xb2\xb2\x34\x30\x30\x37\xb4\xb4\x34\x32\x35\x31\x37\x31\xb0\xb4\x34\xd6\x51\xca\x4b\xcc\x4d\x55\xb2\x52\x4a\x4c\x4e\x4e\x2d\x28\x49\x4d\x51\xd2\x51\xca\xcf\x56\xb2\x2a\x29\x2a\x4d\xad\x05\x00\xfa\x04\xb9\x22\x33\x00\x00\x00".to_vec(),
        "exact-limit.json" => b"\x7b\x22\x76\x61\x6c\x75\x65\x22\x3a\x22\x30\x31\x32\x33\x34\x35\x36\x37\x38\x39\x30\x31\x32\x33\x34\x35\x36\x37\x38\x39\x30\x22\x7d".to_vec(),
        "exact-request.json" => b"\x7b\x22\x77\x69\x64\x65\x22\x3a\x33\x34\x30\x32\x38\x32\x33\x36\x36\x39\x32\x30\x39\x33\x38\x34\x36\x33\x34\x36\x33\x33\x37\x34\x36\x30\x37\x34\x33\x31\x37\x36\x38\x32\x31\x31\x34\x35\x35\x2c\x22\x74\x69\x6e\x79\x22\x3a\x31\x2e\x32\x33\x30\x30\x65\x2d\x34\x30\x30\x2c\x22\x68\x75\x67\x65\x22\x3a\x31\x2e\x32\x33\x30\x30\x65\x2b\x34\x30\x30\x2c\x22\x6e\x65\x67\x22\x3a\x2d\x30\x7d".to_vec(),
        "large.json" => b"\x7b\x22\x76\x61\x6c\x75\x65\x22\x3a\x22\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x78\x22\x7d".to_vec(),
        "numeric.json" => b"\x7b\x22\x73\x61\x66\x65\x22\x3a\x39\x30\x30\x37\x31\x39\x39\x32\x35\x34\x37\x34\x30\x39\x39\x33\x2c\x22\x77\x69\x64\x65\x22\x3a\x33\x34\x30\x32\x38\x32\x33\x36\x36\x39\x32\x30\x39\x33\x38\x34\x36\x33\x34\x36\x33\x33\x37\x34\x36\x30\x37\x34\x33\x31\x37\x36\x38\x32\x31\x31\x34\x35\x35\x2c\x22\x6d\x69\x6e\x22\x3a\x2d\x31\x37\x30\x31\x34\x31\x31\x38\x33\x34\x36\x30\x34\x36\x39\x32\x33\x31\x37\x33\x31\x36\x38\x37\x33\x30\x33\x37\x31\x35\x38\x38\x34\x31\x30\x35\x37\x32\x38\x2c\x22\x6f\x76\x65\x72\x66\x6c\x6f\x77\x22\x3a\x31\x38\x34\x34\x36\x37\x34\x34\x30\x37\x33\x37\x30\x39\x35\x35\x31\x36\x31\x36\x2c\x22\x66\x72\x61\x63\x74\x69\x6f\x6e\x22\x3a\x31\x2e\x32\x33\x30\x30\x2c\x22\x68\x75\x67\x65\x22\x3a\x31\x65\x34\x30\x30\x2c\x22\x6e\x65\x67\x22\x3a\x2d\x30\x7d".to_vec(),
        "ordinary-request.json" => b"\x7b\x22\x6e\x61\x6d\x65\x22\x3a\x22\xe9\x9b\xaa\x22\x2c\x22\x69\x64\x22\x3a\x39\x30\x30\x37\x31\x39\x39\x32\x35\x34\x37\x34\x30\x39\x39\x33\x2c\x22\x61\x63\x74\x69\x76\x65\x22\x3a\x74\x72\x75\x65\x2c\x22\x74\x61\x67\x73\x22\x3a\x5b\x22\x72\x65\x64\x22\x2c\x22\x78\x2f\x79\x22\x5d\x7d".to_vec(),
        "pending-prefix.bin" => b"\x7b\x22\x77\x61\x69\x74\x69\x6e\x67\x22\x3a".to_vec(),
        "problem.json" => b"\x7b\x22\x74\x79\x70\x65\x22\x3a\x22\x61\x62\x6f\x75\x74\x3a\x62\x6c\x61\x6e\x6b\x22\x2c\x22\x74\x69\x74\x6c\x65\x22\x3a\x22\x44\x65\x6e\x69\x65\x64\x22\x2c\x22\x64\x65\x74\x61\x69\x6c\x22\x3a\x22\x72\x65\x73\x70\x6f\x6e\x73\x65\x2d\x73\x65\x63\x72\x65\x74\x2d\x52\x34\x22\x2c\x22\x65\x78\x61\x63\x74\x22\x3a\x39\x30\x30\x37\x31\x39\x39\x32\x35\x34\x37\x34\x30\x39\x39\x33\x7d".to_vec(),
        "success.json" => b"\x7b\x22\x69\x64\x22\x3a\x39\x30\x30\x37\x31\x39\x39\x32\x35\x34\x37\x34\x30\x39\x39\x33\x2c\x22\x6e\x61\x6d\x65\x22\x3a\x22\x61\x63\x63\x65\x70\x74\x65\x64\x22\x2c\x22\x6f\x6b\x22\x3a\x74\x72\x75\x65\x7d".to_vec(),
        _ => panic!("unknown independent fixture"),
    }
}
fn document(body_bytes: usize) -> Document {
    Document::parse(
        SPEC,
        None,
        Limits {
            body_bytes,
            ..Limits::default()
        },
    )
    .unwrap()
}
fn request(client: &Client, doc: &Document, id: &str, server: &Server) -> PreparedRequest {
    client
        .request(&doc.operation_id(id).unwrap())
        .server_variable("port", server.port.to_string())
        .prepare()
        .unwrap()
}
fn credential(value: CredentialValue, server: &Server) -> Credential {
    Credential {
        value,
        origins: vec![server.origin()],
    }
}
fn configured(server: &Server) -> Client {
    Client::builder()
        .credential(
            "bearerAuth",
            credential(CredentialValue::Bearer("bearer-secret-B11".into()), server),
        )
        .credential(
            "basicAuth",
            credential(
                CredentialValue::Basic {
                    username: "user".into(),
                    password: "päss".into(),
                },
                server,
            ),
        )
        .credential(
            "queryKey",
            credential(CredentialValue::ApiKey("query-secret-Q22".into()), server),
        )
        .credential(
            "headerKey",
            credential(CredentialValue::ApiKey("header-secret-H33".into()), server),
        )
        .credential(
            "cookieKey",
            credential(CredentialValue::ApiKey("cookie-secret-K44".into()), server),
        )
        .build()
        .unwrap()
}
async fn execute(client: &Client, request: &PreparedRequest) -> Outcome {
    tokio::time::timeout(
        Duration::from_secs(2),
        client.execute(request, Cancellation::default()),
    )
    .await
    .expect("functional execution deadline")
}
fn safe_text(text: &str) {
    let expected: Json = serde_json::from_str(WANTED).unwrap();
    for secret in expected["secrets"].as_array().unwrap() {
        assert!(
            !text.contains(secret.as_str().unwrap()),
            "default formatting exposed synthetic secret"
        );
    }
}
fn safe_error_chain(mut error: &(dyn std::error::Error + 'static)) {
    for _ in 0..16 {
        safe_text(&format!("{error}"));
        safe_text(&format!("{error:?}"));
        if let Some(next) = error.source() {
            error = next;
        } else {
            return;
        }
    }
    panic!("unexpected cyclic/deep error chain");
}
fn response(outcome: &Outcome, status: u16, raw: &[u8], state: BodyState) {
    assert_eq!(outcome.dispatch, DispatchEvidence::Dispatched);
    assert_eq!(outcome.upload, UploadState::Unknown);
    let value = outcome
        .response
        .as_ref()
        .expect("retained response evidence");
    assert_eq!(value.status(), status);
    assert_eq!(value.raw(), raw);
    assert_eq!(value.body_state(), state);
    assert_eq!(value.provenance(), Provenance::Wire);
    assert_eq!(
        value
            .headers()
            .iter()
            .filter(|h| h.name().eq_ignore_ascii_case("x-duplicate"))
            .map(|h| h.value())
            .collect::<Vec<_>>(),
        vec![b"first".as_slice(), b"second".as_slice()]
    );
    safe_text(&format!("{outcome:?}"));
    safe_text(&format!("{value:?}"));
}
fn wire_observation(wire: &Wire) -> Json {
    json!({"method":wire.method,"target":wire.target,"version":wire.version,"fields":wire.normalized_fields(),"body":wire.body})
}
fn send_future<T: Future + Send>(future: T) -> T {
    future
}

fn cancellation_probes() {
    struct Count(AtomicUsize);
    impl Wake for Count {
        fn wake(self: Arc<Self>) {
            self.0.fetch_add(1, Ordering::Relaxed);
        }
    }
    let token = Cancellation::default();
    let first = Arc::new(Count(AtomicUsize::new(0)));
    let second = Arc::new(Count(AtomicUsize::new(0)));
    let dropped = Arc::new(Count(AtomicUsize::new(0)));
    let (w1, w2, w3) = (
        Waker::from(first.clone()),
        Waker::from(second.clone()),
        Waker::from(dropped.clone()),
    );
    let mut a = Box::pin(token.cancelled());
    let mut b = Box::pin(token.cancelled());
    let mut c = Box::pin(token.cancelled());
    assert!(a.as_mut().poll(&mut Context::from_waker(&w1)).is_pending());
    assert!(b.as_mut().poll(&mut Context::from_waker(&w2)).is_pending());
    assert!(c.as_mut().poll(&mut Context::from_waker(&w3)).is_pending());
    drop(c);
    token.clone().cancel();
    assert_eq!(first.0.load(Ordering::Relaxed), 1);
    assert_eq!(second.0.load(Ordering::Relaxed), 1);
    assert_eq!(dropped.0.load(Ordering::Relaxed), 0);
    assert!(a.as_mut().poll(&mut Context::from_waker(&w1)).is_ready());
    assert!(b.as_mut().poll(&mut Context::from_waker(&w2)).is_ready());
    token.cancel();
    assert_eq!(first.0.load(Ordering::Relaxed), 1);
    assert!(
        Box::pin(token.cancelled())
            .as_mut()
            .poll(&mut Context::from_waker(&w1))
            .is_ready()
    );

    // Safe public Wake destructor reentry. Replacing the stored sole waker must
    // drop it outside locks. A deadline guards the previously identified deadlock.
    struct CancelOnDrop(Cancellation);
    impl Drop for CancelOnDrop {
        fn drop(&mut self) {
            self.0.cancel();
        }
    }
    impl Wake for CancelOnDrop {
        fn wake(self: Arc<Self>) {}
    }
    let (tx, rx) = mpsc::channel();
    std::thread::spawn(move || {
        let token = Cancellation::default();
        let mut future = Box::pin(token.cancelled());
        let old = Waker::from(Arc::new(CancelOnDrop(token.clone())));
        assert!(
            future
                .as_mut()
                .poll(&mut Context::from_waker(&old))
                .is_pending()
        );
        drop(old); // Only the registration now retains this Wake object's Arc.
        let new = Waker::from(Arc::new(Count(AtomicUsize::new(0))));
        assert!(
            future
                .as_mut()
                .poll(&mut Context::from_waker(&new))
                .is_ready()
        );
        assert!(token.is_cancelled());
        tx.send(()).unwrap();
    });
    rx.recv_timeout(Duration::from_secs(2))
        .expect("safe waker-drop reentrancy must not deadlock");
}

#[derive(Deserialize, Debug, PartialEq)]
struct Success {
    id: u64,
    name: String,
    ok: bool,
}
#[derive(Deserialize)]
struct Borrowed<'a> {
    name: &'a str,
}
#[derive(Serialize)]
struct Payload<'a> {
    name: &'a str,
    id: u64,
    active: bool,
    tags: [&'a str; 2],
}

#[tokio::main(flavor = "multi_thread", worker_threads = 2)]
async fn main() {
    cancellation_probes();
    let expected: Json = serde_json::from_str(WANTED).unwrap();
    let server = Server::start();
    let client = configured(&server);
    let doc = document(4 * 1024 * 1024);
    safe_text(&format!("{client:?}"));
    let mut observations =
        vec![json!({"case":"cancellation-waiters-and-safe-waker-drop","passed":true})];

    let payload = Payload {
        name: "雪",
        id: 9_007_199_254_740_993,
        active: true,
        tags: ["red", "x/y"],
    };
    let owner = ExactJson::from_serializable(&payload, Limits::default()).unwrap();
    assert_eq!(owner.source().as_bytes(), fixture("ordinary-request.json"));
    let tags = [
        OrdinaryValue::String("red blue"),
        OrdinaryValue::String("x/y"),
    ];
    let operation = doc.operation_id("createItem").unwrap();
    let prepared = client
        .request(&operation)
        .server_variable("port", server.port.to_string())
        .parameter(ParameterLocation::Path, "id", "A/B ?#雪")
        .parameter(ParameterLocation::Query, "tag", OrdinaryValue::Array(&tags))
        .parameter(ParameterLocation::Query, "flag", true)
        .parameter(ParameterLocation::Query, "limit", 9_007_199_254_740_993_u64)
        .parameter(ParameterLocation::Header, "X-Trace", "trace-A/42")
        .parameter(ParameterLocation::Cookie, "sess", "s/a")
        .header(Header::new("X-Multi", "first"))
        .header(Header::new("x-multi", "second"))
        .json(owner.root())
        .prepare()
        .unwrap();
    safe_text(&format!("{prepared:?}"));
    let outcome = execute(&client, &prepared).await;
    response(&outcome, 200, &fixture("success.json"), BodyState::Complete);
    assert!(outcome.error.is_none());
    let strict = policies::complete_2xx_json(outcome.clone()).unwrap_err();
    assert_eq!(
        strict.reason,
        policies::CompleteJsonRefusal::UploadUncertain
    );
    safe_error_chain(&*strict);
    let accepted = policies::received_2xx_json(outcome).unwrap();
    let success: Success = accepted.json.deserialize().unwrap();
    assert_eq!(
        success,
        Success {
            id: 9_007_199_254_740_993,
            name: "accepted".into(),
            ok: true
        }
    );
    let borrowed: Borrowed<'_> = accepted.json.deserialize().unwrap();
    assert_eq!(borrowed.name, "accepted");
    assert!(
        (accepted.json.source().as_ptr() as usize
            ..accepted.json.source().as_ptr() as usize + accepted.json.source().len())
            .contains(&(borrowed.name.as_ptr() as usize))
    );
    assert_eq!(
        accepted
            .json
            .root()
            .get("id")
            .unwrap()
            .number()
            .unwrap()
            .token(),
        "9007199254740993"
    );
    let wanted = &expected["ordinary"];
    let wire = server.observed(wanted["target"].as_str().unwrap()).await;
    assert_eq!(wire.method, "POST");
    assert_eq!(wire.version, "HTTP/1.1");
    assert_eq!(wire.body.as_bytes(), fixture("ordinary-request.json"));
    for (name, values) in wanted["headers"].as_object().unwrap() {
        assert_eq!(
            wire.values(name),
            values
                .as_array()
                .unwrap()
                .iter()
                .map(|v| v.as_str().unwrap())
                .collect::<Vec<_>>()
        );
    }
    assert_eq!(
        wire.values("host"),
        vec![format!("127.0.0.1:{}", server.port).as_str()]
    );
    assert_eq!(wire.values("content-length"), vec!["71"]);
    observations.push(json!({"case":"ordinary-configured-real-post-and-typed-success","wire":wire_observation(&wire),"strictPolicy":"UploadUncertain","receivedPolicy":"accepted"}));

    let exact = ExactJson::parse(fixture("exact-request.json"), Limits::default()).unwrap();
    let exact_request = client
        .request(&doc.operation_id("sendExact").unwrap())
        .server_variable("port", server.port.to_string())
        .json(exact.root())
        .prepare()
        .unwrap();
    let exact_outcome = execute(&client, &exact_request).await;
    assert!(policies::received_2xx_json(exact_outcome).is_ok());
    let exact_wire = server.observed("/v1/exact").await;
    assert_eq!(exact_wire.body.as_bytes(), fixture("exact-request.json"));
    let before = server.records().len();
    let invalid = ExactJson::from_serializable(&f64::NAN, Limits::default()).unwrap_err();
    assert_eq!(
        invalid.diagnostic().reason(),
        Some(&DiagnosticReason::Serialization(
            SerializationReason::NonFiniteNumber
        ))
    );
    assert_eq!(before, server.records().len());
    safe_error_chain(&invalid);
    observations.push(json!({"case":"exact-body-and-pre-dispatch-nonfinite-refusal","wire":wire_observation(&exact_wire)}));

    for (operation, target, header, want) in [
        (
            "query_secret",
            "/v1/query-secret?api_key=query-secret-Q22",
            None,
            None,
        ),
        (
            "header_key",
            "/v1/header-key",
            Some("x-api-key"),
            Some("header-secret-H33"),
        ),
        (
            "basic",
            "/v1/basic",
            Some("authorization"),
            Some("Basic dXNlcjpww6Rzcw=="),
        ),
        (
            "cookie_key",
            "/v1/cookie-key",
            Some("cookie"),
            Some("sol_cookie=cookie-secret-K44"),
        ),
    ] {
        let prepared = request(&client, &doc, operation, &server);
        let outcome = execute(&client, &prepared).await;
        assert!(policies::received_2xx_json(outcome).is_ok());
        let wire = server.observed(target).await;
        if let Some(header) = header {
            assert_eq!(wire.values(header), vec![want.unwrap()]);
        }
        observations.push(json!({"case":"scoped-auth-variant","operation":operation,"wire":wire_observation(&wire)}));
    }
    let other = Server::start();
    let refused = client
        .request(&operation)
        .server_variable("port", other.port.to_string())
        .parameter(ParameterLocation::Path, "id", "x")
        .json(owner.root())
        .prepare()
        .unwrap_err();
    assert_eq!(refused.diagnostic().code(), Code::CredentialOrigin);
    safe_error_chain(&refused);
    assert!(other.records().is_empty());
    drop(other);
    let public = doc
        .operation_id("ok")
        .unwrap()
        .request()
        .server_variable("port", server.port.to_string())
        .prepare()
        .unwrap();
    assert!(policies::received_2xx_json(execute(&client, &public).await).is_ok());
    let public_wire = server.observed("/v1/ok").await;
    for header in ["authorization", "x-api-key", "cookie"] {
        assert!(public_wire.values(header).is_empty());
    }
    observations.push(json!({"case":"credential-origin-and-no-execute-time-auth-injection","publicWire":wire_observation(&public_wire)}));

    let outcome = execute(&client, &request(&client, &doc, "reject", &server)).await;
    response(&outcome, 422, &fixture("problem.json"), BodyState::Complete);
    let refusal = policies::received_2xx_json(outcome).unwrap_err();
    assert_eq!(refusal.reason, policies::CompleteJsonRefusal::HttpStatus);
    assert!(refusal.decode_error.is_none());
    assert_eq!(
        refusal.decoded.as_ref().unwrap().source().as_bytes(),
        fixture("problem.json")
    );
    safe_error_chain(&*refusal);
    observations.push(json!({"case":"non2xx-retains-response-and-decoded-owner","status":422,"reason":"HttpStatus"}));

    let exact_limit = document(33);
    let limit_outcome = execute(
        &client,
        &request(&client, &exact_limit, "exact_limit", &server),
    )
    .await;
    response(
        &limit_outcome,
        200,
        &fixture("exact-limit.json"),
        BodyState::Complete,
    );
    assert!(policies::received_2xx_json(limit_outcome).is_ok());
    let limited = document(32);
    let outcome = execute(&client, &request(&client, &limited, "large", &server)).await;
    response(
        &outcome,
        200,
        &fixture("large.json")[..32],
        BodyState::Truncated,
    );
    assert!(outcome.error.is_none());
    let refusal = policies::received_2xx_json(outcome).unwrap_err();
    assert_eq!(refusal.reason, policies::CompleteJsonRefusal::Decode);
    assert_eq!(
        refusal.decode_error.as_ref().unwrap().code(),
        Code::IncompleteBody
    );
    observations.push(json!({"case":"exact-bound-eof-and-delivered-overflow","exactBytes":33,"retainedOverflowBytes":32,"overflowState":"Truncated"}));
    let outcome = execute(&client, &request(&client, &doc, "broken", &server)).await;
    response(
        &outcome,
        200,
        &fixture("broken-prefix.bin"),
        BodyState::Failed,
    );
    assert_eq!(
        outcome.error.as_ref().unwrap().code(),
        Code::TransportFailure
    );
    let refusal = policies::received_2xx_json(outcome).unwrap_err();
    assert_eq!(refusal.reason, policies::CompleteJsonRefusal::Transport);
    assert_eq!(
        refusal.decode_error.as_ref().unwrap().code(),
        Code::IncompleteBody
    );
    safe_error_chain(&*refusal);
    observations.push(json!({"case":"late-body-failure-retains-prefix-and-status","status":200,"bodyState":"Failed","primary":"Transport","decode":"IncompleteBody"}));

    let before = server.records().len();
    let token = Cancellation::default();
    token.cancel();
    let pre = client.execute(&public, token).await;
    assert_eq!(pre.dispatch, DispatchEvidence::NotDispatched);
    assert_eq!(pre.upload, UploadState::NotStarted);
    assert!(pre.cancelled && pre.response.is_none());
    assert_eq!(pre.error.unwrap().code(), Code::Cancelled);
    assert_eq!(before, server.records().len());
    for (operation, path) in [("wait", "/v1/wait"), ("wait_body", "/v1/wait-body")] {
        let prepared = request(&client, &doc, operation, &server);
        let token = Cancellation::default();
        let (c, t) = (client.clone(), token.clone());
        let task = tokio::spawn(send_future(async move { c.execute(&prepared, t).await }));
        let observed = server.observed(path).await;
        assert_eq!(observed.method, "GET");
        if operation == "wait_body" {
            tokio::time::sleep(Duration::from_millis(50)).await;
        }
        token.cancel();
        let outcome = tokio::time::timeout(Duration::from_secs(2), task)
            .await
            .expect("pending cancellation functional deadline")
            .unwrap();
        server.release(path);
        assert!(outcome.cancelled);
        assert_eq!(outcome.dispatch, DispatchEvidence::Dispatched);
        assert_eq!(outcome.upload, UploadState::Unknown);
        if operation == "wait" {
            assert!(outcome.response.is_none());
        }
        if let Some(ref response) = outcome.response {
            assert_eq!(response.status(), 200);
            assert_eq!(response.body_state(), BodyState::Failed);
            assert_eq!(
                response.raw(),
                &fixture("pending-prefix.bin")[..response.raw().len()]
            );
        }
        safe_text(&format!("{outcome:?}"));
        let refusal = policies::received_2xx_json(outcome).unwrap_err();
        assert_eq!(refusal.reason, policies::CompleteJsonRefusal::Cancelled);
        safe_error_chain(&*refusal);
        observations.push(json!({"case":"pending-local-cancellation","operation":operation,"serverAlreadyObserved":true,"reason":"Cancelled","remoteUndoAsserted":false}));
    }

    let outcome = execute(&client, &request(&client, &doc, "redirect", &server)).await;
    response(&outcome, 302, &fixture("success.json"), BodyState::Complete);
    assert_eq!(
        outcome
            .response
            .as_ref()
            .unwrap()
            .headers()
            .iter()
            .find(|h| h.name().eq_ignore_ascii_case("location"))
            .unwrap()
            .value(),
        format!("{}/v1/landing", server.origin()).as_bytes()
    );
    assert_eq!(
        policies::received_2xx_json(outcome).unwrap_err().reason,
        policies::CompleteJsonRefusal::HttpStatus
    );
    assert_eq!(server.count("/v1/landing"), 0);
    for operation in ["cookie_set", "cookie_check"] {
        assert!(
            policies::received_2xx_json(
                execute(&client, &request(&client, &doc, operation, &server)).await
            )
            .is_ok()
        );
    }
    assert!(
        server
            .observed("/v1/cookie-check")
            .await
            .values("cookie")
            .is_empty()
    );
    let outcome = execute(&client, &request(&client, &doc, "once", &server)).await;
    assert_eq!(
        outcome.error.as_ref().unwrap().code(),
        Code::TransportFailure
    );
    assert!(outcome.response.is_none());
    assert_eq!(server.count("/v1/once"), 1);
    observations.push(json!({"case":"no-redirect-no-cookie-jar-no-retry","landingRequests":0,"onceRequests":1,"sameConfiguredClientReused":true}));

    let target_op = doc.operation_id("normalizedTarget").unwrap();
    for pair in expected["targets"]["allowed"].as_array().unwrap() {
        let prepared = client
            .request(&target_op)
            .server_variable("port", server.port.to_string())
            .parameter(
                ParameterLocation::Path,
                "id",
                pair["input"].as_str().unwrap(),
            )
            .prepare()
            .unwrap();
        let original = prepared.target().to_owned();
        let outcome = execute(&client, &prepared).await;
        assert!(policies::received_2xx_json(outcome).is_ok());
        assert_eq!(prepared.target(), original);
        let wire = server.observed(pair["wire"].as_str().unwrap()).await;
        observations.push(json!({"case":"exact-allowed-target","wire":wire_observation(&wire)}));
    }
    for id in [".", ".."] {
        let before = server.records().len();
        let refused = client
            .request(&target_op)
            .server_variable("port", server.port.to_string())
            .parameter(ParameterLocation::Path, "id", id)
            .prepare()
            .unwrap_err();
        assert_eq!(refused.diagnostic().code(), Code::InvalidDestination);
        assert_eq!(before, server.records().len());
    }
    for (label, spec, port) in [
        (
            "encoded-dot",
            SPEC.replace(
                "http://127.0.0.1:{port}/v1",
                "http://127.0.0.1:{port}/v1/%2e",
            ),
            server.port.to_string(),
        ),
        (
            "scheme-case",
            SPEC.replace("http://127.0.0.1:{port}", "HTTP://127.0.0.1:{port}"),
            server.port.to_string(),
        ),
        (
            "ipv4-spelling",
            SPEC.replace("127.0.0.1:{port}", "127.000.000.001:{port}"),
            server.port.to_string(),
        ),
        (
            "port-spelling",
            SPEC.to_owned(),
            format!("000{}", server.port),
        ),
    ] {
        let special = Document::parse(spec, None, Limits::default()).unwrap();
        let prepared = client
            .request(&special.operation_id("ok").unwrap())
            .server_variable("port", port)
            .prepare()
            .unwrap();
        let original = prepared.target().to_owned();
        let before = server.records().len();
        let outcome = execute(&client, &prepared).await;
        assert_eq!(outcome.dispatch, DispatchEvidence::NotDispatched);
        assert_eq!(outcome.upload, UploadState::NotStarted);
        assert_eq!(outcome.error.as_ref().unwrap().code(), Code::HostCapability);
        assert!(outcome.response.is_none());
        assert_eq!(prepared.target(), original);
        assert_eq!(before, server.records().len());
        safe_text(&format!("{outcome:?}"));
        observations.push(json!({"case":"normalizing-target-refuses-before-io","variant":label,"code":"HostCapability"}));
    }
    for name in [
        "Host",
        "Content-Length",
        "Transfer-Encoding",
        "Connection",
        "Proxy-Connection",
        "Proxy-Authorization",
        "Keep-Alive",
        "Upgrade",
        "TE",
        "Trailer",
        "Expect",
    ] {
        let prepared = client
            .request(&doc.operation_id("ok").unwrap())
            .server_variable("port", server.port.to_string())
            .header(Header::new(name, "value"))
            .prepare()
            .unwrap();
        let before = server.records().len();
        let outcome = execute(&client, &prepared).await;
        assert_eq!(outcome.dispatch, DispatchEvidence::NotDispatched);
        assert_eq!(outcome.error.as_ref().unwrap().code(), Code::HostCapability);
        assert_eq!(before, server.records().len());
    }
    observations.push(json!({"case":"transport-controlled-headers-refuse-before-io","fields":11}));

    let outcome = execute(&client, &request(&client, &doc, "numeric", &server)).await;
    let accepted = policies::received_2xx_json(outcome).unwrap();
    let source = accepted.json.source().to_owned();
    #[derive(Deserialize)]
    struct Integers {
        safe: u64,
        wide: u128,
        min: i128,
        neg: i64,
    }
    #[derive(Deserialize)]
    struct Floating {
        safe: f64,
        neg: f64,
    }
    #[derive(Deserialize, Debug)]
    struct Overflow {
        #[allow(dead_code)]
        overflow: u64,
    }
    let ints: Integers = accepted.json.deserialize().unwrap();
    assert_eq!(ints.safe, 9_007_199_254_740_993);
    assert_eq!(ints.wide, u128::MAX);
    assert_eq!(ints.min, i128::MIN);
    assert_eq!(ints.neg, 0);
    let float: Floating = accepted.json.deserialize().unwrap();
    assert_eq!(float.safe, 9_007_199_254_740_992.0);
    assert_eq!(float.neg.to_bits(), (-0.0_f64).to_bits());
    let exact = accepted
        .json
        .root()
        .get("safe")
        .unwrap()
        .number()
        .unwrap()
        .to_f64_exact()
        .unwrap_err();
    assert_eq!(
        exact.reason(),
        Some(&DiagnosticReason::Numeric {
            target: NumericTarget::F64Exact,
            reason: NumericReason::PrecisionLoss
        })
    );
    let overflow = accepted.json.deserialize::<Overflow>().unwrap_err();
    safe_error_chain(&overflow);
    assert!(std::error::Error::source(&overflow).is_none());
    assert_eq!(accepted.json.source(), source);
    assert_eq!(accepted.json.source().as_bytes(), fixture("numeric.json"));
    observations.push(json!({"case":"typed-numbers-are-explicit-projections","u128Exact":true,"i128Exact":true,"u64OverflowRefused":true,"floatingRoundingExplicit":true,"exactSourceUnchanged":true}));
    #[derive(Deserialize, Debug)]
    enum Kind {
        Good,
    }
    #[derive(Deserialize, Debug)]
    struct Variant {
        #[allow(dead_code)]
        kind: Kind,
    }
    #[derive(Deserialize, Debug)]
    #[serde(deny_unknown_fields)]
    struct Unexpected {
        #[allow(dead_code)]
        kind: serde::de::IgnoredAny,
    }
    let accepted = policies::received_2xx_json(
        execute(&client, &request(&client, &doc, "bad_type", &server)).await,
    )
    .unwrap();
    for (error, secret) in [
        (
            accepted.json.deserialize::<Variant>().unwrap_err(),
            "variant-secret-V77",
        ),
        (
            accepted.json.deserialize::<Unexpected>().unwrap_err(),
            "unexpected-secret-field",
        ),
    ] {
        safe_error_chain(&error);
        assert!(std::error::Error::source(&error).is_none());
        assert!(!error.detail().to_string().is_empty());
        assert!(error.detail().to_string().contains(secret));
    }
    #[derive(Debug)]
    struct Custom;
    impl<'de> Deserialize<'de> for Custom {
        fn deserialize<D: serde::Deserializer<'de>>(_: D) -> Result<Self, D::Error> {
            Err(serde::de::Error::custom("typed-secret-D99"))
        }
    }
    let accepted = policies::received_2xx_json(
        execute(&client, &request(&client, &doc, "custom_type", &server)).await,
    )
    .unwrap();
    let error = accepted.json.deserialize::<Custom>().unwrap_err();
    safe_error_chain(&error);
    assert!(error.detail().to_string().contains("typed-secret-D99"));
    assert!(std::error::Error::source(&error).is_none());
    observations.push(json!({"case":"typed-errors-private-by-default","explicitDetailAvailable":true,"implicitRawSourceAbsent":true}));

    // A separate independently generated document for the codec fixture leaves
    // the first source/expectation freeze intact.
    let encoded_spec = SPEC.replace("\"/ok\":", "\"/encoded\":");
    let encoded_doc = Document::parse(encoded_spec, None, Limits::default()).unwrap();
    let outcome = execute(&client, &request(&client, &encoded_doc, "ok", &server)).await;
    response(
        &outcome,
        200,
        &fixture("encoded-success.gzip"),
        BodyState::Complete,
    );
    assert_eq!(
        outcome
            .response
            .as_ref()
            .unwrap()
            .json()
            .unwrap_err()
            .code(),
        Code::UnsupportedCoding
    );
    observations.push(json!({"case":"unified-features-do-not-decode-wire-bytes","provenance":"Wire","jsonRefusal":"UnsupportedCoding"}));

    let timed = Client::builder()
        .timeout(Duration::from_millis(100))
        .build()
        .unwrap();
    let timeout_request = request(&timed, &doc, "wait", &server);
    let outcome = execute(&timed, &timeout_request).await;
    server.release("/v1/wait");
    assert!(!outcome.cancelled);
    assert_eq!(
        outcome.error.as_ref().unwrap().code(),
        Code::TransportFailure
    );
    assert!(outcome.response.is_none());
    safe_text(&format!("{outcome:?}"));
    observations.push(json!({"case":"bounded-native-timeout","cancelled":false,"response":false,"code":"TransportFailure"}));

    let records = server.finish();
    println!("{}",serde_json::to_string_pretty(&json!({"passed":true,"observations":observations,"serverObservedRequests":records.len(),"profiles":{"arbitrary_precision":cfg!(feature="arbitrary_precision"),"backend_unified":cfg!(feature="backend_unified")},"deadlinesAreFunctionalOnly":true})).unwrap());
}
