//! The explicit redirect step: method and content rules, Location resolution,
//! credential and cookie handling by origin, downgrade and chain limits, and the
//! response evidence the step requires. The step derives requests and never sends.
use dynamic_openapi_client::inspect::Method;
use dynamic_openapi_client::*;
use dynamic_openapi_client::{diagnostics::*, transport::*};
use std::{
    cell::Cell,
    future::Future,
    sync::Arc,
    task::{Context, Poll, Waker},
};

const DOC: &str = r#"{"openapi":"3.2.1","servers":[{"url":"https://api.example/v1"}],
"components":{"securitySchemes":{
  "bearer":{"type":"http","scheme":"bearer"},
  "key":{"type":"apiKey","in":"query","name":"api_key"},
  "session":{"type":"apiKey","in":"cookie","name":"session"},
  "header":{"type":"apiKey","in":"header","name":"X-Api-Key"}}},
"paths":{"/items/{id}":{
  "parameters":[
    {"in":"path","name":"id","required":true,"schema":{"type":"string"}},
    {"in":"query","name":"q","schema":{"type":"string"}},
    {"in":"cookie","name":"theme","schema":{"type":"string"}}],
  "get":{"operationId":"get","requestBody":{"content":{"application/octet-stream":{}}}},
  "head":{"operationId":"head"},
  "post":{"operationId":"post","requestBody":{"content":{"application/json":{}}}},
  "put":{"operationId":"put","requestBody":{"content":{"application/json":{}}}},
  "delete":{"operationId":"delete"},
  "patch":{"operationId":"secured","security":[{"bearer":[],"key":[],"session":[],"header":[]}],
    "requestBody":{"content":{"application/json":{}}}}}}}"#;
const ORIGIN: &str = "https://api.example";

fn ready<F: Future>(f: F) -> F::Output {
    let mut f = Box::pin(f);
    match f.as_mut().poll(&mut Context::from_waker(Waker::noop())) {
        Poll::Ready(v) => v,
        Poll::Pending => panic!("in-memory host suspended"),
    }
}
fn parse(limits: Limits) -> Document {
    Document::parse(DOC, None, limits).unwrap()
}
fn request(document: &Document, operation: &str) -> RequestBuilder {
    document
        .operation_id(operation)
        .unwrap()
        .request()
        .parameter(ParameterLocation::Path, "id", &"alpha")
}
fn secured(document: &Document) -> PreparedRequest {
    let origins = || vec![ORIGIN.to_owned()];
    request(document, "secured")
        .parameter(ParameterLocation::Query, "q", &"x")
        .parameter(ParameterLocation::Cookie, "theme", &"dark")
        .json(&serde_json::json!({"a": 1}))
        .header(Header::new("X-Trace", "trace-1"))
        .credential("bearer", Credential::bearer("TOKEN", origins()))
        .credential("key", Credential::api_key("KEY", origins()))
        .credential("session", Credential::api_key("SESSION", origins()))
        .credential("header", Credential::api_key("HEADER-KEY", origins()))
        .prepare()
        .unwrap()
}
/// Invoke `request` once against a fixture host returning this response.
fn respond(request: &PreparedRequest, input: ResponseInput) -> Outcome {
    ready(request.invoke(
        Cancellation::default(),
        &HostCapabilities::programmable(),
        |_, _| async move {
            TransportResult {
                response: Some(input),
                upload: UploadState::Complete,
                error_detail: None,
            }
        },
    ))
}
fn response(status: u16, headers: Vec<Header>) -> ResponseInput {
    let mut input = ResponseInput::new(
        status,
        headers,
        Arc::from(br#"{"moved":true}"#.as_slice()),
        BodyState::Complete,
        Provenance::Wire,
    );
    input.header_fidelity = HeaderFidelity::HostProcessed;
    input.header_delivery = HeaderDelivery::Complete;
    input
}
fn follow(
    request: &PreparedRequest,
    status: u16,
    location: &str,
) -> Result<Option<PreparedRequest>, Diagnostic> {
    let outcome = respond(
        request,
        response(status, vec![Header::new("Location", location)]),
    );
    request.follow_redirect(&outcome)
}
fn next(request: &PreparedRequest, status: u16, location: &str) -> PreparedRequest {
    follow(request, status, location).unwrap().unwrap()
}
fn names(request: &PreparedRequest) -> Vec<String> {
    request
        .headers()
        .iter()
        .map(|h| h.name().to_ascii_lowercase())
        .collect()
}
fn value<'a>(request: &'a PreparedRequest, name: &str) -> Option<&'a [u8]> {
    request
        .headers()
        .iter()
        .find(|h| h.name().eq_ignore_ascii_case(name))
        .map(Header::value)
}
fn redirect_refusal(error: &Diagnostic, code: Code, reason: RedirectRefusal) {
    assert_eq!(error.code(), code);
    assert_eq!(error.reason(), Some(&DiagnosticReason::Redirect(reason)));
}

