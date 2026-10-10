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
mod loading;
mod prepare;
mod source;
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
pub use prepare::{
    Body, BodyFieldDescription, BodyFields, Credential, CredentialValue,
    EffectiveOperationDescription, Input, MediaDescription, OAuthFlowDescription, Parameter,
    ParameterInput, ParameterLocation, Parameters, PreparedRequest, RequestBodyDescription,
    ResponseDescription, SchemaDescription, SecurityAlternative, SecurityRequirement,
    SecurityScheme, Selection, ServerDescription,
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

/// Explicit evidence-preserving application acceptance policies.
pub mod policies;
pub use policies::{CompleteJsonRefusal, CompletedJson, JsonCallRefusal};
