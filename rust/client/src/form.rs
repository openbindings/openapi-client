//! Finite logical named forms. Encoding is internal to shared request preparation.
use crate::{
    Argument, BodyFields, Cancellation, Code, Diagnostic, DiagnosticContext, DiagnosticReason,
    Edition, FieldRequirement, LimitKind, Limits, MediaDescription, OrdinaryValue, Value,
    ValueKind,
};
use std::{borrow::Cow, fmt, sync::Arc};

/// Corrective reason for a named-form refusal. It contains no supplied values.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum FormReason {
    /// Two logical fields have the same name; use an array for repetition.
    DuplicateField,
    /// No declared form field matches a Swagger input.
    UnknownField,
    /// The representation needs unresolved schema facts.
    UnresolvedShape,
    /// The selected encoding style is outside this finite profile.
    UnsupportedStyle,
    /// The representation requires an unsupported content coding.
    UnsupportedCoding,
    /// Positional or nested multipart needs a richer body representation.
    StructuredMultipart,
    /// A concrete part media type must be elected.
    PartMediaRequired,
    /// A supplied part media type does not match its declaration.
    PartMediaMismatch,
    /// A required or supplied part header is outside RFC7578 form-data rules.
    UnsupportedHeader,
    /// The library owns Content-Type, Content-Disposition and boundary framing.
    FramingConflict,
    /// A part's metadata was assigned more than once.
    DuplicateMetadata,
    /// A filename/name contains unsafe control characters.
    InvalidMetadata,
    /// Bytes or the supplied value shape cannot use this representation.
    ValueShape,
}
/// Borrowed finite bytes or an existing immutable shared owner. No I/O is performed.
#[derive(Clone)]
pub enum FiniteBytes<'a> {
    /// Borrow bytes until request preparation finishes.
    Borrowed(&'a [u8]),
    /// Retain an already allocated immutable owner until preparation finishes.
    Shared(Arc<[u8]>),
}
impl<'a> From<&'a [u8]> for FiniteBytes<'a> {
    fn from(v: &'a [u8]) -> Self {
        Self::Borrowed(v)
    }
}
impl<'a, const N: usize> From<&'a [u8; N]> for FiniteBytes<'a> {
    fn from(v: &'a [u8; N]) -> Self {
        Self::Borrowed(v)
    }
}
impl From<Arc<[u8]>> for FiniteBytes<'_> {
    fn from(v: Arc<[u8]>) -> Self {
        Self::Shared(v)
    }
}
impl FiniteBytes<'_> {
    fn bytes(&self) -> &[u8] {
        match self {
            Self::Borrowed(v) => v,
            Self::Shared(v) => v,
        }
    }
}
impl fmt::Debug for FiniteBytes<'_> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("FiniteBytes([redacted])")
    }
}
#[derive(Clone)]
enum Content<'a> {
    Value(Argument<'a>),
    Bytes(FiniteBytes<'a>),
}
/// Name-free finite content with optional representation metadata.
/// Header slices borrow caller storage; this object grows no hidden collections.
#[derive(Clone)]
pub struct Part<'a> {
    content: Content<'a>,
    filename: Option<Cow<'a, str>>,
    media: Option<Cow<'a, str>>,
    headers: Option<&'a [(&'a str, Argument<'a>)]>,
    duplicate: bool,
}
impl<'a> Part<'a> {
    /// Supply an ordinary or exact logical value with part metadata.
    pub fn value(v: impl Into<Argument<'a>>) -> Self {
        Self::new(Content::Value(v.into()))
    }
    /// Supply finite resource bytes; filenames do not cause filesystem access.
    pub fn bytes(v: impl Into<FiniteBytes<'a>>) -> Self {
        Self::new(Content::Bytes(v.into()))
    }
    fn new(content: Content<'a>) -> Self {
        Self {
            content,
            filename: None,
            media: None,
            headers: None,
            duplicate: false,
        }
    }
    /// Elect a filename. Duplicate assignments refuse during admission.
    pub fn filename(mut self, v: impl Into<Cow<'a, str>>) -> Self {
        self.duplicate |= self.filename.is_some();
        self.filename = Some(v.into());
        self
    }
    /// Elect one concrete declared part representation; no sniffing occurs.
    pub fn media_type(mut self, v: impl Into<Cow<'a, str>>) -> Self {
        self.duplicate |= self.media.is_some();
        self.media = Some(v.into());
        self
    }
    /// Supply declared part-header values. Strict form-data framing restrictions apply.
    pub fn headers(mut self, v: &'a [(&'a str, Argument<'a>)]) -> Self {
        self.duplicate |= self.headers.is_some();
        self.headers = Some(v);
        self
    }
}
impl fmt::Debug for Part<'_> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("Part([redacted])")
    }
}
/// A logical field value. Arrays express repetition rather than repeated field assignments.
#[derive(Clone)]
pub enum FormValue<'a> {
    /// Ordinary or already admitted exact data, including scalar arrays.
    Value(Argument<'a>),
    /// One resource/value carrying part metadata.
    Part(Part<'a>),
    /// One logical array whose items carry individual metadata or binary data.
    Parts(&'a [Part<'a>]),
}
impl fmt::Debug for FormValue<'_> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("FormValue([redacted])")
    }
}
impl<'a> From<Argument<'a>> for FormValue<'a> {
    fn from(v: Argument<'a>) -> Self {
        Self::Value(v)
    }
}
impl<'a> From<OrdinaryValue<'a>> for FormValue<'a> {
    fn from(v: OrdinaryValue<'a>) -> Self {
        Self::Value(v.into())
    }
}
impl From<Value> for FormValue<'_> {
    fn from(v: Value) -> Self {
        Self::Value(v.into())
    }
}
impl<'a> From<Part<'a>> for FormValue<'a> {
    fn from(v: Part<'a>) -> Self {
        Self::Part(v)
    }
}
impl<'a> From<&'a str> for FormValue<'a> {
    fn from(v: &'a str) -> Self {
        Self::Value(v.into())
    }
}
impl<'a> From<&'a String> for FormValue<'a> {
    fn from(v: &'a String) -> Self {
        Self::Value(v.into())
    }
}
macro_rules! scalars { ($($t:ty),*) => {$(impl From<$t> for FormValue<'_> { fn from(v:$t)->Self { Self::Value(v.into()) } })*}; }
scalars!(bool, i8, i16, i32, i64, isize, u8, u16, u32, u64, usize);
/// One unique logical field in a form, preserving caller order.
#[derive(Clone)]
pub struct FormField<'a> {
    pub(crate) name: Cow<'a, str>,
    value: FormValue<'a>,
}
impl<'a> FormField<'a> {
    /// Construct a field; request admission checks counts, metadata and duplicates.
    pub fn new(name: impl Into<Cow<'a, str>>, value: impl Into<FormValue<'a>>) -> Self {
        Self {
            name: name.into(),
            value: value.into(),
        }
    }
}
impl fmt::Debug for FormField<'_> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("FormField([redacted])")
    }
}
fn refusal(code: Code, reason: FormReason) -> Diagnostic {
    Diagnostic::new(code).reasoned(DiagnosticReason::Form(reason))
}
fn located(source: &Value, code: Code, reason: FormReason) -> Diagnostic {
    source.locate(refusal(code, reason))
}
fn add(total: &mut usize, n: usize, max: usize, kind: LimitKind) -> Result<(), Diagnostic> {
    *total = total
        .checked_add(n)
        .ok_or_else(|| Diagnostic::limited(kind, max, usize::MAX))?;
    if *total > max {
        return Err(Diagnostic::limited(kind, max, *total));
    }
    Ok(())
}
/// Incremental admission before request-owned collection growth.
#[derive(Default)]
pub(crate) struct Admission {
    metadata: usize,
    headers: usize,
    parts: usize,
    work: usize,
    bytes: usize,
}
impl Admission {
    pub(crate) fn comparisons(&mut self, n: usize, limits: &Limits) -> Result<(), Diagnostic> {
        add(&mut self.work, n, limits.form_work, LimitKind::FormWork)
    }
    pub(crate) fn name(&mut self, f: &FormField<'_>, limits: &Limits) -> Result<(), Diagnostic> {
        add(&mut self.work, 1, limits.form_work, LimitKind::FormWork)?;
        add(
            &mut self.metadata,
            f.name.len(),
            limits.form_metadata_bytes,
            LimitKind::FormMetadataBytes,
        )
    }
    pub(crate) fn field(&mut self, f: &FormField<'_>, limits: &Limits) -> Result<(), Diagnostic> {
        match &f.value {
            FormValue::Value(v) => self.arg(v, limits),
            FormValue::Part(p) => self.part(p, limits),
            FormValue::Parts(parts) => {
                if parts.len() > limits.form_parts {
                    return Err(Diagnostic::limited(
                        LimitKind::FormParts,
                        limits.form_parts,
                        parts.len(),
                    ));
                }
                for p in *parts {
                    self.part(p, limits)?;
                }
                Ok(())
            }
        }
    }
    fn arg(&mut self, a: &Argument<'_>, l: &Limits) -> Result<(), Diagnostic> {
        let minimum = match a {
            Argument::Ordinary(OrdinaryValue::String(v)) => v.len(),
            Argument::Exact(v) => match v.kind() {
                ValueKind::String => v.as_str().unwrap().len(),
                ValueKind::Number | ValueKind::Boolean => v.raw().len(),
                _ => 0,
            },
            _ => 0,
        };
        add(&mut self.bytes, minimum, l.body_bytes, LimitKind::BodyBytes)
    }
    fn part(&mut self, p: &Part<'_>, l: &Limits) -> Result<(), Diagnostic> {
        add(&mut self.work, 1, l.form_work, LimitKind::FormWork)?;
        if p.duplicate {
            return Err(refusal(
                Code::InvalidSelection,
                FormReason::DuplicateMetadata,
            ));
        }
        add(&mut self.parts, 1, l.form_parts, LimitKind::FormParts)?;
        for v in [&p.filename, &p.media].into_iter().flatten() {
            add(
                &mut self.metadata,
                v.len(),
                l.form_metadata_bytes,
                LimitKind::FormMetadataBytes,
            )?;
        }
        if let Some(headers) = p.headers {
            add(
                &mut self.headers,
                headers.len(),
                l.headers,
                LimitKind::HeaderCount,
            )?;
            for (name, value) in headers {
                add(&mut self.work, 1, l.form_work, LimitKind::FormWork)?;
                add(
                    &mut self.metadata,
                    name.len(),
                    l.form_metadata_bytes,
                    LimitKind::FormMetadataBytes,
                )?;
                self.arg(value, l)?;
            }
        }
        match &p.content {
            Content::Value(v) => self.arg(v, l),
            Content::Bytes(v) => add(
                &mut self.bytes,
                v.bytes().len(),
                l.body_bytes,
                LimitKind::BodyBytes,
            ),
        }
    }
}
impl<'a> FormField<'a> {
    pub(crate) fn arguments(&self, visit: &mut impl FnMut(&Argument<'a>)) {
        let mut part = |p: &Part<'a>| {
            if let Content::Value(v) = &p.content {
                visit(v)
            };
            if let Some(headers) = p.headers {
                for (_, v) in headers {
                    visit(v)
                }
            }
        };
        match &self.value {
            FormValue::Value(v) => visit(v),
            FormValue::Part(p) => part(p),
            FormValue::Parts(ps) => {
                for p in *ps {
                    part(p)
                }
            }
        }
    }
    pub(crate) fn lower(self, exact: &mut impl FnMut(Argument<'a>) -> Value) -> ReadyField<'a> {
        let content = match self.value {
            FormValue::Value(v) => ReadyValue::One(ReadyPart {
                payload: Payload::Value(exact(v)),
                filename: None,
                media: None,
                headers: Vec::new(),
            }),
            FormValue::Part(p) => ReadyValue::One(p.lower(exact)),
            FormValue::Parts(ps) => {
                ReadyValue::Many(ps.iter().cloned().map(|p| p.lower(exact)).collect())
            }
        };
        ReadyField {
            name: self.name,
            content,
        }
    }
}
impl<'a> Part<'a> {
    fn lower(self, exact: &mut impl FnMut(Argument<'a>) -> Value) -> ReadyPart<'a> {
        ReadyPart {
            payload: match self.content {
                Content::Value(v) => Payload::Value(exact(v)),
                Content::Bytes(b) => Payload::Bytes(b),
            },
            filename: self.filename,
            media: self.media,
            headers: self
                .headers
                .unwrap_or(&[])
                .iter()
                .map(|(n, v)| (*n, exact(v.clone())))
                .collect(),
        }
    }
}
#[derive(Clone)]
enum Payload<'a> {
    Value(Value),
    Bytes(FiniteBytes<'a>),
}
pub(crate) struct ReadyField<'a> {
    name: Cow<'a, str>,
    content: ReadyValue<'a>,
}
enum ReadyValue<'a> {
    One(ReadyPart<'a>),
    Many(Vec<ReadyPart<'a>>),
}
#[derive(Clone)]
struct ReadyPart<'a> {
    payload: Payload<'a>,
    filename: Option<Cow<'a, str>>,
    media: Option<Cow<'a, str>>,
    headers: Vec<(&'a str, Value)>,
}
#[derive(Clone)]
enum Piece<'a> {
    Scalar(Value),
    Json(Value),
    Bytes(FiniteBytes<'a>),
    Separator(&'static [u8]),
}
impl Piece<'_> {
    fn bytes(&self) -> Result<&[u8], Diagnostic> {
        Ok(match self {
            Self::Scalar(v) => match v.kind() {
                ValueKind::String => v.as_str().unwrap().as_bytes(),
                ValueKind::Number | ValueKind::Boolean => v.raw().as_bytes(),
                _ => return Err(located(v, Code::UnsupportedValue, FormReason::ValueShape)),
            },
            Self::Json(v) => v.raw().as_bytes(),
            Self::Bytes(b) => b.bytes(),
            Self::Separator(v) => v,
        })
    }
}
struct Emission<'a> {
    name: String,
    filename: Option<String>,
    media: String,
    pieces: Vec<Piece<'a>>,
}
struct Work<'a> {
    limits: &'a Limits,
    cancel: &'a Cancellation,
    visits: usize,
    metadata: usize,
    headers: usize,
    copied_data: usize,
}
impl Work<'_> {
    fn visit(&mut self) -> Result<(), Diagnostic> {
        self.cancel.check()?;
        add(
            &mut self.visits,
            1,
            self.limits.form_work,
            LimitKind::FormWork,
        )
    }
    fn text(&mut self, s: &str) -> Result<String, Diagnostic> {
        add(
            &mut self.metadata,
            s.len(),
            self.limits.form_metadata_bytes,
            LimitKind::FormMetadataBytes,
        )?;
        Ok(s.into())
    }
}
fn bool_field(node: &Value, key: &str, default: bool) -> Result<bool, Diagnostic> {
    node.get(key)
        .map(|v| v.as_bool().ok_or_else(|| v.error(Code::InvalidDeclaration)))
        .unwrap_or(Ok(default))
}
// Retain the source handle rather than copying untrusted declaration strings.
fn str_field(node: &Value, key: &str) -> Result<Option<Value>, Diagnostic> {
    node.get(key)
        .map(|v| {
            if v.as_str().is_some() {
                Ok(v)
            } else {
                Err(v.error(Code::InvalidDeclaration))
            }
        })
        .transpose()
}
fn field_context(error: Diagnostic, index: usize, name: &str, item: Option<usize>) -> Diagnostic {
    if error.context().is_some() {
        error
    } else if name.len() <= 4096 {
        error.contextual(DiagnosticContext::FormField {
            index,
            name: name.into(),
            item,
        })
    } else {
        error.omit_context()
    }
}

pub(crate) fn encode<'a>(
    fields: &[ReadyField<'a>],
    media: &MediaDescription,
    selected: &str,
    edition: Edition,
    limits: &Limits,
    cancel: &Cancellation,
) -> Result<(Arc<[u8]>, String), Diagnostic> {
    let parsed = crate::prepare::parse_media(selected, false)?;
    let multipart = parsed.essence == "multipart/form-data";
    if !multipart && parsed.essence != "application/x-www-form-urlencoded" {
        return Err(located(
            &media.authored,
            Code::UnsupportedMedia,
            FormReason::StructuredMultipart,
        ));
    }
    if parsed.parameters.contains_key("boundary") {
        return Err(located(
            &media.authored,
            Code::InvalidSelection,
            FormReason::FramingConflict,
        ));
    }
    if parsed
        .parameters
        .get("charset")
        .is_some_and(|v| !v.eq_ignore_ascii_case("utf-8"))
    {
        return Err(media.authored.error(Code::UnsupportedMedia));
    }
    if media.prefix_encoding.is_some()
        || media.item_encoding.is_some()
        || media.item_schema.is_some()
    {
        return Err(located(
            &media.authored,
            Code::UnsupportedMedia,
            FormReason::StructuredMultipart,
        ));
    }
    let known = match &media.fields {
        BodyFields::Declared(fs) => fs.as_slice(),
        BodyFields::Partial { fields, .. } => fields.as_slice(),
        _ => &[],
    };
    let mut work = Work {
        limits,
        cancel,
        visits: 0,
        metadata: 0,
        headers: 0,
        copied_data: 0,
    };
    let mut out = Vec::new();
    for declared in known {
        work.visit()?;
        if declared.requirement == FieldRequirement::ProtocolRequired {
            let mut present = false;
            for field in fields {
                work.visit()?;
                present |= field.name == declared.name;
            }
            if present {
                continue;
            }
            return Err(declared.authored.error(Code::MissingInput));
        }
    }
    for (index, field) in fields.iter().enumerate() {
        work.visit()?;
        let result = (|| {
            let mut desc = None;
            for declared in known {
                work.visit()?;
                if declared.name == field.name {
                    desc = Some(declared);
                    break;
                }
            }
            // Direct properties can be encoded independently of unrelated schema
            // composition. Missing facts must still refuse before choosing a codec.
            if desc.is_none()
                && let BodyFields::Opaque(issue) | BodyFields::Partial { issue, .. } = &media.fields
            {
                return Err(located(
                    &issue.source,
                    Code::UnsupportedMedia,
                    FormReason::UnresolvedShape,
                ));
            }
            if edition == Edition::Swagger20 && desc.is_none() {
                return Err(refusal(Code::InvalidSelection, FormReason::UnknownField));
            }
            let schema = desc.map(|d| d.schema.value.clone());
            let encoding = desc.and_then(|d| d.encoding.clone());
            let source = encoding
                .as_ref()
                .or(schema.as_ref())
                .unwrap_or(&media.authored);
            match &field.content {
                ReadyValue::One(part) => plan(
                    &field.name,
                    part,
                    Rules {
                        schema: schema.as_ref(),
                        encoding: encoding.as_ref(),
                        source,
                        edition,
                        multipart,
                    },
                    &mut out,
                    &mut work,
                ),
                ReadyValue::Many(parts) => {
                    if edition == Edition::Swagger20 {
                        return Err(located(
                            source,
                            Code::UnsupportedValue,
                            FormReason::ValueShape,
                        ));
                    }
                    if edition != Edition::OpenApi32
                        && style(encoding.as_ref(), edition, multipart)? == Some(false)
                    {
                        return Err(located(
                            source,
                            Code::UnsupportedValue,
                            FormReason::ValueShape,
                        ));
                    }
                    let item = schema.as_ref().and_then(|s| s.get("items"));
                    for (i, part) in parts.iter().enumerate() {
                        plan_one(
                            &field.name,
                            part,
                            &part.payload,
                            Rules {
                                schema: item.as_ref(),
                                encoding: encoding.as_ref(),
                                source,
                                edition,
                                multipart,
                            },
                            &mut out,
                            &mut work,
                        )
                        .map_err(|e| field_context(e, index, &field.name, Some(i)))?;
                    }
                    Ok(())
                }
            }
        })();
        result.map_err(|e| field_context(e, index, &field.name, None))?;
    }
    finish(out, selected, multipart, &mut work)
}
fn style(
    encoding: Option<&Value>,
    edition: Edition,
    multipart: bool,
) -> Result<Option<bool>, Diagnostic> {
    let Some(e) = encoding else { return Ok(None) };
    if e.kind() != ValueKind::Object {
        return Err(e.error(Code::InvalidDeclaration));
    }
    if edition == Edition::OpenApi32
        && ["encoding", "prefixEncoding", "itemEncoding"]
            .iter()
            .any(|k| e.get(k).is_some())
    {
        return Err(located(
            e,
            Code::UnsupportedMedia,
            FormReason::StructuredMultipart,
        ));
    }
    if multipart && edition == Edition::OpenApi30 {
        return Ok(None);
    }
    if !["style", "explode", "allowReserved"]
        .iter()
        .any(|k| e.get(k).is_some())
    {
        return Ok(None);
    }
    if str_field(e, "style")?.is_some_and(|s| s.as_str() != Some("form"))
        || (!multipart && bool_field(e, "allowReserved", false)?)
    {
        return Err(located(
            e,
            Code::UnsupportedMedia,
            FormReason::UnsupportedStyle,
        ));
    }
    Ok(Some(bool_field(e, "explode", true)?))
}
#[derive(Clone, Copy)]
struct Rules<'s> {
    schema: Option<&'s Value>,
    encoding: Option<&'s Value>,
    source: &'s Value,
    edition: Edition,
    multipart: bool,
}
fn plan<'a>(
    name: &str,
    part: &ReadyPart<'a>,
    rules: Rules<'_>,
    out: &mut Vec<Emission<'a>>,
    work: &mut Work<'_>,
) -> Result<(), Diagnostic> {
    let Rules {
        schema,
        encoding,
        source,
        edition,
        multipart,
    } = rules;
    work.visit()?;
    let styled = style(encoding, edition, multipart)?;
    validate_part(part, schema, encoding, source, edition, multipart, work)?;
    if styled.is_some() && part.media.is_some() {
        return Err(located(
            source,
            Code::InvalidSelection,
            FormReason::PartMediaMismatch,
        ));
    }
    if let Payload::Value(value) = &part.payload {
        if value.kind() == ValueKind::Array {
            let item = schema.and_then(|s| s.get("items"));
            let legacy = if edition == Edition::Swagger20 {
                let schema = schema.unwrap();
                if schema
                    .get("type")
                    .is_none_or(|v| v.as_str() != Some("array"))
                    || item.is_none()
                {
                    return Err(schema.error(Code::InvalidDeclaration));
                }
                if item
                    .as_ref()
                    .and_then(|s| s.get("type"))
                    .is_some_and(|v| v.as_str() == Some("file"))
                {
                    return Err(item.as_ref().unwrap().error(Code::InvalidDeclaration));
                }
                str_field(schema, "collectionFormat")?
            } else {
                None
            };
            let join =
                match legacy
                    .as_ref()
                    .and_then(Value::as_str)
                    .or(if edition == Edition::Swagger20 {
                        Some("csv")
                    } else {
                        None
                    }) {
                    Some("csv") => Some(b",".as_slice()),
                    Some("ssv") => Some(b" ".as_slice()),
                    Some("tsv") => Some(b"\t".as_slice()),
                    Some("pipes") => Some(b"|".as_slice()),
                    Some("multi") => None,
                    Some(_) => return Err(schema.unwrap().error(Code::InvalidDeclaration)),
                    None if styled == Some(false) && edition != Edition::OpenApi32 => {
                        Some(b",".as_slice())
                    }
                    None => None,
                };
            if let Some(separator) = join {
                if part.media.is_some() {
                    return Err(located(
                        source,
                        Code::InvalidSelection,
                        FormReason::PartMediaMismatch,
                    ));
                }
                let mut pieces = Vec::new();
                for value in value.elements().unwrap() {
                    work.visit()?;
                    if !pieces.is_empty() {
                        push_piece(&mut pieces, Piece::Separator(separator), work)?;
                    }
                    Piece::Scalar(value.clone()).bytes()?;
                    push_piece(&mut pieces, Piece::Scalar(value), work)?;
                }
                if !pieces.is_empty() {
                    emit(
                        name,
                        part,
                        work.text("text/plain")?,
                        pieces,
                        multipart,
                        out,
                        work,
                    )?;
                }
            } else {
                for value in value.elements().unwrap() {
                    work.visit()?;
                    let payload = Payload::Value(value);
                    plan_one(
                        name,
                        part,
                        &payload,
                        Rules {
                            schema: item.as_ref(),
                            ..rules
                        },
                        out,
                        work,
                    )?;
                }
            }
            return Ok(());
        }
        if let Some(explode) = styled
            && value.kind() == ValueKind::Object
        {
            let mut pieces = Vec::new();
            for (key, value) in value.members().unwrap() {
                work.visit()?;
                Piece::Scalar(value.clone()).bytes()?;
                if explode {
                    emit(
                        key,
                        part,
                        work.text("text/plain")?,
                        vec![Piece::Scalar(value)],
                        multipart,
                        out,
                        work,
                    )?;
                } else {
                    if !pieces.is_empty() {
                        push_piece(&mut pieces, Piece::Separator(b","), work)?;
                    }
                    // Exact JSON ownership is reused for values; object keys require one bounded copy.
                    add(
                        &mut work.copied_data,
                        key.len(),
                        work.limits.body_bytes,
                        LimitKind::BodyBytes,
                    )?;
                    work.visit()?; // Admit the key-copy operation before allocation.
                    push_piece(
                        &mut pieces,
                        Piece::Bytes(FiniteBytes::Shared(Arc::from(key.as_bytes()))),
                        work,
                    )?;
                    push_piece(&mut pieces, Piece::Separator(b","), work)?;
                    push_piece(&mut pieces, Piece::Scalar(value), work)?;
                }
            }
            if !explode && !pieces.is_empty() {
                emit(
                    name,
                    part,
                    work.text("text/plain")?,
                    pieces,
                    multipart,
                    out,
                    work,
                )?;
            }
            return Ok(());
        }
    }
    plan_one(name, part, &part.payload, rules, out, work)
}
fn push_piece<'a>(
    pieces: &mut Vec<Piece<'a>>,
    piece: Piece<'a>,
    work: &mut Work<'_>,
) -> Result<(), Diagnostic> {
    work.visit()?;
    // At most one stored piece per charged visit; cap geometric reservation at
    // the explicit work limit rather than allocating from untrusted size hints.
    if pieces.len() == pieces.capacity() {
        let capacity = pieces
            .capacity()
            .saturating_mul(2)
            .max(4)
            .min(work.limits.form_work);
        pieces.reserve_exact(capacity.saturating_sub(pieces.len()));
    }
    pieces.push(piece);
    Ok(())
}
fn validate_part(
    part: &ReadyPart<'_>,
    schema: Option<&Value>,
    encoding: Option<&Value>,
    source: &Value,
    edition: Edition,
    multipart: bool,
    work: &mut Work<'_>,
) -> Result<(), Diagnostic> {
    if let Some(e) = encoding
        && multipart
        && let Some(headers) = e.get("headers")
    {
        for (name, header) in headers
            .members()
            .ok_or_else(|| headers.error(Code::InvalidDeclaration))?
        {
            work.visit()?;
            if name.eq_ignore_ascii_case("content-type") {
                continue;
            }
            if header.get("$ref").is_some() {
                return Err(located(
                    &header,
                    Code::UnsupportedMedia,
                    FormReason::UnresolvedShape,
                ));
            }
            if bool_field(&header, "required", false)? {
                return Err(located(
                    &header,
                    Code::UnsupportedMedia,
                    FormReason::UnsupportedHeader,
                ));
            }
        }
    }
    if !part.headers.is_empty() {
        return Err(located(
            source,
            Code::InvalidHeader,
            if part.headers.iter().any(|(n, _)| {
                n.eq_ignore_ascii_case("content-type")
                    || n.eq_ignore_ascii_case("content-disposition")
            }) {
                FormReason::FramingConflict
            } else {
                FormReason::UnsupportedHeader
            },
        ));
    }
    if let Some(s) = schema {
        if edition != Edition::Swagger20
            && (s.get("contentEncoding").is_some()
                || edition == Edition::OpenApi30
                    && s.get("format").is_some_and(|v| v.as_str() == Some("byte")))
        {
            return Err(located(
                s,
                Code::UnsupportedMedia,
                FormReason::UnsupportedCoding,
            ));
        }
        if edition == Edition::Swagger20
            && s.get("type").is_some_and(|v| v.as_str() == Some("file"))
            && !matches!(part.payload, Payload::Bytes(_))
        {
            return Err(located(s, Code::UnsupportedValue, FormReason::ValueShape));
        }
        if edition == Edition::Swagger20
            && !bool_field(s, "allowEmptyValue", false)?
            && matches!(&part.payload,Payload::Value(v) if v.as_str()==Some(""))
        {
            return Err(located(s, Code::UnsupportedValue, FormReason::ValueShape));
        }
    }
    Ok(())
}
fn plan_one<'a>(
    name: &str,
    part: &ReadyPart<'_>,
    payload: &Payload<'a>,
    rules: Rules<'_>,
    out: &mut Vec<Emission<'a>>,
    work: &mut Work<'_>,
) -> Result<(), Diagnostic> {
    let Rules {
        schema,
        encoding,
        source,
        edition,
        multipart,
    } = rules;
    work.visit()?;
    let styled = style(encoding, edition, multipart)?;
    validate_part(part, schema, encoding, source, edition, multipart, work)?;
    let content_type = if styled.is_some() {
        work.text("text/plain")?
    } else {
        choose_part_media(
            part.media.as_deref(),
            schema,
            encoding,
            source,
            edition,
            work,
        )?
    };
    if styled.is_some() && part.media.is_some() {
        return Err(located(
            source,
            Code::InvalidSelection,
            FormReason::PartMediaMismatch,
        ));
    }
    let parsed = crate::prepare::parse_media(&content_type, false)?;
    if parsed
        .parameters
        .get("charset")
        .is_some_and(|v| !v.eq_ignore_ascii_case("utf-8"))
    {
        return Err(located(
            source,
            Code::UnsupportedMedia,
            FormReason::UnsupportedCoding,
        ));
    }
    if parsed.essence.starts_with("multipart/") {
        return Err(located(
            source,
            Code::UnsupportedMedia,
            FormReason::StructuredMultipart,
        ));
    }
    let piece = match payload {
        Payload::Bytes(b) => {
            if !multipart {
                if parsed.essence == "application/octet-stream" {
                    return Err(located(
                        source,
                        Code::UnsupportedMedia,
                        FormReason::UnsupportedCoding,
                    ));
                }
                std::str::from_utf8(b.bytes())
                    .map_err(|_| located(source, Code::UnsupportedValue, FormReason::ValueShape))?;
            }
            Piece::Bytes(b.clone())
        }
        Payload::Value(v) if parsed.json => Piece::Json(v.clone()),
        Payload::Value(v) if parsed.essence == "text/plain" => {
            let piece = Piece::Scalar(v.clone());
            piece.bytes()?;
            piece
        }
        Payload::Value(_) => {
            return Err(located(
                source,
                Code::UnsupportedMedia,
                FormReason::ValueShape,
            ));
        }
    };
    emit(name, part, content_type, vec![piece], multipart, out, work)
}
fn choose_part_media(
    elected: Option<&str>,
    schema: Option<&Value>,
    encoding: Option<&Value>,
    source: &Value,
    edition: Edition,
    work: &mut Work<'_>,
) -> Result<String, Diagnostic> {
    let declared = encoding.and_then(|e| e.get("contentType"));
    let default;
    let text = if let Some(v) = &declared {
        v.as_str()
            .ok_or_else(|| v.error(Code::InvalidDeclaration))?
    } else {
        let s = schema;
        if s.is_some_and(|s| {
            ["$ref", "$dynamicRef", "allOf", "anyOf", "oneOf", "if"]
                .iter()
                .any(|k| s.get(k).is_some())
        }) {
            return Err(located(
                s.unwrap(),
                Code::UnsupportedMedia,
                FormReason::UnresolvedShape,
            ));
        }
        let ty = s.and_then(|s| s.get("type"));
        let format = s.and_then(|s| s.get("format"));
        default = match ty.as_ref().and_then(Value::as_str) {
            Some("file") if edition == Edition::Swagger20 => "application/octet-stream",
            Some("string")
                if edition == Edition::OpenApi30
                    && format
                        .as_ref()
                        .is_some_and(|f| matches!(f.as_str(), Some("binary" | "byte"))) =>
            {
                "application/octet-stream"
            }
            Some("string" | "number" | "integer" | "boolean") => "text/plain",
            Some("object" | "array") => "application/json",
            None if ty.is_none() && matches!(edition, Edition::OpenApi31 | Edition::OpenApi32) => {
                "application/octet-stream"
            }
            _ => {
                return Err(located(
                    s.unwrap_or(source),
                    Code::UnsupportedMedia,
                    FormReason::UnresolvedShape,
                ));
            }
        };
        default
    };
    // Bound declaration parsing allocations before parsing/copying. This is a
    // temporary-size admission; emitted effective media is charged below.
    if text.len() > work.limits.form_metadata_bytes {
        return Err(declared
            .as_ref()
            .unwrap_or(source)
            .locate(Diagnostic::limited(
                LimitKind::FormMetadataBytes,
                work.limits.form_metadata_bytes,
                text.len(),
            )));
    }
    // Split a media list only outside quoted parameters; a comma inside quotes is data.
    let mut entries = Vec::new();
    let mut start = 0;
    let mut quoted = false;
    let mut escape = false;
    for (i, c) in text.char_indices() {
        if escape {
            escape = false;
            continue;
        }
        if quoted && c == '\\' {
            escape = true;
            continue;
        }
        if c == '"' {
            quoted = !quoted
        }
        if c == ',' && !quoted {
            work.visit()?;
            entries.push(&text[start..i]);
            start = i + 1;
        }
    }
    if quoted || escape {
        return Err(located(
            source,
            Code::InvalidMedia,
            FormReason::PartMediaMismatch,
        ));
    }
    work.visit()?;
    entries.push(&text[start..]);
    let chosen = if let Some(elected) = elected {
        let selected = crate::prepare::parse_media(elected, false)?;
        let mut found = false;
        for entry in &entries {
            work.visit()?;
            let key = crate::prepare::parse_media(entry, true)?;
            found |= crate::prepare::media_matches(&key, &selected).is_some();
        }
        if !found {
            return Err(located(
                declared.as_ref().unwrap_or(source),
                Code::InvalidSelection,
                FormReason::PartMediaMismatch,
            ));
        }
        elected.trim()
    } else if entries.len() == 1
        && !crate::prepare::parse_media(entries[0], true)?
            .essence
            .contains('*')
    {
        entries[0].trim()
    } else {
        return Err(located(
            declared.as_ref().unwrap_or(source),
            Code::AmbiguousSelection,
            FormReason::PartMediaRequired,
        ));
    };
    work.text(chosen)
}
fn emit<'a>(
    name: &str,
    part: &ReadyPart<'_>,
    media: String,
    pieces: Vec<Piece<'a>>,
    multipart: bool,
    out: &mut Vec<Emission<'a>>,
    work: &mut Work<'_>,
) -> Result<(), Diagnostic> {
    if out.len() >= work.limits.form_parts {
        return Err(Diagnostic::limited(
            LimitKind::FormParts,
            work.limits.form_parts,
            out.len().saturating_add(1),
        ));
    }
    if !multipart && part.filename.is_some() {
        return Err(refusal(Code::UnsupportedValue, FormReason::ValueShape));
    }
    let name = work.text(name)?;
    if let Some(filename) = &part.filename {
        add(
            &mut work.metadata,
            filename.len(),
            work.limits.form_metadata_bytes,
            LimitKind::FormMetadataBytes,
        )?;
    }
    if multipart {
        for metadata in std::iter::once(name.as_str()).chain(part.filename.as_deref()) {
            if metadata.bytes().any(|b| b < 32 || b == 127) {
                return Err(refusal(Code::InvalidHeader, FormReason::InvalidMetadata));
            }
        }
        add(
            &mut work.headers,
            2,
            work.limits.headers,
            LimitKind::HeaderCount,
        )?;
    }
    out.reserve_exact(1);
    out.push(Emission {
        name,
        filename: part.filename.as_deref().map(str::to_owned),
        media,
        pieces,
    });
    Ok(())
}

