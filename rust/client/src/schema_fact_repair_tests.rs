//! Additive root-authored R15–R17 fixture checks; the original contract is unchanged.
use super::*;
use crate::schema_facts::{Fact, FactQuery, FactValue, FactWork, SchemaSelection, query};
use crate::{diagnostics::*, source::*};
use serde_json::{Value as Json, json};
use std::{collections::HashSet, sync::Arc};

fn fixtures() -> Json {
    serde_json::from_str(include_str!("../tests/fixtures/schema-fact-repair.json")).unwrap()
}
fn document(value: &Json) -> Document {
    Document::parse(serde_json::to_vec(value).unwrap(), None, Limits::default()).unwrap()
}
fn selection(doc: &Document, names: &[String]) -> SchemaSelection {
    SchemaSelection(
        names
            .iter()
            .map(|name| {
                doc.entry_resource()
                    .schema_at(&format!("/components/schemas/{name}"))
                    .unwrap()
                    .key
            })
            .collect(),
    )
}
fn answer(fact: &Fact<FactValue>) -> Json {
    match fact {
        Fact::Known {
            value: FactValue::Types(types),
            ..
        } => {
            json!({"Known":types.iter().map(|t| format!("{t:?}").to_lowercase()).collect::<Vec<_>>()})
        }
        Fact::Indeterminate { reason, .. } => json!({"Indeterminate":format!("{reason:?}")}),
        Fact::Invalid(error) => json!({"Invalid":format!("{:?}",error.code())}),
        _ => json!({"Unexpected":true}),
    }
}
fn unique_provenance(fact: &Fact<FactValue>) -> bool {
    match fact {
        Fact::Known { provenance, .. } | Fact::Absent { provenance } => {
            provenance.iter().copied().collect::<HashSet<_>>().len() == provenance.len()
        }
        _ => false,
    }
}
#[test]
fn repeated_fact_queries_keep_answers_provenance_and_work_independent() {
    let doc = document(&json!({
        "openapi":"3.1.2", "paths":{}, "components":{"schemas":{
            "Good":{"$ref":"#/components/schemas/Text"},
            "Text":{"type":"string"},
            "Other":{"type":"integer"},
            "Bad":{"$ref":"#/components/schemas/Missing"},
            "Cycle":{"$ref":"#/components/schemas/Cycle"}
        }}
    }));
    let limits = Limits::default();
    let token = Cancellation::default();
    let mut shared = FactWork::new(&limits, &token);
    for (name, expected) in [
        ("Good", json!({"Known":["string"]})),
        ("Bad", json!({"Invalid":"MissingReference"})),
        ("Other", json!({"Known":["integer"]})),
        ("Cycle", json!({"Indeterminate":"RecursiveFact"})),
        ("Good", json!({"Known":["string"]})),
    ] {
        let selected = selection(&doc, &[name.into()]);
        let before = shared.used();
        let observed = query(&doc.0.graph, &selected, FactQuery::Types, &mut shared).unwrap();
        assert_eq!(answer(&observed), expected);
        let mut fresh = FactWork::new(&limits, &token);
        let control = query(&doc.0.graph, &selected, FactQuery::Types, &mut fresh).unwrap();
        assert_eq!(shared.used() - before, fresh.used());
        if let (
            Fact::Known { provenance, .. },
            Fact::Known {
                provenance: fresh, ..
            },
        ) = (&observed, &control)
        {
            assert_eq!(provenance, fresh);
        }
    }
    let empty = query(
        &doc.0.graph,
        &SchemaSelection(vec![]),
        FactQuery::Types,
        &mut shared,
    )
    .unwrap();
    assert!(matches!(empty, Fact::Absent { provenance } if provenance.is_empty()));
    let weak = Arc::downgrade(&doc.0);
    drop(doc);
    assert!(
        weak.upgrade().is_none(),
        "query workspace cannot retain a graph"
    );
    drop(shared);
}

