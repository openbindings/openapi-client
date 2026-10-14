//! Public-API consumer compiled against extracted Cargo archives, never workspace sources.
#![forbid(unsafe_code)]

use dynamic_openapi_client::exact::Value;
use dynamic_openapi_client::transport::ReplayPolicy;
use dynamic_openapi_client::{
    Cancellation, Document, Header, Limits, Outcome, ParameterLocation, qualification,
};
use dynamic_openapi_client_fetch::{Client, Profile};
use serde_json::{Value as Json, json};
use std::{future::Future, pin::Pin, task::Context};
use wasm_bindgen::prelude::*;

fn js_error(error: impl std::fmt::Display) -> JsValue {
    JsValue::from_str(&error.to_string())
}

fn facts(outcome: Outcome) -> String {
    let response = outcome.status().map(|status| {
        json!({
            "status": status,
            "body_state": format!("{:?}", outcome.body_state().unwrap()),
            "provenance": format!("{:?}", outcome.provenance().unwrap()),
            "raw": outcome.raw().unwrap(),
        })
    });
    json!({
        "dispatch": format!("{:?}", outcome.dispatch()),
        "upload": format!("{:?}", outcome.upload()),
        "cancelled": outcome.cancelled(),
        "replay": outcome.replay_assurance().map(|value| format!("{value:?}")),
        "error": outcome.error().map(|error| format!("{:?}", error.code())),
        "reason": outcome.error().and_then(|error| error.reason()).map(|reason| format!("{reason:?}")),
        "response": response,
    }).to_string()
}

/// The harness polls an ordinary product future and can actually drop it mid-call.
/// No spawned task keeps that future alive after JavaScript calls free().
#[wasm_bindgen]
pub struct FetchCall {
    future: Option<Pin<Box<dyn Future<Output = Outcome>>>>,
    cancellation: Cancellation,
    wire: String,
}

#[wasm_bindgen]
pub struct FetchClient(Client);

#[wasm_bindgen]
impl FetchClient {
    #[wasm_bindgen(constructor)]
    pub fn new(profile: &str) -> Result<FetchClient, JsValue> {
        let profile = match profile {
            "browser" => Profile::Browser,
            "workerd" => Profile::Worker,
            _ => return Err(js_error("unknown qualification host")),
        };
        Client::new(profile).map(Self).map_err(js_error)
    }

    pub fn start(
        &self,
        document: &str,
        input: &str,
        body_limit: usize,
        allow_replay: bool,
        cancel_before: bool,
    ) -> Result<FetchCall, JsValue> {
        let client = self.0.clone();
        let limits = Limits {
            body_bytes: body_limit,
            ..Limits::default()
        };
        let value: Json = serde_json::from_str(input).map_err(js_error)?;
        let headers: Vec<(String, String)> =
            serde_json::from_value(value["headers"].clone()).map_err(js_error)?;
        let operation = Document::parse(document, None, limits.clone())
            .map_err(js_error)?
            .operation_id("probe")
            .map_err(js_error)?;
        // Keep independent preparation for wire inspection; send consumes a fresh builder.
        let build = || -> Result<_, JsValue> {
            let mut request = operation.request();
            for (name, value) in &headers {
                request = request.header(Header::new(name, value));
            }
            if let Some(query) = value["query"].as_object() {
                for (name, value) in query {
                    request = request.parameter_exact(
                        ParameterLocation::Query,
                        name,
                        &Value::parse(value.to_string(), limits.clone()).map_err(js_error)?,
                    );
                }
            }
            Ok(request)
        };
        let prepared = build()?.prepare().map_err(js_error)?;
        let wire = json!({
            "target": prepared.target(),
            "method": prepared.method().as_str(),
            "headers": prepared.headers().iter().map(|header| {
                (header.name(), header.value())
            }).collect::<Vec<_>>(),
        })
        .to_string();
        let cancellation = Cancellation::default();
        if cancel_before {
            cancellation.cancel();
        }
        let token = cancellation.clone();
        let request = build()?.cancellation(token);
        let replay = if allow_replay {
            ReplayPolicy::AllowHostManaged
        } else {
            ReplayPolicy::RequireNoReplay
        };
        let future = Box::pin(async move { client.send(request, replay).await });
        Ok(FetchCall {
            future: Some(future),
            cancellation,
            wire,
        })
    }
}

#[wasm_bindgen]
impl FetchCall {
    pub fn wire(&self) -> String {
        self.wire.clone()
    }

    pub fn cancel(&self) {
        self.cancellation.cancel();
    }

    pub fn is_cancelled(&self) -> bool {
        self.cancellation.is_cancelled()
    }

    /// A noop waker is sufficient because the harness explicitly polls after host turns.
    pub fn poll(&mut self) -> Result<Option<String>, JsValue> {
        let future = self
            .future
            .as_mut()
            .ok_or_else(|| js_error("call already completed"))?;
        let mut context = Context::from_waker(std::task::Waker::noop());
        match future.as_mut().poll(&mut context) {
            std::task::Poll::Pending => Ok(None),
            std::task::Poll::Ready(outcome) => {
                self.future = None;
                Ok(Some(facts(outcome)))
            }
        }
    }
}

#[wasm_bindgen]
pub fn live_owners() -> usize {
    qualification::live_owners()
}

#[wasm_bindgen]
pub fn live_js_values() -> u32 {
    wasm_bindgen::externref_heap_live_count()
}
