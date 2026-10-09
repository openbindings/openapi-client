use crate::{
    Body, Cancellation, Code, Credential, CredentialValue, Diagnostic, DiagnosticContext,
    DiagnosticReason, Header, Input, LimitKind, Limits, Operation, OrdinaryValue, ParameterInput,
    ParameterLocation, ParseFailure, PreparedRequest, Selection, SelectionKind, Value,
};
use std::{borrow::Cow, fmt};

/// A checked ordinary draft or an already admitted exact value.
/// Ordinary strings/containers may be borrowed until prepare consumes the builder.
/// No floating-point or arbitrary Serialize conversion occurs.
#[derive(Clone, Debug)]
pub enum Argument<'a> {
    /// The existing bounded ordinary JSON profile.
    Ordinary(OrdinaryValue<'a>),
    /// An exact value retaining its existing source owner and spelling.
    Exact(Value),
}
impl<'a> From<OrdinaryValue<'a>> for Argument<'a> {
    fn from(value: OrdinaryValue<'a>) -> Self {
        Self::Ordinary(value)
    }
}
impl From<Value> for Argument<'_> {
    fn from(value: Value) -> Self {
        Self::Exact(value)
    }
}
impl<'a> From<&'a str> for Argument<'a> {
    fn from(value: &'a str) -> Self {
        Self::Ordinary(OrdinaryValue::String(value))
    }
}
impl<'a> From<&'a String> for Argument<'a> {
    fn from(value: &'a String) -> Self {
        value.as_str().into()
    }
}
impl From<bool> for Argument<'_> {
    fn from(value: bool) -> Self {
        Self::Ordinary(OrdinaryValue::Bool(value))
    }
}
macro_rules! integers {
    ($variant:ident, $wide:ty; $($integer:ty),*) => {$(
        impl From<$integer> for Argument<'_> {
            fn from(value: $integer) -> Self {
                Self::Ordinary(OrdinaryValue::$variant(value as $wide))
            }
        }
    )*};
}
integers!(I64, i64; i8, i16, i32, i64, isize);
integers!(U64, u64; u8, u16, u32, u64, usize);

/// Input-owner construction and request preparation remain distinct failure phases.
#[derive(Clone, Debug)]
pub enum RequestError {
    /// Bounded generated JSON admission failed, retaining source when complete.
    Input(ParseFailure),
    /// Builder assignment/admission or protocol preparation refused.
    Preparation(Diagnostic),
}
impl RequestError {
    /// Structured corrective facts without parsing error text.
    pub fn diagnostic(&self) -> &Diagnostic {
        match self {
            Self::Input(e) => e.diagnostic(),
            Self::Preparation(e) => e,
        }
    }
}
impl fmt::Display for RequestError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        self.diagnostic().fmt(f)
    }
}
impl std::error::Error for RequestError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        Some(match self {
            Self::Input(e) => e,
            Self::Preparation(e) => e,
        })
    }
}

struct ParameterDraft<'a> {
    location: ParameterLocation,
    name: Cow<'a, str>,
    value: Argument<'a>,
    event: usize,
}
enum BodyDraft<'a> {
    Explicit(Body),
    Json(Argument<'a>),
}
struct Variable<'a> {
    key: Cow<'a, str>,
    value: Cow<'a, str>,
    event: usize,
}
struct CredentialDraft<'a> {
    scheme: Cow<'a, str>,
    value: Credential,
    event: usize,
}

