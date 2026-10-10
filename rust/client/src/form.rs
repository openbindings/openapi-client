//! Finite logical named forms. Encoding is internal to shared request preparation.
use crate::prepare::{FieldDeclarations, MediaDeclaration};
use crate::schema_facts::{FactWork, SchemaSelection};
use crate::{
    Argument, Cancellation, Code, Diagnostic, DiagnosticContext, DiagnosticReason, Edition,
    FieldRequirement, LimitKind, Limits, OrdinaryValue, RequestError, Value, ValueKind,
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
    /// Supply declared part-header values. Every nonempty assignment currently
    /// refuses in the supported named-form profile; framing headers always refuse.
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
/// Immutable reusable logical form data. Construction owns borrowed data once;
/// the fluent request form route keeps its borrows until preparation finishes.
/// Cloning shares admitted immutable storage and never copies payloads.
#[derive(Clone)]
pub struct Form {
    fields: Arc<Vec<ReadyField<'static>>>,
}
impl fmt::Debug for Form {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Form")
            .field("fields", &self.len())
            .finish_non_exhaustive()
    }
}
impl Form {
    /// Admit and own finite logical fields with one generated ordinary value owner.
    pub fn new<'a>(
        fields: impl IntoIterator<Item = FormField<'a>>,
        limits: Limits,
    ) -> Result<Self, RequestError> {
        Self::new_with_cancellation(fields, limits, &Cancellation::default())
    }
    /// Check cancellation before converting the caller iterator and pulling each entry and throughout
    /// library-owned admission/copy work. Caller iterator code is not preempted.
    pub fn new_with_cancellation<'a>(
        fields: impl IntoIterator<Item = FormField<'a>>,
        limits: Limits,
        cancellation: &Cancellation,
    ) -> Result<Self, RequestError> {
        crate::schema::checkpoint(cancellation).map_err(RequestError::Preparation)?;
        let mut fields = fields.into_iter();
        let mut admitted: Vec<FormField<'a>> = Vec::new();
        let mut budget = Admission::default();
        loop {
            crate::schema::checkpoint(cancellation).map_err(RequestError::Preparation)?;
            let Some(field) = fields.next() else { break };
            let result = (|| {
                if admitted.len() >= limits.form_fields {
                    return Err(Diagnostic::limited(
                        LimitKind::FormFields,
                        limits.form_fields,
                        admitted.len().saturating_add(1),
                    ));
                }
                budget.name(&field, &limits)?;
                budget.comparisons(admitted.len(), &limits)?;
                for previous in &admitted {
                    cancellation.check()?;
                    if previous.name == field.name {
                        return Err(refusal(Code::InvalidSelection, FormReason::DuplicateField));
                    }
                }
                budget.field_cancel(&field, &limits, Some(cancellation))
            })();
            result.map_err(|e| {
                RequestError::Preparation(field_context(e, admitted.len(), &field.name, None))
            })?;
            reserve_admitted(&mut admitted, limits.form_fields);
            admitted.push(field);
        }
        let mut ordinary = Vec::new();
        for field in &admitted {
            crate::schema::checkpoint(cancellation).map_err(RequestError::Preparation)?;
            field
                .arguments(cancellation, &mut |a| {
                    if let Argument::Ordinary(v) = a {
                        ordinary.push(*v)
                    }
                })
                .map_err(RequestError::Preparation)?;
        }
        let owner = if ordinary.is_empty() {
            None
        } else {
            Some(
                crate::ordinary::request_values(&ordinary, None, limits, cancellation)
                    .map_err(RequestError::Input)?,
            )
        };
        let mut ordinal = 0;
        let mut exact = |argument| match argument {
            Argument::Exact(v) => v,
            Argument::Ordinary(_) => {
                let value = owner
                    .as_ref()
                    .expect("ordinary owner")
                    .root()
                    .at(ordinal)
                    .expect("admitted ordinal");
                ordinal += 1;
                value
            }
        };
        let mut owned = Vec::with_capacity(admitted.len());
        for field in admitted {
            crate::schema::checkpoint(cancellation).map_err(RequestError::Preparation)?;
            owned.push(
                field
                    .lower(&mut exact, cancellation)
                    .map_err(RequestError::Preparation)?
                    .into_owned(cancellation)
                    .map_err(RequestError::Preparation)?,
            );
        }
        Ok(Self {
            fields: Arc::new(owned),
        })
    }
    /// Number of unique logical fields.
    pub fn len(&self) -> usize {
        self.fields.len()
    }
    /// Whether the form has no logical fields. This remains a present body.
    pub fn is_empty(&self) -> bool {
        self.fields.is_empty()
    }
    pub(crate) fn fields(&self) -> &[ReadyField<'static>] {
        &self.fields
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
        self.field_cancel(f, limits, None)
    }
    fn field_cancel(
        &mut self,
        f: &FormField<'_>,
        limits: &Limits,
        cancel: Option<&Cancellation>,
    ) -> Result<(), Diagnostic> {
        if let Some(cancel) = cancel {
            cancel.check()?;
        }
        match &f.value {
            FormValue::Value(v) => self.arg(v, limits),
            FormValue::Part(p) => self.part(p, limits, cancel),
            FormValue::Parts(parts) => {
                if parts.len() > limits.form_parts {
                    return Err(Diagnostic::limited(
                        LimitKind::FormParts,
                        limits.form_parts,
                        parts.len(),
                    ));
                }
                for p in *parts {
                    self.part(p, limits, cancel)?;
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
    fn part(
        &mut self,
        p: &Part<'_>,
        l: &Limits,
        cancel: Option<&Cancellation>,
    ) -> Result<(), Diagnostic> {
        if let Some(cancel) = cancel {
            cancel.check()?;
        }
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
                if let Some(cancel) = cancel {
                    cancel.check()?;
                }
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
    pub(crate) fn arguments(
        &self,
        cancel: &Cancellation,
        visit: &mut impl FnMut(&Argument<'a>),
    ) -> Result<(), Diagnostic> {
        let mut part = |p: &Part<'a>| -> Result<(), Diagnostic> {
            cancel.check()?;
            if let Content::Value(v) = &p.content {
                visit(v);
            }
            if let Some(headers) = p.headers {
                for (_, v) in headers {
                    cancel.check()?;
                    visit(v);
                }
            }
            Ok(())
        };
        match &self.value {
            FormValue::Value(v) => {
                cancel.check()?;
                visit(v);
            }
            FormValue::Part(p) => part(p)?,
            FormValue::Parts(ps) => {
                for p in *ps {
                    part(p)?;
                }
            }
        }
        Ok(())
    }
    pub(crate) fn lower(
        self,
        exact: &mut impl FnMut(Argument<'a>) -> Value,
        cancel: &Cancellation,
    ) -> Result<ReadyField<'a>, Diagnostic> {
        cancel.check()?;
        let content = match self.value {
            FormValue::Value(v) => ReadyValue::One(ReadyPart {
                explicit: false,
                payload: Payload::Value(exact(v)),
                filename: None,
                media: None,
                headers: Vec::new(),
            }),
            FormValue::Part(p) => ReadyValue::One(p.lower(exact, cancel)?),
            FormValue::Parts(ps) => {
                let mut parts = Vec::with_capacity(ps.len());
                for part in ps {
                    cancel.check()?;
                    parts.push(part.clone().lower(exact, cancel)?);
                }
                ReadyValue::Many(parts)
            }
        };
        cancel.check()?;
        Ok(ReadyField {
            name: self.name,
            content,
        })
    }
}
impl<'a> Part<'a> {
    fn lower(
        self,
        exact: &mut impl FnMut(Argument<'a>) -> Value,
        cancel: &Cancellation,
    ) -> Result<ReadyPart<'a>, Diagnostic> {
        cancel.check()?;
        let payload = match self.content {
            Content::Value(v) => Payload::Value(exact(v)),
            Content::Bytes(b) => Payload::Bytes(b.into()),
        };
        cancel.check()?;
        let authored_headers = self.headers.unwrap_or(&[]);
        let mut headers = Vec::with_capacity(authored_headers.len());
        for (name, value) in authored_headers {
            cancel.check()?;
            headers.push((Cow::Borrowed(*name), exact(value.clone())));
        }
        cancel.check()?;
        Ok(ReadyPart {
            explicit: true,
            payload,
            filename: self.filename,
            media: self.media,
            headers,
        })
    }
}
#[derive(Clone)]
enum AdmittedBytes<'a> {
    Borrowed(&'a [u8]),
    Shared(Arc<[u8]>),
    Owned(Arc<Vec<u8>>),
}
impl<'a> From<FiniteBytes<'a>> for AdmittedBytes<'a> {
    fn from(bytes: FiniteBytes<'a>) -> Self {
        match bytes {
            FiniteBytes::Borrowed(v) => Self::Borrowed(v),
            FiniteBytes::Shared(v) => Self::Shared(v),
        }
    }
}
impl AdmittedBytes<'_> {
    fn bytes(&self) -> &[u8] {
        match self {
            Self::Borrowed(v) => v,
            Self::Shared(v) => v,
            Self::Owned(v) => v,
        }
    }
}
#[derive(Clone)]
enum Payload<'a> {
    Value(Value),
    Bytes(AdmittedBytes<'a>),
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
    explicit: bool,
    payload: Payload<'a>,
    filename: Option<Cow<'a, str>>,
    media: Option<Cow<'a, str>>,
    headers: Vec<(Cow<'a, str>, Value)>,
}
// Convert only at the explicitly elected ownership boundary. Fluent lowering
// never calls these helpers. Metadata has already been admitted before copying.
fn owned_text(text: Cow<'_, str>, cancel: &Cancellation) -> Result<Cow<'static, str>, Diagnostic> {
    cancel.check()?;
    match text {
        Cow::Owned(text) => Ok(Cow::Owned(text)),
        Cow::Borrowed(text) => {
            let mut bytes = Vec::with_capacity(text.len());
            for chunk in text.as_bytes().chunks(8192) {
                crate::schema::checkpoint(cancel)?;
                bytes.extend_from_slice(chunk);
            }
            Ok(Cow::Owned(
                String::from_utf8(bytes).expect("original UTF-8"),
            ))
        }
    }
}
impl ReadyField<'_> {
    fn into_owned(self, cancel: &Cancellation) -> Result<ReadyField<'static>, Diagnostic> {
        Ok(ReadyField {
            name: owned_text(self.name, cancel)?,
            content: match self.content {
                ReadyValue::One(p) => ReadyValue::One(p.into_owned(cancel)?),
                ReadyValue::Many(parts) => {
                    let mut owned = Vec::with_capacity(parts.len());
                    for part in parts {
                        cancel.check()?;
                        owned.push(part.into_owned(cancel)?);
                    }
                    ReadyValue::Many(owned)
                }
            },
        })
    }
}
impl ReadyPart<'_> {
    fn into_owned(self, cancel: &Cancellation) -> Result<ReadyPart<'static>, Diagnostic> {
        cancel.check()?;
        let payload = match self.payload {
            Payload::Value(v) => Payload::Value(v),
            Payload::Bytes(AdmittedBytes::Shared(v)) => Payload::Bytes(AdmittedBytes::Shared(v)),
            Payload::Bytes(AdmittedBytes::Owned(v)) => Payload::Bytes(AdmittedBytes::Owned(v)),
            Payload::Bytes(AdmittedBytes::Borrowed(v)) => {
                let mut bytes = Vec::with_capacity(v.len());
                for chunk in v.chunks(8192) {
                    crate::schema::checkpoint(cancel)?;
                    bytes.extend_from_slice(chunk);
                }
                // Arc<Vec> shares the completed allocation without a second
                // data-sized Vec-to-Arc-slice relocation or unsafe initialization.
                Payload::Bytes(AdmittedBytes::Owned(Arc::new(bytes)))
            }
        };
        let mut headers = Vec::with_capacity(self.headers.len());
        for (name, value) in self.headers {
            headers.push((owned_text(name, cancel)?, value));
        }
        Ok(ReadyPart {
            explicit: self.explicit,
            payload,
            filename: self.filename.map(|v| owned_text(v, cancel)).transpose()?,
            media: self.media.map(|v| owned_text(v, cancel)).transpose()?,
            headers,
        })
    }
}
/// Reapply ordered logical use admission without copying payloads or reparsing exact values.
pub(crate) fn admit_ready(
    fields: &[ReadyField<'_>],
    limits: &Limits,
    cancel: &Cancellation,
) -> Result<(), Diagnostic> {
    let mut budget = Admission::default();
    for (index, field) in fields.iter().enumerate() {
        cancel.check()?;
        let result = (|| {
            if index >= limits.form_fields {
                return Err(Diagnostic::limited(
                    LimitKind::FormFields,
                    limits.form_fields,
                    index.saturating_add(1),
                ));
            }
            add(&mut budget.work, 1, limits.form_work, LimitKind::FormWork)?;
            add(
                &mut budget.metadata,
                field.name.len(),
                limits.form_metadata_bytes,
                LimitKind::FormMetadataBytes,
            )?;
            budget.comparisons(index, limits)?;
            match &field.content {
                ReadyValue::One(p) => budget.ready_part(p, limits, cancel),
                ReadyValue::Many(parts) => {
                    if parts.len() > limits.form_parts {
                        return Err(Diagnostic::limited(
                            LimitKind::FormParts,
                            limits.form_parts,
                            parts.len(),
                        ));
                    }
                    for part in parts {
                        budget.ready_part(part, limits, cancel)?;
                    }
                    Ok(())
                }
            }
        })();
        result.map_err(|e| field_context(e, index, &field.name, None))?;
    }
    Ok(())
}
impl Admission {
    fn ready_part(
        &mut self,
        part: &ReadyPart<'_>,
        limits: &Limits,
        cancel: &Cancellation,
    ) -> Result<(), Diagnostic> {
        cancel.check()?;
        // Plain values don't charge Part metadata/count, matching initial admission.
        if part.explicit {
            add(&mut self.work, 1, limits.form_work, LimitKind::FormWork)?;
            add(&mut self.parts, 1, limits.form_parts, LimitKind::FormParts)?;
        }
        for text in [&part.filename, &part.media].into_iter().flatten() {
            add(
                &mut self.metadata,
                text.len(),
                limits.form_metadata_bytes,
                LimitKind::FormMetadataBytes,
            )?;
        }
        add(
            &mut self.headers,
            part.headers.len(),
            limits.headers,
            LimitKind::HeaderCount,
        )?;
        for (name, value) in &part.headers {
            cancel.check()?;
            add(&mut self.work, 1, limits.form_work, LimitKind::FormWork)?;
            add(
                &mut self.metadata,
                name.len(),
                limits.form_metadata_bytes,
                LimitKind::FormMetadataBytes,
            )?;
            self.arg(&Argument::Exact(value.clone()), limits)?;
        }
        match &part.payload {
            Payload::Value(v) => self.arg(&Argument::Exact(v.clone()), limits),
            Payload::Bytes(b) => add(
                &mut self.bytes,
                b.bytes().len(),
                limits.body_bytes,
                LimitKind::BodyBytes,
            ),
        }
    }
}
#[derive(Clone)]
enum Piece<'a> {
    Scalar(Value),
    Json(Value),
    Bytes(AdmittedBytes<'a>),
    Separator(&'static [u8]),
    Delimiter(&'static [u8]),
    Key(Arc<str>),
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
            Self::Key(name) => name.as_bytes(),
            Self::Separator(v) | Self::Delimiter(v) => v,
        })
    }
}
struct Emission<'a> {
    name: String,
    filename: Option<String>,
    media: String,
    pieces: Vec<Piece<'a>>,
    style: Option<crate::style::Escape>,
}
struct Work<'a> {
    graph: &'a crate::graph::Graph,
    declarations: crate::prepare::DescriptorWork<'a>,
    limits: &'a Limits,
    cancel: &'a Cancellation,
    visits: usize,
    metadata: usize,
    headers: usize,
    key_data: usize,
    facts: FactWork<'a>,
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
    graph: &crate::graph::Graph,
    fields: &[ReadyField<'a>],
    media: &MediaDeclaration,
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
        FieldDeclarations::Declared(fs) => fs.as_slice(),
        _ => &[],
    };
    let mut work = Work {
        graph,
        declarations: crate::prepare::DescriptorWork::new(limits),
        limits,
        cancel,
        visits: 0,
        metadata: 0,
        headers: 0,
        key_data: 0,
        facts: FactWork::new(limits, cancel),
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
            if edition == Edition::Swagger20 && desc.is_none() {
                return Err(refusal(Code::InvalidSelection, FormReason::UnknownField));
            }
            let schema = desc.map(|d| d.schema.value.clone());
            let encoding = desc
                .and_then(|d| d.encoding.clone())
                .or_else(|| media.encoding.as_ref().and_then(|e| e.get(&field.name)));
            let styled = style(encoding.as_ref(), edition, multipart)?.is_some();
            let selection = if edition != Edition::Swagger20 && !styled {
                Some(crate::form_description::selected_property(
                    graph,
                    &crate::form_description::root_selection(media),
                    &field.name,
                    &mut work.facts,
                    &media.authored,
                )?)
            } else {
                None
            };
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
                        selection: selection.as_ref(),
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
                        && style(encoding.as_ref(), edition, multipart)?.is_some_and(|s| !s.explode)
                    {
                        return Err(located(
                            source,
                            Code::UnsupportedValue,
                            FormReason::ValueShape,
                        ));
                    }
                    let item = schema.as_ref().and_then(|s| s.get("items"));
                    let item_selection = selection
                        .as_ref()
                        .map(|s| {
                            crate::form_description::selected_items(
                                graph,
                                s,
                                &mut work.facts,
                                source,
                            )
                        })
                        .transpose()?;
                    for (i, part) in parts.iter().enumerate() {
                        plan_one(
                            &field.name,
                            part,
                            &part.payload,
                            Rules {
                                schema: item.as_ref(),
                                selection: item_selection.as_ref(),
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
pub(crate) fn style(
    encoding: Option<&Value>,
    edition: Edition,
    multipart: bool,
) -> Result<Option<crate::style::Config>, Diagnostic> {
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
    let declared = str_field(e, "style")?;
    let kind = match declared.as_ref().and_then(Value::as_str).unwrap_or("form") {
        "form" => crate::style::Style::Form,
        "deepObject" => crate::style::Style::DeepObject,
        "spaceDelimited" => crate::style::Style::SpaceDelimited,
        "pipeDelimited" => crate::style::Style::PipeDelimited,
        _ => {
            return Err(located(
                e,
                Code::UnsupportedMedia,
                FormReason::UnsupportedStyle,
            ));
        }
    };
    let explode = bool_field(e, "explode", kind == crate::style::Style::Form)?;
    Ok(Some(crate::style::Config {
        style: kind,
        explode: explode
            || kind == crate::style::Style::DeepObject && edition == Edition::OpenApi32,
        reserved: bool_field(e, "allowReserved", false)? && !multipart,
    }))
}
#[derive(Clone, Copy)]
struct Rules<'s> {
    schema: Option<&'s Value>,
    selection: Option<&'s SchemaSelection>,
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
        selection,
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
        if let Some(config) = styled
            && !(edition == Edition::OpenApi32 && value.kind() == ValueKind::Array)
        {
            return plan_style(name, part, value, config, rules, out, work);
        }
        if value.kind() == ValueKind::Array {
            let item = schema.and_then(|s| s.get("items"));
            let item_selection = if styled.is_none() {
                selection
                    .map(|s| {
                        crate::form_description::selected_items(
                            work.graph,
                            s,
                            &mut work.facts,
                            source,
                        )
                    })
                    .transpose()?
            } else {
                None
            };
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
                            selection: item_selection.as_ref(),
                            ..rules
                        },
                        out,
                        work,
                    )?;
                }
            }
            return Ok(());
        }
    }
    plan_one(name, part, &part.payload, rules, out, work)
}
fn plan_style<'a>(
    name: &str,
    part: &ReadyPart<'_>,
    value: &Value,
    config: crate::style::Config,
    rules: Rules<'_>,
    out: &mut Vec<Emission<'a>>,
    work: &mut Work<'_>,
) -> Result<(), Diagnostic> {
    use crate::style::{Atom, Escape, Event};
    let mode = Escape::Style {
        reserved: config.reserved,
        path: false,
        cookie: false,
        protected: config.protected(value),
    };
    let mut emitted_name = String::new();
    let mut pieces = Vec::new();
    crate::style::walk(value, name, config, true, rules.source, |event| {
        work.visit()?;
        match event {
            Event::Check => {}
            Event::Begin(name) => {
                add(
                    &mut work.metadata,
                    name.len(),
                    work.limits.form_metadata_bytes,
                    LimitKind::FormMetadataBytes,
                )?;
                emitted_name = String::with_capacity(name.len());
                for text in name.pieces() {
                    emitted_name.push_str(text);
                }
            }
            Event::Atom(atom) => {
                let piece = match atom {
                    Atom::Scalar(value) => Piece::Scalar(value.clone()),
                    Atom::Json(value) => Piece::Json(value.clone()),
                    Atom::Delimiter(bytes) => Piece::Delimiter(bytes),
                    Atom::Separator(bytes) => Piece::Separator(bytes),
                    Atom::Key(key) => {
                        add(
                            &mut work.key_data,
                            key.len(),
                            work.limits.body_bytes,
                            LimitKind::BodyBytes,
                        )?;
                        // Form's accepted styles emit Key only for a member of
                        // this object. Keep the logical key-byte admission above
                        // even though its immutable ownership can now be shared.
                        Piece::Key(value.member_name_owner(key).expect("form style object key"))
                    }
                };
                push_piece(&mut pieces, piece, work)?;
            }
            Event::End => {
                let media = work.text("text/plain")?;
                emit_named(
                    std::mem::take(&mut emitted_name),
                    part,
                    media,
                    std::mem::take(&mut pieces),
                    rules.multipart,
                    out,
                    work,
                )?;
                out.last_mut().unwrap().style = Some(mode);
            }
        }
        Ok(())
    })
}

