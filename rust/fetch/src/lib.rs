//! Optional finite Fetch transport with explicit host and replay policies.
#![doc = include_str!("../README.md")]
#![forbid(unsafe_code)]
#![warn(missing_docs)]

use dynamic_openapi_client::transport::{PreparedRequest, ReplayPolicy};
use dynamic_openapi_client::{Cancellation, Diagnostic, Outcome, RequestBuilder};
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
            Err(dynamic_openapi_client::diagnostics::HostRefusal::Unavailable.into())
        }
    }
    /// The caller-selected, immutable profile.
    pub fn profile(&self) -> Profile {
        self.profile
    }

    /// Consume a checked builder with an explicit replay election. Preparation and
    /// Fetch use its selected cancellation token. AllowHostManaged changes only replay
    /// admission; every profile, origin, target, header and redirect guard remains.
    pub async fn send(&self, request: RequestBuilder, replay: ReplayPolicy) -> Outcome {
        let (prepared, cancellation) = dynamic_openapi_client::advanced::prepare(request);
        match prepared {
            Ok(prepared) => advanced::execute(self, &prepared, cancellation, replay).await,
            Err(error) => Outcome::not_dispatched(error, cancellation.is_cancelled(), None),
        }
    }
}

/// Deliberate prepared execution with a separate cancellation and replay election.
pub mod advanced {
    use super::*;

    /// Execute with the explicit replay policy while preserving every other guard.
    /// One library Fetch call is not a bound on platform/network attempts. Normal
    /// Fetch responses retain Unknown upload and HostDelivered body interpretation.
    pub async fn execute(
        client: &Client,
        request: &PreparedRequest,
        cancellation: Cancellation,
        replay: ReplayPolicy,
    ) -> Outcome {
        #[cfg(all(target_arch = "wasm32", target_os = "unknown"))]
        {
            client
                .platform
                .execute(client.profile, request, cancellation, replay)
                .await
        }
        #[cfg(not(all(target_arch = "wasm32", target_os = "unknown")))]
        {
            let _ = (client, request, replay);
            Outcome::not_dispatched(
                dynamic_openapi_client::diagnostics::HostRefusal::Unavailable.into(),
                cancellation.is_cancelled(),
                None,
            )
        }
    }
}

#[cfg(all(test, not(all(target_arch = "wasm32", target_os = "unknown"))))]
mod send_tests {
    use super::*;
    use dynamic_openapi_client::diagnostics::Code;
    use dynamic_openapi_client::transport::{DispatchEvidence, UploadState};
    use dynamic_openapi_client::{Document, Limits};
    use std::{
        future::Future,
        task::{Context, Poll, Waker},
    };

    // Non-Wasm execution refuses synchronously. Exercise the ordinary handoff
    // without emulating Fetch or acquiring a host, socket or executor.
    fn ready(future: impl Future<Output = Outcome>) -> Outcome {
        let mut future = Box::pin(future);
        match future
            .as_mut()
            .poll(&mut Context::from_waker(Waker::noop()))
        {
            Poll::Ready(outcome) => outcome,
            Poll::Pending => panic!("native refusal must not enter host work"),
        }
    }

    #[test]
    fn send_preserves_preparation_diagnostics_and_selected_cancellation() {
        let doc = Document::parse(r#"{"openapi":"3.1.0","servers":[{"url":"https://example.test"}],"paths":{"/{id}":{"get":{"operationId":"get","parameters":[{"in":"path","name":"id","required":true,"schema":{"type":"string"}}]}}}}"#, None, Limits::default()).unwrap();
        let op = doc.operation_id("get").unwrap();
        for profile in [Profile::Browser, Profile::Worker] {
            let client = Client { profile };
            for replay in [
                ReplayPolicy::RequireNoReplay,
                ReplayPolicy::AllowHostManaged,
            ] {
                let out = ready(client.send(op.request(), replay));
                assert_eq!(out.error().unwrap().code(), Code::MissingInput);
                assert_eq!(out.dispatch(), DispatchEvidence::NotDispatched);
                assert_eq!(out.upload(), UploadState::NotStarted);
                assert_eq!(out.replay_assurance(), None);
                assert_eq!(
                    out.received_2xx_json_exact().unwrap_err(),
                    *out.error().unwrap()
                );
                let token = Cancellation::default();
                let request = op.request().cancellation(token.clone());
                token.cancel();
                let out = ready(client.send(request, replay));
                assert_eq!(out.error().unwrap().code(), Code::Cancelled);
                assert!(out.cancelled());
                assert_eq!(out.dispatch(), DispatchEvidence::NotDispatched);
                assert_eq!(out.replay_assurance(), None);
            }
        }
    }
}
