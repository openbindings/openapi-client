//! Ordinary inspection and both forms of explicit schema dependency recovery.
use dynamic_openapi_client::{
    Code, Diagnostic, Document, DocumentBuilder, GraphLimits, Limits, ResourceInput,
    ResourceInterpretation, ResourceLayout, ResourceNeedKind, SchemaProfile,
};
use std::sync::Arc;

fn complete_bytes(
    document: &Document,
    error: &Diagnostic,
    bytes: &[u8],
) -> Result<Document, Diagnostic> {
    let need = error.resource_need().ok_or_else(|| error.clone())?;
    if need.kind() != ResourceNeedKind::Bytes {
        return Err(error.clone());
    }
    let uri = need
        .requested_uri()
        .ok_or_else(|| Diagnostic::new(Code::MissingResourceBase))?;
    let mut builder = DocumentBuilder::from_document(document);
    builder
        .supply(need, ResourceInput::at(uri, bytes)?)
        .map_err(|failure| failure.diagnostic().clone())?;
    builder.build(document.entry_resource().id())
}
fn complete_layout(
    document: &Document,
    error: &Diagnostic,
    pointer: &str,
) -> Result<Document, Diagnostic> {
    let need = error.resource_need().ok_or_else(|| error.clone())?;
    if need.kind() != ResourceNeedKind::Layout {
        return Err(error.clone());
    }
    let ResourceInterpretation::Schema { default_profile } = need.interpretation() else {
        return Err(error.clone());
    };
    let mut builder = DocumentBuilder::from_document(document);
    builder.apply_layout(
        need,
        ResourceLayout::new().schema(pointer, default_profile.clone()),
    )?;
    builder.build(document.entry_resource().id())
}
fn main() -> Result<(), Diagnostic> {
    let document = Document::parse(
        br#"{
      "openapi":"3.1.2","info":{"title":"Schema recovery","version":"1"},"paths":{},
      "components":{"schemas":{"Request":{"$ref":"https://schemas.example/request"}}}
    }"#,
        None,
        Limits::default(),
    )
    .map_err(|failure| failure.diagnostic().clone())?;
    let original = document
        .entry_resource()
        .schema_at("/components/schemas/Request")?;
    let edge = original
        .references()?
        .next()
        .ok_or_else(|| Diagnostic::new(Code::MissingReference))?;
    let missing = edge
        .resolve_static()
        .expect_err("the example has not supplied its dependency");
    let completed = complete_bytes(
        &document,
        &missing,
        br#"{"type":"object","properties":{"message":{"type":"string"}}}"#,
    )?;
    let current = completed
        .entry_resource()
        .schema_at("/components/schemas/Request")?;
    let resolved = current
        .references()?
        .next()
        .ok_or_else(|| Diagnostic::new(Code::MissingReference))?
        .resolve_static()?;
    assert_eq!(
        resolved
            .target()
            .authored()
            .get("type")
            .and_then(|v| v.as_str().map(str::to_owned))
            .as_deref(),
        Some("object")
    );
    assert!(edge.resolve_static().is_err()); // The original snapshot remains incomplete.

    let mut builder = DocumentBuilder::new(Limits::default(), GraphLimits::default());
    let entry = builder
        .add(
            ResourceInput::anonymous(Arc::<[u8]>::from(
                br##"{"left":{"$ref":"#/right"},"right":{"type":"string"}}"##.as_slice(),
            ))
            .layout(ResourceLayout::new().schema(
                "/left",
                SchemaProfile::JsonSchema {
                    dialect_uri: "https://json-schema.org/draft/2020-12/schema".into(),
                },
            )),
        )
        .map_err(|failure| failure.diagnostic().clone())?;
    let document = builder.build(entry)?;
    let edge = document
        .entry_resource()
        .schema_at("/left")?
        .references()?
        .next()
        .ok_or_else(|| Diagnostic::new(Code::MissingReference))?;
    let missing = edge
        .resolve_static()
        .expect_err("the target is deliberately untyped");
    assert_eq!(
        missing
            .resource_need()
            .and_then(|need| need.requested_uri()),
        None
    );
    let completed = complete_layout(&document, &missing, "/right")?;
    completed
        .entry_resource()
        .schema_at("/left")?
        .references()?
        .next()
        .ok_or_else(|| Diagnostic::new(Code::MissingReference))?
        .resolve_static()?;
    Ok(())
}
