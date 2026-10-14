//! YAML grammar belongs to granit-parser. This adapter applies the JSON-compatible
//! scalar/key policy, bounded graph expansion, and original-source mappings.
use crate::diagnostics::{Code, DiagnosticReason, LimitKind, Location, YamlReason};
use crate::exact::Value;
use crate::source::{AuthoredEntry, PendingMap};
use crate::source::{SourceContext, SourceOrigin};
use crate::{Cancellation, Diagnostic, Limits};
use granit_parser::{Event, Parser, ScalarStyle, Tag};
use std::{
    collections::{HashMap, HashSet},
    ops::Range,
    sync::Arc,
};

#[derive(Debug)]
enum Scalar {
    Null,
    Bool(bool),
    Number(String),
    String(String),
}
enum Kind {
    Scalar(Scalar),
    Sequence(Vec<usize>),
    Mapping(Vec<(String, Range<usize>, usize)>),
    Alias(usize),
}
struct Node {
    span: Range<usize>,
    kind: Kind,
}
struct Frame {
    id: usize,
    key: Option<(String, Range<usize>)>,
    keys: HashMap<String, Range<usize>>,
}

fn at(code: Code, span: &Range<usize>, context: SourceContext) -> Diagnostic {
    Diagnostic::new(code)
        .at(Location {
            start: span.start,
            end: span.end,
            pointer: String::new(),
        })
        .sourced(context)
}
fn refusal(reason: YamlReason, span: &Range<usize>, context: SourceContext) -> Diagnostic {
    at(Code::UnsupportedYaml, span, context).reasoned(DiagnosticReason::Yaml(reason))
}
fn limited(
    kind: LimitKind,
    maximum: usize,
    actual: usize,
    span: &Range<usize>,
    context: SourceContext,
) -> Diagnostic {
    Diagnostic::limited(kind, maximum, actual)
        .at(Location {
            start: span.start,
            end: span.end,
            pointer: String::new(),
        })
        .sourced(context)
}

