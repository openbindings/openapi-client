//! The explicit redirect step: derive the next request from a redirect outcome.
//! Method, content and header rules follow the Fetch standard's HTTP-redirect
//! fetch, which RFC 9110 §15.4 describes as the behavior practice converged on.
use super::{HeaderDelivery, Outcome, PreparedRequest};
use crate::diagnostics::{Code, DiagnosticContext, DiagnosticReason, LimitKind, RedirectRefusal};
use crate::inspect::Method;
use crate::{Diagnostic, Header};

/// Header fields that describe content; they are removed with the content.
const CONTENT_HEADERS: [&str; 4] = [
    "content-type",
    "content-encoding",
    "content-language",
    "content-location",
];

fn refusal(code: Code, reason: RedirectRefusal) -> Diagnostic {
    Diagnostic::new(code).reasoned(DiagnosticReason::Redirect(reason))
}
fn location() -> Diagnostic {
    refusal(Code::InvalidDestination, RedirectRefusal::Location)
        .contextual(DiagnosticContext::PreparedRequest)
}
fn named(field: &Header, name: &str) -> bool {
    field.name().eq_ignore_ascii_case(name)
}
// Credential-bearing fields: lines applied from a supplied Credential and lines
// the caller marked sensitive, every Authorization field, whose value consists of
// credentials (RFC 9110 §11.6.2), and every Cookie field.
fn carries_credentials(field: &Header) -> bool {
    field.is_sensitive() || named(field, "authorization") || named(field, "cookie")
}
fn pair_name(pair: &str) -> &str {
    pair.split_once('=').map_or(pair, |(name, _)| name)
}

impl PreparedRequest {
    /// The request that follows the redirect in `outcome`, which this request produced.
    /// It derives the request and sends nothing; invoking it is a separate, admitted
    /// call the caller chooses to make. The response body is not read.
    ///
    /// `Ok(None)` means there is no redirect to follow and the outcome is final: no
    /// response arrived, its status is not 301, 302, 303, 307 or 308, or it has no
    /// `Location` field.
    ///
    /// 301 and 302 turn POST into GET without content and keep any other method;
    /// 303 turns every method but HEAD into GET without content; 307 and 308 keep
    /// the method and content. Dropping content also removes `Content-Type`,
    /// `Content-Encoding`, `Content-Language` and `Content-Location`. `Location`
    /// resolves against this request's target as an RFC 3986 reference, removing
    /// only literal dot segments, and its fragment is dropped.
    ///
    /// On the same origin (scheme, host and effective port, compared as credential
    /// origins are) the request keeps its credentials, including query credentials,
    /// which are reapplied after the Location's own query. On another origin every
    /// credential the request carries is removed, whether applied in a header, the
    /// query or a cookie, together with every sensitive, `Authorization` and `Cookie`
    /// field. Prepare a new request to send a credential to another origin.
    ///
    /// Refusals: an opaque redirect is [`Code::OpaqueRedirect`]; invalid evidence is
    /// [`Code::InvalidHostEvidence`], with [`RedirectRefusal::HeaderDelivery`] unless
    /// header delivery is `Complete` with no core omission; an unusable `Location` or
    /// an https to http redirect is [`Code::InvalidDestination`] with
    /// [`RedirectRefusal::Location`] or [`RedirectRefusal::Downgrade`]; a chain longer
    /// than `Limits::redirects` is a [`Code::Limit`] of [`LimitKind::Redirects`].
    /// A same-origin Location query naming a query credential with another value is
    /// [`Code::CredentialCollision`], as at preparation.
    pub fn follow_redirect(
        &self,
        outcome: &Outcome,
    ) -> Result<Option<PreparedRequest>, Diagnostic> {
        let Some(response) = outcome.response.as_ref() else {
            return Ok(None);
        };
        let input = &response.input;
        // A browser's manual redirect exposes neither status nor Location.
        if input.opaque_redirect {
            return Err(Diagnostic::new(Code::OpaqueRedirect));
        }
        let status = input.status;
        if !matches!(status, 301 | 302 | 303 | 307 | 308) {
            return Ok(None);
        }
        if let Some(invalid) = &response.validation {
            return Err(invalid.clone());
        }
        // A truncated header list could hide or forge a Location field.
        if input.header_delivery != HeaderDelivery::Complete || response.omitted_header_lines > 0 {
            return Err(refusal(
                Code::InvalidHostEvidence,
                RedirectRefusal::HeaderDelivery,
            ));
        }
        let mut fields = input.headers.iter().filter(|h| named(h, "location"));
        let Some(field) = fields.next() else {
            return Ok(None);
        };
        if fields.next().is_some() {
            return Err(location());
        }
        let reference = std::str::from_utf8(field.value()).map_err(|_| location())?;
        let target =
            crate::uri::redirect_target(reference, &self.target).map_err(|_| location())?;
        if self.redirects >= self.limits.redirects {
            return Err(Diagnostic::limited(
                LimitKind::Redirects,
                self.limits.redirects,
                self.redirects.saturating_add(1),
            ));
        }
        let from = crate::uri::origin(&self.target)?;
        let to = crate::uri::origin(&target)?;
        if from.starts_with("https:") && to.starts_with("http:") {
            return Err(
                refusal(Code::InvalidDestination, RedirectRefusal::Downgrade)
                    .contextual(DiagnosticContext::PreparedRequest),
            );
        }
        let same_origin = from == to;
        let (target, credential_query) = self.credential_target(target, same_origin)?;
        if target.len() > self.limits.target_bytes {
            return Err(Diagnostic::limited(
                LimitKind::TargetBytes,
                self.limits.target_bytes,
                target.len(),
            )
            .contextual(DiagnosticContext::PreparedRequest));
        }
        let without_content = match status {
            301 | 302 => matches!(self.method, Method::Post),
            303 => !matches!(self.method, Method::Head),
            _ => false,
        };
        let headers = self
            .headers
            .iter()
            .filter(|h| {
                !(without_content && CONTENT_HEADERS.iter().any(|name| named(h, name)))
                    && (same_origin || !carries_credentials(h))
            })
            .cloned()
            .collect();
        let (method, body, body_kind) = if without_content {
            (Method::Get, None, "absent")
        } else {
            (self.method.clone(), self.body.clone(), self.body_kind)
        };
        Ok(Some(PreparedRequest {
            operation: self.operation.clone(),
            compiled: self.compiled.clone(),
            method,
            target,
            headers,
            body,
            body_kind,
            limits: self.limits.clone(),
            credential_query,
            redirects: self.redirects + 1,
        }))
    }

