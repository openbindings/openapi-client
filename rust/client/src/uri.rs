//! RFC3986 component handling and literal-dot reference resolution.
//! The grammar parser is fluent-uri. Resolution implements only RFC3986 §5.2;
//! percent-encoded dot segments and absent/empty components remain distinct.
use crate::{Code, Diagnostic};
use fluent_uri::UriRef;

fn invalid() -> Diagnostic {
    Diagnostic::new(Code::InvalidDestination)
}
/// Resolve a URI reference against an explicit absolute base, without normalization.
pub fn resolve(reference: &str, base: &str) -> Result<String, Diagnostic> {
    let r = UriRef::parse(reference).map_err(|_| invalid())?;
    let b = UriRef::parse(base).map_err(|_| invalid())?;
    if b.scheme().is_none() {
        return Err(Diagnostic::new(Code::MissingBase));
    }
    let (scheme, authority, path, query) = if r.scheme().is_some() {
        (
            r.scheme().map(|x| x.as_str()),
            r.authority().map(|x| x.as_str()),
            remove_dots(r.path().as_str()),
            r.query().map(|x| x.as_str()),
        )
    } else if r.authority().is_some() {
        (
            b.scheme().map(|x| x.as_str()),
            r.authority().map(|x| x.as_str()),
            remove_dots(r.path().as_str()),
            r.query().map(|x| x.as_str()),
        )
    } else if r.path().is_empty() {
        (
            b.scheme().map(|x| x.as_str()),
            b.authority().map(|x| x.as_str()),
            b.path().as_str().to_owned(),
            r.query().or(b.query()).map(|x| x.as_str()),
        )
    } else {
        let path = if r.path().as_str().starts_with('/') {
            r.path().as_str().to_owned()
        } else {
            let prefix = if b.authority().is_some() && b.path().is_empty() {
                "/"
            } else {
                b.path()
                    .as_str()
                    .rsplit_once('/')
                    .map(|(s, _)| &b.path().as_str()[..s.len() + 1])
                    .unwrap_or("")
            };
            format!("{prefix}{}", r.path())
        };
        (
            b.scheme().map(|x| x.as_str()),
            b.authority().map(|x| x.as_str()),
            remove_dots(&path),
            r.query().map(|x| x.as_str()),
        )
    };
    let mut result = String::new();
    if let Some(s) = scheme {
        result.push_str(s);
        result.push(':');
    }
    if let Some(a) = authority {
        result.push_str("//");
        result.push_str(a);
    }
    result.push_str(&path);
    if let Some(q) = query {
        result.push('?');
        result.push_str(q);
    }
    if let Some(f) = r.fragment() {
        result.push('#');
        result.push_str(f.as_str());
    }
    Ok(result)
}
fn remove_dots(path: &str) -> String {
    let mut input = path;
    let mut out = String::with_capacity(path.len());
    while !input.is_empty() {
        if let Some(s) = input.strip_prefix("../") {
            input = s;
        } else if let Some(s) = input.strip_prefix("./") {
            input = s;
        } else if input.starts_with("/./") {
            input = &input[2..];
        } else if input == "/." {
            input = "/";
        } else if input.starts_with("/../") {
            input = &input[3..];
            out.truncate(out.rfind('/').unwrap_or(0));
        } else if input == "/.." {
            input = "/";
            out.truncate(out.rfind('/').unwrap_or(0));
        } else if input == "." || input == ".." {
            input = "";
        } else {
            let begin = usize::from(input.starts_with('/'));
            let end = input[begin..]
                .find('/')
                .map(|i| i + begin)
                .unwrap_or(input.len());
            out.push_str(&input[..end]);
            input = &input[end..];
        }
    }
    out
}
/// Validate an HTTP(S) destination and return its normalized origin only.
/// The caller retains the original target spelling for actual dispatch.
pub fn origin(target: &str) -> Result<String, Diagnostic> {
    let u = UriRef::parse(target).map_err(|_| invalid())?;
    let scheme = u
        .scheme()
        .ok_or_else(invalid)?
        .as_str()
        .to_ascii_lowercase();
    if !matches!(scheme.as_str(), "http" | "https") || u.fragment().is_some() {
        return Err(invalid());
    }
    let a = u.authority().ok_or_else(invalid)?;
    if a.userinfo().is_some() || a.host().is_empty() {
        return Err(invalid());
    }
    let port = a
        .port_to_u16()
        .map_err(|_| invalid())?
        .unwrap_or(if scheme == "https" { 443 } else { 80 });
    Ok(format!(
        "{scheme}://{}:{port}",
        a.host().to_ascii_lowercase()
    ))
}
pub(crate) fn server_target(server: &str, base: Option<&str>) -> Result<String, Diagnostic> {
    let u = UriRef::parse(server).map_err(|_| invalid())?;
    if u.query().is_some() || u.fragment().is_some() {
        return Err(invalid());
    }
    let resolved = if u.scheme().is_some() {
        resolve(server, server)?
    } else {
        resolve(
            server,
            base.ok_or_else(|| Diagnostic::new(Code::MissingBase))?,
        )?
    };
    let u = UriRef::parse(resolved.as_str()).map_err(|_| invalid())?;
    if u.query().is_some() || u.fragment().is_some() {
        return Err(invalid());
    }
    origin(&resolved)?;
    Ok(resolved)
}
pub(crate) fn percent_decode(value: &str) -> Result<String, Diagnostic> {
    let bytes = value.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' {
            if i + 2 >= bytes.len() {
                return Err(Diagnostic::new(Code::InvalidReference));
            }
            let h = (bytes[i + 1] as char).to_digit(16);
            let l = (bytes[i + 2] as char).to_digit(16);
            match (h, l) {
                (Some(h), Some(l)) => out.push((h * 16 + l) as u8),
                _ => return Err(Diagnostic::new(Code::InvalidReference)),
            }
            i += 3;
        } else {
            out.push(bytes[i]);
            i += 1;
        }
    }
    String::from_utf8(out).map_err(|_| Diagnostic::new(Code::InvalidReference))
}
pub(crate) fn encode(value: &str) -> String {
    const HEX: &[u8] = b"0123456789ABCDEF";
    let mut out = String::new();
    for b in value.bytes() {
        if b.is_ascii_alphanumeric() || b"-._~".contains(&b) {
            out.push(b as char);
        } else {
            out.push('%');
            out.push(HEX[(b >> 4) as usize] as char);
            out.push(HEX[(b & 15) as usize] as char);
        }
    }
    out
}
/// Whether percent-decoding would turn a full path segment into a dot segment.
/// Host adapters that normalize these segments must refuse the target.
pub fn has_encoded_dot_segment(target: &str) -> bool {
    UriRef::parse(target).ok().is_some_and(|u| {
        u.path().as_str().split('/').any(|s| {
            s.contains('%')
                && percent_decode(s)
                    .ok()
                    .is_some_and(|d| d == "." || d == "..")
        })
    })
}
