//! Disposable qualification bridge. This is not a supported TypeScript API.
use dynamic_openapi_client::*;
use serde_json::{Value as J, json};
use wasm_bindgen::prelude::*;
use wasm_bindgen_futures::JsFuture;
fn js_error(e: impl std::fmt::Display) -> JsValue {
    JsValue::from_str(&e.to_string())
}
#[wasm_bindgen]
pub struct HostDocument(Document);
#[wasm_bindgen]
impl HostDocument {
    #[wasm_bindgen(constructor)]
    pub fn new(text: &str, base: Option<String>) -> Result<HostDocument, JsValue> {
        Document::parse(text, base.as_deref(), Limits::default())
            .map(Self)
            .map_err(js_error)
    }
    pub fn inspect(&self) -> String {
        json!({"version":self.0.version(),"operations":self.0.operations().map(|o|json!({"id":o.id(),"path":o.path(),"method":o.method().as_str()})).collect::<Vec<_>>()}).to_string()
    }
    pub fn raw(&self, pointer: &str) -> Result<String, JsValue> {
        self.0
            .root()
            .pointer(pointer)
            .map(|v| v.raw().to_owned())
            .map_err(js_error)
    }
    pub fn operation(&self, id: &str) -> Result<HostOperation, JsValue> {
        self.0.operation_id(id).map(HostOperation).map_err(js_error)
    }
}
#[wasm_bindgen]
pub struct HostOperation(Operation);
#[wasm_bindgen]
impl HostOperation {
    pub fn prepare(&self, input_json: &str) -> Result<HostRequest, JsValue> {
        let c: J = serde_json::from_str(input_json).map_err(js_error)?;
        self.0
            .prepare(&oac_qualification::input(&c))
            .map(HostRequest)
            .map_err(js_error)
    }
}
#[wasm_bindgen]
pub struct HostRequest(PreparedRequest);
#[wasm_bindgen]
impl HostRequest {
    pub fn inspect(&self) -> String {
        oac_qualification::request_facts(&self.0).to_string()
    }
    pub async fn invoke(&self, callback: js_sys::Function) -> Result<String, JsValue> {
        let mut host = HostCapabilities::programmable();
        host.encoded_dot_segments = false;
        host.forbidden_headers = [
            "host",
            "content-length",
            "cookie",
            "connection",
            "transfer-encoding",
            "accept-encoding",
            "origin",
        ]
        .into_iter()
        .map(str::to_owned)
        .collect();
        let outcome=self.0.invoke(Cancellation::default(),&host,|request,_|async move{
            let input=json!({"method":request.method.as_str(),"target":request.target,"headers":request.headers.iter().map(|h|json!([h.name(),String::from_utf8_lossy(h.value())])).collect::<Vec<_>>(),"body_hex":request.body.as_ref().map(|b|oac_qualification::hex(b)),"response_limit":request.response_limit}).to_string();
            let result=async{let v=callback.call1(&JsValue::NULL,&JsValue::from_str(&input))?;let text=JsFuture::from(js_sys::Promise::resolve(&v)).await?;let s=text.as_string().ok_or_else(||js_error("expected JSON envelope"))?;serde_json::from_str::<J>(&s).map_err(js_error)}.await;
            match result{Ok(v)=>TransportResult{response:Some(oac_qualification::response_input(&v)),upload:oac_qualification::upload(&v),error_detail:None},Err(_)=>TransportResult{response:None,upload:UploadState::Unknown,error_detail:Some("qualification callback failure".into())}}
        }).await;
        Ok(oac_qualification::outcome_facts(outcome, &self.0).to_string())
    }
}
#[wasm_bindgen]
pub fn live_owners() -> usize {
    live_json_owners()
}
#[wasm_bindgen]
pub fn parse_digest(text: &str) -> Result<String, JsValue> {
    let d = Document::parse(text, None, Limits::default()).map_err(js_error)?;
    Ok(format!("{}:{}", d.source().len(), d.operations().len()))
}
/// Batch core work, so the timing series can separate crossings from parser/index work.
#[wasm_bindgen]
pub fn core_batch(text: &str, iterations: usize) -> Result<usize, JsValue> {
    let mut total = 0usize;
    for _ in 0..iterations {
        let d = Document::parse(std::hint::black_box(text), None, Limits::default())
            .map_err(js_error)?;
        total += std::hint::black_box(d.operations().len());
    }
    Ok(total)
}
/// A string crossing and return value without semantic parsing.
#[wasm_bindgen]
pub fn copy_len(text: &str) -> usize {
    std::hint::black_box(text.len())
}
