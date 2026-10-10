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
    Document, Edition, ExtensionMethod, Method, Operation, OperationDescription, ReferenceKind,
    ResolvedReference,
};
pub use error::{
    Code, Diagnostic, DiagnosticContext, DiagnosticReason, LimitKind, Location, NumericReason,
    NumericTarget, SelectionKind, SerializationReason, VersionReason,
};
pub use graph::{
    DocumentBuilder, GraphLimits, ProtocolSite, ResourceFragment, ResourceHandle, ResourceId,
    ResourceInput, ResourceInterpretation, ResourceLayout, ResourceNeed, ResourceNeedKind,
};
pub use prepare::{
    Body, BodyFieldDescription, BodyFields, Credential, CredentialValue,
    EffectiveOperationDescription, FieldRequirement, FieldShapeIssue, FieldShapeReason, Input,
    MediaDescription, OAuthFlowDescription, Parameter, ParameterInput, ParameterLocation,
    Parameters, PreparedRequest, RequestBodyDescription, ResponseDescription, SchemaDescription,
    SecurityAlternative, SecurityRequirement, SecurityScheme, Selection, ServerDescription,
};
pub use transport::{
    BodyState, DeliveredBody, DispatchEvidence, DispatchRequest, Header, HostCapabilities, Outcome,
    Provenance, Response, ResponseInput, TransportResult, UploadState,
};
pub use value::{
    ExactJson, Limits, Number, ParseFailure, SourceContext, SourceOrigin, Value, ValueKind,
    live_json_owners,
};
mod ordinary;
mod serializable;
pub use ordinary::OrdinaryValue;

mod request;
pub use request::{Argument, RequestBuilder, RequestError};
mod form;
pub use form::{FiniteBytes, FormField, FormReason, FormValue, Part};

/// Explicit evidence-preserving application acceptance policies.
pub mod policies;
pub use policies::{CompleteJsonRefusal, CompletedJson, JsonCallRefusal};

pub use schema::{
    DialectCapability, ResolvedSchemaReference, SchemaAccessReason, SchemaHandle, SchemaProfile,
    SchemaReason, SchemaReference, SchemaReferenceKind, SchemaReferences,
};

#[cfg(test)]
mod schema_tests;
