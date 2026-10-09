//! Explicit optional application acceptance policies. Invocation always returns Outcome.
use crate::{Diagnostic, DispatchEvidence, ExactJson, Outcome, Response, UploadState};

/// Primary refusal under complete_2xx_json; independent facts remain in Outcome.
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
/// Accepted complete 2xx JSON call, retaining both execution evidence and decoded owner.
#[derive(Debug)]
pub struct CompletedJson {
    /// Complete, independent execution evidence.
    pub outcome: Outcome,
    /// Decoded once; retain this owner for subsequent reads.
    pub json: ExactJson,
}
/// Refused selected policy, retaining all available execution and decode evidence.
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
        write!(f, "application outcome policy refused: {:?}", self.reason)
    }
}
impl std::error::Error for JsonCallRefusal {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        self.decode_error
            .as_ref()
            .or(self.outcome.error.as_ref())
            .map(|e| e as _)
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
    } else if outcome.upload != UploadState::Complete {
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