// Count, collision scan and final write run the same bounded byte traversal.
// No fully encoded per-part buffers or encoded URL strings are allocated.
enum Sink<'a> {
    Count,
    Scan {
        needle: &'a [u8],
        matched: usize,
        found: bool,
    },
    Output(&'a mut Vec<u8>),
}
struct Writer<'a, 'b> {
    sink: Sink<'a>,
    count: usize,
    maximum: usize,
    cancel: &'b Cancellation,
    work: &'b mut usize,
    maximum_work: usize,
}
impl Writer<'_, '_> {
    fn put(&mut self, bytes: &[u8]) -> Result<(), Diagnostic> {
        add(
            &mut self.count,
            bytes.len(),
            self.maximum,
            LimitKind::BodyBytes,
        )?;
        for chunk in bytes.chunks(4096) {
            self.cancel.check()?;
            add(self.work, 1, self.maximum_work, LimitKind::FormWork)?;
            match &mut self.sink {
                Sink::Count => {}
                Sink::Output(out) => out.extend_from_slice(chunk),
                Sink::Scan {
                    needle,
                    matched,
                    found,
                } => {
                    for &b in chunk {
                        if b == needle[*matched] {
                            *matched += 1;
                            if *matched == needle.len() {
                                *found = true;
                                *matched = 0;
                            }
                        } else {
                            *matched = usize::from(b == needle[0]);
                        }
                    }
                }
            }
        }
        Ok(())
    }
    fn encoded(&mut self, bytes: &[u8]) -> Result<(), Diagnostic> {
        const HEX: &[u8] = b"0123456789ABCDEF";
        // One bounded stack chunk; no allocation proportional to escape expansion.
        let mut buf = [0u8; 3072];
        for input in bytes.chunks(1024) {
            let mut len = 0;
            for &b in input {
                if b.is_ascii_alphanumeric() || b"*-._".contains(&b) {
                    buf[len] = b;
                    len += 1;
                } else if b == b' ' {
                    buf[len] = b'+';
                    len += 1;
                } else {
                    buf[len] = b'%';
                    buf[len + 1] = HEX[(b >> 4) as usize];
                    buf[len + 2] = HEX[(b & 15) as usize];
                    len += 3;
                }
            }
            self.put(&buf[..len])?;
        }
        Ok(())
    }
    fn quoted(&mut self, text: &str) -> Result<(), Diagnostic> {
        let mut start = 0;
        for (i, b) in text.bytes().enumerate() {
            if b == b'"' || b == b'\\' {
                self.put(&text.as_bytes()[start..i])?;
                self.put(b"\\")?;
                self.put(&[b])?;
                start = i + 1;
            }
        }
        self.put(&text.as_bytes()[start..])
    }
}
fn part_bytes(e: &Emission<'_>, writer: &mut Writer<'_, '_>) -> Result<(), Diagnostic> {
    writer.put(b"Content-Disposition: form-data; name=\"")?;
    writer.quoted(&e.name)?;
    writer.put(b"\"")?;
    if let Some(filename) = &e.filename {
        writer.put(b"; filename=\"")?;
        writer.quoted(filename)?;
        writer.put(b"\"")?;
    }
    writer.put(b"\r\nContent-Type: ")?;
    writer.put(e.media.as_bytes())?;
    writer.put(b"\r\n\r\n")?;
    for piece in &e.pieces {
        writer.put(piece.bytes()?)?;
    }
    Ok(())
}
fn write_body(
    parts: &[Emission<'_>],
    boundary: Option<&str>,
    writer: &mut Writer<'_, '_>,
) -> Result<(), Diagnostic> {
    if let Some(boundary) = boundary {
        for part in parts {
            writer.put(b"--")?;
            writer.put(boundary.as_bytes())?;
            writer.put(b"\r\n")?;
            part_bytes(part, writer)?;
            writer.put(b"\r\n")?;
        }
        writer.put(b"--")?;
        writer.put(boundary.as_bytes())?;
        writer.put(b"--\r\n")?;
    } else {
        for (i, part) in parts.iter().enumerate() {
            if i != 0 {
                writer.put(b"&")?;
            }
            writer.encoded(part.name.as_bytes())?;
            writer.put(b"=")?;
            for piece in &part.pieces {
                writer.encoded(piece.bytes()?)?;
            }
        }
    }
    Ok(())
}
fn finish(
    parts: Vec<Emission<'_>>,
    selected: &str,
    multipart: bool,
    work: &mut Work<'_>,
) -> Result<(Arc<[u8]>, String), Diagnostic> {
    let boundary = if multipart {
        let mut chosen = None;
        for attempt in 0..work.limits.boundary_attempts {
            work.visit()?;
            // This fixed prefix has no internal occurrence of its first byte;
            // the incremental scanner's one-byte fallback is therefore sufficient.
            let candidate = format!("openapi-boundary-{attempt:016x}");
            let mut scan = Writer {
                sink: Sink::Scan {
                    needle: candidate.as_bytes(),
                    matched: 0,
                    found: false,
                },
                count: 0,
                maximum: work.limits.body_bytes,
                cancel: work.cancel,
                work: &mut work.visits,
                maximum_work: work.limits.form_work,
            };
            for part in &parts {
                part_bytes(part, &mut scan)?;
                scan.put(b"\r\n")?;
            }
            if matches!(scan.sink, Sink::Scan { found: false, .. }) {
                chosen = Some(candidate);
                break;
            }
        }
        Some(chosen.ok_or_else(|| {
            Diagnostic::limited(
                LimitKind::BoundaryAttempts,
                work.limits.boundary_attempts,
                work.limits.boundary_attempts.saturating_add(1),
            )
        })?)
    } else {
        None
    };
    let mut count = Writer {
        sink: Sink::Count,
        count: 0,
        maximum: work.limits.body_bytes,
        cancel: work.cancel,
        work: &mut work.visits,
        maximum_work: work.limits.form_work,
    };
    write_body(&parts, boundary.as_deref(), &mut count)?;
    let length = count.count;
    work.cancel.check()?;
    let mut bytes = Vec::with_capacity(length);
    let mut writer = Writer {
        sink: Sink::Output(&mut bytes),
        count: 0,
        maximum: work.limits.body_bytes,
        cancel: work.cancel,
        work: &mut work.visits,
        maximum_work: work.limits.form_work,
    };
    write_body(&parts, boundary.as_deref(), &mut writer)?;
    let media = match boundary {
        Some(b) => format!("{selected}; boundary={b}"),
        None => selected.into(),
    };
    Ok((Arc::from(bytes), media))
}

#[cfg(test)]
mod tests {
    use super::*;
    fn collision_body(value: Value, attempts: usize) -> Result<(Arc<[u8]>, String), Diagnostic> {
        let limits = Limits {
            boundary_attempts: attempts,
            ..Limits::default()
        };
        let cancel = Cancellation::default();
        let mut work = Work {
            limits: &limits,
            cancel: &cancel,
            visits: 0,
            metadata: 0,
            headers: 0,
            copied_data: 0,
        };
        finish(
            vec![Emission {
                name: "x".into(),
                filename: None,
                media: "text/plain".into(),
                pieces: vec![Piece::Scalar(value)],
            }],
            "multipart/form-data",
            true,
            &mut work,
        )
    }
    #[test]
    fn boundaries_retry_collisions_in_decoded_material_and_bound_attempts() {
        // Authored JSON has an escape; the decoded part contains the candidate.
        let exact = crate::ExactJson::parse(
            br#""\u006fpenapi-boundary-0000000000000000""#,
            Limits::default(),
        )
        .unwrap();
        assert!(!exact.root().raw().contains("openapi-boundary"));
        let (body, media) = collision_body(exact.root(), 2).unwrap();
        assert!(media.ends_with("openapi-boundary-0000000000000001"));
        assert!(
            String::from_utf8_lossy(&body)
                .contains("\r\n\r\nopenapi-boundary-0000000000000000\r\n")
        );
        let error = collision_body(exact.root(), 1).unwrap_err();
        assert!(matches!(
            error.reason(),
            Some(DiagnosticReason::Limit {
                kind: LimitKind::BoundaryAttempts,
                ..
            })
        ));
        let both = crate::ExactJson::parse(
            br#""openapi-boundary-0000000000000000 openapi-boundary-0000000000000001""#,
            Limits::default(),
        )
        .unwrap();
        assert!(collision_body(both.root(), 2).is_err());
        assert!(collision_body(both.root(), 0).is_err());
    }
    #[test]
    fn collision_scanner_tracks_candidates_across_payload_piece_and_chunk_edges() {
        let needle = b"openapi-boundary-0000000000000000";
        let cancellation = Cancellation::default();
        let mut work = 0;
        let mut scanner = Writer {
            sink: Sink::Scan {
                needle,
                matched: 0,
                found: false,
            },
            count: 0,
            maximum: 8192,
            cancel: &cancellation,
            work: &mut work,
            maximum_work: 100,
        };
        scanner.put(&vec![b'x'; 4090]).unwrap();
        scanner.put(&needle[..15]).unwrap();
        scanner.put(&needle[15..]).unwrap();
        assert!(matches!(scanner.sink, Sink::Scan { found: true, .. }));
    }
}