pub(crate) fn parse(
    source: &str,
    context: SourceContext,
    limits: Limits,
    cancellation: &Cancellation,
) -> Result<Value, Diagnostic> {
    let options = granit_parser::options! {
        emit_comments: false,
        flow_nesting_limit: limits.depth,
        block_nesting_limit: limits.depth,
    };
    let mut nodes: Vec<Node> = vec![];
    let mut stack: Vec<Frame> = vec![];
    let mut anchors = HashMap::new();
    let mut documents = 0;
    let mut root = None;
    for event in Parser::new_from_str_with_options(source, options) {
        cancellation.check().map_err(|e| e.sourced(context))?;
        let (event, span) = event.map_err(|e| {
            let offset = e
                .marker()
                .byte_offset()
                .unwrap_or(source.len())
                .min(source.len());
            let span = offset..offset;
            if matches!(e.kind(), granit_parser::ErrorKind::RecursionLimitExceeded) {
                limited(
                    LimitKind::Depth,
                    limits.depth,
                    limits.depth.saturating_add(1),
                    &span,
                    context,
                )
            } else {
                at(Code::InvalidYaml, &span, context)
            }
        })?;
        let range = span.byte_range().expect("str parser reports byte offsets");
        match event {
            Event::StreamStart | Event::StreamEnd | Event::DocumentEnd | Event::Comment(..) => {
                continue;
            }
            Event::DocumentStart(_, version) => {
                documents += 1;
                if documents > 1 {
                    return Err(refusal(YamlReason::MultipleDocuments, &range, context));
                }
                if version.is_some_and(|v| v.major != 1 || v.minor != 2) {
                    return Err(refusal(YamlReason::UnsupportedVersion, &range, context));
                }
                continue;
            }
            Event::SequenceEnd | Event::MappingEnd => {
                let frame = stack.pop().expect("parser balanced collection events");
                nodes[frame.id].span.end = range.end;
                continue;
            }
            _ => {}
        }
        if nodes.len() >= limits.nodes {
            return Err(limited(
                LimitKind::Nodes,
                limits.nodes,
                nodes.len().saturating_add(1),
                &range,
                context,
            ));
        }
        let (kind, anchor, container) = match event {
            Event::Scalar(value, style, anchor, tag) => {
                let scalar = scalar(&value, style, tag.as_deref(), &limits).map_err(|e| {
                    let range = span
                        .tag_start()
                        .and_then(|m| m.byte_offset())
                        .map_or(range.clone(), |start| start..range.end);
                    match e {
                        ScalarError::Reason(reason) => refusal(reason, &range, context),
                        ScalarError::Length(actual) => limited(
                            LimitKind::NumberCharacters,
                            limits.number_conversion_chars,
                            actual,
                            &range,
                            context,
                        ),
                    }
                })?;
                (Kind::Scalar(scalar), anchor, false)
            }
            Event::SequenceStart(_, anchor, tag) => {
                check_tag(tag.as_deref(), "seq", &range, context)?;
                (Kind::Sequence(vec![]), anchor, true)
            }
            Event::MappingStart(_, anchor, tag) => {
                check_tag(tag.as_deref(), "map", &range, context)?;
                (Kind::Mapping(vec![]), anchor, true)
            }
            Event::Alias(anchor) => (
                Kind::Alias(
                    *anchors
                        .get(&anchor)
                        .ok_or_else(|| refusal(YamlReason::UnknownAlias, &range, context))?,
                ),
                0,
                false,
            ),
            _ => return Err(at(Code::InvalidYaml, &range, context)),
        };
        if container && stack.len() >= limits.depth {
            return Err(limited(
                LimitKind::Depth,
                limits.depth,
                stack.len().saturating_add(1),
                &range,
                context,
            ));
        }
        let id = nodes.len();
        nodes.push(Node {
            span: range.clone(),
            kind,
        });
        if anchor != 0 {
            anchors.insert(anchor, id);
        }
        if let Some(frame) = stack.last_mut() {
            if matches!(nodes[frame.id].kind, Kind::Mapping(_)) && frame.key.is_none() {
                // Aliased string keys retain their semantic identity; composite/non-string
                // keys are refused, including implicit bool/int/null keys.
                let mut key_id = id;
                let mut steps = 0;
                while let Kind::Alias(target) = nodes[key_id].kind {
                    steps += 1;
                    if steps > limits.reference_steps {
                        return Err(limited(
                            LimitKind::AliasSteps,
                            limits.reference_steps,
                            steps,
                            &range,
                            context,
                        ));
                    }
                    key_id = target;
                }
                let Kind::Scalar(Scalar::String(key)) = &nodes[key_id].kind else {
                    return Err(refusal(YamlReason::NonStringKey, &range, context));
                };
                if let Some(previous) = frame.keys.insert(key.clone(), range.clone()) {
                    let mut error = at(Code::DuplicateMember, &range, context);
                    error.related.push(Location {
                        start: previous.start,
                        end: previous.end,
                        pointer: String::new(),
                    });
                    return Err(error);
                }
                frame.key = Some((key.clone(), range.clone()));
            } else {
                match &mut nodes[frame.id].kind {
                    Kind::Sequence(children) => children.push(id),
                    Kind::Mapping(members) => {
                        let (key, range) = frame.key.take().expect("mapping value follows key");
                        members.push((key, range, id));
                    }
                    _ => unreachable!("only collections have frames"),
                }
            }
        } else if root.replace(id).is_some() {
            return Err(at(Code::InvalidYaml, &range, context));
        }
        if container {
            stack.push(Frame {
                id,
                key: None,
                keys: HashMap::new(),
            });
        }
    }
    let root = root.ok_or_else(|| at(Code::InvalidYaml, &(0..source.len()), context))?;
    let (json, mappings) = emit(&nodes, root, context, &limits, cancellation)?;
    // The normalized representation has its own byte/node/depth admission, independently
    // of the original YAML budget. It is never marked as authored JSON.
    let json = Value::parse_in_origin(&json, limits, cancellation, SourceOrigin::Normalized)
        .map_err(|e| {
            // This adapter produces only valid JSON; any remaining admission refusal
            // is attributed to the YAML root rather than exposing generated offsets
            // against Diagnostic::source_bytes().
            let mut d = e;
            d.location = None;
            d.related.clear();
            d.at(Location {
                start: nodes[root].span.start,
                end: nodes[root].span.end,
                pointer: String::new(),
            })
            .sourced(context)
        })?;
    Ok(json.attach_authored(Arc::from(source), context, mappings))
}