fn push_piece<'a>(
    pieces: &mut Vec<Piece<'a>>,
    piece: Piece<'a>,
    work: &mut Work<'_>,
) -> Result<(), Diagnostic> {
    work.visit()?;
    // At most one stored piece per charged visit.
    // Expanded fields commonly need exactly one piece per emission.
    reserve_admitted_with_initial(pieces, work.limits.form_work, 1);
    pieces.push(piece);
    Ok(())
}

// Call only after admitting the next element. Geometric growth keeps aggregate
// allocations linear; the configured ceiling caps unused reserved slots. Never
// trust a caller iterator's size hint or reserve the entire configured maximum.
pub(crate) fn reserve_admitted<T>(values: &mut Vec<T>, maximum: usize) {
    reserve_admitted_with_initial(values, maximum, 4);
}
fn reserve_admitted_with_initial<T>(values: &mut Vec<T>, maximum: usize, initial: usize) {
    debug_assert!(values.len() < maximum);
    if values.len() == values.capacity() {
        let capacity = values
            .capacity()
            .saturating_mul(2)
            .max(initial)
            .min(maximum);
        values.reserve_exact(capacity.saturating_sub(values.len()));
    }
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
            let resolved =
                work.declarations
                    .resolve(work.graph, &header, crate::ReferenceKind::Header)?;
            let header = resolved.target;
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
        selection,
        encoding,
        source,
        edition,
        multipart,
    } = rules;
    work.visit()?;
    let styled = style(encoding, edition, multipart)?;
    validate_part(part, schema, encoding, source, edition, multipart, work)?;
    if let (Some(config), Payload::Value(value)) = (styled, payload) {
        if part.media.is_some() {
            return Err(located(
                source,
                Code::InvalidSelection,
                FormReason::PartMediaMismatch,
            ));
        }
        return plan_style(name, part, value, config, rules, out, work);
    }
    if styled.is_none()
        && let Some(selection) = selection
    {
        let (coding, sites) = crate::form_description::effective_coding(
            work.graph,
            selection,
            &mut work.facts,
            source,
        )?;
        if coding.is_some() {
            let site = sites
                .first()
                .map(|n| work.graph.value(*n))
                .unwrap_or_else(|| source.clone());
            return Err(located(
                &site,
                Code::UnsupportedMedia,
                FormReason::UnsupportedCoding,
            ));
        }
    }
    let content_type = if styled.is_some() {
        work.text("text/plain")?
    } else {
        choose_part_media(
            part.media.as_deref(),
            schema,
            selection,
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
    if styled.is_some_and(|config| config.style != crate::style::Style::Form) {
        return Err(source
            .error(Code::UnsupportedValue)
            .reasoned(DiagnosticReason::UnsupportedStyleCombination));
    }
    emit(name, part, content_type, vec![piece], multipart, out, work)?;
    if let Some(config) = styled {
        out.last_mut().unwrap().style = Some(crate::style::Escape::Style {
            reserved: config.reserved,
            path: false,
            cookie: false,
            protected: crate::style::Protection::None,
        });
    }
    Ok(())
}
fn choose_part_media(
    elected: Option<&str>,
    schema: Option<&Value>,
    selection: Option<&SchemaSelection>,
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
    } else if let Some(selection) = selection {
        default =
            crate::form_description::default_media(work.graph, selection, &mut work.facts, source)?
                .0;
        default
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
    let name = work.text(name)?;
    emit_named(name, part, media, pieces, multipart, out, work)
}
fn emit_named<'a>(
    name: String,
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
    reserve_admitted(out, work.limits.form_parts);
    out.push(Emission {
        name,
        filename: part.filename.as_deref().map(str::to_owned),
        media,
        pieces,
        style: None,
    });
    Ok(())
}

