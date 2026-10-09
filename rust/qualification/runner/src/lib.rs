//! Qualification-only public-API adapter and assertion harness.
use dynamic_openapi_client::*;
use serde_json::{Value as J, json};
use std::{
    collections::BTreeMap,
    future::Future,
    sync::Arc,
    task::{Context, Poll, Waker},
    time::Instant,
};

pub fn block_on<F: Future>(f: F) -> F::Output {
    let mut f = Box::pin(f);
    let mut cx = Context::from_waker(Waker::noop());
    loop {
        if let Poll::Ready(v) = f.as_mut().poll(&mut cx) {
            return v;
        }
        std::thread::yield_now();
    }
}
pub fn hex(b: &[u8]) -> String {
    b.iter().map(|b| format!("{b:02x}")).collect()
}
pub fn unhex(s: &str) -> Vec<u8> {
    (0..s.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(&s[i..i + 2], 16).unwrap())
        .collect()
}
pub fn limits(c: &J) -> Limits {
    let mut l = Limits::default();
    if let Some(o) = c.get("limits").and_then(J::as_object) {
        for (k, v) in o {
            let n = v.as_u64().unwrap() as usize;
            match k.as_str() {
                "document_bytes" => l.document_bytes = n,
                "depth" => l.depth = n,
                "nodes" => l.nodes = n,
                "operations" => l.operations = n,
                "reference_steps" => l.reference_steps = n,
                "body_bytes" => l.body_bytes = n,
                "target_bytes" => l.target_bytes = n,
                "headers" => l.headers = n,
                "number_conversion_chars" => l.number_conversion_chars = n,
                "cached_operations" => l.cached_operations = n,
                _ => panic!("unknown limit"),
            }
        }
    }
    l
}
pub fn input(c: &J) -> Input {
    let mut input = Input::default();
    input.selection.server = c.get("server").and_then(J::as_u64).map(|n| n as usize);
    input.selection.security = c.get("security").and_then(J::as_u64).map(|n| n as usize);
    input.selection.media = c.get("media").and_then(J::as_str).map(str::to_owned);
    if let Some(v) = c.get("variables").and_then(J::as_object) {
        input.selection.variables = v
            .iter()
            .map(|(k, v)| (k.clone(), v.as_str().unwrap().to_owned()))
            .collect();
    }
    if let Some(a) = c.get("parameters").and_then(J::as_array) {
        for p in a {
            input.parameters.push(ParameterInput {
                location: ParameterLocation::parse(p["location"].as_str().unwrap()).unwrap(),
                name: p["name"].as_str().unwrap().to_owned(),
                value: ExactJson::parse(p["value"].as_str().unwrap(), Limits::default())
                    .unwrap()
                    .root(),
            });
        }
    }
    if let Some(b) = c.get("body") {
        if let Some(s) = b.get("json").and_then(J::as_str) {
            input.body = Body::Json(ExactJson::parse(s, Limits::default()).unwrap().root());
        } else if let Some(s) = b.get("raw_hex").and_then(J::as_str) {
            input.body = Body::Raw(unhex(s).into());
        }
    }
    if let Some(a) = c.get("headers").and_then(J::as_array) {
        input.headers = a
            .iter()
            .map(|h| Header::new(h[0].as_str().unwrap(), h[1].as_str().unwrap()))
            .collect();
    }
    if let Some(credentials) = c.get("credentials").and_then(J::as_object) {
        for (name, cr) in credentials {
            let value = match cr["kind"].as_str().unwrap() {
                "basic" => CredentialValue::Basic {
                    username: cr["username"].as_str().unwrap().to_owned(),
                    password: cr["password"].as_str().unwrap().to_owned(),
                },
                "bearer" => CredentialValue::Bearer(cr["value"].as_str().unwrap().to_owned()),
                _ => CredentialValue::ApiKey(cr["value"].as_str().unwrap().to_owned()),
            };
            input.credentials.insert(
                name.clone(),
                Credential {
                    value,
                    origins: cr["origins"]
                        .as_array()
                        .unwrap()
                        .iter()
                        .map(|s| s.as_str().unwrap().to_owned())
                        .collect(),
                },
            );
        }
    }
    input
}
fn err(e: Diagnostic) -> J {
    let display = e.to_string();
    json!({"error":format!("{:?}",e.code()),"location":e.location().map(|l|json!({"start":l.start,"end":l.end,"pointer":l.pointer})),"related":e.related().len(),"setting":e.setting(),"display":display})
}
fn headers(h: &[Header]) -> J {
    json!(
        h.iter()
            .map(|h| json!([h.name(), String::from_utf8_lossy(h.value())]))
            .collect::<Vec<_>>()
    )
}
pub fn request_facts(p: &PreparedRequest) -> J {
    json!({"method":p.method().as_str(),"target":p.target(),"headers":headers(p.headers()),"body_kind":p.body_kind(),"body_hex":p.body().map(hex),"safe_debug":format!("{p:?}")})
}
pub fn operation(d: &Document, c: &J) -> Result<Operation, Diagnostic> {
    if let Some(id) = c.get("operation_id").and_then(J::as_str) {
        d.operation_id(id)
    } else {
        d.operation(
            c.get("path").and_then(J::as_str).unwrap_or("/items"),
            Method::parse(c.get("method").and_then(J::as_str).unwrap_or("get")).unwrap(),
        )
    }
}
pub fn response_input(r: &J) -> ResponseInput {
    ResponseInput {
        status: r.get("status").and_then(J::as_u64).unwrap_or(200) as u16,
        headers: r
            .get("headers")
            .and_then(J::as_array)
            .map(|a| {
                a.iter()
                    .map(|v| Header::new(v[0].as_str().unwrap(), v[1].as_str().unwrap()))
                    .collect()
            })
            .unwrap_or_default(),
        body: DeliveredBody {
            bytes: unhex(r.get("body_hex").and_then(J::as_str).unwrap_or("")).into(),
            state: if r.get("body_error").is_some() {
                BodyState::Failed
            } else if r.get("truncated").and_then(J::as_bool) == Some(true) {
                BodyState::Truncated
            } else {
                BodyState::Complete
            },
            provenance: match r.get("provenance").and_then(J::as_str) {
                Some("wire") => Provenance::Wire,
                Some("decoded") => Provenance::Decoded,
                _ => Provenance::Unknown,
            },
        },
        opaque_redirect: r.get("opaque_redirect").and_then(J::as_bool) == Some(true),
    }
}
pub fn upload(r: &J) -> UploadState {
    match r.get("upload").and_then(J::as_str) {
        Some("complete") => UploadState::Complete,
        Some("incomplete") => UploadState::Incomplete,
        _ => UploadState::Unknown,
    }
}
pub fn outcome_facts(out: Outcome, p: &PreparedRequest) -> J {
    let mut facts = json!({"dispatch":if out.dispatch==DispatchEvidence::Dispatched{"dispatched"}else{"not_dispatched"},"upload":format!("{:?}",out.upload).to_ascii_lowercase(),"cancelled":out.cancelled,"safe_debug":format!("{out:?}")});
    if let Some(e) = out.error {
        facts["error"] = json!(format!("{:?}", e.code()));
        facts["display"] = json!(e.to_string());
    }
    if let Some(r) = out.response {
        facts["response_status"] = json!(r.status());
        facts["omitted_header_lines"] = json!(r.omitted_header_lines());
        facts["headers"] = headers(r.headers());
        facts["raw_hex"] = json!(hex(r.raw()));
        facts["body_state"] = json!(format!("{:?}", r.body_state()).to_ascii_lowercase());
        facts["provenance"] = json!(format!("{:?}", r.provenance()).to_ascii_lowercase());
        facts["response_key"] = json!(
            p.response_declaration(r.status())
                .map(|(k, _)| k.to_owned())
        );
        match r.json() {
            Ok(v) => facts["json_raw"] = json!(v.root().raw()),
            Err(e) => facts["decode_error"] = json!(format!("{:?}", e.code())),
        }
    }
    facts
}
pub fn run(c: &J) -> J {
    let action = c["action"].as_str().unwrap();
    if action == "uri" {
        return match uri::resolve(
            c["reference"].as_str().unwrap(),
            c["base"].as_str().unwrap(),
        ) {
            Ok(s) => json!({"resolved":s}),
            Err(e) => err(e),
        };
    }
    if action == "number" {
        let j = ExactJson::parse(c["number"].as_str().unwrap(), limits(c)).unwrap();
        let n = j.root();
        let n = n.number().unwrap();
        let mut f = json!({});
        match n.to_i64() {
            Ok(i) => f["integer"] = json!(i.to_string()),
            Err(e) => f["integer_error"] = json!(format!("{:?}", e.code())),
        };
        match n.to_f64_exact() {
            Ok(i) => f["float"] = json!(i.to_string()),
            Err(e) => f["float_error"] = json!(format!("{:?}", e.code())),
        };
        return f;
    }
    if action == "limits" {
        return limit_case(c);
    }
    if action == "depth_drop" {
        let before = live_json_owners();
        let depth = c["depth"].as_u64().unwrap() as usize;
        let s = format!("{}0{}", "[".repeat(depth), "]".repeat(depth));
        for _ in 0..c["cycles"].as_u64().unwrap() {
            drop(ExactJson::parse(s.as_bytes(), Limits::default()).unwrap());
        }
        return json!({"passed":live_json_owners()==before,"live_delta":live_json_owners() as i64-before as i64});
    }
    let bytes = c
        .get("document_hex")
        .and_then(J::as_str)
        .map(unhex)
        .unwrap_or_else(|| c["document"].as_str().unwrap().as_bytes().to_vec());
    if action == "ownership" {
        let before = live_json_owners();
        let mut survived = true;
        let mut source_preserved = true;
        for _ in 0..c["cycles"].as_u64().unwrap() {
            let owned = bytes.clone();
            let d = Document::parse(&owned, None, limits(c)).unwrap();
            let op = d.operations().next().unwrap();
            source_preserved &= d.source().as_bytes() == owned;
            let root = d.root();
            drop(owned);
            drop(d);
            survived &= op.id().as_deref() == Some("items") && root.get("paths").is_some();
        }
        return json!({"source_preserved":source_preserved,"survived":survived,"live_delta":live_json_owners() as i64-before as i64});
    }
    let d = match Document::parse(&bytes, c.get("base").and_then(J::as_str), limits(c)) {
        Ok(d) => d,
        Err(e) => {
            let mut f = err(e.diagnostic().clone());
            f["source_preserved"] = json!(e.source_bytes() == bytes);
            return f;
        }
    };
    if action == "document" {
        let mut f = json!({"source_preserved":d.source().as_bytes()==bytes,"version":d.version(),"operation_count":d.operations().len(),"paths":d.operations().map(|o|o.path().to_owned()).collect::<Vec<_>>(),"operation_ids":d.operations().map(|o|o.id()).collect::<Vec<_>>()});
        if let Some(pointer) = c.get("select").and_then(J::as_str) {
            match d.root().pointer(pointer) {
                Ok(v) => f["selected_raw"] = json!(v.raw()),
                Err(e) if e.code() == Code::MissingReference => f["selected_raw"] = J::Null,
                Err(e) => f["selection_error"] = json!(format!("{:?}", e.code())),
            }
        }
        return f;
    }
    if action == "concurrency" {
        let mut threads = vec![];
        for _ in 0..c["threads"].as_u64().unwrap() {
            let d = d.clone();
            let n = c["iterations"].as_u64().unwrap();
            threads.push(std::thread::spawn(move || {
                for _ in 0..n {
                    let p = d
                        .operation_id("items")
                        .unwrap()
                        .prepare(&Input::default())
                        .unwrap();
                    assert_eq!(p.target(), "https://api.example/v1/items");
                }
            }));
        }
        for t in threads {
            t.join().unwrap();
        }
        return json!({"passed":d.cached_operations()==1,"cached_operations":d.cached_operations()});
    }
    let op = match operation(&d, c) {
        Ok(o) => o,
        Err(e) => return err(e),
    };
    let preparation_cancel = Cancellation::default();
    if c.get("cancel").and_then(J::as_str) == Some("prepare") {
        preparation_cancel.cancel();
    }
    let p = match op.prepare_with_cancellation(&input(c), &preparation_cancel) {
        Ok(p) => p,
        Err(e) => return err(e),
    };
    if action == "prepare" {
        return request_facts(&p);
    }
    if action == "exchange" {
        let cancellation = Cancellation::default();
        if c.get("cancel").and_then(J::as_str) == Some("before") {
            cancellation.cancel();
        }
        let host = HostCapabilities::programmable();
        let out = block_on(p.invoke(cancellation, &host, |_request, cancel| {
            if c.get("cancel").and_then(J::as_str) == Some("during") {
                cancel.cancel();
            }
            std::future::ready(TransportResult {
                response: if c.get("transport_error").is_some() {
                    None
                } else {
                    Some(response_input(c.get("response").unwrap_or(&J::Null)))
                },
                upload: upload(c.get("response").unwrap_or(&J::Null)),
                error_detail: c.get("transport_error").and_then(J::as_str).map(Arc::from),
            })
        }));
        return outcome_facts(out, &p);
    }
    panic!("unknown executable action {action}")
}
pub fn check(expected: &J, facts: &J) -> Result<(), String> {
    for (k, v) in expected.as_object().unwrap() {
        if k == "display_excludes" {
            let needle = v.as_str().unwrap();
            if facts
                .get("display")
                .and_then(J::as_str)
                .is_some_and(|s| s.contains(needle))
                || facts["safe_debug"]
                    .as_str()
                    .is_some_and(|s| s.contains(needle))
            {
                return Err(format!("sensitive display contains {needle}"));
            }
        } else if facts.get(k) != Some(v) {
            return Err(format!(
                "{k}: expected {v}, observed {}",
                facts.get(k).unwrap_or(&J::Null)
            ));
        }
    }
    Ok(())
}
fn minimal(operations: usize) -> J {
    json!({"openapi":"3.1.2","info":{"title":"limits","version":"1"},"servers":[{"url":"https://api.example"}],"paths":(0..operations).map(|i|(format!("/{i}"),json!({"get":{"operationId":format!("op{i}"),"responses":{"200":{"description":"ok"}}}}))).collect::<BTreeMap<_,_>>()})
}
fn limit_case(c: &J) -> J {
    let bound = c["bound"].as_str().unwrap();
    let limit = c["limit"].as_u64().unwrap() as usize;
    let position = c["position"].as_str().unwrap();
    let n = match position {
        "below" => limit - 1,
        "at" => limit,
        "above" => limit + 1,
        _ => panic!(),
    };
    let mut l = Limits::default();
    let mut result: Result<(), Code> = Ok(());
    let mut count = None;
    match bound {
        "document_bytes" => {
            l.document_bytes = limit;
            let s = format!("{}{{}}", " ".repeat(n - 2));
            result = ExactJson::parse(s, l)
                .map(|_| ())
                .map_err(|e| e.diagnostic().code());
        }
        "depth" => {
            l.depth = limit;
            let s = format!("{}0{}", "[".repeat(n), "]".repeat(n));
            result = ExactJson::parse(s, l)
                .map(|_| ())
                .map_err(|e| e.diagnostic().code());
        }
        "nodes" => {
            l.nodes = limit;
            let s = format!("[{}]", vec!["0"; n - 1].join(","));
            result = ExactJson::parse(s, l)
                .map(|_| ())
                .map_err(|e| e.diagnostic().code());
        }
        "operations" => {
            l.operations = limit;
            result = Document::parse(minimal(n).to_string(), None, l)
                .map(|_| ())
                .map_err(|e| e.diagnostic().code());
        }
        "reference_steps" => {
            l.reference_steps = limit;
            let mut d = minimal(0);
            let mut params = BTreeMap::new();
            for i in 0..n {
                params.insert(
                    format!("p{i}"),
                    json!({"$ref":format!("#/components/parameters/p{}",i+1)}),
                );
            }
            params.insert(
                format!("p{n}"),
                json!({"name":"q","in":"query","schema":{}}),
            );
            d["components"] = json!({"parameters":params});
            let d = Document::parse(d.to_string(), None, l).unwrap();
            result = d
                .resolve_protocol("/components/parameters/p0", ReferenceKind::Parameter)
                .map(|_| ())
                .map_err(|e| e.code());
        }
        "body_bytes" => {
            l.body_bytes = limit;
            let mut d = minimal(1);
            d["paths"]["/0"]["get"]["requestBody"] =
                json!({"content":{"application/octet-stream":{}}});
            let d = Document::parse(d.to_string(), None, l).unwrap();
            let i = Input {
                body: Body::Raw(vec![0; n].into()),
                ..Input::default()
            };
            result = d
                .operation_id("op0")
                .unwrap()
                .prepare(&i)
                .map(|_| ())
                .map_err(|e| e.code());
        }
        "number_conversion_chars" => {
            l.number_conversion_chars = limit;
            let j = ExactJson::parse("1".repeat(n), l).unwrap();
            result = j
                .root()
                .number()
                .unwrap()
                .to_i64()
                .map(|_| ())
                .map_err(|e| e.code());
        }
        "cached_operations" => {
            l.cached_operations = limit;
            let d = Document::parse(minimal(n).to_string(), None, l).unwrap();
            for op in d.operations() {
                op.prepare(&Input::default()).unwrap();
            }
            count = Some(d.cached_operations());
            assert_eq!(count, Some(n.min(limit)));
        }
        _ => panic!(),
    }
    let expected_error = position == "above" && bound != "cached_operations";
    json!({"passed":if expected_error{result==Err(Code::Limit)}else{result.is_ok()},"bound":bound,"input_measure":n,"limit":limit,"error":result.err().map(|e|format!("{e:?}")),"cached_operations":count})
}
/// Finite mutation campaign; input generation and seed are deterministic.
pub fn campaign(family: &str, seconds: u64, seed: u64) -> J {
    let started = Instant::now();
    let mut rng = seed;
    let mut iterations = 0u64;
    let mut accepted = 0u64;
    let before = live_json_owners();
    let normal = std::fs::read_to_string(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../fixtures/normal.json"
    ))
    .unwrap();
    let doc = Document::parse(&normal, None, Limits::default()).unwrap();
    while started.elapsed().as_secs_f64() < seconds as f64 {
        rng = rng
            .wrapping_mul(6364136223846793005)
            .wrapping_add(1442695040888963407);
        let n = (rng as usize % 96) + 1;
        let bytes = (0..n)
            .map(|i| ((rng.rotate_left(i as u32 % 64) >> 24) & 255) as u8)
            .collect::<Vec<_>>();
        let s = String::from_utf8_lossy(&bytes);
        let step = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| match family {
            "json" => {
                let l = Limits {
                    document_bytes: 4096,
                    depth: 64,
                    nodes: 2048,
                    ..Limits::default()
                };
                let x = if iterations.is_multiple_of(4) {
                    format!("{{\"x\":\"{}\"}}", s)
                } else if iterations % 4 == 1 {
                    format!("{}0{}", "[".repeat(n), "]".repeat(n))
                } else {
                    s.to_string()
                };
                accepted += u64::from(ExactJson::parse(x, l).is_ok());
            }
            "pointer" => {
                let p = format!("/paths/~{}{}", rng % 4, s);
                accepted += u64::from(doc.root().pointer(&p).is_ok());
            }
            "uri" => {
                let r = format!(
                    "{}{}",
                    ["../", "%2e/", "//", "?", "http:"][rng as usize % 5],
                    s
                );
                accepted += u64::from(uri::resolve(&r, "https://api.example/a/b?c").is_ok());
            }
            "selection" => {
                let mut d = minimal(1);
                d["servers"] = match rng % 6 {
                    0 => json!([{"url":s.to_string()}]),
                    1 => json!(null),
                    2 => {
                        json!([{"url":"https://api.example/{x}","variables":{"x":{"default":s.to_string(),"enum":["allowed"]}}}])
                    }
                    3 => json!([{"url":123}]),
                    4 => json!([{"url":"https://one.example"},{"url":"https://two.example"}]),
                    _ => json!([{"url":"https://api.example"}]),
                };
                let d = Document::parse(d.to_string(), None, Limits::default()).unwrap();
                accepted += u64::from(
                    d.operation_id("op0")
                        .unwrap()
                        .prepare(&Input::default())
                        .is_ok(),
                );
            }
            "parameters" => {
                let data = match rng % 5 {
                    0 => json!(s.as_ref()),
                    1 => json!([s.as_ref(), true, 42]),
                    2 => json!({"x":s.as_ref()}),
                    3 => json!({"nested":[s.as_ref()]}),
                    _ => J::Null,
                };
                let value = ExactJson::parse(data.to_string(), Limits::default()).unwrap();
                let input = Input {
                    parameters: vec![ParameterInput {
                        location: ParameterLocation::Query,
                        name: "q".into(),
                        value: value.root(),
                    }],
                    ..Input::default()
                };
                accepted += u64::from(doc.operation_id("item0").unwrap().prepare(&input).is_ok());
            }
            "response" => {
                let p = doc
                    .operation_id("item0")
                    .unwrap()
                    .prepare(&Input::default())
                    .unwrap();
                let host = HostCapabilities::programmable();
                let result = TransportResult {
                    response: Some(ResponseInput {
                        status: (rng % 700) as u16,
                        headers: vec![Header::new("Content-Type", &bytes)],
                        body: DeliveredBody {
                            bytes: bytes.clone().into(),
                            state: BodyState::Complete,
                            provenance: Provenance::Unknown,
                        },
                        opaque_redirect: false,
                    }),
                    upload: UploadState::Unknown,
                    error_detail: None,
                };
                let o = block_on(p.invoke(Cancellation::default(), &host, |_, _| {
                    std::future::ready(result)
                }));
                if let Some(r) = o.response {
                    accepted += u64::from(r.json().is_ok());
                }
            }
            _ => panic!("unknown campaign"),
        }));
        if step.is_err() {
            return json!({"passed":false,"family":family,"seed":seed,"iteration":iterations,"rng_state":rng,"input_hex":hex(&bytes),"error":"panic","seconds":started.elapsed().as_secs_f64()});
        }
        iterations += 1;
    }
    drop(doc);
    json!({"passed":iterations>0&&live_json_owners()==before,"family":family,"seconds":started.elapsed().as_secs_f64(),"seed":seed,"iterations":iterations,"accepted":accepted,"max_generated_bytes":4096,"live_delta":live_json_owners()as i64-before as i64})
}