#[test]
fn each_redirect_status_sets_method_and_content() {
    let document = parse(Limits::default());
    let prepare = |operation: &str| {
        let builder = request(&document, operation);
        match operation {
            "post" | "put" => builder.json(&serde_json::json!({"a": 1})),
            "get" => builder.raw_body(b"bytes".as_slice()),
            _ => builder,
        }
        .prepare()
        .unwrap()
    };
    use Method::*;
    // (operation, status, next method, whether content follows)
    for (operation, status, method, content) in [
        ("post", 301, Get, false),
        ("post", 302, Get, false),
        ("post", 303, Get, false),
        ("post", 307, Post, true),
        ("post", 308, Post, true),
        ("put", 301, Put, true),
        ("put", 302, Put, true),
        ("put", 303, Get, false),
        ("put", 307, Put, true),
        ("put", 308, Put, true),
        ("get", 301, Get, true),
        ("get", 302, Get, true),
        ("get", 303, Get, false),
        ("get", 307, Get, true),
        ("get", 308, Get, true),
        ("delete", 301, Delete, true),
        ("delete", 302, Delete, true),
        ("delete", 303, Get, false),
        ("delete", 307, Delete, true),
        ("delete", 308, Delete, true),
        ("head", 301, Head, true),
        ("head", 302, Head, true),
        ("head", 303, Head, true),
        ("head", 307, Head, true),
        ("head", 308, Head, true),
    ] {
        let original = prepare(operation);
        let derived = next(&original, status, "/v1/moved");
        assert_eq!(derived.method(), method, "{operation} {status}");
        assert_eq!(derived.target(), "https://api.example/v1/moved");
        match (original.body(), content) {
            (Some(body), true) => {
                assert!(Arc::ptr_eq(body, derived.body().unwrap()));
                assert_eq!(derived.body_kind(), original.body_kind());
                assert_eq!(
                    value(&derived, "content-type"),
                    value(&original, "content-type")
                );
            }
            (_, true) => assert!(derived.body().is_none()),
            (_, false) => {
                assert!(derived.body().is_none(), "{operation} {status}");
                assert_eq!(derived.body_kind(), "absent");
                assert_eq!(value(&derived, "content-type"), None);
            }
        }
    }
}

#[test]
fn non_redirect_statuses_and_absent_location_are_final() {
    let document = parse(Limits::default());
    let original = request(&document, "get").prepare().unwrap();
    for status in [200, 201, 204, 300, 304, 305, 306, 309, 399, 400, 404, 500] {
        assert!(follow(&original, status, "/v1/moved").unwrap().is_none());
    }
    for status in [301, 302, 303, 307, 308] {
        let outcome = respond(
            &original,
            response(status, vec![Header::new("X-Other", "v")]),
        );
        assert!(original.follow_redirect(&outcome).unwrap().is_none());
    }
    // A final response needs no particular header evidence.
    let mut final_response = response(200, vec![Header::new("Location", "/v1/x")]);
    final_response.header_delivery = HeaderDelivery::Unspecified;
    let outcome = respond(&original, final_response);
    assert!(original.follow_redirect(&outcome).unwrap().is_none());
    // No response arrived: nothing to follow; the outcome keeps its own error.
    let refused = Outcome::not_dispatched(Diagnostic::new(Code::Cancelled), true, None);
    assert!(original.follow_redirect(&refused).unwrap().is_none());
    let failed = ready(original.invoke(
        Cancellation::default(),
        &HostCapabilities::programmable(),
        |_, _| async {
            TransportResult {
                response: None,
                upload: UploadState::Unknown,
                error_detail: Some("connection reset".into()),
            }
        },
    ));
    assert!(original.follow_redirect(&failed).unwrap().is_none());
    assert_eq!(failed.error().unwrap().code(), Code::TransportFailure);
}

