use dynamic_openapi_client::{Code, ExactJson, Limits};

#[test]
fn pointer_tokens_keep_rfc6901_escaping_array_rules_and_source_positions() {
    let json = ExactJson::parse(
        r#"{"plain":"p","":"empty","é":"unicode","a/b":"slash","~1":"once","arr":[{"a/b":"item"}],"01":"object-key"}"#,
        Limits::default(),
    )
    .unwrap();
    let root = json.root();
    for (pointer, expected) in [
        ("/plain", "p"),
        ("/", "empty"),
        ("/é", "unicode"),
        ("/a~1b", "slash"),
        ("/~01", "once"),
        ("/arr/0/a~1b", "item"),
        ("/01", "object-key"),
    ] {
        let value = root.pointer(pointer).unwrap();
        assert_eq!(value.as_str(), Some(expected));
        assert_eq!(value.location().pointer, pointer);
    }
    for pointer in ["plain", "/a~", "/a~2b", "/arr/01", "/arr/-", "/arr/+0"] {
        let error = root.pointer(pointer).unwrap_err();
        assert_eq!(error.code(), Code::InvalidPointer, "{pointer}");
        assert!(error.source_position().is_some());
    }
    let error = root.pointer("/arr/1").unwrap_err();
    assert_eq!(error.code(), Code::MissingReference);
    assert_eq!(error.location().unwrap().pointer, "/arr");
    let error = root.pointer("/absent/~2").unwrap_err();
    assert_eq!(error.code(), Code::MissingReference);
    assert_eq!(error.location().unwrap().pointer, "");
}
