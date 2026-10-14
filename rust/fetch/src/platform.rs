use crate::{Profile, admission};
use dynamic_openapi_client::diagnostics::HostRefusal;
use dynamic_openapi_client::transport::{
    BodyState, HeaderDelivery, HeaderFidelity, PreparedRequest, Provenance, ReplayPolicy,
    ResponseAdmission, ResponseInput, TransportResult, UploadState,
};
use dynamic_openapi_client::{Cancellation, Diagnostic, Header, Outcome};
use js_sys::{Array, JsString, Object, Promise, Reflect, Uint8Array};
use std::{
    collections::BTreeSet,
    future::{Future, poll_fn},
    pin::Pin,
    sync::Arc,
    task::{Context, Poll},
};
use wasm_bindgen::{JsCast, prelude::*};
use wasm_bindgen_futures::JsFuture;

#[wasm_bindgen(module = "/src/host.js")]
extern "C" {
    #[wasm_bindgen(catch)]
    fn capture() -> Result<JsValue, JsValue>;
    #[wasm_bindgen(catch)]
    fn controller(bindings: &JsValue) -> Result<JsValue, JsValue>;
    #[wasm_bindgen(catch, js_name=request)]
    fn construct(bindings: &JsValue, target: &str, init: &JsValue) -> Result<JsValue, JsValue>;
    #[wasm_bindgen(catch, js_name=fetchOnce)]
    fn fetch_once(bindings: &JsValue, request: &JsValue) -> Result<JsValue, JsValue>;
    #[wasm_bindgen(catch)]
    fn get(object: &JsValue, key: &str) -> Result<JsValue, JsValue>;
    #[wasm_bindgen(catch)]
    fn call0(object: &JsValue, key: &str) -> Result<JsValue, JsValue>;
    #[wasm_bindgen(catch)]
    fn call1(object: &JsValue, key: &str, argument: &JsValue) -> Result<JsValue, JsValue>;
    #[wasm_bindgen(catch)]
    fn watch(original: &JsValue) -> Result<JsValue, JsValue>;
    #[wasm_bindgen(catch)]
    fn stop(observation: &JsValue) -> Result<(), JsValue>;
    #[wasm_bindgen(catch, js_name=yieldTask)]
    fn yield_task(bindings: &JsValue) -> Result<JsValue, JsValue>;
    #[wasm_bindgen(catch, js_name=observeClosed)]
    fn observe_closed(reader: &JsValue) -> Result<(), JsValue>;
    #[wasm_bindgen(catch)]
    fn cleanup(controller: &JsValue, reader: &JsValue, abort: bool) -> Result<(), JsValue>;
}

#[derive(Clone)]
pub(crate) struct Bindings(JsValue);
struct Guard {
    controller: JsValue,
    reader: JsValue,
    abort: bool,
}
impl Drop for Guard {
    fn drop(&mut self) {
        let _ = cleanup(&self.controller, &self.reader, self.abort);
    }
}
struct Preflight {
    request: JsValue,
    guard: Guard,
}