fn check_tag(
    tag: Option<&Tag>,
    expected: &str,
    span: &Range<usize>,
    context: SourceContext,
) -> Result<(), Diagnostic> {
    if tag.is_some_and(|tag| !tag.is_yaml_core_schema_tag(expected)) {
        Err(refusal(YamlReason::UnsupportedTag, span, context))
    } else {
        Ok(())
    }
}

enum ScalarError {
    Reason(YamlReason),
    Length(usize),
}
impl From<YamlReason> for ScalarError {
    fn from(reason: YamlReason) -> Self {
        Self::Reason(reason)
    }
}
fn scalar(
    value: &str,
    style: ScalarStyle,
    tag: Option<&Tag>,
    limits: &Limits,
) -> Result<Scalar, ScalarError> {
    let explicit = if let Some(tag) = tag {
        Some(tag.core_suffix().ok_or(YamlReason::UnsupportedTag)?)
    } else {
        None
    };
    if explicit == Some("str") || (explicit.is_none() && style != ScalarStyle::Plain) {
        return Ok(Scalar::String(value.to_owned()));
    }
    if explicit.is_some_and(|tag| !matches!(tag, "null" | "bool" | "int" | "float")) {
        return Err(YamlReason::UnsupportedTag.into());
    }
    if matches!(value, "" | "~" | "null" | "Null" | "NULL") && explicit.is_none_or(|t| t == "null")
    {
        return Ok(Scalar::Null);
    }
    if let Some(b) = match value {
        "true" | "True" | "TRUE" => Some(true),
        "false" | "False" | "FALSE" => Some(false),
        _ => None,
    } && explicit.is_none_or(|t| t == "bool")
    {
        return Ok(Scalar::Bool(b));
    }
    if explicit.is_none_or(|t| t == "float")
        && matches!(
            value,
            ".inf"
                | ".Inf"
                | ".INF"
                | "+.inf"
                | "+.Inf"
                | "+.INF"
                | "-.inf"
                | "-.Inf"
                | "-.INF"
                | ".nan"
                | ".NaN"
                | ".NAN"
        )
    {
        return Err(YamlReason::NonFiniteNumber.into());
    }
    if explicit.is_none_or(|t| t == "int" || t == "float") {
        if let Some((digits, base)) = value
            .strip_prefix("0x")
            .map(|s| (s, 16))
            .or_else(|| value.strip_prefix("0o").map(|s| (s, 8)))
            && explicit != Some("float")
            && !digits.is_empty()
            && digits.bytes().all(|b| (b as char).is_digit(base))
        {
            if value.len() > limits.number_conversion_chars {
                return Err(ScalarError::Length(value.len()));
            }
            return Ok(Scalar::Number(radix_decimal(digits, base)));
        }
        if let Some(number) = decimal(value, explicit == Some("int")) {
            return Ok(Scalar::Number(number));
        }
    }
    if explicit.is_some() {
        Err(YamlReason::InvalidTaggedScalar.into())
    } else {
        Ok(Scalar::String(value.to_owned()))
    }
}

