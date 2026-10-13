//! Public-API consumer compiled against extracted Cargo archives, never workspace sources.
#![forbid(unsafe_code)]

use dynamic_openapi_client::{
    Cancellation, Document, ExactJson, Header, Input, Limits, Outcome, ParameterInput,
    ParameterLocation, ReplayPolicy, live_json_owners,
};
use dynamic_openapi_client_fetch::{Client, Profile};
use serde_json::{Value, json};
use std::{future::Future, pin::Pin, task::Context};
use wasm_bindgen::prelude::*;

fn js_error(error: impl std::fmt::Display) -> JsValue {
    JsValue::from_str(&error.to_string())
}

fn facts(outcome: Outcome) -> String {
    let response = outcome.response.as_ref().map(|response| {
        json!({
            "status": response.status(),
            "body_state": format!("{:?}", response.body_state()),
            "provenance": format!("{:?}", response.provenance()),
            "raw": response.raw(),
        })
    });
    json!({
        "dispatch": format!("{:?}", outcome.dispatch),
        "upload": format!("{:?}", outcome.upload),
        "cancelled": outcome.cancelled,
        "replay": outcome.replay_assurance().map(|value| format!("{value:?}")),
        "error": outcome.error.as_ref().map(|error| format!("{:?}", error.code())),
        "reason": outcome.error.as_ref().and_then(|error| error.reason()).map(|reason| format!("{reason:?}")),
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
        let value: Value = serde_json::from_str(input).map_err(js_error)?;
        let headers: Vec<(String, String)> =
            serde_json::from_value(value["headers"].clone()).map_err(js_error)?;
        let mut input = Input {
            headers: headers
                .into_iter()
                .map(|(name, value)| Header::new(name, value))
                .collect(),
            ..Input::default()
        };
        if let Some(query) = value["query"].as_object() {
            for (name, value) in query {
                input.parameters.push(ParameterInput {
                    location: ParameterLocation::Query,
                    name: name.clone(),
                    value: ExactJson::parse(value.to_string(), limits.clone())
                        .map_err(js_error)?
                        .root(),
                });
            }
        }
        let prepared = Document::parse(document, None, limits)
            .map_err(js_error)?
            .operation_id("probe")
            .map_err(js_error)?
            .prepare(&input)
            .map_err(js_error)?;
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
        let future = Box::pin(async move {
            if allow_replay {
                client
                    .execute_with_replay(&prepared, token, ReplayPolicy::AllowHostManaged)
                    .await
            } else {
                client.execute(&prepared, token).await
            }
        });
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
    live_json_owners()
}

#[wasm_bindgen]
pub fn live_js_values() -> u32 {
    wasm_bindgen::externref_heap_live_count()
}
