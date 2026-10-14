#[derive(serde::Serialize)]
struct Text<'a> {
    message: &'a str,
    count: u64,
}
use dynamic_openapi_client::exact::Value;
use dynamic_openapi_client::form::{FormField, Part};
use dynamic_openapi_client::{Document, Limits};
fn operation(media: &str) -> dynamic_openapi_client::Operation {
    let schema = serde_json::json!({"type":"object","properties":{
        "message":{"type":"string"},"count":{"type":"integer"},"file":{"type":"string","format":"binary"}}});
    let body = serde_json::json!({"content":{media:{"schema":schema}}});
    let spec = serde_json::json!({"openapi":"3.0.4","servers":[{"url":"https://api.example"}],
        "paths":{"/send":{"post":{"operationId":"send","requestBody":body}}}});
    Document::parse(spec.to_string(), None, Limits::default())
        .unwrap()
        .operation_id("send")
        .unwrap()
}
fn main() -> Result<(), Box<dyn std::error::Error>> {
    let text = operation("application/x-www-form-urlencoded")
        .request()
        .form(&Text {
            message: "café +&",
            count: 9_007_199_254_740_993,
        })
        .prepare()?;
    assert_eq!(
        text.body().map(AsRef::as_ref).unwrap(),
        b"message=caf%C3%A9+%2B%26&count=9007199254740993"
    );
    let upload = operation("multipart/form-data");
    let file = Part::bytes(b"a\0b".as_slice()).filename("note.bin");
    let prepared = upload
        .request()
        .form_fields([
            FormField::value("message", &"plain caption", Limits::default()).unwrap(),
            FormField::exact(
                "count",
                Value::parse("9007199254740993", Limits::default())?.clone(),
            ),
            FormField::part("file", file.clone()),
        ])
        .prepare()?;
    assert!(
        prepared
            .body()
            .map(AsRef::as_ref)
            .unwrap()
            .windows(3)
            .any(|v| v == b"a\0b")
    );
    // Explicit part arrays remain distinct from a JSON array of logical values.
    let files = [file];
    let _ = upload
        .request()
        .form_fields([FormField::parts("file", &files)])
        .prepare()?;
    println!("PASS: current scalar text form, mixed file/exact upload and explicit part array");
    Ok(())
}