/// Per-call fluent construction, lowering to the same Input/prepare primitive.
///
/// Setters check library-owned collection/copy quotas and latch the first refusal.
/// After refusal they retain no later arguments. Ordinary JSON is serialized once
/// into one generated owner when prepare consumes this builder. Exact backing
/// owners and caller-provided allocations are accounted separately, not limited
/// as if the builder allocated them. Caller-defined Into code is caller work.
/// Duplicate explicit assignments refuse; defaults never clear previous fields.
/// No callback is invoked by construction or preparation.
///
/// ```compile_fail
/// use dynamic_openapi_client::{Document, Limits, ParameterLocation};
/// let doc = Document::parse(b"{}", None, Limits::default()).unwrap();
/// let op = doc.operation_id("x").unwrap();
/// let _ = op.request().parameter(ParameterLocation::Query, "x", 1.5_f64);
/// ```
pub struct RequestBuilder<'a> {
    operation: Operation,
    parameters: Vec<ParameterDraft<'a>>,
    body: Option<(BodyDraft<'a>, usize)>,
    server: Option<(usize, usize)>,
    media: Option<(Cow<'a, str>, usize)>,
    security: Option<(usize, usize)>,
    variables: Vec<Variable<'a>>,
    credentials: Vec<CredentialDraft<'a>>,
    headers: Vec<Header>,
    next_event: usize,
    input_bytes: usize,
    failure: Option<Diagnostic>,
}
impl fmt::Debug for RequestBuilder<'_> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("RequestBuilder")
            .field("assignments", &self.next_event)
            .field("failure", &self.failure)
            .finish_non_exhaustive()
    }
}
impl Operation {
    /// Begin one ordinary call using this operation's limits. The builder owns an
    /// Operation clone and may borrow ordinary caller data until prepare consumes it.
    pub fn request<'a>(&self) -> RequestBuilder<'a> {
        RequestBuilder {
            operation: self.clone(),
            parameters: vec![],
            body: None,
            server: None,
            media: None,
            security: None,
            variables: vec![],
            credentials: vec![],
            headers: vec![],
            next_event: 0,
            input_bytes: 0,
            failure: None,
        }
    }
}
impl<'a> RequestBuilder<'a> {
    fn limits(&self) -> &Limits {
        &self.operation.store.limits
    }
    fn event(&mut self) -> usize {
        let event = self.next_event;
        self.next_event += 1;
        event
    }
    fn latch(&mut self, error: Diagnostic) {
        if self.failure.is_none() {
            self.failure = Some(error);
            self.parameters = vec![];
            self.variables = vec![];
            self.credentials = vec![];
            self.headers = vec![];
            self.body = None;
            self.media = None;
        }
    }
    fn count(&mut self, actual: usize, header: bool) -> bool {
        let (kind, maximum) = if header {
            (LimitKind::HeaderCount, self.limits().headers)
        } else {
            (LimitKind::InputCount, self.limits().reference_steps)
        };
        if actual > maximum {
            self.latch(
                Diagnostic::limited(kind, maximum, actual)
                    .contextual(DiagnosticContext::CallerInput),
            );
            false
        } else {
            true
        }
    }
    fn bytes(&mut self, n: usize) -> bool {
        let actual = self.input_bytes.saturating_add(n);
        if actual > self.limits().document_bytes {
            self.latch(
                Diagnostic::limited(LimitKind::InputBytes, self.limits().document_bytes, actual)
                    .contextual(DiagnosticContext::CallerInput),
            );
            false
        } else {
            self.input_bytes = actual;
            true
        }
    }
    fn body_bytes(&mut self, n: usize) -> bool {
        if n > self.limits().body_bytes {
            self.latch(
                Diagnostic::limited(LimitKind::BodyBytes, self.limits().body_bytes, n)
                    .contextual(DiagnosticContext::Body),
            );
            false
        } else {
            true
        }
    }
    fn duplicate(&self, first: usize, repeated: usize) -> Diagnostic {
        Diagnostic::new(Code::InvalidSelection).reasoned(DiagnosticReason::DuplicateAssignment {
            first_assignment: first,
            repeated_assignment: repeated,
        })
    }
    // Complete context strings are capped before copying. No fake/truncated location.
    fn context(
        &self,
        error: Diagnostic,
        name: &str,
        make: impl FnOnce(String) -> DiagnosticContext,
    ) -> Diagnostic {
        if name.len() > self.limits().document_bytes.min(4096) {
            error.omit_context()
        } else {
            error.contextual(make(name.to_owned()))
        }
    }
    /// Assign a dynamic parameter; header identities compare case-insensitively.
    pub fn parameter(
        mut self,
        location: ParameterLocation,
        name: impl Into<Cow<'a, str>>,
        value: impl Into<Argument<'a>>,
    ) -> Self {
        if self.failure.is_some() {
            return self;
        }
        let event = self.event();
        let name = name.into();
        if let Some(p) = self
            .parameters
            .iter()
            .find(|p| p.location == location && crate::prepare::same_name(&p.name, &name, location))
        {
            let error = self.context(self.duplicate(p.event, event), &name, |name| {
                DiagnosticContext::Parameter {
                    index: self.parameters.len(),
                    location,
                    name,
                }
            });
            self.latch(error);
            return self;
        }
        if !self.count(self.parameters.len().saturating_add(1), false) || !self.bytes(name.len()) {
            return self;
        }
        let value = value.into();
        let minimum = match &value {
            Argument::Exact(v) => v.raw().len(),
            Argument::Ordinary(v) => minimum_bytes(*v),
        };
        if !self.bytes(minimum) {
            return self;
        }
        self.parameters.reserve_exact(1);
        self.parameters.push(ParameterDraft {
            location,
            name,
            value,
            event,
        });
        self
    }
    /// Assign an ordinary or exact JSON body; explicit absent body is also an assignment.
    pub fn json(mut self, value: impl Into<Argument<'a>>) -> Self {
        if self.failure.is_some() {
            return self;
        }
        let event = self.event();
        if let Some((_, first)) = &self.body {
            self.latch(
                self.duplicate(*first, event)
                    .contextual(DiagnosticContext::Body),
            );
            return self;
        }
        let value = value.into();
        let minimum = match &value {
            Argument::Exact(v) => v.raw().len(),
            Argument::Ordinary(v) => minimum_bytes(*v),
        };
        if self.body_bytes(minimum) {
            self.body = Some((BodyDraft::Json(value), event));
        }
        self
    }
    /// Assign absent, exact JSON or finite raw body without implicit content-type policy.
    pub fn body(mut self, body: Body) -> Self {
        if self.failure.is_some() {
            return self;
        }
        let event = self.event();
        if let Some((_, first)) = &self.body {
            self.latch(
                self.duplicate(*first, event)
                    .contextual(DiagnosticContext::Body),
            );
            return self;
        }
        let bytes = match &body {
            Body::Absent => 0,
            Body::Json(v) => v.raw().len(),
            Body::Raw(v) => v.len(),
        };
        if self.body_bytes(bytes) {
            self.body = Some((BodyDraft::Explicit(body), event));
        }
        self
    }
    /// Add populated selection fields in server, media, security, lexical-variable order.
    /// None and absent map entries assign nothing. Equal repeats still refuse.
    pub fn selection(mut self, selection: Selection) -> Self {
        if self.failure.is_some() {
            return self;
        }
        if let Some(value) = selection.server {
            self = self.scalar_selection(value, SelectionKind::Server);
        }
        if self.failure.is_some() {
            return self;
        }
        if let Some(value) = selection.media {
            let event = self.event();
            if let Some((_, first)) = &self.media {
                self.latch(self.duplicate(*first, event).contextual(
                    DiagnosticContext::Selection {
                        kind: SelectionKind::Media,
                        index: None,
                        key: None,
                    },
                ));
            } else if self.bytes(value.len()) {
                self.media = Some((Cow::Owned(value), event));
            }
        }
        if self.failure.is_some() {
            return self;
        }
        if let Some(value) = selection.security {
            self = self.scalar_selection(value, SelectionKind::Security);
        }
        for (key, value) in selection.variables {
            if self.failure.is_some() {
                break;
            }
            self = self.server_variable(key, value);
        }
        self
    }
    fn scalar_selection(mut self, value: usize, kind: SelectionKind) -> Self {
        let event = self.event();
        let existing = match kind {
            SelectionKind::Server => self.server,
            SelectionKind::Security => self.security,
            _ => unreachable!(),
        };
        if let Some((_, first)) = existing {
            self.latch(
                self.duplicate(first, event)
                    .contextual(DiagnosticContext::Selection {
                        kind,
                        index: Some(value),
                        key: None,
                    }),
            );
        } else {
            match kind {
                SelectionKind::Server => self.server = Some((value, event)),
                SelectionKind::Security => self.security = Some((value, event)),
                _ => unreachable!(),
            }
        }
        self
    }
    /// Add one variable assignment; it composes with other populated selection fields.
    pub fn server_variable(
        mut self,
        name: impl Into<Cow<'a, str>>,
        value: impl Into<Cow<'a, str>>,
    ) -> Self {
        if self.failure.is_some() {
            return self;
        }
        let event = self.event();
        let name = name.into();
        if let Some(v) = self.variables.iter().find(|v| v.key == name) {
            let error = self.context(self.duplicate(v.event, event), &name, |key| {
                DiagnosticContext::Selection {
                    kind: SelectionKind::ServerVariable,
                    index: None,
                    key: Some(key),
                }
            });
            self.latch(error);
            return self;
        }
        if !self.count(self.variables.len().saturating_add(1), false) || !self.bytes(name.len()) {
            return self;
        }
        let value = value.into();
        if !self.bytes(value.len()) {
            return self;
        }
        self.variables.reserve_exact(1);
        self.variables.push(Variable {
            key: name,
            value,
            event,
        });
        self
    }
    /// Supply per-call credential material with explicit origin admission. No acquisition occurs.
    pub fn credential(mut self, scheme: impl Into<Cow<'a, str>>, credential: Credential) -> Self {
        if self.failure.is_some() {
            return self;
        }
        let event = self.event();
        let scheme = scheme.into();
        if let Some(c) = self.credentials.iter().find(|c| c.scheme == scheme) {
            let error = self.context(self.duplicate(c.event, event), &scheme, |scheme| {
                DiagnosticContext::Credential { scheme }
            });
            self.latch(error);
            return self;
        }
        if !self.count(self.credentials.len().saturating_add(1), false) || !self.bytes(scheme.len())
        {
            return self;
        }
        let bytes = match &credential.value {
            CredentialValue::Basic { username, password } => {
                username.len().saturating_add(password.len())
            }
            CredentialValue::Bearer(v) | CredentialValue::ApiKey(v) => v.len(),
        };
        if !self.bytes(bytes) {
            return self;
        }
        for origin in &credential.origins {
            if !self.bytes(origin.len()) {
                return self;
            }
        }
        self.credentials.reserve_exact(1);
        self.credentials.push(CredentialDraft {
            scheme,
            value: credential,
            event,
        });
        self
    }
    /// Append an ordered header line under the existing multivalue/header policy.
    pub fn header(mut self, header: Header) -> Self {
        if self.failure.is_some() {
            return self;
        }
        self.event();
        if self.count(self.headers.len().saturating_add(1), true)
            && self.bytes(header.name().len().saturating_add(header.value().len()))
        {
            self.headers.reserve_exact(1);
            self.headers.push(header);
        }
        self
    }
    /// Consume drafts into one bounded generated owner and the existing prepared request.
    pub fn prepare(self) -> Result<PreparedRequest, RequestError> {
        self.prepare_with_cancellation(&Cancellation::default())
    }
    /// Observe a latched refusal first, otherwise cancellation before serializing ordinary data.
    pub fn prepare_with_cancellation(
        self,
        cancellation: &Cancellation,
    ) -> Result<PreparedRequest, RequestError> {
        if let Some(error) = self.failure {
            return Err(RequestError::Preparation(error));
        }
        cancellation.check().map_err(RequestError::Preparation)?;
        let mut ordinary = Vec::new();
        for parameter in &self.parameters {
            if let Argument::Ordinary(value) = &parameter.value {
                ordinary.push(*value);
            }
        }
        let body_index = if let Some((BodyDraft::Json(Argument::Ordinary(value)), _)) = &self.body {
            let index = ordinary.len();
            ordinary.push(*value);
            Some(index)
        } else {
            None
        };
        let owner = if ordinary.is_empty() {
            None
        } else {
            Some(
                crate::ordinary::request_values(
                    &ordinary,
                    body_index,
                    self.limits().clone(),
                    cancellation,
                )
                .map_err(RequestError::Input)?,
            )
        };
        let mut cursor = 0;
        let mut exact = |argument: Argument<'a>| match argument {
            Argument::Exact(value) => value,
            Argument::Ordinary(_) => {
                let value = owner
                    .as_ref()
                    .expect("ordinary owner")
                    .root()
                    .at(cursor)
                    .expect("admitted ordinal");
                cursor += 1;
                value
            }
        };
        let mut input = Input::default();
        for parameter in self.parameters {
            input.parameters.push(ParameterInput {
                location: parameter.location,
                name: parameter.name.into_owned(),
                value: exact(parameter.value),
            });
        }
        input.body = match self.body {
            None => Body::Absent,
            Some((BodyDraft::Explicit(body), _)) => body,
            Some((BodyDraft::Json(argument), _)) => Body::Json(exact(argument)),
        };
        input.selection.server = self.server.map(|v| v.0);
        input.selection.security = self.security.map(|v| v.0);
        input.selection.media = self.media.map(|v| v.0.into_owned());
        for variable in self.variables {
            input
                .selection
                .variables
                .insert(variable.key.into_owned(), variable.value.into_owned());
        }
        for credential in self.credentials {
            input
                .credentials
                .insert(credential.scheme.into_owned(), credential.value);
        }
        input.headers = self.headers;
        self.operation
            .prepare_with_cancellation(&input, cancellation)
            .map_err(RequestError::Preparation)
    }
}
// Constant-time lower bounds only. Borrowed nested data is fully admitted once
// at prepare; it is not copied or serialized by setters.
fn minimum_bytes(value: OrdinaryValue<'_>) -> usize {
    match value {
        OrdinaryValue::String(v) => v.len().saturating_add(2),
        OrdinaryValue::Null => 4,
        OrdinaryValue::Bool(true) => 4,
        OrdinaryValue::Bool(false) => 5,
        OrdinaryValue::I64(_) | OrdinaryValue::U64(_) => 1,
        OrdinaryValue::Array(_) | OrdinaryValue::Object(_) => 2,
    }
}
