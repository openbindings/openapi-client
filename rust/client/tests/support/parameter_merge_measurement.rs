// Standalone release measurement, deliberately outside the test harness.
// From rust/, after `cargo build --locked --release -p dynamic-openapi-client`:
// rustc --edition=2024 -O -C lto=thin client/tests/support/parameter_merge_measurement.rs \
//   --extern dynamic_openapi_client=target/release/libdynamic_openapi_client.rlib \
//   -L dependency=target/release/deps -o /tmp/o2-parameter-measurement
// /tmp/o2-parameter-measurement
use dynamic_openapi_client::{Document, Input, Limits};
use std::{hint::black_box, time::Instant};

fn source(n: usize) -> String {
    let parameters = (0..n)
        .map(|i| format!(r#"{{"in":"query","name":"p{i}","schema":{{"type":"string"}}}}"#))
        .collect::<Vec<_>>()
        .join(",");
    format!(
        r#"{{"openapi":"3.1.2","servers":[{{"url":"https://api.example"}}],"paths":{{"/":{{"get":{{"operationId":"read","parameters":[{parameters}]}}}}}}}}"#
    )
}

fn document(source: &str) -> Document {
    Document::parse(source, None, Limits::default()).unwrap()
}

fn median(mut samples: Vec<f64>) -> f64 {
    samples.sort_by(f64::total_cmp);
    samples[samples.len() / 2]
}

fn main() {
    println!("21 samples; parse/index excluded; medians in milliseconds");
    println!("N\tdescribe\tcold parameters\tcold prepare\tcached parameters");
    for n in [1_000, 2_000, 4_000, 8_000] {
        let source = source(n);
        let mut describe = vec![];
        let mut parameters = vec![];
        let mut prepare = vec![];
        let mut cached = vec![];
        for _ in 0..21 {
            let doc = document(&source);
            let op = doc.operation_id("read").unwrap();
            let start = Instant::now();
            let result = black_box(op.describe().unwrap());
            describe.push(start.elapsed().as_secs_f64() * 1_000.0);
            drop(result);

            let start = Instant::now();
            let result = black_box(op.parameters().unwrap());
            parameters.push(start.elapsed().as_secs_f64() * 1_000.0);
            drop(result);

            let start = Instant::now();
            for _ in 0..1_000 {
                black_box(op.parameters().unwrap());
            }
            cached.push(start.elapsed().as_secs_f64()); // seconds / 1000 * 1000 ms

            let doc = document(&source);
            let op = doc.operation_id("read").unwrap();
            let start = Instant::now();
            let result = black_box(op.prepare(&Input::default()).unwrap());
            prepare.push(start.elapsed().as_secs_f64() * 1_000.0);
            drop(result);
        }
        println!(
            "{n}\t{:.4}\t{:.4}\t{:.4}\t{:.6}",
            median(describe),
            median(parameters),
            median(prepare),
            median(cached)
        );
    }
}
