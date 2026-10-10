use super::*;
use serde::Deserialize;

fn response(text: impl AsRef<[u8]>) -> ExactJson {
    ExactJson::parse_response(text.as_ref(), Limits::default()).unwrap()
}

#[test]
fn response_projection_and_metadata_leave_the_index_unmaterialized() {
    #[derive(Deserialize)]
    struct Item<'a> {
        name: &'a str,
        id: u64,
    }
    let text = " \n{\"name\":\"borrowed\",\"id\":9007199254740993} \t";
    let owner = response(text);
    let root = owner.root();
    assert!(owner.0.store.indexed_nodes().is_none());
    let projected: Item<'_> = owner.deserialize().unwrap();
    assert_eq!(projected.name, "borrowed");
    assert_eq!(projected.id, 9_007_199_254_740_993);
    assert!(
        owner
            .source()
            .as_bytes()
            .as_ptr_range()
            .contains(&projected.name.as_ptr())
    );
    assert_eq!(owner.source(), text);
    assert_eq!(root.raw(), text.trim());
    assert_eq!(root.kind(), ValueKind::Object);
    assert_eq!(
        root.location(),
        Location {
            start: 2,
            end: text.len() - 2,
            pointer: String::new()
        }
    );
    assert_eq!(root.source_context().origin, SourceOrigin::Authored);
    assert_eq!(owner.retained_bytes(), text.len());
    assert_eq!(root.retained_bytes(), text.len());
    assert!(!format!("{owner:?}").contains("borrowed"));
    assert!(root.pointer("").is_ok());
    assert!(owner.0.store.indexed_nodes().is_none());
    assert_eq!(root.get("name").unwrap().as_str(), Some("borrowed"));
    assert!(owner.retained_bytes() > text.len());
    assert!(owner.0.store.indexed_nodes().is_some());
}

