use dynamic_openapi_client::*;
use dynamic_openapi_client::{diagnostics::*, graph::*, source::*};
use std::sync::Arc;

const SECRET: &str = "PRIVATE_COLLISION_DECLARATION_319";

fn input(text: &str) -> ResourceInput {
    ResourceInput::anonymous(Arc::<[u8]>::from(text.as_bytes()))
}
fn builder() -> DocumentBuilder {
    DocumentBuilder::new(Limits::default(), GraphLimits::default())
}
fn previous(error: &Diagnostic) -> &SourcePosition {
    assert_eq!(error.code(), Code::IdentityCollision);
    assert!(error.resource_need().is_none());
    for formatted in [
        format!("{error}"),
        format!("{error:?}"),
        format!("{error:#?}"),
    ] {
        assert!(!formatted.contains(SECRET));
    }
    match error.reason() {
        Some(DiagnosticReason::IdentityCollision { previous }) => previous,
        other => panic!("missing earlier claimant: {other:?}"),
    }
}
fn single(yaml: bool, title: &str) -> String {
    if yaml {
        format!(
            "$schema: https://json-schema.org/draft/2020-12/schema\n$id: https://example.com/{SECRET}\ntitle: {title}\ntype: string\n"
        )
    } else {
        format!(
            r#"{{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"https://example.com/{SECRET}","title":"{title}","type":"string"}}"#
        )
    }
}

#[test]
fn same_source_identity_claimants_retain_authored_positions() {
    for yaml in [false, true] {
        let text = if yaml {
            format!(
                "$schema: https://json-schema.org/draft/2020-12/schema\n$defs:\n  first:\n    $id: https://example.com/{SECRET}\n    type: string\n  second:\n    $id: https://example.com/{SECRET}\n    type: number\n"
            )
        } else {
            format!(
                r#"{{"$schema":"https://json-schema.org/draft/2020-12/schema","$defs":{{"first":{{"$id":"https://example.com/{SECRET}","type":"string"}},"second":{{"$id":"https://example.com/{SECRET}","type":"number"}}}}}}"#
            )
        };
        let mut b = builder();
        let id = b.add(input(&text)).unwrap();
        let error = b.build(id).unwrap_err();
        let prior = previous(&error);
        assert_eq!(error.location().unwrap().pointer, "/$defs/second");
        assert_eq!(prior.location().pointer, "/$defs/first");
        assert_eq!(Some(prior.context()), error.source_context());
        assert_eq!(prior.context().origin, SourceOrigin::Authored);
        assert_eq!(prior.source(), text);
        assert!(prior.raw().contains("string"));
        assert!(!prior.raw().contains("number"));
        let primary = error.source_position().unwrap();
        assert_eq!(primary.source(), text);
        assert_eq!(primary.location(), error.location().unwrap());
        assert!(primary.raw().contains("number"));
        assert!(error.related().is_empty());
    }
}

#[test]
fn cross_source_identity_evidence_survives_all_document_owners() {
    for first_yaml in [false, true] {
        for second_yaml in [false, true] {
            let first = single(first_yaml, "first");
            let second = single(second_yaml, "second");
            let (error, expected) = {
                let mut b = builder();
                let id = b.add(input(&first)).unwrap();
                let document = b.build(id).unwrap();
                let expected = document
                    .resource(id)
                    .unwrap()
                    .root()
                    .source_trace()
                    .authored
                    .unwrap();
                let mut b = DocumentBuilder::from_document(&document);
                b.add(input(&second)).unwrap();
                (b.build(id).unwrap_err(), expected)
            };
            let prior = previous(&error);
            assert_eq!(prior, &expected);
            assert_eq!(prior.raw(), expected.raw());
            let source_id = expected.context();
            drop(expected);
            assert_eq!(prior.source(), first);
            assert_eq!(prior.context(), source_id);
            assert_ne!(Some(prior.context()), error.source_context());
            assert!(error.related().is_empty());
            assert!(error.location().unwrap().pointer.is_empty());
            assert!(!format!("{prior:?}").contains(SECRET));
        }
    }
}

#[test]
fn duplicate_anchor_points_to_prior_keyword() {
    for first_keyword in ["$anchor", "$dynamicAnchor"] {
        for second_keyword in ["$anchor", "$dynamicAnchor"] {
            for yaml in [false, true] {
                let text = if yaml {
                    format!(
                        "$schema: https://json-schema.org/draft/2020-12/schema\n$defs:\n  first:\n    {first_keyword}: {SECRET}\n  second:\n    {second_keyword}: {SECRET}\n"
                    )
                } else {
                    format!(
                        r#"{{"$schema":"https://json-schema.org/draft/2020-12/schema","$defs":{{"first":{{"{first_keyword}":"{SECRET}"}},"second":{{"{second_keyword}":"{SECRET}"}}}}}}"#
                    )
                };
                let mut b = builder();
                let id = b.add(input(&text)).unwrap();
                let error = b.build(id).unwrap_err();
                let prior = previous(&error);
                assert_eq!(
                    prior.location().pointer,
                    format!("/$defs/first/{first_keyword}")
                );
                assert_eq!(
                    error.location().unwrap().pointer,
                    format!("/$defs/second/{second_keyword}")
                );
                assert_eq!(
                    prior.raw(),
                    if yaml {
                        SECRET.to_owned()
                    } else {
                        format!("\"{SECRET}\"")
                    }
                );
                assert_eq!(prior.source(), text);
            }
        }
    }
}

#[test]
fn graph_alias_collision_keeps_earlier_resource_owner() {
    let first = format!(r#"{{"title":"{SECRET}"}}"#);
    let mut b = builder();
    b.add(
        ResourceInput::at(
            "https://example.com/first",
            Arc::<[u8]>::from(first.as_bytes()),
        )
        .unwrap()
        .aliases(["https://example.com/shared".to_owned()])
        .unwrap(),
    )
    .unwrap();
    let error = b
        .add(
            ResourceInput::at(
                "https://example.com/second",
                Arc::<[u8]>::from(b"{}".as_slice()),
            )
            .unwrap()
            .aliases(["https://example.com/shared".to_owned()])
            .unwrap(),
        )
        .unwrap_err()
        .clone();
    drop(b);
    let prior = previous(&error);
    assert_eq!(prior.source(), first);
    assert_eq!(prior.raw(), first);
    assert!(prior.location().pointer.is_empty());
    assert_ne!(Some(prior.context()), error.source_context());
    assert!(error.related().is_empty());
}

#[test]
fn preparse_retrieval_collision_keeps_primary_absent_and_precancel_wins() {
    let first = format!(r#"{{"title":"{SECRET}"}}"#);
    let mut b = builder();
    b.add(
        ResourceInput::at(
            "https://example.com/first",
            Arc::<[u8]>::from(first.as_bytes()),
        )
        .unwrap(),
    )
    .unwrap();
    let conflicting =
        ResourceInput::at("https://example.com/first", Arc::<[u8]>::from([0xff])).unwrap();
    let cancel = Cancellation::default();
    cancel.cancel();
    let cancelled = b
        .add_with_cancellation(conflicting.clone(), &cancel)
        .unwrap_err();
    assert_eq!(cancelled.code(), Code::Cancelled);
    assert!(cancelled.reason().is_none());
    let error = b.add(conflicting).unwrap_err().clone();
    drop(b);
    assert!(error.location().is_none());
    assert!(error.source_context().is_none());
    assert_eq!(previous(&error).source(), first);
}
