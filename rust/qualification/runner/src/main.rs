use oac_qualification::{campaign, check, run};
use serde_json::{Value, json};
use std::{env, fs, time::Instant};
fn main() {
    let args: Vec<_> = env::args().collect();
    if args.get(1).map(String::as_str) == Some("campaign") {
        let report = campaign(&args[2], args[3].parse().unwrap(), args[4].parse().unwrap());
        println!("{}", serde_json::to_string_pretty(&report).unwrap());
        if report["passed"] != true {
            std::process::exit(1);
        }
        return;
    }
    let path = args.get(1).map(String::as_str).unwrap_or(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../fixtures/cases.json"
    ));
    let inventory: Value = serde_json::from_slice(&fs::read(path).unwrap()).unwrap();
    let mut results = vec![];
    let mut ids = std::collections::HashSet::new();
    for c in inventory["cases"].as_array().unwrap() {
        assert!(ids.insert(c["id"].as_str().unwrap()), "duplicate fixture");
        if matches!(c["action"].as_str(), Some("host" | "campaign")) {
            continue;
        }
        let start = Instant::now();
        let outcome = std::panic::catch_unwind(|| run(c));
        let (facts, error) = match outcome {
            Ok(f) => {
                let e = check(&c["expected"], &f).err();
                (f, e)
            }
            Err(_) => (json!({}), Some("panic".to_owned())),
        };
        if let Some(e) = &error {
            eprintln!("{}: {e}", c["id"]);
        }
        results.push(json!({"id":c["id"],"group":c["group"],"executed":true,"passed":error.is_none(),"facts":facts,"expected":c["expected"],"error":error,"seconds":start.elapsed().as_secs_f64()}));
    }
    let passed = results.iter().all(|r| r["passed"] == true);
    println!(
        "{}",
        serde_json::to_string_pretty(&json!({"passed":passed,"cases":results})).unwrap()
    );
    if !passed {
        std::process::exit(1);
    }
}
