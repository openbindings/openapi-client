#[path = "support/strict_json.rs"]
mod policies;
#[path = "support/request_case.rs"]
mod request_case;
use dynamic_openapi_client::*;
use dynamic_openapi_client::{
    diagnostics::*, exact::*, graph::*, inspect::*, schema::*, transport::*,
};
use request_case::*;
use serde_json::json;

const VERSIONS: &[&str] = &[
    "2.0", "3.0.0", "3.0.1", "3.0.2", "3.0.3", "3.0.4", "3.1.0", "3.1.1", "3.1.2", "3.2.0", "3.2.1",
];

fn fixture(version: &str) -> serde_json::Value {
    if version == "2.0" {
        json!({"swagger":version,"host":"api.example","basePath":"/v1","schemes":["https"],"consumes":["application/json"],"produces":["application/json"],"paths":{"/items/{id}":{"post":{"operationId":"create","parameters":[{"name":"id","in":"path","required":true,"type":"string"},{"name":"payload","in":"body","required":true,"schema":{"type":"object"}}],"responses":{"200":{"description":"ok","schema":{"type":"object"}}}}}}})
    } else {
        json!({"openapi":version,"servers":[{"url":"https://api.example/v1"}],"paths":{"/items/{id}":{"post":{"operationId":"create","parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}}],"requestBody":{"required":true,"content":{"application/json":{"schema":{"type":"object"}}}},"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object"}}}}}}}}})
    }
}
fn document(value: &serde_json::Value) -> Document {
    Document::parse(
        value.to_string(),
        Some("https://api.example/spec/openapi.json"),
        Limits::default(),
    )
    .unwrap()
}

#[test]
fn all_published_editions_use_one_ordinary_json_consumer() {
    for version in VERSIONS {
        let doc = document(&fixture(version));
        assert_eq!(doc.version().as_deref(), Some(*version));
        let operation = doc.operation_id("create").unwrap();
        let payload = Value::parse(br#"{"integer":9007199254740993}"#, Limits::default()).unwrap();
        let request = operation
            .request()
            .parameter(ParameterLocation::Path, "id", &"A/B")
            .json_exact(&payload.clone())
            .prepare()
            .unwrap();
        assert_eq!(request.method(), Method::Post);
        assert_eq!(
            request.target(),
            "https://api.example/v1/items/A%2FB",
            "{version}"
        );
        assert_eq!(
            request.body().map(AsRef::as_ref),
            Some(br#"{"integer":9007199254740993}"#.as_slice())
        );
        assert_eq!(
            request
                .headers()
                .iter()
                .find(|h| h.name().eq_ignore_ascii_case("content-type"))
                .unwrap()
                .value(),
            b"application/json"
        );
        assert!(request.response_declaration(200).is_some());
        assert_eq!(operation.parameters().unwrap().len(), 1);
    }
}

#[test]
fn patch_compatibility_and_version_diagnostics_are_distinct() {
    for (version, edition) in [
        ("3.0.99", Edition::OpenApi30),
        ("3.1.99", Edition::OpenApi31),
        ("3.2.999999999999999999999999", Edition::OpenApi32),
    ] {
        let doc = document(&fixture(version));
        assert_eq!(doc.edition().unwrap(), edition);
        assert_eq!(doc.version().as_deref(), Some(version));
        assert!(doc.operation_id("create").unwrap().parameters().is_ok());
    }
    for (declaration, reason) in [
        (json!({}), VersionReason::Missing),
        (
            json!({"openapi":"3.1.2","swagger":"2.0"}),
            VersionReason::Conflicting,
        ),
        (json!({"openapi":3.1}), VersionReason::Malformed),
        (json!({"openapi":"3.1"}), VersionReason::Malformed),
        (json!({"openapi":"3.1.0-rc.1"}), VersionReason::Malformed),
        (json!({"openapi":"3.1.00"}), VersionReason::Malformed),
        (json!({"openapi":"3.3.0"}), VersionReason::UnsupportedFamily),
        (json!({"openapi":"2.0.0"}), VersionReason::UnsupportedFamily),
    ] {
        let doc = document(&declaration);
        let error = doc.edition().unwrap_err();
        assert_eq!(error.code(), Code::UnsupportedVersion);
        assert_eq!(error.reason(), Some(&DiagnosticReason::Version(reason)));
        assert!(error.location().is_some());
        assert!(!doc.source().is_empty());
    }
}

#[test]
fn effective_description_retains_schema_dialects_and_source_without_conversion() {
    for version in VERSIONS {
        let mut value = fixture(version);
        if version.starts_with("3.1") || version.starts_with("3.2") {
            value["jsonSchemaDialect"] = json!("https://example.test/dialect");
        }
        let doc = document(&value);
        let operation = doc.operation_id("create").unwrap();
        let desc = operation.describe().unwrap();
        assert_eq!(desc.servers[0].url, "https://api.example/v1");
        assert_eq!(desc.parameters.len(), 1);
        let body = desc.request_body.unwrap();
        assert!(body.required);
        assert_eq!(body.media[0].media_type, "application/json");
        let schema = body.media[0].schema.as_ref().unwrap();
        assert_eq!(schema.value.source_context(), doc.root().source_context());
        assert_eq!(
            dialect(schema),
            if version.starts_with("3.1") || version.starts_with("3.2") {
                Some("https://example.test/dialect")
            } else {
                None
            }
        );
        assert_eq!(desc.responses[0].media[0].media_type, "application/json");
        assert_eq!(
            body.declaration.target.get("name").is_some(),
            *version == "2.0"
        );
    }
}

#[test]
fn legacy_collections_servers_and_bodyless_calls_follow_original_declarations() {
    for (format, expected) in [
        ("csv", "a%2Cb,c%20d"),
        ("ssv", "a%2Cb%20c%20d"),
        ("tsv", "a%2Cb%09c%20d"),
        ("pipes", "a%2Cb%7Cc%20d"),
        ("multi", "a%2Cb&q=c%20d"),
    ] {
        let doc = document(
            &json!({"swagger":"2.0","basePath":"/v2","schemes":["http","https"],"paths":{"/list":{"get":{"operationId":"list","schemes":["https"],"parameters":[{"name":"q","in":"query","type":"array","items":{"type":"string"},"collectionFormat":format}],"responses":{"204":{"description":"empty"}}}}}}),
        );
        let op = doc.operation_id("list").unwrap();
        let q = Value::parse(br#"["a,b","c d"]"#, Limits::default()).unwrap();
        let request = op
            .request()
            .parameter_exact(ParameterLocation::Query, "q", &q.clone())
            .prepare()
            .unwrap();
        assert_eq!(
            request.target(),
            format!("https://api.example/v2/list?q={expected}")
        );
        assert!(request.body().map(AsRef::as_ref).is_none());
        assert_eq!(op.describe().unwrap().servers.len(), 1);
    }
    let doc = document(
        &json!({"swagger":"2.0","host":"other.example:8443","paths":{"/list":{"get":{"operationId":"list","responses":{"204":{"description":"empty"}}}}}}),
    );
    assert_eq!(
        doc.operation_id("list")
            .unwrap()
            .request()
            .prepare()
            .unwrap()
            .target(),
        "https://other.example:8443/list"
    );
}

#[test]
fn root_parameter_definitions_are_only_a_swagger_protocol_position() {
    for version in ["2.0", "3.0.4", "3.1.2", "3.2.1"] {
        let mut fixture = fixture(version);
        let parameter = if version == "2.0" {
            json!({"name":"id","in":"path","required":true,"type":"string","description":"target"})
        } else {
            json!({"name":"id","in":"path","required":true,"schema":{},"description":"target"})
        };
        fixture["parameters"] = json!({"Id":parameter});
        fixture["paths"]["/items/{id}"]["post"]["parameters"][0] =
            json!({"$ref":"#/parameters/Id","description":"use site"});
        let doc = document(&fixture);
        let result = doc.operation_id("create").unwrap().parameters();
        if version == "2.0" {
            assert!(result.is_ok());
        } else {
            assert_eq!(result.unwrap_err().code(), Code::WrongReferenceKind);
        }
    }
}

#[test]
fn local_references_respect_edition_siblings_and_legacy_body_inheritance() {
    for version in ["2.0", "3.0.4", "3.1.2", "3.2.1"] {
        let mut fixture = fixture(version);
        let parameter = if version == "2.0" {
            json!({"name":"id","in":"path","required":true,"type":"string","description":"target"})
        } else {
            json!({"name":"id","in":"path","required":true,"schema":{},"description":"target"})
        };
        let reference = if version == "2.0" {
            fixture["parameters"] = json!({"Id":parameter});
            "#/parameters/Id"
        } else {
            fixture["components"]["parameters"] = json!({"Id":parameter});
            "#/components/parameters/Id"
        };
        fixture["paths"]["/items/{id}"]["post"]["parameters"][0] =
            json!({"$ref":reference,"description":"use site"});
        let doc = document(&fixture);
        let op = doc.operation_id("create").unwrap();
        let resolved = op.parameters().unwrap().first().unwrap().resolved();
        assert_eq!(
            resolved.description.unwrap().as_str(),
            Some(if version == "2.0" || version == "3.0.4" {
                "target"
            } else {
                "use site"
            })
        );
        assert_eq!(resolved.chain.len(), 2);
    }
    let doc = document(
        &json!({"swagger":"2.0","consumes":["application/json"],"paths":{"/body":{"parameters":[{"$ref":"#/parameters/B"}],"post":{"operationId":"post","responses":{"200":{"$ref":"#/responses/R"}}}}},"parameters":{"B":{"name":"anything","in":"body","required":true,"schema":{}}},"responses":{"R":{"description":"ok","schema":{}}}}),
    );
    let op = doc.operation_id("post").unwrap();
    let input = Value::parse(b"null", Limits::default()).unwrap();
    assert_eq!(
        op.request()
            .json_exact(&input.clone())
            .prepare()
            .unwrap()
            .body()
            .map(AsRef::as_ref),
        Some(b"null".as_slice())
    );
    assert_eq!(op.parameters().unwrap().len(), 0);
}

#[test]
fn request_body_applicability_differs_between_30_and_later_editions() {
    for version in ["3.0.4", "3.1.2", "3.2.1"] {
        let doc = document(
            &json!({"openapi":version,"paths":{"/":{"get":{"operationId":"get","requestBody":{"required":true,"content":{"application/json":{}}},"responses":{"200":{"description":"ok"}}}}}}),
        );
        let operation = doc.operation_id("get").unwrap();
        let empty = operation.request().prepare();
        if version == "3.0.4" {
            assert!(empty.is_ok());
            assert!(operation.describe().unwrap().request_body.is_none());
        } else {
            assert_eq!(empty.unwrap_err().code(), Code::MissingBody);
            assert!(operation.describe().unwrap().request_body.is_some());
        }
    }
}

#[test]
fn raw_bodies_and_media_override_use_the_same_selection_api() {
    for version in VERSIONS {
        let mut fixture = fixture(version);
        if *version == "2.0" {
            fixture["paths"]["/items/{id}"]["post"]["consumes"] =
                json!(["application/octet-stream"]);
        } else {
            fixture["paths"]["/items/{id}"]["post"]["requestBody"]["content"] =
                json!({"application/octet-stream":{}});
        }
        let op = document(&fixture).operation_id("create").unwrap();
        let prepared = op
            .request()
            .parameter(ParameterLocation::Path, "id", &"one")
            .raw_body(b"a\0b".as_slice())
            .prepare()
            .unwrap();
        assert_eq!(prepared.body().map(AsRef::as_ref), Some(b"a\0b".as_slice()));
        assert_eq!(prepared.body_kind(), "raw");
        assert_eq!(
            op.describe().unwrap().request_body.unwrap().media[0].media_type,
            "application/octet-stream"
        );
    }
}

#[test]
fn normalized_security_and_scoped_credentials_work_across_editions() {
    for version in ["2.0", "3.0.4", "3.1.2", "3.2.1"] {
        for mechanism in ["basic", "apiKey", "oauth2"] {
            let mut fixture = fixture(version);
            let scheme = match mechanism {
                "basic" => {
                    if version == "2.0" {
                        json!({"type":"basic"})
                    } else {
                        json!({"type":"http","scheme":"basic"})
                    }
                }
                "apiKey" => json!({"type":"apiKey","in":"query","name":"token"}),
                _ => {
                    if version == "2.0" {
                        json!({"type":"oauth2","flow":"application","tokenUrl":"https://issuer.test/token","scopes":{"write":"Write"}})
                    } else {
                        json!({"type":"oauth2","flows":{"clientCredentials":{"tokenUrl":"https://issuer.test/token","scopes":{"write":"Write"}}}})
                    }
                }
            };
            if version == "2.0" {
                fixture["securityDefinitions"] = json!({"auth":scheme});
            } else {
                fixture["components"] = json!({"securitySchemes":{"auth":scheme}});
            }
            fixture["security"] =
                json!([{"auth":if mechanism=="oauth2"{vec!["write"]}else{vec![]}}]);
            let doc = document(&fixture);
            let op = doc.operation_id("create").unwrap();
            let desc = op.describe().unwrap();
            assert!(
                match (&desc.security[0].requirements[0].scheme, mechanism) {
                    (SecurityScheme::Basic, "basic")
                    | (SecurityScheme::ApiKey { .. }, "apiKey") => true,
                    (SecurityScheme::OAuth2 { flows }, "oauth2") =>
                        flows[0].flow == "clientCredentials",
                    _ => false,
                }
            );
            let credential = |origin: &str| match mechanism {
                "basic" => Credential::basic("user", "pass", vec![origin.into()]),
                "apiKey" => Credential::api_key("a/b", vec![origin.into()]),
                _ => Credential::bearer("access-token", vec![origin.into()]),
            };
            let payload = Value::parse(b"{}", Limits::default()).unwrap();
            let input = CaseInput {
                parameters: vec![CaseParameter {
                    location: ParameterLocation::Path,
                    name: "id".into(),
                    value: Value::parse(br#""id""#, Limits::default()).unwrap(),
                }],
                body: CaseBody::Json(payload.clone()),
                credentials: [("auth".into(), credential("https://api.example"))].into(),
                ..CaseInput::default()
            };
            let prepared = op.prepare_case(&input).unwrap();
            if mechanism == "apiKey" {
                assert!(prepared.target().ends_with("?token=a%2Fb"));
            } else {
                let header = prepared
                    .headers()
                    .iter()
                    .find(|h| h.name() == "Authorization")
                    .unwrap();
                assert_eq!(
                    header.value(),
                    if mechanism == "basic" {
                        b"Basic dXNlcjpwYXNz".as_slice()
                    } else {
                        b"Bearer access-token".as_slice()
                    }
                );
            }
            let mut denied = input;
            denied
                .credentials
                .insert("auth".into(), credential("https://elsewhere.test"));
            assert_eq!(
                op.prepare_case(&denied).unwrap_err().code(),
                Code::CredentialOrigin
            );
        }
    }
}

#[test]
fn query_and_additional_methods_remain_discoverable_and_case_sensitive() {
    let doc = document(
        &json!({"openapi":"3.2.1","paths":{"/":{"query":{"operationId":"query","responses":{"200":{"description":"ok"}}},"additionalOperations":{"COPY":{"operationId":"copy","responses":{"200":{"description":"ok"}}},"custom-METHOD":{"operationId":"custom","responses":{"200":{"description":"ok"}}}}}}}),
    );
    assert_eq!(doc.operations().count(), 3);
    for (id, method) in [
        ("query", "QUERY"),
        ("copy", "COPY"),
        ("custom", "custom-METHOD"),
    ] {
        let op = doc.operation_id(id).unwrap();
        assert_eq!(op.method().as_str(), method);
        assert_eq!(op.request().prepare().unwrap().method().as_str(), method);
        assert!(
            doc.operation("/", Method::from_token(method).unwrap())
                .is_ok()
        );
    }
    assert!(Method::from_token("BAD METHOD").is_err());
    assert!(Method::from_token("GET\r\n").is_err());
    assert_ne!(
        Method::from_token("copy").unwrap(),
        Method::from_token("COPY").unwrap()
    );
}

#[test]
fn unrelated_unsupported_parameter_does_not_disable_a_healthy_operation() {
    let doc = document(
        &json!({"openapi":"3.2.1","paths":{"/healthy":{"get":{"operationId":"healthy"}},"/advanced":{"get":{"operationId":"advanced","parameters":[{"name":"whole","in":"querystring","content":{"application/x-www-form-urlencoded":{"schema":{}}}}]}}}}),
    );
    assert!(
        doc.operation_id("healthy")
            .unwrap()
            .request()
            .prepare()
            .is_ok()
    );
    assert_eq!(
        doc.operation_id("advanced")
            .unwrap()
            .request()
            .prepare()
            .unwrap_err()
            .code(),
        Code::UnsupportedParameter
    );
    assert!(
        doc.operation_id("advanced")
            .unwrap()
            .inspect()
            .parameters
            .is_some()
    );
}

#[test]
fn additional_operations_cannot_redeclare_fixed_methods() {
    let doc = document(
        &json!({"openapi":"3.2.1","paths":{"/":{"get":{"operationId":"healthy"},"additionalOperations":{"POST":{"operationId":"invalid"}}}}}),
    );
    assert!(
        doc.operation_id("healthy")
            .unwrap()
            .request()
            .prepare()
            .is_ok()
    );
    assert_eq!(
        doc.operation_id("invalid")
            .unwrap()
            .request()
            .prepare()
            .unwrap_err()
            .code(),
        Code::InvalidDeclaration
    );
}

#[test]
fn raw_and_form_descriptions_report_field_completeness() {
    let doc = document(
        &json!({"swagger":"2.0","consumes":["multipart/form-data"],"paths":{"/":{"post":{"operationId":"upload","parameters":[{"name":"file","in":"formData","type":"file","required":true}]}}}}),
    );
    let body = doc
        .operation_id("upload")
        .unwrap()
        .describe()
        .unwrap()
        .request_body
        .unwrap();
    let BodyFields::Declared { fields, .. } = &body.media[0].fields else {
        panic!("explicit logical form fields expected")
    };
    assert_eq!(fields[0].name, "file");
    assert_eq!(fields[0].requirement, FieldRequirement::ProtocolRequired);
    assert_eq!(
        fields[0].schemas[0].value.get("type").unwrap().as_str(),
        Some("file")
    );
    let doc = document(
        &json!({"openapi":"3.2.1","paths":{"/":{"post":{"operationId":"upload","requestBody":{"content":{"multipart/form-data":{"schema":{"$ref":"external.json"}}}}}}}}),
    );
    let error = doc.operation_id("upload").unwrap().describe().unwrap_err();
    assert_eq!(error.code(), Code::ResourceUnavailable);
    assert_eq!(
        error.resource_need().unwrap().kind(),
        ResourceNeedKind::Bytes
    );
}

#[test]
fn media_type_references_are_32_positions_and_schemas_remain_opaque() {
    let doc = document(
        &json!({"openapi":"3.2.1","components":{"mediaTypes":{"Json":{"schema":{"$ref":"https://outside.invalid/schema"}}}},"paths":{"/":{"post":{"operationId":"post","requestBody":{"content":{"application/json":{"$ref":"#/components/mediaTypes/Json"}}}}}}}),
    );
    let op = doc.operation_id("post").unwrap();
    let body = op.describe().unwrap().request_body.unwrap();
    assert_eq!(
        body.media[0]
            .schema
            .as_ref()
            .unwrap()
            .value
            .get("$ref")
            .unwrap()
            .as_str(),
        Some("https://outside.invalid/schema")
    );
    let value = Value::parse(b"{}", Limits::default()).unwrap();
    assert_eq!(
        op.request()
            .json_exact(&value.clone())
            .prepare()
            .unwrap()
            .body()
            .map(AsRef::as_ref),
        Some(b"{}".as_slice())
    );
}

#[test]
fn actual_loopback_wire_uses_one_consumer_for_all_editions_and_new_methods() {
    use std::io::{Read, Write};
    use std::net::{TcpListener, TcpStream};
    use std::task::{Context, Poll, Waker};
    use std::time::Duration;
    fn ready<F: std::future::Future>(future: F) -> F::Output {
        let mut future = Box::pin(future);
        match future
            .as_mut()
            .poll(&mut Context::from_waker(Waker::noop()))
        {
            Poll::Ready(value) => value,
            Poll::Pending => panic!("synchronous test callback unexpectedly pending"),
        }
    }
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let address = listener.local_addr().unwrap();
    let server = std::thread::spawn(move || {
        let mut received = vec![];
        for _ in 0..VERSIONS.len() + 3 {
            let (mut stream, _) = listener.accept().unwrap();
            stream
                .set_read_timeout(Some(Duration::from_secs(5)))
                .unwrap();
            let mut bytes = vec![];
            let mut buf = [0u8; 1024];
            loop {
                let count = stream.read(&mut buf).unwrap();
                assert!(count > 0);
                bytes.extend_from_slice(&buf[..count]);
                if let Some(end) = bytes.windows(4).position(|v| v == b"\r\n\r\n") {
                    let text = std::str::from_utf8(&bytes[..end]).unwrap();
                    let size = text
                        .lines()
                        .find_map(|line| line.strip_prefix("Content-Length: "))
                        .unwrap()
                        .parse::<usize>()
                        .unwrap();
                    if bytes.len() >= end + 4 + size {
                        break;
                    }
                }
            }
            received.push(bytes);
            stream.write_all(b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 11\r\nConnection: close\r\n\r\n{\"ok\":true}").unwrap();
        }
        received
    });
    let mut docs = vec![];
    for version in VERSIONS {
        let mut value = fixture(version);
        if *version == "2.0" {
            value["host"] = json!(address.to_string());
            value["schemes"] = json!(["http"]);
        } else {
            value["servers"] = json!([{"url":format!("http://{address}/v1")}]);
        }
        docs.push((document(&value), "create"));
    }
    for (field, method) in [
        ("query", "QUERY"),
        ("copy", "COPY"),
        ("custom", "custom-METHOD"),
    ] {
        let mut value = json!({"openapi":"3.2.1","servers":[{"url":format!("http://{address}/v1")}],"paths":{"/items/A%2FB":{}}});
        let operation =
            json!({"operationId":"create","requestBody":{"content":{"application/json":{}}}});
        if field == "query" {
            value["paths"]["/items/A%2FB"]["query"] = operation;
        } else {
            value["paths"]["/items/A%2FB"]["additionalOperations"] = json!({method:operation});
        }
        docs.push((document(&value), "create"));
    }
    for (index, (doc, id)) in docs.into_iter().enumerate() {
        let operation = doc.operation_id(id).unwrap();
        let payload = Value::parse(br#"{"integer":9007199254740993}"#, Limits::default()).unwrap();
        let builder = operation.request().json_exact(&payload.clone());
        let request = if index < VERSIONS.len() {
            builder
                .parameter(ParameterLocation::Path, "id", &"A/B")
                .prepare()
                .unwrap()
        } else {
            builder.prepare().unwrap()
        };
        let outcome = ready(request.invoke(
            Cancellation::default(),
            &HostCapabilities::programmable(),
            |request, _| async move {
                let mut stream = TcpStream::connect(address).unwrap();
                stream
                    .set_read_timeout(Some(Duration::from_secs(5)))
                    .unwrap();
                let path = request
                    .target()
                    .split_once("://")
                    .unwrap()
                    .1
                    .split_once('/')
                    .unwrap()
                    .1;
                write!(
                    stream,
                    "{} /{} HTTP/1.1\r\nHost: {}\r\nContent-Length: {}\r\n",
                    request.method().as_str(),
                    path,
                    address,
                    request.body().map_or(0, |b| b.len())
                )
                .unwrap();
                for header in request.headers() {
                    write!(stream, "{}: ", header.name()).unwrap();
                    stream.write_all(header.value()).unwrap();
                    stream.write_all(b"\r\n").unwrap();
                }
                stream.write_all(b"\r\n").unwrap();
                if let Some(body) = request.body() {
                    stream.write_all(body).unwrap();
                }
                let mut response = vec![];
                stream.read_to_end(&mut response).unwrap();
                let end = response.windows(4).position(|v| v == b"\r\n\r\n").unwrap();
                assert!(response.starts_with(b"HTTP/1.1 200 OK\r\n"));
                TransportResult {
                    response: Some(ResponseInput::new(
                        200,
                        vec![Header::new("Content-Type", "application/json")],
                        response[end + 4..].into(),
                        BodyState::Complete,
                        Provenance::Wire,
                    )),
                    upload: UploadState::Complete,
                    error_detail: None,
                }
            },
        ));
        assert!(policies::complete_2xx_json(outcome).is_ok());
    }
    for (index, wire) in server.join().unwrap().into_iter().enumerate() {
        let method = if index < VERSIONS.len() {
            "POST"
        } else {
            ["QUERY", "COPY", "custom-METHOD"][index - VERSIONS.len()]
        };
        assert!(wire.starts_with(format!("{method} /v1/items/A%2FB HTTP/1.1\r\n").as_bytes()));
        assert!(
            wire.windows(b"Content-Type: application/json\r\n".len())
                .any(|v| v == b"Content-Type: application/json\r\n")
        );
        assert!(wire.ends_with(br#"{"integer":9007199254740993}"#));
    }
}

#[test]
fn malformed_media_keys_are_excluded_before_values_in_effective_descriptions() {
    for version in ["3.0.4", "3.1.2", "3.2.1"] {
        let doc = document(&json!({"openapi":version,"paths":{"/":{"post":{
            "operationId":"post",
            "requestBody":{"content":{"application/json;broken":false,"*/*":{}}},
            "responses":{"200":{"description":"ok","content":{"application/json;broken":false,"*/*":{}}}}
        }}}}));
        let operation = doc.operation_id("post").unwrap();
        let payload = Value::parse(b"null", Limits::default()).unwrap();
        let prepared = operation
            .request()
            .json_exact(&payload.clone())
            .media("application/json")
            .prepare()
            .unwrap();
        assert_eq!(prepared.body().map(AsRef::as_ref), Some(b"null".as_slice()));
        assert_eq!(
            prepared
                .headers()
                .iter()
                .find(|header| header.name().eq_ignore_ascii_case("content-type"))
                .unwrap()
                .value(),
            b"application/json"
        );
        let description = operation.describe().unwrap();
        let body = description.request_body.unwrap();
        assert_eq!(
            body.media
                .iter()
                .map(|m| m.media_type.as_str())
                .collect::<Vec<_>>(),
            ["*/*"]
        );
        assert_eq!(
            description.responses[0]
                .media
                .iter()
                .map(|m| m.media_type.as_str())
                .collect::<Vec<_>>(),
            ["*/*"]
        );
        assert_eq!(
            body.declaration
                .target
                .get("content")
                .unwrap()
                .get("application/json;broken")
                .unwrap()
                .as_bool(),
            Some(false)
        );
        assert_eq!(
            operation
                .inspect()
                .request_body
                .unwrap()
                .get("content")
                .unwrap()
                .get("application/json;broken")
                .unwrap()
                .as_bool(),
            Some(false)
        );
    }
}

#[test]
fn valid_media_key_with_invalid_value_refuses_instead_of_using_wildcard() {
    for version in ["3.0.4", "3.1.2", "3.2.1"] {
        let doc = document(&json!({"openapi":version,"paths":{"/":{"post":{
            "operationId":"post",
            "requestBody":{"content":{"application/json":false,"*/*":{}}}
        }}}}));
        let operation = doc.operation_id("post").unwrap();
        let payload = Value::parse(b"null", Limits::default()).unwrap();
        let refused = operation
            .request()
            .json_exact(&payload.clone())
            .media("application/json")
            .prepare()
            .unwrap_err();
        assert_eq!(refused.code(), Code::InvalidDeclaration);
        assert_eq!(
            refused.location().unwrap().pointer,
            "/paths/~1/post/requestBody/content/application~1json"
        );
        assert_eq!(
            operation.describe().unwrap_err().code(),
            Code::InvalidDeclaration
        );
    }
}

#[test]
fn form_descriptions_use_media_essence_and_preserve_authored_spelling() {
    for version in ["3.0.4", "3.1.2", "3.2.1"] {
        for media in [
            "application/x-www-form-urlencoded; charset=UTF-8",
            "Application/X-WWW-Form-Urlencoded",
            "Multipart/form-data",
        ] {
            let doc = document(&json!({"openapi":version,"paths":{"/":{"post":{
                "operationId":"send","requestBody":{"content":{media:{"schema":{"type":"object"}}}}
            }}}}));
            let op = doc.operation_id("send").unwrap();
            let description = op.describe().unwrap();
            let body = description.request_body.unwrap();
            assert!(
                matches!(&body.media[0].fields, BodyFields::Declared { fields, openness: FieldOpenness::Open } if fields.is_empty()),
                "{version} {media}"
            );
            assert_eq!(body.media[0].media_type, media);
            let input = CaseInput {
                body: CaseBody::Raw(b"field=value".as_slice().into()),
                selection: CaseSelection {
                    media: Some(media.into()),
                    ..CaseSelection::default()
                },
                ..CaseInput::default()
            };
            assert_eq!(
                op.prepare_case(&input).unwrap().body().map(AsRef::as_ref),
                Some(b"field=value".as_slice())
            );
        }
    }
}

fn oauth_description_fixture(
    version: &str,
    description: &str,
    alternatives: usize,
) -> serde_json::Value {
    let mut value = json!({"paths":{"/":{"get":{"operationId":"read"}}},"security":vec![json!({"auth":["read"]});alternatives]});
    if version == "2.0" {
        value["swagger"] = json!(version);
        value["host"] = json!("api.example");
        value["schemes"] = json!(["https"]);
        value["securityDefinitions"] = json!({"auth":{"type":"oauth2","flow":"application","tokenUrl":"https://issuer.test/token","scopes":{"read":description}}});
    } else {
        value["openapi"] = json!(version);
        value["servers"] = json!([{"url":"https://api.example"}]);
        value["components"] = json!({"securitySchemes":{"auth":{"type":"oauth2","flows":{"clientCredentials":{"tokenUrl":"https://issuer.test/token","scopes":{"read":description}}}}}});
    }
    value
}

fn oauth_input() -> CaseInput {
    CaseInput {
        credentials: [(
            "auth".into(),
            Credential::bearer("access-token", vec!["https://api.example".into()]),
        )]
        .into(),
        selection: CaseSelection {
            security: Some(0),
            ..CaseSelection::default()
        },
        ..CaseInput::default()
    }
}

fn assert_descriptor_limit(error: &Diagnostic, kind: LimitKind, maximum: usize, doc: &Document) {
    assert_eq!(error.code(), Code::Limit);
    assert!(
        matches!(error.reason(), Some(DiagnosticReason::Limit {kind: actual_kind, maximum: actual_maximum, actual}) if *actual_kind == kind && *actual_maximum == maximum && *actual > maximum),
        "{error:?}"
    );
    assert_eq!(error.source_context(), Some(doc.root().source_context()));
    assert!(error.location().is_some());
}

#[test]
fn descriptor_security_work_is_aggregate_and_located() {
    let value = json!({"openapi":"3.1.2","paths":{"/":{"get":{"operationId":"read"}}},
        "components":{"securitySchemes":{"a":{"type":"apiKey","in":"query","name":"a"},"b":{"type":"apiKey","in":"query","name":"b"}}},
        "security":[{"a":[],"b":[]}]
    });
    let doc = Document::parse(
        value.to_string(),
        Some("https://api.example/openapi.json"),
        Limits {
            descriptor_work: 1,
            ..Limits::default()
        },
    )
    .unwrap();
    let op = doc.operation_id("read").unwrap();
    let error = op.describe().unwrap_err();
    assert_descriptor_limit(&error, LimitKind::DescriptorWork, 1, &doc);
    assert_eq!(
        op.prepare_case(&CaseInput::default()).unwrap_err().code(),
        Code::Limit
    );
    let mut single = value;
    single["security"] = json!([{"a":[]}]);
    let single = Document::parse(
        single.to_string(),
        Some("https://api.example/openapi.json"),
        Limits {
            descriptor_work: 1,
            ..Limits::default()
        },
    )
    .unwrap();
    assert_eq!(
        single
            .operation_id("read")
            .unwrap()
            .describe()
            .unwrap()
            .security[0]
            .requirements
            .len(),
        1
    );
}

#[test]
fn descriptor_repeated_oauth_metadata_is_admitted_before_copying() {
    for version in ["2.0", "3.0.4", "3.1.2", "3.2.1"] {
        let value = oauth_description_fixture(version, &"s".repeat(4096), 16);
        let source = value.to_string();
        assert!(source.len() < 8192);
        let doc = Document::parse(
            source,
            Some("https://api.example/openapi.json"),
            Limits {
                document_bytes: 8192,
                ..Limits::default()
            },
        )
        .unwrap();
        let op = doc.operation_id("read").unwrap();
        let error = op.describe().unwrap_err();
        assert_descriptor_limit(&error, LimitKind::DescriptorBytes, 8192, &doc);
        assert!(error.location().unwrap().pointer.ends_with("/scopes/read"));
        // Invocation validates borrowed acquisition metadata and only prepares the
        // selected credential, so inspection's copied-metadata refusal is independent.
        let request = op.prepare_case(&oauth_input()).unwrap();
        assert_eq!(
            request
                .headers()
                .iter()
                .find(|h| h.name() == "Authorization")
                .unwrap()
                .value(),
            b"Bearer access-token"
        );
    }
}

#[test]
fn descriptor_repeated_entries_have_a_separate_node_budget() {
    let value = oauth_description_fixture("3.1.2", "read", 30);
    let doc = Document::parse(
        value.to_string(),
        Some("https://api.example/openapi.json"),
        Limits {
            nodes: 200,
            ..Limits::default()
        },
    )
    .unwrap();
    let op = doc.operation_id("read").unwrap();
    let error = op.describe().unwrap_err();
    assert_descriptor_limit(&error, LimitKind::DescriptorNodes, 200, &doc);
    assert!(op.prepare_case(&oauth_input()).is_ok());
}

#[test]
fn descriptor_reference_chain_growth_uses_the_remaining_work_budget() {
    let value = json!({"openapi":"3.1.2","paths":{"/":{"get":{"operationId":"read","parameters":[{"$ref":"#/components/parameters/A"}]}}},"components":{"parameters":{"A":{"$ref":"#/components/parameters/B"},"B":{"name":"q","in":"query","schema":{"type":"string"}}}}});
    let doc = Document::parse(
        value.to_string(),
        Some("https://api.example/openapi.json"),
        Limits {
            descriptor_work: 2,
            ..Limits::default()
        },
    )
    .unwrap();
    let error = doc.operation_id("read").unwrap().describe().unwrap_err();
    assert_descriptor_limit(&error, LimitKind::DescriptorWork, 2, &doc);
    assert_eq!(
        error.location().unwrap().pointer,
        "/components/parameters/A/$ref"
    );
}

#[test]
fn descriptor_shared_root_dialect_is_bounded_for_inspection_and_compilation() {
    for version in ["3.1.2", "3.2.1"] {
        let value = json!({"openapi":version,"jsonSchemaDialect":format!("https://example.test/{}", "d".repeat(4096)),"paths":{"/":{"post":{"operationId":"send","requestBody":{"content":{"application/json":{"schema":{}},"application/problem+json":{"schema":{}}}}}}}});
        let source = value.to_string();
        assert!(source.len() < 8192);
        let doc = Document::parse(
            source,
            Some("https://api.example/openapi.json"),
            Limits {
                document_bytes: 8192,
                ..Limits::default()
            },
        )
        .unwrap();
        let op = doc.operation_id("send").unwrap();
        for error in [
            op.describe().unwrap_err(),
            op.prepare_case(&CaseInput::default()).unwrap_err(),
        ] {
            assert_descriptor_limit(&error, LimitKind::DescriptorBytes, 8192, &doc);
            assert_eq!(error.location().unwrap().pointer, "/jsonSchemaDialect");
        }
    }
}

#[test]
fn descriptor_budgets_are_per_construction_and_do_not_charge_shared_source_owners() {
    let value = oauth_description_fixture("3.1.2", &"s".repeat(4096), 1);
    let source = value.to_string();
    let doc = Document::parse(
        source,
        Some("https://api.example/openapi.json"),
        Limits {
            document_bytes: 8192,
            ..Limits::default()
        },
    )
    .unwrap();
    let op = doc.operation_id("read").unwrap();
    let first = op.describe().unwrap();
    let second = op.describe().unwrap();
    for description in [&first, &second] {
        let SecurityScheme::OAuth2 { flows } = &description.security[0].requirements[0].scheme
        else {
            panic!("OAuth2")
        };
        assert_eq!(flows[0].scopes["read"].len(), 4096);
    }
    assert!(op.prepare_case(&oauth_input()).is_ok());
}

#[test]
fn borrowed_security_validation_preserves_malformed_flow_refusals() {
    for version in ["2.0", "3.0.4", "3.1.2", "3.2.1"] {
        for defect in ["endpoint", "scope", "refresh", "optional-endpoint"] {
            let mut value = oauth_description_fixture(version, "read", 1);
            let flow = if version == "2.0" {
                &mut value["securityDefinitions"]["auth"]
            } else {
                &mut value["components"]["securitySchemes"]["auth"]["flows"]["clientCredentials"]
            };
            match defect {
                "endpoint" => {
                    flow.as_object_mut().unwrap().remove("tokenUrl");
                }
                "scope" => {
                    flow["scopes"]["read"] = json!(false);
                }
                "refresh" => {
                    flow["refreshUrl"] = json!(42);
                }
                _ => {
                    flow["authorizationUrl"] = json!(false);
                }
            }
            let doc = document(&value);
            let op = doc.operation_id("read").unwrap();
            assert_eq!(
                op.describe().unwrap_err().code(),
                Code::InvalidDeclaration,
                "{version} {defect}"
            );
            assert_eq!(
                op.prepare_case(&oauth_input()).unwrap_err().code(),
                Code::InvalidDeclaration,
                "{version} {defect}"
            );
        }
    }
}

#[test]
fn implicit_schema_dialects_follow_applied_minor_profile_and_explicit_precedence() {
    for version in VERSIONS.iter().copied().chain(["3.1.99", "3.2.99"]) {
        for yaml in [false, true] {
            let mut value = fixture(version);
            for explicit in [0, 1, 2] {
                let schema = &mut value["paths"]["/items/{id}"]["post"]["requestBody"]["content"]["application/json"]
                    ["schema"];
                if explicit == 2 {
                    schema["$schema"] = json!("https://example.test/schema-dialect");
                }
                if explicit > 0 {
                    value["jsonSchemaDialect"] = json!("https://example.test/root-dialect");
                }
                let source = format!("{}{}", if yaml { "---\n" } else { "" }, value);
                let doc =
                    Document::parse(source, Some("https://api.example/spec"), Limits::default())
                        .unwrap();
                let description = doc.operation_id("create").unwrap().describe().unwrap();
                let body = description.request_body.unwrap();
                let actual = dialect(body.media[0].schema.as_ref().unwrap());
                let expected = if version.starts_with("3.1") || version.starts_with("3.2") {
                    Some(match explicit {
                        1 => "https://example.test/root-dialect",
                        2 => "https://example.test/schema-dialect",
                        _ if version.starts_with("3.1") => {
                            "https://spec.openapis.org/oas/3.1/dialect/base"
                        }
                        _ => "https://spec.openapis.org/oas/3.2/dialect/2026-02-26",
                    })
                } else {
                    None
                };
                assert_eq!(
                    actual, expected,
                    "{version} yaml={yaml} explicit={explicit}"
                );
                assert_eq!(doc.version().as_deref(), Some(version));
            }
        }
    }
}

#[test]
fn oauth_flow_vocabulary_is_edition_aware_and_device_tokens_use_the_same_request() {
    for version in VERSIONS.iter().copied().chain(["3.2.99"]) {
        for yaml in [false, true] {
            for flow_name in [
                "implicit",
                "password",
                "application",
                "accessCode",
                "clientCredentials",
                "authorizationCode",
                "deviceAuthorization",
                "DeviceAuthorization",
            ] {
                let mut value = oauth_description_fixture(version, "read", 1);
                let flow = json!({"authorizationUrl":"https://issuer.test/authorize","deviceAuthorizationUrl":"https://issuer.test/device","tokenUrl":"https://issuer.test/token","refreshUrl":"https://issuer.test/refresh","scopes":{"read":"Read"}});
                if version == "2.0" {
                    value["securityDefinitions"]["auth"] = flow;
                    value["securityDefinitions"]["auth"]["type"] = json!("oauth2");
                    value["securityDefinitions"]["auth"]["flow"] = json!(flow_name);
                } else {
                    value["components"]["securitySchemes"]["auth"]["flows"] =
                        json!({flow_name:flow});
                }
                let source = format!("{}{}", if yaml { "---\n" } else { "" }, value);
                let doc =
                    Document::parse(source, Some("https://api.example/spec"), Limits::default())
                        .unwrap();
                let operation = doc.operation_id("read").unwrap();
                let allowed = match flow_name {
                    "implicit" | "password" => true,
                    "application" | "accessCode" => version == "2.0",
                    "clientCredentials" | "authorizationCode" => version != "2.0",
                    "deviceAuthorization" => version.starts_with("3.2."),
                    _ => false,
                };
                if !allowed {
                    assert_eq!(
                        operation.describe().unwrap_err().code(),
                        Code::InvalidDeclaration,
                        "{version} {flow_name}"
                    );
                    assert_eq!(
                        operation.prepare_case(&oauth_input()).unwrap_err().code(),
                        Code::InvalidDeclaration,
                        "{version} {flow_name}"
                    );
                    continue;
                }
                let description = operation.describe().unwrap();
                let SecurityScheme::OAuth2 { flows } =
                    &description.security[0].requirements[0].scheme
                else {
                    panic!("OAuth2")
                };
                assert_eq!(
                    flows[0].device_authorization_url.as_deref(),
                    version
                        .starts_with("3.2.")
                        .then_some("https://issuer.test/device")
                );
                assert_eq!(
                    flows[0].flow,
                    match flow_name {
                        "application" => "clientCredentials",
                        "accessCode" => "authorizationCode",
                        other => other,
                    }
                );
                let prepared = operation.prepare_case(&oauth_input()).unwrap();
                assert_eq!(prepared.target(), "https://api.example/");
                assert!(
                    prepared
                        .headers()
                        .iter()
                        .any(|h| h.name().eq_ignore_ascii_case("authorization")
                            && h.value() == b"Bearer access-token")
                );
            }
        }
    }
}

#[test]
fn device_flow_required_endpoints_and_scope_shape_refuse_with_original_sources() {
    for version in ["3.2.0", "3.2.1"] {
        for key in ["deviceAuthorizationUrl", "tokenUrl", "scopes"] {
            for wrong_shape in [false, true] {
                let mut value = oauth_description_fixture(version, "read", 1);
                let mut flow = json!({"deviceAuthorizationUrl":"https://issuer.test/device","tokenUrl":"https://issuer.test/token","scopes":{"read":"Read"}});
                if wrong_shape {
                    flow[key] = json!(42);
                } else {
                    flow.as_object_mut().unwrap().remove(key);
                }
                value["components"]["securitySchemes"]["auth"]["flows"] =
                    json!({"deviceAuthorization":flow});
                let doc = document(&value);
                let operation = doc.operation_id("read").unwrap();
                for error in [
                    operation.describe().unwrap_err(),
                    operation.prepare_case(&oauth_input()).unwrap_err(),
                ] {
                    assert_eq!(error.code(), Code::InvalidDeclaration);
                    assert!(
                        error
                            .location()
                            .unwrap()
                            .pointer
                            .contains("/flows/deviceAuthorization")
                    );
                    assert_eq!(error.source_context(), Some(doc.root().source_context()));
                }
            }
        }
    }
}

fn dialect(schema: &SchemaDescription) -> Option<&str> {
    match &schema.profile {
        SchemaProfile::JsonSchema { dialect_uri } => Some(dialect_uri),
        _ => None,
    }
}
