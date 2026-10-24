// Standalone release measurement of warm preparation, deliberately outside the
// test harness. It times `RequestBuilder::prepare` alone, without credentials and
// with one bearer, query or cookie credential, and counts allocations per call.
// From rust/, after `cargo build --locked --release -p dynamic-openapi-client`:
// rustc --edition=2024 -O -C lto=thin client/tests/support/prepare_measurement.rs \
//   --extern dynamic_openapi_client=target/release/libdynamic_openapi_client.rlib \
//   -L dependency=target/release/deps -o /tmp/prepare-measurement
// /tmp/prepare-measurement
use dynamic_openapi_client::{Credential, Document, Limits, ParameterLocation};
use std::{
    alloc::{GlobalAlloc, Layout, System},
    hint::black_box,
    sync::atomic::{AtomicUsize, Ordering},
    time::Instant,
};

struct Count;
static CALLS: AtomicUsize = AtomicUsize::new(0);
static BYTES: AtomicUsize = AtomicUsize::new(0);
// Measurement instrumentation only. The client itself forbids unsafe code.
unsafe impl GlobalAlloc for Count {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        CALLS.fetch_add(1, Ordering::Relaxed);
        BYTES.fetch_add(layout.size(), Ordering::Relaxed);
        unsafe { System.alloc(layout) }
    }
    unsafe fn dealloc(&self, pointer: *mut u8, layout: Layout) {
        unsafe { System.dealloc(pointer, layout) }
    }
    unsafe fn realloc(&self, pointer: *mut u8, layout: Layout, size: usize) -> *mut u8 {
        CALLS.fetch_add(1, Ordering::Relaxed);
        BYTES.fetch_add(size, Ordering::Relaxed);
        unsafe { System.realloc(pointer, layout, size) }
    }
}
#[global_allocator]
static ALLOCATOR: Count = Count;

const DOCUMENT: &str = r#"{"openapi":"3.1.2","servers":[{"url":"https://api.example/v1"}],
"components":{"securitySchemes":{
 "bearer":{"type":"http","scheme":"bearer"},
 "query":{"type":"apiKey","in":"query","name":"api_key"},
 "cookie":{"type":"apiKey","in":"cookie","name":"session"}}},
"paths":{"/items/{id}":{
 "parameters":[{"in":"path","name":"id","required":true,"schema":{"type":"string"}},
               {"in":"query","name":"q","schema":{"type":"string"}}],
 "get":{"operationId":"plain"},
 "put":{"operationId":"bearer","security":[{"bearer":[]}]},
 "post":{"operationId":"query","security":[{"query":[]}]},
 "patch":{"operationId":"cookie","security":[{"cookie":[]}]}}}}"#;

fn median(mut samples: Vec<f64>) -> f64 {
    samples.sort_by(f64::total_cmp);
    samples[samples.len() / 2]
}

fn main() {
    let document = Document::parse(DOCUMENT, None, Limits::default()).unwrap();
    let origins = vec!["https://api.example".to_owned()];
    const CALLS_PER_SAMPLE: usize = 20_000;
    println!("21 samples of {CALLS_PER_SAMPLE} warm prepare calls; medians");
    println!("case\tns/call\tallocations/call\tallocated bytes/call");
    for case in ["plain", "bearer", "query", "cookie"] {
        let operation = document.operation_id(case).unwrap();
        let build = || {
            let request = operation
                .request()
                .parameter(ParameterLocation::Path, "id", &"alpha")
                .parameter(ParameterLocation::Query, "q", &"x y");
            match case {
                "plain" => request,
                "bearer" => {
                    request.credential(case, Credential::bearer("local-token", origins.clone()))
                }
                _ => request.credential(case, Credential::api_key("local-key", origins.clone())),
            }
        };
        build().prepare().unwrap(); // Compile the operation before timing.
        let (mut times, mut calls, mut bytes) = (vec![], vec![], vec![]);
        for _ in 0..21 {
            // Builders and the result vector exist before the timed region.
            let builders: Vec<_> = (0..CALLS_PER_SAMPLE).map(|_| build()).collect();
            let mut prepared = Vec::with_capacity(CALLS_PER_SAMPLE);
            CALLS.store(0, Ordering::Relaxed);
            BYTES.store(0, Ordering::Relaxed);
            let start = Instant::now();
            for builder in builders {
                prepared.push(black_box(builder.prepare().unwrap()));
            }
            times.push(start.elapsed().as_nanos() as f64 / CALLS_PER_SAMPLE as f64);
            calls.push(CALLS.load(Ordering::Relaxed) as f64 / CALLS_PER_SAMPLE as f64);
            bytes.push(BYTES.load(Ordering::Relaxed) as f64 / CALLS_PER_SAMPLE as f64);
            drop(prepared);
        }
        println!(
            "{case}\t{:.1}\t{:.2}\t{:.1}",
            median(times),
            median(calls),
            median(bytes)
        );
    }
}
