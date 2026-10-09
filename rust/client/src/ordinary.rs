use crate::{
    Cancellation, Code, Diagnostic, DiagnosticContext, ExactJson, LimitKind, Limits, ParseFailure,
    SourceOrigin,
};
use serde::{
    Serialize, Serializer,
    ser::{SerializeMap, SerializeSeq},
};
use std::{
    io::{self, Write},
    sync::Arc,
};

/// Borrowed, deliberately narrow ordinary Rust input. No floats or arbitrary Serialize.
/// The helper serializes once and parses/indexes once; it is not zero-copy.
/// Construct one object envelope and share child Value handles to amortize this cost.
#[derive(Clone, Copy)]
pub enum OrdinaryValue<'a> {
    /// Explicit null; omit an object member to represent absence.
    Null,
    /// A Boolean.
    Bool(bool),
    /// An exactly serialized signed integer.
    I64(i64),
    /// An exactly serialized unsigned integer, including values above 2^53.
    U64(u64),
    /// Unicode string; escaping is performed by the serializer.
    String(&'a str),
    /// Ordered array. Deeper values remain subject to construction limits.
    Array(&'a [OrdinaryValue<'a>]),
    /// Ordered object. Duplicate decoded names remain a fallible refusal.
    Object(&'a [(&'a str, OrdinaryValue<'a>)]),
}
impl std::fmt::Debug for OrdinaryValue<'_> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("OrdinaryValue([redacted])")
    }
}
// Private serialization wrapper: the supported public boundary is OrdinaryValue,
// not arbitrary user Serialize implementations or serde_json::Value.
struct Encoded<'a>(OrdinaryValue<'a>);
impl Serialize for Encoded<'_> {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        match self.0 {
            OrdinaryValue::Null => s.serialize_unit(),
            OrdinaryValue::Bool(v) => s.serialize_bool(v),
            OrdinaryValue::I64(v) => s.serialize_i64(v),
            OrdinaryValue::U64(v) => s.serialize_u64(v),
            OrdinaryValue::String(v) => s.serialize_str(v),
            OrdinaryValue::Array(v) => {
                let mut a = s.serialize_seq(Some(v.len()))?;
                for x in v {
                    a.serialize_element(&Encoded(*x))?;
                }
                a.end()
            }
            OrdinaryValue::Object(v) => {
                let mut a = s.serialize_map(Some(v.len()))?;
                for (k, x) in v {
                    a.serialize_entry(k, &Encoded(*x))?;
                }
                a.end()
            }
        }
    }
}
struct BoundedBytes {
    bytes: Vec<u8>,
    maximum: usize,
    exceeded: bool,
}
impl Write for BoundedBytes {
    fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
        if bytes.len() > self.maximum.saturating_sub(self.bytes.len()) {
            self.exceeded = true;
            return Err(io::Error::other("construction byte limit"));
        }
        self.bytes.extend_from_slice(bytes);
        Ok(bytes.len())
    }
    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}
impl ExactJson {
    /// Construct exact JSON from admitted ordinary types, with bounded serialization
    /// followed by one parse/index. Ranges refer to generated Constructed bytes.
    /// Pre-serialization failures have no complete source; source is empty.
    /// No schema defaults, generic Serialize conversions or floating-point conversions occur.
    pub fn from_ordinary(value: OrdinaryValue<'_>, limits: Limits) -> Result<Self, ParseFailure> {
        admit(value, &limits, &Cancellation::default())?;
        let fail = construction;
        let mut output = BoundedBytes {
            bytes: Vec::new(),
            maximum: limits.document_bytes,
            exceeded: false,
        };
        if serde_json::to_writer(&mut output, &Encoded(value)).is_err() {
            return Err(fail(if output.exceeded {
                Diagnostic::limited(
                    LimitKind::DocumentBytes,
                    limits.document_bytes,
                    limits.document_bytes.saturating_add(1),
                )
            } else {
                Diagnostic::new(Code::InvalidJson)
            }));
        }
        Self::parse_in_origin(
            &output.bytes,
            limits,
            &Cancellation::default(),
            SourceOrigin::Constructed,
        )
    }
}

