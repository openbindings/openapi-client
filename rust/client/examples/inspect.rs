#[path = "support/mod.rs"]
mod support;
use dynamic_openapi_client::{Document, Limits};

fn main() -> Result<(), support::Error> {
    let document = Document::parse(
        support::document("https://api.example"),
        None,
        Limits::default(),
    )?;
    println!("discovery complete: {}", document.discovery_complete());
    for issue in document.graph_issues() {
        eprintln!("{issue}");
    }
    for operation in document.operations() {
        // docs-start: inspect
        let d = operation.describe()?;
        println!("{} {} {:?}", d.method.as_str(), d.path, d.operation_id);
        // docs-end: inspect
        for p in d.parameters.iter() {
            println!("{:?} {} required={}", p.location(), p.name(), p.required());
        }
        for (i, s) in d.servers.iter().enumerate() {
            println!("server {i}: {}", s.url);
            if let Some(v) = &s.variables {
                println!("variables: {}", v.raw());
            }
        }
        if let Some(body) = &d.request_body {
            for media in &body.media {
                println!("request: {}", media.media_type);
            }
        }
        for r in &d.responses {
            println!("response: {}", r.status);
        }
        for (i, s) in d.security.iter().enumerate() {
            println!("security alternative {i}:");
            for r in &s.requirements {
                println!(
                    "scheme={} mechanism={:?} scopes={:?}",
                    r.name, r.scheme, r.scopes
                );
            }
        }
    }
    Ok(())
}
