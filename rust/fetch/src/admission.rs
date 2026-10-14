use crate::Profile;
use dynamic_openapi_client::transport::{HostCapabilities, PreparedRequest};
use dynamic_openapi_client::{Cancellation, Header};

const BROWSER: &[&str] = &[
    "accept-charset",
    "accept-encoding",
    "access-control-request-headers",
    "access-control-request-method",
    "connection",
    "content-length",
    "cookie",
    "cookie2",
    "date",
    "dnt",
    "expect",
    "host",
    "keep-alive",
    "origin",
    "referer",
    "set-cookie",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
    "via",
    "user-agent",
];
const WORKER: &[&str] = &[
    "accept-encoding",
    "connection",
    "content-length",
    "expect",
    "host",
    "keep-alive",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
    "via",
];

pub(crate) fn capabilities(profile: Profile, request: &PreparedRequest) -> HostCapabilities {
    HostCapabilities {
        no_retries: false,
        encoded_dot_segments: false,
        forbidden_headers: request
            .headers()
            .iter()
            .filter(|h| forbidden(profile, h))
            .map(|h| h.name().to_owned())
            .collect(),
        ..HostCapabilities::programmable()
    }
}
fn forbidden(profile: Profile, header: &Header) -> bool {
    let name = header.name();
    let list = match profile {
        Profile::Browser => BROWSER,
        Profile::Worker => WORKER,
    };
    if list.iter().any(|n| name.eq_ignore_ascii_case(n)) {
        return true;
    }
    if profile == Profile::Worker {
        return false;
    }
    if ["proxy-", "sec-"].iter().any(|p| {
        name.get(..p.len())
            .is_some_and(|s| s.eq_ignore_ascii_case(p))
    }) {
        return true;
    }
    [
        "x-http-method",
        "x-http-method-override",
        "x-method-override",
    ]
    .iter()
    .any(|n| name.eq_ignore_ascii_case(n))
        && override_forbidden(header.value())
}
fn forbidden_method(value: &[u8]) -> bool {
    [b"CONNECT".as_slice(), b"TRACE", b"TRACK"]
        .iter()
        .any(|m| value.eq_ignore_ascii_case(m))
}
// Fetch get/decode/split: commas inside HTTP quoted strings are not separators,
// quoted spelling is retained, and only HTTP SP/HTAB are trimmed at each edge.
fn override_forbidden(value: &[u8]) -> bool {
    let (mut start, mut quoted, mut escaped) = (0, false, false);
    for (i, &b) in value.iter().enumerate() {
        if quoted {
            if escaped {
                escaped = false;
            } else if b == b'\\' {
                escaped = true;
            } else if b == b'"' {
                quoted = false;
            }
        } else if b == b'"' {
            quoted = true;
        } else if b == b',' {
            if forbidden_method(trim(&value[start..i])) {
                return true;
            }
            start = i + 1;
        }
    }
    forbidden_method(trim(&value[start..]))
}
fn trim(mut bytes: &[u8]) -> &[u8] {
    while bytes.first().is_some_and(|b| matches!(b, b' ' | b'\t')) {
        bytes = &bytes[1..];
    }
    while bytes.last().is_some_and(|b| matches!(b, b' ' | b'\t')) {
        bytes = &bytes[..bytes.len() - 1];
    }
    bytes
}

// No owned copy until exact byte count and scalar validity are admitted. The
// source must be immutable across the two passes (JsString satisfies that).
pub(crate) fn string_bytes(
    profile: Profile,
    units: u32,
    max: usize,
    mut get: impl FnMut(u32) -> u16,
    cancellation: &Cancellation,
) -> Option<Vec<u8>> {
    if units as usize > max || cancellation.is_cancelled() {
        return None;
    }
    let bytes = string_len(profile, units, max, &mut get, cancellation)?;
    let mut output = Vec::with_capacity(bytes);
    walk(profile, units, &mut get, cancellation, |c| {
        if profile == Profile::Browser {
            output.push(c as u8);
        } else {
            let mut scratch = [0; 4];
            output.extend_from_slice(c.encode_utf8(&mut scratch).as_bytes());
        }
        Some(())
    })?;
    Some(output)
}
pub(crate) fn string_len(
    profile: Profile,
    units: u32,
    max: usize,
    mut get: impl FnMut(u32) -> u16,
    cancellation: &Cancellation,
) -> Option<usize> {
    if units as usize > max || cancellation.is_cancelled() {
        return None;
    }
    let mut bytes = 0usize;
    walk(profile, units, &mut get, cancellation, |c| {
        bytes = bytes.checked_add(if profile == Profile::Browser {
            1
        } else {
            c.len_utf8()
        })?;
        (bytes <= max).then_some(())
    })?;
    Some(bytes)
}
fn walk(
    profile: Profile,
    units: u32,
    get: &mut impl FnMut(u32) -> u16,
    cancellation: &Cancellation,
    mut emit: impl FnMut(char) -> Option<()>,
) -> Option<()> {
    let mut i = 0;
    while i < units {
        if i.is_multiple_of(1024) && cancellation.is_cancelled() {
            return None;
        }
        let first = get(i);
        i += 1;
        let c = match profile {
            Profile::Browser => {
                if first > 255 {
                    return None;
                }
                char::from_u32(first as u32)?
            }
            Profile::Worker => {
                let scalar = if (0xd800..=0xdbff).contains(&first) {
                    if i == units {
                        return None;
                    }
                    let second = get(i);
                    i += 1;
                    if !(0xdc00..=0xdfff).contains(&second) {
                        return None;
                    }
                    0x10000 + (((first as u32) - 0xd800) << 10) + ((second as u32) - 0xdc00)
                } else {
                    first as u32
                };
                char::from_u32(scalar)?
            }
        };
        emit(c)?;
    }
    Some(())
}

