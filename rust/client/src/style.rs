//! Private logical atoms shared by parameters and named form encodings.
//! Framing stays distinct from data; no public template/wire escape API.
use crate::{Code, Diagnostic, DiagnosticReason, Value, ValueKind};

#[derive(Clone, Copy, PartialEq, Eq)]
pub(crate) enum Style {
    Simple,
    Form,
    DeepObject,
}
#[derive(Clone, Copy)]
pub(crate) struct Config {
    pub style: Style,
    pub explode: bool,
    pub reserved: bool,
}
#[derive(Clone, Copy)]
pub(crate) enum Name<'a> {
    Plain(&'a str),
    Deep(&'a str, &'a str),
}
impl<'a> Name<'a> {
    pub fn pieces(self) -> [&'a str; 4] {
        match self {
            Self::Plain(s) => [s, "", "", ""],
            Self::Deep(a, b) => [a, "[", b, "]"],
        }
    }
    pub fn len(self) -> usize {
        self.pieces()
            .iter()
            .fold(0usize, |n, s| n.saturating_add(s.len()))
    }
}
pub(crate) enum Atom<'a> {
    Scalar(&'a Value),
    Key(&'a str),
    Separator(&'static [u8]),
}
pub(crate) enum Event<'a> {
    Check,
    Begin(Name<'a>),
    Atom(Atom<'a>),
    End,
}
pub(crate) fn bytes(v: &Value) -> Result<&[u8], Diagnostic> {
    match v.kind() {
        ValueKind::String => Ok(v.as_str().unwrap().as_bytes()),
        ValueKind::Number | ValueKind::Boolean => Ok(v.raw().as_bytes()),
        _ => Err(v
            .error(Code::UnsupportedValue)
            .reasoned(DiagnosticReason::UnsupportedStyleValue)),
    }
}
fn combination(source: &Value) -> Diagnostic {
    source
        .error(Code::UnsupportedParameter)
        .reasoned(DiagnosticReason::UnsupportedStyleCombination)
}
/// Walk only flat logical data. Callbacks admit each atom before storing it.
/// Empty collections can be omitted for forms without changing parameter policy.
pub(crate) fn walk(
    v: &Value,
    name: &str,
    cfg: Config,
    empty: bool,
    source: &Value,
    mut emit: impl FnMut(Event<'_>) -> Result<(), Diagnostic>,
) -> Result<(), Diagnostic> {
    emit(Event::Check)?;
    if cfg.style == Style::DeepObject {
        if v.kind() != ValueKind::Object || !cfg.explode {
            return Err(combination(source));
        }
        for chunk in name.as_bytes().chunks(4096) {
            emit(Event::Check)?;
            if chunk.iter().any(|b| matches!(b, b'[' | b']')) {
                return Err(source
                    .error(Code::UnsupportedParameter)
                    .reasoned(DiagnosticReason::AmbiguousStyleDelimiter));
            }
        }
    }
    let expanded = cfg.style != Style::Simple && cfg.explode;
    if matches!(v.kind(), ValueKind::Array | ValueKind::Object)
        && (v.elements().is_some_and(|v| v.len() == 0) || v.members().is_some_and(|v| v.len() == 0))
    {
        return if empty {
            Ok(())
        } else {
            Err(combination(source))
        };
    }
    if !expanded || !matches!(v.kind(), ValueKind::Array | ValueKind::Object) {
        emit(Event::Begin(Name::Plain(name)))?;
    }
    match v.kind() {
        ValueKind::Array => {
            for (i, value) in v.elements().unwrap().enumerate() {
                bytes(&value)?;
                if expanded {
                    emit(Event::Begin(Name::Plain(name)))?;
                } else if i != 0 {
                    emit(Event::Atom(Atom::Separator(b",")))?;
                }
                emit(Event::Atom(Atom::Scalar(&value)))?;
                if expanded {
                    emit(Event::End)?;
                }
            }
        }
        ValueKind::Object => {
            for (i, (key, value)) in v.members().unwrap().enumerate() {
                bytes(&value)?;
                if cfg.style == Style::DeepObject {
                    for chunk in key.as_bytes().chunks(4096) {
                        emit(Event::Check)?;
                        if chunk.iter().any(|b| matches!(b, b'[' | b']')) {
                            return Err(source
                                .error(Code::UnsupportedParameter)
                                .reasoned(DiagnosticReason::AmbiguousStyleDelimiter));
                        }
                    }
                }
                if expanded {
                    emit(Event::Begin(if cfg.style == Style::DeepObject {
                        Name::Deep(name, key)
                    } else {
                        Name::Plain(key)
                    }))?;
                } else {
                    if i != 0 {
                        emit(Event::Atom(Atom::Separator(b",")))?;
                    }
                    emit(Event::Atom(Atom::Key(key)))?;
                    emit(Event::Atom(Atom::Separator(if cfg.explode {
                        b"="
                    } else {
                        b","
                    })))?;
                }
                emit(Event::Atom(Atom::Scalar(&value)))?;
                if expanded {
                    emit(Event::End)?;
                }
            }
        }
        _ => {
            bytes(v)?;
            emit(Event::Atom(Atom::Scalar(v)))?;
        }
    }
    if !expanded || !matches!(v.kind(), ValueKind::Array | ValueKind::Object) {
        emit(Event::End)?;
    }
    Ok(())
}
#[derive(Clone, Copy)]
pub(crate) enum Escape {
    Literal,
    FormContent,
    Style {
        reserved: bool,
        path: bool,
        cookie: bool,
        comma: bool,
    },
}
impl Escape {
    pub fn name(self) -> Self {
        match self {
            Self::Style { path, cookie, .. } => Self::Style {
                reserved: false,
                path,
                cookie,
                comma: false,
            },
            other => other,
        }
    }
}
/// Bounded stack chunks; triples are recognized on input, never decoded or
/// re-escaped after construction. The sink performs admission and cancellation.
pub(crate) fn encode(
    bytes: &[u8],
    mode: Escape,
    mut put: impl FnMut(&[u8]) -> Result<(), Diagnostic>,
) -> Result<(), Diagnostic> {
    if matches!(mode, Escape::Literal) {
        for chunk in bytes.chunks(4096) {
            put(chunk)?;
        }
        return Ok(());
    }
    const HEX: &[u8] = b"0123456789ABCDEF";
    let mut buf = [0u8; 3072];
    let mut len = 0;
    let mut i = 0;
    while i < bytes.len() {
        if len > buf.len() - 3 {
            put(&buf[..len])?;
            len = 0;
        }
        let b = bytes[i];
        let (plain, space, triples) = match mode {
            Escape::FormContent => (
                b.is_ascii_alphanumeric() || b"*-._".contains(&b),
                b == b' ',
                false,
            ),
            Escape::Style {
                reserved,
                path,
                cookie,
                comma,
            } => {
                let blocked = if path {
                    b"/?#[]".contains(&b)
                } else if cookie {
                    b";,=".contains(&b)
                } else {
                    b"#[]&=+".contains(&b)
                };
                (
                    b.is_ascii_alphanumeric()
                        || b"-._~".contains(&b)
                        || reserved
                            && b":/?#[]@!$&'()*+,;=".contains(&b)
                            && !blocked
                            && !(comma && b == b','),
                    false,
                    reserved,
                )
            }
            Escape::Literal => unreachable!(),
        };
        if triples
            && b == b'%'
            && bytes.get(i + 1).is_some_and(u8::is_ascii_hexdigit)
            && bytes.get(i + 2).is_some_and(u8::is_ascii_hexdigit)
        {
            buf[len..len + 3].copy_from_slice(&bytes[i..i + 3]);
            len += 3;
            i += 3;
        } else if plain || space {
            buf[len] = if space { b'+' } else { b };
            len += 1;
            i += 1;
        } else {
            buf[len] = b'%';
            buf[len + 1] = HEX[(b >> 4) as usize];
            buf[len + 2] = HEX[(b & 15) as usize];
            len += 3;
            i += 1;
        }
    }
    if len != 0 {
        put(&buf[..len])?;
    }
    Ok(())
}
