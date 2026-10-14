//! Dynamic OpenAPI document inspection and finite programmable invocation.
//!
//! No acquisition, executor, HTTP implementation, schema evaluator or application
//! framework is required. See the capability profile in the package README.
#![doc = include_str!("../README.md")]
#![forbid(unsafe_code)]
#![warn(missing_docs)]

mod cancellation;
mod deserialize;
mod document;
mod error;
mod graph;
mod loading;
mod prepare;
mod schema;
mod schema_facts;
mod source;
mod style;
mod transport;
pub mod uri;
mod value;
mod yaml;
pub use source::{SourceFormat, SourcePosition, SourceTrace, YamlReason};

pub use cancellation::Cancellation;
pub use deserialize::DeserializationError;
pub use document::{
    Document, Edition, ExtensionMethod, Method, Operation, OperationDescription, PathItemConflict,
    ReferenceKind, ResolvedReference,
};
pub use error::{
    Code, Diagnostic, DiagnosticContext, DiagnosticReason, HostRefusal, LimitKind, Location,
    NumericReason, NumericTarget, SelectionKind, SerializationReason, VersionReason,
};
pub use graph::{
    DocumentBuilder, GraphLimits, ProtocolSite, ResourceFragment, ResourceHandle, ResourceId,
    ResourceInput, ResourceInterpretation, ResourceLayout, ResourceNeed, ResourceNeedKind,
};
pub use prepare::{
    Body, Credential, CredentialValue, EffectiveOperationDescription, FieldRequirement,
    FieldShapeIssue, FieldShapeReason, Input, OAuthFlowDescription, Parameter, ParameterInput,
    ParameterLocation, Parameters, PreparedRequest, ResponseDescription, SecurityAlternative,
    SecurityRequirement, SecurityScheme, Selection, ServerDescription,
};
pub use transport::{
    BodyState, DispatchEvidence, Header, HeaderDelivery, HeaderFidelity, HostCapabilities, Outcome,
    Provenance, ReplayAssurance, ReplayPolicy, Response, ResponseAdmission, ResponseInput,
    TransportResult, UploadState,
};
pub use value::{
    ExactJson, Limits, Number, ParseFailure, SourceContext, SourceOrigin, Value, ValueKind,
};

/// Unsupported observations for qualification only, absent from ordinary builds.
#[cfg(feature = "qualification")]
#[doc(hidden)]
pub mod qualification {
    pub use crate::value::live_owners;

    /// Number of retained operation compilation entries.
    pub fn cached_operations(document: &crate::Document) -> usize {
        document.0.cached.load(std::sync::atomic::Ordering::Relaxed)
    }
}
mod ordinary;
mod serializable;
pub use ordinary::OrdinaryValue;

mod request;
pub use request::{Argument, RequestBuilder, RequestError};
mod form;
pub use form::{FiniteBytes, Form, FormField, FormReason, FormValue, Part};

/// Explicit evidence-preserving application acceptance policies.
pub mod policies;
pub use policies::{CompleteJsonRefusal, CompletedJson, JsonCallRefusal};

pub use schema::{
    ResolvedSchemaReference, SchemaAccessReason, SchemaHandle, SchemaProfile, SchemaReason,
    SchemaReference, SchemaReferenceKind,
};

#[cfg(test)]
mod schema_tests;

#[cfg(test)]
mod schema_fact_repair_tests;

mod form_description;
pub use form_description::{
    BodyFieldDescription, BodyFields, FieldOpenness, FormRepresentationDescription,
    MediaDescription, RequestBodyDescription, SchemaDescription,
};