pub(crate) fn grow<T>(v: &mut Vec<T>, required: usize, limit: usize) {
    debug_assert!(required <= limit);
    if required > v.capacity() {
        let capacity = v.capacity().saturating_mul(2).max(required).min(limit);
        v.reserve_exact(capacity - v.len());
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn declarations_do_not_claim_strict_replay() {
        let doc=dynamic_openapi_client::Document::parse(r#"{"openapi":"3.2.1","servers":[{"url":"https://example.test"}],"paths":{"/":{"get":{"operationId":"read"}}}}"#,None,dynamic_openapi_client::Limits::default()).unwrap();
        let prepared = doc
            .operation_id("read")
            .unwrap()
            .request()
            .header(Header::new("Sec-custom", "x"))
            .prepare()
            .unwrap();
        let browser = capabilities(Profile::Browser, &prepared);
        assert!(!browser.no_retries);
        assert_eq!(browser.forbidden_headers, vec!["Sec-custom"]);
        assert!(
            capabilities(Profile::Worker, &prepared)
                .forbidden_headers
                .is_empty()
        );
    }
    #[test]
    fn conditional_headers_follow_quoted_split_and_profiles() {
        for (value, denied) in [
            ("GET, TRACE", true),
            ("trace", true),
            ("XTRACE", false),
            ("\"TRACE\"", false),
            ("\"a,TRACE\", GET", false),
            ("\"a\\\",x\",TRACK", true),
            ("\"unterminated,TRACE", false),
        ] {
            assert_eq!(
                forbidden(Profile::Browser, &Header::new("X-HTTP-Method", value)),
                denied,
                "{value}"
            );
        }
        assert!(forbidden(Profile::Browser, &Header::new("SeC-secret", "x")));
        assert!(forbidden(Profile::Browser, &Header::new("User-Agent", "x")));
        assert!(!forbidden(Profile::Worker, &Header::new("Cookie", "x")));
        assert!(!forbidden(Profile::Worker, &Header::new("Sec-custom", "x")));
    }
    #[test]
    fn encoding_bounds_preserve_bytes_and_reject_invalid_scalars() {
        let token = Cancellation::default();
        let units = [0xd83d, 0xde00];
        assert_eq!(
            string_bytes(Profile::Worker, 2, 4, |i| units[i as usize], &token),
            Some("😀".as_bytes().to_vec())
        );
        assert_eq!(
            string_bytes(Profile::Worker, 2, 3, |i| units[i as usize], &token),
            None
        );
        assert_eq!(
            string_bytes(Profile::Browser, 2, 4, |i| units[i as usize], &token),
            None
        );
        assert_eq!(
            string_bytes(Profile::Worker, 1, 3, |_| 0xd800, &token),
            None
        );
        assert_eq!(
            string_bytes(Profile::Browser, 2, 2, |i| [0xff, 0x80][i as usize], &token),
            Some(vec![255, 128])
        );
        let mut visited = 0;
        assert_eq!(
            string_bytes(
                Profile::Browser,
                16 * 1024 * 1024,
                64,
                |_| {
                    visited += 1;
                    0
                },
                &token
            ),
            None
        );
        assert_eq!(visited, 0);
    }
    #[test]
    fn finite_growth_is_geometric_and_never_reserves_past_limit() {
        let mut bytes = Vec::new();
        let mut changes = 0;
        let mut previous = 0;
        for n in 1..=65536 {
            grow(&mut bytes, n, 65536);
            bytes.push(0u8);
            if bytes.capacity() != previous {
                changes += 1;
                previous = bytes.capacity();
            }
            assert!(bytes.capacity() <= 65536);
        }
        assert!(changes <= 17);
        let mut empty = Vec::<u8>::new();
        grow(&mut empty, 0, 0);
        assert_eq!(empty.capacity(), 0);
    }
}