// Count, collision scan and final write run the same bounded byte traversal.
// No fully encoded per-part buffers or encoded URL strings are allocated.
enum Sink<'a> {
    Count,
    Scan {
        needle: &'a [u8],
        fallback: &'a [usize],
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
                    fallback,
                    matched,
                    found,
                } => {
                    for &b in chunk {
                        while *matched > 0 && b != needle[*matched] {
                            *matched = fallback[*matched - 1];
                        }
                        if b == needle[*matched] {
                            *matched += 1;
                            if *matched == needle.len() {
                                *found = true;
                                *matched = fallback[*matched - 1];
                            }
                        }
                    }
                }
            }
        }
        Ok(())
    }
    fn encoded(&mut self, bytes: &[u8], mode: crate::style::Escape) -> Result<(), Diagnostic> {
        crate::style::encode(bytes, mode, |chunk| self.put(chunk))
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
            writer.encoded(
                part.name.as_bytes(),
                part.style
                    .map_or(crate::style::Escape::FormContent, |mode| mode.name()),
            )?;
            writer.put(b"=")?;
            for piece in &part.pieces {
                let mode = match (part.style, piece) {
                    (Some(_), Piece::Separator(_)) => crate::style::Escape::Literal,
                    (Some(mode), Piece::Key(_)) => mode.name(),
                    (Some(mode), Piece::Delimiter(bytes)) => mode.delimiter(bytes),
                    (Some(mode), _) => mode,
                    (None, _) => crate::style::Escape::FormContent,
                };
                writer.encoded(piece.bytes()?, mode)?;
            }
        }
    }
    Ok(())
}
// The generated boundary is nonempty and at most 70 bytes. A fixed-size KMP
// table preserves overlapping matches across every Writer call and chunk without
// a payload-sized buffer or rescanning an arbitrarily long prefix.
fn boundary_fallback(needle: &[u8]) -> [usize; 70] {
    assert!(!needle.is_empty() && needle.len() <= 70);
    let mut fallback = [0; 70];
    let mut matched = 0;
    for i in 1..needle.len() {
        while matched > 0 && needle[i] != needle[matched] {
            matched = fallback[matched - 1];
        }
        if needle[i] == needle[matched] {
            matched += 1;
        }
        fallback[i] = matched;
    }
    fallback
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
            let candidate = format!("openapi-boundary-{attempt:016x}");
            let fallback = boundary_fallback(candidate.as_bytes());
            let mut scan = Writer {
                sink: Sink::Scan {
                    needle: candidate.as_bytes(),
                    fallback: &fallback,
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
    // The immutable output needs only the emitted bytes. Release the encoding
    // plan before converting the Vec into an Arc (which temporarily owns both
    // byte buffers), rather than keeping every part's metadata alive at that peak.
    drop(parts);
    let media = match boundary {
        Some(b) => format!("{selected}; boundary={b}"),
        None => selected.into(),
    };
    Ok((Arc::from(bytes), media))
}

/// Use the same style applicability and default-media/coding facts as preparation.
/// No payload is inspected here and no codec support is asserted.
#[allow(clippy::too_many_arguments)]
pub(crate) fn describe_representation(
    graph: &crate::graph::Graph,
    selection: &SchemaSelection,
    legacy: Option<&Value>,
    encoding: Option<&Value>,
    source: &Value,
    edition: Edition,
    multipart: bool,
    work: &mut crate::prepare::DescriptorWork<'_>,
    facts: &mut FactWork<'_>,
) -> Result<crate::FormRepresentationDescription, Diagnostic> {
    if style(encoding, edition, multipart)?.is_some() {
        return Ok(crate::FormRepresentationDescription {
            media_types: vec![work.text(source, "text/plain")?],
            content_encoding: None,
            sources: Vec::new(),
        });
    }
    let mut sources = Vec::new();
    let mut record = |sites: Vec<crate::graph::NodeKey>,
                      work: &mut crate::prepare::DescriptorWork<'_>|
     -> Result<(), Diagnostic> {
        for node in sites {
            let value = graph.value(node);
            work.entry(&value)?;
            sources.push(value.diagnostic_position());
        }
        Ok(())
    };
    let coding = if edition == Edition::Swagger20 {
        None
    } else {
        let (coding, sites) =
            crate::form_description::effective_coding(graph, selection, facts, source)?;
        record(sites, work)?;
        coding.map(|s| work.text(source, &s)).transpose()?
    };
    let declared = encoding.and_then(|e| e.get("contentType"));
    let default;
    let text = if let Some(declared) = &declared {
        declared
            .as_str()
            .ok_or_else(|| declared.error(Code::InvalidDeclaration))?
    } else if edition == Edition::Swagger20 {
        let schema = legacy.ok_or_else(|| source.error(Code::InvalidDeclaration))?;
        default = match schema
            .get("type")
            .and_then(|v| v.as_str().map(str::to_owned))
            .as_deref()
        {
            Some("file") => "application/octet-stream",
            Some("string" | "number" | "integer" | "boolean") => "text/plain",
            Some("array") => "text/plain",
            _ => return Err(schema.error(Code::UnsupportedMedia)),
        };
        default
    } else {
        let (media, sites) =
            crate::form_description::default_media(graph, selection, facts, source)?;
        record(sites, work)?;
        default = media;
        default
    };
    // Admit the complete declaration before scanning, parsing or copying entries.
    let text = work.text(declared.as_ref().unwrap_or(source), text)?;
    let mut entries = Vec::new();
    let mut start = 0;
    let mut quoted = false;
    let mut escaped = false;
    for (index, ch) in text.char_indices() {
        if index % 8192 == 0 {
            work.declaration(source)?;
        }
        if escaped {
            escaped = false;
            continue;
        }
        if quoted && ch == '\\' {
            escaped = true;
            continue;
        }
        if ch == '"' {
            quoted = !quoted
        }
        if ch == ',' && !quoted {
            work.declaration(source)?;
            let entry = &text[start..index];
            crate::prepare::parse_media(entry, true)?;
            entries.push(work.text(source, entry.trim())?);
            start = index + 1;
        }
    }
    if quoted || escaped {
        return Err(source.error(Code::InvalidMedia));
    }
    work.declaration(source)?;
    let entry = &text[start..];
    crate::prepare::parse_media(entry, true)?;
    entries.push(work.text(source, entry.trim())?);
    Ok(crate::FormRepresentationDescription {
        media_types: entries,
        content_encoding: coding,
        sources,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn shared_lowering_cancels_before_entries_and_releases_partial_owners() {
        let value = crate::ExactJson::parse(br#""x""#, Limits::default())
            .unwrap()
            .root();
        let cancel = Cancellation::default();
        cancel.cancel();
        let mut calls = 0;
        let result = FormField::new("p", value.clone()).lower(
            &mut |argument| {
                calls += 1;
                match argument {
                    Argument::Exact(value) => value,
                    _ => unreachable!(),
                }
            },
            &cancel,
        );
        assert!(matches!(result, Err(e) if e.code() == Code::Cancelled));
        assert_eq!(calls, 0);

        let bytes: Arc<[u8]> = Arc::from([0, 255]);
        let parts = [
            Part::bytes(bytes.clone()),
            Part::value(value.clone()),
            Part::bytes(bytes.clone()),
        ];
        let owners = Arc::strong_count(&bytes);
        let cancel = Cancellation::default();
        let result = FormField::new("p", FormValue::Parts(&parts)).lower(
            &mut |argument| {
                calls += 1;
                cancel.cancel();
                match argument {
                    Argument::Exact(value) => value,
                    _ => unreachable!(),
                }
            },
            &cancel,
        );
        assert!(matches!(result, Err(e) if e.code() == Code::Cancelled));
        assert_eq!(calls, 1);
        assert_eq!(Arc::strong_count(&bytes), owners);

        let headers = [
            ("x-one", Argument::Exact(value.clone())),
            ("x-two", Argument::Exact(value.clone())),
            ("x-three", Argument::Exact(value.clone())),
        ];
        let cancel = Cancellation::default();
        calls = 0;
        let result = FormField::new("p", Part::value(value).headers(&headers)).lower(
            &mut |argument| {
                calls += 1;
                if calls == 2 {
                    cancel.cancel();
                }
                match argument {
                    Argument::Exact(value) => value,
                    _ => unreachable!(),
                }
            },
            &cancel,
        );
        assert!(matches!(result, Err(e) if e.code() == Code::Cancelled));
        assert_eq!(calls, 2);
    }
    #[test]
    fn piece_growth_admits_before_reserving_and_releases_payload_owners() {
        let document = crate::Document::parse(
            br#"{"openapi":"3.2.1","paths":{}}"#,
            None,
            Limits::default(),
        )
        .unwrap();
        for maximum in [65, 0, 1, 2, 3, 4, 5, 8, 9] {
            let limits = Limits {
                form_work: maximum,
                ..Limits::default()
            };
            let cancel = Cancellation::default();
            let mut work = Work {
                graph: &document.0.graph,
                declarations: crate::prepare::DescriptorWork::new(&limits),
                limits: &limits,
                cancel: &cancel,
                visits: 0,
                metadata: 0,
                headers: 0,
                key_data: 0,
                facts: FactWork::new(&limits, &cancel),
            };
            let bytes: Arc<[u8]> = Arc::from(b"payload".as_slice());
            let owner = Arc::downgrade(&bytes);
            let mut pieces = Vec::new();
            let mut reserved = 0;
            for _ in 0..maximum {
                let previous = pieces.capacity();
                push_piece(
                    &mut pieces,
                    Piece::Bytes(AdmittedBytes::Shared(bytes.clone())),
                    &mut work,
                )
                .unwrap();
                if pieces.capacity() != previous {
                    reserved += pieces.capacity();
                }
                assert!(pieces.capacity() <= maximum);
                if pieces.len() == 1 {
                    eprintln!(
                        "first piece: size={} capacity={}",
                        std::mem::size_of::<Piece<'_>>(),
                        pieces.capacity()
                    );
                    assert_eq!(pieces.capacity(), 1);
                }
                assert!(pieces.capacity() <= pieces.len().next_power_of_two());
            }
            // Cumulative requested slots remain linear across all growth steps.
            assert!(reserved <= 4 * maximum);
            let before = (pieces.len(), pieces.capacity(), Arc::strong_count(&bytes));
            let error = push_piece(
                &mut pieces,
                Piece::Bytes(AdmittedBytes::Shared(bytes.clone())),
                &mut work,
            )
            .unwrap_err();
            assert!(matches!(
                error.reason(),
                Some(DiagnosticReason::Limit {
                    kind: LimitKind::FormWork,
                    ..
                })
            ));
            assert_eq!(
                before,
                (pieces.len(), pieces.capacity(), Arc::strong_count(&bytes))
            );
            // Cancellation likewise cannot reserve or retain the rejected piece.
            work.visits = 0;
            cancel.cancel();
            assert_eq!(
                push_piece(
                    &mut pieces,
                    Piece::Bytes(AdmittedBytes::Shared(bytes.clone())),
                    &mut work
                )
                .unwrap_err()
                .code(),
                Code::Cancelled
            );
            assert_eq!(
                before,
                (pieces.len(), pieces.capacity(), Arc::strong_count(&bytes))
            );
            drop(bytes);
            assert_eq!(owner.upgrade().is_some(), maximum > 0);
            drop(pieces);
            assert!(owner.upgrade().is_none());
        }
        // Other admitted vectors retain their existing initial growth policy.
        let mut emissions: Vec<Emission<'_>> = Vec::new();
        reserve_admitted(&mut emissions, 65);
        assert_eq!(emissions.capacity(), 4);
    }

    fn collision_body(value: Value, attempts: usize) -> Result<(Arc<[u8]>, String), Diagnostic> {
        let limits = Limits {
            boundary_attempts: attempts,
            ..Limits::default()
        };
        let cancel = Cancellation::default();
        let document = crate::Document::parse(
            br#"{"openapi":"3.2.1","paths":{}}"#,
            None,
            Limits::default(),
        )
        .unwrap();
        let mut work = Work {
            graph: &document.0.graph,
            declarations: crate::prepare::DescriptorWork::new(&limits),
            limits: &limits,
            cancel: &cancel,
            visits: 0,
            metadata: 0,
            headers: 0,
            key_data: 0,
            facts: FactWork::new(&limits, &cancel),
        };
        finish(
            vec![Emission {
                name: "x".into(),
                filename: None,
                media: "text/plain".into(),
                pieces: vec![Piece::Scalar(value)],
                style: None,
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
        let fallback = boundary_fallback(needle);
        let cancellation = Cancellation::default();
        let mut work = 0;
        let mut scanner = Writer {
            sink: Sink::Scan {
                needle,
                fallback: &fallback,
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

    fn scans(needle: &[u8], bytes: &[u8], split: usize) -> bool {
        let fallback = boundary_fallback(needle);
        let cancellation = Cancellation::default();
        let mut work = 0;
        let mut scanner = Writer {
            sink: Sink::Scan {
                needle,
                fallback: &fallback,
                matched: 0,
                found: false,
            },
            count: 0,
            maximum: usize::MAX,
            cancel: &cancellation,
            work: &mut work,
            maximum_work: usize::MAX,
        };
        scanner.put(&bytes[..split]).unwrap();
        scanner.put(&bytes[split..]).unwrap();
        matches!(scanner.sink, Sink::Scan { found: true, .. })
    }

    #[test]
    fn boundary_overlap_is_rejected_before_emission() {
        let value = crate::ExactJson::parse(
            br#""openapi-bopenapi-boundary-0000000000000000""#,
            Limits::default(),
        )
        .unwrap();
        let (_, media) = collision_body(value.root(), 2).unwrap();
        assert!(media.ends_with("openapi-boundary-0000000000000001"));
        assert!(matches!(
            collision_body(value.root(), 1).unwrap_err().reason(),
            Some(DiagnosticReason::Limit {
                kind: LimitKind::BoundaryAttempts,
                ..
            })
        ));

        let needle = b"openapi-boundary-0000000000000000";
        // Move the overlap and full occurrence across the writer's 4096-byte
        // boundary, and split every byte around both prefixes between calls.
        for padding in 4080..4100 {
            let mut bytes = vec![b'x'; padding];
            bytes.extend_from_slice(b"openapi-b");
            bytes.extend_from_slice(needle);
            for split in padding..=bytes.len() {
                assert!(scans(needle, &bytes, split));
            }
        }
    }

    #[test]
    fn scanner_agrees_with_independent_windows_oracle_for_overlapping_patterns() {
        fn word(length: usize, bits: usize) -> Vec<u8> {
            (0..length)
                .map(|i| if bits & (1 << i) == 0 { b'a' } else { b'b' })
                .collect()
        }
        for needle_length in 1..=5 {
            for needle_bits in 0..1 << needle_length {
                let needle = word(needle_length, needle_bits);
                for length in 0..=7 {
                    for bits in 0..1 << length {
                        let bytes = word(length, bits);
                        let expected = bytes.windows(needle.len()).any(|w| w == needle);
                        for split in 0..=bytes.len() {
                            assert_eq!(scans(&needle, &bytes, split), expected);
                        }
                    }
                }
            }
        }
    }
}
