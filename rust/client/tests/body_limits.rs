use dynamic_openapi_client::diagnostics::*;
use dynamic_openapi_client::*;
#[test]
fn ordinary_builder_body_limit_reports_encoded_size() {
    let op=Document::parse(r#"{"openapi":"3.1.2","servers":[{"url":"https://api.example"}],"paths":{"/":{"post":{"operationId":"post","requestBody":{"content":{"application/json":{}}}}}}}"#,None,Limits{body_bytes:8,..Limits::default()}).unwrap().operation_id("post").unwrap();
    let text = "x".repeat(1_000_000);
    let failure = op.request().json(&text).prepare().unwrap_err();
    let error = &failure;
    println!("ordinary request body reason {:?}", error.reason());
    assert_eq!(
        error.reason(),
        Some(&DiagnosticReason::Limit {
            kind: LimitKind::BodyBytes,
            maximum: 8,
            actual: 1_000_002
        })
    );
}