#[test]
fn repeated_fact_queries_still_exhaust_the_shared_work_budget() {
    let fixtures = fixtures();
    let doc = document(&fixtures["diamond-20"]);
    let selected = selection(&doc, &["S0".into()]);
    let token = Cancellation::default();
    let mut measured = FactWork::new(&doc.0.limits, &token);
    query(&doc.0.graph, &selected, FactQuery::Types, &mut measured).unwrap();
    let limits = Limits {
        schema_fact_work: measured.used() * 2 - 1,
        ..Limits::default()
    };
    let mut shared = FactWork::new(&limits, &token);
    query(&doc.0.graph, &selected, FactQuery::Types, &mut shared).unwrap();
    let error = query(&doc.0.graph, &selected, FactQuery::Types, &mut shared)
        .err()
        .unwrap();
    assert!(matches!(
        error.reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::SchemaFactWork,
            ..
        })
    ));
    assert!(error.location().is_some());
    assert_eq!(shared.used(), limits.schema_fact_work);
}
#[test]
fn frozen_shared_dag_controls() {
    let fixtures = fixtures();
    let mut failures = Vec::new();
    for n in [4, 8, 16, 20, 32, 64] {
        let doc = document(&fixtures[format!("diamond-{n}")]);
        let selected = selection(&doc, &["S0".into()]);
        let token = Cancellation::default();
        let owners = Arc::strong_count(&doc.0);
        let mut work = FactWork::new(&doc.0.limits, &token);
        let observed = query(&doc.0.graph, &selected, FactQuery::Types, &mut work);
        let result = match &observed {
            Ok(f) => answer(f),
            Err(e) => json!({"Error":format!("{:?}",e.reason())}),
        };
        let unique = observed.as_ref().is_ok_and(unique_provenance);
        println!(
            "FACT_REPAIR_OBSERVATION {}",
            json!({"control":"R15","n":n,"work":work.used(),"result":result,"unique_provenance":unique})
        );
        if result != json!({"Known":["string"]}) || !unique {
            failures.push(n);
        }
        if let Ok(Fact::Known { provenance, .. }) = &observed {
            let mut repeat_work = FactWork::new(&doc.0.limits, &token);
            if let Fact::Known {
                provenance: repeat, ..
            } = query(&doc.0.graph, &selected, FactQuery::Types, &mut repeat_work).unwrap()
            {
                assert_eq!(&repeat, provenance);
            } else {
                panic!("repeat changed outcome");
            }
        }
        drop(observed);
        assert_eq!(Arc::strong_count(&doc.0), owners);
        let weak = Arc::downgrade(&doc.0);
        drop(doc);
        assert!(weak.upgrade().is_none());
    }
    assert!(failures.is_empty(), "DAG controls failed: {failures:?}");
}
#[test]
fn frozen_numeric_semantic_controls() {
    let fixtures = fixtures();
    let expected = [
        json!({"Known":["integer"]}),
        json!({"Known":["integer"]}),
        json!({"Known":["integer"]}),
        json!({"Known":["integer","string"]}),
        json!({"Known":["number"]}),
        json!({"Known":["integer"]}),
        json!({"Indeterminate":"ConflictingFacts"}),
        json!({"Indeterminate":"ConflictingFacts"}),
        json!({"Known":["integer"]}),
    ];
    let mut failures = Vec::new();
    for version in ["3.1.2", "3.2.1"] {
        for (i, expected) in expected.iter().enumerate() {
            let mut fixture = fixtures[format!("numeric-{}", i + 1)].clone();
            fixture["openapi"] = json!(version);
            let doc = document(&fixture);
            let token = Cancellation::default();
            let mut work = FactWork::new(&doc.0.limits, &token);
            let observed = answer(
                &query(
                    &doc.0.graph,
                    &selection(&doc, &["Selected".into()]),
                    FactQuery::Types,
                    &mut work,
                )
                .unwrap(),
            );
            println!(
                "FACT_REPAIR_OBSERVATION {}",
                json!({"control":"R17","case":i+1,"edition":version,"result":observed})
            );
            if &observed != expected {
                failures.push(format!("{version}-{}", i + 1));
            }
            assert_eq!(
                doc.entry_resource().root().raw(),
                serde_json::to_string(&fixture).unwrap()
            );
        }
    }
    let doc = document(&fixtures["legacy-siblings"]);
    let token = Cancellation::default();
    let mut work = FactWork::new(&doc.0.limits, &token);
    assert_eq!(
        answer(
            &query(
                &doc.0.graph,
                &selection(&doc, &["Selected".into()]),
                FactQuery::Types,
                &mut work
            )
            .unwrap()
        ),
        json!({"Known":["number"]})
    );
    assert!(failures.is_empty(), "numeric controls failed: {failures:?}");
}
#[test]
fn frozen_longer_memo_path_and_active_cycle_controls() {
    let fixtures = fixtures();
    let doc = document(&fixtures["memo-longer-path"]);
    let selected = selection(&doc, &["T0".into(), "L0".into()]);
    let token = Cancellation::default();
    for (refs, depth, kind) in [
        (4, 128, Some(LimitKind::ReferenceDepth)),
        (128, 4, Some(LimitKind::SchemaFactDepth)),
        (5, 5, None),
    ] {
        let limits = Limits {
            reference_steps: refs,
            schema_fact_depth: depth,
            ..Limits::default()
        };
        let mut work = FactWork::new(&limits, &token);
        let result = query(&doc.0.graph, &selected, FactQuery::Types, &mut work);
        if let Some(kind) = kind {
            let e = result.err().expect("longer selected path must refuse");
            assert_eq!(
                e.reason(),
                Some(&DiagnosticReason::Limit {
                    kind,
                    maximum: 4,
                    actual: 5
                })
            );
            assert!(
                e.location()
                    .is_some_and(|l| l.pointer.starts_with("/components/schemas/"))
            );
            assert!(e.source_context().is_some());
        } else {
            assert_eq!(answer(&result.unwrap()), json!({"Known":["string"]}));
        }
    }
    let doc = document(&fixtures["memo-cycle"]);
    let mut work = FactWork::new(&doc.0.limits, &token);
    assert_eq!(
        answer(
            &query(
                &doc.0.graph,
                &selection(&doc, &["S0".into()]),
                FactQuery::Types,
                &mut work
            )
            .unwrap()
        ),
        json!({"Indeterminate":"RecursiveFact"})
    );
}
#[test]
fn frozen_flat_shape_controls() {
    let fixtures = fixtures();
    for n in [32, 64, 128, 256] {
        let doc = document(&fixtures[format!("flat-shape-{n}")]);
        let selected = selection(&doc, &(0..n).map(|i| format!("S{i}")).collect::<Vec<_>>());
        let token = Cancellation::default();
        let mut work = FactWork::new(&doc.0.limits, &token);
        let result = query(&doc.0.graph, &selected, FactQuery::ObjectShape, &mut work).unwrap();
        assert!(unique_provenance(&result));
        let Fact::Known {
            value: FactValue::Shape(shape),
            provenance,
        } = result
        else {
            panic!("expected Shape")
        };
        let expected = (0..n)
            .map(|i| format!("common-prefix-{i:05}"))
            .collect::<Vec<_>>();
        assert_eq!(shape.names, expected);
        assert!(shape.open);
        assert!(!shape.incomplete);
        let used = work.used();
        println!(
            "FACT_REPAIR_OBSERVATION {}",
            json!({"control":"R16","n":n,"work":used,"names":shape.names.len(),"provenance":provenance.len()})
        );
        let mut twice = selected.0.clone();
        twice.extend_from_slice(&selected.0);
        let mut work = FactWork::new(&doc.0.limits, &token);
        let result = query(
            &doc.0.graph,
            &SchemaSelection(twice),
            FactQuery::ObjectShape,
            &mut work,
        )
        .unwrap();
        assert!(work.used() > used, "repeated admission must consume work");
        assert!(
            unique_provenance(&result),
            "repeated contributions remain idempotent"
        );
        let Fact::Known {
            value: FactValue::Shape(repeated),
            ..
        } = result
        else {
            panic!("expected repeated Shape")
        };
        assert_eq!(repeated, shape);
    }
}
#[test]
fn frozen_accumulator_small_shared_budget() {
    let fixtures = fixtures();
    let doc = document(&fixtures["flat-shape-32"]);
    let selected = selection(&doc, &(0..32).map(|i| format!("S{i}")).collect::<Vec<_>>());
    let token = Cancellation::default();
    let limits = Limits {
        schema_fact_work: 256,
        ..Limits::default()
    };
    let mut work = FactWork::new(&limits, &token);
    let e = query(&doc.0.graph, &selected, FactQuery::ObjectShape, &mut work)
        .err()
        .expect("small shared budget must refuse");
    assert!(matches!(
        e.reason(),
        Some(DiagnosticReason::Limit {
            kind: LimitKind::SchemaFactWork,
            ..
        })
    ));
    assert!(work.used() > 32 && work.used() <= 256);
    assert!(e.location().is_some());
}

