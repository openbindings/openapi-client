//! Exact JSON/OpenAPI 3.1 document inspection and finite programmable invocation.
//!
//! No acquisition, executor, HTTP implementation, schema evaluator or application
//! framework is required. See the capability profile in the package README.
#![doc = include_str!("../README.md")]
#![forbid(unsafe_code)]
#![warn(missing_docs)]

mod document;
mod error;
mod prepare;
mod transport;
pub mod uri;
mod value;

pub use document::{
    Document, Method, Operation, OperationDescription, ReferenceKind, ResolvedReference,
};
pub use error::{
    Code, Diagnostic, DiagnosticContext, DiagnosticReason, LimitKind, Location, NumericReason,
    NumericTarget, SelectionKind,
};
pub use prepare::{
    Body, Credential, CredentialValue, Input, ParameterInput, ParameterLocation, PreparedRequest,
    Selection,
};
pub use transport::{
    BodyState, Cancellation, DeliveredBody, DispatchEvidence, DispatchRequest, Header,
    HostCapabilities, Outcome, Provenance, Response, ResponseInput, TransportResult, UploadState,
};
pub use value::{
    ExactJson, Limits, Number, ParseFailure, SourceContext, SourceOrigin, Value, ValueKind,
    live_json_owners,
};
mod ordinary;
pub use ordinary::OrdinaryValue;