// Lexical Core-schema scalar resolution, never binary floating-point conversion.
// YAML syntax/escapes/structure are handled entirely by the dependency.
fn decimal(value: &str, integer_only: bool) -> Option<String> {
    let (sign, value) = if let Some(s) = value.strip_prefix('-') {
        ("-", s)
    } else {
        ("", value.strip_prefix('+').unwrap_or(value))
    };
    let split = value.find(['e', 'E']);
    let (mantissa, exponent) = match split {
        Some(i) => (&value[..i], Some(&value[i..])),
        None => (value, None),
    };
    if let Some(exponent) = exponent {
        if integer_only {
            return None;
        }
        let digits = exponent[1..]
            .strip_prefix(['+', '-'])
            .unwrap_or(&exponent[1..]);
        if digits.is_empty() || !digits.bytes().all(|b| b.is_ascii_digit()) {
            return None;
        }
    }
    let (integer, fraction) = match mantissa.split_once('.') {
        Some((i, f)) => (i, Some(f)),
        None => (mantissa, None),
    };
    if integer_only && fraction.is_some() {
        return None;
    }
    if !integer.bytes().all(|b| b.is_ascii_digit())
        || fraction.is_some_and(|f| !f.bytes().all(|b| b.is_ascii_digit()))
    {
        return None;
    }
    if integer.is_empty() && fraction.is_none_or(str::is_empty) {
        return None;
    }
    let integer = integer.trim_start_matches('0');
    let integer = if integer.is_empty() { "0" } else { integer };
    let mut result = format!("{sign}{integer}");
    if let Some(fraction) = fraction {
        result.push('.');
        result.push_str(if fraction.is_empty() { "0" } else { fraction });
    }
    if let Some(exponent) = exponent {
        result.push_str(exponent);
    }
    Some(result)
}
fn radix_decimal(digits: &str, base: u32) -> String {
    let mut decimal = vec![0_u32];
    for digit in digits.chars() {
        let mut carry = digit.to_digit(base).expect("validated radix digit");
        for cell in &mut decimal {
            let n = *cell * base + carry;
            *cell = n % 10;
            carry = n / 10;
        }
        while carry != 0 {
            decimal.push(carry % 10);
            carry /= 10;
        }
    }
    decimal
        .into_iter()
        .rev()
        .map(|d| char::from(b'0' + d as u8))
        .collect()
}