#[test]
fn cancellation_at_memo_accumulator_and_provenance_releases_scratch() {
    use crate::schema_facts::{WorkStage, cancel_at_for_test};
    let fixtures = fixtures();
    for stage in [
        WorkStage::Memo,
        WorkStage::Accumulator,
        WorkStage::Provenance,
    ] {
        let doc = document(&fixtures["diamond-4"]);
        let selected = selection(&doc, &["S0".into()]);
        let owners = Arc::strong_count(&doc.0);
        let token = Cancellation::default();
        let guard = cancel_at_for_test(stage);
        let mut work = FactWork::new(&doc.0.limits, &token);
        let error = query(&doc.0.graph, &selected, FactQuery::Types, &mut work)
            .err()
            .expect("named checkpoint must cancel");
        assert_eq!(error.code(), Code::Cancelled);
        assert!(token.is_cancelled());
        assert!(work.used() > 0 && work.used() < doc.0.limits.schema_fact_work);
        drop(error);
        drop(guard);
        assert_eq!(Arc::strong_count(&doc.0), owners);
        let token = Cancellation::default();
        let mut work = FactWork::new(&doc.0.limits, &token);
        assert_eq!(
            answer(&query(&doc.0.graph, &selected, FactQuery::Types, &mut work).unwrap()),
            json!({"Known":["string"]})
        );
        let weak = Arc::downgrade(&doc.0);
        drop(doc);
        assert!(weak.upgrade().is_none());
    }
}
#[test]
fn joined_selection_and_item_contributions_are_unique_and_keep_their_family() {
    let doc = document(
        &json!({"openapi":"3.1.2","info":{"title":"t","version":"1"},"paths":{},"components":{"schemas":{"S0":{"properties":{"x":{"type":"string"}},"items":{"type":"string"}},"S1":{"$ref":"#/components/schemas/S0","$dynamicRef":"#/components/schemas/S0"}}}}),
    );
    let selected = selection(&doc, &["S1".into(), "S0".into(), "S1".into()]);
    let token = Cancellation::default();
    for query_kind in [FactQuery::Property("x"), FactQuery::Items] {
        let mut work = FactWork::new(&doc.0.limits, &token);
        let result = query(&doc.0.graph, &selected, query_kind.clone(), &mut work).unwrap();
        assert!(unique_provenance(&result));
        match (query_kind, result) {
            (
                FactQuery::Property(_),
                Fact::Known {
                    value: FactValue::Selection(s),
                    ..
                },
            ) => assert_eq!(s.0.len(), 1),
            (
                FactQuery::Items,
                Fact::Known {
                    value: FactValue::Items(crate::schema_facts::Items::Homogeneous(s)),
                    ..
                },
            ) => assert_eq!(s.0.len(), 1),
            _ => panic!("result family or unique selection changed"),
        }
    }
}