pub(crate) fn construction(diagnostic: Diagnostic) -> ParseFailure {
    ParseFailure {
        diagnostic: diagnostic.contextual(DiagnosticContext::Construction),
        source: Arc::from([]),
    }
}
fn admit(
    value: OrdinaryValue<'_>,
    limits: &Limits,
    cancellation: &Cancellation,
) -> Result<(), ParseFailure> {
    let fail = construction;
    // Bounded depth-first iteration: do not enqueue an unadmitted wide collection.
    let mut stack = vec![(value, 0usize)];
    let mut count = 0usize;
    let mut text_bytes = 0usize;
    let mut admit_text = |n: usize| -> Result<(), ParseFailure> {
        text_bytes = text_bytes.saturating_add(n);
        if text_bytes > limits.document_bytes {
            return Err(fail(Diagnostic::limited(
                LimitKind::DocumentBytes,
                limits.document_bytes,
                text_bytes,
            )));
        }
        Ok(())
    };
    while let Some((v, depth)) = stack.pop() {
        cancellation.check().map_err(construction)?;
        count = count.saturating_add(1);
        if count > limits.nodes {
            return Err(fail(Diagnostic::limited(
                LimitKind::Nodes,
                limits.nodes,
                count,
            )));
        }
        let children = match v {
            OrdinaryValue::Array(a) => a.len(),
            OrdinaryValue::Object(o) => o.len(),
            _ => 0,
        };
        if matches!(v, OrdinaryValue::Array(_) | OrdinaryValue::Object(_))
            && depth >= limits.depth.min(128)
        {
            return Err(fail(Diagnostic::limited(
                LimitKind::Depth,
                limits.depth.min(128),
                depth + 1,
            )));
        }
        if children
            > limits
                .nodes
                .saturating_sub(count)
                .saturating_sub(stack.len())
        {
            return Err(fail(Diagnostic::limited(
                LimitKind::Nodes,
                limits.nodes,
                count.saturating_add(stack.len()).saturating_add(children),
            )));
        }
        // String/key lengths are O(1) lower bounds: refuse oversized text
        // before a serializer could scan it looking for escapes.
        match v {
            OrdinaryValue::String(s) => admit_text(s.len())?,
            OrdinaryValue::Object(o) => {
                for (key, _) in o {
                    admit_text(key.len())?;
                }
            }
            _ => (),
        }
        match v {
            OrdinaryValue::Array(a) => stack.extend(a.iter().map(|x| (*x, depth + 1))),
            OrdinaryValue::Object(o) => stack.extend(o.iter().map(|(_, x)| (*x, depth + 1))),
            _ => (),
        }
    }
    Ok(())
}

// One bounded generated array owner for ordinary request arguments. The same
// closed serializer, admission and exact parser serve standalone construction.
pub(crate) fn request_values(
    values: &[OrdinaryValue<'_>],
    body_index: Option<usize>,
    limits: Limits,
    cancellation: &Cancellation,
) -> Result<ExactJson, ParseFailure> {
    admit(OrdinaryValue::Array(values), &limits, cancellation)?;
    let mut output = BoundedBytes {
        bytes: Vec::new(),
        maximum: limits.document_bytes,
        exceeded: false,
    };
    let document_limit = || {
        construction(Diagnostic::limited(
            LimitKind::DocumentBytes,
            limits.document_bytes,
            limits.document_bytes.saturating_add(1),
        ))
    };
    output.write_all(b"[").map_err(|_| document_limit())?;
    for (index, value) in values.iter().enumerate() {
        cancellation.check().map_err(construction)?;
        if index != 0 {
            output.write_all(b",").map_err(|_| document_limit())?;
        }
        let start = output.bytes.len();
        let body_bound = body_index == Some(index)
            && limits.body_bytes < limits.document_bytes.saturating_sub(start);
        if body_bound {
            output.maximum = start.saturating_add(limits.body_bytes);
        }
        if serde_json::to_writer(&mut output, &Encoded(*value)).is_err() {
            return Err(if output.exceeded {
                if body_bound {
                    construction(Diagnostic::limited(
                        LimitKind::BodyBytes,
                        limits.body_bytes,
                        limits.body_bytes.saturating_add(1),
                    ))
                } else {
                    document_limit()
                }
            } else {
                construction(Diagnostic::new(Code::InvalidJson))
            });
        }
        output.maximum = limits.document_bytes;
    }
    output.write_all(b"]").map_err(|_| document_limit())?;
    ExactJson::parse_in_origin(
        &output.bytes,
        limits,
        cancellation,
        SourceOrigin::Constructed,
    )
}
