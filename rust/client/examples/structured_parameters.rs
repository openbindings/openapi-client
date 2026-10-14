//! Logical path/query styles and exact content share the existing request builder.
//! Run with: cargo run -p dynamic-openapi-client --example structured_parameters
use dynamic_openapi_client::exact::*;
use dynamic_openapi_client::*;

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let document = Document::parse(
        r#"{
          "openapi":"3.2.1",
          "servers":[{"url":"https://api.example"}],
          "paths":{"/search{position}":{"get":{
            "operationId":"search",
            "parameters":[
              {"name":"position","in":"path","required":true,
               "style":"matrix","explode":true,"schema":{"type":"object"}},
              {"name":"tags","in":"query","style":"pipeDelimited",
               "schema":{"type":"array","items":{"type":"string"}}},
              {"name":"filter","in":"query",
               "content":{"application/json":{"schema":{"$ref":"opaque-filter-schema"}}}}
            ]
          }}}
        }"#,
        Some("https://docs.example/search"),
        Limits::default(),
    )?;
    let position = std::collections::BTreeMap::from([("x", 10), ("y", 20)]);
    let tags = ["red", "blue"];
    let filter = Value::parse(r#"{"min":1e9999,"active":true}"#, Limits::default())?;
    let prepared = document
        .operation_id("search")?
        .request()
        .parameter(ParameterLocation::Path, "position", &position)
        .parameter(ParameterLocation::Query, "tags", &tags)
        .parameter_exact(ParameterLocation::Query, "filter", &filter.clone())
        .prepare()?;
    assert_eq!(
        prepared.target(),
        "https://api.example/search;x=10;y=20?tags=red%7Cblue&filter=%7B%22min%22%3A1e9999%2C%22active%22%3Atrue%7D"
    );
    println!("Prepared matrix path, pipe query and exact JSON content parameters; no dispatch.");
    Ok(())
}
