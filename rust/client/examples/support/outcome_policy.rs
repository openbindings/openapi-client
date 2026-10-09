// Example application policy, not a universal SDK success rule.
use dynamic_openapi_client::*;

#[derive(Debug)]
pub enum PolicyRefusal {
    Cancelled,
    NotDispatched,
    Transport,
    MissingResponse,
    HttpStatus,
    UploadUncertain,
    Decode,
}
#[derive(Debug)]
pub struct CompletedCall {
    pub outcome: Outcome,
    pub json: ExactJson,
}
#[derive(Debug)]
pub struct CallFailure {
    pub reason: PolicyRefusal,
    pub outcome: Outcome,
    pub decoded: Option<ExactJson>,
    pub decode_error: Option<Diagnostic>,
}
impl std::fmt::Display for CallFailure {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "application outcome policy refused: {:?}", self.reason)
    }
}
impl std::error::Error for CallFailure {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        self.decode_error
            .as_ref()
            .or(self.outcome.error.as_ref())
            .map(|e| e as _)
    }
}
pub fn accept(outcome: Outcome) -> Result<CompletedCall, Box<CallFailure>> {
    // Inspect/decode available data even when execution also failed. Never discard
    // Outcome: dispatch, upload, cancellation, transport and response are independent.
    let (decoded, decode_error) = match outcome.response.as_ref().map(Response::json) {
        Some(Ok(json)) => (Some(json), None),
        Some(Err(error)) => (None, Some(error)),
        None => (None, None),
    };
    let refusal = if outcome.cancelled {
        Some(PolicyRefusal::Cancelled)
    } else if outcome.dispatch != DispatchEvidence::Dispatched {
        Some(PolicyRefusal::NotDispatched)
    } else if outcome.error.is_some() {
        Some(PolicyRefusal::Transport)
    } else if outcome.response.is_none() {
        Some(PolicyRefusal::MissingResponse)
    } else if !outcome
        .response
        .as_ref()
        .is_some_and(|r| (200..300).contains(&r.status()))
    {
        Some(PolicyRefusal::HttpStatus)
    } else if outcome.upload != UploadState::Complete {
        // This application's explicit policy. Another application may accept
        // an early response while retaining/reporting upload uncertainty.
        Some(PolicyRefusal::UploadUncertain)
    } else if decode_error.is_some() {
        Some(PolicyRefusal::Decode)
    } else {
        None
    };
    if let Some(reason) = refusal {
        return Err(Box::new(CallFailure {
            reason,
            outcome,
            decoded,
            decode_error,
        }));
    }
    Ok(CompletedCall {
        outcome,
        json: decoded.expect("validated response JSON"),
    })
}