#[test]
fn response_refusals_replay_every_canonical_diagnostic_field() {
    let cases: &[(&[u8], Limits)] = &[
        (br#"{"same":1,"same":2}"#, Limits::default()),
        (br#"{"same":1,"sa\u006de":2}"#, Limits::default()),
        // ObjectSeed decodes every key before checking earlier duplicates.
        (br#"{"a":1,"a":2,"\ud800":3}"#, Limits::default()),
        // A later shallow duplicate outranks an earlier deep string failure.
        (
            br#"{"deep":["\ud800"],"shallow":{"a":1,"a":2}}"#,
            Limits::default(),
        ),
        // A root duplicate outranks its invalid string child.
        (br#"{"a":"\ud800","a":2}"#, Limits::default()),
        (br#"["\ud800"]"#, Limits::default()),
        (br#"{"\udfff":0}"#, Limits::default()),
        (br#"[1,]"#, Limits::default()),
        (b"\xff", Limits::default()),
        (
            br#"{"a":1,"a":2}"#,
            Limits {
                nodes: 2,
                ..Limits::default()
            },
        ),
        (
            br#"["\ud800",0]"#,
            Limits {
                nodes: 1,
                ..Limits::default()
            },
        ),
        (
            br#"[[null]]"#,
            Limits {
                depth: 1,
                ..Limits::default()
            },
        ),
        (
            br#"[0]"#,
            Limits {
                document_bytes: 2,
                ..Limits::default()
            },
        ),
        (
            br#"null"#,
            Limits {
                nodes: 0,
                ..Limits::default()
            },
        ),
    ];
    for (text, limits) in cases {
        let eager = ExactJson::parse(text, limits.clone()).unwrap_err();
        let deferred = ExactJson::parse_response(text, limits.clone()).unwrap_err();
        assert_eq!(deferred.source_bytes(), eager.source_bytes());
        let context = eager.diagnostic.source_context().unwrap();
        assert_ne!(context.id, deferred.diagnostic.source_context().unwrap().id);
        assert_eq!(
            deferred.diagnostic.sourced(context),
            eager.diagnostic,
            "{text:?}"
        );
    }
    let later_bad_key =
        ExactJson::parse_response(br#"{"a":1,"a":2,"\ud800":3}"#, Limits::default()).unwrap_err();
    assert_eq!(later_bad_key.diagnostic.code(), Code::UnsupportedString);
    let shallow = ExactJson::parse_response(
        br#"{"deep":["\ud800"],"shallow":{"a":1,"a":2}}"#,
        Limits::default(),
    )
    .unwrap_err();
    assert_eq!(shallow.diagnostic.code(), Code::DuplicateMember);
}

#[test]
fn response_admission_preserves_exact_numbers_and_literal_private_names() {
    for text in [
        "-0",
        "1e400",
        "1e-9999",
        "340282366920938463463374607431768211455",
        "-170141183460469231731687303715884105728",
    ] {
        let owner = response(text);
        assert_eq!(owner.root().kind(), ValueKind::Number);
        assert_eq!(owner.root().number().unwrap().token(), text);
        assert_eq!(owner.source(), text);
    }
    let text = br#"{"$serde_json::private::Number":"1e400","$serde_json::private::RawValue":0,"escaped\u006bey":"\ud834\udd1e"}"#;
    let owner = response(text);
    assert_eq!(
        owner
            .root()
            .get("$serde_json::private::Number")
            .unwrap()
            .as_str(),
        Some("1e400")
    );
    assert_eq!(owner.root().get("escapedkey").unwrap().as_str(), Some("𝄞"));
    assert_eq!(owner.root().members().unwrap().count(), 3);
    assert_eq!(owner.source().as_bytes(), text);
}

#[test]
fn concurrent_response_inspection_initializes_one_shared_index_and_retains_children() {
    fn assert_send_sync<T: Send + Sync>() {}
    assert_send_sync::<ExactJson>();
    assert_send_sync::<Value>();
    let text = r#" {"01":{"~/":[null,{"":"kept"}]},"same":[7,7]} "#;
    let owner = response(text);
    let context = owner.root().source_context();
    let barrier = std::sync::Barrier::new(8);
    let results = std::thread::scope(|scope| {
        let mut jobs = Vec::new();
        for _ in 0..8 {
            let owner = &owner;
            let barrier = &barrier;
            jobs.push(scope.spawn(move || {
                barrier.wait();
                let child = owner.root().pointer("/01/~0~1/1/").unwrap();
                (owner.0.store.nodes().as_ptr() as usize, child)
            }));
        }
        jobs.into_iter()
            .map(|job| job.join().unwrap())
            .collect::<Vec<_>>()
    });
    assert!(results.iter().all(|(address, _)| *address == results[0].0));
    let child = results[0].1.clone();
    drop(results);
    drop(owner);
    assert_eq!(child.raw(), r#""kept""#);
    assert_eq!(child.as_str(), Some("kept"));
    assert_eq!(child.location().pointer, "/01/~0~1/1/");
    assert_eq!(
        &text[child.location().start..child.location().end],
        child.raw()
    );
    assert_eq!(child.source_context(), context);
    assert_eq!(child.pointer("").unwrap().source_context(), context);
}

#[test]
fn ordinary_parse_keeps_eager_indexing_and_cancellation() {
    let owner = ExactJson::parse("[0]", Limits::default()).unwrap();
    assert!(matches!(owner.0.store.index, Index::Eager(_)));
    let token = crate::Cancellation::default();
    token.cancel();
    assert_eq!(
        ExactJson::parse_with_cancellation("[0]", Limits::default(), &token)
            .unwrap_err()
            .diagnostic
            .code(),
        Code::Cancelled
    );
    for depth in [127, 128, 129] {
        let text = format!("{}0{}", "[".repeat(depth), "]".repeat(depth));
        let eager = ExactJson::parse(&text, Limits::default());
        let deferred = ExactJson::parse_response(text.as_bytes(), Limits::default());
        match (eager, deferred) {
            (Ok(eager), Ok(deferred)) => assert_eq!(eager.source(), deferred.source()),
            (Err(eager), Err(deferred)) => assert_eq!(
                deferred
                    .diagnostic
                    .sourced(eager.diagnostic.source_context().unwrap()),
                eager.diagnostic
            ),
            _ => panic!("different depth admission at {depth}"),
        }
    }
}

#[test]
fn tiny_response_node_budgets_count_tokens_not_private_number_callbacks() {
    for (text, actual) in [
        ("1e9999", 1),
        ("[1e9999]", 2),
        (r#"{"$serde_json::private::Number":"1e9999"}"#, 2),
        (r#"{"$serde_json::private::Number":1e9999}"#, 2),
    ] {
        for limit in 0..=2 {
            let parsed = ExactJson::parse_response(
                text.as_bytes(),
                Limits {
                    nodes: limit,
                    ..Limits::default()
                },
            );
            if limit < actual {
                let error = parsed.unwrap_err();
                assert_eq!(error.source_bytes(), text.as_bytes());
                assert_eq!(
                    error.diagnostic.reason(),
                    Some(&DiagnosticReason::Limit {
                        kind: LimitKind::Nodes,
                        maximum: limit,
                        actual,
                    })
                );
            } else {
                let owner = parsed.unwrap();
                assert_eq!(owner.source(), text);
                assert_eq!(owner.root().raw(), text);
                assert_eq!(owner.0.store.nodes().len(), actual);
                if text.starts_with('{') {
                    assert_eq!(owner.root().kind(), ValueKind::Object);
                    assert_eq!(owner.root().members().unwrap().count(), 1);
                } else if text.starts_with('[') {
                    assert_eq!(
                        owner.root().at(0).unwrap().number().unwrap().token(),
                        "1e9999"
                    );
                } else {
                    assert_eq!(owner.root().number().unwrap().token(), "1e9999");
                }
            }
        }
    }
}