// JsFuture is attached only to a wrapper that our Drop settles. An unresolved
// original keeps JS-only handlers. Rust callback/waker cleanup needs a microtask.
struct Wait {
    observation: JsValue,
    future: JsFuture,
}
impl Wait {
    fn observed(observation: JsValue) -> Result<Self, ()> {
        let promise = get(&observation, "promise")
            .map_err(|_| ())?
            .dyn_into::<Promise>()
            .map_err(|_| ())?;
        Ok(Self {
            observation,
            future: JsFuture::from(promise),
        })
    }
    fn new(promise: &JsValue) -> Result<Self, ()> {
        Self::observed(watch(promise).map_err(|_| ())?)
    }
}
impl Drop for Wait {
    fn drop(&mut self) {
        let _ = stop(&self.observation);
    }
}
impl Future for Wait {
    type Output = Result<JsValue, ()>;
    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        match Pin::new(&mut self.future).poll(cx) {
            Poll::Pending => Poll::Pending,
            Poll::Ready(Ok(result)) => {
                let array = Array::from(&result);
                Poll::Ready(if array.get(0).as_bool() == Some(true) {
                    Ok(array.get(1))
                } else {
                    Err(())
                })
            }
            Poll::Ready(Err(_)) => Poll::Ready(Err(())),
        }
    }
}
async fn wait(wait: Wait, cancellation: &Cancellation) -> Result<JsValue, ()> {
    let mut waiting = std::pin::pin!(wait);
    let mut cancelled = std::pin::pin!(cancellation.cancelled());
    poll_fn(|cx| {
        if cancelled.as_mut().poll(cx).is_ready() {
            return Poll::Ready(Err(()));
        }
        waiting.as_mut().poll(cx)
    })
    .await
}
impl Bindings {
    pub(crate) fn new() -> Result<Self, Diagnostic> {
        capture()
            .map(Self)
            .map_err(|_| HostRefusal::Unavailable.into())
    }
    pub(crate) async fn execute(
        &self,
        profile: Profile,
        prepared: &PreparedRequest,
        cancellation: Cancellation,
        replay: ReplayPolicy,
    ) -> Outcome {
        if let Err(error) = cancellation.check() {
            return Outcome::not_dispatched(error, true, None);
        }
        let capabilities = admission::capabilities(profile, prepared);
        if let Err(error) = capabilities.admit_with_replay(prepared, replay) {
            return Outcome::not_dispatched(error, cancellation.is_cancelled(), None);
        }
        let preflight = match self.preflight(profile, prepared, &cancellation) {
            Ok(request) => request,
            Err(error) => return Outcome::not_dispatched(error, cancellation.is_cancelled(), None),
        };
        prepared
            .invoke_with_replay(cancellation, &capabilities, replay, |dispatch, token| {
                self.exchange(profile, preflight, dispatch.response_admission(), token)
            })
            .await
    }
    fn preflight(
        &self,
        profile: Profile,
        prepared: &PreparedRequest,
        cancellation: &Cancellation,
    ) -> Result<Preflight, Diagnostic> {
        let prepared_method = prepared.method();
        let method = prepared_method.as_str();
        if ["CONNECT", "TRACE", "TRACK"]
            .iter()
            .any(|m| method.eq_ignore_ascii_case(m))
        {
            return Err(HostRefusal::Method.into());
        }
        if matches!(method, "GET" | "HEAD") && prepared.body().is_some() {
            return Err(HostRefusal::RequestBody.into());
        }
        let mut names = BTreeSet::new();
        let headers = Array::new();
        for header in prepared.headers() {
            cancellation.check()?;
            if !names.insert(header.name().to_ascii_lowercase()) {
                return Err(HostRefusal::ForbiddenHeader.into());
            }
            let value = outbound(profile, header.value())
                .ok_or_else(|| Diagnostic::from(HostRefusal::ForbiddenHeader))?;
            headers.push(&Array::of2(&JsValue::from_str(header.name()), &value));
        }
        let controller =
            controller(&self.0).map_err(|_| Diagnostic::from(HostRefusal::Unavailable))?;
        let guard = Guard {
            controller,
            reader: JsValue::UNDEFINED,
            abort: true,
        };
        let init = Object::new();
        set(&init, "method", &JsValue::from_str(method))?;
        set(&init, "headers", &headers)?;
        set(&init, "redirect", &JsValue::from_str("manual"))?;
        set(&init, "credentials", &JsValue::from_str("omit"))?;
        set(&init, "referrer", &JsValue::from_str(""))?;
        if profile == Profile::Browser {
            set(&init, "mode", &JsValue::from_str("cors"))?;
        }
        set(
            &init,
            "signal",
            &get(&guard.controller, "signal")
                .map_err(|_| Diagnostic::from(HostRefusal::Unavailable))?,
        )?;
        if let Some(body) = prepared.body() {
            set(&init, "body", &Uint8Array::from(body.as_ref()))?;
        }
        cancellation.check()?;
        let request = construct(&self.0, prepared.target(), &init)
            .map_err(|_| Diagnostic::from(HostRefusal::Method))?;
        drop(init); // Release the temporary Uint8Array after Request owns its body.
        drop(headers);
        if get(&request, "url").ok().as_ref() != Some(&JsValue::from_str(prepared.target())) {
            return Err(HostRefusal::ExactTargets.into());
        }
        if get(&request, "method").ok().as_ref() != Some(&JsValue::from_str(method)) {
            return Err(HostRefusal::Method.into());
        }
        let actual =
            get(&request, "headers").map_err(|_| Diagnostic::from(HostRefusal::ForbiddenHeader))?;
        for header in prepared.headers() {
            cancellation.check()?;
            let expected = outbound(profile, header.value())
                .ok_or_else(|| Diagnostic::from(HostRefusal::ForbiddenHeader))?;
            let observed = call1(&actual, "get", &JsValue::from_str(header.name()))
                .map_err(|_| Diagnostic::from(HostRefusal::ForbiddenHeader))?;
            if observed != expected {
                return Err(HostRefusal::ForbiddenHeader.into());
            }
        }
        Ok(Preflight { request, guard })
    }
    async fn exchange(
        &self,
        profile: Profile,
        preflight: Preflight,
        admission: ResponseAdmission,
        cancellation: Cancellation,
    ) -> TransportResult {
        let Preflight { request, mut guard } = preflight;
        if cancellation.is_cancelled() {
            return failed(
                None,
                UploadState::NotStarted,
                "local cancellation before Fetch",
            );
        }
        let promise = match fetch_once(&self.0, &request) {
            Ok(p) => p,
            Err(_) => return failed(None, UploadState::Unknown, "Fetch dispatch failed"),
        };
        let response = match Wait::new(&promise) {
            Ok(w) => match wait(w, &cancellation).await {
                Ok(r) => r,
                Err(()) => {
                    return failed(
                        None,
                        UploadState::Unknown,
                        "Fetch wait failed or was locally cancelled",
                    );
                }
            },
            Err(()) => return failed(None, UploadState::Unknown, "Fetch wait unavailable"),
        };
        // Declared before any body parsing, independent of type/labels/payload.
        let provenance = Provenance::HostDelivered;
        let status = get(&response, "status")
            .ok()
            .and_then(|v| v.as_f64())
            .filter(|n| n.is_finite() && n.fract() == 0.0 && *n >= 0.0 && *n <= u16::MAX as f64)
            .unwrap_or(0.0) as u16;
        let opaque =
            get(&response, "type").ok().as_ref() == Some(&JsValue::from_str("opaqueredirect"));
        let (headers, delivery) = headers(profile, &response, admission, &cancellation);
        let mut bytes = Vec::new();
        let (state, detail) = match get(&response, "body") {
            Ok(body) if body.is_null() => {
                guard.abort = false;
                (BodyState::Complete, None)
            }
            Ok(body) => match call0(&body, "getReader") {
                Ok(reader) => {
                    guard.reader = reader;
                    let _ = observe_closed(&guard.reader);
                    let mut turns = 0;
                    loop {
                        if cancellation.is_cancelled() {
                            break (BodyState::Failed, Some("local cancellation during body"));
                        }
                        let value = match call0(&guard.reader, "read")
                            .ok()
                            .and_then(|p| Wait::new(&p).ok())
                        {
                            Some(w) => match wait(w, &cancellation).await {
                                Ok(v) => v,
                                Err(()) => {
                                    break (
                                        BodyState::Failed,
                                        Some("body read failed or was locally cancelled"),
                                    );
                                }
                            },
                            None => break (BodyState::Failed, Some("body read unavailable")),
                        };
                        match get(&value, "done").ok().and_then(|v| v.as_bool()) {
                            Some(true) => {
                                guard.abort = false;
                                break (BodyState::Complete, None);
                            }
                            Some(false) => {}
                            None => break (BodyState::Failed, Some("invalid body read evidence")),
                        }
                        let chunk = match get(&value, "value")
                            .ok()
                            .and_then(|v| v.dyn_into::<Uint8Array>().ok())
                        {
                            Some(v) => v,
                            None => break (BodyState::Failed, Some("body chunk is not bytes")),
                        };
                        let size = chunk.length() as usize;
                        let remaining = admission.body_bytes() - bytes.len();
                        let copy = size.min(remaining);
                        let start = bytes.len();
                        admission::grow(&mut bytes, start + copy, admission.body_bytes());
                        bytes.resize(start + copy, 0);
                        chunk.subarray(0, copy as u32).copy_to(&mut bytes[start..]);
                        if size > remaining {
                            break (BodyState::Truncated, None);
                        }
                        turns += 1;
                        if turns == 32 {
                            turns = 0;
                            match yield_task(&self.0)
                                .ok()
                                .and_then(|o| Wait::observed(o).ok())
                            {
                                Some(w) => {
                                    if wait(w, &cancellation).await.is_err() {
                                        break (
                                            BodyState::Failed,
                                            Some("local cancellation during body yield"),
                                        );
                                    }
                                }
                                None => break (BodyState::Failed, Some("body yield unavailable")),
                            }
                        }
                    }
                }
                Err(_) => (BodyState::Failed, Some("body reader unavailable")),
            },
            Err(_) => (BodyState::Failed, Some("response body unavailable")),
        };
        let mut input = ResponseInput::new(status, headers, bytes.into(), state, provenance);
        input.opaque_redirect = opaque;
        input.header_fidelity = HeaderFidelity::HostProcessed;
        input.header_delivery = delivery;
        // Guard performs independent cleanup before this owned evidence returns.
        drop(guard);
        TransportResult {
            response: Some(input),
            upload: UploadState::Unknown,
            error_detail: detail.map(Arc::from),
        }
    }
}
fn failed(
    response: Option<ResponseInput>,
    upload: UploadState,
    detail: &'static str,
) -> TransportResult {
    TransportResult {
        response,
        upload,
        error_detail: Some(Arc::from(detail)),
    }
}
fn set(object: &JsValue, key: &str, value: &JsValue) -> Result<(), Diagnostic> {
    Reflect::set(object, &JsValue::from_str(key), value)
        .map(|_| ())
        .map_err(|_| HostRefusal::Unavailable.into())
}
fn outbound(profile: Profile, bytes: &[u8]) -> Option<JsValue> {
    Some(match profile {
        Profile::Browser => {
            let len = bytes
                .iter()
                .try_fold(0usize, |n, b| n.checked_add(if *b < 128 { 1 } else { 2 }))?;
            let mut string = String::with_capacity(len);
            string.extend(bytes.iter().map(|b| char::from(*b)));
            JsValue::from_str(&string)
        }
        Profile::Worker => JsValue::from_str(std::str::from_utf8(bytes).ok()?),
    })
}
fn inbound(
    profile: Profile,
    value: JsValue,
    max: usize,
    cancellation: &Cancellation,
) -> Option<Vec<u8>> {
    let string = value.dyn_into::<JsString>().ok()?;
    admission::string_bytes(
        profile,
        string.length(),
        max,
        |i| string.char_code_at(i) as u16,
        cancellation,
    )
}
fn headers(
    profile: Profile,
    response: &JsValue,
    admission: ResponseAdmission,
    cancellation: &Cancellation,
) -> (Vec<Header>, HeaderDelivery) {
    let mut output = Vec::new();
    let mut bytes = 0usize;
    let Some(iterator) = get(response, "headers")
        .ok()
        .and_then(|h| call0(&h, "entries").ok())
    else {
        return (output, HeaderDelivery::Incomplete);
    };
    loop {
        if cancellation.is_cancelled() {
            return (output, HeaderDelivery::Incomplete);
        }
        let Ok(next) = call0(&iterator, "next") else {
            return (output, HeaderDelivery::Incomplete);
        };
        if get(&next, "done").ok().and_then(|v| v.as_bool()) == Some(true) {
            return (output, HeaderDelivery::Complete);
        }
        if output.len() == admission.header_entries() {
            return (output, HeaderDelivery::Incomplete);
        }
        let Some(pair) = get(&next, "value")
            .ok()
            .and_then(|v| v.dyn_into::<Array>().ok())
        else {
            return (output, HeaderDelivery::Incomplete);
        };
        if pair.length() != 2 {
            return (output, HeaderDelivery::Incomplete);
        }
        let remaining = admission.header_bytes() - bytes;
        // Admit BOTH encoded field sizes while they are JS handles, before any
        // owned Rust name/value copy. Oversized host strings never use as_string.
        let (Ok(name), Ok(value)) = (
            pair.get(0).dyn_into::<JsString>(),
            pair.get(1).dyn_into::<JsString>(),
        ) else {
            return (output, HeaderDelivery::Incomplete);
        };
        let Some(name_len) = admission::string_len(
            profile,
            name.length(),
            remaining,
            |i| name.char_code_at(i) as u16,
            cancellation,
        ) else {
            return (output, HeaderDelivery::Incomplete);
        };
        let Some(value_len) = admission::string_len(
            profile,
            value.length(),
            remaining - name_len,
            |i| value.char_code_at(i) as u16,
            cancellation,
        ) else {
            return (output, HeaderDelivery::Incomplete);
        };
        let Some(name) = inbound(profile, name.into(), name_len, cancellation) else {
            return (output, HeaderDelivery::Incomplete);
        };
        let Some(value) = inbound(profile, value.into(), value_len, cancellation) else {
            return (output, HeaderDelivery::Incomplete);
        };
        let Ok(name) = String::from_utf8(name) else {
            return (output, HeaderDelivery::Incomplete);
        };
        bytes += name.len() + value.len();
        let length = output.len() + 1;
        admission::grow(&mut output, length, admission.header_entries());
        output.push(Header::new(name, value));
    }
}
