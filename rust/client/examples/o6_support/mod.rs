#![allow(dead_code)]
use serde::{Deserialize, Serialize};
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
        "openapi":"3.1.2", "info":{"title":"O6 jobs","version":"1"},
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
#[derive(Debug, Deserialize)]
pub struct ApiError {
    pub error: String,
}
