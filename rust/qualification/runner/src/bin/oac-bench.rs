use dynamic_openapi_client::*;
use serde_json::json;
use std::{
    alloc::{GlobalAlloc, Layout, System},
    hint::black_box,
    sync::atomic::{AtomicUsize, Ordering as O},
    time::Instant,
};
struct Count;
static TRAFFIC: AtomicUsize = AtomicUsize::new(0);
static CALLS: AtomicUsize = AtomicUsize::new(0);
static LIVE: AtomicUsize = AtomicUsize::new(0);
static PEAK: AtomicUsize = AtomicUsize::new(0);
// Qualification instrumentation only. Core has forbid(unsafe_code).
unsafe impl GlobalAlloc for Count {
    unsafe fn alloc(&self, l: Layout) -> *mut u8 {
        let p = unsafe { System.alloc(l) };
        if !p.is_null() {
            CALLS.fetch_add(1, O::Relaxed);
            TRAFFIC.fetch_add(l.size(), O::Relaxed);
            let n = LIVE.fetch_add(l.size(), O::Relaxed) + l.size();
            PEAK.fetch_max(n, O::Relaxed);
        }
        p
    }
    unsafe fn dealloc(&self, p: *mut u8, l: Layout) {
        LIVE.fetch_sub(l.size(), O::Relaxed);
        unsafe { System.dealloc(p, l) }
    }
    unsafe fn realloc(&self, p: *mut u8, l: Layout, n: usize) -> *mut u8 {
        let q = unsafe { System.realloc(p, l, n) };
        if !q.is_null() {
            CALLS.fetch_add(1, O::Relaxed);
            TRAFFIC.fetch_add(n, O::Relaxed);
            LIVE.fetch_sub(l.size(), O::Relaxed);
            let live = LIVE.fetch_add(n, O::Relaxed) + n;
            PEAK.fetch_max(live, O::Relaxed);
        }
        q
    }
}
#[global_allocator]
static ALLOC: Count = Count;
fn measure<S, T>(
    stage: &str,
    n: usize,
    mut setup: impl FnMut() -> S,
    mut call: impl FnMut(&S) -> T,
) -> serde_json::Value {
    let (mut ns, mut traffic, mut calls, mut peak) = (0u128, 0usize, 0usize, 0usize);
    for _ in 0..n {
        let state = setup();
        let base = LIVE.load(O::Relaxed);
        TRAFFIC.store(0, O::Relaxed);
        CALLS.store(0, O::Relaxed);
        PEAK.store(base, O::Relaxed);
        let start = Instant::now();
        let result = black_box(call(black_box(&state)));
        ns += start.elapsed().as_nanos();
        traffic += TRAFFIC.load(O::Relaxed);
        calls += CALLS.load(O::Relaxed);
        peak = peak.max(PEAK.load(O::Relaxed).saturating_sub(base));
        drop(result);
        drop(state);
    }
    json!({"stage":stage,"iterations":n,"elapsed_ns":ns.to_string(),"ns_per_op":ns as f64/n as f64,"allocation_calls":calls,"allocated_bytes":traffic,"allocated_bytes_per_op":traffic as f64/n as f64,"max_extra_live_bytes":peak})
}
fn main() {
    let args: Vec<_> = std::env::args().collect();
    let name = args.get(1).map(String::as_str).unwrap_or("normal");
    let path = format!("{}/../fixtures/{name}.json", env!("CARGO_MANIFEST_DIR"));
    let text = std::fs::read_to_string(&path).unwrap();
    let n = match name {
        "normal" => 500,
        "wide" => 20,
        _ => 5,
    };
    let samples = if args.get(2).map(String::as_str) == Some("once") {
        1
    } else {
        5
    };
    let before = LIVE.load(O::Relaxed);
    let d = Document::parse(&text, None, Limits::default()).unwrap();
    let count = d.operations().len();
    let digest = format!(
        "{}:{}:{}",
        text.len(),
        count,
        d.operation_id("item0")
            .unwrap()
            .prepare(&Input::default())
            .unwrap()
            .target()
    );
    let retained = LIVE.load(O::Relaxed) - before;
    let op = d.operation_id("item0").unwrap();
    let value =
        ExactJson::parse(r#"["x y","a&b","é",9007199254740993]"#, Limits::default()).unwrap();
    let input = Input {
        parameters: vec![ParameterInput {
            location: ParameterLocation::Query,
            name: "q".into(),
            value: value.root(),
        }],
        ..Input::default()
    };
    let payload = r#"{"n":9007199254740993,"fraction":0.12345678901234567890123456789,"a":[true,false,null]}"#;
    let p = op.prepare(&Input::default()).unwrap();
    let host = HostCapabilities::programmable();
    let response = oac_qualification::block_on(p.invoke(Cancellation::default(), &host, |_, _| {
        std::future::ready(TransportResult {
            response: Some(ResponseInput {
                status: 200,
                headers: vec![Header::new("Content-Type", "application/json")],
                body: DeliveredBody {
                    bytes: payload.as_bytes().into(),
                    state: BodyState::Complete,
                    provenance: Provenance::Wire,
                },
                opaque_redirect: false,
            }),
            upload: UploadState::Unknown,
            error_detail: None,
        })
    }))
    .response
    .unwrap();
    let mut rows = vec![];
    for sample in 0..samples {
        let mut stages = vec![];
        stages.push(measure(
            "parse-index",
            n,
            || (),
            |_| Document::parse(&text, None, Limits::default()).unwrap(),
        ));
        stages.push(measure("inspect-one", 10000, || (), |_| op.inspect()));
        stages.push(measure(
            "list-all",
            n,
            || (),
            |_| {
                d.operations()
                    .map(|o| (o.path().to_owned(), o.method(), o.id()))
                    .collect::<Vec<_>>()
            },
        ));
        stages.push(measure(
            "prepare-first",
            n,
            || Document::parse(&text, None, Limits::default()).unwrap(),
            |d| {
                d.operation_id("item0")
                    .unwrap()
                    .prepare(&Input::default())
                    .unwrap()
            },
        ));
        stages.push(measure(
            "prepare-warm",
            10000,
            || (),
            |_| op.prepare(&Input::default()).unwrap(),
        ));
        stages.push(measure(
            "encode-parameter-preparation",
            5000,
            || (),
            |_| op.prepare(&input).unwrap(),
        ));
        stages.push(measure(
            "decode-json-response",
            5000,
            || (),
            |_| response.json().unwrap(),
        ));
        rows.push(json!({"sample":sample,"stages":stages}));
    }
    let owners = live_json_owners();
    for _ in 0..100 {
        drop(Document::parse(&text, None, Limits::default()).unwrap());
    }
    println!("{}",serde_json::to_string_pretty(&json!({"workload":name,"bytes":text.len(),"operations":count,"digest":digest,"samples":rows,"retained_allocator_bytes":retained,"retained_index_estimate":d.retained_bytes(),"cache_entries":d.cached_operations(),"lifecycle_cycles":100,"lifecycle_live_delta":live_json_owners()as i64-owners as i64,"memory_definitions":{"traffic":"requested alloc/realloc bytes; frees do not subtract","live":"requested live allocation sizes, excludes allocator overhead/reservation","peak":"maximum additional live bytes during one measured call; fresh RSS recorded separately","setup":"setup and destruction excluded from stage time; parse result still live at allocation snapshot"}})).unwrap());
}
