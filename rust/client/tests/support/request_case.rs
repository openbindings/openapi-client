//! Test fixture records. Every case lowers through the public checked builder.
#![allow(dead_code)]
use dynamic_openapi_client::{
    Cancellation, Credential, Diagnostic, Header, Operation, ParameterLocation, RequestBuilder,
    exact::Value, form::FormField, transport::PreparedRequest,
};
use std::{collections::BTreeMap, sync::Arc};
#[derive(Clone, Default, Debug)]
pub struct CaseSelection {
    pub server: Option<usize>,
    pub variables: BTreeMap<String, String>,
    pub media: Option<String>,
    pub security: Option<usize>,
}
#[derive(Clone, Default, Debug)]
pub enum CaseBody {
    #[default]
    Absent,
    Json(Value),
    Raw(Arc<[u8]>),
    Form(Vec<FormField<'static>>),
}
#[derive(Clone, Debug)]
pub struct CaseParameter {
    pub location: ParameterLocation,
    pub name: String,
    pub value: Value,
}
#[derive(Clone, Default, Debug)]
pub struct CaseInput {
    pub selection: CaseSelection,
    pub parameters: Vec<CaseParameter>,
    pub body: CaseBody,
    pub headers: Vec<Header>,
    pub credentials: BTreeMap<String, Credential>,
}
pub trait PrepareCase {
    fn prepare_case(&self, input: &CaseInput) -> Result<PreparedRequest, Diagnostic>;
    fn prepare_case_with_cancellation(
        &self,
        input: &CaseInput,
        cancellation: &Cancellation,
    ) -> Result<PreparedRequest, Diagnostic>;
}
impl PrepareCase for Operation {
    fn prepare_case(&self, input: &CaseInput) -> Result<PreparedRequest, Diagnostic> {
        self.prepare_case_with_cancellation(input, &Cancellation::default())
    }
    fn prepare_case_with_cancellation(
        &self,
        input: &CaseInput,
        cancel: &Cancellation,
    ) -> Result<PreparedRequest, Diagnostic> {
        let mut request = self
            .request()
            .cancellation(cancel.clone())
            .case_selection(input.selection.clone());
        for p in &input.parameters {
            request = request.parameter_exact(p.location, &p.name, &p.value);
        }
        request = match &input.body {
            CaseBody::Absent => request,
            CaseBody::Json(v) => request.json_exact(v),
            CaseBody::Raw(v) => request.raw_body(v.clone()),
            CaseBody::Form(fields) => request.form_fields(fields.clone()),
        };
        for header in &input.headers {
            request = request.header(header.clone());
        }
        for (scheme, credential) in &input.credentials {
            request = request.credential(scheme, credential.clone());
        }
        request.prepare()
    }
}
pub trait SelectCase {
    fn case_selection(self, selection: CaseSelection) -> Self;
}
impl SelectCase for RequestBuilder {
    fn case_selection(mut self, s: CaseSelection) -> Self {
        if let Some(v) = s.server {
            self = self.server(v);
        }
        if let Some(v) = s.media {
            self = self.media(v);
        }
        if let Some(v) = s.security {
            self = self.security(v);
        }
        for (k, v) in s.variables {
            self = self.server_variable(k, v);
        }
        self
    }
}
// Ordered application model for Serde vectors, including deliberately duplicate keys.
#[derive(Clone, Copy)]
pub enum TestValue<'a> {
    Null,
    Bool(bool),
    I64(i64),
    U64(u64),
    String(&'a str),
    Array(&'a [TestValue<'a>]),
    Object(&'a [(&'a str, TestValue<'a>)]),
}
impl std::fmt::Debug for TestValue<'_> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("TestValue([redacted])")
    }
}
impl serde::Serialize for TestValue<'_> {
    fn serialize<S: serde::Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        use serde::ser::{SerializeMap, SerializeSeq};
        match self {
            Self::Null => s.serialize_unit(),
            Self::Bool(v) => s.serialize_bool(*v),
            Self::I64(v) => s.serialize_i64(*v),
            Self::U64(v) => s.serialize_u64(*v),
            Self::String(v) => s.serialize_str(v),
            Self::Array(v) => {
                let mut seq = s.serialize_seq(Some(v.len()))?;
                for v in *v {
                    seq.serialize_element(v)?;
                }
                seq.end()
            }
            Self::Object(v) => {
                let mut map = s.serialize_map(Some(v.len()))?;
                for (k, v) in *v {
                    map.serialize_entry(k, v)?;
                }
                map.end()
            }
        }
    }
}