#[test]
fn relative_and_absolute_locations_resolve_against_the_target() {
    let document = parse(Limits::default());
    let original = request(&document, "get")
        .parameter(ParameterLocation::Query, "q", &"x")
        .prepare()
        .unwrap();
    assert_eq!(original.target(), "https://api.example/v1/items/alpha?q=x");
    for (location, target) in [
        ("next", "https://api.example/v1/items/next"),
        ("../other", "https://api.example/v1/other"),
        ("./a/./b/../c", "https://api.example/v1/items/a/c"),
        ("/root", "https://api.example/root"),
        ("/a//b/", "https://api.example/a//b/"),
        ("?q=2", "https://api.example/v1/items/alpha?q=2"),
        ("", "https://api.example/v1/items/alpha?q=x"),
        ("#section", "https://api.example/v1/items/alpha?q=x"),
        ("next#section", "https://api.example/v1/items/next"),
        (
            "%2e%2e/encoded",
            "https://api.example/v1/items/%2e%2e/encoded",
        ),
        ("//cdn.example/file", "https://cdn.example/file"),
        (
            "https://other.example/x/../y?z=1#top",
            "https://other.example/y?z=1",
        ),
        (
            "https://API.Example:8443/A/%7e?",
            "https://API.Example:8443/A/%7e?",
        ),
    ] {
        assert_eq!(
            next(&original, 302, location).target(),
            target,
            "{location}"
        );
    }
}

#[test]
fn unusable_locations_refuse_with_a_controlled_reason() {
    let document = parse(Limits::default());
    let original = request(&document, "get").prepare().unwrap();
    for location in [
        b"ftp://api.example/file".as_slice(),
        b"mailto:someone@example.com",
        b"urn:isbn:0451450523",
        b"https:no-authority",
        b"https:///no-host",
        b"https://user:SECRET@api.example/",
        b"https://user@api.example/",
        b"https://api.example:99999/",
        b"/not a reference",
        b"/bad%zzescape",
        b"/caf\xc3\xa9",
        b"/latin\xe9",
    ] {
        let outcome = respond(
            &original,
            response(302, vec![Header::new("Location", location)]),
        );
        let error = original.follow_redirect(&outcome).unwrap_err();
        redirect_refusal(&error, Code::InvalidDestination, RedirectRefusal::Location);
        assert_eq!(error.context(), Some(&DiagnosticContext::PreparedRequest));
        assert!(!format!("{error} {error:?}").contains("SECRET"));
    }
    let outcome = respond(
        &original,
        response(
            302,
            vec![
                Header::new("Location", "/v1/one"),
                Header::new("location", "/v1/two"),
            ],
        ),
    );
    redirect_refusal(
        &original.follow_redirect(&outcome).unwrap_err(),
        Code::InvalidDestination,
        RedirectRefusal::Location,
    );
}

#[test]
fn same_origin_redirect_keeps_credentials_in_header_query_and_cookie() {
    let document = parse(Limits::default());
    let original = secured(&document);
    assert_eq!(
        original.target(),
        "https://api.example/v1/items/alpha?q=x&api_key=KEY"
    );
    assert_eq!(
        value(&original, "cookie"),
        Some(b"theme=dark; session=SESSION".as_slice())
    );
    for (location, target) in [
        // The Location's own query replaces the request's; the credential follows it.
        (
            "/v2/items/alpha",
            "https://api.example/v2/items/alpha?api_key=KEY",
        ),
        (
            "?page=2",
            "https://api.example/v1/items/alpha?page=2&api_key=KEY",
        ),
        ("/v2?", "https://api.example/v2?api_key=KEY"),
        // A query-preserving redirect already carries it; nothing is duplicated.
        ("", "https://api.example/v1/items/alpha?q=x&api_key=KEY"),
        (
            "/v2?q=x&api_key=KEY",
            "https://api.example/v2?q=x&api_key=KEY",
        ),
        (
            "/v2?api_key=KEY&q=x",
            "https://api.example/v2?q=x&api_key=KEY",
        ),
        // Host case and an explicit default port compare as the same origin.
        (
            "https://API.EXAMPLE:443/v3",
            "https://API.EXAMPLE:443/v3?api_key=KEY",
        ),
    ] {
        let derived = next(&original, 307, location);
        assert_eq!(derived.target(), target, "{location}");
        assert_eq!(names(&derived), names(&original), "{location}");
        assert_eq!(derived.headers(), original.headers());
        // A derived request keeps the provenance a second hop needs.
        let again = next(&derived, 308, "/v4");
        assert!(again.target().ends_with("/v4?api_key=KEY"), "{location}");
        assert_eq!(again.headers(), original.headers());
        let away = next(&derived, 308, "https://other.example/v4");
        assert_eq!(away.target(), "https://other.example/v4");
    }
}