#[test]
fn memo_retains_first_path_limit_priority() {
    let fixtures = fixtures();
    let doc = document(&fixtures["memo-longer-path"]);
    let token = Cancellation::default();
    let limits = Limits {
        reference_steps: 4,
        schema_fact_depth: 4,
        ..Limits::default()
    };
    for names in [vec!["L0".into()], vec!["T0".into(), "L0".into()]] {
        let mut work = FactWork::new(&limits, &token);
        let error = query(
            &doc.0.graph,
            &selection(&doc, &names),
            FactQuery::Types,
            &mut work,
        )
        .err()
        .unwrap();
        assert_eq!(
            error.reason(),
            Some(&DiagnosticReason::Limit {
                kind: LimitKind::ReferenceDepth,
                maximum: 4,
                actual: 5
            })
        );
    }
}

#[test]
fn frozen_memo_first_refusal_matches_unmemoized_path() {
    let fixture: Json = serde_json::from_str(include_str!(
        "../tests/fixtures/schema-memo-first-refusal.json"
    ))
    .unwrap();
    let doc = document(&fixture);
    let token = Cancellation::default();
    let mut failures = Vec::new();
    for (refs, depth, kind, pointer) in [
        (
            6,
            128,
            LimitKind::ReferenceDepth,
            "/components/schemas/T3/$ref",
        ),
        (128, 6, LimitKind::SchemaFactDepth, "/components/schemas/T4"),
    ] {
        let limits = Limits {
            reference_steps: refs,
            schema_fact_depth: depth,
            ..Limits::default()
        };
        let mut previous: Option<(
            Option<SourceContext>,
            Option<Location>,
            Option<DiagnosticReason>,
        )> = None;
        for names in [vec!["L0".into()], vec!["T0".into(), "L0".into()]] {
            let mut work = FactWork::new(&limits, &token);
            let error = query(
                &doc.0.graph,
                &selection(&doc, &names),
                FactQuery::Types,
                &mut work,
            )
            .err()
            .unwrap();
            println!(
                "FACT_REPAIR_OBSERVATION {}",
                json!({"control":"R15-first-refusal","selected":names,"reason":format!("{:?}",error.reason()),"pointer":error.location().map(|l|&l.pointer)})
            );
            if error.reason()
                != Some(&DiagnosticReason::Limit {
                    kind,
                    maximum: 6,
                    actual: 7,
                })
                || error.location().is_none_or(|l| l.pointer != pointer)
            {
                failures.push(format!("{kind:?} {names:?}"));
            }
            if let Some((source, location, reason)) = previous
                && (error.source_context() != source
                    || error.location() != location.as_ref()
                    || error.reason() != reason.as_ref())
            {
                failures.push("memo differs from standalone traversal".to_owned());
            }
            previous = Some((
                error.source_context(),
                error.location().cloned(),
                error.reason().cloned(),
            ));
        }
    }
    assert!(
        failures.is_empty(),
        "first-refusal controls failed: {failures:?}"
    );
}
