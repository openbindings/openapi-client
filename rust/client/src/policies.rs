//! Explicit optional application acceptance policies. Invocation always returns Outcome.
use crate::{Code, Diagnostic, DispatchEvidence, ExactJson, Outcome, Response, UploadState};

/// Primary refusal under the named JSON response policies; independent facts remain in Outcome.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
#[allow(missing_docs)]
pub enum CompleteJsonRefusal {
    Cancelled,
    NotDispatched,
    Transport,
    MissingResponse,
    HttpStatus,
    UploadUncertain,
    Decode,
}
/// Accepted complete 2xx JSON response, retaining execution evidence and decoded owner.
/// Upload requirements depend on the explicitly selected acceptance policy.
#[derive(Debug)]
pub struct CompletedJson {
    /// Complete, independent execution evidence.
    pub outcome: Outcome,
    /// Decoded once; retain this owner for subsequent reads.
    pub json: ExactJson,
}
/// Refused selected policy, retaining all available execution and decode evidence.
/// Error::source follows the primary reason when represented by a Diagnostic:
/// transport/preflight errors, decode errors, or an actual cancellation diagnostic.
/// Status, upload and missing-response conditions have no diagnostic source;
/// secondary errors remain available through the retained fields.
#[derive(Debug)]
pub struct JsonCallRefusal {
    /// Highest-priority failed condition of this explicitly selected policy.
    pub reason: CompleteJsonRefusal,
    /// Complete, independent execution evidence.
    pub outcome: Outcome,
    /// Successfully decoded response, including when execution or status also failed.
    pub decoded: Option<ExactJson>,
    /// Decode refusal, including when another condition is the primary reason.
    pub decode_error: Option<Diagnostic>,
}
impl std::fmt::Display for JsonCallRefusal {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self.reason {
            CompleteJsonRefusal::Cancelled => f.write_str("the local request was cancelled"),
            CompleteJsonRefusal::NotDispatched => {
                f.write_str("the request was refused before dispatch")
            }
            CompleteJsonRefusal::Transport => {
                f.write_str("the HTTP transport failed; response evidence may still be available")
            }
            CompleteJsonRefusal::MissingResponse => {
                f.write_str("the HTTP transport returned no response")
            }
            CompleteJsonRefusal::HttpStatus => match self.outcome.response.as_ref() {
                Some(response) => write!(
                    f,
                    "HTTP status {} is outside the accepted 2xx range",
                    response.status()
                ),
                None => f.write_str("the response did not satisfy the accepted 2xx status policy"),
            },
            CompleteJsonRefusal::UploadUncertain => {
                f.write_str("upload completion was not established; inspect the retained response")
            }
            CompleteJsonRefusal::Decode => {
                f.write_str("the response could not be decoded as complete JSON")
            }
        }
    }
}
impl std::error::Error for JsonCallRefusal {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        let diagnostic = match self.reason {
            CompleteJsonRefusal::Transport | CompleteJsonRefusal::NotDispatched => {
                self.outcome.error.as_ref()
            }
            CompleteJsonRefusal::Decode => self.decode_error.as_ref(),
            CompleteJsonRefusal::Cancelled => self
                .outcome
                .error
                .as_ref()
                .filter(|error| error.code() == Code::Cancelled),
            CompleteJsonRefusal::MissingResponse
            | CompleteJsonRefusal::HttpStatus
            | CompleteJsonRefusal::UploadUncertain => None,
        };
        diagnostic.map(|error| error as _)
    }
}
/// Select dispatched, uncancelled, transport-error-free, 2xx, complete-upload JSON.
///
/// Decodes available response data at most once, even when another condition fails.
/// Primary reason order is cancellation, non-dispatch, transport, missing response,
/// non-2xx status, upload not known complete, decode. No evidence is discarded.
/// This is optional application policy: another caller may accept an early response
/// with uncertain upload by inspecting Outcome directly. This function does not invoke
/// transport, retry, apply schema rules, or change PreparedRequest::invoke semantics.
pub fn complete_2xx_json(outcome: Outcome) -> Result<CompletedJson, Box<JsonCallRefusal>> {
    accept_json(outcome, true)
}

/// Accept a dispatched, uncancelled, transport-error-free, complete 2xx JSON
/// response without requiring evidence that upload completed.
///
/// This explicitly permits an early response and Unknown/Incomplete upload.
/// Upload evidence remains in the returned outcome; a response never proves
/// upload completion or remote side effects. All other refusal conditions and
/// their priority match complete_2xx_json, and JSON is decoded at most once.
pub fn received_2xx_json(outcome: Outcome) -> Result<CompletedJson, Box<JsonCallRefusal>> {
    accept_json(outcome, false)
}

fn accept_json(
    outcome: Outcome,
    require_upload: bool,
) -> Result<CompletedJson, Box<JsonCallRefusal>> {
    // Inspect/decode available data even when execution also failed. Never discard
    // Outcome: dispatch, upload, cancellation, transport and response are independent.
    let (decoded, decode_error) = match outcome.response.as_ref().map(Response::json) {
        Some(Ok(json)) => (Some(json), None),
        Some(Err(error)) => (None, Some(error)),
        None => (None, None),
    };
    let refusal = if outcome.cancelled {
        Some(CompleteJsonRefusal::Cancelled)
    } else if outcome.dispatch != DispatchEvidence::Dispatched {
        Some(CompleteJsonRefusal::NotDispatched)
    } else if outcome.error.is_some() {
        Some(CompleteJsonRefusal::Transport)
    } else if outcome.response.is_none() {
        Some(CompleteJsonRefusal::MissingResponse)
    } else if !outcome
        .response
        .as_ref()
        .is_some_and(|r| (200..300).contains(&r.status()))
    {
        Some(CompleteJsonRefusal::HttpStatus)
    } else if require_upload && outcome.upload != UploadState::Complete {
        // This application's explicit policy. Another application may accept
        // an early response while retaining/reporting upload uncertainty.
        Some(CompleteJsonRefusal::UploadUncertain)
    } else if decode_error.is_some() {
        Some(CompleteJsonRefusal::Decode)
    } else {
        None
    };
    if let Some(reason) = refusal {
        return Err(Box::new(JsonCallRefusal {
            reason,
            outcome,
            decoded,
            decode_error,
        }));
    }
    Ok(CompletedJson {
        outcome,
        json: decoded.expect("validated response JSON"),
    })
}