    // Remove the query pairs this request applied from credentials wherever the
    // resolved target carries them, then reapply them on the same origin. Other
    // pairs keep their bytes and order. Returns the target and its credential offset.
    fn credential_target(
        &self,
        target: String,
        same_origin: bool,
    ) -> Result<(String, usize), Diagnostic> {
        let credentials = &self.target[self.credential_query..];
        if credentials.is_empty() {
            let end = target.len();
            return Ok((target, end));
        }
        let applied: Vec<&str> = credentials.split('&').collect();
        let (path, query) = match target.split_once('?') {
            Some((path, query)) => (path, Some(query)),
            None => (target.as_str(), None),
        };
        let mut pairs: Vec<&str> = query
            .filter(|query| !query.is_empty())
            .map_or_else(Vec::new, |query| query.split('&').collect());
        let supplied = pairs.len();
        pairs.retain(|pair| !applied.contains(pair));
        if same_origin
            && pairs
                .iter()
                .any(|pair| applied.iter().any(|a| pair_name(pair) == pair_name(a)))
        {
            return Err(Diagnostic::new(Code::CredentialCollision)
                .contextual(DiagnosticContext::PreparedRequest));
        }
        // A query that held only credential pairs disappears with them.
        let keep_query = query.is_some() && !(pairs.is_empty() && supplied > 0);
        let mut output = String::with_capacity(target.len() + credentials.len() + 1);
        output.push_str(path);
        if keep_query || same_origin {
            output.push('?');
            for (index, pair) in pairs.iter().enumerate() {
                if index != 0 {
                    output.push('&');
                }
                output.push_str(pair);
            }
        }
        if !same_origin {
            let end = output.len();
            return Ok((output, end));
        }
        if !pairs.is_empty() {
            output.push('&');
        }
        let start = output.len();
        output.push_str(credentials);
        Ok((output, start))
    }
}