enum Task<'a> {
    Visit {
        id: usize,
        depth: usize,
        aliases: Arc<[Range<usize>]>,
    },
    Leave(usize),
    Text(&'a str),
    Key(&'a str),
}
fn emit(
    nodes: &[Node],
    root: usize,
    context: SourceContext,
    limits: &Limits,
    cancellation: &Cancellation,
) -> Result<(Vec<u8>, PendingMap), Diagnostic> {
    let mut output = Vec::new();
    let mut mappings = PendingMap::new();
    let mut tasks = vec![Task::Visit {
        id: root,
        depth: 0,
        aliases: Arc::from([]),
    }];
    let mut active = HashSet::new();
    let mut values = 0_usize;
    while let Some(task) = tasks.pop() {
        cancellation.check().map_err(|e| e.sourced(context))?;
        match task {
            Task::Leave(id) => {
                active.remove(&id);
            }
            Task::Text(text) => append(&mut output, text, limits, &nodes[root].span, context)?,
            Task::Key(key) => append_string(&mut output, key, limits, &nodes[root].span, context)?,
            Task::Visit { id, depth, aliases } => {
                let node = &nodes[id];
                if !active.insert(id) {
                    return Err(refusal(
                        YamlReason::AliasCycle,
                        aliases.last().unwrap_or(&node.span),
                        context,
                    ));
                }
                tasks.push(Task::Leave(id));
                if let Kind::Alias(target) = node.kind {
                    if aliases.len() >= limits.reference_steps {
                        return Err(limited(
                            LimitKind::AliasSteps,
                            limits.reference_steps,
                            aliases.len().saturating_add(1),
                            &node.span,
                            context,
                        ));
                    }
                    let mut chain = aliases.to_vec();
                    chain.push(node.span.clone());
                    tasks.push(Task::Visit {
                        id: target,
                        depth,
                        aliases: chain.into(),
                    });
                    continue;
                }
                values += 1;
                if values > limits.nodes {
                    return Err(limited(
                        LimitKind::Nodes,
                        limits.nodes,
                        values,
                        &node.span,
                        context,
                    ));
                }
                mappings.insert(
                    output.len(),
                    AuthoredEntry {
                        span: node.span.clone(),
                        aliases: aliases.clone(),
                    },
                );
                match &node.kind {
                    Kind::Scalar(scalar) => match scalar {
                        Scalar::Null => append(&mut output, "null", limits, &node.span, context)?,
                        Scalar::Bool(b) => append(
                            &mut output,
                            if *b { "true" } else { "false" },
                            limits,
                            &node.span,
                            context,
                        )?,
                        Scalar::Number(n) => append(&mut output, n, limits, &node.span, context)?,
                        Scalar::String(s) => {
                            append_string(&mut output, s, limits, &node.span, context)?
                        }
                    },
                    Kind::Sequence(children) => {
                        if depth >= limits.depth {
                            return Err(limited(
                                LimitKind::Depth,
                                limits.depth,
                                depth.saturating_add(1),
                                &node.span,
                                context,
                            ));
                        }
                        append(&mut output, "[", limits, &node.span, context)?;
                        tasks.push(Task::Text("]"));
                        for (i, child) in children.iter().enumerate().rev() {
                            tasks.push(Task::Visit {
                                id: *child,
                                depth: depth + 1,
                                aliases: aliases.clone(),
                            });
                            if i > 0 {
                                tasks.push(Task::Text(","));
                            }
                        }
                    }
                    Kind::Mapping(members) => {
                        if depth >= limits.depth {
                            return Err(limited(
                                LimitKind::Depth,
                                limits.depth,
                                depth.saturating_add(1),
                                &node.span,
                                context,
                            ));
                        }
                        append(&mut output, "{", limits, &node.span, context)?;
                        tasks.push(Task::Text("}"));
                        for (i, (key, _, child)) in members.iter().enumerate().rev() {
                            tasks.push(Task::Visit {
                                id: *child,
                                depth: depth + 1,
                                aliases: aliases.clone(),
                            });
                            tasks.push(Task::Text(":"));
                            tasks.push(Task::Key(key));
                            if i > 0 {
                                tasks.push(Task::Text(","));
                            }
                        }
                    }
                    Kind::Alias(_) => unreachable!(),
                }
            }
        }
    }
    Ok((output, mappings))
}
fn append(
    output: &mut Vec<u8>,
    text: &str,
    limits: &Limits,
    span: &Range<usize>,
    context: SourceContext,
) -> Result<(), Diagnostic> {
    let actual = output.len().saturating_add(text.len());
    if actual > limits.document_bytes {
        return Err(limited(
            LimitKind::NormalizedBytes,
            limits.document_bytes,
            actual,
            span,
            context,
        ));
    }
    output.extend_from_slice(text.as_bytes());
    Ok(())
}

fn append_string(
    output: &mut Vec<u8>,
    text: &str,
    limits: &Limits,
    span: &Range<usize>,
    context: SourceContext,
) -> Result<(), Diagnostic> {
    // Use Serde's escaping while enforcing the output cap before every write.
    struct Writer<'a> {
        output: &'a mut Vec<u8>,
        maximum: usize,
        attempted: usize,
    }
    impl std::io::Write for Writer<'_> {
        fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
            self.attempted = self.output.len().saturating_add(bytes.len());
            if self.attempted > self.maximum {
                return Err(std::io::Error::other("normalized YAML byte limit"));
            }
            self.output.extend_from_slice(bytes);
            Ok(bytes.len())
        }
        fn flush(&mut self) -> std::io::Result<()> {
            Ok(())
        }
    }
    let mut writer = Writer {
        output,
        maximum: limits.document_bytes,
        attempted: 0,
    };
    serde_json::to_writer(&mut writer, text).map_err(|_| {
        limited(
            LimitKind::NormalizedBytes,
            limits.document_bytes,
            writer.attempted,
            span,
            context,
        )
    })
}