#[test]
fn cross_origin_redirect_removes_credentials_from_header_query_and_cookie() {
    let document = parse(Limits::default());
    let original = secured(&document);
    for (location, target) in [
        (
            "https://other.example/v1/items/alpha?q=x&api_key=KEY",
            "https://other.example/v1/items/alpha?q=x",
        ),
        (
            "https://other.example/files?api_key=KEY",
            "https://other.example/files",
        ),
        // Only the applied credential pair is ours; other values are the server's.
        (
            "https://other.example/files?api_key=OTHER",
            "https://other.example/files?api_key=OTHER",
        ),
        ("//cdn.example/blob", "https://cdn.example/blob"),
        ("https://api.example:8443/v1", "https://api.example:8443/v1"),
    ] {
        let derived = next(&original, 307, location);
        assert_eq!(derived.target(), target, "{location}");
        assert_eq!(names(&derived), ["x-trace", "content-type"], "{location}");
        assert_eq!(derived.method(), Method::Patch);
        assert!(Arc::ptr_eq(
            original.body().unwrap(),
            derived.body().unwrap()
        ));
        // Nothing removed can return on a later hop back to the original origin.
        let back = next(&derived, 307, "https://api.example/v1/back");
        assert_eq!(back.target(), "https://api.example/v1/back");
        assert_eq!(names(&back), ["x-trace", "content-type"]);
    }
    // Fields the caller supplied as credential-bearing are removed as well.
    let original = request(&document, "post")
        .json(&serde_json::json!({}))
        .header(Header::new("Authorization", "Bearer caller"))
        .header(Header::sensitive("X-Signature", "signed"))
        .header(Header::new("X-Trace", "trace-1"))
        .prepare()
        .unwrap();
    let derived = next(&original, 308, "https://other.example/v1");
    assert_eq!(names(&derived), ["x-trace", "content-type"]);
    let same = next(&original, 308, "/v2");
    assert_eq!(same.headers(), original.headers());
}

#[test]
fn cross_origin_redirect_removes_every_cookie_field() {
    let document = parse(Limits::default());
    let parameter = request(&document, "get")
        .parameter(ParameterLocation::Cookie, "theme", &"dark")
        .prepare()
        .unwrap();
    let supplied = request(&document, "get")
        .header(Header::new("Cookie", "theme=dark"))
        .header(Header::new("Accept", "application/json"))
        .prepare()
        .unwrap();
    for original in [parameter, supplied] {
        assert!(value(&original, "cookie").is_some());
        let away = next(&original, 302, "https://other.example/v1");
        assert_eq!(value(&away, "cookie"), None);
        assert_eq!(value(&away, "accept"), value(&original, "accept"));
        let same = next(&original, 302, "/v2");
        assert_eq!(value(&same, "cookie"), value(&original, "cookie"));
    }
}

#[test]
fn dropped_content_removes_the_headers_that_describe_it() {
    let document = parse(Limits::default());
    let original = request(&document, "post")
        .json(&serde_json::json!({"a": 1}))
        .header(Header::new("content-language", "en"))
        .header(Header::new("Content-Location", "/drafts/1"))
        .header(Header::new("CONTENT-ENCODING", "identity"))
        .header(Header::new("X-Trace", "trace-1"))
        .prepare()
        .unwrap();
    assert_eq!(
        names(&original),
        [
            "content-language",
            "content-location",
            "content-encoding",
            "x-trace",
            "content-type"
        ]
    );
    let dropped = next(&original, 303, "/v1/status");
    assert_eq!(dropped.method(), Method::Get);
    assert!(dropped.body().is_none());
    assert_eq!(names(&dropped), ["x-trace"]);
    let kept = next(&original, 307, "/v1/again");
    assert_eq!(kept.headers(), original.headers());
    assert!(Arc::ptr_eq(original.body().unwrap(), kept.body().unwrap()));
}

