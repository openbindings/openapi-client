//! Format admission only. Acquisition and reference-resource graphs are separate capabilities.
use crate::diagnostics::{Code, LimitKind, Location};
use crate::exact::Value;
use crate::{Cancellation, Diagnostic, Limits};
use std::sync::Arc;

pub(crate) fn parse(
    bytes: &[u8],
    limits: Limits,
    cancellation: &Cancellation,
) -> Result<Value, Diagnostic> {
    // Admission precedes both format detection and any copy of original source.
    let admitted = bytes.len() <= limits.document_bytes;
    let fail = |diagnostic: Diagnostic| {
        (diagnostic).parse_failure(if admitted {
            Arc::from(bytes)
        } else {
            Arc::from([])
        })
    };
    cancellation.check().map_err(fail)?;
    if bytes.len() > limits.document_bytes {
        return Err(fail(Diagnostic::limited(
            LimitKind::DocumentBytes,
            limits.document_bytes,
            bytes.len(),
        )));
    }
    // Keep the normal JSON path identical to Value: no extra grammar scan.
    // Only syntax failure may select YAML. Valid JSON's duplicate, Unicode and
    // node-limit refusals must never be bypassed by another interpretation.
    let json_failure = match Value::parse_with_cancellation(bytes, limits.clone(), cancellation) {
        Ok(json) => return Ok(json),
        Err(error) => {
            let ambiguous_depth = matches!(
                error.reason(),
                Some(crate::diagnostics::DiagnosticReason::Limit {
                    kind: LimitKind::Depth,
                    ..
                })
            );
            let may_be_yaml = error.code() == Code::InvalidJson
                || (ambiguous_depth
                    && serde_json::from_slice::<&serde_json::value::RawValue>(bytes).is_err());
            if !may_be_yaml {
                return Err(error);
            }
            error
        }
    };
    let context = crate::value::authored_source_context().map_err(fail)?;
    let source = std::str::from_utf8(bytes).map_err(|error| {
        fail(
            Diagnostic::new(Code::InvalidUtf8)
                .at(Location {
                    start: error.valid_up_to(),
                    end: error.valid_up_to() + error.error_len().unwrap_or(0),
                    pointer: String::new(),
                })
                .sourced(context),
        )
    })?;
    crate::yaml::parse(source, context, limits, cancellation).map_err(|diagnostic| {
        // Both grammars failed: preserve the existing JSON diagnostic for an
        // ambiguous brace/bracket-leading input. This does not detect format.
        if diagnostic.code() == Code::InvalidYaml
            && matches!(
                bytes.iter().find(|b| !b.is_ascii_whitespace()),
                Some(b'{') | Some(b'[')
            )
        {
            fail(json_failure)
        } else {
            fail(diagnostic)
        }
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::diagnostics::{DiagnosticReason, YamlReason};
    use crate::exact::ValueKind;
    use crate::source::{SourceFormat, SourceOrigin};

    fn load(source: &str) -> Value {
        parse(
            source.as_bytes(),
            Limits::default(),
            &Cancellation::default(),
        )
        .unwrap()
    }
    fn failure(source: &str) -> Diagnostic {
        parse(
            source.as_bytes(),
            Limits::default(),
            &Cancellation::default(),
        )
        .unwrap_err()
    }
    fn reason(source: &str, expected: YamlReason) {
        let error = failure(source);
        assert_eq!(error.source_bytes(), source.as_bytes());
        assert!(error.source_position().is_none());
        assert_eq!(error.reason(), Some(&DiagnosticReason::Yaml(expected)));
        let location = error.location().unwrap();
        assert!(source.get(location.start..location.end).is_some());
        assert_eq!(
            error.source_context().unwrap().origin,
            SourceOrigin::Authored
        );
    }

    #[test]
    fn original_json_contract_remains_exact() {
        let source = "  {\"wide\": 123456789012345678901234567890,\"x\":null} \n";
        let owner = load(source);
        assert_eq!(owner.source(), source);
        assert_eq!(owner.original_source(), source);
        assert_eq!(owner.source_format(), SourceFormat::Json);
        let value = owner.root().get("wide").unwrap();
        assert_eq!(value.raw(), "123456789012345678901234567890");
        let trace = value.source_trace();
        assert_eq!(
            trace.representation.context(),
            trace.authored.as_ref().unwrap().context()
        );
        assert_eq!(
            trace.representation.raw(),
            trace.authored.as_ref().unwrap().raw()
        );
        assert!(trace.alias_uses.is_empty());
        assert_eq!(failure("{\"x\":1,\"x\":2}").code(), Code::DuplicateMember);
    }
    #[test]
    fn yaml_numbers_preserve_values_and_lexemes() {
        let source = "wide: 123456789012345678901234567890\nexponent: 1e9999\nfraction: +001.2500\nhex: 0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF\noctal: 0o777\nzero: -00\nsmall: .5\n";
        let owner = load(source);
        assert_eq!(owner.source_format(), SourceFormat::Yaml);
        assert_eq!(owner.original_source(), source);
        for (key, expected, authored) in [
            (
                "wide",
                "123456789012345678901234567890",
                "123456789012345678901234567890",
            ),
            ("exponent", "1e9999", "1e9999"),
            ("fraction", "1.2500", "+001.2500"),
            (
                "hex",
                "340282366920938463463374607431768211455",
                "0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF",
            ),
            ("octal", "511", "0o777"),
            ("zero", "-0", "-00"),
            ("small", "0.5", ".5"),
        ] {
            let v = owner.root().get(key).unwrap();
            assert_eq!(v.number().unwrap().token(), expected);
            assert_eq!(v.source_context().origin, SourceOrigin::Normalized);
            let trace = v.source_trace();
            assert_eq!(trace.authored.as_ref().unwrap().raw(), authored);
            assert_eq!(trace.authored.as_ref().unwrap().source(), source);
            assert_eq!(trace.representation.raw(), expected);
            assert_ne!(
                trace.representation.context(),
                trace.authored.unwrap().context()
            );
        }
    }
    #[test]
    fn yaml_scalar_resolution_preserves_absent_null_and_strings() {
        let owner = load(
            "empty:\nnull_value: null\nfalse_value: FALSE\ntext: 'false'\nyes_word: yes\ndate: 2026-10-10\nversion: 3.1.0\nquoted: \"\\uD83D\\uDE00\"\n",
        );
        let root = owner.root();
        assert!(root.get("absent").is_none());
        assert_eq!(root.get("empty").unwrap().kind(), ValueKind::Null);
        assert_eq!(root.get("null_value").unwrap().kind(), ValueKind::Null);
        assert_eq!(root.get("false_value").unwrap().as_bool(), Some(false));
        assert_eq!(root.get("text").unwrap().as_str(), Some("false"));
        assert_eq!(root.get("yes_word").unwrap().as_str(), Some("yes"));
        assert_eq!(root.get("date").unwrap().as_str(), Some("2026-10-10"));
        assert_eq!(root.get("version").unwrap().as_str(), Some("3.1.0"));
        assert_eq!(root.get("quoted").unwrap().as_str(), Some("😀"));
    }
    #[test]
    fn aliases_retain_definition_and_all_use_sites() {
        let source =
            "original: &a {name: café, nested: [null, true]}\nsecond: &b {inner: *a}\nthird: *b\n";
        let owner = load(source);
        let v = owner.root().pointer("/third/inner/name").unwrap();
        assert_eq!(v.as_str(), Some("café"));
        let trace = v.source_trace();
        assert_eq!(trace.authored.as_ref().unwrap().raw(), "café");
        assert_eq!(
            trace.alias_uses.iter().map(|p| p.raw()).collect::<Vec<_>>(),
            ["*b", "*a"]
        );
        let diagnostic = v.error(Code::InvalidDeclaration);
        let position = diagnostic.location().unwrap();
        assert_eq!(&source[position.start..position.end], "café");
        assert_eq!(diagnostic.related().len(), 2);
        assert_eq!(
            diagnostic.source_context(),
            Some(trace.authored.unwrap().context())
        );
    }
    #[test]
    fn duplicate_decoded_keys_and_key_types_are_explicit() {
        let source = "foo: 1\n\"f\\u006fo\": 2\n";
        let error = failure(source);
        assert_eq!(error.code(), Code::DuplicateMember);
        let p = error.location().unwrap();
        let earlier = &error.related()[0];
        assert_eq!(&source[p.start..p.end], "\"f\\u006fo\"");
        assert_eq!(&source[earlier.start..earlier.end], "foo");
        for source in ["200: ok\n", "true: ok\n", "null: ok\n", "? [a,b]\n: ok\n"] {
            reason(source, YamlReason::NonStringKey);
        }
        assert_eq!(
            load("'200': ok\n").root().get("200").unwrap().as_str(),
            Some("ok")
        );
    }
    #[test]
    fn aliased_string_keys_are_not_stringified() {
        let owner = load("definition: &k name\nobject: {*k : yes}\n");
        assert_eq!(
            owner.root().pointer("/object/name").unwrap().as_str(),
            Some("yes")
        );
        reason(
            "definition: &k 200\nobject: {*k : yes}\n",
            YamlReason::NonStringKey,
        );
    }
    #[test]
    fn unsupported_yaml_is_located_and_redacted() {
        reason("a: .nan\n", YamlReason::NonFiniteNumber);
        reason("a: !secret secret-value\n", YamlReason::UnsupportedTag);
        reason("a: !!int not-an-integer\n", YamlReason::InvalidTaggedScalar);
        reason("a: !!merge b\n", YamlReason::UnsupportedTag);
        reason("---\na: 1\n---\na: 2\n", YamlReason::MultipleDocuments);
        reason("%YAML 1.1\n---\na: 1\n", YamlReason::UnsupportedVersion);
        reason("a: &a [*a]\n", YamlReason::AliasCycle);
        let error = failure("a: !secret secret-value\n");
        assert!(!format!("{error:?} {error}").contains("secret"));
        assert_eq!(
            load("'<<': literal\n").root().get("<<").unwrap().as_str(),
            Some("literal")
        );
    }
    #[test]
    fn yaml_flow_comments_bom_and_scalar_styles_use_dependency_grammar() {
        let source = "\u{feff}---\n# heading\nx: >-\n  first\n  second\ny: |\n  two\n  lines\n";
        let owner = load(source);
        assert_eq!(owner.original_source(), source);
        assert_eq!(
            owner.root().get("x").unwrap().as_str(),
            Some("first second")
        );
        assert_eq!(
            owner.root().get("y").unwrap().as_str(),
            Some("two\nlines\n")
        );
        assert_eq!(
            load("{x: [one, two]}")
                .root()
                .pointer("/x/1")
                .unwrap()
                .as_str(),
            Some("two")
        );
        assert_eq!(failure("x: [one, two\n").code(), Code::InvalidYaml);
    }
    #[test]
    fn input_and_normalized_output_have_independent_byte_admission() {
        let source = "a: &a abcdefghijklmnopqrstuvwxyz\nb: [*a, *a, *a, *a]\n";
        let error = parse(
            source.as_bytes(),
            Limits {
                document_bytes: source.len(),
                ..Limits::default()
            },
            &Cancellation::default(),
        )
        .unwrap_err();
        assert!(matches!(
            error.reason(),
            Some(DiagnosticReason::Limit {
                kind: LimitKind::NormalizedBytes,
                ..
            })
        ));
        assert_eq!(error.source_bytes(), source.as_bytes());
        let error = parse(
            source.as_bytes(),
            Limits {
                document_bytes: source.len() - 1,
                ..Limits::default()
            },
            &Cancellation::default(),
        )
        .unwrap_err();
        assert!(matches!(
            error.reason(),
            Some(DiagnosticReason::Limit {
                kind: LimitKind::DocumentBytes,
                ..
            })
        ));
        assert!(error.source_bytes().is_empty());
    }
    #[test]
    fn alias_expansion_is_bounded_by_nodes_depth_and_steps() {
        for (source, limits, kind) in [
            (
                "a: &a [x,x,x,x]\nb: [*a,*a,*a,*a]\n",
                Limits {
                    nodes: 20,
                    ..Limits::default()
                },
                LimitKind::Nodes,
            ),
            (
                "a: &a [x]\nb: [*a]\n",
                Limits {
                    depth: 2,
                    ..Limits::default()
                },
                LimitKind::Depth,
            ),
            (
                "a: &a [x]\nb: *a\n",
                Limits {
                    reference_steps: 0,
                    ..Limits::default()
                },
                LimitKind::AliasSteps,
            ),
            (
                "a: [[[x]]]\n",
                Limits {
                    depth: 2,
                    ..Limits::default()
                },
                LimitKind::Depth,
            ),
        ] {
            let error = parse(source.as_bytes(), limits, &Cancellation::default()).unwrap_err();
            assert!(
                matches!(error.reason(), Some(DiagnosticReason::Limit { kind: actual, .. }) if *actual == kind),
                "{error:?}"
            );
        }
    }
    #[test]
    fn cancelled_loading_and_invalid_utf8_retain_original_bytes() {
        let cancellation = Cancellation::default();
        cancellation.cancel();
        let error = parse(b"a: x\n", Limits::default(), &cancellation).unwrap_err();
        assert_eq!(error.code(), Code::Cancelled);
        assert_eq!(error.source_bytes(), b"a: x\n");
        let error = parse(b"a: \xff", Limits::default(), &Cancellation::default()).unwrap_err();
        assert_eq!(error.code(), Code::InvalidUtf8);
        assert_eq!(error.source_bytes(), b"a: \xff");
    }
    #[test]
    fn constructed_values_have_no_authored_source() {
        let owner = Value::parse_in_origin(
            b"42",
            Limits::default(),
            &Cancellation::default(),
            SourceOrigin::Constructed,
        )
        .unwrap();
        assert!(owner.root().source_trace().authored.is_none());
    }
    #[test]
    fn format_fallback_never_bypasses_json_policies() {
        for source in [r#"{"x":1,"x":2}"#, r#"{"x":"\uD800"}"#] {
            let expected = Value::parse(source, Limits::default()).unwrap_err();
            assert_eq!(failure(source).code(), expected.code());
        }
        let limits = Limits {
            depth: 1,
            ..Limits::default()
        };
        let error = parse(br#"{"x":[]}"#, limits.clone(), &Cancellation::default()).unwrap_err();
        assert!(matches!(
            error.reason(),
            Some(DiagnosticReason::Limit {
                kind: LimitKind::Depth,
                ..
            })
        ));
        let owner = parse(b"x: '[[[[[[[[[[['\n", limits, &Cancellation::default()).unwrap();
        assert_eq!(owner.root().get("x").unwrap().as_str(), Some("[[[[[[[[[[["));
        assert_eq!(failure("{").code(), Code::InvalidJson);
    }
}
