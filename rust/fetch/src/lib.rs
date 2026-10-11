//! Optional finite Fetch transport with explicit host and replay policies.
#![doc = include_str!("../README.md")]
#![forbid(unsafe_code)]
#![warn(missing_docs)]

use dynamic_openapi_client::{Cancellation, Diagnostic, Outcome, PreparedRequest, ReplayPolicy};
use std::fmt;

#[cfg(any(test, all(target_arch = "wasm32", target_os = "unknown")))]
mod admission;
#[cfg(all(target_arch = "wasm32", target_os = "unknown"))]
mod platform;

/// The selected platform's field-byte and request-admission contract.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum Profile {
    /// Browser ByteString field values, CORS and browser forbidden-header rules.
    Browser,
    /// Worker UTF-8 field strings and explicit Cookie support.
    Worker,
}

/// Reusable host bindings and profile. Cloning never clones active requests.
/// Execution futures are local and need not be Send.
#[derive(Clone)]
pub struct Client {
    profile: Profile,
    #[cfg(all(target_arch = "wasm32", target_os = "unknown"))]
    platform: platform::Bindings,
}
impl fmt::Debug for Client {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Client")
            .field("profile", &self.profile)
            .finish_non_exhaustive()
    }
}
impl Client {
    /// Capture required APIs without I/O. Native targets return Unavailable.
    /// Worker execution must occur in an active host request handler.
    pub fn new(profile: Profile) -> Result<Self, Diagnostic> {
        #[cfg(all(target_arch = "wasm32", target_os = "unknown"))]
        {
            Ok(Self {
                profile,
                platform: platform::Bindings::new()?,
            })
        }
        #[cfg(not(all(target_arch = "wasm32", target_os = "unknown")))]
        {
            let _ = profile;
            Err(dynamic_openapi_client::HostRefusal::Unavailable.into())
        }
    }
    /// The caller-selected, immutable profile.
    pub fn profile(&self) -> Profile {
        self.profile
    }
    /// Execute with strict no-replay admission. Ordinary Fetch refuses Replay
    /// before host execution because it cannot make that declaration.
    pub async fn execute(&self, request: &PreparedRequest, cancellation: Cancellation) -> Outcome {
        self.execute_with_replay(request, cancellation, ReplayPolicy::RequireNoReplay)
            .await
    }
    /// Explicitly authorize host-managed replay while preserving every other guard.
    /// One library Fetch call is not a bound on platform/network attempts. Normal
    /// Fetch responses retain Unknown upload and HostDelivered body interpretation.
    pub async fn execute_with_replay(
        &self,
        request: &PreparedRequest,
        cancellation: Cancellation,
        replay: ReplayPolicy,
    ) -> Outcome {
        #[cfg(all(target_arch = "wasm32", target_os = "unknown"))]
        {
            self.platform
                .execute(self.profile, request, cancellation, replay)
                .await
        }
        #[cfg(not(all(target_arch = "wasm32", target_os = "unknown")))]
        {
            let _ = (request, replay);
            Outcome::not_dispatched(
                dynamic_openapi_client::HostRefusal::Unavailable.into(),
                cancellation.is_cancelled(),
                None,
            )
        }
    }
}
