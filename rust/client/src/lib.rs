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
#[doc = include_str!("../GRAPH.md")]
pub mod graph;
mod loading;
mod prepare;
/// Owning schema inspection and reference access.
#[doc = include_str!("../SCHEMAS.md")]
pub mod schema;
mod schema_facts;
/// Original source identities, formats and retained positions.
#[doc = include_str!("../LOADING.md")]
pub mod source;
mod style;
/// Finite prepared requests and supplied-host evidence.
#[doc = include_str!("../HOSTS.md")]
pub mod transport;
pub mod uri;
mod value;
mod yaml;

pub use cancellation::Cancellation;

pub use document::{Document, Operation};

pub use error::Diagnostic;

pub use prepare::{Credential, ParameterLocation};

pub use transport::{Header, Outcome};

pub use value::Limits;

/// Unsupported observations for qualification only, absent from ordinary builds.
#[cfg(feature = "qualification")]
#[doc(hidden)]
pub mod qualification {
    pub use crate::value::live_owners;

    /// Number of retained operation compilation entries.
    pub fn cached_operations(document: &crate::Document) -> usize {
        document.0.cached.load(std::sync::atomic::Ordering::Relaxed)
    }

    /// Accounted immutable graph storage, excluding compiled operation caches.
    pub fn document_retained_bytes(document: &crate::Document) -> usize {
        document
            .0
            .graph
            .resources
            .iter()
            .map(|r| value_retained_bytes(&r.json))
            .sum()
    }

    /// Accounted shared value-owner storage; does not materialize a deferred index.
    pub fn value_retained_bytes(value: &crate::exact::Value) -> usize {
        value.retained_bytes()
    }
}

mod serializable;

mod request;
pub use request::RequestBuilder;
#[doc = include_str!("../FORMS.md")]
pub mod form;

/// Preparation for custom transports using the same public route as the companions.
pub mod advanced {
    /// Consume a checked builder and return its preparation result and selected
    /// cancellation token. Preparation does not serialize caller input again.
    ///
    /// The token is returned even on failure, so a transport can preserve both the
    /// actual diagnostic and independent cancellation evidence in an Outcome.
    /// With no explicit selection it is the builder's fresh uncancelled token.
    /// On success, pass it to PreparedRequest::invoke or invoke_with_replay.
    /// This function performs no I/O and applies no transport credential defaults.
    pub fn prepare(
        request: crate::RequestBuilder,
    ) -> (
        Result<crate::transport::PreparedRequest, crate::Diagnostic>,
        crate::Cancellation,
    ) {
        request.prepare_with_token()
    }
}

#[cfg(test)]
mod schema_tests;

#[cfg(test)]
mod schema_fact_repair_tests;

mod form_description;

#[cfg(test)]
use prepare::Input;

/// Structured diagnostic categories and corrective details.
pub mod diagnostics {
    pub use crate::error::Code;
    pub use crate::error::DiagnosticContext;
    pub use crate::error::DiagnosticReason;
    pub use crate::error::HostRefusal;
    pub use crate::error::LimitKind;
    pub use crate::error::Location;
    pub use crate::error::NumericReason;
    pub use crate::error::NumericTarget;
    pub use crate::error::SelectionKind;
    pub use crate::error::SerializationReason;
    pub use crate::error::VersionReason;
    pub use crate::error::YamlReason;
}

/// Effective OpenAPI inspection descriptors.
/// See [`crate::graph`], [`crate::schema`] and [`crate::form`] for their reference
/// contracts, and [`crate::uri`] for parameter serialization.
pub mod inspect {
    pub use crate::document::Edition;
    pub use crate::document::ExtensionMethod;
    pub use crate::document::Method;
    pub use crate::document::OperationDescription;
    pub use crate::form_description::BodyFieldDescription;
    pub use crate::form_description::BodyFields;
    pub use crate::form_description::FieldOpenness;
    pub use crate::form_description::FormRepresentationDescription;
    pub use crate::form_description::MediaDescription;
    pub use crate::form_description::RequestBodyDescription;
    pub use crate::prepare::EffectiveOperationDescription;
    pub use crate::prepare::FieldRequirement;
    pub use crate::prepare::FieldShapeIssue;
    pub use crate::prepare::FieldShapeReason;
    pub use crate::prepare::OAuthFlowDescription;
    pub use crate::prepare::Parameter;
    pub use crate::prepare::ResponseDescription;
    pub use crate::prepare::SecurityAlternative;
    pub use crate::prepare::SecurityRequirement;
    pub use crate::prepare::SecurityScheme;
    pub use crate::prepare::ServerDescription;
}

/// Owning exact JSON values and checked number views.
#[doc = include_str!("../EXACT.md")]
pub mod exact {
    pub use crate::value::Number;
    pub use crate::value::Value;
    pub use crate::value::ValueKind;
}