#[test]
fn https_to_http_redirect_is_refused() {
    let document = parse(Limits::default());
    let original = secured(&document);
    for location in [
        "http://api.example/v1",
        "http://other.example/v1",
        "HTTP://api.example:443/",
    ] {
        let error = follow(&original, 302, location).unwrap_err();
        redirect_refusal(&error, Code::InvalidDestination, RedirectRefusal::Downgrade);
    }
    // An http request may move to https; that is another origin.
    let plain = Document::parse(
        DOC.replace("https://api.example/v1", "http://api.example/v1"),
        None,
        Limits::default(),
    )
    .unwrap();
    let original = plain
        .operation_id("get")
        .unwrap()
        .request()
        .parameter(ParameterLocation::Path, "id", &"alpha")
        .header(Header::new("Authorization", "Basic caller"))
        .prepare()
        .unwrap();
    let upgraded = next(&original, 301, "https://api.example/v1/items/alpha");
    assert_eq!(upgraded.target(), "https://api.example/v1/items/alpha");
    assert_eq!(value(&upgraded, "authorization"), None);
}

#[test]
fn redirect_limit_admits_twenty_and_refuses_the_twenty_first() {
    assert_eq!(Limits::default().redirects, 20);
    let document = parse(Limits::default());
    let mut current = secured(&document);
    for hop in 1..=20 {
        current = next(&current, 302, &format!("/v1/hop/{hop}"));
        assert_eq!(
            current.target(),
            format!("https://api.example/v1/hop/{hop}?api_key=KEY")
        );
    }
    let error = follow(&current, 302, "/v1/hop/21").unwrap_err();
    assert_eq!(error.code(), Code::Limit);
    assert_eq!(
        error.reason(),
        Some(&DiagnosticReason::Limit {
            kind: LimitKind::Redirects,
            maximum: 20,
            actual: 21,
        })
    );
    // A final response still ends the chain at the limit.
    assert!(follow(&current, 200, "/v1/hop/21").unwrap().is_none());
    let none = parse(Limits {
        redirects: 0,
        ..Limits::default()
    });
    let original = request(&none, "get").prepare().unwrap();
    assert_eq!(
        follow(&original, 308, "/v1").unwrap_err().reason(),
        Some(&DiagnosticReason::Limit {
            kind: LimitKind::Redirects,
            maximum: 0,
            actual: 1,
        })
    );
}

#[test]
fn redirect_target_respects_the_target_byte_limit() {
    let document = parse(Limits {
        target_bytes: 64,
        ..Limits::default()
    });
    let original = request(&document, "get").prepare().unwrap();
    let error = follow(&original, 302, &format!("/{}", "a".repeat(64))).unwrap_err();
    assert_eq!(error.code(), Code::Limit);
    assert!(matches!(
        error.reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::TargetBytes,
            maximum: 64,
            ..
        })
    ));
}

#[test]
fn same_origin_location_that_renames_a_query_credential_collides() {
    let document = parse(Limits::default());
    let original = secured(&document);
    for location in [
        "/v2?api_key=OTHER",
        "/v2?api_key",
        "?q=x&api_key=KEY&api_key=OTHER",
    ] {
        let error = follow(&original, 307, location).unwrap_err();
        assert_eq!(error.code(), Code::CredentialCollision, "{location}");
        assert_eq!(error.context(), Some(&DiagnosticContext::PreparedRequest));
        assert!(!format!("{error} {error:?}").contains("KEY"));
    }
}

