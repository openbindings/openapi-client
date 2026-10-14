use crate::diagnostics::{Code, DiagnosticContext, DiagnosticReason, LimitKind, SelectionKind};
use crate::exact::Value;
use crate::prepare::{Body, CredentialValue, Input, ParameterInput};
use crate::transport::PreparedRequest;
use crate::{Cancellation, Credential, Diagnostic, Header, Limits, Operation, ParameterLocation};
use serde::Serialize;
use std::{collections::HashSet, fmt, sync::Arc};
struct ParameterDraft {
    location: ParameterLocation,
    name: String,
    value: Value,
    event: usize,
}
struct Variable {
    key: String,
    value: String,
    event: usize,
}
struct CredentialDraft {
    scheme: String,
    value: Credential,
    event: usize,
}
/// An owning checked request builder. Setters admit immediately and latch the first
/// refusal. No later serializer or iterator is invoked after refusal. Caller
/// Serialize/Display work cannot be preempted. Exact values use explicit setters.
///
/// Checked setters require a borrow of the serializable input.
/// ```compile_fail
/// use dynamic_openapi_client::{Document, Limits, ParameterLocation};
/// let doc = Document::parse(b"{}", None, Limits::default()).unwrap();
/// let op = doc.operation_id("x").unwrap();
/// let _ = op.request().parameter(ParameterLocation::Query, "x", 1.5_f64);
/// ```
///
/// ```compile_fail
/// use dynamic_openapi_client::{Document, Limits, exact::Value};
/// let doc = Document::parse("{}", None, Limits::default()).unwrap();
/// let value = Value::parse("null", Limits::default()).unwrap();
/// let _ = doc.operation_id("x").unwrap().request().json(&value);
/// ```
pub struct RequestBuilder {
    operation: Operation,
    parameters: Vec<ParameterDraft>,
    body: Option<(Body, usize)>,
    server: Option<(usize, usize)>,
    media: Option<(String, usize)>,
    security: Option<(usize, usize)>,
    variables: Vec<Variable>,
    credentials: Vec<CredentialDraft>,
    headers: Vec<Header>,
    next_event: usize,
    input_bytes: usize,
    input_nodes: usize,
    owners: HashSet<usize>,
    sources: HashSet<usize>,
    owner_nodes: usize,
    failure: Option<Diagnostic>,
    cancellation: Cancellation,
}
impl fmt::Debug for RequestBuilder {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("RequestBuilder")
            .field("assignments", &self.next_event)
            .field("failure", &self.failure)
            .finish_non_exhaustive()
    }
}
impl Operation {
    /// Begin an owning checked request using this operation's resource limits.
    pub fn request(&self) -> RequestBuilder {
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
            input_nodes: 0,
            owners: HashSet::new(),
            sources: HashSet::new(),
            owner_nodes: 0,
            failure: None,
            cancellation: Cancellation::default(),
        }
    }
}
impl RequestBuilder {
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
            self.owners.clear();
            self.sources.clear();
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
    fn guard(&mut self) -> bool {
        if self.failure.is_some() {
            return false;
        }
        if let Err(error) = self.cancellation.check() {
            self.latch(error);
            return false;
        }
        true
    }
    fn parameter_slot(&mut self, location: ParameterLocation, name: &str) -> Option<usize> {
        if self.failure.is_some() {
            return None;
        }
        let event = self.event();
        if let Some(p) = self
            .parameters
            .iter()
            .find(|p| p.location == location && crate::prepare::same_name(&p.name, name, location))
        {
            let error = self.context(self.duplicate(p.event, event), name, |name| {
                DiagnosticContext::Parameter {
                    index: self.parameters.len(),
                    location,
                    name,
                }
            });
            self.latch(error);
            return None;
        }
        if !self.guard()
            || !self.count(self.parameters.len().saturating_add(1), false)
            || !self.bytes(name.len())
        {
            return None;
        }
        Some(event)
    }
    fn body_slot(&mut self) -> Option<usize> {
        if self.failure.is_some() {
            return None;
        }
        let event = self.event();
        if let Some((_, first)) = self.body {
            self.latch(
                self.duplicate(first, event)
                    .contextual(DiagnosticContext::Body),
            );
            return None;
        }
        self.guard().then_some(event)
    }
    fn encode<T: Serialize + ?Sized>(
        &mut self,
        value: &T,
        body: bool,
        form: bool,
    ) -> Option<Value> {
        let mut limits = self.limits().clone();
        limits.document_bytes = limits.document_bytes.saturating_sub(self.input_bytes);
        if body {
            limits.document_bytes = limits.document_bytes.min(limits.body_bytes);
        }
        limits.nodes = limits
            .nodes
            .saturating_sub(self.input_nodes.saturating_add(self.owner_nodes));
        // Check a minimum nonempty JSON token and node before calling user code.
        if limits.document_bytes == 0 || limits.nodes == 0 {
            let (kind, maximum, actual) = if limits.nodes == 0 {
                (
                    LimitKind::Nodes,
                    self.limits().nodes,
                    self.input_nodes
                        .saturating_add(self.owner_nodes)
                        .saturating_add(1),
                )
            } else if body && self.limits().body_bytes == 0 {
                (LimitKind::BodyBytes, 0, 1)
            } else {
                (
                    LimitKind::InputBytes,
                    self.limits().document_bytes,
                    self.input_bytes.saturating_add(1),
                )
            };
            self.latch(
                Diagnostic::limited(kind, maximum, actual)
                    .contextual(DiagnosticContext::Construction),
            );
            return None;
        }
        match crate::serializable::admit(value, limits, &self.cancellation, form) {
            Ok(value) => {
                self.input_nodes += value.node_count();
                for (id, bytes) in value.retained_sources() {
                    if self.sources.insert(id) {
                        self.input_bytes += bytes;
                    }
                }
                Some(value)
            }
            Err(mut error) => {
                if let Some(DiagnosticReason::Limit {
                    kind: LimitKind::Nodes,
                    actual,
                    ..
                }) = error.reason()
                {
                    error = Diagnostic::limited(
                        LimitKind::Nodes,
                        self.limits().nodes,
                        self.input_nodes
                            .saturating_add(self.owner_nodes)
                            .saturating_add(*actual),
                    )
                    .contextual(DiagnosticContext::Construction);
                }
                if let Some(DiagnosticReason::Limit {
                    kind: LimitKind::DocumentBytes,
                    actual,
                    ..
                }) = error.reason()
                {
                    let actual = *actual;
                    error = if body
                        && self.limits().body_bytes
                            <= self
                                .limits()
                                .document_bytes
                                .saturating_sub(self.input_bytes)
                    {
                        Diagnostic::limited(LimitKind::BodyBytes, self.limits().body_bytes, actual)
                    } else {
                        Diagnostic::limited(
                            LimitKind::InputBytes,
                            self.limits().document_bytes,
                            self.input_bytes.saturating_add(actual),
                        )
                    }
                    .contextual(DiagnosticContext::Construction);
                }
                self.latch(error);
                None
            }
        }
    }
    fn exact(&mut self, value: &Value, body: bool) -> bool {
        if !self.guard() {
            return false;
        }
        if body && value.raw().len() > self.limits().body_bytes {
            self.latch(
                value.locate(
                    Diagnostic::limited(
                        LimitKind::BodyBytes,
                        self.limits().body_bytes,
                        value.raw().len(),
                    )
                    .contextual(DiagnosticContext::Body),
                ),
            );
            return false;
        }
        for (id, bytes) in value.retained_sources() {
            if !self.sources.contains(&id) {
                if !self.bytes(bytes) {
                    return false;
                }
                self.sources.insert(id);
            }
        }
        let id = value.source_context().id;
        if !self.owners.contains(&id) {
            let nodes = self.owner_nodes.saturating_add(value.node_count());
            if nodes.saturating_add(self.input_nodes) > self.limits().nodes {
                self.latch(
                    Diagnostic::limited(
                        LimitKind::Nodes,
                        self.limits().nodes,
                        nodes.saturating_add(self.input_nodes),
                    )
                    .contextual(DiagnosticContext::CallerInput),
                );
                return false;
            }
            self.owner_nodes = nodes;
            self.owners.insert(id);
        }
        true
    }
    /// Immediately check a Serde parameter, after duplicate and cancellation admission.
    pub fn parameter<T: Serialize + ?Sized>(
        mut self,
        location: ParameterLocation,
        name: impl AsRef<str>,
        value: &T,
    ) -> Self {
        if self.failure.is_some() {
            return self;
        }
        let name = name.as_ref();
        if let Some(event) = self.parameter_slot(location, name)
            && let Some(value) = self.encode(value, false, false)
        {
            self.parameters.push(ParameterDraft {
                location,
                name: name.to_owned(),
                value,
                event,
            });
        }
        self
    }
    /// Retain an admitted exact parameter subtree without rewriting numbers.
    pub fn parameter_exact(
        mut self,
        location: ParameterLocation,
        name: impl AsRef<str>,
        value: &Value,
    ) -> Self {
        if self.failure.is_some() {
            return self;
        }
        let name = name.as_ref();
        if let Some(event) = self.parameter_slot(location, name)
            && self.exact(value, false)
        {
            self.parameters.push(ParameterDraft {
                location,
                name: name.to_owned(),
                value: value.clone(),
                event,
            });
        }
        self
    }
    /// Immediately check a Serde JSON body. Media election remains explicit when ambiguous.
    pub fn json<T: Serialize + ?Sized>(mut self, value: &T) -> Self {
        if let Some(event) = self.body_slot()
            && let Some(value) = self.encode(value, true, false)
        {
            self.body = Some((Body::Json(value), event));
        }
        self
    }
    /// Retain an exact JSON body without rewriting its selected bytes.
    pub fn json_exact(mut self, value: &Value) -> Self {
        if let Some(event) = self.body_slot()
            && self.exact(value, true)
        {
            self.body = Some((Body::Json(value.clone()), event));
        }
        self
    }
    /// Immediately check an object/map and use its ordered members as form fields.
    pub fn form<T: Serialize + ?Sized>(mut self, value: &T) -> Self {
        if let Some(event) = self.body_slot()
            && let Some(value) = self.encode(value, false, true)
        {
            if value.kind() != crate::exact::ValueKind::Object {
                self.latch(
                    value
                        .error(Code::UnsupportedValue)
                        .reasoned(DiagnosticReason::Form(crate::form::FormReason::ValueShape))
                        .contextual(DiagnosticContext::Body),
                );
            } else {
                let fields = value
                    .members()
                    .expect("object")
                    .map(|(name, value)| crate::form::FormField::exact(name, value));
                self.admit_fields(fields, event, false);
            }
        }
        self
    }
    /// Admit finite fields, copying borrowed bytes and metadata during this call.
    /// Cancellation is checked before IntoIterator and before each next call.
    pub fn form_fields<'a>(
        mut self,
        fields: impl IntoIterator<Item = crate::form::FormField<'a>>,
    ) -> Self {
        if let Some(event) = self.body_slot() {
            self.admit_fields(fields, event, true);
        }
        self
    }
    fn admit_fields<'a>(
        &mut self,
        fields: impl IntoIterator<Item = crate::form::FormField<'a>>,
        event: usize,
        account: bool,
    ) {
        if !self.guard() {
            return;
        }
        let mut fields = fields.into_iter();
        let mut admitted: Vec<crate::form::ReadyField<'static>> = Vec::new();
        let mut budget = crate::form::Admission::default();
        loop {
            if !self.guard() {
                return;
            }
            let Some(field) = fields.next() else { break };
            let result = (|| {
                if admitted.len() >= self.limits().form_fields {
                    return Err(Diagnostic::limited(
                        LimitKind::FormFields,
                        self.limits().form_fields,
                        admitted.len().saturating_add(1),
                    ));
                }
                budget.name(&field, self.limits())?;
                budget.comparisons(admitted.len(), self.limits())?;
                for previous in &admitted {
                    self.cancellation.check()?;
                    if previous.name == field.name {
                        return Err(Diagnostic::new(Code::InvalidSelection).reasoned(
                            DiagnosticReason::Form(crate::form::FormReason::DuplicateField),
                        ));
                    }
                }
                budget.field_cancel(&field, self.limits(), Some(&self.cancellation))
            })();
            if let Err(error) = result {
                let error = self.context(error, &field.name, |name| DiagnosticContext::FormField {
                    index: admitted.len(),
                    name,
                    item: None,
                });
                self.latch(error);
                return;
            }
            if account {
                if !self.bytes(field.name.len()) {
                    return;
                }
                let cancel = self.cancellation.clone();
                if let Err(error) = field.arguments(&cancel, &mut |value| {
                    if self.failure.is_none() {
                        self.exact(value, false);
                    }
                }) {
                    self.latch(error);
                }
                if self.failure.is_some() {
                    return;
                }
            }
            match field
                .lower(&mut |value| value, &self.cancellation)
                .and_then(|field| field.into_owned(&self.cancellation))
            {
                Ok(field) => {
                    crate::form::reserve_admitted(&mut admitted, self.limits().form_fields);
                    admitted.push(field);
                }
                Err(error) => {
                    self.latch(error);
                    return;
                }
            }
        }
        self.body = Some((Body::Form(admitted), event));
    }
    /// Assign finite raw bytes; preparation checks the elected media declaration.
    pub fn raw_body(mut self, bytes: impl Into<Arc<[u8]>>) -> Self {
        if let Some(event) = self.body_slot() {
            let bytes = bytes.into();
            if self.body_bytes(bytes.len()) {
                self.body = Some((Body::Raw(bytes), event));
            }
        }
        self
    }
    /// Explicitly assign absence, distinct from null or an empty body.
    pub fn no_body(mut self) -> Self {
        if let Some(event) = self.body_slot() {
            self.body = Some((Body::Absent, event));
        }
        self
    }
    /// Elect the authored server index.
    pub fn server(self, index: usize) -> Self {
        self.scalar_selection(index, SelectionKind::Server)
    }
    /// Elect the authored security alternative index.
    pub fn security(self, index: usize) -> Self {
        self.scalar_selection(index, SelectionKind::Security)
    }
    /// Elect a concrete request media type.
    pub fn media(mut self, value: impl AsRef<str>) -> Self {
        if self.failure.is_some() {
            return self;
        }
        let event = self.event();
        if let Some((_, first)) = &self.media {
            self.latch(
                self.duplicate(*first, event)
                    .contextual(DiagnosticContext::Selection {
                        kind: SelectionKind::Media,
                        index: None,
                        key: None,
                    }),
            );
        } else if self.guard() {
            let value = value.as_ref();
            if self.bytes(value.len()) {
                self.media = Some((value.to_owned(), event));
            }
        }
        self
    }
    fn scalar_selection(mut self, value: usize, kind: SelectionKind) -> Self {
        if self.failure.is_some() {
            return self;
        }
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
        } else if self.guard() {
            match kind {
                SelectionKind::Server => self.server = Some((value, event)),
                SelectionKind::Security => self.security = Some((value, event)),
                _ => unreachable!(),
            }
        }
        self
    }
    /// Add one variable assignment; it composes with other populated selection fields.
    pub fn server_variable(mut self, name: impl AsRef<str>, value: impl AsRef<str>) -> Self {
        if self.failure.is_some() {
            return self;
        }
        let event = self.event();
        let name = name.as_ref();
        if let Some(v) = self.variables.iter().find(|v| v.key == name) {
            let error = self.context(self.duplicate(v.event, event), name, |key| {
                DiagnosticContext::Selection {
                    kind: SelectionKind::ServerVariable,
                    index: None,
                    key: Some(key),
                }
            });
            self.latch(error);
            return self;
        }
        if !self.guard()
            || !self.count(self.variables.len().saturating_add(1), false)
            || !self.bytes(name.len())
        {
            return self;
        }
        let value = value.as_ref();
        if !self.bytes(value.len()) {
            return self;
        }
        self.variables.reserve_exact(1);
        self.variables.push(Variable {
            key: name.to_owned(),
            value: value.to_owned(),
            event,
        });
        self
    }
    /// Supply per-call credential material with explicit origin admission. No acquisition occurs.
    pub fn credential(mut self, scheme: impl AsRef<str>, credential: Credential) -> Self {
        if self.failure.is_some() {
            return self;
        }
        let event = self.event();
        let scheme = scheme.as_ref();
        if let Some(c) = self.credentials.iter().find(|c| c.scheme == scheme) {
            let error = self.context(self.duplicate(c.event, event), scheme, |scheme| {
                DiagnosticContext::Credential { scheme }
            });
            self.latch(error);
            return self;
        }
        if !self.guard()
            || !self.count(self.credentials.len().saturating_add(1), false)
            || !self.bytes(scheme.len())
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
            scheme: scheme.to_owned(),
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
        if !self.guard() {
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
    /// Select a cooperative cancellation token. Earlier callbacks cannot be undone.
    pub fn cancellation(mut self, token: Cancellation) -> Self {
        if self.failure.is_none() {
            self.cancellation = token;
        }
        self
    }
    /// Consume the admitted values into a finite prepared request, without serializing again.
    pub fn prepare(self) -> Result<PreparedRequest, Diagnostic> {
        if let Some(error) = self.failure {
            return Err(error.caused());
        }
        self.cancellation.check().map_err(Diagnostic::caused)?;
        let mut input = Input::default();
        for p in self.parameters {
            input.parameters.push(ParameterInput {
                location: p.location,
                name: p.name,
                value: p.value,
            });
        }
        input.body = self.body.map_or(Body::Absent, |v| v.0);
        input.selection.server = self.server.map(|v| v.0);
        input.selection.security = self.security.map(|v| v.0);
        input.selection.media = self.media.map(|v| v.0);
        for v in self.variables {
            input.selection.variables.insert(v.key, v.value);
        }
        for c in self.credentials {
            input.credentials.insert(c.scheme, c.value);
        }
        input.headers = self.headers;
        self.operation
            .prepare_with_cancellation(&input, &self.cancellation)
            .map_err(Diagnostic::caused)
    }
}
