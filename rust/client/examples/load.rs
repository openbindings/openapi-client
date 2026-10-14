#[path = "support/mod.rs"]
mod support;
use dynamic_openapi_client::{Document, Limits};

fn main() -> Result<(), support::Error> {
    // docs-start: load
    let bytes = support::document("https://api.example");
    let document = Document::parse(
        bytes,
        Some("https://api.example/openapi.json"),
        Limits::default(),
    )?;
    println!("discovery complete: {}", document.discovery_complete());
    // docs-end: load
    Ok(())
}