#[test]
fn redirect_needs_complete_header_evidence() {
    let document = parse(Limits::default());
    let original = request(&document, "get").prepare().unwrap();
    for delivery in [HeaderDelivery::Unspecified, HeaderDelivery::Incomplete] {
        for headers in [vec![Header::new("Location", "/v1/moved")], vec![]] {
            let mut input = response(302, headers);
            input.header_delivery = delivery;
            let outcome = respond(&original, input);
            redirect_refusal(
                &original.follow_redirect(&outcome).unwrap_err(),
                Code::InvalidHostEvidence,
                RedirectRefusal::HeaderDelivery,
            );
        }
    }
    // Complete adapter delivery, but core bounds omitted an entry.
    let bounded = parse(Limits {
        headers: 1,
        ..Limits::default()
    });
    let small = request(&bounded, "get").prepare().unwrap();
    let outcome = respond(
        &small,
        response(
            302,
            vec![
                Header::new("X-First", "1"),
                Header::new("Location", "/v1/moved"),
            ],
        ),
    );
    assert_eq!(outcome.omitted_header_lines(), Some(1));
    redirect_refusal(
        &small.follow_redirect(&outcome).unwrap_err(),
        Code::InvalidHostEvidence,
        RedirectRefusal::HeaderDelivery,
    );
    // Invalid or contradictory evidence keeps the outcome's own refusal.
    let mut contradictory = response(302, vec![Header::new("Location", "/v1/moved")]);
    contradictory.header_fidelity = HeaderFidelity::ExactFields;
    contradictory.header_delivery = HeaderDelivery::Incomplete;
    let invalid = response(
        302,
        vec![
            Header::new("Location", "/v1/moved"),
            Header::new("X-Bad", b"a\x01b"),
        ],
    );
    for input in [contradictory, invalid] {
        let outcome = respond(&original, input);
        let error = original.follow_redirect(&outcome).unwrap_err();
        assert_eq!(error.code(), Code::InvalidHostEvidence);
        assert_eq!(Some(&error), outcome.error());
    }
}

#[test]
fn opaque_redirect_is_refused_rather_than_guessed() {
    let document = parse(Limits::default());
    let original = request(&document, "get").prepare().unwrap();
    for detail in [None, Some("browser detail")] {
        let mut input = response(0, vec![]);
        input.opaque_redirect = true;
        let outcome = ready(original.invoke(
            Cancellation::default(),
            &HostCapabilities::programmable(),
            |_, _| async move {
                TransportResult {
                    response: Some(input),
                    upload: UploadState::Unknown,
                    error_detail: detail.map(Arc::from),
                }
            },
        ));
        let error = original.follow_redirect(&outcome).unwrap_err();
        assert_eq!(error.code(), Code::OpaqueRedirect);
        assert!(!format!("{error} {error:?}").contains("detail"));
    }
}

#[test]
fn each_hop_is_a_separate_admitted_call_and_the_step_never_sends() {
    let document = parse(Limits::default());
    let calls = Cell::new(0);
    let host = HostCapabilities::programmable();
    // A scripted server: 303 to a status resource, then 200.
    let send = |request: PreparedRequest, _: Cancellation| {
        calls.set(calls.get() + 1);
        let input = if request.target().ends_with("/status") {
            assert_eq!(request.method(), Method::Get);
            assert!(request.body().is_none());
            response(200, vec![Header::new("Content-Type", "application/json")])
        } else {
            response(303, vec![Header::new("Location", "status")])
        };
        async move {
            TransportResult {
                response: Some(input),
                upload: UploadState::Complete,
                error_detail: None,
            }
        }
    };
    let cancellation = Cancellation::default();
    let prepared = request(&document, "post")
        .json(&serde_json::json!({"a": 1}))
        .prepare()
        .unwrap();
    let result: Result<Outcome, Diagnostic> = ready(async {
        let mut request = prepared;
        let mut outcome = request.invoke(cancellation.clone(), &host, &send).await;
        while let Some(next) = request.follow_redirect(&outcome)? {
            assert_eq!(calls.get(), 1, "deriving a request sends nothing");
            request = next;
            outcome = request.invoke(cancellation.clone(), &host, &send).await;
        }
        Ok(outcome)
    });
    let outcome = result.unwrap();
    assert_eq!(outcome.status(), Some(200));
    assert_eq!(calls.get(), 2);
    // Host admission applies to every hop: this host refuses the derived request.
    let mut strict = HostCapabilities::programmable();
    strict.forbidden_headers.push("x-trace".into());
    let original = request(&document, "get")
        .header(Header::new("X-Trace", "trace-1"))
        .prepare()
        .unwrap();
    let derived = next(&original, 302, "/v1/elsewhere");
    let refused = ready(
        derived.invoke(Cancellation::default(), &strict, |_, _| async {
            unreachable!("admission refuses before the callback")
        }),
    );
    assert_eq!(refused.dispatch(), DispatchEvidence::NotDispatched);
    assert_eq!(
        refused.error().unwrap().reason(),
        Some(&DiagnosticReason::Host(HostRefusal::ForbiddenHeader))
    );
    assert!(derived.follow_redirect(&refused).unwrap().is_none());
    assert_eq!(derived.response_admission(), original.response_admission());
    assert_eq!(derived.operation().id(), original.operation().id());
}
